package registry

import "sync"

var GlobalHostRegistry = NewHostRegistry()

type HostRegistry struct {
	mu    sync.Mutex
	hosts []string
}

func NewHostRegistry() *HostRegistry {
	return &HostRegistry{}
}

func (r *HostRegistry) RegisterHost(host string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hosts = append(r.hosts, host)

	return len(r.hosts) - 1
}

func (r *HostRegistry) GetHost(id int) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.hosts[id]
}
