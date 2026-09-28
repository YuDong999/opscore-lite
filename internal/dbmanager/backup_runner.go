// 备份执行器: **一个执行器, 两个数据源**(见 backup.go 顶部的设计说明)。
//
//   runNative()  —— 目标机上的 mysqldump / pg_dump, 输出经 gzip 落盘
//   runBuiltin() —— 我们自己生成 DDL + INSERT, 同样经 gzip 落盘
//
// 两条路都收在 runBackup() 里: 探测工具 → 选路径 → 执行 → **校验产物** → 记录。
// 调用方(HTTP handler)不关心走的哪条, 只看 BackupRecord 里的 Mode/Includes/Excludes。
package dbmanager

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// BackupRequest 一次备份请求。
type BackupRequest struct {
	ConnID       string   `json:"connId"`
	Database     string   `json:"database"`
	Tables       []string `json:"tables,omitempty"`       // 空 = 全库
	HostID       string   `json:"hostId,omitempty"`       // 备份在哪台机器上跑/落在哪台机器上
	Dir          string   `json:"dir,omitempty"`          // 目标机上的目录, 默认 /var/backups/opscore
	Keep         int      `json:"keep,omitempty"`         // 保留最近 N 份(同连接同库), 默认 5
	ForceBuiltin bool     `json:"forceBuiltin,omitempty"` // 显式要求走内置导出(为了跨引擎一致或复现)
}

// runBackup 是唯一的入口: 探测 → 选路 → 执行 → 校验 → 记录。
func (h *Handlers) runBackup(ctx context.Context, req BackupRequest) BackupRecord {
	start := time.Now()
	rec := BackupRecord{
		ID:        newID(),
		ConnID:    req.ConnID,
		Database:  req.Database,
		Tables:    splitTableArgs(req.Tables),
		HostID:    req.HostID,
		StartedAt: start.Unix(),
	}

	conn, err := h.store.Get(req.ConnID)
	if err != nil {
		rec.Status, rec.Error = "failed", "连接不存在: "+err.Error()
		return rec
	}
	rec.ConnName = conn.Info.Name
	rec.Engine = string(conn.Info.Engine)

	if !validBackupTarget(req.Database) {
		rec.Status, rec.Error = "failed", "库名非法"
		return rec
	}
	for _, t := range rec.Tables {
		if !validBackupTarget(t) {
			rec.Status, rec.Error = "failed", "表名非法: "+t
			return rec
		}
	}
	dir := strings.TrimSpace(req.Dir)
	if dir == "" {
		dir = backupDefaultDir
	}
	if !validBackupPath(dir) {
		rec.Status, rec.Error = "failed", "目标目录非法(只允许常规字符的绝对路径): "+dir
		return rec
	}
	filePath := strings.TrimSuffix(dir, "/") + "/" + backupFileName(rec.Engine, req.Database, start)
	if !validBackupPath(filePath) {
		rec.Status, rec.Error = "failed", "备份路径非法: "+filePath
		return rec
	}
	rec.FilePath = filePath

	// ── 选路: 能调原生工具就调, 否则内置导出 ──
	// 注意: ForceBuiltin 让用户能显式选内置路径(跨引擎一致 / 复现问题), 不必依赖探测结果。
	mode := BackupModeBuiltin
	tool, version := "", ""
	if !req.ForceBuiltin {
		if t, v, ok := detectNativeBackupTool(req.HostID, rec.Engine); ok {
			mode, tool, version = BackupModeNative, t, v
		}
	}
	rec.Mode = mode
	rec.Tool = tool
	if version != "" {
		rec.Tool = tool + " (" + version + ")"
	}
	// 这两行是"能不能完整恢复"的显式声明, 前端必须展示
	rec.Includes = mode.IncludesFor(rec.Engine)
	rec.Excludes = mode.ExcludesFor(rec.Engine)
	rec.Consistency = mode.ConsistencyFor(rec.Engine)

	var runErr error
	if mode == BackupModeNative {
		runErr = h.runNative(ctx, req, rec, tool)
	} else {
		runErr = h.runBuiltin(ctx, req, rec)
	}
	rec.DurationMs = time.Since(start).Milliseconds()
	if runErr != nil {
		rec.Status, rec.Error = "failed", runErr.Error()
		return rec
	}

	// ── 校验产物: 只看退出码不够(工具可能报错但退出 0) ──
	if err := verifyBackupIsUsable(req.HostID, filePath); err != nil {
		rec.Status, rec.Error = "failed", err.Error()
		return rec
	}
	rec.SizeBytes = fileSizeOnTarget(req.HostID, filePath)
	rec.Status = "ok"
	return rec
}

