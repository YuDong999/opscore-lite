package handlers

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Script struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Content   string   `json:"content"`
	Vars      []VarDef `json:"vars"`
	Created   string   `json:"created"`
	Updated   string   `json:"updated"`
}

type VarDef struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Type  string `json:"type"` // "number" | "text" | "select"
	Value string `json:"value"`
	Options string `json:"options"` // for select: "a,b,c"
}

var scriptDir string

func InitScripts(dir string) {
	scriptDir = filepath.Join(dir, "scripts")
	os.MkdirAll(scriptDir, 0755)
}

// GuardScriptList 列出所有脚本
func GuardScriptList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	scripts, err := listScripts()
	if err != nil {
		json.NewEncoder(w).Encode(map[string]any{"scripts": []Script{}, "error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"scripts": scripts})
}

type scriptAction struct {
	Action string `json:"action"`
	Script Script `json:"script"`
	ID     string `json:"id"`
}

// GuardScriptAction CRUD + 执行
func GuardScriptAction(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body scriptAction
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid body"})
		return
	}

	switch body.Action {
	case "save":
		s := body.Script
		if s.ID == "" {
			s.ID = fmt.Sprintf("script_%d", time.Now().UnixMilli())
			s.Created = time.Now().Format(time.RFC3339)
		}
		s.Updated = time.Now().Format(time.RFC3339)
		data, _ := json.MarshalIndent(s, "", "  ")
		os.WriteFile(filepath.Join(scriptDir, s.ID+".json"), data, 0644)
		json.NewEncoder(w).Encode(map[string]string{"ok": "true", "id": s.ID})

	case "delete":
		if body.ID == "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "id required"})
			return
		}
		os.Remove(filepath.Join(scriptDir, body.ID+".json"))
		json.NewEncoder(w).Encode(map[string]string{"ok": "true"})

	case "run":
		if body.ID == "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "id required"})
			return
		}
		script, err := loadScript(body.ID)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "脚本不存在"})
			return
		}
		result, err := evalScript(script)
		if err != nil {
			json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"result": result})

	default:
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "unknown action"})
	}
}

func listScripts() ([]Script, error) {
	entries, err := os.ReadDir(scriptDir)
	if err != nil {
		return nil, err
	}
	var scripts []Script
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(scriptDir, e.Name()))
		if err != nil {
			continue
		}
		var s Script
		json.Unmarshal(data, &s)
		scripts = append(scripts, s)
	}
	sort.Slice(scripts, func(i, j int) bool {
		return scripts[i].Updated > scripts[j].Updated
	})
	return scripts, nil
}

func loadScript(id string) (*Script, error) {
	data, err := os.ReadFile(filepath.Join(scriptDir, id+".json"))
	if err != nil {
		return nil, err
	}
	var s Script
	json.Unmarshal(data, &s)
	return &s, nil
}

// ── 完整公式引擎 ──

func evalScript(s *Script) (map[string]any, error) {
	vars := make(map[string]any)
	for _, v := range s.Vars {
		vars[v.Name] = v.Value
	}

	lines := strings.Split(s.Content, "\n")
	results := make(map[string]any)
	var lastOutput string

	for lineNum, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}

		// 变量赋值: LET name = expr
		if strings.HasPrefix(strings.ToUpper(line), "LET ") {
			parts := regexp.MustCompile(`^LET\s+(\w+)\s*=\s*(.+)$`).FindStringSubmatch(line)
			if parts == nil {
				return nil, fmt.Errorf("行 %d: LET 语法错误", lineNum+1)
			}
			val, err := evalExpr(parts[2], vars)
			if err != nil {
				return nil, fmt.Errorf("行 %d: %v", lineNum+1, err)
			}
			vars[parts[1]] = val
			continue
		}

		// 输出: PRINT expr 或 expr
		if strings.HasPrefix(strings.ToUpper(line), "PRINT ") {
			expr := strings.TrimSpace(line[6:])
			val, err := evalExpr(expr, vars)
			if err != nil {
				return nil, fmt.Errorf("行 %d: %v", lineNum+1, err)
			}
			lastOutput = fmt.Sprintf("%v", val)
			continue
		}

		// 默认当作表达式求值
		val, err := evalExpr(line, vars)
		if err != nil {
			return nil, fmt.Errorf("行 %d: %v", lineNum+1, err)
		}
		lastOutput = fmt.Sprintf("%v", val)
	}

	results["output"] = lastOutput
	results["vars"] = vars
	return results, nil
}

