# OpsCore 项目记忆（AGENTS.md）

> 由 project-memory-sculptor 工作流维护：`[待确认]` 提案经用户批准后提升为 `[已生效]`。
> 每次会话启动自动读取；违反 [已生效] 规则即返工。

## [已生效]

### 前端规范
- 规则：改一处视觉样式（尺寸/间距/行高/颜色/图标）必须横向枚举同布局全部同类项一起改，收尾交同类项清单。
- 证据：2026-09 卡片间距/行高两轮返工，用户两次纠正"要看同类项"。

### 前端规范
- 规则：布局链六页签（流水线/运行历史/脚本库/仓库/凭据/概览）统一 `lg:h-[calc(100vh-14rem)]` + 卡片 flex 填充 + hover-scroll 框内滚动 + STICKY_THEAD 表头吸附，整页零滚动；新增页签照抄同构。
- 证据：e4df966 同类项核查；新增页签漏根容器曾致双卡贴死(513413d)。

### 工程规范
- 规则：状态→色映射唯一权威 = `web/src/modules/cicd/shared.tsx` 的 `STATUS_COLOR`；禁止在页面内再定义本地状态色映射。
- 证据：曾 4 份重复定义且 pending 值互相不一致，已收敛(1d22be3)。

### 工程规范
- 规则：提交纪律 —— 工作分支 feat/cicd；构建通过才提交；构建产物(dist/exe)永不入库；用户要求"只提交本地"时不推送。
- 证据：用户多次明确要求。

### 已知坑
- 规则：Windows 下 python 写文件后立刻 `vite build` 会撞文件锁静默失败——构建无 ✓ built 输出时先重跑一次再排查。
- 证据：多次"改了没生效"实为旧产物；一次把失败构建误提交。
- 规则：index.css 的 legacy `.grid` 规则(components层)会给所有 `grid` 类元素加 gap/margin——用 `grid` 类的布局容器需显式 `mb-0`/gap 覆盖。
- 证据：概览/流水线卡片 25px 死区间根因(abfd138/f7845e0)。

## Skill 路由（改动前读对应 skill）
- 修 bug / 调样式 / 重构 → `debug-and-refactor`
- 写代码 / 加功能 → `ponytail`（阶梯：仓内已有→标准库→平台原生→已装依赖→一行→最小实现）
- 前端布局 / 新页面 → `frontend-design` + 上方前端规范
- 不确定 → `find-skills`
- 经验沉淀 / 修改本文件 → `project-memory-sculptor`

## [待确认]

（暂无。新经验由 sculpt.py propose --write 进入本区，批准后上移。）
