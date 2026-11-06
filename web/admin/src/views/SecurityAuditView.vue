<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { listSecurityAuditEvents, type SecurityAuditEvent } from '../api'

const events = ref<SecurityAuditEvent[]>([])
const loading = ref(false)
const page = ref(1)
const total = ref(0)
const pageSize = 20

const actionLabels: Record<SecurityAuditEvent['action'], string> = {
  login: '登录',
  logout: '登出',
  password_change: '修改密码',
  admin_create: '创建管理员',
  admin_role_update: '变更角色',
  admin_delete: '删除管理员',
}
const outcomeLabels: Record<SecurityAuditEvent['outcome'], string> = {
  succeeded: '成功',
  failed: '失败',
  denied: '拒绝',
}
function actionLabel(value: unknown): string {
  if (typeof value !== 'string' || !Object.hasOwn(actionLabels, value)) return String(value ?? '')
  return actionLabels[value as SecurityAuditEvent['action']]
}
function outcomeLabel(value: unknown): string {
  if (typeof value !== 'string' || !Object.hasOwn(outcomeLabels, value)) return String(value ?? '')
  return outcomeLabels[value as SecurityAuditEvent['outcome']]
}
function outcomeType(value: unknown) {
  if (value === 'succeeded') return 'success'
  if (value === 'failed' || value === 'denied') return 'danger'
  return 'info'
}
async function refresh() {
  loading.value = true
  try {
    const result = await listSecurityAuditEvents(pageSize, (page.value - 1) * pageSize)
    events.value = result.items
    total.value = result.total
  } catch {
    ElMessage.error('安全审计加载失败，请确认当前账号具有管理员权限')
  } finally {
    loading.value = false
  }
}
function changePage(next: number) {
  page.value = next
  void refresh()
}
onMounted(() => void refresh())
</script>

<template>
  <section class="security-audit-view">
    <div class="page-heading">
      <div>
        <div class="eyebrow">SYSTEM / SECURITY AUDIT</div>
        <h2>安全审计</h2>
        <p>追踪登录、密码变更和管理员账号生命周期事件。审计记录不包含密码或会话凭据。</p>
      </div>
      <el-button :loading="loading" @click="refresh">刷新记录</el-button>
    </div>

    <el-alert
      title="仅管理员可查看"
      description="来源地址取管理 API 直接观察到的远端地址，不信任 X-Forwarded-For 等转发头。"
      type="info"
      :closable="false"
      show-icon
    />

    <el-card shadow="never">
      <el-table :data="events" v-loading="loading" row-key="id" empty-text="暂无安全审计事件">
        <el-table-column label="时间" min-width="190">
          <template #default="{ row }">{{ new Date(row.occurred_at).toLocaleString() }}</template>
        </el-table-column>
        <el-table-column label="事件" min-width="140">
          <template #default="{ row }">{{ actionLabel(row.action) }}</template>
        </el-table-column>
        <el-table-column label="结果" width="110">
          <template #default="{ row }">
            <el-tag :type="outcomeType(row.outcome)" effect="plain">{{ outcomeLabel(row.outcome) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="actor" label="操作者" min-width="150" />
        <el-table-column prop="subject" label="目标账号" min-width="150" />
        <el-table-column prop="request_source" label="来源地址" min-width="150" />
        <el-table-column prop="details" label="说明" min-width="220" show-overflow-tooltip />
      </el-table>
      <div class="pagination">
        <span>共 {{ total }} 条记录</span>
        <el-pagination :current-page="page" :page-size="pageSize" :total="total" layout="prev, pager, next" @current-change="changePage" />
      </div>
    </el-card>
  </section>
</template>

<style scoped>
.security-audit-view { display: flex; flex-direction: column; gap: 18px; }
.page-heading { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
h2 { margin: 0 0 8px; font-size: 22px; }
p { margin: 0; color: var(--el-text-color-secondary); }
.pagination { display: flex; justify-content: flex-end; align-items: center; gap: 16px; margin-top: 18px; color: var(--el-text-color-secondary); font-size: 12px; }
@media (max-width: 760px) { .page-heading { align-items: flex-start; flex-direction: column; } .pagination { align-items: flex-start; flex-direction: column; } }
</style>
