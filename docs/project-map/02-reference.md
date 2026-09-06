# 参考项目交互层 —— 倒模源清单

> 测绘对象: Kibana Discover/Logs、Grafana Explore、Loki Label Browser (前端交互层)。
> 后端实现 `🚧 边界外` (已有 `docs/log-monitor/loki-research.md`、`kibana-grafana-research.md` 覆盖架构)。

## R-01 Kibana Discover 字段值交互

每个结果行/抽屉里的**任意字段值**都支持：
- 点击值 → `Filter for value`（加为包含条件）
- 悬停弹出 `Filter for value / Filter out value / Copy / View details` 
- 字段名旁 `↓` 打开字段统计 (top 5 values + 分布图)

**这是 Kibana 交互密度最高的单点**。OpsCore 只支持 indexId/service 两个字段点击过滤。

## R-02 高亮命中词

搜索词在结果 message / 全部字段中被 ``<mark>`` 包裹高亮。
OpsCore: `kib-hl` class 已存在 (logmonitor-kibana.css L303-309)，但 **未接入搜索词**。

## R-03 时间直方图框选缩放

Discover 顶部直方图可**框选一段 → 自动缩放到该时间窗口**，并联动刷新。
OpsCore: 直方图纯展示 (hist)，无框选。

## R-04 字段统计/分布

任意字段 → 点开显示 top 5 值计数 + 迷你直方图。
OpsCore: `stats/terms` 后端已实现(L43)，**前端无入口**（这是典型的 🕳️ 未暴露）。

## R-05 Kibana Logs 上下文查看 (Show surrounding)

点某行日志 → "Show surrounding" → 弹出该行**前后 N 条**原始日志（同一 filter 下）。
OpsCore: 详情抽屉有字段但**无可查前后文入口** → 对应后端也没有 `/surrounding` 端点。

## R-06 级别着色

ERROR 红 / WARN 橙 / INFO 蓝 / DEBUG 灰。
OpsCore: `log-level` class 已有, `kib-lvl` 徽标已有，基本覆盖。

## R-07 保存查询 ⛔

Kibana 可把 KQL + 过滤 + 时间组合保存为一"已保存搜索"，下次一键载入。
OpsCore: **完全没有** 保存/历史/收藏任一功能。

## R-08/R-09 Grafana Explore Builder/Code + 多查询

- 双模式: 可视化构建器 vs 手写查询编辑器可切换。
- 多个查询并存，结果合并在同一时间轴。
OpsCore: 只有单一 KQL 输入，无 builder、无多查询。

## R-10 Grafana 面板联动

点击某面板数据 → 全局变量更新 → 其他面板联动刷新。
OpsCore: 统计页无联动。

## R-11 Loki Label Browser

输入标签名 → 列出该标签当前值 → 点击即过滤。
OpsCore: FIELD_TREE 已部分实现 (level/source 静态子项)，但**值来自硬编码**，非动态从数据统计。

## R-12 Grafana CSV 导出

表格式面板可整表导出 CSV。
OpsCore: **无任何导出**（前端无导出按钮，后端无导出端点）。

## R-13 单资源级导出（用户提议）✅

用户上一轮提议: 单个流水线/最小资源右键导出。扩展至日志 = **单索引导出 / 单个源日志导出**。

---

> 5 项已具备、8 项参考缺失。横向结论写入 `05-insights.md`。