package cicd

// ── nginx 流量分发可视化: 远端配置探测 + upstream 块改写应用 ──
// 解析器: 词法级(注释剥离 + ;/{/} 切分), 支持 include 递归(相对目录 + glob, 深度≤3)。
// 应用: 引擎侧改写 upstream 块文本 → base64 推远端临时文件 → nginx -t 通过才原子替换+reload, 失败自动回滚。

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
)

// NginxUpstreamServer upstream 内单个 server 行的结构化视图
type NginxUpstreamServer struct {
	Addr    string `json:"addr"`    // 127.0.0.1:9001
	Weight  int    `json:"weight"`  // 0 = 未写 weight(nginx 默认 1, 展示按 1)
	Down    bool   `json:"down"`
	Backup  bool   `json:"backup"`
	RawArgs string `json:"rawArgs"` // 原始参数串(保底展示)
}

// NginxUpstream 一个 upstream 块
type NginxUpstream struct {
	Name   string               `json:"name"`
	LB     string               `json:"lb"` // 轮询="" / least_conn / ip_hash / random / hash …
	File   string               `json:"file"`
	Servers []NginxUpstreamServer `json:"servers"`
	Raw    string               `json:"raw"` // 原始块文本(应用时按此定位替换)
}

// NginxServerBlock server 块(只读展示: 监听与转发目标)
type NginxServerBlock struct {
	Listen     string   `json:"listen"`
	ServerName string   `json:"serverName"`
	ProxyPass  []string `json:"proxyPass"`
	File       string   `json:"file"`
}

// NginxConfFile 一个配置文件及其解析结果
type NginxConfFile struct {
	Path      string            `json:"path"`
	Upstreams []NginxUpstream   `json:"upstreams"`
	Servers   []NginxServerBlock `json:"servers"`
}

// NginxProbe 探测结果
type NginxProbe struct {
	Host    string         `json:"host"`
	MainConf string        `json:"mainConf"`
	Files   []NginxConfFile `json:"files"`
}

// NginxApplyEdit 单个文件的改写: 原 upstream 块文本 → 新块文本
type NginxApplyEdit struct {
	File string `json:"file"`
	Orig string `json:"orig"`
	New  string `json:"new"`
}

// NginxApplyRequest 应用请求
type NginxApplyRequest struct {
	Host    string          `json:"host"`
	MainConf string         `json:"mainConf"`
	Edits   []NginxApplyEdit `json:"edits"`
}

// ── 极简 nginx 词法: 剥注释, 按 ; / { / } 切分 ──

type nxStmt struct {
	args     []string
	children []nxStmt
	hasBlock bool
}

