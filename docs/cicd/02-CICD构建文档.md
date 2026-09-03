# OpsCore CI/CD 模块构建文档

> 更新于 2026-09-03(v2) · 配套文档: [01-开源CI-CD项目调研分析.md](./01-开源CI-CD项目调研分析.md) / [03-CICD功能需求文档.md](./03-CICD功能需求文档.md)

## 一、目标与范围

在 OpsCore 运维控制台内新增 **CI/CD 流水线** 核心模块, 提供轻量级(对标 Jenkins 做减法)流水线编排与执行能力:

- **做什么**: 可视化编排"阶段→步骤"流水线; 步骤在本机或清单内任意远程主机上执行 shell 命令; 连接**代码仓库**(自动拉取)、**镜像仓库**(凭据注入); 发布目标覆盖 **K8s / Docker / 裸机**; 手动 / Webhook / cron 三种触发; **凭据中心**与**脚本库**; SSE 实时日志与**环节进度条**。
- **不做什么**(与单二进制零依赖定位一致): 不做容器化执行、不做制品库/缓存、不做 K8s CRD、不引入任何第三方依赖。

## 二、总体架构

```
┌────────────────────────── 前端 CicdModule.tsx (5 tabs) ─────────────────┐
│  流水线 / 运行历史(进度条) / 脚本库 / 仓库(代码+镜像) / 凭据               │
│  流水线列表 → 编辑器(modal: 代码源/镜像仓库/凭据下拉 + 模板步骤插入)        │
│  → 运行详情(modal: 进度条 + 阶段时间线 + SSE 日志)                        │
└──────────────┬───────────────────────────────────────────────────────┘
               │ REST (getJSON/postJSON, Bearer) + SSE(POST text/event-stream)
               ▼
┌────────────────────────── Go Server ───────────────────────────────────┐
│  main.go registerCoreModules                                           │
│    └─ modCfg{ man("cicd",...), routes[27 条] }   # 与其他 core 模块一致   │
│                                                                        │
│  internal/handlers/cicd.go        # HTTP 层: 参数校验/白名单/分发          │
│    └─ internal/cicd (Engine)      # 引擎: 队列/并发/状态机/持久化/触发器    │
│         ├─ Exec 回调 (main.go 注入 handlers.CicdExec)                    │
│         │    ├─ 本机: exec.CommandContext("sh","-c",cmd) 逐行流式         │
│         │    └─ 远程: remote.Pool.ExecLine(sh -c 转义单行) 完成后落盘      │
│         ├─ resolveRuntime: 触发时解析 代码源/镜像仓库/kubeconfig 为运行时   │
│         ├─ cronLoop: 每分钟对齐扫描 cron 触发器                           │
│         └─ store.JSONFile ×6: pipelines/runs/credentials/repos/          │
│              registries/scripts + logs/<runID>.log                       │
└────────────────────────────────────────────────────────────────────────┘
```

设计原则(承袭项目既有约定):

1. **目标主机分发**: 所有步骤经统一执行回调落到所选主机(本机进程内 / 远程 SSH 单会话), 与 `RunOnTarget` 契约一致; 远程命令参数单引号转义防注入。
2. **模块注册中心**: 编译期 `registerCoreModules` 注册 Manifest+Routes, 前端经 `/api/manifest` 动态出侧栏。
3. **JSON 文件存储**: 复用 `internal/store.JSONFile`(读写锁), 不动 CentralStore。
4. **SSE 复用**: 与 ansible 模块同款 `text/event-stream` + `flusher.Flush()` 范式; 事件结构 `{type, payload}`。
5. **领域层纯净**: internal/cicd 不依赖 net/http(通知的 http.Client 除外), 执行通道由 main.go 注入。

## 三、数据模型

### 3.1 Pipeline(定义, 存 data/cicd/pipelines.json)

