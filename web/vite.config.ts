import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { fileURLToPath, URL } from 'node:url'

export default defineConfig({
  plugins: [react()],
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
  css: {
    postcss: './postcss.config.js'
  }
})
