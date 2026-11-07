<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { api } from '../api'
import { useAdminPermissions } from '../permissions'
import TenantDiscoveryDialog from '../components/TenantDiscoveryDialog.vue'

interface DataSource {
  id: string
  name: string
  driver: 'mysql' | 'postgres' | 'sqlite' | 'clickhouse'
}
interface Tenant {
  id: string
  name: string
  code: string
  data_source_id: string
  data_source_name: string
  driver: string
  status: 'active' | 'suspended'
  created_at: string
  updated_at: string
}
interface MigrationBatchAuditEvent {
  id: number
  event: string
  actor: string
  request_source: string
  message: string
  occurred_at: string
}
interface MigrationBatch {
  id: string
  total: number
  queued: number
  running: number
  succeeded: number
  failed: number
  canceled: number
  interrupted: number
  submission_failed: number
  status: string
  actor: string
  request_source: string
  created_at: string
  updated_at: string
  audit_events: MigrationBatchAuditEvent[]
  failures: Array<{ tenant_id: string; code: string; message: string }>
  tasks: Array<{
    id: string
    tenant_id: string
    status: string
    message: string
    completed_steps: number
    total_steps: number
  }>
}
interface TenantAuditSnapshot {
  name: string
  code: string
  data_source_id: string
  status: string
}
interface TenantAuditEvent {
  id: number
  tenant_id: string
  action: 'created' | 'updated' | 'deleted'
  actor: string
  request_source: string
  before?: TenantAuditSnapshot | null
  after?: TenantAuditSnapshot | null
  occurred_at: string
}
interface TenantInput {
  name: string
  code: string
  data_source_id: string
  status: 'active' | 'suspended'
}

interface TenantHealthSettings {
  enabled: boolean
  interval_seconds: number
  timeout_seconds: number
  history_retention_days: number
  updated_at: string
}
interface TenantHealthHistory {
  id: number
  tenant_id: string
  source: 'manual' | 'scheduled'
  status: 'healthy' | 'unhealthy'
  checked_at: string
  duration_ms: number
  message: string
}

interface TenantHealth {
  tenant_id: string
  status: 'healthy' | 'unhealthy'
  checked_at: string
  duration_ms: number
  message: string
}

const { canManageTenants, canTestConnections, canRunMigrations } = useAdminPermissions()

const items = ref<Tenant[]>([])
const dataSources = ref<DataSource[]>([])
const healthByTenant = ref<Record<string, TenantHealth>>({})
const healthSettings = reactive<TenantHealthSettings>({ enabled: false, interval_seconds: 300, timeout_seconds: 5, history_retention_days: 30, updated_at: "" })
const healthSettingsLoading = ref(false)
const healthSettingsSaving = ref(false)
const healthHistory = ref<TenantHealthHistory[]>([])
const healthHistoryLoading = ref(false)
const healthHistoryCursor = ref("")
const healthHistoryNext = ref("")
const healthHistorySource = ref("")
const healthHistoryStatus = ref("")
const healthHistoryTenant = ref("")
const loading = ref(false)
const healthBatching = ref(false)
const saving = ref(false)
const dialogVisible = ref(false)
const discoveryVisible = ref(false)
const editingID = ref('')
const testingID = ref('')
const migratingID = ref('')
const selectedTenantIDs = ref<string[]>([])
const batching = ref(false)
const batchDialogVisible = ref(false)
const auditVisible = ref(false)
const auditLoading = ref(false)
const auditTenantName = ref('')
const auditEvents = ref<TenantAuditEvent[]>([])
const auditTotal = ref(0)
const batch = ref<MigrationBatch | null>(null)
const form = reactive<TenantInput>({
  name: '', code: '', data_source_id: '', status: 'active',
})

