package admin

import (
	"fmt"
	"plugin"
	"sync"

	"github.com/meanwebuser/whitetransport/core/internal/provider"
)

type Loader interface {
	Load(path string) (provider.Provider, error)
	Unload(name string) error
	List() []string
}

type GoPluginLoader struct {
	mu      sync.RWMutex
	adapters map[string]provider.Provider
}

func NewGoPluginLoader() *GoPluginLoader {
	return &GoPluginLoader{
		adapters: make(map[string]provider.Provider),
	}
}

func (l *GoPluginLoader) Load(path string) (provider.Provider, error) {
	p, err := plugin.Open(path)
	if err != nil {
		return nil, fmt.Errorf("plugin open: %w", err)
	}

	symbol, err := p.Lookup("NewProvider")
	if err != nil {
		return nil, fmt.Errorf("plugin lookup NewProvider: %w", err)
	}

	factory, ok := symbol.(func() provider.Provider)
	if !ok {
		return nil, fmt.Errorf("plugin NewProvider has wrong signature")
	}

	adapter := factory()
	l.mu.Lock()
	l.adapters[adapter.ID()] = adapter
	l.mu.Unlock()
	return adapter, nil
}

func (l *GoPluginLoader) Unload(name string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	adapter, ok := l.adapters[name]
	if !ok {
		return fmt.Errorf("plugin %s not found", name)
	}
	if err := adapter.Unload(); err != nil {
		return err
	}
	delete(l.adapters, name)
	return nil
}

func (l *GoPluginLoader) List() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	names := make([]string, 0, len(l.adapters))
	for name := range l.adapters {
		names = append(names, name)
	}
	return names
}
