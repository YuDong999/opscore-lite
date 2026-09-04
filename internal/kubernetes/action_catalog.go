package kubernetes

// 资源 × 操作 动作目录: 前后端共享同一份"做什么 / 怎么做"的事实。
// 思路来自 kubectl 子命令模型, 落地为"按钮 + 表单 schema"由前端按资源类型动态渲染。
//
// 设计原则:
//   - 不破坏既有 K8sResourceActionHandler (legacy single-action 端点),
//     新增一个并行端点 K8sActionCatalogRunHandler 走查表分发, 方便逐步迁移。
//   - Action.Run 收到 dynamic client + 集群元数据, 不感知 HTTP; 易于单测。
//   - Params 描述前端表单 schema, 与 Action 一一对应; 必填校验在 Run 入口做。
//   - AllowedRes 用 "*" 表示所有资源, 否则精确白名单 (res 名来自 ValidResource)。
//   - 涉及"编辑/写"的操作全部要求 write permission, 由 RBAC (SelfSubjectAccessReview) 验证;
//     开箱 false 是为了不在 opscore 默认打穿 K8s ACL。

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// ParamType 表单字段类型
type ParamType string

const (
	ParamString  ParamType = "string"
	ParamNumber  ParamType = "number"
	ParamBool    ParamType = "bool"
	ParamSelect  ParamType = "select"
	ParamCode    ParamType = "code"   // 多行(YAML/JSON/patch)
	ParamSecret  ParamType = "secret" // 输入遮罩
)

// Param 表单字段 schema
type Param struct {
	Name     string           `json:"name"`
	Label    string           `json:"label"`
	Type     ParamType        `json:"type"`
	Required bool             `json:"required,omitempty"`
	Default  any              `json:"default,omitempty"`
	Help     string           `json:"help,omitempty"`
	Options  []SelectOption   `json:"options,omitempty"` // type=select 时使用
	Min      *float64         `json:"min,omitempty"`
	Max      *float64         `json:"max,omitempty"`
	Pattern  string           `json:"pattern,omitempty"` // 简单正则
}

type SelectOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// ActionSpec 描述一个动作
type ActionSpec struct {
	Name        string   `json:"name"`                  // "edit-yaml" / "patch" / "label" ...
	Label       string   `json:"label"`                 // "编辑 YAML" / "合并 Patch" ...
	Category    string   `json:"category"`              // "lifecycle" | "edit" | "scale" | "debug" | "stream" | "network"
	AllowedRes  []string `json:"allowedRes"`            // 资源白名单, 含 "*"
	Params      []Param  `json:"params"`                // 表单 schema
	Run         RunFunc  `json:"-"`                     // 实际执行
	RequiresTTY bool     `json:"requiresTTY,omitempty"` // 是否走 WS(交互/流式)
	Description string   `json:"description,omitempty"`
}

// RunCtx 提供给 Run 的执行上下文
type RunCtx struct {
	Ctx     context.Context
	Cluster string
	Res     string
	Ns      string
	Name    string
	Params  map[string]any
}

// RunFunc 真正的执行; m 可为 nil (只读/前端 dryrun)
type RunFunc func(m *Manager, rc RunCtx) error

// ActionCatalog 资源 × 操作 矩阵
//
// 现有 14 个 legacy action 也注册为 Catalog 项, 但前端可以继续走老端点。
// 这里把"kubectl 风格" 命名标准化, 便于前端动态渲染:
//   delete, scale, restart, rollback, pause, resume, suspend, trigger, rerun, expand,
//   cordon, uncordon, drain, setImage,
//   edit-yaml, patch, label, annotate, set-image (别名),
//   ephemeral-add, ephemeral-remove, exec-tty, logs-stream, port-forward, cp
var ActionCatalog = map[string]*ActionSpec{}

func registerAction(a *ActionSpec) {
	if a == nil || a.Name == "" {
		return
	}
	ActionCatalog[a.Name] = a
}

