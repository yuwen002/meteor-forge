<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { api } from '../api'

interface DataSource { id: string; name: string; driver: string }
interface MigrationStatusItem { version: string; description: string; is_applied: boolean }
interface MigrationPlanItem {
  version: string
  description: string
  action: string
  reversible: boolean
  checksum: string
}
interface MigrationStatusResponse {
  data_source_id: string
  items: MigrationStatusItem[]
  applied_count: number
  pending_count: number
}
interface MigrationPreviewResponse {
  data_source_id: string
  items: MigrationPlanItem[]
  change_count: number
}

const dataSources = ref<DataSource[]>([])
const selectedID = ref('')
const status = ref<MigrationStatusResponse | null>(null)
const preview = ref<MigrationPreviewResponse | null>(null)
const loadingSources = ref(false)
const loadingStatus = ref(false)
const previewing = ref(false)
const selectedSource = computed(() => dataSources.value.find(item => item.id === selectedID.value))
interface MigrationTask {
  id: string
  data_source_id: string
  action: string
  status: string
  message?: string
  created_at: string
}
const tasks = ref<MigrationTask[]>([])
const currentTask = ref<MigrationTask | null>(null)
const taskActive = computed(() => !!currentTask.value && ['queued', 'running'].includes(currentTask.value.status))
const rollbackSteps = ref(1)
const targetVersion = ref('')
const submittingTask = ref(false)

async function loadSources() {
  loadingSources.value = true
  try {
    const { data } = await api.get<{ items: DataSource[] }>('/datasources')
    dataSources.value = data.items
    if (!selectedID.value && data.items.length) selectedID.value = data.items[0].id
    if (selectedID.value && !data.items.some(item => item.id === selectedID.value)) {
      selectedID.value = data.items[0]?.id ?? ''
    }
  } catch {
    ElMessage.error('加载数据源失败')
  } finally {
    loadingSources.value = false
  }
}

async function loadStatus() {
  if (!selectedID.value) return
  loadingStatus.value = true
  preview.value = null
  try {
    const { data } = await api.get<MigrationStatusResponse>('/datasources/' + selectedID.value + '/migrations/status')
    status.value = data
  } catch {
    status.value = null
    ElMessage.error('读取迁移状态失败，请检查迁移目录和目标数据库连接')
  } finally {
    loadingStatus.value = false
  }
}

async function loadTasks() {
  try {
    const { data } = await api.get<{ items: MigrationTask[] }>('/migration-tasks')
    tasks.value = data.items
    if (currentTask.value) currentTask.value = data.items.find(item => item.id === currentTask.value?.id) ?? currentTask.value
  } catch {
    ElMessage.error('读取迁移任务失败')
  }
}

async function waitForTask(id: string) {
  for (let attempt = 0; attempt < 60; attempt++) {
    await new Promise(resolve => window.setTimeout(resolve, 1000))
    try {
      const { data } = await api.get<MigrationTask>('/migration-tasks/' + id)
      currentTask.value = data
      if (!['queued', 'running'].includes(data.status)) {
        await loadStatus()
        await loadTasks()
        ElMessage[data.status === 'succeeded' ? 'success' : 'warning'](data.message ?? '迁移任务已结束')
        return
      }
    } catch {
      ElMessage.error('读取任务状态失败')
      return
    }
  }
  ElMessage.info('任务仍在执行，可稍后刷新任务状态')
  await loadTasks()
}

async function submitTask(action: 'up' | 'down' | 'down_steps' | 'to_version') {
  if (!selectedID.value || submittingTask.value || taskActive.value) return
  if (action !== 'up') {
    const detail = action === 'down' ? '回滚最近一次迁移' :
      action === 'down_steps' ? '连续回滚最近 ' + rollbackSteps.value + ' 次迁移' :
        '将数据库调整到版本 ' + targetVersion.value
    try {
      await ElMessageBox.confirm(detail + ' 可能改变或删除数据库对象。请确认已备份目标数据库。', '确认危险操作', {
        type: 'warning', confirmButtonText: '确认执行', cancelButtonText: '取消',
      })
    } catch { return }
  }
  if (action === 'down_steps' && (!Number.isInteger(rollbackSteps.value) || rollbackSteps.value < 1 || rollbackSteps.value > 100)) {
    ElMessage.warning('回滚步数必须在 1 到 100 之间')
    return
  }
  if (action === 'to_version' && !targetVersion.value.trim()) {
    ElMessage.warning('请输入目标版本，0 表示回滚全部迁移')
    return
  }
  submittingTask.value = true
  try {
    const body: Record<string, unknown> = { action }
    if (action !== 'up') body.confirm = true
    if (action === 'down_steps') body.steps = rollbackSteps.value
    if (action === 'to_version') body.target_version = targetVersion.value.trim()
    const { data } = await api.post<MigrationTask>('/datasources/' + selectedID.value + '/migrations/tasks', body)
    currentTask.value = data
    ElMessage.info('迁移任务已提交')
    await loadTasks()
    void waitForTask(data.id)
  } catch (error: unknown) {
    const response = (error as { response?: { data?: { message?: string } } })?.response
    ElMessage.error(response?.data?.message ?? '提交迁移任务失败')
  } finally {
    submittingTask.value = false
  }
}

