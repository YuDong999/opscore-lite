// Docker 能力补齐: 单端点表格驱动的 misc 操作。
// 全部经 RunOnTarget 落在所选主机; 二进制(镜像导出/文件外拷)以 base64 串传输, 单次上限 100MB。
// 端点: POST /api/plugins/containers/docker/tool/action

package handlers

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

var reDockerName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.\-/]{0,200}$`)

type dockerToolBody struct {
	Host       string            `json:"host"`
	Scope      string            `json:"scope"` // container | image | network | system
	Action     string            `json:"action"`
	Name       string            `json:"name"` // 容器名 / 网络名 / 镜像(部分场景)
	Image      string            `json:"image"`
	Network    string            `json:"network"`
	Container  string            `json:"container"`
	Path       string            `json:"path"` // cp 容器内路径 / save 导出用镜像走 Image
	Dir        string            `json:"dir"`
	Alias      string            `json:"alias"`
	Force      bool              `json:"force"`
	Env        map[string]string `json:"env"`
	RemoveEnv  []string          `json:"removeEnv"`
	ArchiveB64 string            `json:"archiveB64"` // cp-in / load 的上传内容
}

func DockerToolActionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginGuard(containersPluginID, w) {
		return
	}
	var b dockerToolBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": "invalid body"})
		return
	}
	rtCmd := dockerOrPodman(b.Host)
	if msg := runtimeReadOnly(rtCmd); msg != "" && b.Scope != "system" {
		WriteJSON(w, map[string]any{"ok": false, "error": msg})
		return
	}
	// 鉴权共享/临时文件命名
	tmpName := func() string {
		return fmt.Sprintf("/tmp/opscore-rt-%d-%d", time.Now().UnixNano(), os.Getpid())
	}
	chk := func(s string) bool { return s != "" && reDockerName.MatchString(s) }

	audit := func(action, detail string, err error) { dockerAudit(b.Host, action, detail, err) }

	switch b.Scope {
	case "container":
		if b.Action != "prune" && (!chk(b.Name) || b.Name == "") {
			writeErr(w, "container 名非法", http.StatusBadRequest)
			return
		}
		switch b.Action {
		case "top":
			out, err := RunOnTarget(b.Host, []string{rtCmd, "top", b.Name})
			if err != nil {
				audit("top", b.Name, err)
				WriteJSON(w, map[string]any{"ok": false, "error": lastLines(out, 8)})
				return
			}
			WriteJSON(w, map[string]any{"ok": true, "output": out})
			return
		case "pause", "unpause":
			argv := []string{rtCmd, b.Action, b.Name}
			out, err := RunOnTarget(b.Host, argv)
			if err != nil {
				WriteJSON(w, map[string]any{"ok": false, "error": lastLines(out, 8)})
				return
			}
			audit(b.Action, b.Name, nil)
			InvalidateRespCache("/api/plugins/containers/list")
			WriteJSON(w, map[string]any{"ok": true, "action": b.Action, "name": b.Name, "output": lastLines(out, 3)})
			return
		case "prune":
			out, err := RunOnTarget(b.Host, []string{rtCmd, "container", "prune", "-f"})
			if err != nil {
				WriteJSON(w, map[string]any{"ok": false, "error": lastLines(out, 8)})
				return
			}
			audit("container-prune", "all", nil)
			InvalidateRespCache("/api/plugins/containers/list")
			WriteJSON(w, map[string]any{"ok": true, "output": lastLines(out, 8)})
			return
		case "env-update":
			argv := []string{rtCmd, "update"}
			for k, v := range b.Env {
				argv = append(argv, "--env-add", k+"="+v)
			}
			for _, k := range b.RemoveEnv {
				argv = append(argv, "--env-rm", k)
			}
			argv = append(argv, b.Name)
			out, err := RunOnTarget(b.Host, argv)
			if err != nil {
				WriteJSON(w, map[string]any{"ok": false, "error": lastLines(out, 8)})
				return
			}
			audit("env-update", b.Name, nil)
			WriteJSON(w, map[string]any{"ok": true, "name": b.Name, "output": lastLines(out, 3)})
			return
		case "cp-out", "cp-in":
			if b.Path == "" || len(b.Path) > 1024 || !chk(b.Path) {
				writeErr(w, "path 非法", http.StatusBadRequest)
				return
			}
			if b.Action == "cp-out" {
				out, err := RunOnTarget(b.Host, []string{rtCmd, "cp", b.Name + ":" + b.Path, "-"})
				if err != nil {
					WriteJSON(w, map[string]any{"ok": false, "error": lastLines(out, 8)})
					return
				}
				data := []byte(out)
				if len(data) > 100<<20 {
					WriteJSON(w, map[string]any{"ok": false, "error": "文件超过 100MB 上限, 请用 docker cp 直连"})
					return
				}
				audit("cp-out", b.Name+":"+b.Path, nil)
				base := b.Path
				if i := strings.LastIndex(base, "/"); i >= 0 {
					base = base[i+1:]
				}
				WriteJSON(w, map[string]any{
					"ok": true, "action": "cp-out", "name": b.Name, "base": base,
					"archive": base64.StdEncoding.EncodeToString(data), "size": len(data),
				})
				return
			}
			if b.ArchiveB64 == "" {
				writeErr(w, "archiveB64 为空", http.StatusBadRequest)
				return
			}
			data, err := base64.StdEncoding.DecodeString(b.ArchiveB64)
			if err != nil || len(data) == 0 || len(data) > 100<<20 {
				WriteJSON(w, map[string]any{"ok": false, "error": "上传内容非法或超过 100MB"})
				return
			}
			tmp := tmpName() + ".tar"
			if err := CicdPush(r.Context(), b.Host, tmp, data); err != nil {
				WriteJSON(w, map[string]any{"ok": false, "error": "写入宿主机临时文件失败: " + err.Error()})
				return
			}
			defer RunOnTarget(b.Host, []string{"rm", "-f", tmp})
			dir := b.Dir
			if dir == "" {
				dir = b.Path
			}
			if dir[len(dir)-1] != '/' {
				dir += "/"
			}
			out, err := RunOnTarget(b.Host, []string{rtCmd, "cp", tmp, b.Name + ":" + dir})
			if err != nil {
				WriteJSON(w, map[string]any{"ok": false, "error": lastLines(out, 8)})
				return
			}
			audit("cp-in", b.Name+":"+dir, nil)
			WriteJSON(w, map[string]any{"ok": true, "action": "cp-in", "name": b.Name, "dir": dir, "output": lastLines(out, 3)})
			return
		}
		writeErr(w, "未支持 container action: "+b.Action, http.StatusBadRequest)
		return

	case "image":
		if b.Image != "" && (!reDockerImage.MatchString(b.Image) || strings.Contains(b.Image, "..")) {
			writeErr(w, "非法镜像名", http.StatusBadRequest)
			return
		}
		switch b.Action {
		case "prune":
			out, err := RunOnTarget(b.Host, []string{rtCmd, "image", "prune", "-f"})
			if err != nil {
				WriteJSON(w, map[string]any{"ok": false, "error": lastLines(out, 8)})
				return
			}
			audit("image-prune", "all", nil)
			InvalidateRespCache("/api/plugins/containers/images")
			WriteJSON(w, map[string]any{"ok": true, "output": lastLines(out, 8)})
			return
		case "save":
			out, err := RunOnTarget(b.Host, []string{rtCmd, "save", b.Image})
			if err != nil {
				WriteJSON(w, map[string]any{"ok": false, "error": lastLines(out, 8)})
				return
			}
			data := []byte(out)
			if len(data) > 100<<20 {
				WriteJSON(w, map[string]any{"ok": false, "error": "镜像超过 100MB 上限, 请在宿主机执行 docker save -o"})
				return
			}
			audit("save", b.Image, nil)
			tag := strings.ReplaceAll(b.Image, "/", "_")
			WriteJSON(w, map[string]any{"ok": true, "action": "save",
				"fileName": tag + ".tar", "archive": base64.StdEncoding.EncodeToString(data), "size": len(data)})
			return
		case "load":
			var data []byte
			var err error
			if b.ArchiveB64 != "" {
				data, err = base64.StdEncoding.DecodeString(b.ArchiveB64)
				if err != nil || len(data) == 0 || len(data) > 100<<20 {
					WriteJSON(w, map[string]any{"ok": false, "error": "上传内容非法或超过 100MB"})
					return
				}
			} else if chk(b.Path) {
				data, err = ReadRemoteFileBytes(b.Host, b.Path)
				if err != nil {
					WriteJSON(w, map[string]any{"ok": false, "error": "读取镜像文件失败: " + err.Error()})
					return
				}
			} else {
				writeErr(w, "需提供 archiveB64(上传) 或 path(宿主机已有镜像 tar)", http.StatusBadRequest)
				return
			}
			tmp := tmpName() + ".tar"
			if err := CicdPush(r.Context(), b.Host, tmp, data); err != nil {
				WriteJSON(w, map[string]any{"ok": false, "error": "写入宿主机临时文件失败: " + err.Error()})
				return
			}
			defer RunOnTarget(b.Host, []string{"rm", "-f", tmp})
			out, err := RunOnTarget(b.Host, []string{rtCmd, "load", "-i", tmp})
			if err != nil {
				WriteJSON(w, map[string]any{"ok": false, "error": lastLines(out, 8)})
				return
			}
			audit("load", b.Path+"|upload", nil)
			InvalidateRespCache("/api/plugins/containers/images")
			WriteJSON(w, map[string]any{"ok": true, "action": "load", "output": lastLines(out, 6)})
			return
		}
		writeErr(w, "未支持 image action: "+b.Action, http.StatusBadRequest)
		return

	case "network":
		if !chk(b.Network) || !chk(b.Container) {
			writeErr(w, "network/container 名非法", http.StatusBadRequest)
			return
		}
		switch b.Action {
		case "connect":
			argv := []string{rtCmd, "network", "connect"}
			if b.Alias != "" {
				argv = append(argv, "--alias", b.Alias)
			}
			argv = append(argv, b.Network, b.Container)
			out, err := RunOnTarget(b.Host, argv)
			if err != nil {
				WriteJSON(w, map[string]any{"ok": false, "error": lastLines(out, 8)})
				return
			}
			audit("network-connect", b.Network+"→"+b.Container, nil)
			InvalidateRespCache("/api/plugins/containers/docker/networks")
			WriteJSON(w, map[string]any{"ok": true, "output": lastLines(out, 3)})
			return
		case "disconnect":
			argv := []string{rtCmd, "network", "disconnect"}
			if b.Force {
				argv = append(argv, "--force")
			}
			argv = append(argv, b.Network, b.Container)
			out, err := RunOnTarget(b.Host, argv)
			if err != nil {
				WriteJSON(w, map[string]any{"ok": false, "error": lastLines(out, 8)})
				return
			}
			audit("network-disconnect", b.Network+"→"+b.Container, nil)
			InvalidateRespCache("/api/plugins/containers/docker/networks")
			WriteJSON(w, map[string]any{"ok": true, "output": lastLines(out, 3)})
			return
		}
		writeErr(w, "未支持 network action: "+b.Action, http.StatusBadRequest)
		return

	case "system":
		switch b.Action {
		case "df":
			out, err := RunOnTarget(b.Host, []string{rtCmd, "system", "df", "--format", "{{json .}}"})
			if err != nil {
				WriteJSON(w, map[string]any{"ok": false, "error": lastLines(out, 8)})
				return
			}
			rows := []map[string]any{}
			for _, l := range nonEmptyLines(out) {
				var m map[string]any
				if json.Unmarshal([]byte(l), &m) == nil {
					rows = append(rows, m)
				}
			}
			WriteJSON(w, map[string]any{"ok": true, "rows": rows})
			return
		}
		writeErr(w, "未支持 system action: "+b.Action, http.StatusBadRequest)
		return
	}
	writeErr(w, "未支持 scope: "+b.Scope, http.StatusBadRequest)
}

// ReadRemoteFileBytes 读取宿主机文件内容(SSH cat), 上限 100MB。
func ReadRemoteFileBytes(hostID, path string) ([]byte, error) {
	out, err := RunOnTarget(hostID, []string{"cat", path})
	if err != nil {
		return nil, err
	}
	if len(out) > 100<<20 {
		return nil, fmt.Errorf("文件超过 100MB 上限")
	}
	return []byte(out), nil
}