<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { listTenantHealthSchedulerRuns, type TenantHealthSchedulerRun, type TenantHealthSchedulerRunStatus } from '../api'
import { createRequestSequence, runLatestRequest } from '../utils/requestSequence'

const pageSize = 20
const items = ref<TenantHealthSchedulerRun[]>([])
const total = ref(0)
const page = ref(1)
const status = ref<TenantHealthSchedulerRunStatus | ''>('')
const timeRange = ref<[Date, Date] | null>(null)
const loading = ref(false)
const requests = createRequestSequence()

function loadRuns(resetPage = false) {
  if (resetPage) page.value = 1
  loading.value = true
  const params: {
    status?: TenantHealthSchedulerRunStatus
    from?: string
    to?: string
    limit: number
    offset: number
  } = { limit: pageSize, offset: (page.value - 1) * pageSize }
  if (status.value) params.status = status.value
  if (timeRange.value) {
    params.from = timeRange.value[0].toISOString()
    params.to = timeRange.value[1].toISOString()
  }
  void runLatestRequest(requests, () => listTenantHealthSchedulerRuns(params), {
    onSuccess: result => {
      items.value = result.items
      total.value = result.total
    },
    onError: () => {
      items.value = []
      total.value = 0
      ElMessage.error('读取定时巡检运行记录失败')
    },
    onFinally: () => { loading.value = false },
  })
}

function changePage(value: number) {
  page.value = value
  loadRuns()
}

function statusLabel(value: TenantHealthSchedulerRunStatus): string {
  const labels: Record<TenantHealthSchedulerRunStatus, string> = {
    running: '执行中',
    succeeded: '成功',
    failed: '失败',
    lease_lost: '租约丢失',
    interrupted: '接管后中断',
  }
  return labels[value]
}

function statusType(value: TenantHealthSchedulerRunStatus): 'success' | 'danger' | 'warning' | 'info' | 'primary' {
  if (value === 'succeeded') return 'success'
  if (value === 'failed' || value === 'lease_lost') return 'danger'
  if (value === 'interrupted') return 'warning'
  return 'primary'
}

function formatDuration(value: number): string {
  if (value < 1000) return value + ' ms'
  return (value / 1000).toFixed(2) + ' 秒'
}

function formatTime(value?: string): string {
  return value ? new Date(value).toLocaleString() : '—'
}

onMounted(() => loadRuns())
</script>

<template>
  <section class="tenant-health-runs">
    <div class="runs-heading">
      <div>
        <strong>调度运行记录</strong>
        <span>全局巡检轮次，不等同于单租户巡检历史</span>
      </div>
      <el-button text :loading="loading" @click="loadRuns()">刷新</el-button>
    </div>
    <div class="runs-filters">
      <el-select v-model="status" clearable placeholder="全部运行状态" @change="loadRuns(true)">
        <el-option label="执行中" value="running" />
        <el-option label="成功" value="succeeded" />
        <el-option label="失败" value="failed" />
        <el-option label="租约丢失" value="lease_lost" />
        <el-option label="接管后中断" value="interrupted" />
      </el-select>
      <el-date-picker
        v-model="timeRange"
        type="datetimerange"
        range-separator="至"
        start-placeholder="开始时间"
        end-placeholder="结束时间"
        clearable
        @change="loadRuns(true)"
      />
    </div>
    <el-table :data="items" v-loading="loading" size="small" empty-text="暂无调度运行记录">
      <el-table-column prop="id" label="轮次 ID" width="90" />
      <el-table-column label="状态" width="120">
        <template #default="{ row }">
          <el-tag :type="statusType(row.status)">{{ statusLabel(row.status) }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="开始时间" min-width="180">
        <template #default="{ row }">{{ formatTime(row.started_at) }}</template>
      </el-table-column>
      <el-table-column label="结束时间" min-width="180">
        <template #default="{ row }">{{ formatTime(row.finished_at) }}</template>
      </el-table-column>
      <el-table-column label="耗时" width="110">
        <template #default="{ row }">{{ formatDuration(row.duration_ms) }}</template>
      </el-table-column>
      <el-table-column prop="lease_generation" label="租约代次" width="100" />
      <el-table-column label="错误摘要" min-width="220" show-overflow-tooltip>
        <template #default="{ row }">{{ row.error_message || '—' }}</template>
      </el-table-column>
    </el-table>
    <div class="runs-pagination">
      <span>共 {{ total }} 条</span>
      <el-pagination
        :current-page="page"
        :page-size="pageSize"
        :total="total"
        layout="prev, pager, next"
        :disabled="loading || total === 0"
        @current-change="changePage"
      />
    </div>
  </section>
</template>

<style scoped>
.tenant-health-runs { margin: 18px 0 8px; padding-top: 16px; border-top: 1px solid #edf0f5; min-width: 0; }
.runs-heading, .runs-filters, .runs-pagination { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.runs-heading { margin-bottom: 12px; }
.runs-heading > div { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
.runs-heading span, .runs-pagination > span { color: #98a2b3; font-size: 11px; }
.runs-filters { justify-content: flex-start; flex-wrap: wrap; margin-bottom: 12px; }
.runs-filters > * { max-width: 100%; }
.runs-pagination { justify-content: flex-end; margin-top: 12px; }
@media (max-width: 640px) {
  .runs-heading { align-items: flex-start; }
  .runs-filters { align-items: stretch; flex-direction: column; }
  .runs-filters > * { width: 100%; }
  .runs-pagination { align-items: flex-start; flex-direction: column; }
  .runs-pagination :deep(.el-pagination) { max-width: 100%; overflow-x: auto; }
}
</style>
