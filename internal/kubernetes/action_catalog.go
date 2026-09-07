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
	"strconv"
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
	Preview     PreviewFunc `json:"-"`                  // 生成"将要执行的 kubectl 命令"预览(前端确认前展示)
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

// PreviewFunc 根据已填参数生成将要执行的 kubectl 命令预览 + 可选默认值(回填表单)。
// defaults 返回形如 {"min":1,"max":4} 的字段回填; 仅在用户尚未编辑表单时应用。
type PreviewFunc func(m *Manager, rc RunCtx) (command string, defaults map[string]any, err error)

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
	registerAction(catalogDeleteNode())
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
	// 新增: taint / set-env / set-resources / set-sa / wait
	registerAction(catalogTaintAdd())
	registerAction(catalogTaintRemove())
	registerAction(catalogSetEnv())
	registerAction(catalogSetResources())
	registerAction(catalogSetSA())
	registerAction(catalogWait())
	// 新增: 创建类
	registerAction(catalogAutoscale())
	registerAction(catalogCreatePDB())
	registerAction(catalogCreateQuota())
	registerAction(catalogCreateLimitRange())
	registerAction(catalogCreateNP())
	registerAction(catalogCreateSC())
	registerAction(catalogCreatePV())
	registerAction(catalogCreatePriorityClass())
	registerAction(catalogCreateNamespace())
	registerAction(catalogExpose())
	// 新增: HPA 可视化编辑 (样例)
	registerAction(catalogUpdateHPA())
}

// ===== 现有 14 个 legacy action (与 k8s_actions.go 行为一致) =====

func catalogDelete() *ActionSpec {
	return &ActionSpec{
		Name: "delete", Label: "删除", Category: "lifecycle",
		AllowedRes: []string{"*"},
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
			{Name: "ignoreDaemonsets", Label: "忽略 DaemonSet", Type: ParamBool, Default: false,
				Help: "true 时也驱逐 DaemonSet Pod (控制器会重建); false 跳过并计数"},
			{Name: "deleteEmptyDirData", Label: "删除 emptyDir 数据", Type: ParamBool, Default: false,
				Help: "true 时允许驱逐挂载 emptyDir 的 Pod; 否则遇到即报错停止"},
			{Name: "graceSeconds", Label: "优雅期 (秒)", Type: ParamNumber, Default: 30,
				Help: "驱逐时每个 Pod 的 grace period; force=true 时强制为 0"},
		},
		Run: func(m *Manager, rc RunCtx) error {
			opt := DrainOptions{
				IgnoreDaemonsets: boolOpt(rc, "ignoreDaemonsets"),
				DeleteEmptyDir:   boolOpt(rc, "deleteEmptyDirData"),
				Force:            boolOpt(rc, "force"),
				GraceSeconds:     int64Opt(rc, "graceSeconds", 30),
			}
			_, _, err := m.NodeDrain(rc.Ctx, rc.Cluster, rc.Name, opt)
			return err
		},
	}
}

// catalogDeleteNode 删除节点: drain(可选择项) → 删除 Node 对象。
// 高危, 走 catalog 面板给足选择项 + 前端二确认。
func catalogDeleteNode() *ActionSpec {
	return &ActionSpec{
		Name: "delete-node", Label: "删除节点", Category: "lifecycle",
		AllowedRes: []string{"nodes"},
		Params: []Param{
			{Name: "force", Label: "强制驱逐", Type: ParamBool, Default: false,
				Help: "true 时跳过 PDB 与优雅期, 直接删除 Pod; false 尊重 PDB 走 Eviction"},
			{Name: "ignoreDaemonsets", Label: "忽略 DaemonSet", Type: ParamBool, Default: false,
				Help: "true 时也驱逐 DaemonSet Pod; false 跳过并计数"},
			{Name: "deleteEmptyDirData", Label: "删除 emptyDir 数据", Type: ParamBool, Default: false,
				Help: "true 时允许驱逐挂载 emptyDir 的 Pod; 否则遇到即报错停止"},
			{Name: "graceSeconds", Label: "优雅期 (秒)", Type: ParamNumber, Default: 30,
				Help: "驱逐优雅期; force=true 时强制为 0"},
		},
		Description: "排空节点(Drain)并从集群移除该节点。目标机上的 etcd/网络组件需另行处理(kubeadm reset)。",
		Run: func(m *Manager, rc RunCtx) error {
			opt := DrainOptions{
				IgnoreDaemonsets: boolOpt(rc, "ignoreDaemonsets"),
				DeleteEmptyDir:   boolOpt(rc, "deleteEmptyDirData"),
				Force:            boolOpt(rc, "force"),
				GraceSeconds:     int64Opt(rc, "graceSeconds", 30),
			}
			_, _, err := m.NodeDelete(rc.Ctx, rc.Cluster, rc.Name, opt)
			return err
		},
	}
}

