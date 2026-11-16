import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'
import { fileURLToPath } from 'node:url'

// 一份源码，三个入口：端口与后端地址都从环境读取，不再复制三份 web 目录。
// 启动方式见 package.json 的 dev / dev:user / dev:delivery。
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  const port = Number(env.VITE_PORT || '5173')
  const target = env.VITE_API_TARGET || 'http://127.0.0.1:18090'
  return {
    root: fileURLToPath(new URL('.', import.meta.url)),
    plugins: [react()],
    server: {
      port,
      strictPort: true,
      proxy: {
        '/api': target,
        '/healthz': target,
      },
    },
  }
})
