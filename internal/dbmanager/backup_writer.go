// 备份写入器: 把"生成出来的字节"落到目标机的文件上。
//
// 本机 → 直接写文件; 远程 → 本机先落临时文件(边生成边压), 完成后整体推过去。
// **刻意用"先写 .part 再改名"**: 中途失败时不会留下一个看着像备份的半截文件 ——
// 那种文件比"没有备份"更危险(有人会以为它能恢复)。
package dbmanager

import (
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
)

// builtinDumpMemoryGuard 是内置路径单次推送的上限(压缩后)。
// 远程推送要把整份内容过一次 SSH stdin, 所以这条路径**不适合几十 GB 的库** ——
// 那种体量应当用原生工具(它不经过我们)。这里给个明确上限, 而不是让它把服务端吃爆。
const builtinDumpMemoryGuard = 1 << 30 // 1 GiB(压缩后)

// targetWriter 是备份内容的写入端。
type targetWriter struct {
	hostID   string
	filePath string
	local    string // 本机临时文件(远程时最终要推过去的那份)
	f        *os.File
	gz       *gzip.Writer
	isLocal  bool
	closed   bool
}

// openTargetWriter 准备写入目标机上的 filePath。
func openTargetWriter(hostID, filePath string) (*targetWriter, error) {
	ex, err := requireExecutor()
	if err != nil {
		return nil, err
	}
	isLocal := ex.IsLocal(hostID)

	// 远程时先落在本机临时目录(生成完再推); 本机时直接落在目标同目录的 .part,
	// 这样最后的改名是同文件系统内的原子操作。
	dir := filepath.Dir(filePath)
	if isLocal {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("创建目录失败: %w", err)
		}
	}
	suffix := ".part"
	if !isLocal {
		// 远程模式: 临时文件在本机, 用系统临时目录
		dir = os.TempDir()
	}
	f, err := os.CreateTemp(dir, "opscore-backup-*"+suffix)
	if err != nil {
		return nil, fmt.Errorf("创建临时文件失败: %w", err)
	}
	gz := gzip.NewWriter(f)
	return &targetWriter{
		hostID: hostID, filePath: filePath, local: f.Name(),
		f: f, gz: gz, isLocal: isLocal,
	}, nil
}

func (w *targetWriter) Write(p []byte) (int, error) { return w.gz.Write(p) }

// Close 收尾: 关 gzip → 关文件 → 落到目标位置(本机改名 / 远程推送)。任何一步失败都算失败。
func (w *targetWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	if err := w.gz.Close(); err != nil {
		_ = w.cleanup()
		return fmt.Errorf("压缩收尾失败: %w", err)
	}
	if err := w.f.Close(); err != nil {
		_ = w.cleanup()
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}

	if w.isLocal {
		if err := os.Rename(w.local, w.filePath); err != nil {
			_ = os.Remove(w.local)
			return fmt.Errorf("改名到目标路径失败: %w", err)
		}
		return nil
	}

	// 远程: 读回本机临时文件整体推送。先看大小是否超上限, 超了给明确指引。
	st, err := os.Stat(w.local)
	if err != nil {
		_ = os.Remove(w.local)
		return fmt.Errorf("取临时文件大小失败: %w", err)
	}
	if st.Size() > builtinDumpMemoryGuard {
		_ = os.Remove(w.local)
		return fmt.Errorf("内置导出的压缩结果 %.1f GB 超过单次推送上限(%.0f GB): "+
			"这种体量请改用原生工具(该库若在 MySQL/PG 上, 装了 mysqldump/pg_dump 即会自动走原生路径)",
			float64(st.Size())/(1<<30), float64(builtinDumpMemoryGuard)/(1<<30))
	}
	data, err := os.ReadFile(w.local)
	if err != nil {
		_ = os.Remove(w.local)
		return fmt.Errorf("读回临时文件失败: %w", err)
	}
	// 远端: 写 .part 再改名(同样是"只有完整文件才叫备份")
	part := w.filePath + ".part"
	ex, err := requireExecutor()
	if err != nil {
		_ = os.Remove(w.local)
		return err
	}
	if _, err := ex.RunOnHost(w.hostID, []string{"sh", "-c", "mkdir -p " + Shq(dirOf(w.filePath))}); err != nil {
		_ = os.Remove(w.local)
		return fmt.Errorf("创建目标目录失败: %w", err)
	}
	if err := execWithInput(w.hostID, "cat > "+Shq(part), data); err != nil {
		_ = os.Remove(w.local)
		return fmt.Errorf("推送备份到目标机失败: %w", err)
	}
	if _, err := ex.RunOnHost(w.hostID, []string{"sh", "-c",
		"mv -f " + Shq(part) + " " + Shq(w.filePath)}); err != nil {
		_ = os.Remove(w.local)
		return fmt.Errorf("目标机改名失败: %w", err)
	}
	_ = os.Remove(w.local)
	return nil
}

// Abort 丢弃这次写入(失败路径调用), 不留半截文件。
func (w *targetWriter) Abort() error {
	if w.closed {
		return nil
	}
	w.closed = true
	_ = w.gz.Close()
	_ = w.f.Close()
	return w.cleanup()
}

func (w *targetWriter) cleanup() error {
	return os.Remove(w.local)
}
