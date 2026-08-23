package handlers

import (
	"encoding/json"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
)

// LVM 参数白名单: 防止通过 device/vg/lv/size/mount 注入远程 shell 命令。
var (
	reLVMName  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`) // vg/lv 名
	reLVMDev   = regexp.MustCompile(`^/dev/[A-Za-z0-9/_.-]+$`)           // /dev/sdb1 等
	reLVMSize  = regexp.MustCompile(`^\d+(\.\d+)?[bBsSkKmMgGtT]?$`)      // 如 10G
	reLVMMount = regexp.MustCompile(`^/[A-Za-z0-9/_.-]*$`)               // 绝对路径
)

func validLVMName(s string) bool { return reLVMName.MatchString(s) }
func validLVMDev(s string) bool  { return reLVMDev.MatchString(s) }
func validLVMSize(s string) bool { return reLVMSize.MatchString(s) }
func validLVMMount(s string) bool {
	if !reLVMMount.MatchString(s) || strings.Contains(s, "..") {
		return false
	}
	return true
}

type PVInfo struct {
	Name string `json:"name"`
	Size string `json:"size"`
	Free string `json:"free"`
	VG   string `json:"vg"`
}

type VGInfo struct {
	Name string `json:"name"`
	Size string `json:"size"`
	Free string `json:"free"`
	PV   string `json:"pv"`
	LV   string `json:"lv"`
}

type LVInfo struct {
	Name    string `json:"name"`
	Size    string `json:"size"`
	VG      string `json:"vg"`
	Path    string `json:"path"`
	Mounted string `json:"mounted"`
}

type lvmActionBody struct {
	Action string `json:"action"`
	Device string `json:"device"`
	VG     string `json:"vg"`
	LV     string `json:"lv"`
	Size   string `json:"size"`
	Mount  string `json:"mount"`
	Host   string `json:"host"`
}

func LvmHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		if hostID := r.URL.Query().Get("host"); hostID != "" {
			lvmRemoteGet(w, hostID)
			return
		}
		hasLVM := true
		if _, err := exec.LookPath("lvm"); err != nil {
			hasLVM = false
		}
		WriteJSON(w, map[string]any{
			"hasLvm": hasLVM,
			"pvs":    parsePVS(runLVM("pvs --noheadings --separator=| -o pv_name,pv_size,pv_free,vg_name 2>/dev/null")),
			"vgs":    parseVGS(runLVM("vgs --noheadings --separator=| -o vg_name,vg_size,vg_free,pv_count,lv_count 2>/dev/null")),
			"lvs":    parseLVS(runLVM("lvs --noheadings --separator=| -o lv_name,lv_size,vg_name,lv_path 2>/dev/null"), runLVM("mount | grep /dev/mapper 2>/dev/null || true")),
		})
		return
	}

	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body lvmActionBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Action == "" {
		WriteJSON(w, map[string]any{"ok": false, "error": "缺少 action"})
		return
	}

	if body.Host != "" {
		lvmRemoteAction(w, body)
		return
	}

	var cmd *exec.Cmd
	switch body.Action {
	case "pvcreate":
		if body.Device == "" {
			WriteJSON(w, map[string]any{"ok": false, "error": "缺少 device"})
			return
		}
		cmd = exec.Command("pvcreate", body.Device)
	case "vgcreate":
		if body.VG == "" || body.Device == "" {
			WriteJSON(w, map[string]any{"ok": false, "error": "缺少 vg 或 device"})
			return
		}
		cmd = exec.Command("vgcreate", body.VG, body.Device)
	case "lvcreate":
		if body.VG == "" || body.LV == "" || body.Size == "" {
			WriteJSON(w, map[string]any{"ok": false, "error": "缺少 vg/lv/size"})
			return
		}
		cmd = exec.Command("lvcreate", "-L", body.Size, "-n", body.LV, body.VG)
	case "lvextend":
		if body.LV == "" || body.Size == "" {
			WriteJSON(w, map[string]any{"ok": false, "error": "缺少 lv/size"})
			return
		}
		cmd = exec.Command("lvextend", "-L", "+"+body.Size, body.LV)
		if body.Device != "" {
			cmd = exec.Command("lvextend", "-L", "+"+body.Size, body.LV, body.Device)
		}
	case "mount":
		if body.LV == "" || body.Mount == "" {
			WriteJSON(w, map[string]any{"ok": false, "error": "缺少 lv/mount"})
			return
		}
		exec.Command("mkdir", "-p", body.Mount).Run()
		cmd = exec.Command("mount", body.LV, body.Mount)
	default:
		WriteJSON(w, map[string]any{"ok": false, "error": "未知 action: " + body.Action})
		return
	}

	out, err := cmd.CombinedOutput()
	resp := map[string]any{"ok": err == nil}
	if err != nil {
		resp["error"] = err.Error()
	}
	resp["output"] = strings.TrimSpace(string(out))
	WriteJSON(w, resp)
}

func runLVM(cmd string) string {
	out, err := exec.Command("sh", "-c", cmd).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func lvmRemoteGet(w http.ResponseWriter, hostID string) {
	h := resolveAnsibleHost(hostID)
	if h == nil {
		writeErr(w, "未找到指定主机", http.StatusNotFound)
		return
	}
	rmHost := resolveRemoteHost(*h)
	cmds := map[string]string{
		"pvs":   "pvs --noheadings --separator='|' -o pv_name,pv_size,pv_free,vg_name 2>/dev/null",
		"vgs":   "vgs --noheadings --separator='|' -o vg_name,vg_size,vg_free,pv_count,lv_count 2>/dev/null",
		"lvs":   "lvs --noheadings --separator='|' -o lv_name,lv_size,vg_name,lv_path 2>/dev/null",
		"mount": "mount | grep /dev/mapper 2>/dev/null || true",
	}
	res := remotePool.Exec(rmHost, cmds)
	WriteJSON(w, map[string]any{
		"hasLvm": true,
		"pvs":    parsePVS(res["pvs"].Output),
		"vgs":    parseVGS(res["vgs"].Output),
		"lvs":    parseLVS(res["lvs"].Output, res["mount"].Output),
	})
}

func lvmRemoteAction(w http.ResponseWriter, body lvmActionBody) {
	h := resolveAnsibleHost(body.Host)
	if h == nil {
		WriteJSON(w, map[string]any{"ok": false, "error": "未找到指定主机"})
		return
	}
	rmHost := resolveRemoteHost(*h)
	var cmd string
	switch body.Action {
	case "pvcreate":
		if body.Device == "" {
			WriteJSON(w, map[string]any{"ok": false, "error": "缺少 device"})
			return
		}
		if !validLVMDev(body.Device) {
			WriteJSON(w, map[string]any{"ok": false, "error": "device 格式非法(须为 /dev/ 下的绝对路径)"})
			return
		}
		cmd = "pvcreate " + body.Device + " 2>&1"
	case "vgcreate":
		if body.VG == "" || body.Device == "" {
			WriteJSON(w, map[string]any{"ok": false, "error": "缺少 vg 或 device"})
			return
		}
		if !validLVMName(body.VG) || !validLVMDev(body.Device) {
			WriteJSON(w, map[string]any{"ok": false, "error": "vg 或 device 格式非法"})
			return
		}
		cmd = "vgcreate " + body.VG + " " + body.Device + " 2>&1"
	case "lvcreate":
		if body.VG == "" || body.LV == "" || body.Size == "" {
			WriteJSON(w, map[string]any{"ok": false, "error": "缺少 vg/lv/size"})
			return
		}
		if !validLVMName(body.VG) || !validLVMName(body.LV) || !validLVMSize(body.Size) {
			WriteJSON(w, map[string]any{"ok": false, "error": "vg/lv/size 格式非法"})
			return
		}
		cmd = "lvcreate -L " + body.Size + " -n " + body.LV + " " + body.VG + " 2>&1"
	case "lvextend":
		if body.LV == "" || body.Size == "" {
			WriteJSON(w, map[string]any{"ok": false, "error": "缺少 lv/size"})
			return
		}
		if !validLVMSize(body.Size) || (!validLVMName(body.LV) && !strings.HasPrefix(body.LV, "/dev/")) {
			WriteJSON(w, map[string]any{"ok": false, "error": "lv/size 格式非法"})
			return
		}
		cmd = "lvextend -L +" + body.Size + " " + body.LV
		if body.Device != "" {
			if !validLVMDev(body.Device) {
				WriteJSON(w, map[string]any{"ok": false, "error": "device 格式非法"})
				return
			}
			cmd += " " + body.Device
		}
		cmd += " 2>&1"
	case "mount":
		if body.LV == "" || body.Mount == "" {
			WriteJSON(w, map[string]any{"ok": false, "error": "缺少 lv/mount"})
			return
		}
		if !validLVMMount(body.Mount) || (!validLVMName(body.LV) && !strings.HasPrefix(body.LV, "/dev/")) {
			WriteJSON(w, map[string]any{"ok": false, "error": "lv/mount 格式非法"})
			return
		}
		cmd = "mkdir -p " + body.Mount + " && mount " + body.LV + " " + body.Mount + " 2>&1"
	default:
		WriteJSON(w, map[string]any{"ok": false, "error": "未知 action: " + body.Action})
		return
	}
	res := remotePool.Exec(rmHost, map[string]string{"out": cmd})
	if res["out"].Error != "" {
		WriteJSON(w, map[string]any{"ok": false, "error": res["out"].Error})
		return
	}
	WriteJSON(w, map[string]any{"ok": true, "output": res["out"].Output})
}

func parsePVS(raw string) []PVInfo {
	if raw == "" {
		return nil
	}
	var list []PVInfo
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "|")
		if len(fields) < 3 {
			continue
		}
		vg := ""
		if len(fields) >= 4 {
			vg = strings.TrimSpace(fields[3])
		}
		list = append(list, PVInfo{
			Name: strings.TrimSpace(fields[0]),
			Size: strings.TrimSpace(fields[1]),
			Free: strings.TrimSpace(fields[2]),
			VG:   vg,
		})
	}
	return list
}

func parseVGS(raw string) []VGInfo {
	if raw == "" {
		return nil
	}
	var list []VGInfo
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "|")
		if len(fields) < 3 {
			continue
		}
		pv, lv := "", ""
		if len(fields) >= 4 {
			pv = strings.TrimSpace(fields[3])
		}
		if len(fields) >= 5 {
			lv = strings.TrimSpace(fields[4])
		}
		list = append(list, VGInfo{
			Name: strings.TrimSpace(fields[0]),
			Size: strings.TrimSpace(fields[1]),
			Free: strings.TrimSpace(fields[2]),
			PV:   pv,
			LV:   lv,
		})
	}
	return list
}

func parseLVS(raw, mountOut string) []LVInfo {
	if raw == "" {
		return nil
	}
	mounts := make(map[string]string)
	for _, line := range strings.Split(mountOut, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 {
			mounts[fields[0]] = fields[2]
		}
	}
	var list []LVInfo
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "|")
		if len(fields) < 3 {
			continue
		}
		path := ""
		if len(fields) >= 4 {
			path = strings.TrimSpace(fields[3])
		}
		list = append(list, LVInfo{
			Name:    strings.TrimSpace(fields[0]),
			Size:    strings.TrimSpace(fields[1]),
			VG:      strings.TrimSpace(fields[2]),
			Path:    path,
			Mounted: mounts[path],
		})
	}
	return list
}
