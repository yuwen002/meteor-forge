<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { api, getHealth, type HealthResponse } from '../api'

interface DataSource { id: string; name: string; driver: string }
interface Tenant { id: string; status: 'active' | 'suspended' }
interface ShardRule { id: string; base_table: string; data_source_id: string }
interface TaskSummary {
  id: string
  action: string
  status: string
  data_source_id: string
  created_at: string
  message?: string
  source_table?: string
  target_table?: string
}
interface OperationAuditEvent {
  id: number
  task_id: string
  kind: 'migration' | 'shard_ddl'
  action: string
  to_status: string
  actor: string
  message?: string
  data_source_id: string
  source_table?: string
  target_table?: string
  occurred_at: string
}

const health = ref<HealthResponse | null>(null)
const dataSources = ref<DataSource[] | null>(null)
const tenants = ref<Tenant[] | null>(null)
const migrationTasks = ref<TaskSummary[] | null>(null)
const shardTasks = ref<TaskSummary[] | null>(null)
const shardRules = ref<ShardRule[] | null>(null)
const recentEvents = ref<OperationAuditEvent[] | null>(null)
const loading = ref(false)
const partialErrors = ref<string[]>([])
const lastUpdated = ref<Date | null>(null)
let pollHandle: number | undefined

const allTasks = computed(() => [...(migrationTasks.value ?? []), ...(shardTasks.value ?? [])])
const tasksReady = computed(() => migrationTasks.value !== null && shardTasks.value !== null)
const activeTaskCount = computed(() => tasksReady.value
  ? allTasks.value.filter(task => ['queued', 'running'].includes(task.status)).length
  : null)
const failedTaskCount = computed(() => tasksReady.value
  ? allTasks.value.filter(task => ['failed', 'interrupted'].includes(task.status)).length
  : null)
const successfulTaskCount = computed(() => tasksReady.value
  ? allTasks.value.filter(task => task.status === 'succeeded').length
  : null)
const activeTenantCount = computed(() => tenants.value?.filter(tenant => tenant.status === 'active').length ?? null)

async function loadList<T>(path: string, label: string, failures: string[]): Promise<T[] | null> {
  try {
    const { data } = await api.get<{ items: T[] }>(path)
    return data.items
  } catch {
    failures.push(label)
    return null
  }
}

async function refresh() {
  if (loading.value) return
  loading.value = true
  const failures: string[] = []
  const [healthResult, sourcesResult, tenantsResult, migrationResult, shardRulesResult, shardTasksResult, auditResult] = await Promise.all([
    getHealth().catch(() => { failures.push('管理 API 健康状态'); return null }),
    loadList<DataSource>('/datasources', '数据源统计', failures),
    loadList<Tenant>('/tenants', '租户统计', failures),
    loadList<TaskSummary>('/migration-tasks', '迁移任务统计', failures),
    loadList<ShardRule>('/shard-rules', '分片规则统计', failures),
    loadList<TaskSummary>('/shard-ddl-tasks', '分片 DDL 任务统计', failures),
    api.get<{ items: OperationAuditEvent[] }>('/audit-events', { params: { limit: 8, offset: 0 } })
      .then(response => response.data.items)
      .catch(() => { failures.push('最近操作审计'); return null }),
  ])
  health.value = healthResult
  dataSources.value = sourcesResult
  tenants.value = tenantsResult
  migrationTasks.value = migrationResult
  shardRules.value = shardRulesResult
  shardTasks.value = shardTasksResult
  recentEvents.value = auditResult
  partialErrors.value = failures
  lastUpdated.value = new Date()
  loading.value = false
}

function countLabel(value: number | null | undefined): string {
  return value === null || value === undefined ? '—' : value.toLocaleString()
}
function statusLabel(status: string): string {
  const labels: Record<string, string> = {
    queued: '排队中', running: '执行中', succeeded: '成功',
    failed: '失败', canceled: '已取消', interrupted: '中断',
  }
  return labels[status] ?? status
}
function statusType(status: string): 'success' | 'warning' | 'danger' | 'info' | 'primary' {
  if (status === 'succeeded') return 'success'
  if (status === 'failed' || status === 'interrupted') return 'danger'
  if (status === 'running') return 'primary'
  if (status === 'canceled') return 'info'
  return 'warning'
}
function eventKindLabel(kind: OperationAuditEvent['kind']): string {
  return kind === 'migration' ? '数据库迁移' : '分片 DDL'
}
function eventTarget(event: OperationAuditEvent): string {
  if (event.kind === 'shard_ddl') return `${event.source_table ?? '—'} → ${event.target_table ?? '—'}`
  return event.data_source_id
}
function formatTime(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}

