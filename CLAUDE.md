# OpsCore 开发规约（每次会话自动生效）

## Skill 路由（改动前必读对应 skill，不要凭记忆）
- 修 bug / 调样式 / 重构 → `debug-and-refactor`：Part B.5 完整性核查（改一处必须横向枚举同类项一起改，收尾交同类项清单）
- 写代码 / 加功能 → `ponytail`：先查仓内已有实现(阶梯第2级)，重复第 2 次必须抽公共；状态色/行高等视觉常量只允许一份定义
- 前端布局 / 新页面 → `frontend-design` + 仓内既定规范：布局链 `lg:h-[calc(100vh-14rem)]`+卡片flex+hover-scroll 框内滚动+STICKY_THEAD 表头吸附；整页零滚动
- 不确定该用哪个 skill → `find-skills`

## 仓内硬规范（违反即返工）
- 状态色唯一权威：`web/src/modules/cicd/shared.tsx` 的 `STATUS_COLOR`（禁止再定义本地状态→色映射）
- 布局链六页签：流水线/运行历史/脚本库/仓库/凭据/概览（新增页签照抄同构）
- 提交纪律：feat/cicd 分支；构建(tsc+vite)通过才提交；构建产物(dist/exe)永不入库；推送前用户确认

## 已知坑
- Windows 下 python 写文件后立刻 vite build 会撞文件锁（首次构建静默失败）→ 构建失败先重跑一次再排查
- 裸 `grid` 类会被 index.css 的 legacy `.grid` 规则(components层)加 gap/margin → 需要显式 mb-0/gap 覆盖
