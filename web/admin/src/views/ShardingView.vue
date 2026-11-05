<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { api } from '../api'

interface DataSource {
  id: string
  name: string
  driver: string
}
interface ShardRule {
  id: string
  data_source_id: string
  data_source_name: string
  driver: string
  base_table: string
  shard_type: string
  hash_shards: number
  range_size: number
  created_at: string
  updated_at: string
}
interface RoutePreview {
  shard_rule_id: string
  data_source_id: string
  base_table: string
  shard_type: string
  tables: string[]
  table_count: number
}
interface ClonePreview {
  driver: string
  source_table: string
  target_table: string
  sql: string
  copies_data: boolean
  warning: string
}
interface ShardDDLTask {
  id: string
  source_table: string
  target_table: string
  status: string
  message: string
}
interface ShardDDLAuditEvent {
  from_status: string
  to_status: string
  message: string
  occurred_at: string
}

const loading = ref(false)
const saving = ref(false)
const previewing = ref(false)
const rules = ref<ShardRule[]>([])
const dataSources = ref<DataSource[]>([])
const dialogVisible = ref(false)
const previewVisible = ref(false)
const tableDialogVisible = ref(false)
const tableLoading = ref(false)
const tableChecking = ref(false)
const physicalTables = ref<string[]>([])
const tableRule = ref<ShardRule | null>(null)
const tableCheckName = ref("")
const tableCheckResult = ref<boolean | null>(null)
const cloneSource = ref('')
const cloneTarget = ref('')
const clonePreview = ref<ClonePreview | null>(null)
const clonePreviewing = ref(false)
const cloneSubmitting = ref(false)
const cloneTask = ref<ShardDDLTask | null>(null)
const cloneAudit = ref<ShardDDLAuditEvent[]>([])
const cloneTaskRefreshing = ref(false)
const editingID = ref('')
const previewRule = ref<ShardRule | null>(null)
const previewResult = ref<RoutePreview | null>(null)
const isTimeRule = computed(() => ['year', 'quarter', 'month', 'week', 'ten_day'].includes(previewRule.value?.shard_type ?? ''))
const form = reactive({
  data_source_id: '',
  base_table: '',
  shard_type: 'month',
  hash_shards: 16,
  range_size: 1000000,
})
const routeInput = reactive({
  timestamp: '',
  id: '',
  start_time: '',
  end_time: '',
  start_id: '',
  end_id: '',
})

const shardTypes = [
  { label: '按年', value: 'year' },
  { label: '按季度', value: 'quarter' },
  { label: '按月', value: 'month' },
  { label: '按 ISO 周', value: 'week' },
  { label: '按旬', value: 'ten_day' },
  { label: '按 ID 哈希', value: 'id_hash' },
  { label: '按 ID 范围', value: 'id_range' },
]
const shardTypeLabel = (value: string) => shardTypes.find((item) => item.value === value)?.label ?? value

