package memory

import (
	"context"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/provider"
)

type Provider struct {
	mu      sync.RWMutex
	config  provider.ProviderConfig
	messages [][]byte
	metrics  provider.Metrics
	health   provider.Health
}

func (p *Provider) ID() string { return "memory" }

func (p *Provider) Type() provider.Type { return provider.TypeMessaging }

func (p *Provider) Category() provider.Category { return provider.CategoryOther }

func (p *Provider) Version() string { return "1.0.0" }

func (p *Provider) Configure(cfg provider.ProviderConfig) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.config = cfg
	p.messages = make([][]byte, 0)
	p.metrics = provider.Metrics{}
	p.health = provider.Health{LastCheck: time.Now()}
	return nil
}

func (p *Provider) GetSchema() provider.Schema {
	return provider.Schema{
		Name:        "Memory",
		Description: "In-memory carrier for testing",
		Version:     "1.0.0",
		Fields: []provider.Field{
			{Name: "buffer_size", Type: "int", Required: false, Description: "Max messages in buffer", Default: 100},
		},
	}
}

func (p *Provider) Send(ctx context.Context, payload []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]byte, len(payload))
	copy(cp, payload)
	p.messages = append(p.messages, cp)
	p.metrics.MessagesSent++
	p.metrics.SentBytes += int64(len(payload))
	return nil
}

func (p *Provider) Receive(ctx context.Context) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.messages) == 0 {
		return nil, nil
	}
	msg := p.messages[0]
	p.messages = p.messages[1:]
	p.metrics.MessagesRecv++
	p.metrics.ReceivedBytes += int64(len(msg))
	return msg, nil
}

func (p *Provider) Health() provider.Health {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.health
}

func (p *Provider) GetLimits() provider.Limits {
	return provider.Limits{
		MaxPayloadBytes:  1 << 20,
		MaxRatePerMinute: 10000,
		MaxDailyBytes:    1 << 40,
	}
}

func (p *Provider) GetMetrics() provider.Metrics {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.metrics
}

func (p *Provider) UpdateMetrics(m provider.Metrics) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.metrics = m
}

func (p *Provider) Load() error  { return nil }
func (p *Provider) Unload() error { return nil }
