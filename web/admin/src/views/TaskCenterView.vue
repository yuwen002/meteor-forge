<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { api } from '../api'
import { useAdminPermissions } from '../permissions'
import { canSubmitMigrationDisposition, canUseMigrationDispositionAction, migrationDispositionConfirmation, migrationDispositionLabels, type MigrationDispositionAction } from '../utils/migrationDisposition'

type TaskKind = 'migration' | 'shard_ddl'
type TaskStatus = 'queued' | 'running' | 'succeeded' | 'failed' | 'canceled' | 'interrupted'

interface UnifiedTask {
  id: string
  kind: TaskKind
  action: string
  status: TaskStatus
  data_source_id: string
  created_at: string
  started_at?: string
  finished_at?: string
  message?: string
  tenant_id?: string
  shard_rule_id?: string
  source_table?: string
  target_table?: string
  current_version?: string
  completed_steps?: number
  total_steps?: number
}

interface MigrationReconciliation {
  task_id: string
  status: string
  observed_at: string
  history_available: boolean
  current_version?: string
  current_action?: string
  current_version_state: 'present' | 'absent' | 'unknown'
  current_version_checksum_state: 'matched' | 'mismatch' | 'legacy_unverified' | 'local_script_missing' | 'not_comparable' | 'unknown'
  applied_version_count: number
  unknown_applied_version_count: number
  assessment: string
}

interface MigrationTaskDisposition {
  id: number
  task_id: string
  data_source_id: string
  action: MigrationDispositionAction
  reconciliation: MigrationReconciliation
  operator_note: string
  actor: string
  request_source?: string
  occurred_at: string
}

interface AuditEvent {
  id: number
  task_id: string
  action: string
  from_status?: string
  to_status: string
  actor: string
  request_source?: string
  message?: string
  occurred_at: string
}

const { canCancelMigrationTask, canCancelShardDDL, isAdministrator } = useAdminPermissions()

const tasks = ref<UnifiedTask[]>([])
const selected = ref<UnifiedTask | null>(null)
const audit = ref<AuditEvent[]>([])
const reconciliation = ref<MigrationReconciliation | null>(null)
const reconciliationLoading = ref(false)
const disposition = ref<MigrationTaskDisposition | null>(null)
const dispositionLoading = ref(false)
const dispositionSubmitting = ref(false)
const dispositionAction = ref<MigrationDispositionAction | ''>('')
const dispositionNote = ref('')
const dispositionConfirmation = ref('')
const loading = ref(false)
const detailLoading = ref(false)
const statusFilter = ref('')
const kindFilter = ref('')
const errorText = ref('')
let pollHandle: number | undefined

const filteredTasks = computed(() => tasks.value.filter(task =>
  (!statusFilter.value || task.status === statusFilter.value) &&
  (!kindFilter.value || task.kind === kindFilter.value),
))
const activeCount = computed(() => tasks.value.filter(task => ['queued', 'running'].includes(task.status)).length)

function statusLabel(status: TaskStatus) {
  const labels: Record<TaskStatus, string> = {
    queued: '排队中', running: '执行中', succeeded: '成功', failed: '失败',
    canceled: '已取消', interrupted: '中断',
  }
  return labels[status] ?? status
}
function statusType(status: TaskStatus) {
  if (status === 'succeeded') return 'success'
  if (status === 'failed' || status === 'interrupted') return 'danger'
  if (status === 'running') return 'primary'
  if (status === 'canceled') return 'info'
  return 'warning'
}
function kindLabel(kind: TaskKind) {
  return kind === 'migration' ? '数据库迁移' : '分片 DDL'
}
function actionLabel(task: UnifiedTask) {
  if (task.kind === 'shard_ddl') return task.action === 'clone' ? '物理表结构克隆' : task.action
  const labels: Record<string, string> = { up: '升级迁移', down: '回滚一步', down_steps: '按步数回滚', to_version: '迁移到指定版本' }
  return labels[task.action] ?? task.action
}
function canCancelTask(task: UnifiedTask | null): boolean {
  if (!task) return false
  return task.kind === 'migration' ? canCancelMigrationTask(task.action) : canCancelShardDDL.value
}
function duration(task: UnifiedTask) {
  if (!task.started_at) return '未开始'
  const end = task.finished_at ? new Date(task.finished_at).getTime() : Date.now()
  const seconds = Math.max(0, Math.floor((end - new Date(task.started_at).getTime()) / 1000))
  return seconds >= 60 ? Math.floor(seconds / 60) + ' 分 ' + seconds % 60 + ' 秒' : seconds + ' 秒'
}
function taskTarget(task: UnifiedTask) {
  if (task.kind === 'shard_ddl') return (task.source_table ?? '—') + ' → ' + (task.target_table ?? '—')
  return task.tenant_id ? '租户 ' + task.tenant_id : '数据源 ' + task.data_source_id
}

