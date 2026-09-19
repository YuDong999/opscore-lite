# 前端分层约定

一句话：**别的模块也会用的，放 L1/L2；只有自己用的，留在自己模块里。**

## 三层结构

| 层 | 位置 | 判据 | 现状 |
|---|---|---|---|
| L1 原子 | `src/components/ui/` | 无业务语义的 UI 原语 | 已有 16 个组件 |
| L2 通用 | `src/components/common/`、`src/lib/` | 跨模块复用，但含运维领域语义 | 新建中 |
| L3 模块 | `src/modules/<模块>/` | 只服务单一模块 | 5 个目录模块（分层见 `modules/README.md`） |

判断一个东西属于哪层，只问一个问题：**除了我这个模块，还有谁会用它？**

- 有 → 提到 L1（如果纯粹是 UI 原语）或 L2（如果带运维语义）
- 没有 → 留在 `modules/<模块>/shared.tsx`

## 各层放什么

**L1 `components/ui/`** —— 不认识"主机""容器""流水线"这些概念
`button` `card` `input` `select` `tabs` `dialog` `table` `badge` `checkbox` `label` `progress` `separator` `textarea` `dropdown-menu` `alert` `alert-dialog`

**L2 `components/common/` + `lib/`** —— 认识业务，但不挑模块
- `Modal.tsx` 统一弹层（`Modal` / `ConfirmModal`）—— **已建并完成全仓接入**
- `OptSelect.tsx` 可选值下拉（**已提升** 2026-09-19，LogMonitor/Cicd/TrafficTab 共用）
- `DataGrid.tsx` 数据表格（**已提升** 2026-09-19：类型单一事实源随组件，后端交互经 `backend` prop 注入（onExport/runWrite），L2 不再依赖模块 api 层；api.ts 反向 re-export 类型）
- `ActionIcon.tsx` 通用操作图标（**已提升** 2026-09-19，自 DbIcons 提取）
- **位置变更（09-19）**：DatabaseManager 全家（22 文件）已从 `components/DatabaseManager/` 迁入 `modules/dbmanager/`——L3 私有组件不再占用 L1/L2 目录位；引擎品牌图标随迁至 `modules/dbmanager/DbIcons.tsx`
- `GaugeCard.tsx` 百分比仪表盘卡片（**已建** 2026-09-19）。~~收编资源页 CPU / K8s Ready 率两处~~ 现实际消费者仅 K8s Ready 率一处（资源页 CPU 已按主理人指示恢复内联实现）——单人 L2，保留原因：承载 canvas 几何自适应的成套教训，且百分比 gauge 预期会再出现。
  **几何全自适应**：弧带宽/内径/字号全部从实测容器尺寸推导，字号用 canvas measureText 实测「本次要画的字符串」
  反推恰好放进弧内径的字号——文字变长自动缩小，缩放/窄窗 ResizeObserver 重算，数字不会穿模。
  两次翻车教训见实现文件头注释（canvas 不认 CSS 变量；固定字号+百分比弧=缩放必穿模）
- ~~`CascadeSelector.tsx` 级联选择（待提升）~~ **不提升**：实现留在 `modules/dbmanager/CascadeSelector.tsx`（243 行，2026-09-20 复核模块内外 0 消费者，为死代码删除候选）
- `ContextMenu.tsx` 右键菜单（待合并两份实现）
- `lib/format.ts` 格式化函数集（**已建**）
- `lib/hooks/` `useResource` `usePolling` `useConfirm` `useTableSort`（待从 `cicd/shared.tsx` 提升）

**L3 `modules/<模块>/`** —— 模块的私有领地
```
modules/docker/
├── index.tsx      模块入口（default export，供 App.tsx 懒加载）
├── shared.tsx     模块内共用：类型、常量、小工具、私有小组件
└── panels/        各功能面板
```
判据：**把整个目录删掉，其他模块不受影响**——这才叫独立模块。

## 弹层公共层 API（2026-09-16）

`Modal.tsx` 导出两个组件。设计前提：**沿用 `index.css` 已有类名，不引入新样式**，替换后视觉零变化。

### `Modal` —— 弹层骨架

