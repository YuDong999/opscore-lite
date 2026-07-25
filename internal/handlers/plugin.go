package handlers

import (
	"encoding/json"
	"net/http"
	"opscore/internal/module"
	"strings"
)

func PluginList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	states := module.AllPluginStates()
	all := module.CoreModules()
	type entry struct {
		module.Manifest
		Active bool `json:"active"`
	}
	res := make([]entry, 0, len(all))
	for _, m := range all {
		active := true
		if m.Group == "plugin" {
			if v, ok := states[m.ID]; ok {
				active = v
			}
		}
		res = append(res, entry{Manifest: m, Active: active})
	}
	json.NewEncoder(w).Encode(res)
}

func PluginAction(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/plugins/"), "/")
	if len(parts) < 2 {
		http.Error(w, `{"error":"bad path"}`, http.StatusBadRequest)
		return
	}
	id := parts[0]
	action := parts[1]
	switch action {
	case "activate":
		module.SetPluginActive(id, true)
	case "deactivate":
		module.SetPluginActive(id, false)
	default:
		http.Error(w, `{"error":"unknown action"}`, http.StatusBadRequest)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"ok": "true"})
}