func nxStripComments(text string) string {
	var b strings.Builder
	inS, inD := false, false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c == '\\' && (inS || inD) && i+1 < len(text) {
			b.WriteByte(c)
			i++
			b.WriteByte(text[i])
			continue
		}
		if !inD && c == '\'' {
			inS = !inS
		} else if !inS && c == '"' {
			inD = !inD
		} else if !inS && !inD && c == '#' {
			for i < len(text) && text[i] != '\n' {
				i++
			}
			b.WriteByte('\n')
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// nxParse 切分一层语句; block 内容以原始文本递归解析
func nxParse(text string) []nxStmt {
	text = nxStripComments(text)
	var out []nxStmt
	i := 0
	for i < len(text) {
		// 跳空白
		for i < len(text) && (text[i] == ' ' || text[i] == '\t' || text[i] == '\n' || text[i] == '\r') {
			i++
		}
		if i >= len(text) {
			break
		}
		// 读 token 到 ; 或 {
		start := i
		for i < len(text) && text[i] != ';' && text[i] != '{' && text[i] != '}' {
			i++
		}
		tok := strings.TrimSpace(text[start:i])
		if i < len(text) && text[i] == '}' {
			// 当前层结束, 剩余交还上层
			if tok != "" {
				out = append(out, nxStmt{args: strings.Fields(tok)})
			}
			i++
			return out
		}
		if i >= len(text) {
			break
		}
		if text[i] == ';' {
			i++
			if tok != "" {
				out = append(out, nxStmt{args: strings.Fields(tok)})
			}
			continue
		}
		// block: '{' → 找配对 '}'
		depth := 1
		i++
		bodyStart := i
		for i < len(text) && depth > 0 {
			switch text[i] {
			case '{':
				depth++
			case '}':
				depth--
			case '\'':
				i++
				for i < len(text) && text[i] != '\'' {
					i++
				}
			case '"':
				i++
				for i < len(text) && text[i] != '"' {
					i++
				}
			}
			i++
		}
		body := text[bodyStart:min(i, len(text))]
		st := nxStmt{hasBlock: true}
		if tok != "" {
			st.args = strings.Fields(tok)
		}
		st.children = nxParse(body)
		out = append(out, st)
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// nxWalk 递归提取 upstream/server 块并解析 include(深度≤3)
func (e *Engine) nxWalk(host string, filePath, text string, depth int, probe *NginxProbe, seen map[string]bool) {
	file := NginxConfFile{Path: filePath}
	stmts := nxParse(text)
	for i := range stmts {
		st := &stmts[i]
		if len(st.args) == 0 {
			continue
		}
		switch st.args[0] {
		case "upstream":
			if len(st.args) < 2 {
				continue
			}
			up := NginxUpstream{Name: st.args[1], File: filePath}
			var raw strings.Builder
			raw.WriteString("upstream " + st.args[1] + " {")
			for _, c := range st.children {
				if len(c.args) == 0 {
					continue
				}
				if c.args[0] == "server" && len(c.args) >= 2 {
					s := NginxUpstreamServer{Addr: c.args[1], RawArgs: strings.Join(c.args[2:], " ")}
					for _, a := range c.args[2:] {
						switch {
						case a == "down":
							s.Down = true
						case a == "backup":
							s.Backup = true
						case strings.HasPrefix(a, "weight="):
							fmt.Sscanf(a, "weight=%d", &s.Weight)
						}
					}
					if s.Weight == 0 && !s.Down {
						s.Weight = 1
					}
					up.Servers = append(up.Servers, s)
					raw.WriteString("\n    server " + c.args[1] + " " + strings.Join(c.args[2:], " ") + ";")
				} else if c.args[0] != "server" {
					up.LB = c.args[0]
					raw.WriteString("\n    " + strings.Join(c.args, " ") + ";")
				}
			}
			raw.WriteString("\n}")
			up.Raw = raw.String()
			file.Upstreams = append(file.Upstreams, up)
		case "server":
			sv := NginxServerBlock{File: filePath}
			for _, c := range st.children {
				if len(c.args) == 0 {
					continue
				}
				switch c.args[0] {
				case "listen":
					if len(c.args) >= 2 {
						sv.Listen = c.args[1]
					}
				case "server_name":
					if len(c.args) >= 2 {
						sv.ServerName = strings.Join(c.args[1:], " ")
					}
				case "location":
					for _, g := range c.children {
						if len(g.args) >= 2 && g.args[0] == "proxy_pass" {
							sv.ProxyPass = append(sv.ProxyPass, g.args[1])
						}
					}
				}
			}
			if sv.Listen != "" {
				file.Servers = append(file.Servers, sv)
			}
		case "include":
			if depth >= 3 || len(st.args) < 2 {
				continue
			}
			pattern := st.args[1]
			if !strings.HasPrefix(pattern, "/") {
				pattern = path.Join(path.Dir(filePath), pattern)
			}
			// 远端 glob 展开
			out, rc, err := e.ExecLineOutput(host, "ls -1 "+shq(pattern)+" 2>/dev/null")
			if err != nil || rc != 0 {
				continue
			}
			for _, f := range strings.Split(strings.TrimSpace(out), "\n") {
				f = strings.TrimSpace(f)
				if f == "" || seen[f] {
					continue
				}
				seen[f] = true
				if content, ferr := e.nxFetch(host, f); ferr == nil {
					e.nxWalk(host, f, content, depth+1, probe, seen)
				}
			}
		}
	}
	probe.Files = append(probe.Files, file)
}

// nxFetch 远端取文件内容(base64 传输防编码问题)
func (e *Engine) nxFetch(host, filePath string) (string, error) {
	out, rc, err := e.ExecLineOutput(host, "base64 < "+shq(filePath)+" 2>/dev/null | tr -d '\\n'")
	if err != nil || rc != 0 {
		return "", fmt.Errorf("读取失败: %s", filePath)
	}
	dec, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(out))
	if derr != nil {
		return "", derr
	}
	return string(dec), nil
}

// NginxProbe 探测目标主机 nginx 配置结构
func (e *Engine) NginxProbe(hostID string) (*NginxProbe, error) {
	// 主配置定位: nginx -V 的 --conf-path, 兜底 /etc/nginx/nginx.conf
	out, _, err := e.ExecLineOutput(hostID, "nginx -V 2>&1 | grep -oE '\\-\\-conf-path=[^ ]+' | head -1 | cut -d= -f2")
	main := "/etc/nginx/nginx.conf"
	if err == nil {
		if v := strings.TrimSpace(out); v != "" {
			main = v
		}
	}
	probe := &NginxProbe{Host: hostID, MainConf: main}
	text, ferr := e.nxFetch(hostID, main)
	if ferr != nil {
		return nil, fmt.Errorf("读取主配置失败: %w", ferr)
	}
	seen := map[string]bool{main: true}
	e.nxWalk(hostID, main, text, 0, probe, seen)
	// 文件按路径排序稳定输出
	sort.Slice(probe.Files, func(i, j int) bool { return probe.Files[i].Path < probe.Files[j].Path })
	return probe, nil
}

// NginxApply 应用 upstream 改写: 逐文件 base64 推 .new → nginx -t → 原子替换 → reload(失败回滚)
func (e *Engine) NginxApply(hostID string, req *NginxApplyRequest) error {
	if len(req.Edits) == 0 {
		return fmt.Errorf("没有需要应用的修改")
	}
	script := "set -e; "
	for i, ed := range req.Edits {
		b64 := base64.StdEncoding.EncodeToString([]byte(ed.New))
		tmp := fmt.Sprintf("%s.opscore-new-%d", ed.File, i)
		script += fmt.Sprintf("echo %s | base64 -d > %s; ", shq(b64), shq(tmp))
	}
	script += "nginx -t >/dev/null 2>&1; "
	for i, ed := range req.Edits {
		bak := fmt.Sprintf("%s.opscore-bak-%d", ed.File, i)
		script += fmt.Sprintf("cp %s %s; mv %s.opscore-new-%d %s; ", shq(ed.File), shq(bak), shq(ed.File), i, shq(ed.File))
	}
	script += "(nginx -s reload 2>/dev/null || nginx); "
	for i, ed := range req.Edits {
		script += fmt.Sprintf("rm -f %s.opscore-bak-%d; ", shq(ed.File), i)
	}
	script += "echo APPLY_OK"
	out, rc, err := e.ExecLineOutput(hostID, script)
	if err != nil {
		return err
	}
	if rc != 0 || !strings.Contains(out, "APPLY_OK") {
		// nginx -t 失败: .bak 尚未删(脚本 set -e 中断), 回滚还原
		rollback := "set -e; "
		for i, ed := range req.Edits {
			bak := fmt.Sprintf("%s.opscore-bak-%d", ed.File, i)
			rollback += fmt.Sprintf("if [ -f %s ]; then mv %s %s; fi; ", shq(bak), shq(bak), shq(ed.File))
		}
		_, _, _ = e.ExecLineOutput(hostID, rollback)
		detail := strings.TrimSpace(out)
		if len(detail) > 200 {
			detail = detail[:200]
		}
		return fmt.Errorf("应用失败(已回滚): %s", detail)
	}
	return nil
}

// marshalNginxJSON 便捷封装(避免到处 json.Marshal 错误处理)
func marshalNginxJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
