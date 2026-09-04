package kubernetes

// 平台侧开关: 边车容器 (kubectl debug) 允许?
// 关闭 = 运维开关, 默认 false。生产集群如果 kubeconfig 没有 pods/ephemeralcontainers
// 的 update 权限, 或担心调试用镜像被注入, 可以走环境变量 OPSCORE_K8S_EPHEMERAL_ALLOW=1
// 显式打开。
//
// 读一次缓存, 无需热重载 (要重载请重启 opscore)。后续若需要持久化, 接入 central store。

import (
	"os"
	"strconv"
	"strings"
	"sync"
)

var (
	ephemeralAllow     bool
	ephemeralAllowOnce sync.Once
)

// EphemeralContainersAllowed 平台侧允许边车?
func EphemeralContainersAllowed() bool {
	ephemeralAllowOnce.Do(func() {
		v := strings.TrimSpace(os.Getenv("OPSCORE_K8S_EPHEMERAL_ALLOW"))
		if v == "" {
			ephemeralAllow = false
			return
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			ephemeralAllow = false
			return
		}
		ephemeralAllow = b
	})
	return ephemeralAllow
}