func evalExpr(expr string, vars map[string]any) (any, error) {
	expr = strings.TrimSpace(expr)

	// 字符串字面量
	if strings.HasPrefix(expr, "\"") && strings.HasSuffix(expr, "\"") {
		return expr[1 : len(expr)-1], nil
	}

	// 数字
	if n, err := strconv.ParseFloat(expr, 64); err == nil {
		return n, nil
	}

	// 变量引用
	if val, ok := vars[expr]; ok {
		return val, nil
	}

	// 函数调用: FUNC(args)
	funcRe := regexp.MustCompile(`^(\w+)\((.+)\)$`)
	if m := funcRe.FindStringSubmatch(expr); m != nil {
		fn := strings.ToUpper(m[1])
		argsStr := m[2]
		args := splitArgs(argsStr)

		// 递归求值参数
		evaluated := make([]any, 0, len(args))
		for _, a := range args {
			v, err := evalExpr(strings.TrimSpace(a), vars)
			if err != nil {
				return nil, err
			}
			evaluated = append(evaluated, v)
		}

		return callFunc(fn, evaluated)
	}

	// 简单算术: a + b, a - b, a * b, a / b
	// 先尝试找到最后一个 + 或 -（最低优先级）
	// 但要跳过括号内的
	depth := 0
	for i := len(expr) - 1; i >= 1; i-- {
		c := expr[i]
		if c == ')' {
			depth++
		} else if c == '(' {
			depth--
		} else if depth == 0 && (c == '+' || c == '-') {
			left := expr[:i]
			right := expr[i+1:]
			op := string(c)

			lv, err := evalExpr(left, vars)
			if err != nil {
				return nil, err
			}
			rv, err := evalExpr(right, vars)
			if err != nil {
				return nil, err
			}

			lf, lok := toFloat(lv)
			rf, rok := toFloat(rv)
			if lok && rok {
				if op == "+" {
					return lf + rf, nil
				}
				return lf - rf, nil
			}
			if op == "+" {
				return fmt.Sprintf("%v%v", lv, rv), nil
			}
			return nil, fmt.Errorf("不支持的运算: %v %s %v", lv, op, rv)
		}
	}

	// * / %
	depth = 0
	for i := len(expr) - 1; i >= 1; i-- {
		c := expr[i]
		if c == ')' {
			depth++
		} else if c == '(' {
			depth--
		} else if depth == 0 && (c == '*' || c == '/' || c == '%') {
			left := expr[:i]
			right := expr[i+1:]
			op := string(c)

			lv, err := evalExpr(left, vars)
			if err != nil {
				return nil, err
			}
			rv, err := evalExpr(right, vars)
			if err != nil {
				return nil, err
			}

			lf, lok := toFloat(lv)
			rf, rok := toFloat(rv)
			if lok && rok {
				switch op {
				case "*":
					return lf * rf, nil
				case "/":
					if rf == 0 {
						return nil, fmt.Errorf("除零错误")
					}
					return lf / rf, nil
				case "%":
					if rf == 0 {
						return nil, fmt.Errorf("除零错误")
					}
					return math.Mod(lf, rf), nil
				}
			}
			return nil, fmt.Errorf("不支持的运算: %v %s %v", lv, op, rv)
		}
	}

	// 括号
	if strings.HasPrefix(expr, "(") && strings.HasSuffix(expr, ")") {
		return evalExpr(expr[1:len(expr)-1], vars)
	}

	return nil, fmt.Errorf("无法解析: %s", expr)
}

func splitArgs(s string) []string {
	var args []string
	depth := 0
	start := 0
	for i, c := range s {
		switch c {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				args = append(args, s[start:i])
				start = i + 1
			}
		}
	}
	args = append(args, s[start:])
	return args
}

