# 下一批 CI/CD 功能实施蓝图

> 状态：已评审，待实施。本文档是唯一规格来源；实现按此逐步落地，每步独立提交 + E2E 验证。
> 会话续接：直接按本蓝图开工，无需追溯对话历史。

## 一、质量检查动作（Quality Action）—— 阶段 1

对标 Jenkins 测试聚合插件的最小实现：不造插件体系，只做一个「跑命令 → 收 JUnit 报告 → 按门槛判成败」的通用动作。

### 1.1 动作注册表（internal/cicd/actions.go 或 engine 的 ActionSpec 表）

新增 `quality` 类型，字段走现有 params 机制：

| 字段 | 默认 | 说明 |
|---|---|---|
| `command` | 必填 | 跑什么：`mvn test` / `npm test` / `go test -json` 等 |
| `reportGlob` | `target/**/TEST-*.xml` | JUnit XML 报告位置（生态事实标准：surefire/testng/jest/gotestsum） |
| `failOn.failed` | 0 | 失败用例数 > 该值 → 门禁失败 |
| `failOn.passRate` | - | 成功率 < 该值(%) → 门禁失败（-1 表示不启用） |
| `failOn.coverage` | - | 覆盖率 < 该值(%) → 门禁失败（阶段 2 起，需命令输出覆盖率） |

### 1.2 引擎侧（internal/cicd/）

- 步骤执行成功且该步为 `quality` → 追加一条远端命令：按 `reportGlob` 收集并解析 JUnit XML（Go `encoding/xml`，结构仅需 testsuite{name,tests,failures,errors,skipped,time} + testcase），跨文件求和。
- 比对 `failOn`：
  - 达标 → 步骤成功，`StepRun` 附加 `quality: {tests, failed, skipped, passRate}`（持久化）。
  - 不达标 → 步骤标 failed，错误信息带汇总（如 `质量门禁未通过: 2 失败 / 128 用例`）；沿用「失败即中止」既有语义 → 后续发布阶段自动不执行。
- 解析命令与测试命令分开执行（避免测试失败导致解析中断）。

### 1.3 前端（web/src/modules/CicdModule.tsx）

- 运行详情步骤行下方：有 `quality` 数据时渲染迷你条 `测试 128✓ 2✗ 3↷ (97%)`，失败红色点缀。
- 概览趋势线 → 阶段 2。

### 1.4 验证

- 单测：JUnit XML 解析（含 失败/跳过/多文件）。
- E2E：给 E2E-D（jar）插一个 `quality` 步骤（`mvn test` + surefire glob），先制造一次失败用例验证门禁拦截发布。

### 1.5 后续阶段

- 阶段 2：`coverage` 门槛（`go test -cover` / jacoco）+ 概览质量趋势线。
- 阶段 3：同一动作换命令即可复用为外部质检 CLI（sonar-scanner 等）收集器。

## 二、部署策略动作族（Deploy Strategy Actions）

### 2.1 概念分类（先对齐术语）

| 大类 | 策略 | 环境 | 切换 | 回滚 |
|---|---|---|---|---|
| 整切类 | 蓝绿（=红黑/红蓝） | 双份完整环境 | 一次性整切 | 秒切回 |
| 渐进类 | 金丝雀（=灰度） | 单份流量+权重 | 渐进放量 | 拉回权重 |
| 原地替换类 | 滚动 | 同批资源 | 逐个替换 | 再滚回去 |

### 2.2 原语（全部复用现有机制，零新框架）

| 原语 | 承载 |
|---|---|
| 双实例/双环境 | 主机 green/blue 目录、docker 两套容器名、k8s 两套 deployment（命名约定参数化） |
| 流量切换 | nginx upstream（weight= + reload）/ docker 端口 / k8s service selector |
| 探活验证 | 现有「部署验证」步骤模式（curl/健康检查循环） |
| 回切/回滚 | 现有 commit 钉住回滚 + 权重拉回 |

### 2.3 三个复合动作（动作注册表 `deploy-strategy` 族）

- `blue-green`：①部署到备环境（不碰当前流量）→ ②探活窗口（失败即中止=无需回切）→ ③切换流量+验证 → 旧环境标 `待回收`。另加 `rollback` 变体（流量切回旧环境）。
- `canary`：①金丝雀实例（weight=X）→ ②探活验证 → ③每轮通过 weight+=Y 再验证 → 100% 全量 → ④任一轮失败自动拉回权重。
- `rolling`：遍历主机清单，逐个「部署新 → 探活验证 → 下一个」，全部通过收尾。

### 2.4 实施顺序与验证

1. 蓝绿（最简单，双环境静态）→ E2E：nginx 两站点目录 + reload 切换。
2. 金丝雀 → E2E：nginx weight 阶梯 + 探活失败回切。
3. 滚动 → E2E：多主机逐个替换。

## 三、约束

- 每步独立提交到 feat/cicd，构建 + E2E 验证通过再推。
- 动作族不新增后端框架，只加动作类型与参数化；探活/回滚复用现有原语。
