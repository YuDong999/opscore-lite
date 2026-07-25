package module

import (
	"log"
	"opscore/internal/store"
	"sync"
)

var (
	pluginStore *store.JSONFile
	activeMap   = map[string]bool{}
	activeMu    sync.RWMutex
)

type pluginConfig struct {
	Active map[string]bool `json:"active"`
}

func InitPluginStore(dir string) {
	var err error
	pluginStore, err = store.New(dir, "plugins.json")
	if err != nil {
		log.Printf("[plugin] store init failed: %v", err)
		return
	}
	var cfg pluginConfig
	pluginStore.Read(&cfg)
	if cfg.Active == nil {
		cfg.Active = map[string]bool{}
	}
	activeMu.Lock()
	activeMap = cfg.Active
	activeMu.Unlock()
}

func IsPluginActive(id string) bool {
	activeMu.RLock()
	defer activeMu.RUnlock()
	return activeMap[id]
}

func SetPluginActive(id string, active bool) {
	activeMu.Lock()
	activeMap[id] = active
	activeMu.Unlock()
	if pluginStore != nil {
		pluginStore.Write(&pluginConfig{Active: activeMap})
	}
}

func AllPluginStates() map[string]bool {
	activeMu.RLock()
	defer activeMu.RUnlock()
	ret := make(map[string]bool, len(activeMap))
	for k, v := range activeMap {
		ret[k] = v
	}
	return ret
}
