package providers

import (
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/provider"
)

type Model struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Type     provider.Type     `json:"type"`
	Category provider.Category `json:"category"`
	Version  string            `json:"version"`

	Encoding   EncodingConfig    `json:"encoding"`
	Protocols  []string          `json:"protocols"`
	Ports      []int             `json:"ports"`
	Channels   []provider.Channel `json:"channels"`
	Keys       []KeyRef          `json:"keys"`
	RateLimits RateLimitsConfig  `json:"rate_limits"`
	Health     provider.Health   `json:"health"`
	Admin      ProviderAdmin     `json:"admin"`
}

type EncodingConfig struct {
	Charset            string `json:"charset"`
	MaxPayload         int    `json:"max_payload"`
	SupportsCompression bool  `json:"supports_compression"`
}

type RateLimitsConfig struct {
	MessagesPerMinute int   `json:"messages_per_minute"`
	MessagesPerHour   int   `json:"messages_per_hour"`
	MessagesPerDay    int   `json:"messages_per_day"`
	BytesPerMinute    int64 `json:"bytes_per_minute"`
	BytesPerHour      int64 `json:"bytes_per_hour"`
	BytesPerDay       int64 `json:"bytes_per_day"`
}

type KeyRef struct {
	ID         string            `json:"id"`
	ProviderID string            `json:"provider_id"`
	Type       provider.KeyType  `json:"type"`
	Status     provider.KeyStatus `json:"status"`
	CreatedAt  time.Time         `json:"created_at"`
	ExpiresAt  *time.Time        `json:"expires_at,omitempty"`
	LastUsed   time.Time         `json:"last_used"`
}

type ProviderAdmin struct {
	Endpoint string `json:"endpoint"`
	APIKey   string `json:"api_key,omitempty"`
}

type Store struct {
	providers map[string]*Model
}

func NewStore() *Store {
	return &Store{providers: make(map[string]*Model)}
}

func (s *Store) Get(id string) (*Model, bool) {
	p, ok := s.providers[id]
	return p, ok
}

func (s *Store) Set(p *Model) {
	s.providers[p.ID] = p
}

func (s *Store) Delete(id string) {
	delete(s.providers, id)
}

func (s *Store) List() []*Model {
	out := make([]*Model, 0, len(s.providers))
	for _, p := range s.providers {
		out = append(out, p)
	}
	return out
}

func (s *Store) ListByType(t provider.Type) []*Model {
	var out []*Model
	for _, p := range s.providers {
		if p.Type == t {
			out = append(out, p)
		}
	}
	return out
}

func (s *Store) ListByCategory(c provider.Category) []*Model {
	var out []*Model
	for _, p := range s.providers {
		if p.Category == c {
			out = append(out, p)
		}
	}
	return out
}
