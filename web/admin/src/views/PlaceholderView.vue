<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'

const route = useRoute()
const title = computed(() => String(route.meta.title ?? '管理模块'))
const description = computed(() => String(route.meta.description ?? '该模块正在建设中。'))
</script>

<template>
  <div class="page-heading">
    <div>
      <div class="eyebrow">METEOR-FORGE / MANAGEMENT</div>
      <h1>{{ title }}</h1>
      <p>{{ description }}</p>
    </div>
    <el-tag type="info" effect="plain">建设中</el-tag>
  </div>
  <el-card shadow="never" class="module-empty">
    <el-empty description="模块尚未接入真实业务 API">
      <template #image>
        <div class="empty-illustration">MF</div>
      </template>
    </el-empty>
    <p>该页面已纳入管理后台导航。后续将复用现有 Go 核心能力实现真实数据查询与操作。</p>
  </el-card>
</template>
