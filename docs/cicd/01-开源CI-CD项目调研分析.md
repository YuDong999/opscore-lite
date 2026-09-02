# 开源 CI/CD 项目调研分析

> OpsCore CI/CD 模块前置调研 · 更新于 2026-09-03
> 调研对象: Drone CI / Woodpecker CI / Gitea Actions / Jenkins / GitLab CI/CD / GitHub Actions / Tekton Pipelines / Argo Workflows
> 目的: 提炼各项目功能体系, 为 OpsCore 自研 CI/CD 模块确定功能范围与设计取舍

---

## 一、Drone CI

**定位**: 容器化持续集成平台, Go 编写, Server+Runner 分离, 配置即代码 (`.drone.yml` 入库)。

**架构与技术栈**: Go Server (UI/API/调度) + Runner (gRPC 领取任务, 每个步骤=一个 Docker 容器); 数据存储 SQLite/MySQL/Postgres; 单仓库单配置文件。

**功能树**:

```
Drone CI
├─ Pipeline 定义 (.drone.yml)
│  ├─ kind: pipeline / type: docker|kubernetes|exec    # 流水线类型(执行载体)
│  ├─ name / platform(os,arch) / workspace             # 命名 / 目标平台 / 工作目录
│  ├─ clone: depth / disable                           # 克隆控制
│  ├─ steps[]                                          # 步骤(串行执行)
│  │  ├─ image + commands                              # 容器镜像 + shell 命令
│  │  ├─ environment / secrets                         # 环境变量 / 密钥注入
│  │  ├─ settings                                      # plugin 插件参数(社区插件生态)
│  │  ├─ privileged / detach                           # 特权模式 / 后台服务步骤
│  │  ├─ when: branch|event|cron|status|ref|paths      # 步骤级执行条件
│  │  ├─ failure: ignore                               # 失败继续
│  │  └─ depends_on                                    # 步骤并行化(DAG)
│  ├─ services[]                                       # 伴生服务容器(db/redis 供步骤连接)
│  ├─ trigger: branch|event|cron|ref                   # 流水线级触发过滤
│  ├─ depends_on                                       # 多流水线编排(命名流水线间依赖)
│  ├─ concurrency: limit                               # 流水线并发上限
│  └─ volumes / networks / node(标签选 Runner)
├─ 触发方式
│  ├─ push / tag / pull_request (webhook 自动)
│  ├─ cron (UI/CLI 注册定时, YAML 内仅 trigger.cron 过滤)
│  ├─ promote / rollback (自定义事件, 配合部署)
│  └─ 手动 (UI Restart/Trigger 按钮)
├─ Secrets
│  ├─ 仓库级 / 组织级, include/exclude 限定可用镜像
│  ├─ UI/CLI/API 管理, 日志中自动掩码
│  └─ signed: pull_request 可用性控制(防投毒)
├─ 构建历史与日志
│  ├─ Build → Stage → Step 三级模型
│  ├─ 实时流式日志(SSE/WS), 每步骤折叠视图
│  └─ 构建保留策略 / 手动清理
├─ Matrix 构建 (YAML matrix: 展开多组合并行)
├─ API: REST /api/repos/{owner}/{repo}/builds|secrets|cron + CLI (drone exec 本地运行)
└─ 数据模型: User / Repo / Build / Stage / Step / Secret / Cron / Registry
```

---

## 二、Woodpecker CI

**定位**: Drone 0.8 社区分支(Apache-2.0), 更开放的多后端轻量 CI。

**架构**: server + agent 横向扩展, agent↔server 走 gRPC(共享密钥认证); 后端 backend: docker / kubernetes / local / auto-detect。

**功能树**:

```
Woodpecker CI
├─ Pipeline 定义 (.woodpecker.yaml, 支持多文件 .woodpecker/*.yaml)
│  ├─ steps[]: name + image + commands / backend 选项
│  │  ├─ when: branch|event|cron|path|status|platform|environment|instance
│  │  ├─ failure: ignore / depends_on(步骤级)
│  │  └─ directory(工作子目录) / environment
│  ├─ services[] / workspace / skip_clone / clone: git-image 配置
│  ├─ labels: 匹配 agent 标签调度(pipeline 和 agent 双侧)
│  ├─ runs_on / branches / event 触发过滤
│  └─ depends_on: 多流水线编排 + path 过滤(变更目录触发特定流水线)
├─ 触发: push/tag/PR/manual/cron/deployment + path 变更过滤
├─ Secrets: 服务器端存储(仓库/组织级), 按 event 限制使用场景, 日志掩码
├─ 环境/变量: ${CI_*} 内置变量族(repo/commit/pipeline/CI 编号), environment 注入
├─ Matrix 构建
├─ 徽章(Badge) URL / 通知插件生态
├─ API: REST /api/repos/{id}/pipelines|secrets|cron, CLI woodpecker-cli
└─ 数据模型: Repo / Pipeline / Workflow(多流水线) / Step / Secret / Cron / Agent
```

