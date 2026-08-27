package central

import "encoding/json"

type CentralStore interface {
	// 认证
	GetToken() (string, error)
	SetToken(token string) error

	// 模块状态
	GetModuleState(id string) (bool, error)
	SetModuleState(id string, active bool) error
	GetAllModuleStates() (map[string]bool, error)

	// 健康检查
	Ping() error
	Close() error

	// K8s 集群注册表(元数据)
	GetK8sClusters() ([]K8sCluster, error)
	SetK8sClusters(clusters []K8sCluster) error

	// 迁移
	Export() (map[string]json.RawMessage, error)
	Import(data map[string]json.RawMessage) error
}
