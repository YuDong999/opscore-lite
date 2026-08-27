package central

// K8s 集群注册表(元数据)。凭据(kubeconfig 文件)不进 DB, 只存 <dataDir>/kubeconfigs/<id>.yaml。
// 复用 meta KV(key=k8s:clusters, 值为 JSON 数组), Export/Import 自动携带。

import (
	"encoding/json"
	"fmt"
)

type K8sCluster struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	APIServer string `json:"apiServer"`
	Version   string `json:"version"`
	Status    string `json:"status"` // ready | unreachable
	CreatedAt int64  `json:"createdAt"`
}

const k8sClustersKey = "k8s:clusters"

func decodeK8sClusters(raw string) ([]K8sCluster, error) {
	if raw == "" {
		return []K8sCluster{}, nil
	}
	var out []K8sCluster
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("central: decode k8s clusters: %w", err)
	}
	return out, nil
}

func encodeK8sClusters(cs []K8sCluster) (string, error) {
	b, err := json.Marshal(cs)
	if err != nil {
		return "", fmt.Errorf("central: encode k8s clusters: %w", err)
	}
	return string(b), nil
}

func (s *SQLiteStore) GetK8sClusters() ([]K8sCluster, error) {
	raw, err := s.get(k8sClustersKey)
	if err != nil {
		return nil, err
	}
	return decodeK8sClusters(raw)
}

func (s *SQLiteStore) SetK8sClusters(cs []K8sCluster) error {
	raw, err := encodeK8sClusters(cs)
	if err != nil {
		return err
	}
	return s.set(k8sClustersKey, raw)
}

func (s *PostgresStore) GetK8sClusters() ([]K8sCluster, error) {
	raw, err := s.get(k8sClustersKey)
	if err != nil {
		return nil, err
	}
	return decodeK8sClusters(raw)
}

func (s *PostgresStore) SetK8sClusters(cs []K8sCluster) error {
	raw, err := encodeK8sClusters(cs)
	if err != nil {
		return err
	}
	return s.set(k8sClustersKey, raw)
}
