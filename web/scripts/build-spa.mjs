// 主 SPA 构建入口（替代裸 vite build）：构建到暂存目录 dist.build，完成后毫秒级换上
// —— 消除构建期间 dist 被清空导致的"用户恰在此时访问 → 白屏"窗口。
// 用法: node scripts/build-spa.mjs（package.json 的 build 已指向这里）
//
// 两个真实踩过的坑（2026-09-26 / 09-28 各一次）：
//  1. 8088 正在服务 dist 时，Windows 上 rename('dist', …) 会 EPERM —— 原子切换做不成。
//     以前这里直接抛异常退出，于是 dist.build 留在原地、dist 还是旧的：**你以为发了新版，
//     其实一点没变**。现在降级成"覆盖拷贝"（实测服务在跑也能覆盖），并在结尾把用的哪条路打出来。
//  2. emptyOutDir 会把 dist/modules/*（单模块独立页，build-module.mjs 的产物）一起清掉 ——
//     它们不是本脚本的产物。以前只在原子切换那条路上从 retired 搬回来，降级路径就全丢了。
//     现在两条路都保住它，并且**在模块源码可能已变时明确提醒重跑 build-module**，
//     而不是让"模块页还是旧的"变成界面验收时的惊喜。
import { build } from 'vite'
import fs from 'node:fs'

const t0 = Date.now()
await build({ configFile: 'vite.config.ts', build: { outDir: 'dist.build', emptyOutDir: true } })

const hasModules = () => fs.existsSync('dist/modules') && fs.readdirSync('dist/modules').length > 0

let how = 'atomic'
let hadOld = false
const retired = `dist.retired-${Date.now()}`
try {
  fs.renameSync('dist', retired)
  hadOld = true
} catch {
  // 服务在跑 → 目录被占，原子切换不可用，走覆盖
  how = 'copy'
}

if (how === 'atomic') {
  fs.renameSync('dist.build', 'dist')
  // 单模块页随 dist 一起搬回（它们不在本次构建产物里）
  try {
    if (hadOld && fs.existsSync(retired + '/modules')) {
      fs.cpSync(retired + '/modules', 'dist/modules', { recursive: true })
    }
  } catch {}
  if (hadOld) setTimeout(() => { try { fs.rmSync(retired, { recursive: true, force: true }) } catch {} }, 5000)
} else {
  // 覆盖拷贝: dist 原地保留（modules/ 自然不受影响），只把新 SPA 产物盖上去
  const modsBefore = hasModules()
  fs.cpSync('dist.build', 'dist', { recursive: true, force: true })
  fs.rmSync('dist.build', { recursive: true, force: true })
  if (!modsBefore && hasModules()) how = 'copy(+modules)'
}

const modulesNote = hasModules()
  ? 'modules/ 已保留 —— 若这轮改过模块源码, 记得再跑 node scripts/build-module.mjs <模块>'
  : 'dist/modules/ 不存在: 单模块独立页还没构建过 (node scripts/build-module.mjs <模块>)'
console.log(`[build-spa] 完成 (${((Date.now() - t0) / 1000).toFixed(1)}s), 切换方式=${how}`)
console.log(`[build-spa] ${modulesNote}`)
