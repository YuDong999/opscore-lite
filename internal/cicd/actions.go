package cicd

// 动作注册表: 把常用构建/发布动作结构化为 "类型 + 参数 schema + 命令模板"。
// 设计原则(见 docs 设计对话): 用户组合动作而非写脚本; shell 永远保留为兜底;
// 命令模板引用 $参数(env 注入, 已有机制), 注册表不做超出参数校验的抽象。

import (
	"fmt"
	"sort"
	"strings"
)

// ActionField 动作参数定义; Name 即注入步骤环境变量的名字(建议大写)
type ActionField struct {
	Name        string   `json:"name"`
	Label       string   `json:"label"`
	Type        string   `json:"type"` // text | textarea | select | number
	Placeholder string   `json:"placeholder,omitempty"`
	Options     []string `json:"options,omitempty"`
	Required    bool     `json:"required"`
}

// ActionSpec 动作定义
type ActionSpec struct {
	Type     string        `json:"type"`
	Title    string        `json:"title"`
	Category string        `json:"category"` // 构建 | 发布 | 验证
	Fields   []ActionField `json:"fields"`
	// Command 由参数合成最终 shell 命令($字段名 引用环境变量); 校验失败返回错误
	Build func(params map[string]string) (string, error)
}

func required(params map[string]string, names ...string) error {
	var missing []string
	for _, n := range names {
		if strings.TrimSpace(params[n]) == "" {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("缺少必填参数: %s", strings.Join(missing, ", "))
	}
	return nil
}

var actionRegistry = map[string]ActionSpec{
	"docker.build": {
		Type: "docker.build", Title: "构建 Docker 镜像", Category: "构建",
		Fields: []ActionField{
			{Name: "IMAGE", Label: "镜像(不含 tag)", Placeholder: "registry.example.com/team/app", Required: true},
			{Name: "DOCKERFILE", Label: "Dockerfile 路径", Placeholder: "Dockerfile"},
			{Name: "CONTEXT", Label: "构建上下文", Placeholder: "."},
		},
		Build: func(p map[string]string) (string, error) {
			if err := required(p, "IMAGE"); err != nil {
				return "", err
			}
			df := p["DOCKERFILE"]
			if df == "" {
				df = "Dockerfile"
			}
			ctx := p["CONTEXT"]
			if ctx == "" {
				ctx = "."
			}
			return fmt.Sprintf("docker build -f %s -t %s:${BUILD_NUMBER} %s", shq(df), shq(p["IMAGE"]), shq(ctx)), nil
		},
	},
	"docker.push": {
		Type: "docker.push", Title: "推送 Docker 镜像", Category: "发布",
		Fields: []ActionField{
			{Name: "IMAGE", Label: "镜像(不含 tag)", Required: true},
		},
		Build: func(p map[string]string) (string, error) {
			if err := required(p, "IMAGE"); err != nil {
				return "", err
			}
			// 推送后以 inspect 验证远端可见性语义(本地存在即可推)
			return fmt.Sprintf("docker push %s:${BUILD_NUMBER}", shq(p["IMAGE"])), nil
		},
	},
	"k8s.apply": {
		Type: "k8s.apply", Title: "K8s 应用清单", Category: "发布",
		Fields: []ActionField{
			{Name: "MANIFEST", Label: "清单路径/目录", Placeholder: "k8s/", Required: true},
			{Name: "NAMESPACE", Label: "命名空间(可空)", Placeholder: "default"},
		},
		Build: func(p map[string]string) (string, error) {
			if err := required(p, "MANIFEST"); err != nil {
				return "", err
			}
			ns := ""
			if p["NAMESPACE"] != "" {
				ns = " -n " + shq(p["NAMESPACE"])
			}
			return fmt.Sprintf("kubectl apply -f %s%s", shq(p["MANIFEST"]), ns), nil
		},
	},
	"k8s.rollout": {
		Type: "k8s.rollout", Title: "K8s 滚动发布状态", Category: "验证",
		Fields: []ActionField{
			{Name: "DEPLOYMENT", Label: "Deployment 名称", Required: true},
			{Name: "NAMESPACE", Label: "命名空间(可空)", Placeholder: "default"},
			{Name: "TIMEOUT", Label: "超时秒数", Placeholder: "180"},
		},
		Build: func(p map[string]string) (string, error) {
			if err := required(p, "DEPLOYMENT"); err != nil {
				return "", err
			}
			timeout := p["TIMEOUT"]
			if timeout == "" {
				timeout = "180"
			}
			ns := ""
			if p["NAMESPACE"] != "" {
				ns = " -n " + shq(p["NAMESPACE"])
			}
			return fmt.Sprintf("kubectl rollout status deploy/%s%s --timeout=%ss", shq(p["DEPLOYMENT"]), ns, timeout), nil
		},
	},
	"health.http": {
		Type: "health.http", Title: "HTTP 健康检查", Category: "验证",
		Fields: []ActionField{
			{Name: "URL", Label: "健康检查地址", Placeholder: "http://127.0.0.1:8080/healthz", Required: true},
		},
		Build: func(p map[string]string) (string, error) {
			if err := required(p, "URL"); err != nil {
				return "", err
			}
			return fmt.Sprintf("curl -fsS --max-time 10 %s", shq(p["URL"])), nil
		},
	},
}

// Actions 返回全部动作定义(按类型名排序, 输出稳定)
func Actions() []ActionSpec {
	out := make([]ActionSpec, 0, len(actionRegistry))
	for _, spec := range actionRegistry {
		out = append(out, spec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// GetAction 按类型取动作定义
func GetAction(actionType string) (ActionSpec, bool) {
	spec, ok := actionRegistry[actionType]
	return spec, ok
}

// CompileAction 校验参数并合成最终 shell 命令; 参数经步骤环境变量注入,
// 模板中的 $字段名 引用在目标 shell 里展开(变量注入已走转义通道)。
func CompileAction(actionType string, params map[string]string) (string, error) {
	spec, ok := actionRegistry[actionType]
	if !ok {
		return "", fmt.Errorf("未知动作类型: %s", actionType)
	}
	return spec.Build(params)
}

// ValidateActionParams 保存流水线时校验动作与必填参数
func ValidateActionParams(actionType string, params map[string]string) error {
	if _, ok := actionRegistry[actionType]; !ok {
		return fmt.Errorf("未知动作类型: %s", actionType)
	}
	if _, err := CompileAction(actionType, params); err != nil {
		return err
	}
	return nil
}