async function load() {
  loading.value = true
  try {
    const [ruleResponse, sourceResponse] = await Promise.all([
      api.get<{ items: ShardRule[] }>('/shard-rules'),
      api.get<{ items: DataSource[] }>('/datasources'),
    ])
    rules.value = ruleResponse.data.items
    dataSources.value = sourceResponse.data.items
  } catch {
    ElMessage.error('加载分片规则或数据源失败')
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingID.value = ''
  Object.assign(form, {
    data_source_id: dataSources.value[0]?.id ?? '',
    base_table: '',
    shard_type: 'month',
    hash_shards: 16,
    range_size: 1000000,
  })
  dialogVisible.value = true
}

function openEdit(rule: ShardRule) {
  editingID.value = rule.id
  Object.assign(form, {
    data_source_id: rule.data_source_id,
    base_table: rule.base_table,
    shard_type: rule.shard_type,
    hash_shards: rule.hash_shards,
    range_size: rule.range_size,
  })
  dialogVisible.value = true
}

async function save() {
  if (!form.data_source_id || !form.base_table.trim()) {
    ElMessage.warning('请选择数据源并填写基础表名')
    return
  }
  saving.value = true
  try {
    const payload = { ...form, base_table: form.base_table.trim() }
    if (editingID.value) await api.put('/shard-rules/' + editingID.value, payload)
    else await api.post('/shard-rules', payload)
    ElMessage.success(editingID.value ? '分片规则已更新' : '分片规则已创建')
    dialogVisible.value = false
    await load()
  } catch (error: unknown) {
    const response = (error as { response?: { data?: { message?: string } } })?.response
    ElMessage.error(response?.data?.message ?? '保存分片规则失败')
  } finally {
    saving.value = false
  }
}

async function openTables(rule: ShardRule) {
  tableRule.value = rule
  physicalTables.value = []
  tableCheckName.value = ''
  tableCheckResult.value = null
  cloneSource.value = rule.base_table
  cloneTarget.value = ''
  clonePreview.value = null
  cloneTask.value = null
  cloneAudit.value = []
  tableDialogVisible.value = true
  tableLoading.value = true
  try {
    const { data } = await api.get<{ items: string[] }>(`/shard-rules/${rule.id}/tables`)
    physicalTables.value = data.items
  } catch (error: unknown) {
    const response = (error as { response?: { data?: { message?: string } } })?.response
    ElMessage.error(response?.data?.message ?? '读取物理分片表失败')
  } finally {
    tableLoading.value = false
  }
}

async function previewClone() {
  if (!tableRule.value || !cloneSource.value.trim() || !cloneTarget.value.trim()) {
    ElMessage.warning('请填写模板物理表和目标分片表名')
    return
  }
  clonePreviewing.value = true
  clonePreview.value = null
  try {
    const { data } = await api.post<ClonePreview>(`/shard-rules/${tableRule.value.id}/clone-preview`, {
      source_table: cloneSource.value.trim(),
      target_table: cloneTarget.value.trim(),
    })
    clonePreview.value = data
  } catch (error: unknown) {
    const response = (error as { response?: { data?: { message?: string } } })?.response
    ElMessage.error(response?.data?.message ?? '生成克隆预览失败')
  } finally {
    clonePreviewing.value = false
  }
}

async function submitClone() {
  if (!tableRule.value || !clonePreview.value) return
  try {
    await ElMessageBox.confirm(
      `将仅复制 ${clonePreview.value.source_table} 的表结构到 ${clonePreview.value.target_table}，不会复制数据。请确认目标表不存在，并已检查 SQL 与驱动限制。`,
      '确认创建物理分片表',
      { type: 'warning', confirmButtonText: '提交 DDL 任务', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  cloneSubmitting.value = true
  try {
    const { data } = await api.post<ShardDDLTask>(`/shard-rules/${tableRule.value.id}/clone-tasks`, {
      source_table: clonePreview.value.source_table,
      target_table: clonePreview.value.target_table,
      confirm: true,
    })
    cloneTask.value = data
    cloneAudit.value = []
    clonePreview.value = null
    ElMessage.success('物理表克隆任务已提交')
    await refreshCloneTask()
  } catch (error: unknown) {
    const response = (error as { response?: { data?: { message?: string } } })?.response
    ElMessage.error(response?.data?.message ?? '提交物理表克隆任务失败')
  } finally {
    cloneSubmitting.value = false
  }
}

async function refreshCloneTask() {
  if (!cloneTask.value) return
  cloneTaskRefreshing.value = true
  try {
    const [taskResponse, auditResponse] = await Promise.all([
      api.get<ShardDDLTask>(`/shard-ddl-tasks/${cloneTask.value.id}`),
      api.get<{ items: ShardDDLAuditEvent[] }>(`/shard-ddl-tasks/${cloneTask.value.id}/audit`),
    ])
    cloneTask.value = taskResponse.data
    cloneAudit.value = auditResponse.data.items
    if (cloneTask.value.status === 'succeeded') {
      ElMessage.success('物理表结构克隆完成')
      const { data } = await api.get<{ items: string[] }>(`/shard-rules/${tableRule.value?.id}/tables`)
      physicalTables.value = data.items
    }
  } catch (error: unknown) {
    const response = (error as { response?: { data?: { message?: string } } })?.response
    ElMessage.error(response?.data?.message ?? '刷新 DDL 任务失败')
  } finally {
    cloneTaskRefreshing.value = false
  }
}

async function checkTableExists() {
  if (!tableRule.value || !tableCheckName.value.trim()) {
    ElMessage.warning('请填写要检查的物理表名')
    return
  }
  tableChecking.value = true
  tableCheckResult.value = null
  try {
    const { data } = await api.post<{ exists: boolean }>(
      `/shard-rules/${tableRule.value.id}/tables/exists`,
      { table_name: tableCheckName.value.trim() },
    )
    tableCheckResult.value = data.exists
  } catch (error: unknown) {
    const response = (error as { response?: { data?: { message?: string } } })?.response
    ElMessage.error(response?.data?.message ?? '检查物理表失败')
  } finally {
    tableChecking.value = false
  }
}

function openPreview(rule: ShardRule) {
  previewRule.value = rule
  previewResult.value = null
  Object.assign(routeInput, {
    timestamp: '', id: '', start_time: '', end_time: '', start_id: '', end_id: '',
  })
  previewVisible.value = true
}

async function runPreview() {
  if (!previewRule.value) return
  const payload: Record<string, string | number> = {}
  if (isTimeRule.value) {
    if (routeInput.start_time.trim() || routeInput.end_time.trim()) {
      if (!routeInput.start_time.trim() || !routeInput.end_time.trim()) {
        ElMessage.warning('开始时间和结束时间需要同时填写')
        return
      }
      payload.start_time = routeInput.start_time.trim()
      payload.end_time = routeInput.end_time.trim()
    } else if (routeInput.timestamp.trim()) {
      payload.timestamp = routeInput.timestamp.trim()
    } else {
      ElMessage.warning('请填写单点时间，或完整的起止时间范围')
      return
    }
  } else if (routeInput.start_id.trim() || routeInput.end_id.trim()) {
    const start = Number(routeInput.start_id)
    const end = Number(routeInput.end_id)
    if (!routeInput.start_id.trim() || !routeInput.end_id.trim() || !Number.isSafeInteger(start) || !Number.isSafeInteger(end)) {
      ElMessage.warning('请填写有效的起始 ID 和结束 ID')
      return
    }
    payload.start_id = start
    payload.end_id = end
  } else {
    const id = Number(routeInput.id)
    if (!routeInput.id.trim() || !Number.isSafeInteger(id)) {
      ElMessage.warning('请填写有效的 ID')
      return
    }
    payload.id = id
  }

  previewing.value = true
  try {
    const { data } = await api.post<RoutePreview>('/shard-rules/' + previewRule.value.id + '/preview', payload)
    previewResult.value = data
  } catch (error: unknown) {
    const response = (error as { response?: { data?: { message?: string } } })?.response
    ElMessage.error(response?.data?.message ?? '路由预览失败')
  } finally {
    previewing.value = false
  }
}

async function remove(rule: ShardRule) {
  try {
    await ElMessageBox.confirm(
      '只删除 meteor-forge 中的分片规则配置，不会删除目标数据库中的物理表或数据。',
      '确认删除分片规则：' + rule.base_table,
      { type: 'warning', confirmButtonText: '删除配置', cancelButtonText: '取消' },
    )
    await api.delete('/shard-rules/' + rule.id)
    ElMessage.success('分片规则配置已删除')
    await load()
  } catch (error: unknown) {
    if (error !== 'cancel' && error !== 'close') ElMessage.error('删除分片规则失败')
  }
}

onMounted(load)
</script>

<template>
  <div class="page-heading">
    <div>
      <div class="eyebrow">SHARDING CONTROL PLANE</div>
      <h1>分库分表</h1>
      <p>登记逻辑表的分片策略，并调用核心 sharding 规则预览单点或范围路由。当前预览不会访问或修改目标数据库。</p>
    </div>
    <el-button type="primary" :disabled="!dataSources.length" @click="openCreate">新增分片规则</el-button>
  </div>

  <el-alert
    title="当前阶段仅管理规则与路由预览"
    description="规则保存在独立管理元数据库。创建、更新或删除规则不会自动建表、迁移数据或删除物理表；物理表盘点和 DDL 操作将在后续阶段实现。"
    type="info"
    :closable="false"
    show-icon
    class="sharding-alert"
  />

  <el-card shadow="never" class="shard-card">
    <div class="table-toolbar">
      <strong>分片规则列表</strong>
      <div class="toolbar-actions">
        <span>{{ rules.length }} 条规则</span>
        <el-button text :loading="loading" @click="load">刷新</el-button>
      </div>
    </div>
    <el-empty v-if="!dataSources.length && !loading" description="请先在数据源管理中登记目标数据库" />
    <el-table v-else :data="rules" v-loading="loading" empty-text="尚未配置分片规则" style="width: 100%">
      <el-table-column prop="base_table" label="逻辑表" min-width="150" />
      <el-table-column label="目标数据源" min-width="190">
        <template #default="{ row }">
          <div class="source-cell"><strong>{{ row.data_source_name }}</strong><span>{{ row.driver }}</span></div>
        </template>
      </el-table-column>
      <el-table-column label="分片策略" min-width="130">
        <template #default="{ row }">{{ shardTypeLabel(row.shard_type) }}</template>
      </el-table-column>
      <el-table-column label="策略参数" min-width="150">
        <template #default="{ row }">
          <span v-if="row.shard_type === 'id_hash'">{{ row.hash_shards }} 个哈希分片</span>
          <span v-else-if="row.shard_type === 'id_range'">每片 {{ row.range_size.toLocaleString() }} 个 ID</span>
          <span v-else>时间维度</span>
        </template>
      </el-table-column>
      <el-table-column prop="updated_at" label="最近更新" min-width="180">
        <template #default="{ row }">{{ new Date(row.updated_at).toLocaleString() }}</template>
      </el-table-column>
      <el-table-column label="操作" fixed="right" width="210">
        <template #default="{ row }">
          <el-button link type="primary" @click="openPreview(row)">路由预览</el-button>
          <el-button link type="success" @click="openTables(row)">物理表</el-button>
          <el-button link @click="openEdit(row)">编辑</el-button>
          <el-button link type="danger" @click="remove(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>
  </el-card>

  <el-dialog v-model="dialogVisible" :title="editingID ? '编辑分片规则' : '新增分片规则'" width="560px" destroy-on-close>
    <el-form :model="form" label-position="top">
      <el-form-item label="目标数据源" required>
        <el-select v-model="form.data_source_id" style="width: 100%" placeholder="请选择数据源">
          <el-option v-for="source in dataSources" :key="source.id" :label="source.name + ' · ' + source.driver" :value="source.id" />
        </el-select>
      </el-form-item>
      <el-form-item label="逻辑基础表名" required>
        <el-input v-model="form.base_table" maxlength="40" placeholder="例如：orders" />
        <div class="field-hint">仅允许字母、数字和下划线，必须以字母或下划线开头；为生成后缀预留表名长度。</div>
      </el-form-item>
      <el-form-item label="分片策略" required>
        <el-select v-model="form.shard_type" style="width: 100%">
          <el-option v-for="item in shardTypes" :key="item.value" :label="item.label" :value="item.value" />
        </el-select>
      </el-form-item>
      <el-form-item v-if="form.shard_type === 'id_hash'" label="哈希分片数量">
        <el-input-number v-model="form.hash_shards" :min="1" :max="4096" />
      </el-form-item>
      <el-form-item v-if="form.shard_type === 'id_range'" label="每个分片的 ID 容量">
        <el-input-number v-model="form.range_size" :min="1" :max="2147483647" />
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="dialogVisible = false">取消</el-button>
      <el-button type="primary" :loading="saving" @click="save">保存规则</el-button>
    </template>
  </el-dialog>

  <el-dialog v-model="tableDialogVisible" title="物理分片表" width="620px" destroy-on-close>
    <template v-if="tableRule">
      <div class="table-inventory-heading">
        <div><strong>{{ tableRule.base_table }}</strong><span>{{ tableRule.data_source_name }} · {{ shardTypeLabel(tableRule.shard_type) }}</span></div>
        <el-tag effect="plain">{{ physicalTables.length }} 张匹配表</el-tag>
      </div>
      <el-table :data="physicalTables.map((name) => ({ name }))" v-loading="tableLoading" max-height="280" empty-text="目标数据库中没有符合当前策略命名规则的物理表">
        <el-table-column prop="name" label="物理表名" />
      </el-table>
      <el-divider />
      <div class="table-check-row">
        <el-input v-model="tableCheckName" placeholder="输入物理表名，例如 orders_202611" />
        <el-button type="primary" :loading="tableChecking" @click="checkTableExists">检查存在性</el-button>
      </div>
      <el-alert v-if="tableCheckResult !== null" :title="tableCheckResult ? '物理表存在' : '物理表不存在'" :type="tableCheckResult ? 'success' : 'warning'" :closable="false" show-icon class="table-check-result" />
      <el-divider />
      <div class="clone-heading"><strong>结构克隆</strong><el-tag type="warning" effect="plain">不会复制数据</el-tag></div>
      <div class="clone-form">
        <el-input v-model="cloneSource" placeholder="模板表，例如 orders" />
        <el-input v-model="cloneTarget" placeholder="目标表，例如 orders_202611" />
        <el-button type="primary" :loading="clonePreviewing" @click="previewClone">生成 DDL 预览</el-button>
      </div>
      <template v-if="clonePreview">
        <el-alert :title="clonePreview.warning" type="warning" :closable="false" show-icon class="clone-warning" />
        <pre class="clone-sql">{{ clonePreview.sql }}</pre>
        <el-button type="danger" :loading="cloneSubmitting" @click="submitClone">确认并提交克隆任务</el-button>
      </template>
      <div v-if="cloneTask" class="clone-task">
        <div class="clone-task-heading">
          <strong>任务 {{ cloneTask.id.slice(0, 12) }}</strong>
          <el-tag :type="cloneTask.status === 'succeeded' ? 'success' : cloneTask.status === 'failed' ? 'danger' : 'info'">{{ cloneTask.status }}</el-tag>
          <el-button text :loading="cloneTaskRefreshing" @click="refreshCloneTask">刷新状态与审计</el-button>
        </div>
        <p>{{ cloneTask.message }}</p>
        <el-timeline v-if="cloneAudit.length">
          <el-timeline-item v-for="(event, index) in cloneAudit" :key="index" :timestamp="new Date(event.occurred_at).toLocaleString()">
            {{ event.from_status || 'created' }} → {{ event.to_status }} · {{ event.message }}
          </el-timeline-item>
        </el-timeline>
      </div>
      <p class="table-inventory-note">物理表盘点为只读。克隆操作会创建新表，需先预览 SQL 并二次确认；不复制数据，不提供 DROP。</p>
    </template>
    <template #footer><el-button @click="tableDialogVisible = false">关闭</el-button></template>
  </el-dialog>

  <el-dialog v-model="previewVisible" title="分片路由预览" width="650px" destroy-on-close>
    <template v-if="previewRule">
      <el-descriptions :column="2" border size="small" class="preview-meta">
        <el-descriptions-item label="逻辑表">{{ previewRule.base_table }}</el-descriptions-item>
        <el-descriptions-item label="策略">{{ shardTypeLabel(previewRule.shard_type) }}</el-descriptions-item>
        <el-descriptions-item label="数据源">{{ previewRule.data_source_name }}</el-descriptions-item>
        <el-descriptions-item label="驱动">{{ previewRule.driver }}</el-descriptions-item>
      </el-descriptions>
      <el-form label-position="top" class="preview-form">
        <template v-if="isTimeRule">
          <el-form-item label="单点时间（RFC3339，可选）">
            <el-input v-model="routeInput.timestamp" placeholder="2026-11-05T09:00:00+08:00" />
          </el-form-item>
          <div class="range-fields">
            <el-form-item label="开始时间（RFC3339）"><el-input v-model="routeInput.start_time" placeholder="2026-10-01T00:00:00Z" /></el-form-item>
            <el-form-item label="结束时间（RFC3339）"><el-input v-model="routeInput.end_time" placeholder="2026-11-30T23:59:59Z" /></el-form-item>
          </div>
        </template>
        <template v-else>
          <el-form-item label="单个 ID（可选）"><el-input v-model="routeInput.id" placeholder="123456" /></el-form-item>
          <div class="range-fields">
            <el-form-item label="起始 ID"><el-input v-model="routeInput.start_id" placeholder="100000" /></el-form-item>
            <el-form-item label="结束 ID"><el-input v-model="routeInput.end_id" placeholder="299999" /></el-form-item>
          </div>
        </template>
      </el-form>
      <el-button type="primary" :loading="previewing" @click="runPreview">计算路由</el-button>
      <div v-if="previewResult" class="preview-result">
        <div class="preview-result-heading"><strong>命中 {{ previewResult.table_count }} 张物理表</strong><el-tag type="success" effect="plain">只读预览</el-tag></div>
        <el-tag v-for="table in previewResult.tables" :key="table" class="table-tag">{{ table }}</el-tag>
      </div>
    </template>
    <template #footer><el-button @click="previewVisible = false">关闭</el-button></template>
  </el-dialog>
</template>

<style scoped>
.sharding-alert { margin-bottom: 20px; }
.shard-card { border: 1px solid #e8edf4; border-radius: 12px; }
.table-toolbar { display: flex; align-items: center; justify-content: space-between; margin-bottom: 18px; }
.table-toolbar strong { color: #26354d; font-size: 13px; }
.toolbar-actions { display: flex; align-items: center; gap: 12px; color: #8a95a5; font-size: 12px; }
.source-cell { display: flex; flex-direction: column; gap: 4px; }
.source-cell strong { color: #344054; font-size: 12px; }
.source-cell span { color: #98a2b3; font-size: 11px; }
.field-hint { margin-top: 5px; color: #98a2b3; font-size: 11px; line-height: 1.5; }
.preview-meta { margin-bottom: 18px; }
.table-inventory-heading { display: flex; align-items: center; justify-content: space-between; margin-bottom: 14px; }
.table-inventory-heading > div { display: flex; flex-direction: column; gap: 5px; }
.table-inventory-heading strong { color: #344054; font-size: 13px; }
.table-inventory-heading span, .table-inventory-note { color: #98a2b3; font-size: 11px; }
.table-check-row { display: flex; gap: 10px; }
.table-check-result { margin-top: 12px; }
.clone-heading, .clone-task-heading { display: flex; align-items: center; gap: 10px; margin: 14px 0 10px; }
.clone-form { display: grid; grid-template-columns: 1fr 1fr auto; gap: 8px; }
.clone-warning { margin: 12px 0; }
.clone-sql { overflow-x: auto; padding: 12px; border-radius: 8px; background: #f5f7fa; color: #344054; font-size: 11px; white-space: pre-wrap; }
.clone-task { margin-top: 18px; padding-top: 12px; border-top: 1px solid #edf0f5; }
.clone-task-heading { justify-content: space-between; }
.clone-task p { color: #667085; font-size: 12px; }
.preview-form { margin-top: 18px; }
.range-fields { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; }
.preview-result { margin-top: 22px; padding-top: 16px; border-top: 1px solid #edf0f5; }
.preview-result-heading { display: flex; align-items: center; justify-content: space-between; margin-bottom: 12px; }
.table-tag { margin: 0 8px 8px 0; }
@media (max-width: 720px) { .clone-form { grid-template-columns: 1fr; } .clone-task-heading { flex-wrap: wrap; } }
@media (max-width: 560px) { .range-fields { grid-template-columns: 1fr; gap: 0; } }
</style>
