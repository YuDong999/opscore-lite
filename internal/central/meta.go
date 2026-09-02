package central

// 通用 meta KV 读写(供 dbmanager 等模块复用 meta 表存储 JSON 列表)。
// 保持 CentralStore 接口不动, 由具体实现(SQLiteStore / PostgresStore)提供。

import (
	"fmt"
)

// GetMetaString 读取 meta 表中指定 key 的字符串值(空字符串表示不存在)。
// 通过类型断言分发到具体实现, 避免污染 CentralStore 接口。
func GetMetaString(s CentralStore, key string) (string, error) {
	switch v := s.(type) {
	case *SQLiteStore:
		return v.get(key)
	case *PostgresStore:
		return v.get(key)
	}
	return "", fmt.Errorf("unsupported store type: %T", s)
}

// SetMetaString 写入 meta KV。返回 nil 表示成功。
func SetMetaString(s CentralStore, key, val string) error {
	switch v := s.(type) {
	case *SQLiteStore:
		return v.set(key, val)
	case *PostgresStore:
		return v.set(key, val)
	}
	return fmt.Errorf("unsupported store type: %T", s)
}
