import { computed, inject, type InjectionKey, type Ref } from 'vue'

export type AdminRole = 'administrator' | 'viewer' | 'migration_operator' | 'shard_operator'
export const adminRoleKey: InjectionKey<Ref<AdminRole | null>> = Symbol('meteor-forge-admin-role')

const roleLabels: Record<AdminRole, string> = {
  administrator: '管理员',
  viewer: '只读查看者',
  migration_operator: '迁移操作员',
  shard_operator: '分片操作员',
}

export function useAdminPermissions() {
  const injectedRole = inject(adminRoleKey, null)
  const role = computed<AdminRole | null>(() => injectedRole?.value ?? null)
  const isAdministrator = computed(() => role.value === 'administrator')
  const canManageDataSources = isAdministrator
  const canTestConnections = isAdministrator
  const canManageTenants = isAdministrator
  const canRunMigrations = computed(() => isAdministrator.value || role.value === 'migration_operator')
  const canRollbackMigrations = isAdministrator
  const canPreviewShardRoutes = computed(() =>
    role.value === 'administrator' || role.value === 'viewer' || role.value === 'shard_operator',
  )
  const canCheckShardTables = canPreviewShardRoutes
  const canManageShardRules = isAdministrator
  const canRunShardDDL = computed(() => isAdministrator.value || role.value === 'shard_operator')
  const canCancelShardDDL = canRunShardDDL
  const roleLabel = computed(() => role.value ? roleLabels[role.value] : '未登录')

  function canCancelMigrationTask(action: string): boolean {
    return isAdministrator.value || (role.value === 'migration_operator' && action === 'up')
  }

  return {
    role, roleLabel, isAdministrator, canManageDataSources, canTestConnections,
    canManageTenants, canRunMigrations, canRollbackMigrations, canPreviewShardRoutes,
    canCheckShardTables, canManageShardRules, canRunShardDDL, canCancelShardDDL,
    canCancelMigrationTask,
  }
}
