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
  // 覆盖拷贝: dist 被 8088 占用时无法原子换, 只能原地覆盖。
  // 关键: Vite 产物名带内容哈希, **只覆盖不删除**会让 index-*.js / index-*.css 每构建一次
  // 多留一份 —— 实测 22 次发布后远端 assets/ 堆到 432 个文件, 而且整包一起传给用户。
  // 所以先盖新的, 再把"这次产物里没有的"旧文件清掉。
  // 顺序不能反: 先删会让 index.html 指着一个已被删掉的 js(白屏窗口)。
  const modsBefore = hasModules()
  fs.cpSync('dist.build', 'dist', { recursive: true, force: true })
  const keep = new Set(['modules'])            // 单模块页不在本次产物里, 不是陈旧文件
  for (const n of fs.readdirSync('dist.build')) keep.add(n)
  let removed = 0
  for (const name of fs.readdirSync('dist')) {
    if (keep.has(name)) continue
    fs.rmSync(`dist/${name}`, { recursive: true, force: true })
    removed++
  }
  for (const dir of ['assets']) {              // 只清 assets/: 顶层旧文件本就该没了
    const target = `dist/${dir}`, fresh = `dist.build/${dir}`
    if (!fs.existsSync(target) || !fs.existsSync(fresh)) continue
    for (const n of fs.readdirSync(target)) {
      if (fs.existsSync(`${fresh}/${n}`)) continue
      fs.rmSync(`${target}/${n}`, { recursive: true, force: true })
      removed++
    }
  }
  fs.rmSync('dist.build', { recursive: true, force: true })
  if (removed > 0) console.log(`[build-spa] 清掉 ${removed} 个上一版遗留产物(哈希文件名不删除就会一直堆)`)
  if (!modsBefore && hasModules()) how = 'copy(+modules)'
}

const modulesNote = hasModules()
  ? 'modules/ 已保留 —— 若这轮改过模块源码, 记得再跑 node scripts/build-module.mjs <模块>'
  : 'dist/modules/ 不存在: 单模块独立页还没构建过 (node scripts/build-module.mjs <模块>)'
console.log(`[build-spa] 完成 (${((Date.now() - t0) / 1000).toFixed(1)}s), 切换方式=${how}`)
console.log(`[build-spa] ${modulesNote}`)