async function loadTasks(quiet = false) {
  if (!quiet) loading.value = true
  errorText.value = ''
  try {
    const [migrationResult, ddlResult] = await Promise.all([
      api.get<{ items: Omit<UnifiedTask, 'kind'>[] }>('/migration-tasks'),
      api.get<{ items: Omit<UnifiedTask, 'kind'>[] }>('/shard-ddl-tasks'),
    ])
    const merged: UnifiedTask[] = [
      ...migrationResult.data.items.map(task => ({ ...task, kind: 'migration' as const })),
      ...ddlResult.data.items.map(task => ({ ...task, kind: 'shard_ddl' as const })),
    ]
    merged.sort((a, b) => new Date(b.created_at).getTime() - new Date(a.created_at).getTime())
    tasks.value = merged
    if (selected.value) {
      selected.value = merged.find(task => task.id === selected.value?.id && task.kind === selected.value?.kind) ?? selected.value
    }
  } catch {
    errorText.value = '任务列表加载失败，请确认管理 API 已更新到包含分片 DDL 任务接口的版本。'
    if (!quiet) ElMessage.error(errorText.value)
  } finally {
    loading.value = false
  }
}

async function selectTask(task: UnifiedTask) {
  selected.value = task
  detailLoading.value = true
  audit.value = []
  reconciliation.value = null
  disposition.value = null
  dispositionNote.value = ''
  dispositionAction.value = ''
  dispositionConfirmation.value = ''
  try {
    const prefix = task.kind === 'migration' ? '/migration-tasks/' : '/shard-ddl-tasks/'
    const [detail, events] = await Promise.all([
      api.get<UnifiedTask>(prefix + task.id),
      api.get<{ items: AuditEvent[] }>(prefix + task.id + '/audit'),
    ])
    selected.value = { ...detail.data, kind: task.kind }
    audit.value = events.data.items
    if (selected.value?.kind === 'migration' && selected.value.status === 'interrupted') {
      dispositionLoading.value = true
      try {
        const saved = await api.get<MigrationTaskDisposition>(
          '/migration-tasks/' + encodeURIComponent(task.id) + '/disposition',
        )
        if (selected.value?.id === task.id) disposition.value = saved.data
      } catch (error: unknown) {
        const status = (error as { response?: { status?: number } })?.response?.status
        if (status !== 404) ElMessage.error('读取人工处置记录失败')
      } finally {
        dispositionLoading.value = false
      }
    }
  } catch {
    ElMessage.error('读取任务详情或审计记录失败')
  } finally {
    detailLoading.value = false
  }
}

async function reconcileSelected() {
  const task = selected.value
  if (!task || task.kind !== 'migration' || task.status !== 'interrupted') return
  reconciliationLoading.value = true
  reconciliation.value = null
  try {
    const { data } = await api.get<MigrationReconciliation>(
      '/migration-tasks/' + encodeURIComponent(task.id) + '/reconciliation',
    )
    if (selected.value?.id === task.id && selected.value.kind === 'migration') {
      reconciliation.value = data
    }
  } catch {
    ElMessage.error('只读核对失败，请检查目标数据源连接和迁移历史')
  } finally {
    reconciliationLoading.value = false
  }
}

function canUseDisposition(action: MigrationDispositionAction) {
  return canUseMigrationDispositionAction(action, reconciliation.value)
}

