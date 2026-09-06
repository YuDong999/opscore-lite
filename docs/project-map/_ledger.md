# OpsCore 日志监控模块 —— 测绘台账 (Traversal Ledger)

> 测绘方式: **倒模模式 (参考开源日志项目 → 找缺口)** + **体检模式 (自身前后端闭环)**
> 当前清单: 本项目 logmonitor 模块全量 + 参考项目交互层
> 状态图例: ✅ 已测绘 ｜ ⛔ 死胡同 ｜ 🚧 待测绘 ｜ 🕳️ 未暴露 ｜ 🪝 悬空调用

## 核心对象

| ID | 对象 | 状态 | 说明 |
|----|------|------|------|
| CORE-1 | Discover 检索页 (search tab) | ✅ 已测绘 | 查询 + 字段树 + 结果表 + 详情抽屉 |
| CORE-2 | 统计总览页 (stats tab) | ✅ 已测绘 | 聚合 + 图表 |
| CORE-3 | 日志源管理页 (sources tab) | ✅ 已测绘 | 源 CRUD |
| CORE-4 | 索引与 ILM (indexes tab) | ✅ 已测绘 | 索引 CRUD + ILM 策略 |
| CORE-5 | K8S Pod / 容器发现采集 | ✅ 已测绘 | discover + ingest |
| CORE-6 | 扫描文件入库 | ✅ 已测绘 | scan 弹层 |

## 后端 API 面 (logmonitor 插件)

| ID | 端点 | 状态 | 前端消费 |
|----|------|------|---------|
| API-01 | GET /api/logmonitor/query | ✅ 已测绘 | CORE-1 查询 |
| API-02 | GET /api/logmonitor/stats | ✅ 已测绘 | CORE-1 直方图 / CORE-2 |
| API-03 | GET /api/logmonitor/stats/terms | ✅ 已测绘 | 字段聚合 |
| API-04 | GET /api/logmonitor/raw?id= | ✅ 已测绘 | CORE-1 详情抽屉 |
| API-05 | GET/POST /api/logmonitor/sources | ✅ 已测绘 | CORE-3 |
| API-06 | POST /api/logmonitor/sources/save | ✅ 已测绘 | CORE-3 |
| API-07 | POST /api/logmonitor/sources/delete | ✅ 已测绘 | CORE-3 |
| API-08 | POST /api/logmonitor/scan | ✅ 已测绘 | CORE-6 |
| API-09 | POST /api/logmonitor/delete | ✅ 已测绘 | 批量删除日志 |
| API-10 | GET/POST /api/logmonitor/indexes | ✅ 已测绘 | CORE-4 |
| API-11 | POST /api/logmonitor/indexes/save | ✅ 已测绘 | CORE-4 |
| API-12 | GET /api/logmonitor/indexes/get | ✅ 已测绘 | CORE-4 |
| API-13 | POST /api/logmonitor/indexes/delete | ✅ 已测绘 | CORE-4 |
| API-14 | GET /api/logmonitor/indexes/stats | ✅ 已测绘 | CORE-4 |
| API-15 | POST /api/logmonitor/ilm/run | ✅ 已测绘 | CORE-4 / 顶栏 |
| API-16 | GET /api/logmonitor/discover/containers | ✅ 已测绘 | CORE-5 |
| API-17 | GET /api/logmonitor/discover/clusters | ✅ 已测绘 | CORE-5 |
| API-18 | POST /api/logmonitor/ingest | ✅ 已测绘 | CORE-5 |
| API-19 | GET /api/logmonitor/discover/pods | 🚧 待测绘 | 已在后端注册(lines 55-56) |
| API-20 | POST /api/logmonitor/discover/pods | 🚧 待测绘 | 已在后端注册 |

## 前端交互点簇 (已测绘)

