# GoNavi 移植说明

本目录下全部代码源自 GoNavi (https://github.com/Syngnat/GoNavi)，Apache-2.0 许可。
- import 路径已从 `GoNavi-Wails` 重写为 `opscore`
- logger/wails_adapter.go 已删除（Wails 桌面耦合）
- 详见仓库根目录 NOTICE 文件

服务层接线（ADR-001 分层）：
- `../types.go`     —— API 类型与 GoNavi 类型的解耦层
- `../service.go`   —— DBService 接口 + GoNavi 底座实现
- `../pool.go`      —— DatabasePool（按 connID 缓存 GoNavi Database 实例）