onMounted(() => {
  void refresh()
  pollHandle = window.setInterval(() => { void refresh() }, 30_000)
})
onBeforeUnmount(() => {
  if (pollHandle !== undefined) window.clearInterval(pollHandle)
})
</script>

<template>
  <div class="page-heading">
    <div>
      <div class="eyebrow">CONTROL CENTER</div>
      <h1>数据库管理总览</h1>
      <p>查看数据源、租户、迁移任务和分片资源的实时概况。</p>
    </div>
    <div class="heading-actions">
      <span v-if="lastUpdated" class="updated-at">更新于 {{ lastUpdated.toLocaleTimeString() }}</span>
      <el-button :loading="loading" @click="refresh">刷新数据</el-button>
    </div>
  </div>

  <el-alert
    v-if="partialErrors.length"
    title="部分统计暂不可用"
    :description="`以下数据暂时无法读取：${partialErrors.join('、')}。其他已成功读取的数据仍正常展示。`"
    type="warning"
    :closable="false"
    show-icon
    class="dashboard-alert"
  />

  <div class="overview-grid" v-loading="loading && !lastUpdated">
    <el-card shadow="never" class="overview-card">
      <div class="card-label">已登记数据源</div>
      <div class="card-value">{{ countLabel(dataSources?.length) }}</div>
      <div class="card-foot">MySQL / PostgreSQL / SQLite / ClickHouse</div>
      <router-link to="/datasources" class="card-link">数据源管理</router-link>
    </el-card>
    <el-card shadow="never" class="overview-card">
      <div class="card-label">租户总数</div>
      <div class="card-value">{{ countLabel(tenants?.length) }}</div>
      <div class="card-foot">运行中 {{ countLabel(activeTenantCount) }} 个</div>
      <router-link to="/tenants" class="card-link">多租户管理</router-link>
    </el-card>
    <el-card shadow="never" class="overview-card">
      <div class="card-label">活动任务</div>
      <div class="card-value">{{ countLabel(activeTaskCount) }}</div>
      <div class="card-foot">失败 / 中断 {{ countLabel(failedTaskCount) }} 个</div>
      <router-link to="/tasks" class="card-link">执行任务中心</router-link>
    </el-card>
    <el-card shadow="never" class="overview-card">
      <div class="card-label">分片规则</div>
      <div class="card-value">{{ countLabel(shardRules?.length) }}</div>
      <div class="card-foot">分片 DDL 任务 {{ countLabel(shardTasks?.length) }} 个</div>
      <router-link to="/sharding" class="card-link">分库分表</router-link>
    </el-card>
  </div>

  <div class="dashboard-columns">
    <el-card shadow="never" class="panel-card">
      <template #header>
        <div class="panel-heading">
          <div class="panel-title">任务运行概况</div>
          <router-link to="/tasks" class="panel-link">查看全部</router-link>
        </div>
      </template>
      <div class="task-health-grid">
        <div><span>排队 / 执行中</span><strong>{{ countLabel(activeTaskCount) }}</strong></div>
        <div><span>已成功</span><strong class="success-value">{{ countLabel(successfulTaskCount) }}</strong></div>
        <div><span>失败 / 中断</span><strong class="danger-value">{{ countLabel(failedTaskCount) }}</strong></div>
      </div>
      <el-alert
        v-if="failedTaskCount !== null && failedTaskCount > 0"
        :title="`有 ${failedTaskCount} 个任务失败或中断，请检查任务详情和审计记录。`"
        type="warning"
        :closable="false"
        show-icon
        class="task-warning"
      />
      <el-empty v-else-if="tasksReady && activeTaskCount === 0" description="当前没有排队或执行中的任务" :image-size="64" />
      <div v-else-if="!tasksReady" class="empty-state">任务统计暂不可用</div>
    </el-card>

    <el-card shadow="never" class="panel-card service-card">
      <template #header><div class="panel-title">管理服务状态</div></template>
      <div class="service-row">
        <div class="service-icon">API</div>
        <div class="service-info">
          <strong>{{ health?.service ?? 'meteor-forge-admin' }}</strong>
          <span v-if="health">API {{ health.api }} · {{ formatTime(health.timestamp) }}</span>
          <span v-else>健康检查暂不可用</span>
        </div>
        <el-tag :type="health?.status === 'ok' ? 'success' : 'danger'" effect="plain">
          {{ health?.status === 'ok' ? '运行正常' : '状态未知' }}
        </el-tag>
      </div>
      <div class="service-foot">管理 API 默认仅监听本机地址，数据源凭据由服务端加密保存。</div>
    </el-card>
  </div>

  <el-card shadow="never" class="audit-card">
    <template #header>
      <div class="panel-heading">
        <div class="panel-title">最近操作审计</div>
        <router-link to="/audit" class="panel-link">查看完整审计</router-link>
      </div>
    </template>
    <el-table :data="recentEvents ?? []" v-loading="loading" empty-text="暂无操作审计记录">
      <el-table-column label="类型" width="125">
        <template #default="{ row }">{{ eventKindLabel(row.kind) }}</template>
      </el-table-column>
      <el-table-column label="动作" width="130">
        <template #default="{ row }">{{ row.action }}</template>
      </el-table-column>
      <el-table-column label="目标" min-width="190" show-overflow-tooltip>
        <template #default="{ row }">{{ eventTarget(row) }}</template>
      </el-table-column>
      <el-table-column label="状态" width="110">
        <template #default="{ row }"><el-tag :type="statusType(row.to_status)" effect="plain">{{ statusLabel(row.to_status) }}</el-tag></template>
      </el-table-column>
      <el-table-column prop="actor" label="操作者" width="130" />
      <el-table-column label="时间" min-width="180">
        <template #default="{ row }">{{ formatTime(row.occurred_at) }}</template>
      </el-table-column>
    </el-table>
  </el-card>
