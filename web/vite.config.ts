import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { fileURLToPath, URL } from 'node:url'

// base: './' 让构建产物用相对路径,便于被 Go 的 go:embed 直接托管。
export default defineConfig({
  plugins: [react(), tailwindcss()],
  base: './',
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5173,
    proxy: { '/api': 'http://host.docker.internal:8088' },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
})