async function submitDisposition(action: MigrationDispositionAction) {
  const task = selected.value
  if (!task || task.kind !== 'migration' || !canSubmitMigrationDisposition(isAdministrator.value, task.status, disposition.value)) return
  if (!canUseDisposition(action)) {
    ElMessage.warning('当前只读核对结果不足以支持该处置，请先核实目标库状态')
    return
  }
  const note = dispositionNote.value.trim()
  if (!note || note.length > 2000) {
    ElMessage.warning('请填写 1 到 2000 字的处理说明')
    return
  }
  if (dispositionConfirmation.value !== migrationDispositionConfirmation) {
    ElMessage.warning('请输入完整的风险确认语句')
    return
  }
  try {
    await ElMessageBox.confirm(
      '此操作只记录人工决定，不会修改原任务状态、执行 SQL 或自动创建重试任务。请确认已检查目标数据库实际结构。',
      '确认记录人工处置',
      { type: 'warning', confirmButtonText: '确认记录', cancelButtonText: '返回' },
    )
    dispositionSubmitting.value = true
    const { data } = await api.post<MigrationTaskDisposition>(
      '/migration-tasks/' + encodeURIComponent(task.id) + '/disposition',
      { action, operator_note: note, confirmation: dispositionConfirmation.value },
    )
    if (selected.value?.id === task.id) disposition.value = data
    ElMessage.success('人工处置记录已保存')
    await loadTasks(true)
  } catch (error: unknown) {
    if ((error as { response?: unknown })?.response) {
      const response = (error as { response?: { status?: number; data?: { message?: string; error?: string } } }).response
      if (response?.status === 409) ElMessage.error('任务状态或处置记录已变化，请刷新后重新核对')
      else if (response?.status === 403) ElMessage.error('当前账号没有管理员处置权限')
      else ElMessage.error(response?.data?.message || '保存人工处置失败')
    }
  } finally {
    dispositionSubmitting.value = false
  }
}

function reconciliationVersionLabel(state: MigrationReconciliation['current_version_state']) {
  const labels = { present: '历史记录存在', absent: '历史记录不存在', unknown: '无法判断' }
  return labels[state]
}

function reconciliationChecksumLabel(state: MigrationReconciliation['current_version_checksum_state']) {
  const labels: Record<MigrationReconciliation['current_version_checksum_state'], string> = {
    matched: '校验和一致',
    mismatch: '校验和不一致',
    legacy_unverified: '历史记录未保存校验和',
    local_script_missing: '本地脚本缺失',
    not_comparable: '无可比较记录',
    unknown: '无法判断',
  }
  return labels[state]
}

function reconciliationTagType(state: string) {
  if (state === 'present' || state === 'matched') return 'success'
  if (state === 'mismatch' || state === 'local_script_missing') return 'danger'
  if (state === 'absent') return 'warning'
  return 'info'
}

async function cancelSelected() {
  const task = selected.value
  if (!task || !['queued', 'running'].includes(task.status) || !canCancelTask(task)) return
  try {
    await ElMessageBox.confirm(
      task.kind === 'migration'
        ? '取消不会撤销已经完成的迁移步骤。请在取消后检查目标数据库状态。'
        : 'DDL 取消不保证数据库驱动立即停止操作，请检查目标物理表后再重试。',
      '确认取消任务',
      { type: 'warning', confirmButtonText: '确认取消', cancelButtonText: '返回' },
    )
    const prefix = task.kind === 'migration' ? '/migration-tasks/' : '/shard-ddl-tasks/'
    await api.post(prefix + task.id + '/cancel')
    ElMessage.success('已发送取消请求')
    await loadTasks(true)
    const refreshed = tasks.value.find(item => item.id === task.id && item.kind === task.kind)
    if (refreshed) await selectTask(refreshed)
  } catch (error: unknown) {
    if ((error as { response?: unknown })?.response) ElMessage.error('取消请求失败，任务可能已进入终态')
  }
}

onMounted(() => {
  void loadTasks()
  pollHandle = window.setInterval(() => {
    if (activeCount.value > 0) void loadTasks(true)
  }, 5000)
})
onBeforeUnmount(() => {
  if (pollHandle !== undefined) window.clearInterval(pollHandle)
})
</script>