```go
type Pipeline struct {
    ID          string    `json:"id"`          // 短随机 ID (pl-XXXXXX)
    Name        string    `json:"name"`
    Description string    `json:"description"`
    Env         []Var     `json:"env"`         // 流水线级环境变量
    Trigger     Trigger   `json:"trigger"`
    Stages      []Stage   `json:"stages"`      // 顺序执行
    Source      Source    `json:"source"`      // 代码源(可选): 首阶段自动拉取代码
    RegistryID  string    `json:"registryId"`  // 镜像仓库 → 注入 REGISTRY 系变量
    KubeCredID  string    `json:"kubeCredId"`  // kubeconfig 凭据 → 注入 KUBECONFIG
    TimeoutMin  int       `json:"timeoutMin"`
    MaxRuns     int       `json:"maxRuns"`
    NotifyURL   string    `json:"notifyURL"`
    CreatedAt   time.Time `json:"createdAt"`
    UpdatedAt   time.Time `json:"updatedAt"`
}
type Source struct{ RepoID, Branch string }
```

其余(Trigger/Var/Stage/Step)与 v1 相同, 见 03 需求文档。

### 3.2 资源定义(data/cicd/ 下 4 个 JSON)

```go
type Credential struct {  // credentials.json
    ID, Name, Type string   // Type: git | registry | kubeconfig | generic
    Username       string   // git/registry 用户名
    Server, Note   string   // 备注用途
    Data           string   // ★密文仅写不读: 列表接口回 hasData 标记
    UpdatedAt      time.Time
}
type Repo struct {        // repos.json — 代码仓库
    ID, Name, URL, CredID, DefaultBranch, Note string
}
type Registry struct {    // registries.json — 镜像仓库
    ID, Name, Server, CredID, Note string   // Server 不含协议
}
type Script struct {      // scripts.json — 脚本库
    ID, Name, Description, Content string
}
```

### 3.3 Run 与状态机

Run 增加 `Progress int`(读取时计算: 终态步骤数/总步骤数×100)。状态机:

```
Run:   queued → running → success | failed | canceled
Stage: pending → (approval? waiting → running) → success | failed | skipped | canceled
Step:  pending → running → success | failed | skipped | canceled
```

### 3.3.1 阶段审批门禁(v2.1)

Stage 增加 `Approval bool`: 开启后该阶段执行前暂停在 `waiting`, 由人工在运行详情中批准或拒绝(POST /api/cicd/run/approve):

- 等待期间**临时让出全局并发槽位**(`<-e.sem` / 取回), 不占并发名额(引擎槽位的获取/释放集中在 runWithSlot, 净变化为零);
- 拒绝 → 阶段 canceled + 后续阶段 skipped + 运行 canceled(错误信息"阶段 %q 人工拒绝");
- 等待中取消运行 → ctx 贯穿, waitApproval 视为拒绝;
- 服务重启时 waiting 与其他非终态一样被孤儿恢复为 failed;

## 四、执行引擎(internal/cicd)

### 4.1 运行时解析(resolveRuntime, 触发时执行)

触发时把流水线引用的外部资源解析为 `runtimeCtx`(不落盘):

1. **内置环境变量**(每个步骤可引用):

   | 变量 | 含义 |
   |---|---|
   | CICD_RUN_ID / CICD_PIPELINE_ID / CICD_PIPELINE_NAME / CICD_TRIGGER | 本次运行标识 |
   | CICD_BUILD_NUMBER / BUILD_NUMBER | 递增构建号(历史条数+1) |
   | CICD_BRANCH / CICD_REPO_URL | 代码源信息(配置代码源时) |

