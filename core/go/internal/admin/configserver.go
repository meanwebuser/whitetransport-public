package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/keys"
	"github.com/meanwebuser/whitetransport/core/internal/provider"
	"github.com/meanwebuser/whitetransport/core/internal/providers"
)

type ConfigChangeCallback func(config any)

type ConfigServer struct {
	mu         sync.RWMutex
	providers  *providers.Store
	keys       *keys.Store
	adapters   map[string]provider.Provider
	watchers   map[string][]ConfigChangeCallback
	storage    ConfigStorage
	cache      ConfigCache
}

type ConfigStorage interface {
	Get(providerID string) (any, error)
	Set(providerID string, config any) error
	List() ([]string, error)
}

type ConfigCache struct {
	mu     sync.RWMutex
	items  map[string]cacheEntry
	ttl    time.Duration
}

type cacheEntry struct {
	value     any
	expiresAt time.Time
}

func NewConfigCache(ttl time.Duration) *ConfigCache {
	return &ConfigCache{
		items: make(map[string]cacheEntry),
		ttl:   ttl,
	}
}

func (c *ConfigCache) Get(key string) (any, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.items[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.expiresAt) {
		return nil, false
	}
	return entry.value, true
}

func (c *ConfigCache) Set(key string, value any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = cacheEntry{
		value:     value,
		expiresAt: time.Now().Add(c.ttl),
	}
}

func (c *ConfigCache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, key)
}

type MemoryConfigStorage struct {
	mu    sync.RWMutex
	items map[string]any
}

func NewMemoryConfigStorage() *MemoryConfigStorage {
	return &MemoryConfigStorage{
		items: make(map[string]any),
	}
}

func (s *MemoryConfigStorage) Get(providerID string) (any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	val, ok := s.items[providerID]
	if !ok {
		return nil, fmt.Errorf("config not found: %s", providerID)
	}
	return val, nil
}

func (s *MemoryConfigStorage) Set(providerID string, config any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[providerID] = config
	return nil
}

func (s *MemoryConfigStorage) List() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]string, 0, len(s.items))
	for k := range s.items {
		keys = append(keys, k)
	}
	return keys, nil
}

func NewConfigServer(providers *providers.Store, keys *keys.Store) *ConfigServer {
	return &ConfigServer{
		providers: providers,
		keys:      keys,
		adapters:  make(map[string]provider.Provider),
		watchers:  make(map[string][]ConfigChangeCallback),
		cache:     *NewConfigCache(30 * time.Second),
		storage:   NewMemoryConfigStorage(),
	}
}

func (s *ConfigServer) SetStorage(storage ConfigStorage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storage = storage
}

func (s *ConfigServer) RegisterAdapter(name string, adapter provider.Provider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.adapters[name] = adapter
}

func (s *ConfigServer) GetProviderConfig(providerID string) (any, error) {
	if cached, ok := s.cache.Get(providerID); ok {
		return cached, nil
	}
	s.mu.RLock()
	storage := s.storage
	s.mu.RUnlock()
	config, err := storage.Get(providerID)
	if err != nil {
		return nil, err
	}
	s.cache.Set(providerID, config)
	return config, nil
}

func (s *ConfigServer) SetProviderConfig(providerID string, config any) error {
	s.mu.RLock()
	storage := s.storage
	watchers := s.watchers[providerID]
	s.mu.RUnlock()

	if err := storage.Set(providerID, config); err != nil {
		return err
	}
	s.cache.Delete(providerID)

	for _, cb := range watchers {
		cb(config)
	}
	return nil
}

func (s *ConfigServer) WatchConfig(providerID string, callback ConfigChangeCallback) {
	s.mu.Lock()
	s.watchers[providerID] = append(s.watchers[providerID], callback)
	s.mu.Unlock()
}

func (s *ConfigServer) UnmarshalProviderConfig(data []byte) (*providers.Model, error) {
	var p providers.Model
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("unmarshal provider config: %w", err)
	}
	return &p, nil
}

func (s *ConfigServer) UnmarshalKeyConfig(data []byte) (*keys.Model, error) {
	var k keys.Model
	if err := json.Unmarshal(data, &k); err != nil {
		return nil, fmt.Errorf("unmarshal key config: %w", err)
	}
	return &k, nil
}

func (s *ConfigServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/config", s.handleConfigList)
	mux.HandleFunc("/api/v1/config/", s.handleConfigByID)
	return mux
}

func (s *ConfigServer) handleConfigList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	ids, err := s.storage.List()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	configs := make(map[string]any)
	for _, id := range ids {
		cfg, err := s.storage.Get(id)
		if err == nil {
			configs[id] = cfg
		}
	}
	writeJSON(w, http.StatusOK, configs)
}

func (s *ConfigServer) handleConfigByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/config/")
	id = strings.TrimRight(id, "/")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "config id is required"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		cfg, err := s.storage.Get(id)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, cfg)
	case http.MethodPut:
		var cfg any
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("decode config: %v", err)})
			return
		}
		if err := s.SetProviderConfig(id, cfg); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}