<template>
  <div class="page-heading">
    <div>
      <div class="eyebrow">OPERATIONS / TASK CENTER</div>
      <h1>执行任务</h1>
      <p>统一查看数据库迁移、租户迁移和分片 DDL 任务，跟踪执行状态与审计事件。</p>
    </div>
    <el-button :loading="loading" @click="loadTasks()">刷新任务</el-button>
  </div>

  <el-alert
    v-if="errorText"
    :title="errorText"
    type="error"
    :closable="false"
    show-icon
    class="task-alert"
  />

  <div class="task-summary">
    <div class="task-summary-item"><span>任务总数</span><strong>{{ tasks.length }}</strong></div>
    <div class="task-summary-item"><span>执行中 / 排队中</span><strong>{{ activeCount }}</strong></div>
    <div class="task-summary-item"><span>成功</span><strong>{{ tasks.filter(item => item.status === 'succeeded').length }}</strong></div>
    <div class="task-summary-item"><span>失败 / 中断</span><strong>{{ tasks.filter(item => ['failed', 'interrupted'].includes(item.status)).length }}</strong></div>
  </div>

  <el-card shadow="never" class="task-center-card">
    <div class="task-toolbar">
      <strong>任务记录</strong>
      <div class="task-filters">
        <el-select v-model="kindFilter" clearable placeholder="全部任务类型" style="width: 150px">
          <el-option label="数据库迁移" value="migration" />
          <el-option label="分片 DDL" value="shard_ddl" />
        </el-select>
        <el-select v-model="statusFilter" clearable placeholder="全部状态" style="width: 140px">
          <el-option label="排队中" value="queued" />
          <el-option label="执行中" value="running" />
          <el-option label="成功" value="succeeded" />
          <el-option label="失败" value="failed" />
          <el-option label="已取消" value="canceled" />
          <el-option label="中断" value="interrupted" />
        </el-select>
      </div>
    </div>
    <el-table :data="filteredTasks" v-loading="loading" row-key="id" empty-text="暂无任务记录" @row-click="selectTask">
      <el-table-column label="类型" width="120">
        <template #default="{ row }">{{ kindLabel(row.kind) }}</template>
      </el-table-column>
      <el-table-column label="操作" min-width="150">
        <template #default="{ row }">{{ actionLabel(row) }}</template>
      </el-table-column>
      <el-table-column label="目标" min-width="220" show-overflow-tooltip>
        <template #default="{ row }">{{ taskTarget(row) }}</template>
      </el-table-column>
      <el-table-column label="状态" width="110">
        <template #default="{ row }"><el-tag :type="statusType(row.status)" effect="plain">{{ statusLabel(row.status) }}</el-tag></template>
      </el-table-column>
      <el-table-column label="创建时间" min-width="175">
        <template #default="{ row }">{{ new Date(row.created_at).toLocaleString() }}</template>
      </el-table-column>
      <el-table-column label="耗时" width="110">
        <template #default="{ row }">{{ duration(row) }}</template>
      </el-table-column>
      <el-table-column label="详情" width="90" fixed="right">
        <template #default="{ row }"><el-button link type="primary" @click.stop="selectTask(row)">查看</el-button></template>
      </el-table-column>
    </el-table>
  </el-card>

  <el-card v-if="selected" shadow="never" class="task-detail-card" v-loading="detailLoading">
    <div class="task-detail-heading">
      <div>
        <strong>任务详情</strong>
        <span class="task-id">{{ selected.id }}</span>
      </div>
      <div class="task-detail-actions">
        <el-tag :type="statusType(selected.status)" effect="plain">{{ statusLabel(selected.status) }}</el-tag>
        <el-button
          v-if="selected.kind === 'migration' && selected.status === 'interrupted'"
          :loading="reconciliationLoading"
          size="small"
          type="warning"
          plain
          @click="reconcileSelected"
        >只读核对</el-button>
        <el-button
          v-if="['queued', 'running'].includes(selected.status) && canCancelTask(selected)"
          type="danger"
          plain
          size="small"
          @click="cancelSelected"
        >取消任务</el-button>
      </div>
    </div>
    <el-descriptions :column="2" border>
      <el-descriptions-item label="任务类型">{{ kindLabel(selected.kind) }}</el-descriptions-item>
      <el-descriptions-item label="执行动作">{{ actionLabel(selected) }}</el-descriptions-item>
      <el-descriptions-item label="数据源 ID">{{ selected.data_source_id }}</el-descriptions-item>
      <el-descriptions-item label="目标对象">{{ taskTarget(selected) }}</el-descriptions-item>
      <el-descriptions-item label="开始时间">{{ selected.started_at ? new Date(selected.started_at).toLocaleString() : '—' }}</el-descriptions-item>
      <el-descriptions-item label="结束时间">{{ selected.finished_at ? new Date(selected.finished_at).toLocaleString() : '—' }}</el-descriptions-item>
      <el-descriptions-item v-if="selected.kind === 'migration'" label="迁移进度">
        {{ selected.completed_steps ?? 0 }} / {{ selected.total_steps ?? 0 }}
        <el-progress v-if="(selected.total_steps ?? 0) > 0" :percentage="Math.round((selected.completed_steps ?? 0) / (selected.total_steps ?? 1) * 100)" />
      </el-descriptions-item>
      <el-descriptions-item label="执行结果" :span="selected.kind === 'migration' ? 1 : 2">{{ selected.message || '—' }}</el-descriptions-item>
    </el-descriptions>

    <div v-if="selected.kind === 'migration' && selected.status === 'interrupted'" class="reconciliation-panel">
      <div class="task-audit-heading">
        <strong>中断任务核对</strong>
        <span>只读取目标库迁移历史，不执行 SQL</span>
      </div>
      <el-empty v-if="!reconciliation && !reconciliationLoading" description="尚未执行核对" :image-size="56">
        <el-button type="warning" plain :loading="reconciliationLoading" @click="reconcileSelected">开始只读核对</el-button>
      </el-empty>
      <template v-if="reconciliation">
        <el-descriptions :column="2" border size="small">
          <el-descriptions-item label="最后进度版本">{{ reconciliation.current_version || '—' }}</el-descriptions-item>
          <el-descriptions-item label="目标库历史">{{ reconciliation.history_available ? '可读取' : '不可读取' }}</el-descriptions-item>
          <el-descriptions-item label="版本记录">
            <el-tag :type="reconciliationTagType(reconciliation.current_version_state)" effect="plain">
              {{ reconciliationVersionLabel(reconciliation.current_version_state) }}
            </el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="脚本校验和">
            <el-tag :type="reconciliationTagType(reconciliation.current_version_checksum_state)" effect="plain">
              {{ reconciliationChecksumLabel(reconciliation.current_version_checksum_state) }}
            </el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="已应用版本数">{{ reconciliation.applied_version_count }}</el-descriptions-item>
          <el-descriptions-item label="本地脚本缺失版本">{{ reconciliation.unknown_applied_version_count }}</el-descriptions-item>
          <el-descriptions-item label="核对时间" :span="2">{{ new Date(reconciliation.observed_at).toLocaleString() }}</el-descriptions-item>
        </el-descriptions>
        <el-alert
          class="reconciliation-assessment"
          :title="reconciliation.assessment"
          type="warning"
          :closable="false"
          show-icon
        />
        <el-alert
          title="核对结果仅供人工判断"
          description="迁移历史缺少版本记录不能证明 SQL 未执行；非事务型 DDL 可能已部分生效。请检查目标库实际结构，不要直接重放任务。"
          type="info"
          :closable="false"
          show-icon
        />
      </template>
    </div>

    <div v-if="selected.kind === 'migration' && selected.status === 'interrupted'" class="disposition-panel">
      <div class="task-audit-heading">
        <strong>人工处置</strong>
        <span>仅管理员可提交；每个任务只能记录一次</span>
      </div>
      <el-skeleton v-if="dispositionLoading" :rows="2" animated />
      <template v-else-if="disposition">
        <el-descriptions :column="2" border size="small">
          <el-descriptions-item label="处置决定">{{ migrationDispositionLabels[disposition.action] }}</el-descriptions-item>
          <el-descriptions-item label="操作者">{{ disposition.actor }}</el-descriptions-item>
          <el-descriptions-item label="记录时间">{{ new Date(disposition.occurred_at).toLocaleString() }}</el-descriptions-item>
          <el-descriptions-item label="请求来源">{{ disposition.request_source || '—' }}</el-descriptions-item>
          <el-descriptions-item label="处理说明" :span="2">{{ disposition.operator_note }}</el-descriptions-item>
        </el-descriptions>
        <el-alert
          class="disposition-warning"
          title="此记录不会改变原任务状态，也不会执行或重放 SQL"
          description="授权重试仅代表批准后续人工创建新任务；确认完成也不替代对目标数据库实际结构的检查。"
          type="warning"
          :closable="false"
          show-icon
        />
      </template>
      <template v-else-if="canSubmitMigrationDisposition(isAdministrator, selected.status, disposition)">
        <el-alert
          title="操作前请完成目标库实际结构核查"
          description="服务端会在提交时重新生成只读核对快照。核对结果未知、校验和不一致或本地脚本缺失时，不允许确认完成或授权重试；终止决定仍可记录。"
          type="warning"
          :closable="false"
          show-icon
        />
        <el-form label-position="top" class="disposition-form">
          <el-form-item label="处置决定">
            <el-radio-group v-model="dispositionAction">
              <el-radio-button label="confirm_completed" :disabled="!canUseDisposition('confirm_completed')">确认已完成</el-radio-button>
              <el-radio-button label="authorize_retry" :disabled="!canUseDisposition('authorize_retry')">授权后续重新提交</el-radio-button>
              <el-radio-button label="terminate">终止并保留现场</el-radio-button>
            </el-radio-group>
          </el-form-item>
          <el-form-item label="处理说明（1–2000 字）">
            <el-input v-model="dispositionNote" type="textarea" :rows="3" maxlength="2000" show-word-limit placeholder="记录检查过的实际结构、备份情况、风险判断和后续责任人" />
          </el-form-item>
          <el-form-item label="风险确认（请完整输入以下语句）">
            <p class="disposition-confirmation">{{ migrationDispositionConfirmation }}</p>
            <el-input v-model="dispositionConfirmation" maxlength="100" placeholder="输入上方完整确认语句" />
          </el-form-item>
          <el-button
            type="warning"
            :loading="dispositionSubmitting"
            :disabled="!dispositionAction || !canSubmitMigrationDisposition(isAdministrator, selected.status, disposition) || !dispositionNote.trim() || dispositionConfirmation !== migrationDispositionConfirmation"
            @click="dispositionAction && submitDisposition(dispositionAction)"
          >记录人工处置</el-button>
        </el-form>
      </template>
      <el-empty v-else description="当前账号无管理员处置权限或任务已存在处置记录" :image-size="56" />
    </div>

    <div class="task-audit-heading"><strong>审计记录</strong><span>{{ audit.length }} 条事件</span></div>
    <el-table :data="audit" size="small" empty-text="暂无审计事件">
      <el-table-column label="时间" min-width="175">
        <template #default="{ row }">{{ new Date(row.occurred_at).toLocaleString() }}</template>
      </el-table-column>
      <el-table-column label="状态变化" min-width="140">
        <template #default="{ row }">{{ row.from_status || '—' }} → {{ row.to_status }}</template>
      </el-table-column>
      <el-table-column prop="actor" label="操作者" width="120" />
      <el-table-column prop="request_source" label="请求来源" min-width="150" />
      <el-table-column prop="message" label="事件说明" min-width="220" show-overflow-tooltip />
    </el-table>
    <el-alert
      title="审计身份说明"
      description="操作者来自已认证的管理账号；请求来源由管理 API 直接观察，不信任 X-Forwarded-For 等转发头。"
      type="info"
      :closable="false"
      class="task-audit-note"
    />
  </el-card>
