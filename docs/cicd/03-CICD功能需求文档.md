# OpsCore CI/CD 模块功能需求文档

> 版本 v2.0 · 更新于 2026-09-03
> 配套: [02-CICD构建文档.md](./02-CICD构建文档.md)(技术实现) / [01-开源CI-CD项目调研分析.md](./01-开源CI-CD项目调研分析.md)(取舍依据)
> v2 增补: FR-13 凭据中心 / FR-14 代码仓库 / FR-15 镜像仓库 / FR-16 发布模板(K8s·Docker·裸机) / FR-17 脚本库 / FR-18 环节进度
> v2.1 增补: FR-19 阶段审批门禁
> v2.2 增补: FR-20 制品收集与下载
> v2.3 增补: FR-21 制品分发(拉取到目标主机)
> 优先级: P0 = 必须, P2 = 未来版本

---

## 一、定位与使用场景

OpsCore 的 CI/CD 模块走"精简链路"(对标 Jenkins 做减法): **代码库 → 构建 → 镜像仓库 → 发布(K8s/Docker/裸机)**, 配上凭据、脚本库、全程进度与日志。面向"运维/开发一体"的中小团队:

- **场景 A — 多机构建**: 连接代码库自动拉取, 在构建机编译测试, 每个环节实时进度+日志。
- **场景 B — 镜像发布**: docker build → push 到镜像仓库 → 目标 Docker 主机拉取重建容器。
- **场景 C — K8s 发布**: kubectl apply + rollout status, kubeconfig 凭据自动下发目标主机。
- **场景 D — 裸机发布**: 产物分发到裸机, 重启服务并做健康检查。
- **场景 E — 仓库联动/例行任务**: Git Webhook push 自动触发; cron 夜间构建巡检。

---

## 二、功能需求清单

### FR-01 流水线定义与管理 (P0)

1. 字段约束: 名称必填(1-64, 不含 `<>{}`, 唯一); 描述 ≤256; 整体超时 0-1440 分钟(0=不限); 历史保留 maxRuns 5-500(默认 50)。
2. 保存生成稳定 ID(`pl-`+6 位); 列表展示名称/描述/阶段数/触发器徽标/最近一次运行(状态+耗时)/更新时间。
3. 删除需二次确认; 运行中/排队拒绝(409); 删除同时清理历史与日志文件。
4. 编辑 modal 表单化; 前后端双重校验, 非法输入明确报错且不落盘。
5. 支持复制流水线(名称加"副本", secret 重新生成)。

### FR-02 阶段(Stage)编排 (P0)

1. 阶段字段: 名称(必填 ≤64)、目标主机(本机或 Ansible 清单主机, 下拉)、工作目录(可选)、步骤列表(≥1)。
2. 阶段顺序执行; 支持增删与上下移。
3. 失败语义: 前一阶段 failed/canceled → 后续阶段全部 skipped。

### FR-03 步骤(Step)编排 (P0)

1. 步骤字段: 名称(必填 ≤64)、命令(必填 ≤8KB, POSIX shell, 经 `sh -c` 执行)、步骤超时(0=不限)、失败继续 continueOnFail。
2. 失败语义: 退出码非 0 且未开启 continueOnFail → 本步 failed, 同阶段后续步骤 skipped, 阶段 failed; **开启 continueOnFail → 本步仍标记 failed(告警可见)但不阻断阶段与流水线**(GitLab `allow_failure` 语义)。
3. 命令可引用注入的环境变量(FR-05/FR-13~15); 本机进程内逐行流式输出, 远程 SSH 单会话(参数单引号转义防注入)。
4. 支持增删与上下移; 状态: pending/running/success/failed/skipped/canceled。

### FR-04 执行引擎与运行控制 (P0)

1. 手动触发(可关闭); 同一流水线串行(已有 queued/running 拒绝, 409); 全局并发默认 2(`OPCORE_CICD_MAXRUNS` 可调 1-16), 其余排队(队列 64, 满则拒绝)。
2. 取消: 排队中直接 canceled; 运行中"取消中"呈现, 本机步骤 kill 进程, 远程步骤放弃等待, 后续全部 skipped。
3. 每步记录开始/结束/耗时/退出码; 运行记录含流水线名称快照。
4. 服务重启: 孤儿运行(queued/running)自动标记 failed("服务重启中断"), 历史不丢。
5. 保留策略: 每流水线按 maxRuns 滚动裁剪, 同步删除日志文件。
6. 运行结束可向 notifyURL POST 结果 JSON(10s 超时, 不重试, 不含日志全文与 secret)。