2. **凭据注入**: 代码源绑定的 git 凭据 → `GIT_REPO_USER`/`GIT_REPO_TOKEN`(secret); 镜像仓库凭据 → `REGISTRY`/`REGISTRY_USER`/`REGISTRY_PASS`(secret)。用户自定义 Env 最后追加, 可覆盖内置变量。
3. **拉取代码步骤**: 配置代码源后, 首阶段自动插入"拉取代码 repo@branch"合成步骤:

   ```sh
   if [ -d .git ]; then
     R=$(git remote get-url origin | sed 's#.*/##; s#\.git$##')
     [ "$R" != '<仓库名>' ] && { echo "工作目录是其他仓库($R), 拒绝重置"; exit 64; }   # ★安全护栏
     git fetch origin '<branch>' && git reset --hard 'origin/<branch>' && git clean -fd
   else
     git clone --depth 1 -b '<branch>' '<认证URL>' .
   fi
   ```

   **安全护栏(事故教训)**: ① 保存时强制"启用代码源 → 首阶段必须设置工作目录"(引擎+HTTP 双重校验), 杜绝 git 操作落到服务器进程 cwd; ② clone 命令内校验现有 .git 的远端仓库名与目标仓库一致, 不一致拒绝 reset/clean —— 只可能重置"同一个仓库"的工作副本。
4. **kubeconfig**: 凭据内容 base64 编码暂存; 运行时对涉及的主机执行 `echo <b64> | base64 -d > /tmp/.opscore-kubeconfig-<plID>.yaml && chmod 600`, 并为该主机后续步骤追加 `KUBECONFIG` 环境变量; 运行结束 `rm -f` 清理(尽力而为)。每主机只写一次。

### 4.2 执行流程

```
Trigger(校验/解析) → Run{queued} 落盘 → 队列 → 信号量(默认2) → running
  逐 Stage: stageEnv = rt.env + (kubeconfig ? KUBECONFIG : 空)
    逐 Step(对齐 defs: 首阶段含合成的拉取代码步骤):
      engine.Exec(ctx, host, workspace, cmd, stageEnv, onLine)  → 掩码 → 日志文件
  汇总 → success/failed/canceled → 落盘 → kubeconfig 清理 → 裁剪历史 → NotifyURL
```

其余与 v1 相同: 同流水线串行(409)、全局并发 2(`OPCORE_CICD_MAXRUNS`)、队列 64、取消(ctx 贯穿, 本机 kill/远程放弃等待)、孤儿恢复、日志 `data/cicd/logs/<runID>.log`、按 MaxRuns 滚动裁剪。

## 五、HTTP API(internal/handlers/cicd.go, 26 条)

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | /api/cicd/pipelines | 流水线列表(含最近运行, secret 脱敏) |
| GET | /api/cicd/pipeline/get | 流水线详情(编辑用) |
| POST | /api/cicd/pipeline/save | 新建/更新(校验白名单+cron+代码源护栏) |
| POST | /api/cicd/pipeline/delete | 删除(运行中 409) |
| POST | /api/cicd/pipeline/run | 手动触发 |
| POST | /api/cicd/run/cancel | 取消运行 |
| POST | /api/cicd/run/approve | 审批等待中的阶段(approve=true 放行/false 拒绝) |
| GET | /api/cicd/runs | 运行历史(?pipeline=&limit=) |
| GET | /api/cicd/run/get | 运行详情(含 progress) |
| GET | /api/cicd/run/log | 日志回填(?id=&offset=) |
| POST | /api/cicd/run/stream | SSE(status/log/done 帧) |
| POST | /api/cicd/webhook/{id} | Webhook 触发(X-Opscore-Token / ?token= / body.secret) |
| GET | /api/cicd/overview | 概览统计 |
| GET | /api/cicd/credentials | 凭据列表(Data 恒空, 仅 hasData) |
| POST | /api/cicd/credential/save | 凭据保存(编辑留空 data=保持原值) |
| POST | /api/cicd/credential/delete | 凭据删除 |
| GET | /api/cicd/repos | 代码仓库列表 |
| POST | /api/cicd/repo/save · /repo/delete | 仓库保存/删除 |
| POST | /api/cicd/repo/test | 连通性测试(服务端 git ls-remote, 20s 超时) |
| GET | /api/cicd/registries | 镜像仓库列表 |
| POST | /api/cicd/registry/save · /registry/delete | 镜像仓库保存/删除 |
| POST | /api/cicd/registry/test | 探活(GET /v2/, 200/401 均视为存活) |
| GET | /api/cicd/scripts | 脚本库列表 |
| POST | /api/cicd/script/save · /script/delete | 脚本保存/删除 |

