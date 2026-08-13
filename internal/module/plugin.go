package module

import (
	"log"
	"opscore/internal/central"
	"sync"
)

var (
	cs       central.CentralStore
	activeMap   = map[string]bool{}
	activeMu    sync.RWMutex
)

func InitPluginStore(c central.CentralStore) {
	cs = c
	states, err := c.GetAllModuleStates()
	if err != nil {
		log.Printf("[module] load states: %v", err)
		return
	}
	activeMu.Lock()
	activeMap = states
	if activeMap == nil {
		activeMap = map[string]bool{}
	}
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
	if cs != nil {
		if err := cs.SetModuleState(id, active); err != nil {
			log.Printf("[module] persist state: %v", err)
		}
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
