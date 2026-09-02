# 2026-09-03 改动记录

本次为一次整体提交, 新增 **CI/CD 流水线** 核心模块(第 9 大模块)并迭代 v2(精简 CI/CD 链路), 配套调研/构建/需求三份设计文档。

---

## 一、新增模块: CI/CD 流水线 (id: cicd, core)

定位: 轻量级流水线编排与执行 —— 阶段/步骤可视化编排, 步骤在本机或清单内任意主机经 SSH 执行; 取材于 Drone/Woodpecker/Jenkins/GitLab CI/GitHub Actions/Tekton/Argo 的功能取舍(对标 Jenkins 做减法), 详见 `docs/cicd/01-开源CI-CD项目调研分析.md`。

### v1 基础能力
- **internal/cicd/engine.go**: 领域层引擎(不依赖 net/http)。Pipeline(Trigger/Env/Stage/Step) + Run 状态机; 同流水线串行(409), 全局并发信号量(默认 2, `OPCORE_CICD_MAXRUNS`), 队列 64; 取消(ctx 贯穿, 本机 kill/远程放弃等待); 日志 `data/cicd/logs/<runID>.log` 逐行落盘 + secret 掩码; 历史按 maxRuns 滚动裁剪; 重启孤儿恢复; notifyURL 终态通知。
- **internal/cicd/cron.go**: 5 字段 cron 解析/匹配(vixie 语义, 日+周受限为"或"), 每分钟对齐扫描, 错过不补跑。
- **internal/handlers/cicd.go**: 26 个端点 + 执行回调 `CicdExec`(本机 `sh -c` 逐行流式; 远程连接池 `ExecLine` 单会话, Shq 转义防注入); Webhook 为全站唯一无 Bearer 端点(constant-time 凭证比较); SSE(status/log/done 帧)。
- **前端 CicdModule.tsx**: 流水线编排/运行历史/脚本库/仓库/凭据 5 tabs; 运行详情(阶段时间线 + SSE 实时日志 + 进度条); Webhook 信息弹窗。
- 校验白名单/凭据脱敏/触发器三件套等见需求文档 FR-01~FR-12。

### v2 增补(精简链路: 代码库 → 构建 → 镜像仓库 → 发布 K8s·Docker·裸机)
- **凭据中心**(FR-13): git/registry/kubeconfig/generic 四类, 密文仅写不读(列表只回 hasData), 编辑留空保持原值。
- **代码仓库**(FR-14): 地址+凭据+默认分支; 连通性测试(服务端 git ls-remote); 流水线引用后首阶段自动插入"拉取代码"步骤(已有同仓库工作副本则 fetch+reset+clean, 否则浅克隆), 注入 GIT_REPO_USER/GIT_REPO_TOKEN/CICD_BRANCH/CICD_REPO_URL。
- **镜像仓库**(FR-15): 地址+凭据; 探活(GET /v2/, 200/401 判存活); 引用后注入 REGISTRY/REGISTRY_USER/REGISTRY_PASS。
- **发布模板**(FR-16): Docker 构建+推送 / Docker 主机发布 / K8s 发布(apply+rollout) / 裸机发布(备份+重启), 编辑器一键插入, 命令引用注入变量($BUILD_NUMBER/$REGISTRY/...)。
- **脚本库**(FR-17): 自定义脚本 CRUD, 步骤一键引用填充。
- **kubeconfig 下发**(FR-13): 凭据内容 base64 落盘目标主机 /tmp(600 权限), 注入 KUBECONFIG, 运行结束清理。
- **环节进度**(FR-18): Run.progress(终态步骤占比, 读取时计算), 历史表与详情页进度条, SSE 实时刷新。
- **构建号**: CICD_BUILD_NUMBER / BUILD_NUMBER(别名)。

### 安全护栏(必须, 见 FR-14)
1. 启用代码源后首阶段必须设置工作目录(保存前后端校验 + 引擎运行时二次校验) —— 防止 git 操作落到服务器进程 cwd。
2. clone/reset 前校验目录内 .git 远端仓库名与目标仓库一致(忽略协议差异), 不一致拒绝重置(exit 64) —— 只可能重置"同一个仓库"的工作副本。

## 二、验证

- `go build ./...` / `go vet` / `go test ./...` 通过(cron 解析匹配/掩码/仓库名解析/clone 护栏有单测); `tsc --noEmit` 全绿; `vite build` 通过。
- 端到端冒烟: 凭据 CRUD+脱敏(列表无明文, 编辑留空保持原值) → 代码仓库测试(github 公开库返回分支) → 镜像探活(docker.io 401 判存活) → 脚本库 → 完整流水线(代码源+镜像仓库+多阶段) → 拉取代码步骤自动注入 → 变量注入与掩码 → Webhook 401/403/200 → 运行中重复触发/删除 409 → 取消 → 进度计算。

## 三、事故记录与修复(重要)

冒烟测试期间发生一次**工作目录误用事故**, 已定位根因并加双重护栏:

- **经过**: 测试流水线启用代码源但未设置阶段工作目录, 自动拉取步骤在本机执行时继承了服务器进程 cwd(项目根目录), 其中的 `git fetch + reset --hard + clean` 作用于了项目自身(该目录存在一个只跟踪了 2 个文件的杂散 .git, origin 指向 GitHub 仓库), 导致工作树被替换、未跟踪文件被清理。
- **恢复**: 项目源码经 `git reset --hard origin/main` 从 GitHub 远端完整恢复(恢复到丢失前对应的 cdb1e4b; 远端还有更新的 dbmanager 提交 433ec5f 可自行 pull); data/ 运行时数据(主机清单/执行历史等)从**仍在运行的旧服务进程内存**经 API 导出重建(ansible_hosts/ansible_history 等 6 个文件); 我的 CI/CD 新增文件全部按记录重建。
- **修复**: 见上方"安全护栏"两条 —— 该类事故已不可能复现(保存被拒 + 运行时拒绝重置)。
- **教训**: 自动化步骤的执行目录必须是显式声明的工作目录, 永不依赖进程 cwd; 涉及 reset/clean 的操作必须先验证"这是目标仓库的工作副本"。

## 四、边界说明

- 步骤命令为 POSIX shell 语义: Windows 本机执行需 Git Bash 的 `sh`(无则报错提示); 服务器主力场景 Linux; 远程目标主机零依赖(复用 SSH 池, 无需 Agent)。
- 远程步骤取消为"放弃等待"(SSH 会话无法安全终止, 命令在远端自行结束), 文档已注明。
- cron 触发不补跑停机期间错过的调度。
