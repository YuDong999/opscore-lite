package cicd

// ── nginx 流量分发可视化: 基于 `nginx -T` 全量转储的结构化探测 + upstream 块改写应用 ──
// 转储由 nginx 自身解析 include 链(比自研递归抓文件更准), 本文件只做分段与结构化。
// 应用: 引擎侧改写 upstream 块文本 → base64 推远端临时文件 → nginx -t 通过才原子替换+reload, 失败自动回滚。

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// NginxUpstreamServer upstream 内单个 server 行的结构化视图
type NginxUpstreamServer struct {
	Addr    string `json:"addr"`   // 127.0.0.1:9001
	Weight  int    `json:"weight"` // 0 = 未写 weight(nginx 默认 1, 展示按 1)
	Down    bool   `json:"down"`
	Backup  bool   `json:"backup"`
	RawArgs string `json:"rawArgs"` // 原始参数串(保底展示)
}

// NginxUpstream 一个 upstream 块
type NginxUpstream struct {
	Name    string               `json:"name"`
	LB      string               `json:"lb"` // 轮询="" / least_conn / ip_hash / random / hash …
	File    string               `json:"file"`
	Servers []NginxUpstreamServer `json:"servers"`
	Raw     string               `json:"raw"` // 原始块文本(应用时按此定位替换)
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
	Path      string             `json:"path"`
	Upstreams []NginxUpstream    `json:"upstreams"`
	Servers   []NginxServerBlock `json:"servers"`
}

// NginxProbe 探测结果
type NginxProbe struct {
	Host        string          `json:"host"`
	MainConf    string          `json:"mainConf"`
	NginxActive bool            `json:"nginxActive"` // systemd is-active == active
	Files       []NginxConfFile `json:"files"`
}

// NginxApplyEdit 单个文件的改写: 原 upstream 块文本 → 新块文本
type NginxApplyEdit struct {
	File string `json:"file"`
	Orig string `json:"orig"`
	New  string `json:"new"`
}

// NginxApplyRequest 应用请求
type NginxApplyRequest struct {
	Host     string           `json:"host"`
	MainConf string           `json:"mainConf"`
	Edits    []NginxApplyEdit `json:"edits"`
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

// nxParse 切分一层语句; block 内容递归解析
func nxParse(text string) []nxStmt {
	text = nxStripComments(text)
	var out []nxStmt
	i := 0
	for i < len(text) {
		for i < len(text) && (text[i] == ' ' || text[i] == '\t' || text[i] == '\n' || text[i] == '\r') {
			i++
		}
		if i >= len(text) {
			break
		}
		start := i
		for i < len(text) && text[i] != ';' && text[i] != '{' && text[i] != '}' {
			i++
		}
		tok := strings.TrimSpace(text[start:i])
		if i < len(text) && text[i] == '}' {
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

// nxExtractFile 从单文件文本提取 upstream/server 块(含嵌套块遍历)
func nxExtractFile(filePath, text string) NginxConfFile {
	file := NginxConfFile{Path: filePath, Upstreams: []NginxUpstream{}, Servers: []NginxServerBlock{}}
	var walk func(stmts []nxStmt)
	walk = func(stmts []nxStmt) {
		for i := range stmts {
			st := &stmts[i]
			if len(st.args) == 0 {
				if st.hasBlock {
					walk(st.children)
				}
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
						sv := NginxUpstreamServer{Addr: c.args[1], RawArgs: strings.Join(c.args[2:], " ")}
						for _, a := range c.args[2:] {
							switch {
							case a == "down":
								sv.Down = true
							case a == "backup":
								sv.Backup = true
							case strings.HasPrefix(a, "weight="):
								fmt.Sscanf(a, "weight=%d", &sv.Weight)
							}
						}
						if sv.Weight == 0 && !sv.Down {
							sv.Weight = 1
						}
						up.Servers = append(up.Servers, sv)
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
				sv := NginxServerBlock{File: filePath, ProxyPass: []string{}}
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
			}
			if st.hasBlock {
				walk(st.children)
			}
		}
	}
	walk(nxParse(text))
	return file
}

// splitNginxTDump 按 `# configuration file <path>:` 标记切分 nginx -T 转储
func splitNginxTDump(dump string) []NginxConfFile {
	const marker = "# configuration file "
	files := []NginxConfFile{}
	var cur *NginxConfFile
	var buf []string
	flush := func() {
		if cur == nil {
			return
		}
		f := nxExtractFile(cur.Path, strings.Join(buf, "\n"))
		files = append(files, f)
	}
	for _, line := range strings.Split(dump, "\n") {
		if strings.HasPrefix(line, marker) {
			flush()
			cur = &NginxConfFile{Path: strings.TrimSpace(strings.TrimPrefix(line, marker))}
			buf = nil
			continue
		}
		if cur != nil {
			buf = append(buf, line)
		}
	}
	flush()
	return files
}

// NginxProbe 探测目标主机 nginx: 运行状态 + nginx -T 全量转储结构化
func (e *Engine) NginxProbe(hostID string) (*NginxProbe, error) {
	// 运行状态双信号: systemd is-active 或 pidof(裸 nginx 启动的进程 systemd 看不到)
	out, _, err := e.ExecLineOutput(hostID, "systemctl is-active nginx 2>/dev/null || true; pidof nginx 2>/dev/null; echo ---CONF-DUMP---; nginx -T 2>&1")
	if err != nil {
		return nil, err
	}
	state, dump := out, ""
	if idx := strings.Index(out, "---CONF-DUMP---"); idx >= 0 {
		state = strings.TrimSpace(out[:idx])
		dump = out[idx+len("---CONF-DUMP---"):]
	}
	// 运行双信号: systemd active 或 pidof 有输出(裸 nginx 启动的进程 systemd 看不到)
	lines := strings.Split(strings.TrimSpace(state), "\n")
	sysdActive := len(lines) > 0 && strings.TrimSpace(lines[0]) == "active"
	pidofHit := false
	for _, l := range lines[1:] {
		if t := strings.TrimSpace(l); t != "" {
			pidofHit = true
		}
	}
	nginxActive := sysdActive || pidofHit
	probe := &NginxProbe{Host: hostID, MainConf: "/etc/nginx/nginx.conf", NginxActive: nginxActive, Files: []NginxConfFile{}}
	if strings.Contains(dump, "nginx: [emerg]") || strings.Contains(dump, "command not found") || strings.TrimSpace(dump) == "" {
		return probe, nil // nginx 未安装/配置不可读: 返回空结构(前端给出明确提示)
	}
	probe.Files = splitNginxTDump(dump)
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