// runNative 走目标机上的原生工具。参数里的一致性开关是**必须项**(见 nativeBackupArgv)。
func (h *Handlers) runNative(ctx context.Context, req BackupRequest, rec BackupRecord, tool string) error {
	argv := nativeBackupArgv(tool, rec.Engine, req.Database, rec.Tables, rec.FilePath)
	cmd := buildBackupShellCommand(argv, rec.FilePath)
	if _, err := RunOnTargetQuiet(req.HostID, []string{"sh", "-c", cmd}); err != nil {
		return fmt.Errorf("%s 执行失败: %w", tool, err)
	}
	return nil
}

// runBuiltin 走内置 SQL 导出: 表结构(DDL) + 数据(INSERT), 全部经 gzip 落盘。
//
// **保真度不如原生**(不含触发器/例程/权限) —— 这一点不在这里解释, 而是由 Includes/Excludes
// 如实报给用户; 这里只负责"把能导的导对"。
func (h *Handlers) runBuiltin(ctx context.Context, req BackupRequest, rec BackupRecord) error {
	db, engine, err := h.pool.AcquireForSync(req.ConnID)
	if err != nil {
		return fmt.Errorf("连接不可用: %w", err)
	}
	dialect := syncDialectOf(engine)
	if dialect == "" {
		return fmt.Errorf("该引擎暂不支持内置导出")
	}

	// 表清单: 调用方给了就用, 没给就枚举全库
	tables := rec.Tables
	if len(tables) == 0 {
		all, err := db.GetTables(req.Database)
		if err != nil {
			return fmt.Errorf("枚举表失败: %w", err)
		}
		tables = splitTableArgs(all)
	}
	if len(tables) == 0 {
		return fmt.Errorf("库里没有可导出的表")
	}

	// 在目标机上开一条 gzip 落盘的通道, 边生成边写。
	// 用临时文件 + 改名: 中途失败时不会留下一个"看着像备份"的半截文件。
	tmpPath := rec.FilePath + ".part"
	if _, err := RunOnTargetQuiet(req.HostID, []string{"sh", "-c", "mkdir -p " + Shq(dirOf(rec.FilePath))}); err != nil {
		return fmt.Errorf("创建目标目录失败: %w", err)
	}

	// 内置导出是"逐表生成 SQL + 逐批写入", 走 Go 侧拼接(表可能很大, 所以按批 flush)。
	// 这里不用 shell 管道: SQL 是我们自己生成的, 没有几十 GB 的外部工具输出要中转。
	w, err := openTargetWriter(req.HostID, tmpPath)
	if err != nil {
		return err
	}
	writeErr := h.emitBuiltinDump(ctx, db, engine, dialect, req.Database, tables, w)
	if cerr := w.Close(); cerr != nil && writeErr == nil {
		writeErr = cerr
	}
	if writeErr != nil {
		_ = w.Abort()
		return writeErr
	}
	// 改名: 只有完整写完的文件才叫备份
	if _, err := RunOnTargetQuiet(req.HostID, []string{"sh", "-c",
		"mv -f " + Shq(tmpPath) + " " + Shq(rec.FilePath)}); err != nil {
		return fmt.Errorf("落盘改名失败: %w", err)
	}
	return nil
}
