# 前端子树 —— OpsCore 日志监控 5 功能簇

> 交互点全量清点 (LogMonitorModule.tsx, 1583 行)。每个点标注 `file:line` 证据。
> 树上每个交互点 = 一个"路面尽头"，标注其触发的后端调用。

## D1 Discover 检索页 (tab=search) `line 818-1116`

```
Discover 检索
├── 查询构建区
│   ├── 索引下拉 (F-05) → GET /api/logmonitor/indexes `L921`
│   ├── 时间范围 (F-06) 相对: 15m..30d `L932`
│   ├── Live tail (F-07) 每3s轮询 `L942`
│   ├── 搜索框 KQL (F-08) `L951-962`
│   │   ├── Enter→applyKql `L959`
│   │   └── 建议下拉 (F-09) FIELD_HINTS 4项 `L963-972` ⛔ 纯静态提示
│   ├── 查询按钮 (F-10) → GET query `L974`
│   └── 重置按钮 (F-10) 清空全部 `L975`
├── 已应用过滤 chips (F-11) 5个 `L979-987`
├── 字段树侧栏 (F-02~F-04) `L845-906`
│   ├── level [x]/[ ] 多选　→ query `L889-903`
│   ├── source [x]/[ ] 多选　→ query
│   ├── service / indexId / message 点击 → 展开或提示 `L866-868`
│   └── **⛔ 无字段搜索框、无字段类型标注、无字段统计入口**
├── 结果直方图 (hist) `L1024-1034` ⛔ 纯展示, 无框选缩放
├── 结果文档表 `L1035-1057`
│   ├── 行点击 (F-12) → 详情抽屉 → GET raw?id `L1036`
│   ├── 索引徽标点击 (F-13) → addFieldFilter(indexId) `L1043`
│   └── service 点击 (F-14) → addFieldFilter(service) `L1050`
├── 分页 (F-15) `L1060-1065` ⛔ 无每页条数 / 采样率
└── 详情抽屉 (F-16/F-17) `L1074-1116`
    ├── 字段值 → "同索引过滤" chip (F-16) `L1099`
    └── 表格/JSON 视图 (F-17) `L1109`
    └── **🪝 message 字段在抽屉里无"上下文查看"按钮**
```

## D2 统计总览页 (tab=stats) `L1276-1372`

```
统计总览
├── 级别条 (level bars) ⛔ 无点击下钻
├── 直方图 ⛔ 无框选缩放
├── 服务下拉 + 加载按钮 (F-20) → GET stats `L1290-1292`
└── 顶部/来源统计 ⛔ 无字段分布详情
```

## D3 日志源管理页 (tab=sources) `L1118-1150`

```
日志源管理
├── 新增日志源 (F-21) 弹层 → POST sources/save `L1118`
└── 源列表 + 删除 (F-21) → POST sources/delete `L1145`
```

## D4 索引与 ILM (tab=indexes) `L1376-1510`

```
索引与ILM
├── 索引列表 (名称/文档数/字节/阶段/保留/操作) `L1388-1421`
│   ├── 编辑 (F-29) `L1414`
│   └── 删除 (F-29) `L1415`
├── 索引编辑 (editing) `L1426-1510`
│   ├── 基础: 名称/来源/路径/服务/保留 `L1436-1450`
│   ├── ILM 四阶段 hot→delete (F-30) 保留/只读/压缩/冻结/优先级 `L1452-1475`
│   └── 字段映射 (F-31) 增删/类型/索引开关 `L1476-1510`
```

## D5 发现采集 (K8S/容器/Pod) `L1152-1275`

```
发现采集
├── 容器发现面板 `L1152-1200`
│   ├── 开关/收起 (F-22) → GET discover/containers
│   ├── 目标索引下拉 (默认 file) `L1177`
│   ├── 容器全选 (F-23) / checkbox 勾选 `L1188-1196`
├── Pod 级联发现 `L1200-1270`
│   ├── 集群下拉 (F-24) → GET discover/clusters → 联动 `L1219`
│   ├── 命名空间下拉 (F-25) → GET discover/pods `L1223`
│   ├── 搜索框 (F-26) 本地过滤 `L1232`
│   └── Pod 全选/勾选 (F-27) `L1259`
└── 采集入库 (F-28) → POST ingest `L1273`
```

## 命令/注册标记

- 前端入口: `App.tsx` 路由注册 logmonitor
- 交互标记: React `onClick/onChange/onKeyDown` (68 处)
- 出站标记: `getJSON/postJSON` from `../api/client` (15 端点)