async function load() {
  loading.value = true
  try {
    const [tenantResponse, sourceResponse, healthResponse] = await Promise.all([
      api.get<{ items: Tenant[] }>('/tenants'),
      api.get<{ items: DataSource[] }>('/datasources'),
      api.get<{ items: TenantHealth[] }>('/tenant-health'),
    ])
    items.value = tenantResponse.data.items
    dataSources.value = sourceResponse.data.items
    healthByTenant.value = Object.fromEntries(
      healthResponse.data.items.map((item) => [item.tenant_id, item]),
    )
  } catch {
    ElMessage.error('加载租户或数据源信息失败')
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingID.value = ''
  Object.assign(form, {
    name: '', code: '', data_source_id: dataSources.value[0]?.id ?? '', status: 'active',
  })
  dialogVisible.value = true
}

function openEdit(item: Tenant) {
  editingID.value = item.id
  Object.assign(form, {
    name: item.name, code: item.code, data_source_id: item.data_source_id, status: item.status,
  })
  dialogVisible.value = true
}

async function save() {
  if (!canManageTenants.value) return
  if (!form.name.trim() || !form.code.trim() || !form.data_source_id) {
    ElMessage.warning('请填写租户名称、标识并选择数据源')
    return
  }
  saving.value = true
  try {
    if (editingID.value) await api.put('/tenants/' + editingID.value, form)
    else await api.post('/tenants', form)
    ElMessage.success(editingID.value ? '租户配置已更新' : '租户已创建')
    dialogVisible.value = false
    await load()
  } catch (error: unknown) {
    const response = (error as { response?: { data?: { message?: string } } })?.response
    ElMessage.error(response?.data?.message ?? '保存租户失败')
  } finally {
    saving.value = false
  }
}

async function runMigrations(item: Tenant) {
  if (!canRunMigrations.value) return
  try {
    await ElMessageBox.confirm(
      '系统将在租户关联的数据源上执行全部待执行迁移。若多个租户共用该数据源，迁移只会执行一次并共享版本记录。',
      '确认执行迁移：' + item.name,
      { type: 'warning', confirmButtonText: '提交迁移任务', cancelButtonText: '取消' },
    )
    migratingID.value = item.id
    const { data } = await api.post<{ id: string }>('/tenants/' + item.id + '/migrations/tasks', { action: 'up' })
    ElMessage.success('迁移任务已提交：' + data.id)
  } catch (error: unknown) {
    if (error !== 'cancel' && error !== 'close') {
      const response = (error as { response?: { data?: { message?: string } } })?.response
      ElMessage.error(response?.data?.message ?? '提交租户迁移任务失败')
    }
  } finally {
    migratingID.value = ''
  }
}


function isTenantSelectable(row: Tenant) {
  return row.status === 'active'
}

function updateSelection(rows: Tenant[]) {
  selectedTenantIDs.value = rows.filter((row) => row.status === 'active').map((row) => row.id)
}

async function runBatchMigrations() {
  if (!canRunMigrations.value) return
  if (!selectedTenantIDs.value.length) {
    ElMessage.warning('请先选择至少一个运行中的租户')
    return
  }
  try {
    await ElMessageBox.confirm(
      `将为所选 ${selectedTenantIDs.value.length} 个租户提交 Up 迁移任务。共用同一数据源的租户会串行执行，并共享迁移版本记录；某个租户失败不会阻止其他租户。`,
      '确认批量执行迁移',
      { type: 'warning', confirmButtonText: '提交批量任务', cancelButtonText: '取消' },
    )
    batching.value = true
    const { data } = await api.post<MigrationBatch>(
      '/tenant-migration-batches', { tenant_ids: selectedTenantIDs.value },
    )
    batch.value = data
    batchDialogVisible.value = true
    ElMessage.success(`批次 ${data.id}：已提交 ${data.queued}/${data.total} 个任务，提交失败 ${data.failed} 个`)
    selectedTenantIDs.value = []
    await load()
    await refreshBatch()
  } catch (error: unknown) {
    if (error !== 'cancel' && error !== 'close') {
      const response = (error as { response?: { data?: { message?: string } } })?.response
      ElMessage.error(response?.data?.message ?? '提交批量迁移失败')
    }
  } finally {
    batching.value = false
  }
}

async function refreshBatch() {
  if (!batch.value || batch.value.tasks.length === 0) return
  try {
    const { data } = await api.get<MigrationBatch>('/tenant-migration-batches/' + batch.value.id)
    batch.value = data
  } catch {
    ElMessage.error('读取批量迁移状态失败')
  }
}

async function testConnection(item: Tenant) {
  testingID.value = item.id
  try {
    const { data } = await api.post<{ success: boolean; message: string }>('/tenants/' + item.id + '/test')
    if (data.success) ElMessage.success(data.message)
    else ElMessage.warning(data.message)
    await load()
  } catch {
    ElMessage.error('租户连接测试请求失败')
  } finally {
    testingID.value = ''
  }
}

async function runBatchHealthCheck() {
  if (!canTestConnections.value) return
  if (!selectedTenantIDs.value.length) {
    ElMessage.warning('请先选择需要巡检的运行中租户')
    return
  }
  if (selectedTenantIDs.value.length > 100) {
    ElMessage.warning('单次最多巡检 100 个租户，请减少选择数量')
    return
  }
  try {
    await ElMessageBox.confirm(
      `将对所选 ${selectedTenantIDs.value.length} 个租户执行连接巡检，最多 4 个并发，每个连接最多等待 5 秒。巡检失败不会自动停用租户。`,
      '确认批量连接巡检',
      { type: 'warning', confirmButtonText: '开始巡检', cancelButtonText: '取消' },
    )
    healthBatching.value = true
    const { data } = await api.post<{ items: TenantHealth[]; count: number }>(
      '/tenant-health-checks', { tenant_ids: selectedTenantIDs.value },
    )
    healthByTenant.value = {
      ...healthByTenant.value,
      ...Object.fromEntries(data.items.map((item) => [item.tenant_id, item])),
    }
    const healthy = data.items.filter((item) => item.status === 'healthy').length
    ElMessage.success(`巡检完成：${healthy} 个正常，${data.count - healthy} 个异常`)
  } catch (error: unknown) {
    if (error !== 'cancel' && error !== 'close') {
      const response = (error as { response?: { data?: { message?: string } } })?.response
      ElMessage.error(response?.data?.message ?? '批量连接巡检失败')
    }
  } finally {
    healthBatching.value = false
  }
}

async function remove(item: Tenant) {
  if (!canManageTenants.value) return
  try {
    await ElMessageBox.confirm(
      '此操作只删除 meteor-forge 中的租户登记，不会删除关联数据源、目标数据库或其中的数据。',
      '确认删除租户：' + item.name,
      { type: 'warning', confirmButtonText: '删除登记', cancelButtonText: '取消' },
    )
    await api.delete('/tenants/' + item.id)
    ElMessage.success('租户登记已删除')
    await load()
  } catch (error: unknown) {
    if (error !== 'cancel' && error !== 'close') {
      const response = (error as { response?: { data?: { message?: string } } })?.response
      ElMessage.error(response?.data?.message ?? '删除租户失败')
    }
  }
}

async function viewAudit(item: Tenant) {
  auditTenantName.value = item.name
  auditEvents.value = []
  auditTotal.value = 0
  auditVisible.value = true
  auditLoading.value = true
  try {
    const { data } = await api.get<{ items: TenantAuditEvent[]; total: number }>(
      '/tenants/' + item.id + '/audit',
      { params: { limit: 50, offset: 0 } },
    )
    auditEvents.value = data.items
    auditTotal.value = data.total
  } catch (error: unknown) {
    const response = (error as { response?: { data?: { message?: string } } })?.response
    ElMessage.error(response?.data?.message ?? '读取租户审计失败')
  } finally {
    auditLoading.value = false
  }
}

async function loadHealthSettings() {
  healthSettingsLoading.value = true
  try {
    const { data } = await api.get<TenantHealthSettings>('/tenant-health/settings')
    Object.assign(healthSettings, data)
  } catch {
    ElMessage.error('读取定时巡检配置失败')
  } finally {
    healthSettingsLoading.value = false
  }
}

async function saveHealthSettings() {
  if (!canManageTenants.value) return
  healthSettingsSaving.value = true
  try {
    const { data } = await api.put<TenantHealthSettings>('/tenant-health/settings', healthSettings)
    Object.assign(healthSettings, data)
    ElMessage.success(data.enabled ? '定时巡检已启用' : '定时巡检已关闭')
  } catch (error: unknown) {
    const response = (error as { response?: { data?: { message?: string } } })?.response
    ElMessage.error(response?.data?.message ?? '保存定时巡检配置失败')
  } finally {
    healthSettingsSaving.value = false
  }
}

async function loadHealthHistory(next = false) {
  healthHistoryLoading.value = true
  try {
    const { data } = await api.get<{ items: TenantHealthHistory[]; next_cursor?: string }>('/tenant-health/history', {
      params: { limit: 20, source: healthHistorySource.value || undefined, status: healthHistoryStatus.value || undefined, tenant_id: healthHistoryTenant.value || undefined, cursor: next ? healthHistoryNext.value : undefined },
    })
    healthHistory.value = next ? [...healthHistory.value, ...data.items] : data.items
    healthHistoryNext.value = data.next_cursor ?? ''
  } catch {
    ElMessage.error('读取巡检历史失败')
  } finally {
    healthHistoryLoading.value = false
  }
}

function auditActionLabel(action: TenantAuditEvent['action']): string {
  return action === 'created' ? '创建租户' : action === 'updated' ? '修改租户' : '删除租户'
}
function auditSnapshotLabel(snapshot?: TenantAuditSnapshot | null): string {
  if (!snapshot) return '—'
  return `${snapshot.name}（${snapshot.code}） · ${snapshot.data_source_id} · ${snapshot.status}`
}

onMounted(() => { void load(); void loadHealthSettings(); void loadHealthHistory() })
</script>

<template>
  <div class="page-heading">
    <div>
      <div class="eyebrow">TENANT CONTROL PLANE</div>
      <h1>多租户管理</h1>
      <p>维护租户标识与目标数据源映射。每个租户映射到一个已登记的数据源，连接凭据仍由数据源管理统一加密保管。</p>
    </div>
    <div class="heading-actions">
      <el-button v-if="canManageTenants" plain @click="discoveryVisible = true">发现并导入租户</el-button>
      <el-button v-if="canManageTenants" type="primary" :disabled="!dataSources.length" @click="openCreate">添加租户</el-button>
    </div>
  </div>

  <el-alert
    title="租户与数据源的安全边界"
    description="租户只引用已登记的数据源，不单独保存 DSN。被租户引用的数据源不能修改连接配置或删除；请先调整或删除租户映射。删除租户不会删除目标数据库。"
    type="info"
    :closable="false"
    show-icon
    class="tenant-alert"
  />

  <el-card shadow="never" class="tenant-card">
    <div class="table-toolbar">
      <div>
        <strong>租户列表</strong>
        <span>{{ items.length }} 个租户</span>
      </div>
      <div class="toolbar-actions">
        <el-button v-if="canTestConnections" plain :disabled="!selectedTenantIDs.length" :loading="healthBatching" @click="runBatchHealthCheck">批量巡检（{{ selectedTenantIDs.length }}）</el-button>
        <el-button v-if="canRunMigrations" type="success" plain :disabled="!selectedTenantIDs.length" :loading="batching" @click="runBatchMigrations">批量执行 Up（{{ selectedTenantIDs.length }}）</el-button>
        <el-button v-if="batch" text type="primary" @click="batchDialogVisible = true">查看最近批次</el-button>
        <el-button text :loading="loading" @click="load">刷新</el-button>
      </div>
    </div>
    <el-empty v-if="!dataSources.length && !loading" description="请先在数据源管理中添加目标数据库" />
    <el-table v-else :data="items" v-loading="loading" empty-text="尚未创建租户" style="width: 100%" @selection-change="updateSelection">
      <el-table-column type="selection" width="48" :selectable="isTenantSelectable" />
      <el-table-column prop="name" label="租户名称" min-width="170" />
      <el-table-column prop="code" label="租户标识" min-width="150" />
      <el-table-column label="目标数据源" min-width="200">
        <template #default="{ row }">
          <div class="source-cell">
            <strong>{{ row.data_source_name }}</strong>
            <span>{{ row.driver }}</span>
          </div>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="120">
        <template #default="{ row }">
          <el-tag :type="row.status === 'active' ? 'success' : 'info'" effect="plain">
            {{ row.status === 'active' ? '运行中' : '已停用' }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="连接状态" width="130">
        <template #default="{ row }">
          <el-tag
            v-if="healthByTenant[row.id]"
            :type="healthByTenant[row.id].status === 'healthy' ? 'success' : 'danger'"
            effect="plain"
            :title="healthByTenant[row.id].message"
          >{{ healthByTenant[row.id].status === 'healthy' ? '连接正常' : '连接异常' }}</el-tag>
          <el-tag v-else type="info" effect="plain">未巡检</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="最近巡检" min-width="190">
        <template #default="{ row }">
          <span v-if="healthByTenant[row.id]">
            {{ new Date(healthByTenant[row.id].checked_at).toLocaleString() }}
            <small> · {{ healthByTenant[row.id].duration_ms }} ms</small>
          </span>
          <span v-else>—</span>
        </template>
      </el-table-column>
      <el-table-column prop="updated_at" label="最近更新" min-width="190">
        <template #default="{ row }">{{ new Date(row.updated_at).toLocaleString() }}</template>
      </el-table-column>
      <el-table-column label="操作" fixed="right" width="390">
        <template #default="{ row }">
          <el-button link @click="viewAudit(row)">审计</el-button>
          <el-button v-if="canTestConnections" link type="primary" :loading="testingID === row.id" @click="testConnection(row)">测试连接</el-button>
          <el-button v-if="canRunMigrations" link type="success" :loading="migratingID === row.id" :disabled="row.status !== 'active'" @click="runMigrations(row)">执行 Up</el-button>
          <el-button v-if="canManageTenants" link @click="openEdit(row)">编辑</el-button>
          <el-button v-if="canManageTenants" link type="danger" @click="remove(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>
  </el-card>


  <el-card shadow="never" class="tenant-card health-schedule-card">
    <div class="table-toolbar">
      <div>
        <strong>定时连接巡检</strong>
        <span>默认关闭，仅巡检运行中的租户</span>
      </div>
      <el-tag :type="healthSettings.enabled ? 'success' : 'info'">{{ healthSettings.enabled ? '已启用' : '已关闭' }}</el-tag>
    </div>
    <el-form v-loading="healthSettingsLoading" label-position="top" class="health-settings-form">
      <el-form-item label="定时巡检">
        <el-switch v-model="healthSettings.enabled" :disabled="!canManageTenants" active-text="启用" inactive-text="关闭" />
      </el-form-item>
      <el-form-item label="巡检间隔（秒）">
        <el-input-number v-model="healthSettings.interval_seconds" :min="30" :max="86400" :step="30" :disabled="!canManageTenants" />
      </el-form-item>
      <el-form-item label="单租户超时（秒）">
        <el-input-number v-model="healthSettings.timeout_seconds" :min="1" :max="30" :disabled="!canManageTenants" />
      </el-form-item>
      <el-form-item label="历史保留天数">
        <el-input-number v-model="healthSettings.history_retention_days" :min="1" :max="365" :disabled="!canManageTenants" />
      </el-form-item>
      <el-form-item>
        <el-button v-if="canManageTenants" type="primary" :loading="healthSettingsSaving" @click="saveHealthSettings">保存配置</el-button>
        <span class="field-hint">最多 4 个并发；停用租户会自动跳过。修改配置后调度器会重新加载。</span>
      </el-form-item>
    </el-form>
    <el-divider content-position="left">巡检历史</el-divider>
    <div class="table-toolbar">
      <div><strong>历史记录</strong><span>按检查时间倒序</span></div>
      <div class="toolbar-actions">
        <el-select v-model="healthHistoryTenant" clearable filterable placeholder="全部租户" style="width: 180px" @change="loadHealthHistory()">
          <el-option v-for="tenant in items" :key="tenant.id" :label="tenant.name + '（' + tenant.code + '）'" :value="tenant.id" />
        </el-select>
        <el-select v-model="healthHistoryStatus" clearable placeholder="全部结果" style="width: 120px" @change="loadHealthHistory()">
          <el-option label="正常" value="healthy" />
          <el-option label="异常" value="unhealthy" />
        </el-select>
        <el-select v-model="healthHistorySource" clearable placeholder="全部来源" style="width: 130px" @change="loadHealthHistory()">
          <el-option label="手动巡检" value="manual" />
          <el-option label="定时巡检" value="scheduled" />
        </el-select>
        <el-button text :loading="healthHistoryLoading" @click="loadHealthHistory()">刷新</el-button>
      </div>
    </div>
    <el-table :data="healthHistory" v-loading="healthHistoryLoading" size="small" empty-text="暂无巡检历史">
      <el-table-column prop="tenant_id" label="租户 ID" min-width="190" />
      <el-table-column label="来源" width="110">
        <template #default="{ row }">{{ row.source === 'scheduled' ? '定时巡检' : '手动巡检' }}</template>
      </el-table-column>
      <el-table-column label="结果" width="100">
        <template #default="{ row }"><el-tag :type="row.status === 'healthy' ? 'success' : 'danger'">{{ row.status === 'healthy' ? '正常' : '异常' }}</el-tag></template>
      </el-table-column>
      <el-table-column prop="duration_ms" label="耗时（ms）" width="110" />
      <el-table-column label="检查时间" min-width="190">
        <template #default="{ row }">{{ new Date(row.checked_at).toLocaleString() }}</template>
      </el-table-column>
      <el-table-column prop="message" label="结果摘要" min-width="220" show-overflow-tooltip />
    </el-table>
    <div v-if="healthHistoryNext" class="history-more">
      <el-button plain :loading="healthHistoryLoading" @click="loadHealthHistory(true)">加载更多历史</el-button>
    </div>
  </el-card>

  <el-dialog v-model="batchDialogVisible" title="批量迁移进度" width="760px">
    <template v-if="batch">
      <div class="batch-summary">
        <span>批次：{{ batch.id }}</span>
        <span>发起人：{{ batch.actor || '—' }}</span>
        <span>来源：{{ batch.request_source || '—' }}</span>
        <el-tag type="info">{{ batch.status }}</el-tag>
        <el-tag type="info">排队 {{ batch.queued }}</el-tag>
        <el-tag type="primary">执行中 {{ batch.running }}</el-tag>
        <el-tag type="success">成功 {{ batch.succeeded }}</el-tag>
        <el-tag type="danger">失败 {{ batch.failed }}</el-tag>
        <el-tag>取消 {{ batch.canceled }}</el-tag>
        <el-tag type="warning">中断 {{ batch.interrupted }}</el-tag>
      </div>
      <el-table :data="batch.tasks" size="small" max-height="360">
        <el-table-column prop="tenant_id" label="租户 ID" min-width="190" />
        <el-table-column label="状态" width="110">
          <template #default="{ row }">
            <el-tag :type="row.status === 'succeeded' ? 'success' : row.status === 'failed' ? 'danger' : row.status === 'running' ? 'primary' : 'info'">{{ row.status }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="进度" width="100">
          <template #default="{ row }">{{ row.completed_steps }}/{{ row.total_steps }}</template>
        </el-table-column>
        <el-table-column prop="message" label="结果摘要" min-width="220" />
      </el-table>
      <el-alert
        v-if="batch.submission_failed"
        :title="`有 ${batch.submission_failed} 个租户在创建子任务时提交失败`"
        type="warning"
        :closable="false"
        show-icon
        class="batch-alert"
      />
      <el-divider v-if="batch.audit_events?.length" content-position="left">批次审计</el-divider>
      <el-timeline v-if="batch.audit_events?.length" class="batch-audit">
        <el-timeline-item
          v-for="event in batch.audit_events"
          :key="event.id"
          :timestamp="new Date(event.occurred_at).toLocaleString()"
        >
          <strong>{{ event.event === 'submission_started' ? '开始提交' : '提交结果已持久化' }}</strong>
          · {{ event.actor }} · {{ event.message }}
        </el-timeline-item>
      </el-timeline>
      <el-alert
        v-if="batch.failures.length"
        title="部分租户迁移未成功"
        :description="batch.failures.map((item) => item.tenant_id + '：' + item.message).join('；')"
        type="warning"
        :closable="false"
        show-icon
        class="batch-alert"
      />
    </template>
    <template #footer>
      <el-button @click="batchDialogVisible = false">关闭</el-button>
      <el-button type="primary" :loading="batching" @click="refreshBatch">刷新状态</el-button>
    </template>
  </el-dialog>

  <el-dialog v-model="auditVisible" :title="`租户审计 · ${auditTenantName}`" width="760px">
    <div class="audit-total">共 {{ auditTotal }} 条生命周期记录</div>
    <el-timeline v-loading="auditLoading" v-if="auditEvents.length">
      <el-timeline-item
        v-for="event in auditEvents"
        :key="event.id"
        :timestamp="new Date(event.occurred_at).toLocaleString()"
        :type="event.action === 'created' ? 'success' : event.action === 'deleted' ? 'danger' : 'primary'"
      >
        <div class="audit-event-title">{{ auditActionLabel(event.action) }} · {{ event.actor }}</div>
        <div class="audit-event-meta">来源：{{ event.request_source || '—' }}</div>
        <div v-if="event.before" class="audit-snapshot">变更前：{{ auditSnapshotLabel(event.before) }}</div>
        <div v-if="event.after" class="audit-snapshot">变更后：{{ auditSnapshotLabel(event.after) }}</div>
      </el-timeline-item>
    </el-timeline>
    <el-empty v-else-if="!auditLoading" description="该租户暂无审计记录" :image-size="64" />
    <template #footer><el-button @click="auditVisible = false">关闭</el-button></template>
  </el-dialog>

  <el-dialog v-model="dialogVisible" :title="editingID ? '编辑租户' : '添加租户'" width="560px" destroy-on-close>
    <el-form :model="form" label-position="top">
      <el-form-item label="租户名称" required>
        <el-input v-model="form.name" maxlength="128" placeholder="例如：Acme 工作空间" />
      </el-form-item>
      <el-form-item label="租户标识" required>
        <el-input v-model="form.code" maxlength="64" placeholder="例如：acme-prod">
          <template #append>唯一</template>
        </el-input>
        <div class="field-hint">仅允许字母、数字、短横线和下划线；保存时自动转为小写。</div>
      </el-form-item>
      <el-form-item label="目标数据源" required>
        <el-select v-model="form.data_source_id" style="width: 100%" placeholder="请选择数据源">
          <el-option
            v-for="source in dataSources"
            :key="source.id"
            :label="source.name + ' · ' + source.driver"
            :value="source.id"
          />
        </el-select>
      </el-form-item>
      <el-form-item label="租户状态">
        <el-radio-group v-model="form.status">
          <el-radio value="active">运行中</el-radio>
          <el-radio value="suspended">停用</el-radio>
        </el-radio-group>
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="dialogVisible = false">取消</el-button>
      <el-button type="primary" :loading="saving" @click="save">保存</el-button>
    </template>
  </el-dialog>

  <TenantDiscoveryDialog v-model="discoveryVisible" :data-sources="dataSources" @imported="load" />
</template>

<style scoped>
.heading-actions { display: flex; flex-wrap: wrap; gap: 10px; }
.tenant-card { border: 1px solid #e8edf4; border-radius: 12px; }
.tenant-alert { margin-bottom: 20px; }
.health-schedule-card { margin-top: 18px; }
.health-settings-form { display: flex; flex-wrap: wrap; gap: 0 24px; align-items: flex-end; }
.health-settings-form :deep(.el-form-item) { min-width: 170px; }
.history-more { display: flex; justify-content: center; padding-top: 14px; }
.batch-summary { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin-bottom: 16px; font-size: 12px; }
.batch-summary span { margin-right: 8px; color: #667085; }
.batch-alert { margin-top: 14px; }
.audit-total { margin-bottom: 14px; color: #8a95a5; font-size: 11px; }
.audit-event-title { color: #34445d; font-size: 12px; font-weight: 600; }
.audit-event-meta, .audit-snapshot { margin-top: 5px; color: #8a95a5; font-size: 11px; line-height: 1.6; }
.table-toolbar { display: flex; align-items: center; justify-content: space-between; margin-bottom: 18px; }
.table-toolbar > div { display: flex; align-items: center; gap: 10px; }
.toolbar-actions { display: flex; align-items: center; gap: 10px; }
.table-toolbar strong { color: #26354d; font-size: 13px; }
.table-toolbar span, .field-hint { color: #98a2b3; font-size: 11px; }
.source-cell { display: flex; flex-direction: column; gap: 4px; }
.source-cell strong { color: #34445d; font-size: 12px; font-weight: 600; }
.source-cell span { color: #98a2b3; font-size: 11px; }
.field-hint { margin-top: 5px; }
</style>