func toFloat(v any) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case int:
		return float64(val), true
	case string:
		f, err := strconv.ParseFloat(val, 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func callFunc(fn string, args []any) (any, error) {
	switch fn {
	case "SUM":
		total := 0.0
		for _, a := range args {
			if f, ok := toFloat(a); ok {
				total += f
			}
		}
		return total, nil

	case "AVG", "AVERAGE":
		total := 0.0
		n := 0
		for _, a := range args {
			if f, ok := toFloat(a); ok {
				total += f
				n++
			}
		}
		if n == 0 {
			return 0, nil
		}
		return total / float64(n), nil

	case "MAX":
		max := math.Inf(-1)
		for _, a := range args {
			if f, ok := toFloat(a); ok && f > max {
				max = f
			}
		}
		return max, nil

	case "MIN":
		min := math.Inf(1)
		for _, a := range args {
			if f, ok := toFloat(a); ok && f < min {
				min = f
			}
		}
		return min, nil

	case "ABS":
		if len(args) != 1 {
			return nil, fmt.Errorf("ABS 需要 1 个参数")
		}
		f, ok := toFloat(args[0])
		if !ok {
			return nil, fmt.Errorf("ABS 参数类型错误")
		}
		return math.Abs(f), nil

	case "ROUND":
		if len(args) < 1 || len(args) > 2 {
			return nil, fmt.Errorf("ROUND 需要 1-2 个参数")
		}
		f, ok := toFloat(args[0])
		if !ok {
			return nil, fmt.Errorf("ROUND 参数类型错误")
		}
		prec := 0
		if len(args) == 2 {
			if p, ok := toFloat(args[1]); ok {
				prec = int(p)
			}
		}
		mult := math.Pow(10, float64(prec))
		return math.Round(f*mult) / mult, nil

	case "SQRT":
		if len(args) != 1 {
			return nil, fmt.Errorf("SQRT 需要 1 个参数")
		}
		f, ok := toFloat(args[0])
		if !ok {
			return nil, fmt.Errorf("SQRT 参数类型错误")
		}
		return math.Sqrt(f), nil

	case "POW", "POWER":
		if len(args) != 2 {
			return nil, fmt.Errorf("POW 需要 2 个参数")
		}
		base, ok := toFloat(args[0])
		if !ok {
			return nil, fmt.Errorf("POW 参数类型错误")
		}
		exp, ok := toFloat(args[1])
		if !ok {
			return nil, fmt.Errorf("POW 参数类型错误")
		}
		return math.Pow(base, exp), nil

	case "LEN":
		if len(args) != 1 {
			return nil, fmt.Errorf("LEN 需要 1 个参数")
		}
		s, ok := args[0].(string)
		if !ok {
			return nil, fmt.Errorf("LEN 参数类型错误")
		}
		return float64(len(s)), nil

	case "UPPER":
		if len(args) != 1 {
			return nil, fmt.Errorf("UPPER 需要 1 个参数")
		}
		s, ok := args[0].(string)
		if !ok {
			return nil, fmt.Errorf("UPPER 参数类型错误")
		}
		return strings.ToUpper(s), nil

	case "LOWER":
		if len(args) != 1 {
			return nil, fmt.Errorf("LOWER 需要 1 个参数")
		}
		s, ok := args[0].(string)
		if !ok {
			return nil, fmt.Errorf("LOWER 参数类型错误")
		}
		return strings.ToLower(s), nil

	case "NOW":
		return time.Now().Format("2006-01-02 15:04:05"), nil

	case "DATE":
		return time.Now().Format("2006-01-02"), nil

	case "TIME":
		return time.Now().Format("15:04:05"), nil

	case "IF":
		if len(args) < 3 {
			return nil, fmt.Errorf("IF 需要 3 个参数: IF(cond, true, false)")
		}
		cond, ok := toFloat(args[0])
		if !ok {
			return nil, fmt.Errorf("IF 条件类型错误")
		}
		if cond != 0 {
			return args[1], nil
		}
		return args[2], nil

	case "FLOOR":
		if len(args) != 1 {
			return nil, fmt.Errorf("FLOOR 需要 1 个参数")
		}
		f, ok := toFloat(args[0])
		if !ok {
			return nil, fmt.Errorf("FLOOR 参数类型错误")
		}
		return math.Floor(f), nil

	case "CEIL", "CEILING":
		if len(args) != 1 {
			return nil, fmt.Errorf("CEIL 需要 1 个参数")
		}
		f, ok := toFloat(args[0])
		if !ok {
			return nil, fmt.Errorf("CEIL 参数类型错误")
		}
		return math.Ceil(f), nil

	default:
		return nil, fmt.Errorf("未知函数: %s", fn)
	}
}
