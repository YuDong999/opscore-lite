# AGENTS.md — opscore-lite

## [项目家风 - 状态优先行动协议]

### 行动力要强，破坏力为零

1. 任何任务第一步必须用只读工具获取状态（read/ls/grep/git log/curl），禁止不侦察就下结论或拒绝。
2. 说"不能/做不到"之前，必须先只读自查现状；拒绝时必须附上自查证据。
3. 纯只读操作永不视为"危险"，不需要额外授权。
4. 写/删/改/重启类操作执行前必须自检：影响范围（量化）→ 可逆性（有回滚）→ 环境隔离（生产/VM 分支部署前确认归属）。

## [已生效]

- 暂空

## [待确认]

- 暂空
- [工程纪律]：实现新功能前必须回查 docs/dbmanager-三方对比矩阵-2026-09.md 台账——参考项目能力清单已探查完毕(含源码位置)，不等问题驱动（使用条件：数据库管理模块；证据：dbx 服务器仪表盘报告里有但实现时没回查，跟着用户给的词 grep 导致漏做）
- [前端工程]：index.css 有 V1-V19 版本化覆盖段，同选择器历史多段定义会属性残活——改样式先 grep 全部定义块逐属性比对（使用条件：web/src/index.css；证据：margin-bottom/margin-top 残留导致按钮留白不对称三轮排查）
- [前端工程]：弹层/面板内容出视口或被截断，先查祖先链 flex:1/min-height:0/overflow 三要素是否齐全（使用条件：数据库管理模块布局；证据：查询页编辑条被推出视口根因是 db-query-section 未参与 flex 高度链）
- [后端契约]：Go 结构体序列化字段必须加 json tag 小写——默认大写驼峰前端读不到；MySQL 驱动信息库列名大写也需兼容（使用条件：internal/dbmanager；证据：FkInfo 无 tag 导致 ER 图连线全丢；table-counts 大小写兼容修复）
## [安全红线 - 不可触碰区域]

- 平台 Windows：禁止未确认就 taskkill 非本项目进程；VM 部署前必须确认分支归属（VM=容器管理分支，本地 dev/dbmanager=数据库分支，互不覆盖）
- 禁止自动执行数据库 DELETE / DROP（必须人工确认；E2E 验证前先 /write-unlock 限时解锁）
- 禁止直接 push 到 main；VM 的 /opt/opscore 运行实例未经确认不得替换

## 项目概览

- **opscore-lite**: 轻量运维控制台。React 18 + TS + Vite(前端) / Go 单二进制(后端)。
- **分支约定**: `dev/dbmanager` = 数据库管理模块开发分支(本地主力); VM(192.168.207.10) 上 `/opt/opscore-lite` 是**容器管理分支**(`feat/k8s-action-catalog`)——**两分支互不覆盖**, 部署前先确认分支归属。
- **参考项目**: `C:/Users/31807/.zcode/workspace/default/ref/` 下有 dbx(Vue3, 主要倒模对象)与 GoNavi(React+Antd)完整源码。

## 数据库管理模块(当前主战场)

- 前端: `web/src/components/DatabaseManager/`(27 组件) + `modules/DatabaseManagerModule.tsx`
- 后端: `internal/dbmanager/`(handlers/service/pool + gonavi 驱动底座 + sync 同步引擎)
- **能力台账(开发 backlog)**: `docs/dbmanager-三方对比矩阵-2026-09.md`
  —— 一切新功能先查此表, 完成即更新状态; 剩余批次 A(小活)/B(树子组)/C(批量编辑+任务库)/D(低优先) 见表内批次标注。
- **设计基准**: `docs/dbmanager-sync-matrix.md`(同步层级组合表)

## 关键工程要点(踩过的坑)

1. **构建**: `cd web && npm run build` 产物由 Go 托管(改前端必须重建才在 8088 生效); 实时预览用 `npm run dev`(5173, proxy /api→8088)。
2. **SQLite 驱动**: 编译需 `-tags gonavi_sqlite_driver`(内嵌 modernc 纯 Go 实现); 其他可选驱动默认走 driver-agent 模式(未部署)。
3. **样式覆盖段**: `web/src/index.css` 尾部有 V1–V19 版本化覆盖段。同选择器历史多段定义, **老块属性会残活**——改样式先 grep 全部定义(样式考古法, 见 skill: project-cartographer/references/ui-layout-debug-method.md)。
4. **flex 高度链**: 弹层/面板内容"出视口/被截"先查祖先链 `flex:1/min-height:0/overflow` 三要素。
5. **写操作护栏**: SQL 写操作默认只读拦截, 需 `/api/dbmanager/write-unlock` 限时解锁; E2E 验证写路径前先解锁。
6. **图标纪律**: 禁用字形类字符(字体缺字形渲染成月牙), 一律内联 SVG。
7. **双人同仓**: opencode 可能同时在同仓工作——提交前 `git log --oneline -3` 看有没有别人的新提交, rebase 而非 merge; `--theirs/--ours` 在 rebase 语境语义反转, 用前想清楚。

## 方法论沉淀

- 探查/倒模: skill `project-cartographer`(菜单全集发现法/结构机制提取法/运行时验证)
- 调试: skill `debug-and-refactor`(Part E 高频根因速查: 占位端点审计/flex 高度链/样式考古)
