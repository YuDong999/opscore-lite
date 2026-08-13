package ansible

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	reAnsibleArrow = regexp.MustCompile(`^(\S+)\s*\|\s*(SUCCESS|FAILED|UNREACHABLE!?|CHANGED|SKIPPED)\s*=>\s*(.*)$`)
	reAnsiblePipe  = regexp.MustCompile(`^(\S+)\s*\|\s*(CHANGED|FAILED|OK|SUCCESS)\s*\|\s*rc=(\d+)\s*(?:>>|=>)?\s*$`)
)

type ansibleTaskResult struct {
	Ping    string `json:"ping"`
	Rc      int    `json:"rc"`
	Stdout  string `json:"stdout"`
	Stderr  string `json:"stderr"`
	Msg     string `json:"msg"`
	Changed bool   `json:"changed"`
}

// parseAnsibleOutput 解析 ansible CLI 文本输出为逐主机简洁结果。
// 支持两种格式:
//   - "host | SUCCESS => {json...}"  (ping/模块默认输出, payload 可跨多行)
//   - "host | CHANGED | rc=0 >>" 后跟 stdout 多行 (模块 with 输出)
//
// host 显示为清单中的 IP (映射失败时保留原名)。
func parseAnsibleOutput(out string, targetHosts []Host) []Result {
	nameToAddr := map[string]string{}
	for _, h := range targetHosts {
		nameToAddr[h.ID] = h.Addr
		if h.Alias != "" {
			nameToAddr[h.Alias] = h.Addr
		}
	}
	displayName := func(name string) string {
		if addr, ok := nameToAddr[name]; ok && addr != "" {
			return addr
		}
		return name
	}

	lines := strings.Split(out, "\n")
	var results []Result
	idxByHost := map[string]int{}
	appendResult := func(r Result) {
		if idx, ok := idxByHost[r.Host]; ok {
			results[idx] = r
			return
		}
		idxByHost[r.Host] = len(results)
		results = append(results, r)
	}
	var other []string

	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}
		if m := reAnsibleArrow.FindStringSubmatch(trimmed); m != nil {
			host, state, payload := displayName(m[1]), strings.TrimSuffix(m[2], "!"), strings.TrimSpace(m[3])
			if strings.HasPrefix(payload, "{") {
				jsonText := payload
				for i+1 < len(lines) {
					i++
					nl := strings.TrimSpace(lines[i])
					jsonText += "\n" + nl
					if strings.HasSuffix(nl, "}") {
						break
					}
				}
				appendResult(resultFromJSON(host, state, jsonText))
			} else {
				appendResult(Result{Host: host, Success: stateOK(state), Output: firstLineOr(payload, state)})
			}
			continue
		}
		if m := reAnsiblePipe.FindStringSubmatch(trimmed); m != nil {
			host, state, rc := displayName(m[1]), m[2], m[3]
			var body []string
			for i+1 < len(lines) {
				nl := strings.TrimSpace(lines[i+1])
				if nl == "" || reAnsibleArrow.MatchString(nl) || reAnsiblePipe.MatchString(nl) {
					break
				}
				body = append(body, strings.TrimSpace(lines[i+1]))
				i++
			}
			text := strings.Join(body, "\n")
			out := firstLineOr(text, "")
			if out == "" {
				out = "rc=" + rc
			}
			appendResult(Result{Host: host, Success: stateOK(state), Output: out, Stdout: text})
			continue
		}
		other = append(other, trimmed)
	}

	if len(results) == 0 {
		if strings.TrimSpace(out) == "" {
			results = []Result{{Host: "all", Success: false, Output: "无输出"}}
		} else {
			results = []Result{{Host: "all", Success: true, Output: strings.Join(other, "\n")}}
		}
	}
	return results
}

func resultFromJSON(host, state, jsonText string) Result {
	r := Result{Host: host, Success: stateOK(state)}
	var tr ansibleTaskResult
	if err := json.Unmarshal([]byte(jsonText), &tr); err != nil {
		r.Output = firstLineOr(state+": "+firstLineOr(jsonText, ""), state)
		return r
	}
	switch {
	case tr.Ping != "":
		r.Output = tr.Ping
	case tr.Rc != 0 || strings.TrimSpace(tr.Stderr) != "":
		r.Output = firstLineOr(strings.TrimSpace(tr.Stderr), tr.Msg)
		if r.Output == "" {
			r.Output = "rc=" + itoa(tr.Rc)
		}
	default:
		r.Output = firstLineOr(strings.TrimSpace(tr.Stdout), tr.Msg)
	}
	if r.Output == "" {
		r.Output = state
	}
	r.Stdout = tr.Stdout
	r.Stderr = tr.Stderr
	r.Changed = tr.Changed
	return r
}

func stateOK(state string) bool {
	return state == "SUCCESS" || state == "CHANGED" || state == "OK" || state == "SKIPPED"
}

func firstLineOr(s, fallback string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
