package vk

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
	messaging *carriers.VKMessagesCarrier
	metrics   provider.Metrics
	health    provider.Health
}

func (p *Provider) ID() string { return "vk" }

func (p *Provider) Type() provider.Type { return provider.TypeMessaging }

func (p *Provider) Category() provider.Category { return provider.CategorySocial }

func (p *Provider) Version() string { return "1.0.0" }

func (p *Provider) Configure(cfg provider.ProviderConfig) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	token, ok := cfg.Credentials["token"]
	if !ok || token == "" {
		return fmt.Errorf("vk provider: token required")
	}

	apiVersion := cfg.Settings["api_version"]
	apiVerStr, _ := apiVersion.(string)

	carrierCfg := carriers.VKMessagesConfig{
		Token:      token,
		APIVersion: apiVerStr,
	}
	if baseURL, ok := cfg.Endpoints["api_url"]; ok {
		carrierCfg.BaseURL = baseURL
	}

	carrier, err := carriers.NewVKMessagesCarrier(carrierCfg)
	if err != nil {
		return fmt.Errorf("vk provider: %w", err)
	}

	p.config = cfg
	p.messaging = carrier
	p.metrics = provider.Metrics{}
	p.health = provider.Health{LastCheck: time.Now()}
	return nil
}

func (p *Provider) GetSchema() provider.Schema {
	return provider.Schema{
		Name:        "VK Messages",
		Description: "VKontakte messaging API provider",
		Version:     "1.0.0",
		Fields: []provider.Field{
			{Name: "token", Type: "string", Required: true, Description: "VK API access token"},
			{Name: "api_version", Type: "string", Required: false, Description: "VK API version", Default: "5.199"},
			{Name: "api_url", Type: "string", Required: false, Description: "VK API base URL", Default: "https://api.vk.com/method"},
		},
	}
}

func (p *Provider) Send(ctx context.Context, payload []byte) error {
	p.mu.RLock()
	m := p.messaging
	p.mu.RUnlock()
	if m == nil {
		return fmt.Errorf("vk provider: not configured")
	}

	ep := carriers.Endpoint{
		Carrier: carriers.CarrierVKMessages,
		Address: p.config.Endpoints["peer_id"],
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
		return nil, fmt.Errorf("vk provider: not configured")
	}

	ep := carriers.Endpoint{
		Carrier: carriers.CarrierVKMessages,
		Address: p.config.Endpoints["peer_id"],
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
		MaxRatePerMinute: 120,
		MaxDailyBytes:    1_000_000_000,
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
