<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { api } from '../api'

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
interface TenantInput {
  name: string
  code: string
  data_source_id: string
  status: 'active' | 'suspended'
}

const items = ref<Tenant[]>([])
const dataSources = ref<DataSource[]>([])
const loading = ref(false)
const saving = ref(false)
const dialogVisible = ref(false)
const editingID = ref('')
const testingID = ref('')
const migratingID = ref('')
const form = reactive<TenantInput>({
  name: '', code: '', data_source_id: '', status: 'active',
})

async function load() {
  loading.value = true
  try {
    const [tenantResponse, sourceResponse] = await Promise.all([
      api.get<{ items: Tenant[] }>('/tenants'),
      api.get<{ items: DataSource[] }>('/datasources'),
    ])
    items.value = tenantResponse.data.items
    dataSources.value = sourceResponse.data.items
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

async function testConnection(item: Tenant) {
  testingID.value = item.id
  try {
    const { data } = await api.post<{ success: boolean; message: string }>('/tenants/' + item.id + '/test')
    if (data.success) ElMessage.success(data.message)
    else ElMessage.warning(data.message)
  } catch {
    ElMessage.error('租户连接测试请求失败')
  } finally {
    testingID.value = ''
  }
}

async function remove(item: Tenant) {
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

onMounted(load)
</script>

<template>
  <div class="page-heading">
    <div>
      <div class="eyebrow">TENANT CONTROL PLANE</div>
      <h1>多租户管理</h1>
      <p>维护租户标识与目标数据源映射。每个租户映射到一个已登记的数据源，连接凭据仍由数据源管理统一加密保管。</p>
    </div>
    <el-button type="primary" :disabled="!dataSources.length" @click="openCreate">添加租户</el-button>
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
      <el-button text :loading="loading" @click="load">刷新</el-button>
    </div>
    <el-empty v-if="!dataSources.length && !loading" description="请先在数据源管理中添加目标数据库" />
    <el-table v-else :data="items" v-loading="loading" empty-text="尚未创建租户" style="width: 100%">
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
      <el-table-column prop="updated_at" label="最近更新" min-width="190">
        <template #default="{ row }">{{ new Date(row.updated_at).toLocaleString() }}</template>
      </el-table-column>
      <el-table-column label="操作" fixed="right" width="290">
        <template #default="{ row }">
          <el-button link type="primary" :loading="testingID === row.id" @click="testConnection(row)">测试连接</el-button>
          <el-button link type="success" :loading="migratingID === row.id" :disabled="row.status !== 'active'" @click="runMigrations(row)">执行 Up</el-button>
          <el-button link @click="openEdit(row)">编辑</el-button>
          <el-button link type="danger" @click="remove(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>
  </el-card>

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
</template>

<style scoped>
.tenant-card { border: 1px solid #e8edf4; border-radius: 12px; }
.tenant-alert { margin-bottom: 20px; }
.table-toolbar { display: flex; align-items: center; justify-content: space-between; margin-bottom: 18px; }
.table-toolbar > div { display: flex; align-items: center; gap: 10px; }
.table-toolbar strong { color: #26354d; font-size: 13px; }
.table-toolbar span, .field-hint { color: #98a2b3; font-size: 11px; }
.source-cell { display: flex; flex-direction: column; gap: 4px; }
.source-cell strong { color: #34445d; font-size: 12px; font-weight: 600; }
.source-cell span { color: #98a2b3; font-size: 11px; }
.field-hint { margin-top: 5px; }
</style>
