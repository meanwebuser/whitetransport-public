//go:build mobile

package runtime

import (
	"fmt"
	"sync"

	memadapter "github.com/meanwebuser/whitetransport/core/internal/adapters/memory"
	okadapter "github.com/meanwebuser/whitetransport/core/internal/adapters/ok"
	vkadapter "github.com/meanwebuser/whitetransport/core/internal/adapters/vk"
	wbadapter "github.com/meanwebuser/whitetransport/core/internal/adapters/whitelist"
	"github.com/meanwebuser/whitetransport/core/internal/keys"
	"github.com/meanwebuser/whitetransport/core/internal/provider"
	"github.com/meanwebuser/whitetransport/core/internal/providers"
)

type ProviderRegistry struct {
	mu        sync.RWMutex
	factories map[string]ProviderFactory
	adapters  map[string]provider.Provider
	providers *providers.Store
	keys      *keys.Store
}

type ProviderFactory func() provider.Provider

func NewProviderRegistry(ps *providers.Store, ks *keys.Store) *ProviderRegistry {
	r := &ProviderRegistry{
		factories: make(map[string]ProviderFactory),
		adapters:  make(map[string]provider.Provider),
		providers: ps,
		keys:      ks,
	}
	r.registerBuiltins()
	return r
}

func (r *ProviderRegistry) registerBuiltins() {
	r.factories["vk"] = func() provider.Provider { return &vkadapter.Provider{} }
	r.factories["ok"] = func() provider.Provider { return &okadapter.Provider{} }
	r.factories["wbstream"] = func() provider.Provider { return &wbadapter.Provider{} }
	r.factories["memory"] = func() provider.Provider { return &memadapter.Provider{} }
}

func (r *ProviderRegistry) Register(name string, factory ProviderFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[name] = factory
}

func (r *ProviderRegistry) Create(name string, cfg provider.ProviderConfig) (provider.Provider, error) {
	r.mu.RLock()
	factory, ok := r.factories[name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown provider: %s", name)
	}

	adapter := factory()
	if err := adapter.Configure(cfg); err != nil {
		return nil, fmt.Errorf("configure %s: %w", name, err)
	}

	r.mu.Lock()
	r.adapters[name] = adapter
	r.mu.Unlock()
	return adapter, nil
}

func (r *ProviderRegistry) Get(name string) (provider.Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[name]
	return a, ok
}

func (r *ProviderRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.adapters))
	for name := range r.adapters {
		names = append(names, name)
	}
	return names
}

func (r *ProviderRegistry) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.adapters, name)
}