### FR-05 环境变量 (P0)

1. 三元组: 名称(≤64, 无空白)/值(≤4096)/是否敏感; 支持增删行。
2. 注入: 本机 cmd.Env 追加; 远程 `export NAME='value';` 转义拼接; 不覆盖系统环境(本机保留 os.Environ)。
3. 敏感值保护: 日志逐行掩码(值 ≥4 字符, 掩为 `******`); 列表/历史接口只回 `******`; 仅编辑详情返回真实值。

### FR-06 触发器 (P0)

1. **手动**: 默认开启可关闭。
2. **Webhook**: `POST /api/cicd/webhook/{pipelineId}`; 凭证经 X-Opscore-Token 头 / `?token=` / body.secret 任一; constant-time 比较; 401(缺)/403(错/未启用)/404; 兼容任意 JSON body(通用 Git webhook); UI 提供触发地址/凭证/curl 示例一键复制; 凭证保存时自动生成 32 位随机串, 可重新生成。
3. **cron**: 5 字段(分 时 日 月 周), 支持 `*` `*/n` `a-b` `a,b`; 保存时校验; 每分钟对齐扫描; 同分钟去重; 错过不补跑; UI 展示人类可读描述。
4. 运行历史标记触发来源 manual/webhook/cron。

### FR-07 日志与实时观测 (P0)

1. 日志文件 `data/cicd/logs/<runId>.log`, 行带毫秒时间戳, 步骤分隔头(阶段/步骤/主机)。
2. SSE(POST `/api/cicd/run/stream`): `status`(运行/阶段/步骤状态)/`log`(增量行)/`done` 三类帧; 终态补发后结束; 客户端断开即停。
3. 断线恢复: GET `/api/cicd/run/log?id=&offset=` 全量/增量回填。
4. 日志面板等宽字体、自动滚动(用户上滚暂停); 历史日志可回看; 删除流水线/裁剪历史同步删文件。

### FR-08 运行历史与概览 (P0)

1. 历史 tab: 流水线/触发/状态/**进度条**/开始时间/耗时/操作, 按流水线过滤, limit 分页。
2. 运行详情: 头部(状态/耗时/触发/取消按钮) + **进度条** + 阶段时间线(主机/工作目录/每步状态与退出码/耗时) + 日志面板。
3. 概览: 流水线总数/运行中·排队/24h 成功/24h 失败/最近 10 次运行。

### FR-09 通知 (P1)

见 FR-04 第 6 条; 通知 JSON: runId/pipelineId/pipeline/status/durationMs/trigger/finishedAt/error。

### FR-10 主机分发与安全执行 (P0)

1. 远程步骤经 SSH 连接池单会话, 参数转义防注入, 传输故障自动换新连接重试一次。
2. 阶段主机 ID 仅接受清单存在的主机; 所有写操作 POST; 模块遵循全局 Bearer 认证(Webhook 为唯一豁免端点, 自身凭证保障)。

### FR-11 流水线复制 (P1)

列表"复制"操作: 以源流水线为蓝本新建(名称+"副本", secret 重新生成)。

### FR-19 阶段审批门禁 (P0, v2.1)

**描述**: 发布类阶段执行前强制暂停, 等待人工批准 —— 对标 Jenkins `input` / GitLab `manual job`, 是发布安全的核心门禁。

**详细需求**:

