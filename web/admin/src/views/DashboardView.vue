<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { getHealth, type HealthResponse } from '../api'

const health = ref<HealthResponse | null>(null)
const healthError = ref('')
const loading = ref(true)

onMounted(async () => {
  try {
    health.value = await getHealth()
  } catch {
    healthError.value = '无法连接管理 API，请确认 Go 服务已启动。'
  } finally {
    loading.value = false
  }
})
</script>

<template>
  <div class="page-heading">
    <div>
      <div class="eyebrow">CONTROL CENTER</div>
      <h1>数据库管理总览</h1>
      <p>统一管理 Schema 迁移、多租户数据库与分片资源。</p>
    </div>
    <el-tag type="info" effect="plain">第一阶段 · 工程预览</el-tag>
  </div>

  <el-alert
    title="尚未连接管理元数据库"
    description="当前为工程初始化版本。数据源、租户、迁移和分片统计将在对应模块接入真实服务后展示，不使用模拟数字。"
    type="info"
    :closable="false"
    show-icon
    class="intro-alert"
  />

  <div class="overview-grid">
    <el-card shadow="never" class="overview-card">
      <div class="card-label">数据源</div>
      <div class="card-value">—</div>
      <div class="card-foot">等待配置数据源</div>
    </el-card>
    <el-card shadow="never" class="overview-card">
      <div class="card-label">租户</div>
      <div class="card-value">—</div>
      <div class="card-foot">等待接入租户注册表</div>
    </el-card>
    <el-card shadow="never" class="overview-card">
      <div class="card-label">迁移状态</div>
      <div class="card-value">—</div>
      <div class="card-foot">等待连接目标数据库</div>
    </el-card>
    <el-card shadow="never" class="overview-card">
      <div class="card-label">分片表</div>
      <div class="card-value">—</div>
      <div class="card-foot">等待配置分片规则</div>
    </el-card>
  </div>

  <div class="dashboard-columns">
    <el-card shadow="never" class="panel-card">
      <template #header><div class="panel-title">平台服务</div></template>
      <div v-if="loading" class="empty-state">正在检查管理 API…</div>
      <el-result v-else-if="healthError" icon="warning" title="服务未连接" :sub-title="healthError" />
      <div v-else class="service-row">
        <div class="service-icon">API</div>
        <div class="service-info">
          <strong>meteor-forge-admin</strong>
          <span>API {{ health?.api }} · {{ health?.timestamp }}</span>
        </div>
        <el-tag type="success">运行正常</el-tag>
      </div>
    </el-card>
    <el-card shadow="never" class="panel-card">
      <template #header><div class="panel-title">首期建设范围</div></template>
      <div class="scope-list">
        <div><span class="scope-index">01</span><span>数据源注册与连接管理</span><el-tag size="small">计划中</el-tag></div>
        <div><span class="scope-index">02</span><span>迁移版本与执行控制</span><el-tag size="small">计划中</el-tag></div>
        <div><span class="scope-index">03</span><span>租户发现与批量迁移</span><el-tag size="small">计划中</el-tag></div>
        <div><span class="scope-index">04</span><span>分片规则与物理表管理</span><el-tag size="small">计划中</el-tag></div>
      </div>
    </el-card>
  </div>
</template>