```tsx
<Modal
  onClose={close}                    // 必填
  title={<>标题 <span className="pill pill-sub">副标</span></>}  // 给了才渲染 .modal-head
  headRight={<div>…</div>}           // 自定义右侧；不传则默认渲染「关闭」按钮
  showClose                          // 默认 true
  footer={<><button/>…</>}           // 传了就渲染成 .modal-actions
  size="default" | "log"             // log = .modal.log-modal（无内边距，内容自己管滚动）
  maxWidth={860}                     // 只设 maxWidth，不覆盖 .modal 的 width
  style={{ width: '95vw' }}          // 需要同时控 width/height 时用这个
  className="stats-modal"            // 附加类名
  closeOnOverlay={!busy}             // 默认 true；置 false 可禁止点遮罩关闭
  stopWheelPropagation               // 默认 true，遮罩吞滚轮事件
>
  内容
</Modal>
```

自动带 **Esc 关闭**。

### `ConfirmModal` —— 二次确认

```tsx
<ConfirmModal
  title="删除卷: xxx"
  desc="说明文字"                      // 或者用 children 放更复杂的内容
  okLabel="确认删除"  cancelLabel="取消"
  danger                             // 确认按钮用 .btn-danger
  busy                               // 禁用并显示「执行中…」
  maxWidth={420}
  onOk={doIt}  onCancel={close}
/>
```

### 接入范围（本次一次性完成）

原先全仓手写 32 处弹层骨架（`modal-overlay` + `modal` + 点遮罩关闭 + `stopPropagation` + 标题栏 + 按钮区），现已全部收敛：

| 文件 | 接入点数 |
|---|---|
| `modules/DockerModule.tsx` | 18（含删掉模块内自带的局部 `ConfirmModal`） |
| `modules/K8sModule.tsx` | 7 |
| `modules/AnsibleModule.tsx` | 3 |
| `modules/FirewallModule.tsx` | 1 |
| `components/LogStreamModal.tsx` | 1 |
| `components/ExecTerminalModal.tsx` | 1 |
| `components/K8sActionPanel.tsx` | 1 |
| `components/K8sCertsModal.tsx` | 1 |

`grep -rn "modal-overlay" src/` 现在只会命中 `Modal.tsx` 自己。

构建产物里 `Modal-*.js` 是一个 ~1.5 kB 的独立 chunk，被上述 8 个入口共享——这正是"公共层"该有的形态。

## 格式化规则（2026-09-16 定稿）

字节数统一走 `lib/format.ts`，规则两条：

1. **进位阈值：当前档位数值达到 1024 才进位**。所以显示值永远落在 `[1, 1024)`，不会出现 `0.04 GB` / `0.7 GiB` 这种要心算才看得懂的数。
2. **小数位：2 位**（B 档为整数，不显示小数）。

实际效果：

| 输入（字节） | 输出 | 说明 |
|---|---|---|
| 1023 | `1023 B` | 不到 1 KB 不进位 |
| 1024 | `1.00 KiB` | |
| 1048575 | `1.00 MiB` | 四舍五入后再判断进位 |
| 716 MB | `716.80 MiB` | **不会**显示成 0.70 GiB |
| 1073741823 | `1.00 GiB` | 差 1 字节也是 1 GiB，不是 1024.00 MiB |
| 1099511627776 | `1.00 TiB` | |

两个字节函数口径相同，只是单位后缀不同：
- `fmtBytes` → `KiB / MiB / GiB / TiB`（推荐，新代码用这个）
- `fmtSize` → `KB / MB / GB / TB`（历史口径，资源/总览/数据库类面板在用）

### 全量 API

命名上刻意把两个**同名但语义不同**的东西分开了，这是接入时最容易踩的坑：

| 函数 | 含义 | 备注 |
|---|---|---|
| `fmtBytes(n, digits=2)` | 字节，`KiB/MiB/GiB…` | |
| `fmtSize(n, digits=2)` | 字节，`KB/MB/GB…` | |
| `fmtByteRate(n)` | **字节**/秒，会自动缩放单位 | 原 `MultiOverview.fmtRate` |
| `fmtPerSec(n, digits=1)` | **次数**/秒，QPS/TPS 用，**不缩放** | 原 `ServerDashboardPanel.fmtRate` |
| `fmtTime(t)` | 绝对时间，跟随 locale | |
| `fmtDateTime(t)` | 固定 `YYYY-MM-DD HH:mm:ss` | 日志时间轴必须用这个，见下 |
| `timeAgo(t, compact?)` | 相对时间 | `compact=true` → `3m 前` |
| `fmtDur(ms)` | 紧凑时长 → `1.2s` / `3m05s` / `1h30m` | 贴在数字旁用 |
| `fmtDurCn(ms)` | 中文时长 → `30 秒` / `5 分钟` / `2 小时` | 中文表格/描述句用 |
| `fmtNum(n, digits=0)` | 千分位 | |
| `fmtPct(n, digits=2)` | 百分比，**0 会正常显示成 `0.00%`** | |
| `truncate(s, max)` | 按字符截断 | |
| `EMPTY` | 无值占位符 `—` | 全文件统一用它 |