1. 阶段增加"执行前需审批"开关(`approval`), 编辑器阶段卡内勾选。
2. 开启后运行到该阶段时: 阶段状态置 `waiting`(等待审批), 运行保持 running, 日志记录"等待人工审批"。
3. 审批操作在运行详情页: waiting 阶段卡片显示"✓ 批准执行 / ✗ 拒绝"按钮(仅运行中可见); API 为 POST /api/cicd/run/approve {runId, approve}。
4. 批准 → 阶段立即继续执行; 拒绝 → 该阶段 canceled、后续阶段 skipped、运行标记 canceled(错误信息"阶段 %q 人工拒绝")。
5. 等待期间不占用全局并发名额(引擎让出槽位, 批准后重新竞争), 其他流水线不受阻塞。
6. 等待中取消运行 → 与拒绝等效(阶段取消)。无超时限制(等待可以过夜)。
7. 审批无独立用户体系, 与全站一致使用全局 Token; 日志记录批准/拒绝动作时间。
8. 验收: 两阶段流水线(构建/带审批的发布)运行到第二阶段暂停; 批准后继续且运行 success; 拒绝后运行 canceled 且第三阶段 skipped; 等待期间另一条流水线可正常并发执行。

### FR-20 制品收集与下载 (P0, v2.2)

**描述**: 构建产物自动归档到服务端, 运行详情一键下载 —— 构建机与部署机分离场景的刚需(发布阶段/人工取用)。

**详细需求**:

1. 步骤声明制品路径(编辑器步骤行"📦 制品"输入, 逗号分隔多个, 支持 `* ? [ ]` 通配, 相对阶段工作目录)。
2. 收集时机: 步骤**成功后**自动执行; 失败/跳过/取消的步骤不收集。
3. 归档格式: 每步骤一个 tar.gz, 命名 `s<阶段序号>-step<步骤序号>.tar.gz`, 存 `data/cicd/artifacts/<runID>/`; 归档内保留相对路径; 只收集普通文件(目录需用户自行打包)。
4. 本机步骤: 纯 Go 归档; 远程步骤: 先 `du` 量体积再 `tar|base64` 传输, 单步上限 100MB, 超限/未匹配在日志中明确提示(warn/error)且不影响运行结果。
5. 展示与下载: 运行详情步骤行显示 `📦 大小` 按钮(悬停显示声明路径与文件名), 点击经 `/api/cicd/artifact/download?run=&file=` 下载(支持 ?token= 配合浏览器)。
6. 安全: 制品路径白名单(禁 shell 元字符); 下载路径严格校验防穿越; 制品受全局 Token 保护。
7. 清理: 删除流水线 / 历史滚动裁剪时同步删除制品; 运行记录保留制品清单(步骤/文件/大小/路径)。
8. 验收: `echo built > dist/app.js` + 制品 `dist/*` 的本机流水线运行后, 详情出现下载按钮, 下载得到合法 tar.gz 且含 dist/app.js 与 dist/style.css; 穿越请求(`run=../x` / `file=../x`)返回 404。

### FR-21 制品分发(拉取到目标主机) (P0, v2.3)

**描述**: 部署阶段的步骤可声明"拉取制品", 运行时把同次运行中已收集的制品**推送到该步骤目标主机的工作目录**, 命令中用 `$CICD_ARTIFACT` 引用 —— 构建/部署主机分离时无需任何手工中转, 流水线内闭环。

**详细需求**:

1. 步骤声明 `拉取制品`: 编辑器下拉枚举同流水线中**当前步骤之前**声明了制品的步骤(跨阶段+同阶段在前), 值为确定性文件名 `s<阶段>-step<步骤>.tar.gz`。
2. 执行时机: 步骤命令执行**前**推送; 本机阶段直接复制, 远程阶段经 SSH stdin(`cat > 目标路径`)写入目标主机工作目录。
3. 命令引用: 推送成功后向该步骤注入环境变量 `CICD_ARTIFACT=<制品文件名>`, 典型用法 `tar xzf $CICD_ARTIFACT -C . && ./deploy.sh`。
4. 失败语义: 制品不存在(未在更早步骤收集/收集失败)或推送失败 → 本步骤 failed(退出码 -1), 后续按 continueOnFail 与阶段失败规则处理; **不执行步骤命令**。
5. 校验: 保存时校验拉取文件名格式(`s\d+-step\d+\.tar\.gz`), 防路径注入。
6. 约束: 仅支持引用**同一次运行**内更早步骤的制品(跨流水线分发属未来规划); 制品体积受 FR-20 的 100MB 上限约束。
7. 验收: 两阶段流水线(本机构建+收集 → 本机/远程拉取)运行成功, 目标工作目录出现制品文件, 日志有 📥 分发行; 引用不存在制品时步骤 failed 且命令未执行。

