<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  createAdminUser, deleteAdminUser, listAdminUsers, updateAdminUserRole,
  type AdminUser,
} from '../api'

const users = ref<AdminUser[]>([])
const loading = ref(false)
const saving = ref(false)
const form = reactive<{ username: string; password: string; role: AdminUser['role'] }>({
  username: '', password: '', role: 'viewer',
})
const roleOptions: { label: string; value: AdminUser['role'] }[] = [
  { label: '只读查看者', value: 'viewer' },
  { label: '迁移操作员', value: 'migration_operator' },
  { label: '分片操作员', value: 'shard_operator' },
  { label: '管理员', value: 'administrator' },
]

async function refresh() {
  loading.value = true
  try { users.value = await listAdminUsers() }
  catch { ElMessage.error('加载管理员列表失败') }
  finally { loading.value = false }
}
async function create() {
  if (!form.username.trim() || form.password.length < 12) {
    ElMessage.warning('用户名不能为空，初始密码至少需要 12 个字符')
    return
  }
  saving.value = true
  try {
    await createAdminUser({ username: form.username.trim(), password: form.password, role: form.role })
    form.username = ''; form.password = ''; form.role = 'viewer'
    ElMessage.success('管理员账号已创建')
    await refresh()
  } catch { ElMessage.error('创建失败，请检查用户名是否重复及密码长度') }
  finally { saving.value = false }
}
async function handleRoleChange(user: AdminUser, role: string) {
  await changeRole(user, role as AdminUser['role'])
}
async function changeRole(user: AdminUser, role: AdminUser['role']) {
  if (user.role === role) return
  try {
    await updateAdminUserRole(user.id, role)
    ElMessage.success('角色已更新，目标账号的现有会话已失效')
    await refresh()
  } catch { ElMessage.error('角色更新失败；不能降级最后一个管理员') ; await refresh() }
}
async function remove(user: AdminUser) {
  try { await ElMessageBox.confirm(`确定删除管理员“${user.username}”吗？该账号的会话也会被撤销。`, '删除管理员', { type: 'warning' }) }
  catch { return }
  try {
    await deleteAdminUser(user.id)
    ElMessage.success('管理员账号已删除')
    await refresh()
  } catch { ElMessage.error('删除失败；不能删除当前账号或最后一个管理员') }
}
onMounted(refresh)
</script>

<template>
  <section class="users-view">
    <div class="page-heading">
      <div>
        <h2>管理员管理</h2>
        <p>创建管理账号并分配最小权限角色。角色变更会立即撤销该账号的现有会话。</p>
      </div>
      <el-button :loading="loading" @click="refresh">刷新</el-button>
    </div>

    <el-card shadow="never" class="create-card">
      <template #header><strong>创建管理员账号</strong></template>
      <el-form inline @submit.prevent="create">
        <el-form-item label="用户名"><el-input v-model="form.username" maxlength="128" /></el-form-item>
        <el-form-item label="初始密码"><el-input v-model="form.password" type="password" show-password autocomplete="new-password" /></el-form-item>
        <el-form-item label="角色">
          <el-select v-model="form.role" style="width: 170px">
            <el-option v-for="item in roleOptions" :key="item.value" :label="item.label" :value="item.value" />
          </el-select>
        </el-form-item>
        <el-form-item><el-button type="primary" :loading="saving" @click="create">创建账号</el-button></el-form-item>
      </el-form>
      <el-text type="info" size="small">密码至少 12 个字符；密码仅在创建时提交，服务端不会回显或保存明文。</el-text>
    </el-card>

    <el-card shadow="never">
      <el-table :data="users" v-loading="loading" row-key="id">
        <el-table-column prop="username" label="用户名" min-width="180" />
        <el-table-column label="角色" min-width="210">
          <template #default="{ row }">
            <el-select :model-value="row.role" @change="handleRoleChange(row, $event)">
              <el-option v-for="item in roleOptions" :key="item.value" :label="item.label" :value="item.value" />
            </el-select>
          </template>
        </el-table-column>
        <el-table-column prop="created_at" label="创建时间" min-width="200" />
        <el-table-column label="操作" width="100" fixed="right">
          <template #default="{ row }"><el-button link type="danger" @click="remove(row)">删除</el-button></template>
        </el-table-column>
      </el-table>
    </el-card>
  </section>
</template>

<style scoped>
.users-view { display: flex; flex-direction: column; gap: 18px; }
.page-heading { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
h2 { margin: 0 0 8px; font-size: 22px; }
p { margin: 0; color: var(--el-text-color-secondary); }
.create-card :deep(.el-form) { display: flex; flex-wrap: wrap; align-items: flex-end; gap: 4px 12px; }
</style>