// boolOpt 从 rc.Params 取 bool 型参数。
func boolOpt(rc RunCtx, key string) bool {
	v, _ := rc.Params[key].(bool)
	return v
}

// int64Opt 从 rc.Params 取数值参数, 缺省/非法回退 def。
func int64Opt(rc RunCtx, key string, def int64) int64 {
	switch v := rc.Params[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case int32:
		return int64(v)
	case string:
		var n int64
		if _, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
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

// ===== 新增: taint / set-env / set-resources / set-sa / wait =====

func catalogTaintAdd() *ActionSpec {
	return &ActionSpec{
		Name: "taint-add", Label: "添加污点", Category: "lifecycle",
		AllowedRes: []string{"nodes"},
		Params: []Param{
			{Name: "key", Label: "Taint 键", Type: ParamString, Required: true},
			{Name: "effect", Label: "效应", Type: ParamSelect, Required: true, Default: "NoSchedule",
				Options: []SelectOption{
					{Label: "NoSchedule", Value: "NoSchedule"},
					{Label: "PreferNoSchedule", Value: "PreferNoSchedule"},
					{Label: "NoExecute", Value: "NoExecute"},
				}},
			{Name: "value", Label: "值 (可空)", Type: ParamString, Default: "",
				Help: "留空则为 key:effect, 否则 key=value:effect"},
		},
		Run: func(m *Manager, rc RunCtx) error {
			k, _ := rc.Params["key"].(string)
			e, _ := rc.Params["effect"].(string)
			v, _ := rc.Params["value"].(string)
			return m.NodeTaintAdd(rc.Ctx, rc.Cluster, rc.Name, k, e, v)
		},
	}
}

func catalogTaintRemove() *ActionSpec {
	return &ActionSpec{
		Name: "taint-remove", Label: "移除污点", Category: "lifecycle",
		AllowedRes: []string{"nodes"},
		Params: []Param{
			{Name: "key", Label: "Taint 键", Type: ParamString, Required: true},
			{Name: "effect", Label: "效应 (留空=全部)", Type: ParamSelect, Default: "",
				Options: []SelectOption{
					{Label: "全部匹配 (仅按 key)", Value: ""},
					{Label: "NoSchedule", Value: "NoSchedule"},
					{Label: "PreferNoSchedule", Value: "PreferNoSchedule"},
					{Label: "NoExecute", Value: "NoExecute"},
				}},
		},
		Run: func(m *Manager, rc RunCtx) error {
			k, _ := rc.Params["key"].(string)
			e, _ := rc.Params["effect"].(string)
			return m.NodeTaintRemove(rc.Ctx, rc.Cluster, rc.Name, k, e)
		},
	}
}

func catalogSetEnv() *ActionSpec {
	return &ActionSpec{
		Name: "set-env", Label: "设置环境变量", Category: "edit",
		AllowedRes: []string{"deployments", "statefulsets", "daemonsets"},
		Params: []Param{
			{Name: "key", Label: "变量名", Type: ParamString, Required: true},
			{Name: "value", Label: "值", Type: ParamString, Required: true},
		},
		Run: func(m *Manager, rc RunCtx) error {
			k, _ := rc.Params["key"].(string)
			v, _ := rc.Params["value"].(string)
			return m.SetWorkloadEnv(rc.Ctx, rc.Cluster, rc.Res, rc.Ns, rc.Name, k, v)
		},
	}
}

func catalogSetResources() *ActionSpec {
	return &ActionSpec{
		Name: "set-resources", Label: "设置资源限制", Category: "edit",
		AllowedRes: []string{"deployments", "statefulsets", "daemonsets"},
		Params: []Param{
			{Name: "cpuReq", Label: "CPU 请求 (如 100m)", Type: ParamString, Default: "",
				Help: "留空则不修改该字段"},
			{Name: "cpuLim", Label: "CPU 上限 (如 500m)", Type: ParamString, Default: ""},
			{Name: "memReq", Label: "内存 请求 (如 64Mi)", Type: ParamString, Default: ""},
			{Name: "memLim", Label: "内存 上限 (如 128Mi)", Type: ParamString, Default: ""},
		},
		Run: func(m *Manager, rc RunCtx) error {
			cr, _ := rc.Params["cpuReq"].(string)
			cl, _ := rc.Params["cpuLim"].(string)
			mr, _ := rc.Params["memReq"].(string)
			ml, _ := rc.Params["memLim"].(string)
			return m.SetWorkloadResources(rc.Ctx, rc.Cluster, rc.Res, rc.Ns, rc.Name, cr, cl, mr, ml)
		},
	}
}

func catalogSetSA() *ActionSpec {
	return &ActionSpec{
		Name: "set-sa", Label: "设置 ServiceAccount", Category: "edit",
		AllowedRes: []string{"deployments", "statefulsets", "daemonsets"},
		Params: []Param{
			{Name: "sa", Label: "ServiceAccount 名", Type: ParamString, Required: true,
				Help: "命名空间中已存在的 ServiceAccount; 留空=default"},
		},
		Run: func(m *Manager, rc RunCtx) error {
			sa, _ := rc.Params["sa"].(string)
			return m.SetWorkloadSA(rc.Ctx, rc.Cluster, rc.Res, rc.Ns, rc.Name, sa)
		},
	}
}

func catalogWait() *ActionSpec {
	return &ActionSpec{
		Name: "wait", Label: "等待条件就绪", Category: "lifecycle",
		AllowedRes: []string{"deployments", "statefulsets", "daemonsets", "pods", "jobs", "nodes"},
		Params: []Param{
			{Name: "condition", Label: "条件类型", Type: ParamSelect, Required: true, Default: "Available",
				Options: []SelectOption{
					{Label: "Available (默认)", Value: "Available"},
					{Label: "Ready", Value: "Ready"},
					{Label: "Progressing", Value: "Progressing"},
					{Label: "Complete", Value: "Complete"},
					{Label: "PodScheduled", Value: "PodScheduled"},
				},
				Help: "Deployment → Available, Pod → Ready, Job → Complete, Node → Ready"},
			{Name: "timeoutSec", Label: "超时 (秒)", Type: ParamNumber, Default: 60,
				Min: floatPtr(5), Max: floatPtr(180)},
		},
		Run: func(m *Manager, rc RunCtx) error {
			cond, _ := rc.Params["condition"].(string)
			t, _ := toInt(rc.Params["timeoutSec"])
			return m.WaitForCondition(rc.Ctx, rc.Cluster, rc.Res, rc.Ns, rc.Name, cond, t)
		},
	}
}

// ===== 新增: 创建类 =====

func catalogAutoscale() *ActionSpec {
	return &ActionSpec{
		Name: "autoscale", Label: "HPA 自动扩缩", Category: "scale",
		AllowedRes: []string{"deployments", "statefulsets"},
		Params: []Param{
			{Name: "name", Label: "HPA 名称", Type: ParamString, Required: true},
			{Name: "min", Label: "最小副本", Type: ParamNumber, Required: true, Default: 1,
				Min: floatPtr(0), Max: floatPtr(1000)},
			{Name: "max", Label: "最大副本", Type: ParamNumber, Required: true, Default: 10,
				Min: floatPtr(1), Max: floatPtr(1000)},
			{Name: "cpuPercent", Label: "CPU 目标利用率 (%)", Type: ParamNumber, Default: 80,
				Min: floatPtr(1), Max: floatPtr(1000),
				Help: "当 Pod 平均 CPU 使用率超过此值时扩容, 低于时缩容"},
		},
		Description: "创建 HorizontalPodAutoscaler; 依赖 Pod 已设置 CPU requests",
		Run: func(m *Manager, rc RunCtx) error {
			n, _ := rc.Params["name"].(string)
			min, _ := toInt(rc.Params["min"])
			max, _ := toInt(rc.Params["max"])
			cpu, _ := toInt(rc.Params["cpuPercent"])
			return m.CreateHPA(rc.Ctx, rc.Cluster, rc.Ns, n, rc.Res, rc.Name, min, max, cpu)
		},
	}
}

func catalogCreatePDB() *ActionSpec {
	return &ActionSpec{
		Name: "create-pdb", Label: "创建 PDB", Category: "lifecycle",
		AllowedRes: []string{"deployments", "statefulsets", "daemonsets"},
		Params: []Param{
			{Name: "pdbName", Label: "PDB 名称", Type: ParamString, Required: true},
			{Name: "minAvailable", Label: "最小可用数 (与 maxUnavailable 二选一)", Type: ParamString, Default: "",
				Help: "如 1 或 50%; 与 maxUnavailable 互斥, 只填其中一个"},
			{Name: "maxUnavailable", Label: "最大不可用数", Type: ParamString, Default: "",
				Help: "如 1 或 25%; 与 minAvailable 互斥"},
		},
		Description: "创建 PodDisruptionBudget, 自动匹配工作负载的 Pod selector",
		Run: func(m *Manager, rc RunCtx) error {
			pn, _ := rc.Params["pdbName"].(string)
			ma, _ := rc.Params["minAvailable"].(string)
			mu, _ := rc.Params["maxUnavailable"].(string)
			return m.CreatePDB(rc.Ctx, rc.Cluster, rc.Ns, rc.Res, rc.Name, pn, ma, mu)
		},
	}
}

func catalogCreateQuota() *ActionSpec {
	return &ActionSpec{
		Name: "create-quota", Label: "创建 ResourceQuota", Category: "edit",
		AllowedRes: []string{"namespaces"},
		Params: []Param{
			{Name: "quotaName", Label: "Quota 名称", Type: ParamString, Required: true},
			{Name: "cpu", Label: "CPU 总量 (如 4)", Type: ParamString, Default: "",
				Help: "同时限制 requests.cpu 和 limits.cpu; 留空则不限"},
			{Name: "mem", Label: "内存 总量 (如 8Gi)", Type: ParamString, Default: ""},
			{Name: "storage", Label: "存储 总量 (如 100Gi)", Type: ParamString, Default: ""},
			{Name: "pods", Label: "Pod 数量", Type: ParamString, Default: ""},
		},
		Description: "限制命名空间内 CPU/内存/存储/Pod 总用量",
		Run: func(m *Manager, rc RunCtx) error {
			qn, _ := rc.Params["quotaName"].(string)
			cpu, _ := rc.Params["cpu"].(string)
			mem, _ := rc.Params["mem"].(string)
			stor, _ := rc.Params["storage"].(string)
			pods, _ := rc.Params["pods"].(string)
			return m.CreateResourceQuota(rc.Ctx, rc.Cluster, rc.Ns, qn, cpu, mem, stor, pods)
		},
	}
}

func catalogCreateLimitRange() *ActionSpec {
	return &ActionSpec{
		Name: "create-limitrange", Label: "创建 LimitRange", Category: "edit",
		AllowedRes: []string{"namespaces"},
		Params: []Param{
			{Name: "lrName", Label: "LimitRange 名称", Type: ParamString, Required: true},
			{Name: "defaultCpu", Label: "默认 CPU 上限", Type: ParamString, Default: "",
				Help: "未设置 limits 的容器自动获得此值; 留空则不限"},
			{Name: "defaultReqCpu", Label: "默认 CPU 请求", Type: ParamString, Default: ""},
			{Name: "maxCpu", Label: "CPU 最大值", Type: ParamString, Default: ""},
			{Name: "defaultMem", Label: "默认内存 上限", Type: ParamString, Default: ""},
			{Name: "defaultReqMem", Label: "默认内存 请求", Type: ParamString, Default: ""},
			{Name: "maxMem", Label: "内存 最大值", Type: ParamString, Default: ""},
		},
		Description: "为命名空间内未设置 limits 的容器设置默认资源限制; 至少填一个值",
		Run: func(m *Manager, rc RunCtx) error {
			n, _ := rc.Params["lrName"].(string)
			dc, _ := rc.Params["defaultCpu"].(string)
			drc, _ := rc.Params["defaultReqCpu"].(string)
			mc, _ := rc.Params["maxCpu"].(string)
			dm, _ := rc.Params["defaultMem"].(string)
			drm, _ := rc.Params["defaultReqMem"].(string)
			mm, _ := rc.Params["maxMem"].(string)
			return m.CreateLimitRange(rc.Ctx, rc.Cluster, rc.Ns, n, dc, drc, mc, dm, drm, mm)
		},
	}
}

func catalogCreateNP() *ActionSpec {
	return &ActionSpec{
		Name: "create-networkpolicy", Label: "创建 NetworkPolicy", Category: "network",
		AllowedRes: []string{"namespaces", "networkpolicies"},
		Params: []Param{
			{Name: "npName", Label: "策略名称", Type: ParamString, Required: true},
			{Name: "podSelKey", Label: "目标 Pod 标签键 (留空=全部 Pod)", Type: ParamString, Default: "",
				Help: "如 app; 留空则选择该命名空间全部 Pod"},
			{Name: "podSelValue", Label: "目标 Pod 标签值", Type: ParamString, Default: ""},
			{Name: "ingressFromKey", Label: "来源 Pod 标签键 (留空=全部来源)", Type: ParamString, Default: "",
				Help: "如 app; 留空则允许所有入站流量"},
			{Name: "ingressFromValue", Label: "来源 Pod 标签值", Type: ParamString, Default: ""},
			{Name: "ingressPorts", Label: "放行端口 (如 80/TCP,443/TCP)", Type: ParamString, Default: "",
				Help: "格式 port/protocol; 留空则放行全部端口"},
		},
		Description: "创建命名空间级网络隔离: 仅放行指定来源的入站流量",
		Run: func(m *Manager, rc RunCtx) error {
			n, _ := rc.Params["npName"].(string)
			pk, _ := rc.Params["podSelKey"].(string)
			pv, _ := rc.Params["podSelValue"].(string)
			fk, _ := rc.Params["ingressFromKey"].(string)
			fv, _ := rc.Params["ingressFromValue"].(string)
			ports, _ := rc.Params["ingressPorts"].(string)
			return m.CreateNetworkPolicy(rc.Ctx, rc.Cluster, rc.Ns, n, pk, pv, fk, fv, ports)
		},
	}
}

func catalogCreateSC() *ActionSpec {
	return &ActionSpec{
		Name: "create-sc", Label: "创建 StorageClass", Category: "edit",
		AllowedRes: []string{"storageclasses", "namespaces"},
		Params: []Param{
			{Name: "scName", Label: "StorageClass 名称", Type: ParamString, Required: true},
			{Name: "provisioner", Label: "Provisioner *", Type: ParamString, Required: true,
				Help: "如 kubernetes.io/no-provisioner / nfs / ceph.com/cephfs 等"},
			{Name: "reclaimPolicy", Label: "回收策略", Type: ParamSelect, Default: "Retain",
				Options: []SelectOption{
					{Label: "Retain (保留)", Value: "Retain"},
					{Label: "Delete (删除 PVC 时删除 PV)", Value: "Delete"},
				}},
			{Name: "volumeBindingMode", Label: "绑定模式", Type: ParamSelect, Default: "Immediate",
				Options: []SelectOption{
					{Label: "Immediate (立即绑定)", Value: "Immediate"},
					{Label: "WaitForFirstConsumer (等待调度)", Value: "WaitForFirstConsumer"},
				}},
			{Name: "allowExpansion", Label: "允许扩容", Type: ParamBool, Default: false},
			{Name: "params", Label: "参数 (JSON)", Type: ParamCode, Default: "{}",
				Help: "如 {\"type\":\"ext4\"} 或 {\"pathPattern\":\"$(PV)-$(PVC)\"}"},
		},
		Description: "创建动态存储供给类; Provisioner 字段参照所用存储插件文档",
		Run: func(m *Manager, rc RunCtx) error {
			n, _ := rc.Params["scName"].(string)
			p, _ := rc.Params["provisioner"].(string)
			rp, _ := rc.Params["reclaimPolicy"].(string)
			vbm, _ := rc.Params["volumeBindingMode"].(string)
			ae, _ := rc.Params["allowExpansion"].(bool)
			pm, _ := rc.Params["params"].(string)
			return m.CreateStorageClass(rc.Ctx, rc.Cluster, n, p, rp, vbm, ae, pm)
		},
	}
}

func catalogCreatePV() *ActionSpec {
	return &ActionSpec{
		Name: "create-pv", Label: "创建 PV", Category: "edit",
		AllowedRes: []string{"persistentvolumes", "namespaces"},
		Params: []Param{
			{Name: "pvName", Label: "PV 名称", Type: ParamString, Required: true},
			{Name: "capacity", Label: "容量", Type: ParamString, Required: true, Default: "1Gi",
				Pattern: `^[0-9]+(\.[0-9]+)?([EPTGMK]i?|i)$`},
			{Name: "accessMode", Label: "访问模式", Type: ParamSelect, Default: "ReadWriteOnce",
				Options: []SelectOption{
					{Label: "ReadWriteOnce (单节点读写)", Value: "ReadWriteOnce"},
					{Label: "ReadOnlyMany (多节点只读)", Value: "ReadOnlyMany"},
					{Label: "ReadWriteMany (多节点读写)", Value: "ReadWriteMany"},
				}},
			{Name: "storageClassName", Label: "StorageClass 名", Type: ParamString, Default: "manual",
				Help: "静态供给需手动创建 PVC 并指定此名称"},
			{Name: "mode", Label: "存储后端", Type: ParamSelect, Default: "hostPath",
				Options: []SelectOption{
					{Label: "hostPath (本机目录)", Value: "hostPath"},
					{Label: "nfs (NFS 共享)", Value: "nfs"},
					{Label: "local (本地设备)", Value: "local"},
				}},
			{Name: "hostPath", Label: "路径 (hostPath/local)", Type: ParamString, Default: "/mnt/data"},
			{Name: "nfsServer", Label: "NFS 服务器地址", Type: ParamString, Default: ""},
			{Name: "nfsPath", Label: "NFS 路径", Type: ParamString, Default: ""},
		},
		Description: "创建静态 PV; 通常需先手动在节点上准备好存储目录",
		Run: func(m *Manager, rc RunCtx) error {
			n, _ := rc.Params["pvName"].(string)
			cap_, _ := rc.Params["capacity"].(string)
			am, _ := rc.Params["accessMode"].(string)
			sc, _ := rc.Params["storageClassName"].(string)
			md, _ := rc.Params["mode"].(string)
			hp, _ := rc.Params["hostPath"].(string)
			ns, _ := rc.Params["nfsServer"].(string)
			np, _ := rc.Params["nfsPath"].(string)
			return m.CreatePersistentVolume(rc.Ctx, rc.Cluster, n, cap_, am, sc, md, hp, ns, np)
		},
	}
}

func catalogCreatePriorityClass() *ActionSpec {
	return &ActionSpec{
		Name: "create-priorityclass", Label: "创建 PriorityClass", Category: "edit",
		AllowedRes: []string{"priorityclasses", "namespaces"},
		Params: []Param{
			{Name: "pcName", Label: "名称", Type: ParamString, Required: true},
			{Name: "value", Label: "优先级值 (数字越大越高)", Type: ParamNumber, Required: true, Default: 1000000,
				Min: floatPtr(-2147483648), Max: floatPtr(2147483647)},
			{Name: "description", Label: "描述", Type: ParamString, Default: ""},
			{Name: "globalDefault", Label: "全局默认", Type: ParamBool, Default: false,
				Help: "true 则未显式设置 priorityClassName 的 Pod 使用此值"},
		},
		Run: func(m *Manager, rc RunCtx) error {
			n, _ := rc.Params["pcName"].(string)
			v, _ := toInt(rc.Params["value"])
			desc, _ := rc.Params["description"].(string)
			gd, _ := rc.Params["globalDefault"].(bool)
			return m.CreatePriorityClass(rc.Ctx, rc.Cluster, n, v, desc, gd)
		},
	}
}

func catalogCreateNamespace() *ActionSpec {
	return &ActionSpec{
		Name: "create-namespace", Label: "创建 Namespace", Category: "edit",
		AllowedRes: []string{"namespaces"},
		Params: []Param{
			{Name: "nsName", Label: "命名空间名称", Type: ParamString, Required: true,
				Pattern: `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`},
		},
		Run: func(m *Manager, rc RunCtx) error {
			n, _ := rc.Params["nsName"].(string)
			return m.CreateNamespaceObj(rc.Ctx, rc.Cluster, n)
		},
	}
}

func catalogExpose() *ActionSpec {
	return &ActionSpec{
		Name: "expose", Label: "暴露为 Service", Category: "network",
		AllowedRes: []string{"deployments", "statefulsets", "daemonsets"},
		Params: []Param{
			{Name: "svcName", Label: "Service 名称 (留空=同名)", Type: ParamString, Default: ""},
			{Name: "svcType", Label: "Service 类型", Type: ParamSelect, Default: "ClusterIP",
				Options: []SelectOption{
					{Label: "ClusterIP (集群内)", Value: "ClusterIP"},
					{Label: "NodePort (节点端口)", Value: "NodePort"},
					{Label: "LoadBalancer (负载均衡)", Value: "LoadBalancer"},
				}},
			{Name: "port", Label: "Service 端口", Type: ParamNumber, Default: 80,
				Min: floatPtr(1), Max: floatPtr(65535)},
			{Name: "targetPort", Label: "目标端口 (0=自动检测)", Type: ParamNumber, Default: 0,
				Help: "0 则读取容器 containerPort; 若无法读取则回退 80"},
		},
		Description: "为工作负载创建 Service, 自动读取 Pod selector 与容器端口",
		Run: func(m *Manager, rc RunCtx) error {
			sn, _ := rc.Params["svcName"].(string)
			st, _ := rc.Params["svcType"].(string)
			p, _ := toInt(rc.Params["port"])
			tp, _ := toInt(rc.Params["targetPort"])
			return m.CreateServiceFromWorkload(rc.Ctx, rc.Cluster, rc.Res, rc.Ns, rc.Name, sn, st, int32(p), int32(tp))
		},
	}
}

// ===== 新增: HPA 可视化编辑 (样例) =====

func catalogUpdateHPA() *ActionSpec {
	return &ActionSpec{
		Name: "update-hpa", Label: "编辑扩缩规则", Category: "scale",
		AllowedRes: []string{"horizontalpodautoscalers"},
		Params: []Param{
			{Name: "min", Label: "最小副本", Type: ParamNumber, Required: true,
				Min: floatPtr(0), Max: floatPtr(1000)},
			{Name: "max", Label: "最大副本", Type: ParamNumber, Required: true,
				Min: floatPtr(1), Max: floatPtr(1000)},
			{Name: "metricMode", Label: "扩容规则 (CPU / 内存 / 两者)", Type: ParamSelect, Default: "cpu",
				Options: []SelectOption{
					{Label: "仅 CPU 利用率", Value: "cpu"},
					{Label: "仅 内存", Value: "memory"},
					{Label: "CPU + 内存 同时", Value: "both"},
				}},
			{Name: "cpuPercent", Label: "CPU 目标利用率 (%)", Type: ParamNumber, Default: 80,
				Min: floatPtr(1), Max: floatPtr(1000),
				Help: "Pod 平均 CPU 使用率超过该值扩容, 低于缩容"},
			{Name: "memoryTarget", Label: "内存目标 (如 512Mi 或 80%)", Type: ParamString, Default: "",
				Help: "仅选内存/两者时必填; 可用绝对量(512Mi)或百分比(80%)"},
		},
		Description: "可视化编辑当前 HPA 的副本范围与扩容规则 (等价 kubectl patch, 后端 Go 原生执行)",
		Run: func(m *Manager, rc RunCtx) error {
			return m.UpdateHPA(rc.Ctx, rc.Cluster, rc.Ns, rc.Name, rc.Params)
		},
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