安全要点: 名称/ID/URL 白名单正则; webhook secret constant-time 比较; 凭据 Data 仅写不读; secret 值日志掩码(≥4 字符); 所有写操作 POST。

## 六、前端(web/src/modules/CicdModule.tsx, 5 tabs)

```
CicdModule
├─ 流水线: 列表(触发器徽标/最近运行) + 编辑器(modal)
│    编辑器 = 基本信息表单 + 触发方式 + 代码源与凭据(仓库/镜像/KUBECONFIG 下拉)
│              + 环境变量行编辑 + 阶段卡(主机下拉/工作目录/步骤行)
│              + ★发布模板插入(Docker构建推送/Docker发布/K8s发布/裸机发布)
│              + ★步骤"脚本库…"下拉(选脚本填充命令)
├─ 运行历史: 过滤表 + ★进度条(usage-bar) + 详情/取消
├─ 脚本库: 表格 + 编辑 modal(名称/描述/内容 textarea)
├─ 仓库: 代码仓库卡片(测试=git ls-remote) + 镜像仓库卡片(测试=/v2/ 探活)
└─ 凭据: 表格(类型徽标/用户名/hasData) + 编辑 modal(类型切换字段; kubeconfig=textarea)
     运行详情(modal): ★进度条 + 阶段时间线(状态/退出码/耗时) + SSE 日志面板
```

内置变量提示、发布模板常量(STEP_TEMPLATES)与引擎注入的变量名一致($BUILD_NUMBER/$REGISTRY/$REGISTRY_USER/$REGISTRY_PASS/$GIT_REPO_TOKEN/$KUBECONFIG)。

## 七、注册与接线改动清单

| 文件 | 改动 |
|---|---|
| internal/cicd/engine.go / cron.go / creds.go / cron_test.go | 新增: 引擎+触发器+资源层 |
| internal/handlers/cicd.go | 新增: 27 个 handler + CicdExec 执行回调 |
| main.go | 引擎初始化+Exec 注入+26 条路由注册+defer Stop |
| internal/module/manifest.go | coreModules 增加 cicd 条目 |
| web/src/modules/CicdModule.tsx | 新增: 模块页面(5 tabs) |
| web/src/App.tsx | MODULE_MAP/路由 + cicd 火箭图标(顺带移除 containers 死条目) |

## 八、错误处理与边界

- 同一流水线重复触发/删除运行中流水线 → 409; 队列满 → 明确报错。
- **代码源安全护栏**(见 4.1): 无首阶段工作目录 → 保存被拒; 工作目录非目标仓库克隆 → 步骤内拒绝重置(exit 64)。
- SSH 主机不可达 → 步骤 failed + 后续 skipped; Windows 本机无 sh → 明确报错提示。
- 远程步骤取消为"放弃等待"(SSH 无法安全终止, 命令在远端自行结束)。
- cron 触发不补跑停机期间错过的调度。
- 时钟: 时间戳本地时区, 耗时用 time.Since 单调时钟。

## 九、未来规划(v1.1+)

1. Stage 级手动审批(借鉴 Jenkins input / GitLab manual job)。
2. 制品收集: 步骤声明 artifacts 路径, 打包到 data/cicd/artifacts/ 并提供下载。
3. 步骤级 when 条件(分支/事件/变量表达式)与 matrix 展开。
4. 通知渠道扩展(邮件/钉钉/飞书)。
5. 流水线导入导出(JSON/YAML)与模板库。
6. 并行阶段(DAG 依赖)与 git commit 信息展示(拉取步骤输出解析)。
