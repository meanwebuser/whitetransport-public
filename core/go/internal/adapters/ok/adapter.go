package ok

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/provider"
)

type Provider struct {
	mu        sync.RWMutex
	config    provider.ProviderConfig
	messaging *carriers.OKMessagesCarrier
	metrics   provider.Metrics
	health    provider.Health
}

func (p *Provider) ID() string { return "ok" }

func (p *Provider) Type() provider.Type { return provider.TypeMessaging }

func (p *Provider) Category() provider.Category { return provider.CategorySocial }

func (p *Provider) Version() string { return "1.0.0" }

func (p *Provider) Configure(cfg provider.ProviderConfig) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	token, ok := cfg.Credentials["token"]
	if !ok || token == "" {
		return fmt.Errorf("ok provider: token required")
	}

	carrierCfg := carriers.OKMessagesConfig{Token: token}
	if baseURL, ok := cfg.Endpoints["api_url"]; ok {
		carrierCfg.BaseURL = baseURL
	}

	carrier, err := carriers.NewOKMessagesCarrier(carrierCfg)
	if err != nil {
		return fmt.Errorf("ok provider: %w", err)
	}

	p.config = cfg
	p.messaging = carrier
	p.metrics = provider.Metrics{}
	p.health = provider.Health{LastCheck: time.Now()}
	return nil
}

func (p *Provider) GetSchema() provider.Schema {
	return provider.Schema{
		Name:        "OK Messages",
		Description: "Odnoklassniki Graph API messaging provider",
		Version:     "1.0.0",
		Fields: []provider.Field{
			{Name: "token", Type: "string", Required: true, Description: "OK API access token"},
			{Name: "api_url", Type: "string", Required: false, Description: "OK API base URL", Default: "https://api.ok.ru/graph"},
		},
	}
}

func (p *Provider) Send(ctx context.Context, payload []byte) error {
	p.mu.RLock()
	m := p.messaging
	p.mu.RUnlock()
	if m == nil {
		return fmt.Errorf("ok provider: not configured")
	}

	ep := carriers.Endpoint{
		Carrier: carriers.CarrierOKMessages,
		Address: p.config.Endpoints["chat_id"],
	}
	env, err := carriers.ParseEnvelope(payload)
	if err != nil {
		return err
	}
	return m.Write(ctx, ep, env)
}

func (p *Provider) Receive(ctx context.Context) ([]byte, error) {
	p.mu.RLock()
	m := p.messaging
	p.mu.RUnlock()
	if m == nil {
		return nil, fmt.Errorf("ok provider: not configured")
	}

	ep := carriers.Endpoint{
		Carrier: carriers.CarrierOKMessages,
		Address: p.config.Endpoints["chat_id"],
	}
	result, err := m.Read(ctx, ep, "")
	if err != nil {
		return nil, err
	}
	if len(result.Envelopes) == 0 {
		return nil, nil
	}
	return carriers.MarshalEnvelope(result.Envelopes[0])
}

func (p *Provider) Health() provider.Health {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.health
}

func (p *Provider) GetLimits() provider.Limits {
	return provider.Limits{
		MaxPayloadBytes:  4096,
		MaxRatePerMinute: 90,
		MaxDailyBytes:    844_000_000,
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
