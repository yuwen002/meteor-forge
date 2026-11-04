import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  server: {
    host: '127.0.0.1',
    port: 5173,
    proxy: {
      '/api': {
        target: process.env.METEOR_ADMIN_API ?? 'http://127.0.0.1:8081',
        changeOrigin: true,
      },
    },
  },
})
