import { createRouter, createWebHistory } from 'vue-router'
import DashboardView from './views/DashboardView.vue'
import DataSourcesView from './views/DataSourcesView.vue'
import MigrationsView from './views/MigrationsView.vue'
import TenantsView from './views/TenantsView.vue'
import PlaceholderView from './views/PlaceholderView.vue'

const routes = [
  { path: '/', name: 'dashboard', component: DashboardView, meta: { title: '总览', section: '工作台' } },
  { path: '/datasources', name: 'datasources', component: DataSourcesView, meta: { title: '数据源管理', section: '资源管理' } },
  { path: '/migrations', name: 'migrations', component: MigrationsView, meta: { title: '迁移管理', section: '数据库变更', description: '查看迁移版本、执行状态、Dry Run、升级和回滚。' } },
  { path: '/tenants', name: 'tenants', component: TenantsView, meta: { title: '多租户管理', section: '数据库变更', description: '维护租户标识、数据源映射和启停状态。' } },
  { path: '/sharding', name: 'sharding', component: PlaceholderView, meta: { title: '分库分表', section: '数据库变更', description: '管理分片策略、路由规则和物理分片表。' } },
  { path: '/tasks', name: 'tasks', component: PlaceholderView, meta: { title: '执行任务', section: '运维审计', description: '跟踪迁移、批量租户任务和分片 DDL 的执行过程。' } },
  { path: '/audit', name: 'audit', component: PlaceholderView, meta: { title: '操作审计', section: '运维审计', description: '追踪操作者、操作对象、执行耗时和结果。' } },
]

export default createRouter({
  history: createWebHistory(),
  routes,
})