| ID | 交互点 | 所属 | 后端调用 |
|----|--------|------|---------|
| F-01 | tab 切换 ×5 | 外壳 | 无 |
| F-02 | 字段树 level 多选 | CORE-1 | query |
| F-03 | 字段树 source 多选 | CORE-1 | query |
| F-04 | 字段树 service/indexId/message 点击 | CORE-1 | query |
| F-05 | 索引下拉 | CORE-1 | indexes |
| F-06 | 时间范围下拉(相对) | CORE-1 | query |
| F-07 | Live tail 开关 | CORE-1 | query 轮询 |
| F-08 | 搜索框 KQL 输入 | CORE-1 | query |
| F-09 | 搜索建议下拉 (FIELD_HINTS) | CORE-1 | 无 |
| F-10 | 查询 / 重置按钮 | CORE-1 | query |
| F-11 | 已应用过滤 chips ×5 | CORE-1 | query |
| F-12 | 结果行点击 → 详情抽屉 | CORE-1 | raw |
| F-13 | 结果行字段点击 → 快速过滤 (indexId/service) | CORE-1 | query |
| F-14 | 结果行 service 点击 | CORE-1 | query |
| F-15 | 分页 上一页/下一页 | CORE-1 | query |
| F-16 | 详情抽屉字段 → 同索引过滤 | CORE-1 | query |
| F-17 | 详情抽屉 JSON/表视图 | CORE-1 | 无 |
| F-18 | 顶栏扫描文件入库 | 外壳 | scan |
| F-19 | 顶栏执行 ILM 清理 | 外壳 | ilm/run |
| F-20 | 统计服务下拉 + 加载 | CORE-2 | stats |
| F-21 | 日志源新增/删除 | CORE-3 | sources/save+delete |
| F-22 | K8S/容器发现面板开关 | CORE-5 | discover |
| F-23 | 容器全选/勾选 | CORE-5 | 无 |
| F-24 | Pod 集群下拉 (级联) | CORE-5 | discover/clusters |
| F-25 | Pod 命名空间下拉 (级联) | CORE-5 | discover/pods |
| F-26 | Pod 搜索框 | CORE-5 | 本地过滤 |
| F-27 | Pod 全选/勾选/清空 | CORE-5 | 无 |
| F-28 | 采集入库按钮 | CORE-5 | ingest |
| F-29 | 索引新增/编辑/删除 | CORE-4 | indexes/* |
| F-30 | ILM 阶段编辑 (hot/warm/cold/delete) | CORE-4 | indexes/save |
| F-31 | 字段映射编辑 | CORE-4 | indexes/save |
| F-32 | 扫描弹层 | CORE-6 | scan |

## 参考项目交互层 (倒模源)

| ID | 参考对象 | 状态 |
|----|---------|------|
| R-01 | Kibana Discover: 字段值点击→加列/过滤/inclusion/exclusion | ✅ 已测绘 |
| R-02 | Kibana Discover: 高亮命中词 | ✅ 已测绘 |
| R-03 | Kibana Discover: 时间直方图框选缩放 | ✅ 已测绘 |
| R-04 | Kibana Discover: 字段统计/分布 | ✅ 已测绘 |
| R-05 | Kibana Logs: 上下文查看 (Show surrounding) | ✅ 已测绘 |
| R-06 | Kibana Logs: 级别着色 ERROR红/WARN橙/INFO蓝 | ✅ 已测绘 |
| R-07 | Kibana: 保存查询 / 保存搜索 | ⛔ 死胡同(见 02-frontend) |
| R-08 | Grafana Explore: Builder/Code 双模式 | ✅ 已测绘 |
| R-09 | Grafana Explore: 多查询并存合并 | ✅ 已测绘 |
| R-10 | Grafana: 面板间联动 | ✅ 已测绘 |
| R-11 | Loki: 标签点击即过滤 (label browser) | ✅ 已测绘 |
| R-12 | Grafana: 导出 CSV (表数据) | ✅ 已测绘 |
| R-13 | CI/CD 流水线右键导出 | ✅ 已在上一轮讨论 |

## 闭环状态

- [ ] 台账无 🚧 (剩余 API-19/20 后端确认中)
- [ ] 映射矩阵无未解释 🕳️/🪝
- [ ] 功能流到达后端函数 (已确认 15 端点)