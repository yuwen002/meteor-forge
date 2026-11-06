<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { api } from '../api'
import { useAdminPermissions } from '../permissions'

interface DataSource {
  id: string
  name: string
  driver: 'mysql' | 'postgres' | 'sqlite' | 'clickhouse'
}
interface DiscoveryRow {
  control_tenant_id: string
  driver?: string
  suggested_name: string
  suggested_code: string
  registered: boolean
  name: string
  code: string
  data_source_id: string
}
const props = defineProps<{ modelValue: boolean; dataSources: DataSource[] }>()
const emit = defineEmits<{ 'update:modelValue': [value: boolean]; imported: [] }>()
const { isAdministrator } = useAdminPermissions()
const visible = computed({
  get: () => props.modelValue,
  set: (value: boolean) => emit('update:modelValue', value),
})
const controlDataSourceID = ref('')
const rows = ref<DiscoveryRow[]>([])
const selectedRows = ref<DiscoveryRow[]>([])
const loading = ref(false)
const syncing = ref(false)
const previewed = ref(false)
const targetSources = computed(() => props.dataSources.filter((source) => source.id !== controlDataSourceID.value))

watch(() => props.modelValue, (open) => {
  if (open) {
    controlDataSourceID.value = ''
    rows.value = []
    selectedRows.value = []
    previewed.value = false
  }
})

function defaultTarget(driver?: string): string {
  return targetSources.value.find((source) => source.driver === driver)?.id
    ?? targetSources.value[0]?.id
    ?? ''
}

async function preview() {
  if (!isAdministrator.value) return
  if (!controlDataSourceID.value) {
    ElMessage.warning('请选择控制库数据源')
    return
  }
  loading.value = true
  try {
    const { data } = await api.get<{
      count: number
      items: Array<Omit<DiscoveryRow, 'name' | 'code' | 'data_source_id'>>
    }>('/tenant-discovery/preview', { params: { data_source_id: controlDataSourceID.value } })
    rows.value = data.items.map((item) => ({
      ...item,
      name: item.suggested_name,
      code: item.suggested_code,
      data_source_id: defaultTarget(item.driver),
    }))
    selectedRows.value = []
    previewed.value = true
    if (!data.count) ElMessage.info('控制库中没有发现 active 状态的租户')
    else ElMessage.success(`发现 ${data.count} 个 active 租户，请检查映射后勾选导入项`)
  } catch (error: unknown) {
    const response = (error as { response?: { data?: { message?: string } } })?.response
    ElMessage.error(response?.data?.message ?? '租户发现失败，请检查控制库连接和表结构')
    previewed.value = false
  } finally {
    loading.value = false
  }
}

async function sync() {
  if (!isAdministrator.value) return
  if (!selectedRows.value.length) {
    ElMessage.warning('请至少勾选一个需要导入的租户')
    return
  }
  const codes = new Set<string>()
  for (const row of selectedRows.value) {
    const code = row.code.trim().toLowerCase()
    if (!row.name.trim() || !code || !row.data_source_id) {
      ElMessage.warning(`请完善租户 ${row.control_tenant_id} 的名称、标识和目标数据源`)
      return
    }
    if (codes.has(code)) {
      ElMessage.warning(`导入列表中存在重复租户标识：${code}`)
      return
    }
    codes.add(code)
  }
  try {
    await ElMessageBox.confirm(
      `即将导入 ${selectedRows.value.length} 个租户。系统会重新检查控制库中的 active 状态；只创建本地登记，不会导入 DSN、覆盖或删除已有租户。是否继续？`,
      '确认导入租户',
      { type: 'warning', confirmButtonText: '确认导入', cancelButtonText: '返回检查' },
    )
    syncing.value = true
    const { data } = await api.post<{ count: number }>('/tenant-discovery/sync', {
      control_data_source_id: controlDataSourceID.value,
      confirm: true,
      items: selectedRows.value.map((row) => ({
        control_tenant_id: row.control_tenant_id,
        name: row.name.trim(),
        code: row.code.trim().toLowerCase(),
        data_source_id: row.data_source_id,
      })),
    })
    ElMessage.success(`成功导入 ${data.count} 个租户，生命周期审计已记录`)
    visible.value = false
    emit('imported')
  } catch (error: unknown) {
    if (error !== 'cancel' && error !== 'close') {
      const response = (error as { response?: { data?: { message?: string } } })?.response
      ElMessage.error(response?.data?.message ?? '租户导入失败，请刷新预览后重试')
    }
  } finally {
    syncing.value = false
  }
}
</script>

<template>
  <el-dialog v-model="visible" title="发现并导入租户" width="min(1100px, 94vw)" destroy-on-close>
    <el-alert
      title="只读发现 · 人工确认导入"
      description="控制库仅用于读取租户 ID、状态和可选驱动。不会读取或回传控制库 DSN；目标数据源必须提前登记。导入前会重新检查租户是否仍为 active。"
      type="info"
      :closable="false"
      show-icon
      class="discovery-alert"
    />
    <div class="discovery-toolbar">
      <el-select v-model="controlDataSourceID" placeholder="选择控制库数据源" style="width: min(420px, 100%)" :disabled="loading || syncing">
        <el-option
          v-for="source in dataSources"
          :key="source.id"
          :label="`${source.name} · ${source.driver}`"
          :value="source.id"
        />
      </el-select>
      <el-button type="primary" :loading="loading" :disabled="!isAdministrator" @click="preview">读取发现预览</el-button>
      <span v-if="previewed" class="discovery-count">发现 {{ rows.length }} 个租户 · 已选择 {{ selectedRows.length }} 个</span>
    </div>
    <el-table
      v-if="previewed"
      :data="rows"
      row-key="control_tenant_id"
      max-height="430"
      empty-text="没有可导入的 active 租户"
      @selection-change="selectedRows = $event"
    >
      <el-table-column type="selection" width="48" />
      <el-table-column prop="control_tenant_id" label="控制库租户 ID" min-width="150" />
      <el-table-column label="发现驱动" width="115">
        <template #default="{ row }">{{ row.driver || '未提供' }}</template>
      </el-table-column>
      <el-table-column label="本地名称" min-width="150">
        <template #default="{ row }">
          <el-input v-model="row.name" size="small" maxlength="128" />
        </template>
      </el-table-column>
      <el-table-column label="本地标识" min-width="145">
        <template #default="{ row }">
          <el-input v-model="row.code" size="small" maxlength="64" />
          <el-tag v-if="row.registered" size="small" type="warning" effect="plain">建议标识已存在</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="目标数据源" min-width="210">
        <template #default="{ row }">
          <el-select v-model="row.data_source_id" size="small" placeholder="选择目标数据源" style="width: 100%">
            <el-option
              v-for="source in targetSources"
              :key="source.id"
              :label="`${source.name} · ${source.driver}`"
              :value="source.id"
              :disabled="Boolean(row.driver && source.driver !== row.driver)"
            />
          </el-select>
        </template>
      </el-table-column>
    </el-table>
    <el-empty v-else description="选择控制库数据源后读取预览；此操作不会修改租户登记" :image-size="72" />
    <template #footer>
      <el-button @click="visible = false">取消</el-button>
      <el-button v-if="previewed" type="primary" :loading="syncing" :disabled="!selectedRows.length || !targetSources.length" @click="sync">
        确认导入所选租户（{{ selectedRows.length }}）
      </el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.discovery-alert { margin-bottom: 18px; }
.discovery-toolbar { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; margin-bottom: 16px; }
.discovery-count { color: #7b8799; font-size: 12px; }
</style>