---

## 三、Gitea Actions

**定位**: Gitea 1.19+ 内置 CI 系统, 与 GitHub Actions 语法兼容(基于 act/nektos fork), act_runner 独立执行器。

**功能树**:

```
Gitea Actions
├─ Workflow 定义 (.gitea/workflows/*.yml, 兼容 .github/workflows)
│  ├─ on: push/pull_request/schedule(cron)/workflow_dispatch/release…
│  ├─ jobs[]: runs-on(labels 选 runner) / needs(DAG) / strategy.matrix
│  ├─ steps[]: uses(★支持绝对 URL 引用任意 git 仓库 action, 超出 GitHub 能力) / run / with
│  ├─ env / secrets(GITEA_TOKEN, REPO_TOKEN) / services
│  └─ actions/cache、upload/download-artifact 兼容
├─ Runner (act_runner)
│  ├─ 注册(token) + labels 声明能力(ubuntu-latest→node:16 容器等)
│  ├─ 执行模式: container / host(直接 shell)
│  └─ 并发配置 / 一次性 ephemeral
├─ 管理: 仓库/组织/全局三级 runner, 并发 job 数配置
├─ 可观测: 每步骤日志流, 运行历史, Webhook 通知
└─ 差异点: 部分 marketplace action 不兼容(服务/预构建 action 有缺口)
```

---

## 四、Jenkins

**定位**: 最老牌开源自动化服务器(Java), 插件生态 1800+, Master(Controller)+Agent 分布式。

**功能树**:

```
Jenkins
├─ Job 类型
│  ├─ Freestyle(表单配置) / Maven / Matrix
│  ├─ Pipeline(Jenkinsfile, Declarative/Scripted 两风格)
│  ├─ Multibranch Pipeline(分支自动发现, 分支索引) / Folder 组织
├─ Declarative Pipeline 语法
│  ├─ agent: any|label|docker|kubernetes(stage 级可覆盖)
│  ├─ stages[] → steps; parallel 并行块; matrix(axis/excludes/failFast)
│  ├─ environment { CREDS = credentials('id') }   # 凭据绑定
│  ├─ options: timestamps/timeout/retry/buildDiscarder/ansiColor
│  ├─ parameters(手动触发参数表单) / triggers: cron|pollSCM|upstream
│  ├─ when: branch|tag|environment|expression|anyOf/allOf/changelog
│  ├─ input: 手动审批(暂停等待确认, 可指定审批人)
│  └─ post: always|success|failure|fixed|regression|aborted|changed|cleanup
├─ 凭据管理 (Credentials Plugin)
│  ├─ 类型: Secret text / Username+password / SSH key / Secret file / Token
│  ├─ 作用域: 全局/文件夹/系统; withCredentials 遮蔽日志
│  └─ 凭据轮转与审计
├─ 分布式构建
│  ├─ Agent(JNLP/SSH) 注册, labels 标签匹配调度
│  ├─ executor 并发数 / 节点监控 / 节点离线处理
│  └─ 队列与节流(throttle), Cloud agent(K8s 动态扩缩)
├─ 触发: 手动/webhook(GitHub/GitLab 插件)/pollSCM 轮询/cron/上游 job 触发
├─ 构建历史与制品
│  ├─ 构建编号保留策略(discarder), Console 日志归档
│  ├─ archiveArtifacts 制品归档 + fingerprint 指纹追踪
│  └─ JUnit/覆盖率等报告插件解析
├─ 通知: Email/Slack/钉钉/webhook 插件
├─ 权限: RBAC(Role-based)/Matrix 授权/项目级隔离, 审计日志
├─ API: REST(/job/x/y/api/json|xml), CLI, Remote Access API
└─ 数据模型: Jenkins(实例) / Item(Job) / Build / Run / Node / Credential / View
```

