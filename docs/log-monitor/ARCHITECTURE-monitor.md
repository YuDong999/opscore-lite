# OpsCore 监控模块 —— 结构化归档文档（本期仅文档，不开发代码）

> 定位：监控 = 对标 **Prometheus + Grafana**（指标采集/存储/可视化/告警）与 **Zabbix**（主机/网络监控、告警）。资源监控数据已由现有 `resources` 模块采集(CPU/内存/磁盘/网络)。
> **本期明确不做代码实现**，本文件作为结构化需求/架构蓝图沉淀在项目内，供后续规划开发。

---

## 一、目标与范围（文档化范围）

- 替代/补充常用开源监控栈（Prometheus + Grafana、Zabbix）的 **UI 与告警编排** 能力。
- 底层指标数据来源：现有 `resources` 模块（主机 CPU/内存/磁盘/网络）。
- 面向未来：容器/多集群指标、日志转指标。

---

## 二、功能区（对标 Grafana Dashboard / Zabbix）

### 2.1 指标面板（Dashboard）

对标 Grafana 24 列网格系统的"面板网格"：

| 能力 | 说明 | 对标 |
|------|------|------|
| 面板网格 | 24 列网格, 面板占 n 列, 可拖拽/分组(Row)/全屏 | Grafana Dashboard |
| 面板类型 | timeseries / table / gauge / stat / barchart / logs | Grafana panel type |
| 变量 | 模板变量 `$service` `$host` | Grafana template variables |
| 面板联动 | 点击某面板 → 联动过滤其他面板 | Grafana interlink |
| 布局 | 分组标题折叠, 全局时间范围 | - |

### 2.2 指标数据模型

```jsonc
{
  "metric": "host.cpu.usage",
  "labels": { "host": "app-01", "env": "prod" },
  "value": 32.5,          // gauge 当前值
  "timestamp": 1725300000000
}
```

- **Gauge** 型：内存/磁盘使用率、连接数。
- **Counter/Rate** 型：CPU 累计/速率、网络流量字节。
- 每主机最新快照 + 历史时序（按时间桶聚合 avg/max/min）。

### 2.3 告警（对标 Prometheus Alerting / Zabbix）

| 组件 | 对标 Prometheus/Grafana | 说明 |
|------|------------------------|------|
| 告警规则 | AlertRule | 查询 + 阈值条件 + 周期 |
| 状态机 | pending → firing → resolved | 触发/静默 |
| 通知路由 | route | 路由到不同联系渠道 |
| 联系方式 | contact point | 邮件 / Slack / DingTalk / Webhook |

```jsonc
{
  "rule": {
    "name": "CPU 高负载",
    "query": { "metric": "host.cpu.usage", "agg": "avg", "window": "5m" },
    "condition": { "op": "gt", "threshold": 90, "for": "10m" },
    "state": "pending|firing|resolved"
  },
  "notify": { "channels": ["email-ops", "webhook-dingtalk"] }
}
```

---

## 三、数据来源

| 来源 | 类型 | 是否已有 | 说明 |
|------|------|---------|------|
| resources 模块 | 主机指标 | ✅ 已有 | CPU/内存/磁盘/网络 |
| 容器指标 | 容器运行时 | 未来 | 复用容器模块 |
| 多集群指标 | agent 采集 | 未来 | |
| **日志转指标** | logmonitor 聚合 | 未来 | 由日志统计出错误率/请求量告警 → 打通日志+监控 |

---

## 四、与日志模块的关系

- **共用前端外壳**：LogMonitor 主模块两个 tab ——「日志」和「监控」。监控 tab 占位，当前仅显示"规划中"说明或直接隐藏。
- **日志转指标**：日志的 histogram/stats 可转换为监控指标（如每分钟 ERROR 数），是未来打通两类的桥梁。
- **数据源抽象**：监控面板渲染与数据源解耦（同 Grafana `query→frame` 思想），未来可接 Prometheus。

---

## 五、实现优先级（未来开发时参考）

1. **P0**：指标面板网格 + resources 指标数据源 + timeseries/stat/gauge 面板。
2. **P1**：告警规则 + 状态机 + 邮件/Slack/Webhook 通知。
3. **P2**：日志转指标, 容器/多集群指标, 变量联动。

> 本期不实现以上任何代码；资源监控仅以本文档形式沉淀。默认隐藏监控 tab，或显示"监控规划中"。
