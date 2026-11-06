<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { api } from '../api'
import { useAdminPermissions } from '../permissions'

interface DataSource {
  id: string
  name: string
  driver: 'mysql' | 'postgres' | 'sqlite' | 'clickhouse'
  created_at: string
  updated_at: string
}
interface DataSourceInput {
  name: string
  driver: DataSource['driver']
  dsn: string
}

const { canManageDataSources, canTestConnections } = useAdminPermissions()

const items = ref<DataSource[]>([])
const loading = ref(false)
const saving = ref(false)
const dialogVisible = ref(false)
const testingID = ref('')
const form = reactive<DataSourceInput>({ name: '', driver: 'mysql', dsn: '' })
const editingID = ref('')
const rules = {
  name: [{ required: true, message: '请输入数据源名称', trigger: 'blur' }],
  driver: [{ required: true, message: '请选择数据库类型', trigger: 'change' }],
  dsn: [{ required: true, message: '请输入连接字符串', trigger: 'blur' }],
}

async function load() {
  loading.value = true
  try {
    const { data } = await api.get<{ items: DataSource[] }>('/datasources')
    items.value = data.items
  } catch {
    ElMessage.error('加载数据源失败，请检查管理服务连接')
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingID.value = ''
  Object.assign(form, { name: '', driver: 'mysql', dsn: '' })
  dialogVisible.value = true
}

async function save() {
  if (!form.name.trim() || !form.dsn.trim()) {
    ElMessage.warning('名称和连接字符串不能为空')
    return
  }
  saving.value = true
  try {
    if (editingID.value) await api.put('/datasources/' + editingID.value, form)
    else await api.post('/datasources', form)
    ElMessage.success(editingID.value ? '数据源已更新' : '数据源已添加')
    dialogVisible.value = false
    await load()
  } catch (error: unknown) {
    const response = (error as { response?: { data?: { message?: string } } })?.response
    ElMessage.error(response?.data?.message ?? '保存数据源失败，请检查输入内容和租户引用状态')
  } finally {
    saving.value = false
  }
}

function openEdit(item: DataSource) {
  editingID.value = item.id
  Object.assign(form, { name: item.name, driver: item.driver, dsn: '' })
  dialogVisible.value = true
}

async function testConnection(item: DataSource) {
  testingID.value = item.id
  try {
    const { data } = await api.post<{ success: boolean; message: string }>('/datasources/' + item.id + '/test')
    if (data.success) ElMessage.success(data.message)
    else ElMessage.warning(data.message)
  } catch {
    ElMessage.error('连接测试请求失败')
  } finally {
    testingID.value = ''
  }
}

async function remove(item: DataSource) {
  try {
    await ElMessageBox.confirm(
      '删除只会移除 meteor-forge 中的数据源登记，不会删除目标数据库或其中的数据。',
      '确认删除数据源：' + item.name,
      { type: 'warning', confirmButtonText: '删除登记', cancelButtonText: '取消' },
    )
    await api.delete('/datasources/' + item.id)
    ElMessage.success('数据源登记已删除')
    await load()
  } catch (error: unknown) {
    if (error !== 'cancel' && error !== 'close') {
      const response = (error as { response?: { data?: { message?: string } } })?.response
      ElMessage.error(response?.data?.message ?? '删除数据源失败，请先解除租户引用或检查迁移任务')
    }
  }
}

onMounted(load)
</script>

<template>
  <div class="page-heading">
    <div>
      <div class="eyebrow">RESOURCE MANAGEMENT</div>
      <h1>数据源管理</h1>
      <p>登记需要由 meteor-forge 管理的数据库连接。连接凭据在服务端加密保存，不会通过列表接口返回。</p>
    </div>
    <el-button v-if="canManageDataSources" type="primary" @click="openCreate">添加数据源</el-button>
  </div>

  <el-alert
    title="凭据保护已启用"
    description="DSN 使用管理服务配置的 AES-256-GCM 密钥加密保存。修改现有数据源时，需要重新输入完整连接字符串。"
    type="info"
    :closable="false"
    show-icon
    class="intro-alert"
  />

  <el-card shadow="never" class="datasource-card">
    <div class="table-toolbar">
      <div>
        <strong>已登记数据源</strong>
        <span>{{ items.length }} 个</span>
      </div>
      <el-button text :loading="loading" @click="load">刷新</el-button>
    </div>
    <el-table :data="items" v-loading="loading" empty-text="尚未添加数据源" style="width: 100%">
      <el-table-column prop="name" label="名称" min-width="180" />
      <el-table-column prop="driver" label="数据库类型" width="150">
        <template #default="{ row }">
          <el-tag effect="plain">{{ row.driver }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="连接凭据" min-width="150">
        <template #default><span class="credential-state"><i />已加密保存</span></template>
      </el-table-column>
      <el-table-column prop="updated_at" label="最近更新" min-width="190">
        <template #default="{ row }">{{ new Date(row.updated_at).toLocaleString() }}</template>
      </el-table-column>
      <el-table-column v-if="canManageDataSources || canTestConnections" label="操作" fixed="right" width="230">
        <template #default="{ row }">
          <el-button v-if="canTestConnections" link type="primary" :loading="testingID === row.id" @click="testConnection(row)">测试连接</el-button>
          <el-button v-if="canManageDataSources" link @click="openEdit(row)">编辑</el-button>
          <el-button v-if="canManageDataSources" link type="danger" @click="remove(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>
  </el-card>

  <el-dialog v-model="dialogVisible" :title="editingID ? '编辑数据源' : '添加数据源'" width="560px" destroy-on-close>
    <el-form :model="form" :rules="rules" label-position="top">
      <el-form-item label="数据源名称" prop="name">
        <el-input v-model="form.name" maxlength="128" placeholder="例如：生产订单库" />
      </el-form-item>
      <el-form-item label="数据库类型" prop="driver">
        <el-select v-model="form.driver" style="width: 100%">
          <el-option label="MySQL" value="mysql" />
          <el-option label="PostgreSQL" value="postgres" />
          <el-option label="SQLite" value="sqlite" />
          <el-option label="ClickHouse" value="clickhouse" />
        </el-select>
      </el-form-item>
      <el-form-item label="连接字符串（DSN）" prop="dsn">
        <el-input v-model="form.dsn" type="textarea" :rows="3" autocomplete="off"
          :placeholder="editingID ? '请输入新的完整 DSN；原凭据不会回显' : '请输入数据库连接字符串'" />
      </el-form-item>
      <el-alert v-if="editingID" title="出于安全考虑，原 DSN 不会回显；保存时将使用新输入的 DSN 替换。" type="warning" :closable="false" />
    </el-form>
    <template #footer>
      <el-button @click="dialogVisible = false">取消</el-button>
      <el-button type="primary" :loading="saving" @click="save">保存</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.datasource-card { border: 1px solid #e8edf4; border-radius: 12px; }
.table-toolbar { display: flex; align-items: center; justify-content: space-between; margin-bottom: 18px; }
.table-toolbar > div { display: flex; align-items: center; gap: 10px; }
.table-toolbar strong { color: #26354d; font-size: 13px; }
.table-toolbar span { color: #98a2b3; font-size: 11px; }
.credential-state { display: inline-flex; align-items: center; gap: 7px; color: #64748b; font-size: 12px; }
.credential-state i { width: 7px; height: 7px; border-radius: 50%; background: #22c55e; }
</style>
