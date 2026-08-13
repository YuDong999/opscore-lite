package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"opscore/internal/ansible"
)

func SSHCmdGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		writeErr(w, "name 不能为空", http.StatusBadRequest)
		return
	}
	pair, err := ansibleMgr.SSH.GenerateKey(body.Name)
	if err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	WriteJSON(w, pair)
}

func SSHCmdListKeys(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, ansibleMgr.SSH.ListKeys())
}

func SSHCmdDeleteKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		writeErr(w, "name 不能为空", http.StatusBadRequest)
		return
	}
	if err := ansibleMgr.SSH.DeleteKey(body.Name); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	WriteJSON(w, map[string]string{"ok": "true"})
}

func SSHCmdDeploy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req ansible.SSHDeployReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, "请求参数错误", http.StatusBadRequest)
		return
	}
	if req.Port == 0 {
		req.Port = 22
	}
	if req.User == "" {
		req.User = "root"
	}
	if err := ansibleMgr.SSH.DeployKey(req); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	WriteJSON(w, map[string]string{"ok": "true"})
}

func SSHCmdTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		KeyName string `json:"keyName"`
		Host    string `json:"host"`
		Port    string `json:"port"`
		User    string `json:"user"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.KeyName == "" || body.Host == "" {
		writeErr(w, "keyName 和 host 不能为空", http.StatusBadRequest)
		return
	}
	port, _ := strconv.Atoi(body.Port)
	if err := ansibleMgr.SSH.TestConnection(body.KeyName, body.Host, body.User, port); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	WriteJSON(w, map[string]string{"ok": "true", "message": "SSH 连接成功"})
}

func SSHCmdBind(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		KeyName string `json:"keyName"`
		HostID  string `json:"hostId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.KeyName == "" || body.HostID == "" {
		writeErr(w, "keyName 和 hostId 不能为空", http.StatusBadRequest)
		return
	}
	if err := ansibleMgr.BindKeyToHost(body.KeyName, body.HostID); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	WriteJSON(w, map[string]string{"ok": "true"})
}
