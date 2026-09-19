// 主 SPA 构建入口（替代裸 vite build）：构建到暂存目录 dist.build，
// 完成后毫秒级换上——消除构建期间 dist 被清空导致的"用户恰在此时访问 → 白屏"窗口。
// 用法: node scripts/build-spa.mjs（package.json 的 build 已指向这里）
import { build } from 'vite'
import fs from 'node:fs'

const t0 = Date.now()
await build({ configFile: 'vite.config.ts', build: { outDir: 'dist.build', emptyOutDir: true } })

// 原子切换: 旧 dist 先改名让位（保留几秒以便正在写响应的请求收尾），新目录顶上
const retired = `dist.retired-${Date.now()}`
let hadOld = false
try { fs.renameSync('dist', retired); hadOld = true } catch { /* 首次构建无旧目录 */ }
fs.renameSync('dist.build', 'dist')
// 保留单模块独立页(build-module.mjs 的产物): 换目录时随 dist 一起搬回
try {
  if (hadOld && fs.existsSync(retired + '/modules')) {
    fs.cpSync(retired + '/modules', 'dist/modules', { recursive: true })
  }
} catch {}
if (hadOld) setTimeout(() => { try { fs.rmSync(retired, { recursive: true, force: true }) } catch {} }, 5000)
console.log(`[build-spa] 完成 (${((Date.now() - t0) / 1000).toFixed(1)}s)，已原子切换到 dist/`)
