<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { loginAdmin } from '../api'

const route = useRoute()
const router = useRouter()
const username = ref('admin')
const password = ref('')
const loading = ref(false)

async function submit() {
  if (!username.value.trim() || !password.value) {
    ElMessage.warning('请输入用户名和密码')
    return
  }
  loading.value = true
  try {
    await loginAdmin(username.value.trim(), password.value)
    const target = typeof route.query.redirect === 'string' ? route.query.redirect : '/'
    await router.replace(target.startsWith('/') && !target.startsWith('//') ? target : '/')
  } catch {
    ElMessage.error('登录失败，请检查用户名、密码或稍后重试')
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <main class="login-page">
    <el-card class="login-card" shadow="never">
      <div class="login-brand">meteor-forge</div>
      <h1>管理员登录</h1>
      <p>登录后管理数据源、迁移、租户与分片任务。</p>
      <el-form label-position="top" @submit.prevent="submit">
        <el-form-item label="用户名">
          <el-input v-model="username" autocomplete="username" @keyup.enter="submit" />
        </el-form-item>
        <el-form-item label="密码">
          <el-input v-model="password" type="password" show-password autocomplete="current-password" @keyup.enter="submit" />
        </el-form-item>
        <el-button type="primary" :loading="loading" class="login-submit" @click="submit">登录</el-button>
      </el-form>
    </el-card>
  </main>
</template>

<style scoped>
.login-page { min-height: 100vh; display: grid; place-items: center; padding: 24px; background: var(--el-bg-color-page); }
.login-card { width: min(420px, 100%); border-radius: 16px; }
.login-brand { color: var(--el-color-primary); font-weight: 700; letter-spacing: .04em; }
h1 { margin: 18px 0 8px; font-size: 24px; }
p { margin: 0 0 24px; color: var(--el-text-color-secondary); }
.login-submit { width: 100%; margin-top: 8px; }
</style>
