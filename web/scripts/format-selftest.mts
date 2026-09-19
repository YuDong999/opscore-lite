/**
 * lib/format.ts 的自测 —— 纯函数，不需要测试框架
 *
 * 跑法（项目根的 node 即可，Node ≥ 22 自带类型擦除）：
 *   cd web && npm run test:format
 *   或  node --experimental-strip-types scripts/format-selftest.mts
 *
 * 放在 scripts/ 而不是 src/ 下：tsconfig 的 include 只有 "src"，
 * 这里不会被 tsc --noEmit 拖进构建；同时它 import 的是**真实实现**，不是副本。
 */
import {
  fmtBytes, fmtSize, fmtByteRate, fmtPerSec, fmtTime, fmtDateTime,
  timeAgo, fmtDur, fmtDurCn, fmtNum, fmtPct, truncate, EMPTY,
} from '../src/lib/format.ts'

const rows: [string, string, string][] = []
const add = (input: string, get: () => string, expect: string) => {
  let got: string
  try { got = get() } catch (e) { got = 'THROW: ' + e }
  rows.push([input, got, got === expect ? 'ok' : `✗ 期望 ${expect}`])
}

console.log('\n── 字节进位规则（用户指定的两条）──')
add('0', () => fmtBytes(0), '0 B')
add('1023', () => fmtBytes(1023), '1023 B')
add('1024', () => fmtBytes(1024), '1.00 KiB')
add('1048575 差1字节到1MiB', () => fmtBytes(1048575), '1.00 MiB')
add('1048576', () => fmtBytes(1048576), '1.00 MiB')
add('716MB=750780416 不该显示成 0.70GiB', () => fmtBytes(750780416), '716.00 MiB')
add('1073741823 差1字节到1GiB', () => fmtBytes(1073741823), '1.00 GiB')
add('1073741824', () => fmtBytes(1073741824), '1.00 GiB')
add('1099511627776', () => fmtBytes(1099511627776), '1.00 TiB')
add('null', () => fmtBytes(null), EMPTY)
add('-1', () => fmtBytes(-1), EMPTY)
add('NaN', () => fmtBytes(NaN), EMPTY)

console.log('\n── fmtSize 同规则，只换后缀 ──')
add('1048575 不该显示成 1024.00KB', () => fmtSize(1048575), '1.00 MB')
add('750780416', () => fmtSize(750780416), '716.00 MB')

console.log('\n── 速率 ──')
add('fmtByteRate(1536) 会缩放', () => fmtByteRate(1536), '1.50 KiB/s')
add('fmtPerSec(1234) 不缩放', () => fmtPerSec(1234), '1,234/s')
add('fmtPerSec(1234.56)', () => fmtPerSec(1234.56), '1,234.6/s')
add('fmtPerSec(null)', () => fmtPerSec(null), `${EMPTY}/s`)

console.log('\n── 时间 ──')
// 用本机时区无关的断言：毫秒与秒两条路径必须给出同一结果
const MS = 1758000000000
add('秒级/毫秒级 两条路径一致', () => fmtDateTime(MS / 1000) === fmtDateTime(MS) ? 'same' : 'diff', 'same')
add('固定格式形状 YYYY-MM-DD HH:mm:ss', () => /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/.test(fmtDateTime(MS)) ? 'ok' : fmtDateTime(MS), 'ok')
add('slice(5,16) = MM-DD HH:mm（图表轴标签）', () => fmtDateTime(MS).slice(5, 16), fmtDateTime(MS).substring(5, 16))
add('label 长度必须为 11', () => String(fmtDateTime(MS).slice(5, 16).length), '11')
add('fmtTime 空', () => fmtTime(undefined), EMPTY)
add('fmtDateTime 非法串', () => fmtDateTime('not-a-date'), EMPTY)
add('fmtDateTime 空串', () => fmtDateTime(''), EMPTY)

console.log('\n── 时长 ──')
add('fmtDur(0) 视为无值', () => fmtDur(0), EMPTY)
add('fmtDur(500)', () => fmtDur(500), '500ms')
add('fmtDur(1200)', () => fmtDur(1200), '1.2s')
add('fmtDur(185000) 秒数补零', () => fmtDur(185000), '3m05s')
add('fmtDur(3600000) 满1小时进位', () => fmtDur(3600000), '1h00m')
add('fmtDurCn(30000)', () => fmtDurCn(30000), '30 秒')
add('fmtDurCn(300000)', () => fmtDurCn(300000), '5 分钟')
add('fmtDurCn(7200000)', () => fmtDurCn(7200000), '2 小时')
add('fmtDurCn(90000) 不整除按秒', () => fmtDurCn(90000), '90 秒')

console.log('\n── 数字 / 百分比 / 文本 ──')
add('fmtNum(1234567)', () => fmtNum(1234567), '1,234,567')
add('fmtNum(null)', () => fmtNum(null), EMPTY)
add('fmtPct(0) ← 0 不再是"无值"', () => fmtPct(0), '0.00%')
add('fmtPct(26.6612)', () => fmtPct(26.6612), '26.66%')
add('fmtPct(null)', () => fmtPct(null), EMPTY)
add('truncate("abcdefgh",5)', () => truncate('abcdefgh', 5), 'abcd…')

console.log('\n── 相对时间（compact 两种口径）──')
const ago = (sec: number) => Math.floor(Date.now() / 1000) - sec
add('30秒前 compact', () => timeAgo(ago(30), true), '30s 前')
add('3分钟前 compact', () => timeAgo(ago(180), true), '3m 前')
add('2小时前 compact', () => timeAgo(ago(7200), true), '2h 前')
add('30秒前 标准', () => timeAgo(ago(30)), '刚刚')
add('3分钟前 标准', () => timeAgo(ago(180)), '3 分钟前')
add('2天前 标准', () => timeAgo(ago(172800)), '2 天前')
add('null', () => timeAgo(null), EMPTY)

let fail = 0
for (const [input, got, verdict] of rows) {
  if (verdict !== 'ok') fail++
  console.log(`  ${verdict === 'ok' ? '✓' : '✗'}  ${input.padEnd(34)} → ${String(got).padEnd(20)} ${verdict === 'ok' ? '' : verdict}`)
}
console.log(`\n合计 ${rows.length} 项，失败 ${fail} 项\n`)
process.exit(fail ? 1 : 0)
