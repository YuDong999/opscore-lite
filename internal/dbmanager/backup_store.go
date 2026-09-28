// 备份历史的持久化。与 SavedQuery 同一模式(central store 的 meta 里存一份 JSON 数组),
// 所以重启不丢 —— 备份历史丢了等于"不知道有没有备过", 那比没备份还危险。
//
// 上限: 保留最近 backupHistoryMax 条(空间占用可忽略; 老记录仍指向目标机上的文件)。
package dbmanager

import (
	"encoding/json"
	"fmt"

	"opscore/internal/central"
)

const (
	backupHistoryKey = "dbmanager:backup_history"
	backupHistoryMax = 500
)

// loadBackupRecords 读全部历史(读不到就返回空, 不报错 —— 首次运行本来就没有)。
func (h *Handlers) loadBackupRecords() []BackupRecord {
	st := h.store.store()
	if st == nil {
		return nil
	}
	raw, err := central.GetMetaString(st, backupHistoryKey)
	if err != nil || raw == "" {
		return nil
	}
	var out []BackupRecord
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// saveBackupRecord 追加一条历史。
func (h *Handlers) saveBackupRecord(rec BackupRecord) {
	st := h.store.store()
	if st == nil {
		return
	}
	list := append(h.loadBackupRecords(), rec)
	if len(list) > backupHistoryMax {
		list = list[len(list)-backupHistoryMax:]
	}
	// 切片字段一律给 [] 不给 nil: JSON 里出 null, 前端 .map 会炸(与资源模块同一个坑)
	for i := range list {
		if list[i].Includes == nil {
			list[i].Includes = []string{}
		}
		if list[i].Excludes == nil {
			list[i].Excludes = []string{}
		}
		if list[i].Tables == nil {
			list[i].Tables = []string{}
		}
	}
	b, err := json.Marshal(list)
	if err != nil {
		return
	}
	_ = central.SetMetaString(st, backupHistoryKey, string(b))
}

// pruneBackups 按保留策略清理旧备份文件, 返回被清掉的记录(供报告)。
//
// **只动同一条连接 + 同一个库**的记录: 跨连接误删别人的备份是不可接受的。
// 删文件失败不中断(记录里仍留着说明"文件可能还在"), 但会记进返回值让用户知道。
func (h *Handlers) pruneBackups(connID, database string, keep int, hostID string) []BackupRecord {
	if keep <= 0 {
		return nil
	}
	list := h.loadBackupRecords()
	victims := planPrune(list, connID, database, keep)
	if len(victims) == 0 {
		return nil
	}
	var pruned []BackupRecord
	alive := make([]BackupRecord, 0, len(list))
	dead := map[string]bool{}
	for _, v := range victims {
		dead[v.ID] = true
	}
	for _, rec := range list {
		if dead[rec.ID] {
			// 用记录里的 hostId(那是当时写文件的那台机器), 拿不到就退回当前请求的 hostID
			h2 := rec.HostID
			if h2 == "" {
				h2 = hostID
			}
			if err := removeOnTarget(h2, rec.FilePath); err == nil {
				pruned = append(pruned, rec)
				continue // 删成功才从历史里去掉
			}
			// 删不掉就留着记录 —— 让用户还能看到"这份备份文件本来在哪"
		}
		alive = append(alive, rec)
	}
	if len(pruned) > 0 {
		if b, err := json.Marshal(alive); err == nil {
			if st := h.store.store(); st != nil {
				_ = central.SetMetaString(st, backupHistoryKey, string(b))
			}
		}
	}
	return pruned
}

// errNoBackupHistory 只是给调用方一个明确的错误串(目前未用, 保留给将来的"回滚"功能)。
func errNoBackupHistory() error { return fmt.Errorf("没有备份历史") }