---

## 五、GitLab CI/CD

**定位**: GitLab 内置 CI/CD, YAML 配置(`.gitlab-ci.yml`)+Runner 执行, DevSecOps 一体化。

**功能树**:

```
GitLab CI/CD
├─ 定义 (.gitlab-ci.yml)
│  ├─ stages: 声明阶段顺序(默认 .pre/build/test/deploy/.post)
│  ├─ jobs[]: script / stage / image / tags(选 runner) / services
│  ├─ before_script / after_script / pre_clone_script
│  ├─ rules: if|changes|exists + when(on_success|manual|always|delayed)
│  ├─ needs: DAG 依赖(跳过阶段墙, 可附带 artifacts 依赖)
│  ├─ parallel: matrix 矩阵展开 / retry: when+max / timeout
│  ├─ allow_failure(失败不阻塞) / interruptible(被新流水线取消)
│  ├─ extends/include(模板复用, include: local|remote|template) / YAML anchor
│  ├─ environment: name+url+on_stop(环境生命周期)/deployment_tier
│  ├─ artifacts: paths+expire_in+reports(junit/sast…) / dependencies 控制下载
│  ├─ cache: key(fallback)+paths+policy(pull|push)
│  └─ variables(masked/protected/expand) / id_tokens(OIDC)
├─ Runner 体系
│  ├─ 类型: shared/group/specific; executor: shell/docker/k8s/ssh/custom
│  ├─ tags 双向匹配(job.tags ↔ runner.tags) / lock 专仓 / protected 分支限定
│  └─ concurrency 并发 / 缓存分布式(S3/MinIO)
├─ 触发: push/webhook/schedules(cron, 可带变量)/manual/_pipeline(父子流水线)
│        /multi-project trigger/merge_request_event
├─ 环境与部署
│  ├─ 环境视图 / review app / protected environments(人工批准)
│  ├─ 手动 job(when: manual) 作为部署门禁
│  └─ rollback / stop environment
├─ 安全: protected branch/tag+masked/protected variables, secret 检测/SAST/DAST 模板
├─ 可观测: Pipeline DAG 可视化 / lint 校验 / 耗时统计 / 测试报告合并
├─ API: /api/v4/projects/:id/pipeline|trigger|schedules, trigger tokens, CI Lint
└─ 数据模型: Project / Pipeline / Job / Stage / Runner / Variable / Schedule / Environment
```

---

## 六、GitHub Actions