### FR-12 未来规划 (P2)

阶段手动审批 / 制品收集与下载 / 步骤级 when 条件与 matrix / 并行阶段 DAG / 通知渠道扩展 / 流水线导入导出与模板库 / git commit 信息展示。

---

## 三、v2 增补需求(精简 CI/CD 链路)

### FR-13 凭据中心 (P0)

**描述**: 集中管理流水线所需的外部凭据, 类型化注入, 仅写不读。

1. 四类凭据: `git`(代码库 token/密码, 可带用户名) / `registry`(镜像仓库用户名+密码) / `kubeconfig`(K8s 集群配置全文) / `generic`(通用密文)。
2. 字段: 名称(唯一)、类型(创建后不可改)、用户名、密文 Data、备注、更新时间。
3. **安全**: Data 保存后不可回读(编辑留空=保持原值); 列表接口只回 `hasData: true` 标记; 日志自动掩码; 全站仅全局 Token 认证, 凭据管理同受保护。
4. 注入方式(流水线级下拉引用):
   - git 凭据(经代码仓库绑定) → `GIT_REPO_USER` / `GIT_REPO_TOKEN`(secret);
   - registry 凭据(经镜像仓库绑定) → `REGISTRY_USER` / `REGISTRY_PASS`(secret);
   - kubeconfig 凭据(经流水线直接绑定) → 写入目标主机 `/tmp/.opscore-kubeconfig-<plID>.yaml`(600 权限), 注入 `KUBECONFIG` 环境变量, 运行结束清理。
5. 删除凭据: 已引用它的仓库/流水线运行时按"无凭据"降级并记日志。
6. 验收: 保存后列表抓包无明文; 运行日志中 `echo $GIT_REPO_TOKEN` 显示 `******`。

### FR-14 代码仓库连接与自动拉取 (P0)

**描述**: 维护代码库地址与凭据, 流水线一键引用, 首阶段自动拉取代码。