</template>

<style scoped>
.task-summary { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 14px; margin-bottom: 18px; }
.task-summary-item { display: flex; flex-direction: column; gap: 9px; padding: 17px 19px; border: 1px solid #e8edf4; border-radius: 10px; background: #fff; }
.task-summary-item span { color: #78859a; font-size: 12px; }
.task-summary-item strong { color: #26354d; font-size: 25px; }
.task-center-card, .task-detail-card { margin-bottom: 18px; border: 1px solid #e8edf4; border-radius: 12px; }
.task-toolbar, .task-detail-heading, .task-audit-heading { display: flex; justify-content: space-between; align-items: center; gap: 14px; margin-bottom: 16px; }
.task-toolbar > strong, .task-detail-heading strong, .task-audit-heading strong { color: #26354d; font-size: 14px; }
.task-filters, .task-detail-actions { display: flex; align-items: center; gap: 9px; }
.task-detail-heading > div:first-child { display: flex; flex-direction: column; gap: 6px; min-width: 0; }
.task-id { color: #8a95a5; font-size: 11px; overflow-wrap: anywhere; }
.task-audit-heading { margin: 24px 0 12px; }
.task-audit-heading span { color: #8a95a5; font-size: 11px; }
 .task-audit-note { margin-top: 14px; }
.reconciliation-panel { margin-top: 22px; padding-top: 18px; border-top: 1px solid #e8edf4; }
.reconciliation-assessment { margin: 12px 0; }
.disposition-panel { margin-top: 22px; padding-top: 18px; border-top: 1px solid #e8edf4; }
.disposition-form { margin-top: 16px; max-width: 760px; }
.disposition-confirmation { margin: 0 0 8px; padding: 10px 12px; border-radius: 6px; background: #f5f7fa; color: #46546a; font-size: 13px; }
.disposition-warning { margin-top: 12px; }
.task-alert { margin-bottom: 16px; }
@media (max-width: 760px) {
  .task-summary { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .task-toolbar { align-items: flex-start; flex-direction: column; }
  .task-filters { width: 100%; }
  .task-filters .el-select { flex: 1; min-width: 0; }
  .task-detail-heading { align-items: flex-start; }
}
</style>
