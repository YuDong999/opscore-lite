# 2026-08-24 改动记录

本次为一次整体提交, 覆盖: 目标主机操作正确性、性能优化、服务列表完整性、日志体验、并发安全。

---

## 一、正确性修复(高危): 写操作按所选主机分发

此前防火墙启停、服务启停、磁盘分区/格式化/挂载等**写操作一律在 server 本机执行**, 与页面所选目标主机无关 —— 曾导致"对 worker 停 kubelet 实际停掉了 master 的 kubelet"、"开启防火墙实际开在了 master"等事故。

- 新增统一执行器 `internal/handlers/target.go`:
  `RunOnTarget(hostID, argv)` — 本机走进程内 exec; 远程经连接池单会话 SSH(参数单引号转义防注入), 传输层故障自动换新连接重试。
- **firewall.go**: 读(状态/规则/区域/rich-rule/端口转发)与写(action)全链按 host 分发; 审计新增 target/verified 字段。
- **tasks.go**: 磁盘 mount/umount/delete/partition/format/smart 远程分发(diskActionRemote)。
- **services.go**: 服务 start/stop/restart 远程分发 + 单元名白名单防注入。
- LVM / netconfig 此前已具备双路径, 复核确认无改动。

### 回读验证
start/stop/restart 类操作执行后自动回读目标机真实状态(systemctl is-active 等, 轮询窗口最长 6~8s 抗过渡态), 结果以 `verified` 字段返回 —— 杜绝"显示已执行实际没执行"。

### 其它修正
- firewalld 的 restart 由 `firewall-cmd --reload`(重载≠重启)改为 `systemctl restart firewalld`。
- `systemctl is-active` 在 inactive 时退出码为 3, 回读只依据输出文本判定。

---

## 二、性能优化

| 项 | 改动 | 实测 |
|----|------|------|
| 资源快照 | 新增 `snapshotcache.go`: per-host 2s TTL 缓存 + singleflight; SSH 采集合并为单会话脚本(10 条命令→1 次往返) | 冷 ~0.5s → 缓存内 <1ms |
| CPU 采集 | `top -bn1` 改 `/proc/stat` 双采样: 更快且为真实瞬时值; 补齐远程每核占用(此前恒为空) | — |
| 应用与容器 | 读接口 5s TTL 响应缓存(`cache.go` 中间件)+singleflight; 站点端口探测串行→并发, 超时 3s→1s | 冷 3.3s → 缓存内 0.7ms |
| netconfig 读 | 5 条命令合并为单会话哨兵脚本一次往返 | ~0.8-2s → 0.35-0.42s |
| 连接池 | 引用计数(use)+失效标记(dead)+延迟关闭: 一个请求的重连不再误关其它并发请求正在用的连接("use of closed network connection" 根因); ExecLine/ExecScript 传输层故障自动弃连重试 | 并发压测 0 错误 |
| 前端 SWR | GET 中央缓存(TTL 2.5s): 切模块/主机秒出数据; POST 自动失效同路径缓存; 资源轮询/日志尾随等实时端点排除 | 切换加载态基本消失 |

---

## 三、服务列表完整性

- `list-units` 增加 `--all`, 并用 `list-unit-files` 差集补齐"已安装但未加载"的服务 —— **停止的服务不再从列表消失**(状态筛选新增"已停止")。
- 跳过两类噪音:
  - LOAD=not-found 的悬空引用残影(模板克隆遗留的失效软链接);
  - systemctl 为异常单元输出的 ● 前缀导致的列位偏移(解析前剥离)。
- agent 与 server 共享同一实现: 解析工具下沉至 `metrics.NormUnitLine / StoppedUnitStubs / MergeStoppedUnits`, 三条数据路径(server 本地 / agent 推送 / SSH 回退)行为完全一致。

---

## 四、日志体验

- 日志弹窗切换 access/error 等多文件时整屏替换(修复: 增量去重逻辑把新文件行号全部滤掉, 表现为"选了 error 还是 access"); 同源自动刷新的增量追加行为保留。
- mysql/mariadb/postgres 补 RHEL 系日志路径变体(/var/log/mysql/mysqld.log 等)。

---

## 五、应用健康语义

- 新增 skip 分类: nginx 未运行的主机其站点不计入"异常", 明确提示"nginx 未运行, 未探测"。
- 应用与容器总览横幅改为直接列出异常/警告对象名(容器:xxx、站点:server-1(...)), 点击跳转对应子页。

---

## 六、其它

- ARM(飞腾/鲲鹏)CPU 型号读取兜底: /proc/cpuinfo 无 model name 时回退 /proc/device-tree/model(server 与 SSH 脚本双侧)。
- Makefile 新增 `agent-linux-loong64` 构建目标(龙芯)。
- kubeadm-init.yaml 模板移除无效字段 etcd.local.resources(消除 strict decoding 告警)。※注: 该文件属 k8s-setup.sh, 不在本仓库提交范围, 仅备忘。

---

## 验收结论

- go build / go vet / go test(internal 6 包) 全绿。
- 三主机(本机+2 worker)服务列表: 无重复、无残影、停止服务可见(各 ~192/288 项)。
- 防火墙 start/restart/stop 循环压测 verified 全通过, 目标机真实状态同步变化, master 零误伤。
- 并发压测(netconfig 40 连发、resources 轮询)零连接错误。
