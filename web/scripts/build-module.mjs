// 单模块构建入口: node scripts/build-module.mjs <模块名>
// 例: node scripts/build-module.mjs cicd  → dist/modules/cicd/
import { build } from 'vite'

const mod = process.argv[2]
if (!mod) { console.error('用法: node scripts/build-module.mjs <模块名>'); process.exit(1) }
process.env.MODULE = mod
const t0 = Date.now()
await build({ configFile: 'vite.module.config.ts' })
console.log(`[build-module] ${mod} 完成 (${((Date.now() - t0) / 1000).toFixed(1)}s) → dist/modules/${mod}/`)