1. 仓库字段: 名称(唯一)、地址(https:// 或 git@/ssh:// 形态)、访问凭据(git 类型, 可空=公开库或主机 ssh key)、默认分支(默认 master)、备注。
2. **连通性测试**: 服务端执行 `git ls-remote --heads <认证URL>`(https 凭据自动注入 user:token; ssh 形态依赖服务端/主机 ssh key), 20s 超时, 返回分支列表或错误。
3. 流水线引用: 编辑器选择仓库+分支(空=默认分支) → 运行时首阶段自动插入"拉取代码 repo@branch"步骤: 目录已有同仓库 .git 则 `fetch + reset --hard + clean`(工作副本重置), 否则 `clone --depth 1`。
4. **安全护栏(必须)**:
   - 启用代码源后首阶段必须设置工作目录(保存时前后端双重校验; 引擎运行时二次校验), 防止 git 操作落到服务器进程工作目录;
   - 重置前校验目录内 .git 的远端仓库名与目标仓库一致(忽略协议差异), 不一致拒绝并报"工作目录是其他仓库, 拒绝重置"。
5. 注入变量: `CICD_BRANCH` / `CICD_REPO_URL`。
6. 验收: 公开库测试返回分支列表; 错误凭据测试明确报错; 配置非法工作目录的流水线被拒绝保存; 拉取步骤在目标主机指定目录产生 .git。

### FR-15 镜像仓库连接 (P0)

**描述**: 维护镜像仓库地址与凭据, 流水线引用后 docker 登录/推拉镜像开箱即用。

1. 字段: 名称(唯一)、地址(域名[:端口], 不含协议)、访问凭据(registry 类型, 可空=匿名)、备注。
2. **探活测试**: 服务端 GET `https://<server>/v2/`(有凭据则 Basic Auth): 200(匿名可访问)/401(存活需认证) 均判成功, 其他状态码报错, 10s 超时。
3. 流水线引用: 编辑器选择镜像仓库 → 运行时所有步骤注入 `REGISTRY` / `REGISTRY_USER` / `REGISTRY_PASS`(secret, 日志掩码)。
4. 配合 FR-16 模板: `docker login $REGISTRY -u "$REGISTRY_USER" -p "$REGISTRY_PASS"` 直接可用。
5. 验收: docker.io 探活返回 401 判存活; 注入变量在步骤中可见且密码掩码。

### FR-16 发布模板(K8s / Docker / 裸机) (P0)

**描述**: 内置常见发布动作模板, 编辑器一键插入为步骤, 参数即注入变量, 可改。

| 模板 | 步骤 | 前置条件 |
|---|---|---|
| Docker 构建+推送镜像 | docker login → build → push(`$REGISTRY/myapp:${BUILD_NUMBER}`) | 阶段主机有 docker + 配置镜像仓库 |
| Docker 主机发布 | docker pull → rm 旧容器 + docker run -d --restart | 目标主机 docker |
| K8s 发布 | kubectl apply -f k8s/ → rollout status --timeout | 流水线绑定 kubeconfig 凭据, 阶段主机有 kubectl |
| 裸机发布(备份+重启) | tar 备份(失败继续) → systemctl restart + is-active | 目标主机 systemd 服务 |

1. 模板插入后即普通步骤, 名称/命令可自由修改。
2. 内置变量全家桶: `$BUILD_NUMBER`(=CICD_BUILD_NUMBER) / `$CICD_RUN_ID` / `$CICD_PIPELINE_NAME` / `$CICD_TRIGGER` / `$CICD_BRANCH` / `$CICD_REPO_URL` / `$REGISTRY` 系 / `$GIT_REPO_*` / `$KUBECONFIG`。
3. 验收: 用模板搭一条"构建→推送→发布"流水线, 不手写任何 docker/kubectl 认证命令。

### FR-17 脚本库(自定义任务/脚本) (P0)

1. 脚本字段: 名称(唯一)、描述、内容(POSIX shell, ≤64KB)、更新时间。
2. CRUD 完整; 流水线编辑器中每个步骤提供"脚本库…"下拉, 选中即以脚本内容填充步骤命令(可再改)。
3. 脚本内容可引用全部注入变量; 服务端校验非空与长度。
4. 验收: 保存"健康检查"脚本 → 新建流水线步骤引用 → 运行日志输出脚本执行结果。

### FR-18 环节进度 (P0)

1. Run 增加进度字段: 终态步骤数/总步骤数 ×100(读取时实时计算, 不落盘)。
2. 呈现: 运行历史表进度列 + 运行详情头部进度条(颜色随状态: 成功绿/失败红/进行中黄); SSE status 帧携带, 前端实时刷新。
3. 验收: 长流水线运行中进度条按步骤完成推进; 失败中断时进度停在当前完成度。

---

## 四、非功能需求

| 类别 | 需求 |
|---|---|
| 性能 | 列表接口 <100ms; SSE 增量 ≤1s; 单步日志追加不重写全文件 |
| 可靠性 | 重启不丢历史; 孤儿运行可识别; 磁盘失败仅记日志 |
| 安全 | secret 掩码 + constant-time 比较 + 白名单校验 + SSH 转义; 凭据仅写不读; clone 双重安全护栏 |
| 资源 | 全局并发默认 2; 队列 64; 历史滚动清理(含日志) |
| 兼容 | Linux 为主; Windows 本机需 Git Bash 的 sh; 远程主机零 Agent 依赖 |
| 可维护 | 引擎与 HTTP 层分离; cron/掩码/仓库名解析/clone 护栏均有单元测试 |

---

## 五、API 汇总(与 FR 对应, 26 条)

| FR | 端点 | 方法 |
|---|---|---|
| FR-01/11 | /api/cicd/pipelines · /pipeline/get · /pipeline/save · /pipeline/delete | GET/POST |
| FR-04/09 | /pipeline/run · /run/cancel · /run/approve | POST |
| FR-08 | /runs · /run/get · /overview | GET |
| FR-07 | /run/log · /run/stream(SSE) | GET/POST |
| FR-20 | /artifact/download | GET |
| FR-06 | /webhook/{id} | POST |
| FR-13 | /credentials · /credential/save · /credential/delete | GET/POST |
| FR-14 | /repos · /repo/save · /repo/delete · /repo/test | GET/POST |
| FR-15 | /registries · /registry/save · /registry/delete · /registry/test | GET/POST |
| FR-17 | /scripts · /script/save · /script/delete | GET/POST |
