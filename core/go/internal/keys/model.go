package keys

import (
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/provider"
)

type Model struct {
	ID          string                    `json:"id"`
	ProviderID  string                    `json:"provider_id"`
	Type        provider.KeyType          `json:"type"`
	Token       string                    `json:"token"`
	Refresh     string                    `json:"refresh,omitempty"`
	Credentials map[string]string         `json:"credentials"`
	Channels    map[string]ChannelConfig  `json:"channels"`
	Status      provider.KeyStatus        `json:"status"`
	CreatedAt   time.Time                 `json:"created_at"`
	ExpiresAt   *time.Time                `json:"expires_at,omitempty"`
	LastUsed    time.Time                 `json:"last_used"`
	Health      Health                    `json:"health"`
	Usage       Usage                     `json:"usage"`
}

type ChannelConfig struct {
	Enabled bool  `json:"enabled"`
	MaxRate int   `json:"max_rate,omitempty"`
	MaxSize int   `json:"max_size,omitempty"`
}

type Health struct {
	SuccessRate    float64       `json:"success_rate"`
	AvgLatency     time.Duration `json:"avg_latency"`
	ErrorCount     int64         `json:"error_count"`
	RateLimitHit   bool          `json:"rate_limit_hit"`
	GlobalLimitHit bool          `json:"global_limit_hit"`
	QuotaExhausted bool          `json:"quota_exhausted"`
}

type Usage struct {
	MessagesSent int64 `json:"messages_sent"`
	BytesSent    int64 `json:"bytes_sent"`
	MessagesRecv int64 `json:"messages_recv"`
	BytesRecv    int64 `json:"bytes_recv"`
	Errors       int64 `json:"errors"`
}

type Store struct {
	mu   sync.RWMutex
	keys map[string]*Model
}

func NewStore() *Store {
	return &Store{keys: make(map[string]*Model)}
}

func (s *Store) Get(id string) (*Model, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.keys[id]
	return k, ok
}

func (s *Store) Set(k *Model) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[k.ID] = k
}

func (s *Store) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.keys, id)
}

func (s *Store) List() []*Model {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Model, 0, len(s.keys))
	for _, k := range s.keys {
		out = append(out, k)
	}
	return out
}

func (s *Store) ListByProvider(providerID string) []*Model {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Model
	for _, k := range s.keys {
		if k.ProviderID == providerID {
			out = append(out, k)
		}
	}
	return out
}

func (s *Store) ListByType(t provider.KeyType) []*Model {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Model
	for _, k := range s.keys {
		if k.Type == t {
			out = append(out, k)
		}
	}
	return out
}