// CatalogByRes 给定 res 返回它支持的 actions (按 Category 排序)
func CatalogByRes(res string) []*ActionSpec {
	out := make([]*ActionSpec, 0, len(ActionCatalog))
	for _, a := range ActionCatalog {
		if a.allowsRes(res) {
			out = append(out, a)
		}
	}
	// 排序: category 优先, name 次之
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[i].Category > out[j].Category ||
				(out[i].Category == out[j].Category && out[i].Name > out[j].Name) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func (a *ActionSpec) allowsRes(res string) bool {
	for _, r := range a.AllowedRes {
		if r == "*" || r == res {
			return true
		}
	}
	return false
}

// AllowsRes 公开方法, 供外部 (HTTP handler) 判断
func (a *ActionSpec) AllowsRes(res string) bool {
	return a.allowsRes(res)
}

// init 注册所有 action
func init() {
	registerAction(catalogDelete())
	registerAction(catalogScale())
	registerAction(catalogRestart())
	registerAction(catalogRollback())
	registerAction(catalogPause())
	registerAction(catalogResume())
	registerAction(catalogSuspend())
	registerAction(catalogTrigger())
	registerAction(catalogRerun())
	registerAction(catalogExpand())
	registerAction(catalogCordon())
	registerAction(catalogUncordon())
	registerAction(catalogDrain())
	registerAction(catalogSetImage())
	registerAction(catalogEditYAML())
	registerAction(catalogPatch())
	registerAction(catalogLabel())
	registerAction(catalogAnnotate())
	registerAction(catalogEphemeralAdd())
	registerAction(catalogEphemeralRemove())
	registerAction(catalogExecTTY())
	registerAction(catalogLogsStream())
	registerAction(catalogPortForward())
	registerAction(catalogCp())
}

// ===== 现有 14 个 legacy action (与 k8s_actions.go 行为一致) =====

func catalogDelete() *ActionSpec {
	return &ActionSpec{
		Name: "delete", Label: "删除", Category: "lifecycle",
		AllowedRes: []string{"pods", "deployments", "statefulsets", "daemonsets", "jobs", "cronjobs",
			"services", "ingresses", "configmaps", "secrets",
			"persistentvolumeclaims", "namespaces", "networkpolicies", "resourcequotas",
			"serviceaccounts", "roles", "rolebindings"},
		Params: []Param{
			{Name: "force", Label: "强制 (grace=0)", Type: ParamBool,
				Help: "Pod 强制删除, 跳过 30s 优雅期; 其他资源无效"},
		},
		Run: func(m *Manager, rc RunCtx) error {
			force, _ := rc.Params["force"].(bool)
			return m.DeleteResource(rc.Ctx, rc.Cluster, rc.Res, rc.Ns, rc.Name, force)
		},
	}
}

func catalogScale() *ActionSpec {
	return &ActionSpec{
		Name: "scale", Label: "扩缩容", Category: "scale",
		AllowedRes: []string{"deployments", "statefulsets"},
		Params: []Param{
			{Name: "replicas", Label: "副本数", Type: ParamNumber, Required: true,
				Min: floatPtr(0), Max: floatPtr(1000)},
		},
		Run: func(m *Manager, rc RunCtx) error {
			n, _ := toInt(rc.Params["replicas"])
			return m.ScaleWorkload(rc.Ctx, rc.Cluster, rc.Res, rc.Ns, rc.Name, int32(n))
		},
	}
}

func catalogRestart() *ActionSpec {
	return &ActionSpec{
		Name: "restart", Label: "滚动重启", Category: "lifecycle",
		AllowedRes: []string{"deployments", "statefulsets", "daemonsets"},
		Run: func(m *Manager, rc RunCtx) error {
			return m.RestartWorkload(rc.Ctx, rc.Cluster, rc.Res, rc.Ns, rc.Name)
		},
	}
}

func catalogRollback() *ActionSpec {
	return &ActionSpec{
		Name: "rollback", Label: "回滚", Category: "lifecycle",
		AllowedRes: []string{"deployments", "statefulsets"},
		Params: []Param{
			{Name: "revision", Label: "目标版本 (0=上一版)", Type: ParamNumber, Default: 0,
				Min: floatPtr(0), Max: floatPtr(1000)},
		},
		Run: func(m *Manager, rc RunCtx) error {
			n, _ := toInt(rc.Params["revision"])
			return m.RolloutUndo(rc.Ctx, rc.Cluster, rc.Ns, rc.Name, int64(n))
		},
	}
}

func catalogPause() *ActionSpec {
	return &ActionSpec{
		Name: "pause", Label: "暂停发布", Category: "lifecycle",
		AllowedRes: []string{"deployments"},
		Run: func(m *Manager, rc RunCtx) error {
			return m.RolloutPause(rc.Ctx, rc.Cluster, "deployments", rc.Ns, rc.Name, true)
		},
	}
}

func catalogResume() *ActionSpec {
	return &ActionSpec{
		Name: "resume", Label: "恢复发布", Category: "lifecycle",
		AllowedRes: []string{"deployments"},
		Run: func(m *Manager, rc RunCtx) error {
			return m.RolloutPause(rc.Ctx, rc.Cluster, "deployments", rc.Ns, rc.Name, false)
		},
	}
}

func catalogSuspend() *ActionSpec {
	return &ActionSpec{
		Name: "suspend", Label: "挂起 / 恢复", Category: "lifecycle",
		AllowedRes: []string{"cronjobs"},
		Params: []Param{
			{Name: "suspend", Label: "是否挂起", Type: ParamBool, Default: true},
		},
		Run: func(m *Manager, rc RunCtx) error {
			v, _ := rc.Params["suspend"].(bool)
			return m.SuspendCronJob(rc.Ctx, rc.Cluster, rc.Ns, rc.Name, v)
		},
	}
}

func catalogTrigger() *ActionSpec {
	return &ActionSpec{
		Name: "trigger", Label: "立即触发", Category: "lifecycle",
		AllowedRes: []string{"cronjobs"},
		Run: func(m *Manager, rc RunCtx) error {
			_, err := m.TriggerCronJob(rc.Ctx, rc.Cluster, rc.Ns, rc.Name)
			return err
		},
	}
}

func catalogRerun() *ActionSpec {
	return &ActionSpec{
		Name: "rerun", Label: "重跑", Category: "lifecycle",
		AllowedRes: []string{"jobs"},
		Run: func(m *Manager, rc RunCtx) error {
			_, err := m.RerunJob(rc.Ctx, rc.Cluster, rc.Ns, rc.Name)
			return err
		},
	}
}

func catalogExpand() *ActionSpec {
	return &ActionSpec{
		Name: "expand", Label: "扩容存储", Category: "scale",
		AllowedRes: []string{"persistentvolumeclaims"},
		Params: []Param{
			{Name: "storage", Label: "新容量 (如 10Gi)", Type: ParamString, Required: true,
				Pattern: `^[0-9]+(\.[0-9]+)?([EPTGMK]i?|i)$`},
		},
		Run: func(m *Manager, rc RunCtx) error {
			s, _ := rc.Params["storage"].(string)
			return m.ExpandPVC(rc.Ctx, rc.Cluster, rc.Ns, rc.Name, s)
		},
	}
}

func catalogCordon() *ActionSpec {
	return &ActionSpec{
		Name: "cordon", Label: "Cordon 封锁调度", Category: "lifecycle",
		AllowedRes: []string{"nodes"},
		Run: func(m *Manager, rc RunCtx) error { return m.NodeCordon(rc.Ctx, rc.Cluster, rc.Name, true) },
	}
}

func catalogUncordon() *ActionSpec {
	return &ActionSpec{
		Name: "uncordon", Label: "Uncordon 解除", Category: "lifecycle",
		AllowedRes: []string{"nodes"},
		Run: func(m *Manager, rc RunCtx) error { return m.NodeCordon(rc.Ctx, rc.Cluster, rc.Name, false) },
	}
}

func catalogDrain() *ActionSpec {
	return &ActionSpec{
		Name: "drain", Label: "Drain 排空", Category: "lifecycle",
		AllowedRes: []string{"nodes"},
		Params: []Param{
			{Name: "force", Label: "强制", Type: ParamBool, Default: false,
				Help: "true 时使用 grace=0 删除 Pod; false 优雅"},
		},
		Run: func(m *Manager, rc RunCtx) error {
			_, _, err := m.NodeDrain(rc.Ctx, rc.Cluster, rc.Name)
			return err
		},
	}
}

func catalogSetImage() *ActionSpec {
	return &ActionSpec{
		Name: "set-image", Label: "更新镜像", Category: "lifecycle",
		AllowedRes: []string{"deployments", "statefulsets", "daemonsets"},
		Params: []Param{
			{Name: "image", Label: "镜像 (name:tag)", Type: ParamString, Required: true},
		},
		Run: func(m *Manager, rc RunCtx) error {
			s, _ := rc.Params["image"].(string)
			return m.SetWorkloadImage(rc.Ctx, rc.Cluster, rc.Res, rc.Ns, rc.Name, s)
		},
	}
}

// ===== 新增: 通用化 / 修补 / 调试 =====

// edit-yaml 任意资源直接编辑 YAML 后 save
// Secret 走脱敏规则: data 字段展示为 **N bytes**, 保存时回填原值, 用户改了也不算
func catalogEditYAML() *ActionSpec {
	return &ActionSpec{
		Name: "edit-yaml", Label: "编辑 YAML", Category: "edit",
		AllowedRes: []string{"*"},
		Params: []Param{
			{Name: "yaml", Label: "YAML", Type: ParamCode, Required: true,
				Help: "完整资源 YAML; name/namespace 必须与原资源一致"},
		},
		Run: func(m *Manager, rc RunCtx) error {
			s, _ := rc.Params["yaml"].(string)
			return m.UpdateResourceYAML(rc.Ctx, rc.Cluster, rc.Res, rc.Ns, rc.Name, s)
		},
	}
}

// patch 任意资源走 patch, 4 种 patchType; 解锁 label/annotate/toleration/affinity/nodeSelector/...
func catalogPatch() *ActionSpec {
	return &ActionSpec{
		Name: "patch", Label: "合并 Patch", Category: "edit",
		AllowedRes: []string{"*"},
		Params: []Param{
			{Name: "patchType", Label: "Patch 类型", Type: ParamSelect, Required: true, Default: "strategic-merge",
				Options: []SelectOption{
					{Label: "strategic-merge (推荐)", Value: "strategic-merge"},
					{Label: "merge (JSON)", Value: "merge"},
					{Label: "json-patch (RFC 6902)", Value: "json-patch"},
					{Label: "json", Value: "json"},
				},
				Help: "strategic-merge: K8s 默认; json-patch: [{op,path,value}] 数组"},
			{Name: "patch", Label: "Patch body", Type: ParamCode, Required: true,
				Help: "按 patchType 写; strategic-merge 写 spec 子结构; json-patch 写 JSON Patch 数组"},
		},
		Run: func(m *Manager, rc RunCtx) error {
			pt, _ := rc.Params["patchType"].(string)
			patch, _ := rc.Params["patch"].(string)
			return m.PatchResource(rc.Ctx, rc.Cluster, rc.Res, rc.Ns, rc.Name, pt, patch)
		},
	}
}

// label 任意资源 label, 走 strategic-merge
func catalogLabel() *ActionSpec {
	return &ActionSpec{
		Name: "label", Label: "打标签", Category: "edit",
		AllowedRes: []string{"*"},
		Params: []Param{
			{Name: "key", Label: "标签键", Type: ParamString, Required: true,
				Pattern: `^[a-zA-Z0-9]([-a-zA-Z0-9_.]{0,61}[a-zA-Z0-9])?(/[a-zA-Z0-9]([-a-zA-Z0-9_.]{0,61}[a-zA-Z0-9])?)?$`},
			{Name: "value", Label: "标签值", Type: ParamString, Default: "",
				Help: "留空 = 移除该标签"},
			{Name: "overwrite", Label: "允许覆盖已有", Type: ParamBool, Default: false,
				Help: "默认 false, 已存在同 key 报错; true 强制覆盖"},
		},
		Run: func(m *Manager, rc RunCtx) error {
			k, _ := rc.Params["key"].(string)
			v, _ := rc.Params["value"].(string)
			ow, _ := rc.Params["overwrite"].(bool)
			return m.LabelResource(rc.Ctx, rc.Cluster, rc.Res, rc.Ns, rc.Name, k, v, ow)
		},
	}
}

func catalogAnnotate() *ActionSpec {
	return &ActionSpec{
		Name: "annotate", Label: "打注解", Category: "edit",
		AllowedRes: []string{"*"},
		Params: []Param{
			{Name: "key", Label: "注解键", Type: ParamString, Required: true,
				Pattern: `^[a-zA-Z0-9]([-a-zA-Z0-9_.]{0,61}[a-zA-Z0-9])?(/[a-zA-Z0-9]([-a-zA-Z0-9_.]{0,61}[a-zA-Z0-9])?)?$`},
			{Name: "value", Label: "注解值", Type: ParamString, Default: ""},
			{Name: "overwrite", Label: "允许覆盖已有", Type: ParamBool, Default: false},
		},
		Run: func(m *Manager, rc RunCtx) error {
			k, _ := rc.Params["key"].(string)
			v, _ := rc.Params["value"].(string)
			ow, _ := rc.Params["overwrite"].(bool)
			return m.AnnotateResource(rc.Ctx, rc.Cluster, rc.Res, rc.Ns, rc.Name, k, v, ow)
		},
	}
}

// ephemeral-add Pod 加一个 ephemeral container (kubectl debug 等价)
func catalogEphemeralAdd() *ActionSpec {
	return &ActionSpec{
		Name: "ephemeral-add", Label: "添加边车容器", Category: "debug",
		AllowedRes: []string{"pods"},
		Params: []Param{
			{Name: "containerName", Label: "容器名", Type: ParamString, Required: true,
				Pattern: `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`},
			{Name: "image", Label: "镜像 (含 tag)", Type: ParamString, Required: true,
				Help: "如 nicolaka/netshoot:latest / busybox:1.36 / docker.io/library/bash:5"},
			{Name: "command", Label: "启动命令 (shell 形式)", Type: ParamString, Default: "sh",
				Help: "容器 ENTRYPOINT; 默认 sh"},
			{Name: "targetContainerName", Label: "共享命名空间的目标容器 (可选)", Type: ParamString, Default: "",
				Help: "K8s ≥ 1.23 推荐指定; 共享 PID/Network"},
		},
		Run: func(m *Manager, rc RunCtx) error {
			name, _ := rc.Params["containerName"].(string)
			image, _ := rc.Params["image"].(string)
			cmd, _ := rc.Params["command"].(string)
			target, _ := rc.Params["targetContainerName"].(string)
			return m.EphemeralAdd(rc.Ctx, rc.Cluster, rc.Ns, rc.Name, name, image, cmd, target)
		},
	}
}

func catalogEphemeralRemove() *ActionSpec {
	return &ActionSpec{
		Name: "ephemeral-remove", Label: "移除边车容器", Category: "debug",
		AllowedRes: []string{"pods"},
		Params: []Param{
			{Name: "containerName", Label: "边车容器名", Type: ParamString, Required: true},
		},
		Run: func(m *Manager, rc RunCtx) error {
			name, _ := rc.Params["containerName"].(string)
			return m.EphemeralRemove(rc.Ctx, rc.Cluster, rc.Ns, rc.Name, name)
		},
	}
}

// exec-tty 交互式 exec, 走 WS; 前端 xterm.js
func catalogExecTTY() *ActionSpec {
	return &ActionSpec{
		Name: "exec-tty", Label: "进入终端 (TTY)", Category: "stream",
		AllowedRes: []string{"pods"},
		RequiresTTY: true,
		Params: []Param{
			{Name: "container", Label: "容器名", Type: ParamString, Default: "",
				Help: "留空选首个容器"},
			{Name: "command", Label: "命令 (默认 sh)", Type: ParamString, Default: "sh"},
			{Name: "rows", Label: "终端行数", Type: ParamNumber, Default: 24,
				Min: floatPtr(5), Max: floatPtr(200)},
			{Name: "cols", Label: "终端列数", Type: ParamNumber, Default: 80,
				Min: floatPtr(20), Max: floatPtr(500)},
		},
		Run: nil, // WS 单独通道
		Description: "kubectl exec -it 等价, 走 WebSocket; 前端 xterm.js 终端",
	}
}

// logs-stream 日志流式跟随, 走 WS
func catalogLogsStream() *ActionSpec {
	return &ActionSpec{
		Name: "logs-stream", Label: "日志 (流式跟随)", Category: "stream",
		AllowedRes: []string{"pods"},
		RequiresTTY: true,
		Params: []Param{
			{Name: "container", Label: "容器名", Type: ParamString, Default: ""},
			{Name: "tail", Label: "起始行数", Type: ParamNumber, Default: 200,
				Min: floatPtr(1), Max: floatPtr(10000)},
			{Name: "previous", Label: "上一次实例", Type: ParamBool, Default: false},
			{Name: "follow", Label: "跟随", Type: ParamBool, Default: true},
			{Name: "timestamps", Label: "带时间戳", Type: ParamBool, Default: true},
		},
		Run: nil,
		Description: "kubectl logs -f 等价, 走 WebSocket",
	}
}

// port-forward 把 Pod/Service 端口映射到本机端口; 走 WS, opscore 自动开本地端口
func catalogPortForward() *ActionSpec {
	return &ActionSpec{
		Name: "port-forward", Label: "端口转发", Category: "network",
		AllowedRes: []string{"pods", "services"},
		RequiresTTY: true,
		Params: []Param{
			{Name: "ports", Label: "端口映射 (local:remote, 逗号分隔)", Type: ParamString, Required: true,
				Help: "如 8080:80 (本机 8080 → Pod 80); 多对用逗号"},
			{Name: "address", Label: "本地监听地址", Type: ParamString, Default: "127.0.0.1",
				Help: "默认 127.0.0.1; 改成 0.0.0.0 暴露所有网卡(慎用)"},
		},
		Run: nil,
		Description: "kubectl port-forward 等价, 走 WebSocket; opscore 服务端开端口",
	}
}

// cp Pod 文件互拷, 走 WS + tar
func catalogCp() *ActionSpec {
	return &ActionSpec{
		Name: "cp", Label: "文件互拷", Category: "stream",
		AllowedRes: []string{"pods"},
		RequiresTTY: true,
		Params: []Param{
			{Name: "container", Label: "容器名", Type: ParamString, Default: ""},
			{Name: "direction", Label: "方向", Type: ParamSelect, Required: true, Default: "from-pod",
				Options: []SelectOption{
					{Label: "从 Pod 下载 → 本机", Value: "from-pod"},
					{Label: "本机上传 → Pod", Value: "to-pod"},
				}},
			{Name: "podPath", Label: "Pod 内路径", Type: ParamString, Required: true,
				Help: "如 /etc/hosts; 上传时若末尾为目录则 tar -x 自动建"},
			{Name: "localPath", Label: "本机路径", Type: ParamString, Required: true,
				Help: "如 /tmp/hosts.bak; 相对路径基于服务端 cwd"},
		},
		Run: nil,
		Description: "kubectl cp 等价, 走 WebSocket + tar",
	}
}

// ===== 工具 =====

func floatPtr(v float64) *float64 { return &v }

func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int32:
		return int(x), true
	case int64:
		return int(x), true
	case float32:
		return int(x), true
	case float64:
		return int(x), true
	case string:
		var n int
		_, err := fmt.Sscanf(x, "%d", &n)
		return n, err == nil
	}
	return 0, false
}

// ParamDefaults 把 params schema 的 Default 应用到 inputs, 缺省视为零值
func ParamDefaults(params []Param) map[string]any {
	out := map[string]any{}
	for _, p := range params {
		if p.Default != nil {
			out[p.Name] = p.Default
		}
	}
	return out
}

// ValidateParams 必填校验
func ValidateParams(spec *ActionSpec, got map[string]any) error {
	for _, p := range spec.Params {
		v, present := got[p.Name]
		if p.Required && (!present || v == nil || v == "") {
			return fmt.Errorf("缺少必填参数: %s (%s)", p.Name, p.Label)
		}
		if p.Pattern != "" && present {
			s, ok := v.(string)
			if !ok {
				return fmt.Errorf("参数 %s 应为字符串", p.Name)
			}
			if s != "" {
				if re, err := regexp.Compile(p.Pattern); err == nil && !re.MatchString(s) {
					return fmt.Errorf("参数 %s 不符合格式 %s", p.Name, p.Pattern)
				}
			}
		}
	}
	return nil
}

// StringStringReuse 占位避免 imports lint
var _ = strings.TrimSpace