</template>

<style scoped>
.heading-actions { display: flex; align-items: center; gap: 12px; }
.updated-at { color: #8a95a5; font-size: 11px; }
.dashboard-alert { margin: 16px 0; }
.overview-grid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 14px; margin: 18px 0; }
.overview-card { position: relative; min-height: 156px; border: 1px solid #e8edf4; border-radius: 12px; }
.card-label { color: #78859a; font-size: 12px; }
.card-value { margin-top: 13px; color: #26354d; font-size: 30px; font-weight: 650; line-height: 1; }
.card-foot { margin-top: 9px; color: #8a95a5; font-size: 11px; }
.card-link, .panel-link { color: #3977bd; font-size: 12px; text-decoration: none; }
.card-link { display: inline-block; margin-top: 13px; }
.card-link:hover, .panel-link:hover { text-decoration: underline; }
.dashboard-columns { display: grid; grid-template-columns: minmax(0, 1.2fr) minmax(320px, .8fr); gap: 16px; margin-bottom: 16px; }
.panel-card, .audit-card { border: 1px solid #e8edf4; border-radius: 12px; }
.panel-heading { display: flex; justify-content: space-between; align-items: center; gap: 12px; }
.panel-title { color: #26354d; font-size: 14px; font-weight: 650; }
.task-health-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; }
.task-health-grid > div { display: flex; flex-direction: column; gap: 9px; padding: 15px; border-radius: 9px; background: #f7f9fc; }
.task-health-grid span { color: #78859a; font-size: 11px; }
.task-health-grid strong { color: #26354d; font-size: 24px; }
.task-health-grid .success-value { color: #26956b; }
.task-health-grid .danger-value { color: #d65b5b; }
.task-warning { margin-top: 14px; }
.service-row { display: flex; align-items: center; gap: 12px; }
.service-icon { display: grid; place-items: center; width: 38px; height: 38px; border-radius: 9px; background: #edf4fb; color: #3977bd; font-size: 11px; font-weight: 700; }
.service-info { display: flex; flex: 1; min-width: 0; flex-direction: column; gap: 5px; }
.service-info strong { color: #26354d; font-size: 13px; }
.service-info span, .service-foot, .empty-state { color: #8a95a5; font-size: 11px; }
.service-foot { margin-top: 17px; line-height: 1.6; }
.empty-state { padding: 22px 0; text-align: center; }
.audit-card { margin-bottom: 16px; }
@media (max-width: 980px) {
  .overview-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .dashboard-columns { grid-template-columns: 1fr; }
}
@media (max-width: 600px) {
  .overview-grid { grid-template-columns: 1fr; }
  .heading-actions { align-items: flex-end; flex-direction: column; }
  .task-health-grid { grid-template-columns: 1fr; }
}
</style>
