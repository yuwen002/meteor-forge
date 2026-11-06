<script setup lang="ts">
import { computed, onMounted, provide, ref, watch } from 'vue'
import { getAdminSession, logoutAdmin } from './api'
import { adminRoleKey, type AdminRole } from './permissions'
import { useRoute, useRouter } from 'vue-router'
import {
  DataAnalysis, Connection, Files, OfficeBuilding, Grid, List, Document,
  Setting, Menu as MenuIcon, User, Lock,
} from '@element-plus/icons-vue'

const route = useRoute()
const router = useRouter()
const currentTitle = computed(() => String(route.meta.title ?? '管理后台'))
const sessionRole = ref<AdminRole | null>(null)
const isAdministrator = computed(() => sessionRole.value === 'administrator')
const roleLabels: Record<AdminRole, string> = {
  administrator: '管理员', viewer: '只读查看者',
  migration_operator: '迁移操作员', shard_operator: '分片操作员',
}
const roleLabel = computed(() => sessionRole.value ? roleLabels[sessionRole.value] : '未登录')
provide(adminRoleKey, sessionRole)

async function refreshSession() {
  try { sessionRole.value = (await getAdminSession()).role }
  catch { sessionRole.value = null }
}
onMounted(() => { void refreshSession() })
watch(() => route.fullPath, () => { void refreshSession() })

async function handleLogout() {
  try { await logoutAdmin() } finally { await router.replace({ name: 'login' }) }
}

const navigation = [
  { section: '工作台', items: [{ path: '/', label: '总览', icon: DataAnalysis }] },
  { section: '资源管理', items: [{ path: '/datasources', label: '数据源管理', icon: Connection }] },
  { section: '数据库变更', items: [
    { path: '/migrations', label: '迁移管理', icon: Files },
    { path: '/tenants', label: '多租户管理', icon: OfficeBuilding },
    { path: '/sharding', label: '分库分表', icon: Grid },
  ] },
  { section: '运维审计', items: [
    { path: '/tasks', label: '执行任务', icon: List },
    { path: '/audit', label: '操作审计', icon: Document },
  ] },
]
</script>

<template>
  <router-view v-if="route.name === 'login'" />
  <el-container v-else class="app-shell">
    <el-aside width="248px" class="sidebar">
      <div class="brand">
        <div class="brand-mark">MF</div>
        <div>
          <div class="brand-name">meteor-forge</div>
          <div class="brand-caption">DATABASE CONTROL PLANE</div>
        </div>
      </div>
      <div v-for="group in navigation" :key="group.section" class="nav-group">
        <div class="nav-heading">{{ group.section }}</div>
        <el-menu :default-active="route.path" router class="side-menu">
          <el-menu-item v-for="item in group.items" :key="item.path" :index="item.path">
            <el-icon><component :is="item.icon" /></el-icon>
            <span>{{ item.label }}</span>
          </el-menu-item>
        </el-menu>
      </div>
      <div v-if="isAdministrator" class="nav-group">
        <div class="nav-heading">系统管理</div>
        <el-menu :default-active="route.path" router class="side-menu">
          <el-menu-item index="/users">
            <el-icon><User /></el-icon>
            <span>管理员管理</span>
          </el-menu-item>
          <el-menu-item index="/security-audit">
            <el-icon><Lock /></el-icon>
            <span>安全审计</span>
          </el-menu-item>
        </el-menu>
      </div>
      <div class="sidebar-footer">
        <el-icon><Setting /></el-icon>
        <span>管理控制台 · 预览版</span>
      </div>
    </el-aside>

    <el-container>
      <el-header class="topbar">
        <div class="topbar-title">
          <el-icon class="mobile-menu"><MenuIcon /></el-icon>
          <span>{{ currentTitle }}</span>
        </div>
        <div class="topbar-meta">
          <span class="status-dot"></span>
          <span>本地管理服务</span>
          <el-tag size="small" effect="plain" type="info">{{ roleLabel }}</el-tag>
          <el-tag size="small" effect="plain" type="info">0.1.0-dev</el-tag>
          <el-button link type="primary" @click="handleLogout">退出登录</el-button>
        </div>
      </el-header>
      <el-main class="main-content">
        <router-view />
      </el-main>
    </el-container>
  </el-container>
</template>