**为什么 `fmtDateTime` 必须单独存在**：`toLocaleString()` 在中文环境下是 `2026/9/16 16:30:00`，而日志模块要对时间戳做 `slice(5, 16)` 取 `09-16 16:30` 当图表轴标签——字符位置对不上就全乱了。凡是要**截子串或按字符串排序**的时间，一律用 `fmtDateTime`。

**无值统一用 `—`**（U+2014）。此前仓库里 `-` / `—` / `暂无` 混用，接入时统一成长破折号。

### 接入范围（2026-09-16 完成）

原先 10 处重复实现已全部收敛，本地定义清零（`grep -rn "function fmtBytes\|const fmtSize…" src/` 不再命中）：

| 文件 | 迁入 |
|---|---|
| `DockerModule.tsx` | `fmtBytes` |
| `ResourcesModule.tsx` | `fmtSize` + `fmtByteRate`（16 处调用） |
| `MultiOverview.tsx` | `fmtSize` + `fmtByteRate` |
| `LogMonitorModule.tsx` | `fmtSize` + `fmtDateTime` + `fmtDurCn` |
| `TasksModule.tsx` | `fmtBytes` + `fmtTime` |
| `ServicesModule.tsx` | `fmtPct` |
| `DatabaseManager/ServerDashboardPanel.tsx` | `fmtSize` + `fmtByteRate` + `fmtPerSec` + `fmtNum` |
| `DatabaseManager/TableOverviewPanel.tsx` | `fmtSize` |
| `DatabaseManager/AuditPanel.tsx` | `timeAgo(t, true)` |
| `CicdModule.tsx` + `cicd/shared.tsx` | `fmtDur` + `fmtTime` + `fmtSize` |

**有意产生的显示变化**（都是「统一到两条字节规则」的必然结果，不是 bug）：

1. 字节小数位从各处的 1 位 / 混合，统一成 **2 位** —— `716.0 MB` → `716.00 MB`，`26.1 GiB` → `26.11 GiB`
2. 消除 `1024.0 KB` 这类边界 —— `1048575` 从 `1024.0 KB` 变成 `1.00 MB`
3. 时长秒数补零、超过 1 小时进位 —— `3m5s` → `3m05s`，`60m0s` → `1h00m`
4. 服务列表 `cpuPercent = 0` 从 `—` 变成 `0.00%`（`fmtPct` 不再把 0 当作"无值"）
5. 无值占位符统一成 `—`

**未纳入统一**（语义确实不同，保留本地实现）：
- `fmtUptime` —— `2d 5h`（总览表紧凑格）与 `2天 5时 30分`（仪表盘）是两种表达，不是重复
- `fmtRows` —— `1.5K` / `2.0M` 的行数缩写

## 硬性约定

1. **新代码必须用 L1/L2**。不要在模块里再写 `className="modal-overlay"` 手搭弹层，不要自己定义 `fmtBytes`/`fmtTime`/`fmtSize`。
2. **不要跨模块穿透引用**。`import ... from '../cicd/common'` 这种写法不允许——需要就提升到 L2。
   - ~~现存违规：`modules/LogMonitorModule.tsx` 里 `import { OptSelect } from './cicd/common'`~~ 已修复（2026-09-19）：OptSelect 提升为 `components/common/OptSelect.tsx`，三个消费方（LogMonitor/Cicd/TrafficTab）全部直连 L2。
3. **跨模块引用的东西只能来自 L1/L2**，不能来自 `modules/<别的模块>/`。
4. 修改样式前先读 `AGENTS.md` 的样式考古法：`index.css` 有 V1–V19 版本化覆盖段，同选择器多段定义会让老属性"残活"。

## 一个重要的样式约束

L1 的 `components/ui/` 用 **Tailwind 4**；老模块的类名（`modal-overlay`、`btn-glass-soft`）定义在 **`index.css`**（3685 行，含 V1-V19 覆盖段）。两套体系并存。

因此 L2 组件**沿用 `index.css` 已有类名实现**（见 `Modal.tsx`），而不是重写成 Tailwind——这样替换后视觉零变化，且不用碰 `index.css` 的历史覆盖段。

**迁移一个模块的 UI 时要么整块迁、要么不迁**，不要半迁，否则新旧体系混用会出现样式打架。