**定位**: GitHub 平台内置 CI/CD, workflow YAML + Marketplace action 生态; 开源部分为 actions/runner(C#/.NET, 仅维护)。

**架构**: 服务端(闭源)解析/排队/编排; Runner.Listener 常驻进程长轮询领取 job(仅出站 HTTPS), 收到 JobMessage 派生 Runner.Worker 执行 step(node/docker/shell/composite handler)。

**功能树**:

```
GitHub Actions
├─ Workflow (.github/workflows/*.yml)
│  ├─ 顶层: on / permissions(GITHUB_TOKEN 最小授权) / env / defaults
│  │        / concurrency(并发组+cancel-in-progress) / jobs
│  ├─ job: runs-on(标签) / needs(DAG) / strategy.matrix(include/exclude,fail-fast)
│  │       / if(success|failure|always|cancelled) / outputs(job 间传值)
│  │       / environment(审批保护+环境级 secrets) / services(sidecar 容器)
│  │       / timeout-minutes / container
│  ├─ steps: uses(action 引用: repo@ref / docker:// / ./local) / run(shell)
│  │         / with / env / if / working-directory / continue-on-error
│  │         └─ 工作流命令 ::error/::warning/::notice/::group/set-output
│  ├─ 触发(on): push|pull_request(branches/tags/paths 过滤) / schedule(cron)
│  │   / workflow_dispatch(手动+inputs 表单) / workflow_call(可复用)
│  │   / release|create|delete / repository_dispatch(外部 API)
│  │   / workflow_run(workflow 链式) / 30+ 平台事件
│  ├─ 表达式: ${{ }} + 20 函数(contains/format/toJSON/hashFiles…)
│  │   └─ 上下文 github/env/vars/secrets/jobs/steps/needs/matrix/inputs
│  ├─ Secret: 仓库/环境/组织三级; GITHUB_TOKEN 自动签发轮换
│  ├─ 制品与缓存: upload/download-artifact / actions.cache(key+hashFiles)
│  └─ 重试: 无原生字段(action 或脚本自实现)
├─ Runner(开源): 注册(registration token)/labels/ephemeral/日志逐行掩码/自动升级
└─ 数据模型: Workflow / WorkflowRun / Job / Step / CheckRun
```

---

## 七、Tekton Pipelines

**定位**: K8s 原生 CI/CD 框架(CNCF CDF), 一切皆 CRD, 无自有服务端。

**架构**: TaskRun=Pod, 每个 Step=Pod 内一个容器, entrypoint 注入器按序唤醒; resolver(git/hub/bundle)远程解析引用。

**功能树**:

```
Tekton Pipelines
├─ CRD 模型
│  ├─ Task: params(name/type/default) / steps(image|script|env|onError)
│  │        / workspaces(声明卷) / results(/tekton/results) / sidecars / stepTemplate
│  ├─ Pipeline: tasks[](taskRef+runAfter 定序) / when(in|notin|exists 守卫)
│  │        / matrix(fan-out) / finally[](收尾 Task, 可读 taskStatus)
│  ├─ TaskRun / PipelineRun: params/workspaces/serviceAccount/timeouts(三级)/retries
│  └─ Custom Run(扩展: 人工审批等自定义任务)
├─ 触发: kubectl/tkn 手动创建 Run / Triggers 组件(EventListener→Interceptor
│        →TriggerBinding→TriggerTemplate) / 远程 resolver
├─ 变量: $(params.x)/$(workspaces.x.path)/$(results.x.path) 显式替换(无通用表达式)
├─ 工作区: PVC/emptyDir/configMap/secret 卷后端 + Affinity Assistant
├─ 供应链: Chains(SLSA 证明签名) / Results(历史持久化) / OCI Bundle 分发
├─ 可观测: K8s Event + Condition(Succeeded,reason) / Pod 日志 / Dashboard / tkn CLI
└─ 数据模型: Task / TaskRun / Pipeline / PipelineRun / Workspace / Result
```

---

## 八、Argo Workflows

**定位**: K8s 容器原生工作流引擎(CNCF 毕业), Steps/DAG 双编排模型, 通用批处理+CI/CD。

**架构**: workflow-controller reconcile Workflow CRD; argo-server 提供 REST/gRPC+UI+SSO; executor sidecar 上报状态/抓取 artifact; 以 CRD 为唯一事实来源。

**功能树**:

```
Argo Workflows
├─ Workflow CRD
│  ├─ spec: entrypoint+arguments / templates[] / volumes / parallelism
│  │        / ttlStrategy / podGC / onExit(退出钩子) / synchronization(semaphore|mutex)
│  ├─ template 六型: container / script / resource(操作 K8s 资源)
│  │   / suspend(人工审批) / steps(顺序块, 同块并行) / dag(dependencies 依赖图)
│  ├─ steps/dag 内: when 条件 / withItems|withParam|withSequence 循环展开
│  │   / hooks 生命周期钩子 / templateRef 复用(WorkflowTemplate, 集群级变体)
│  ├─ 通用: inputs(parameters+artifacts) / outputs(valueFrom:path|JQ)
│  │   / retryStrategy(limit+指数退避+retryPolicy) / timeout / activeDeadlineSeconds
│  ├─ 表达式: {{workflow.parameters.x}}/{{inputs...}}/{{item}} + sprig expr
│  └─ Artifacts: S3/GCS/Azure/OSS/Artifactory/HTTP/Git/raw 多后端仓库
├─ 触发: CLI submit / CronWorkflow(cron+时区+并发策略) / Webhook
│        / Argo Events(EventSource/Sensor) / REST API+四种语言 SDK
├─ 控制面: 并行度(两级)/semaphore 限流/cron 并发策略(Allow|Forbid|Replace)
│        /suspend-resume-stop/memoization(输出缓存复用)
├─ 可观测: 拓扑 UI+实时日志多路查看 / K8s Event / Prometheus 指标
│        / 状态树(Pending|Running|Succeeded|Failed|Error+node phase) / 归档与 GC
└─ 数据模型: Workflow / WorkflowTemplate / CronWorkflow / ClusterWorkflowTemplate / Artifact
```

---

## 九、八项目横向对比

| 维度 | Drone | Woodpecker | Gitea Actions | Jenkins | GitLab CI | GH Actions | Tekton | Argo |
|---|---|---|---|---|---|---|---|---|
| 配置载体 | .drone.yml | .woodpecker.yaml | workflow yml | Jenkinsfile | .gitlab-ci.yml | workflow yml | CRD | CRD |
| 执行载体 | Docker 容器 | docker/k8s/local | 容器/host | agent JVM | runner(多 executor) | runner | Pod | Pod |
| 触发 | push/tag/PR/cron/promote | +path 过滤 | +workflow_dispatch | +pollSCM/上游 | +schedules/父子流水线 | 30+ 事件/复用 | Triggers CRD | Cron/Events |
| 条件控制 | when | when(含 path) | if + on 过滤 | when + post | rules + only/except | if + on 过滤 | when 子句 | when + hooks |
| 编排 | 步骤串行+depends_on | 同左 | needs DAG+matrix | stages/parallel/matrix | stages+needs DAG | needs+matrix | runAfter DAG+finally | steps/DAG 双模 |
| Secret | 仓库/组织级 | 服务端存储 | 仓库级 | credentials 插件 | masked/protected | 三级作用域 | K8s Secret | K8s Secret |
| 制品 | 外部(插件) | 外部 | artifacts 兼容 | archiveArtifacts | artifacts+reports | upload-artifact | results/workspace | artifact 仓库 |
| 缓存 | 无原生 | 无原生 | actions/cache | 无原生(插件) | cache 关键字 | actions/cache | workspace 复用 | memoization |
| 定时 | cron(UI 注册) | cron | schedule | cron/pollSCM | schedules | schedule | 无原生 | CronWorkflow |
| 手动审批 | — | — | environment(部分) | input | manual job+protected env | environment 审批 | Custom Run | suspend 模板 |
| 通知 | 插件 | 插件 | webhook | 邮件/IM 插件 | 内置+模板 | 内置 | 外部 | 外部/Event |
| 权限 | 仓库级 | 组织/仓库 | 仓库权限 | RBAC 矩阵 | protected+角色 | 环境保护 | K8s RBAC | SSO+RBAC |
| 部署形态 | server+runner | server+agent | 内置+runner | controller+agent | 内置+runner | SaaS+runner | k8s controller | k8s controller+server |

---

## 十、对 OpsCore 的启示(取舍结论)

结合 OpsCore 现状(Go 单二进制 / 标准库 net/http / JSON 文件存储 / SSH 执行池 / 多主机目标分发 / SSE 日志流), 各项目给我们的关键启示:

| 借鉴点 | 来源 | 在 OpsCore 的落地 |
|---|---|---|
| Stage/Step 两级编排模型 | Jenkins Declarative / GitLab stages | Pipeline → Stage(顺序) → Step(顺序), 阶段即"在哪台主机做什么" |
| 执行条件 when | Drone/Woodpecker | Step 级 continueOnFail(GitLab allow_failure 语义) |
| GUI 定义流水线(不强制 yml) | Jenkins Freestyle | 表单化编排, 降低使用门槛; 数据模型天然可导出 JSON |
| 触发三件套 | 全部 | 手动 + Webhook(secret 校验) + cron(5 字段解析器) |
| "主机即 Runner" | Jenkins agent label / Woodpecker agent | 复用现有 remote.Pool, Step 指定目标主机(本机或 SSH 远程) |
| 日志实时流 | Drone/GitLab/Jenkins | 复用 ansible 模块 SSE 范式 + 日志文件落盘(历史可回看) |
| Secret 掩码 | Jenkins/GitLab masked | 运行时日志逐行替换 Secret 值; 凭据仅写不读 |
| 收尾钩子 | Jenkins post / Tekton finally / Argo onExit | Pipeline 级 notify webhook(成功/失败通知) |
| 历史保留策略 | Jenkins buildDiscarder | 运行历史按数量滚动清理(含日志文件) |
| 凭据中心 | Jenkins credentials / GitLab variables | git/registry/kubeconfig/generic 四类, 类型化注入 |
| 进度可视化 | GitLab stage view / Drone 折叠视图 | 步骤完成占比 progress 字段 + 前端进度条 |
| API 风格 | 全部 | REST + 项目统一 WriteJSON 范式 |

**明确不做**(对标 Jenkins 减法): 容器化执行(与单二进制零依赖定位冲突)、matrix 展开、制品库、缓存体系、K8s CRD、插件系统 —— 均列入需求文档的"未来规划"。
