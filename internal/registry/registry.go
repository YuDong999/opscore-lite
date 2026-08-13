package registry

import (
	"net/http"
	"sync"
)

type Route struct {
	Path    string
	Handler http.HandlerFunc
}

type Manifest struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Icon        string `json:"icon"`
	RoutePath   string `json:"routePath"`
	Group       string `json:"group"`
	Description string `json:"description"`
}

type Module struct {
	Manifest Manifest
	Routes   []Route
}

type Registry struct {
	mu      sync.RWMutex
	modules []*Module
}

func New() *Registry {
	return &Registry{}
}

func (r *Registry) Register(m *Module) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.modules = append(r.modules, m)
}

func (r *Registry) All() []*Module {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Module, len(r.modules))
	copy(out, r.modules)
	return out
}

func (r *Registry) Active() []Manifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Manifest
	for _, m := range r.modules {
		out = append(out, m.Manifest)
	}
	return out
}

func (r *Registry) RegisterRoutes(mux *http.ServeMux) {
	for _, m := range r.All() {
		for _, route := range m.Routes {
			mux.HandleFunc(route.Path, route.Handler)
		}
	}
}