async function cancelTask() {
  if (!currentTask.value || !taskActive.value) return
  try {
    await ElMessageBox.confirm('取消不会自动撤销已经完成的迁移步骤，是否继续？', '取消迁移任务', { type: 'warning' })
    await api.post('/migration-tasks/' + currentTask.value.id + '/cancel')
    ElMessage.info('已发送取消请求')
  } catch {
    // Confirmation dismissed or cancellation request failed.
  }
}

async function createPreview() {
  if (!selectedID.value) return
  previewing.value = true
  try {
    const { data } = await api.post<MigrationPreviewResponse>('/datasources/' + selectedID.value + '/migrations/preview')
    preview.value = data
    ElMessage.success('预览完成，共 ' + data.change_count + ' 个待执行迁移')
  } catch (error: unknown) {
    preview.value = null
    const response = (error as { response?: { data?: { message?: string } } })?.response
    ElMessage.error(response?.data?.message ?? '生成迁移预览失败')
  } finally {
    previewing.value = false
  }
}

watch(selectedID, () => {
  status.value = null
  preview.value = null
  if (selectedID.value) void loadStatus()
})

onMounted(() => { void loadSources(); void loadTasks() })
</script>

<template>
  <div class="page-heading">
    <div>
      <div class="eyebrow">DATABASE CHANGE MANAGEMENT</div>
      <h1>迁移管理</h1>
      <p>查看本地 SQL 迁移脚本与目标数据库的版本差异，并生成待执行计划。</p>
    </div>
    <el-button :loading="loadingSources" @click="loadSources">刷新数据源</el-button>
  </div>

  <el-card shadow="never" class="migration-card">
    <div class="selector-row">
      <div class="selector-copy">
        <strong>目标数据源</strong>
        <span>选择需要检查迁移状态的数据库</span>
      </div>
      <el-select v-model="selectedID" placeholder="请选择数据源" :loading="loadingSources" style="width: min(360px, 100%)">
        <el-option v-for="item in dataSources" :key="item.id" :label="item.name + ' · ' + item.driver" :value="item.id" />
      </el-select>
    </div>

    <el-empty v-if="!dataSources.length && !loadingSources" description="请先在数据源管理中添加数据库连接" />
    <template v-else-if="selectedSource">
      <el-alert
        title="预览模式，不会执行迁移 SQL"
        description="状态检查和预览会确保目标库存在 schema_migrations 历史表；预览不会执行待迁移脚本。"
        type="info"
        :closable="false"
        show-icon
        class="migration-alert"
      />

      <div class="summary-grid" v-loading="loadingStatus">
        <div class="summary-tile">
          <span>迁移脚本</span>
          <strong>{{ status?.items.length ?? '—' }}</strong>
        </div>
        <div class="summary-tile">
          <span>已应用</span>
          <strong class="success-value">{{ status?.applied_count ?? '—' }}</strong>
        </div>
        <div class="summary-tile">
          <span>待执行</span>
          <strong class="pending-value">{{ status?.pending_count ?? '—' }}</strong>
        </div>
      </div>

      <div class="section-heading">
        <div>
          <strong>版本状态</strong>
          <span>按迁移版本升序展示</span>
        </div>
        <el-button size="small" :loading="loadingStatus" @click="loadStatus">刷新状态</el-button>
      </div>
      <el-table :data="status?.items ?? []" v-loading="loadingStatus" empty-text="当前迁移目录没有 SQL 脚本">
        <el-table-column prop="version" label="版本" width="160" />
        <el-table-column prop="description" label="说明" min-width="220" />
        <el-table-column label="状态" width="130">
          <template #default="{ row }">
            <el-tag :type="row.is_applied ? 'success' : 'warning'" effect="plain">
              {{ row.is_applied ? '已应用' : '待执行' }}
            </el-tag>
          </template>
        </el-table-column>
      </el-table>

      <div class="preview-toolbar">
        <div>
          <strong>迁移计划预览</strong>
          <span>核对脚本校验和后列出待执行版本</span>
        </div>
        <el-button type="primary" :disabled="!status" :loading="previewing" @click="createPreview">生成 Dry Run</el-button>
      </div>
      <el-table v-if="preview" :data="preview.items" empty-text="没有待执行迁移">
        <el-table-column prop="version" label="版本" width="160" />
        <el-table-column prop="description" label="说明" min-width="180" />
        <el-table-column label="动作" width="100">
          <template #default><el-tag type="warning" effect="plain">Up</el-tag></template>
        </el-table-column>
        <el-table-column label="可回滚" width="110">
          <template #default="{ row }">
            <el-tag :type="row.reversible ? 'success' : 'danger'" effect="plain">{{ row.reversible ? '是' : '缺少 Down' }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="checksum" label="SHA-256" min-width="240" />
      </el-table>
      <div class="execution-panel">
        <div class="section-heading">
          <div><strong>迁移执行</strong><span>后台执行耗时任务；同一数据源的任务会串行运行</span></div>
          <el-tag v-if="taskActive" type="warning" effect="plain">任务执行中</el-tag>
        </div>
        <el-alert title="执行前请确认目标数据库已备份" description="Up 执行待处理 SQL；Down 和版本跳转可能改变数据库结构。当前管理 API 尚无用户认证/授权，只能在可信本机环境使用。" type="warning" :closable="false" show-icon class="migration-alert" />
        <div class="execution-actions">
          <el-button type="primary" :disabled="!preview || preview.change_count === 0 || !!taskActive" :loading="submittingTask" @click="submitTask('up')">执行全部 Up</el-button>
          <el-button type="danger" plain :disabled="!status?.applied_count || !!taskActive" :loading="submittingTask" @click="submitTask('down')">回滚最近一次</el-button>
          <div class="inline-action">
            <el-input-number v-model="rollbackSteps" :min="1" :max="100" :disabled="!!taskActive" />
            <el-button plain :disabled="!status?.applied_count || !!taskActive" :loading="submittingTask" @click="submitTask('down_steps')">回滚指定步数</el-button>
          </div>
          <div class="inline-action">
            <el-input v-model="targetVersion" placeholder="目标版本，0 表示全部回滚" :disabled="!!taskActive" />
            <el-button plain :disabled="!!taskActive" :loading="submittingTask" @click="submitTask('to_version')">跳转版本</el-button>
          </div>
        </div>
        <div v-if="currentTask" class="task-status">
          <div><strong>当前任务</strong><span>{{ currentTask.id }}</span></div>
          <el-tag>{{ currentTask.status }}</el-tag>
          <p>{{ currentTask.message || '任务已排队' }}</p>
          <el-button v-if="taskActive" size="small" type="danger" plain @click="cancelTask">取消任务</el-button>
        </div>
        <div class="section-heading task-history-heading">
          <div><strong>最近任务</strong><span>最多显示最近 50 条</span></div>
          <el-button size="small" @click="loadTasks">刷新任务</el-button>
        </div>
        <el-table :data="tasks.filter(item => item.data_source_id === selectedID)" size="small" empty-text="暂无迁移任务">
          <el-table-column prop="action" label="操作" width="120" />
          <el-table-column prop="status" label="状态" width="120" />
          <el-table-column prop="message" label="结果说明" min-width="220" />
          <el-table-column prop="created_at" label="创建时间" min-width="190" />
        </el-table>
      </div>
    </template>
  </el-card>
</template>

<style scoped>
.migration-card { border: 1px solid #e8edf4; border-radius: 12px; }
.selector-row, .section-heading, .preview-toolbar { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.selector-copy, .section-heading > div, .preview-toolbar > div { display: flex; flex-direction: column; gap: 5px; }
.selector-copy strong, .section-heading strong, .preview-toolbar strong { color: #26354d; font-size: 13px; }
.selector-copy span, .section-heading span, .preview-toolbar span { color: #98a2b3; font-size: 11px; }
.migration-alert { margin: 22px 0; }
.summary-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 14px; margin: 20px 0 28px; }
.summary-tile { display: flex; flex-direction: column; gap: 9px; padding: 16px; border: 1px solid #edf0f5; border-radius: 10px; }
.summary-tile span { color: #8792a5; font-size: 12px; }
.summary-tile strong { color: #26354d; font-size: 25px; font-weight: 650; }
.summary-tile .success-value { color: #16a36a; }
.summary-tile .pending-value { color: #d18b27; }
.section-heading { margin: 10px 0 14px; }
.preview-toolbar { margin: 28px 0 14px; }
.execution-panel { margin-top: 32px; padding-top: 24px; border-top: 1px solid #edf0f5; }
.execution-actions { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; }
.inline-action { display: flex; align-items: center; gap: 8px; }
.task-status { display: grid; grid-template-columns: 1fr auto; gap: 8px 16px; align-items: center; margin-top: 18px; padding: 14px; border: 1px solid #edf0f5; border-radius: 10px; }
.task-status > div { display: flex; flex-direction: column; gap: 4px; }
.task-status span, .task-status p { color: #8792a5; font-size: 12px; overflow-wrap: anywhere; }
.task-status p { grid-column: 1 / -1; margin: 0; }
.task-history-heading { margin-top: 26px; }
@media (max-width: 680px) {
  .selector-row, .section-heading, .preview-toolbar { align-items: stretch; flex-direction: column; }
  .summary-grid { grid-template-columns: 1fr; }
  .execution-actions, .inline-action { align-items: stretch; flex-direction: column; }
  .inline-action { width: 100%; }
}
</style>
