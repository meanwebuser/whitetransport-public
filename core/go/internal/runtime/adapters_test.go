package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/keys"
	"github.com/meanwebuser/whitetransport/core/internal/provider"
	"github.com/meanwebuser/whitetransport/core/internal/providers"
)

func TestProviderRegistry_Create(t *testing.T) {
	ps := providers.NewStore()
	ks := keys.NewStore()
	reg := NewProviderRegistry(ps, ks)

	cfg := provider.ProviderConfig{
		Credentials: map[string]string{"token": "test-token"},
	}

	adapter, err := reg.Create("vk", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if adapter == nil {
		t.Fatal("expected non-nil adapter")
	}
	if adapter.ID() != "vk" {
		t.Fatalf("expected provider vk, got %s", adapter.ID())
	}
}

func TestProviderRegistry_List(t *testing.T) {
	ps := providers.NewStore()
	ks := keys.NewStore()
	reg := NewProviderRegistry(ps, ks)

	adapters := reg.List()
	if len(adapters) != 0 {
		t.Fatalf("expected 0 adapters, got %d", len(adapters))
	}

	cfg := provider.ProviderConfig{}
	_, err := reg.Create("ok", cfg)
	if err == nil {
		t.Fatal("expected error for ok without token")
	}

	_, err = reg.Create("unknown", cfg)
	if err == nil {
		t.Fatal("expected error for unknown adapter")
	}
}

func TestProviderRegistry_RegisterCustom(t *testing.T) {
	ps := providers.NewStore()
	ks := keys.NewStore()
	reg := NewProviderRegistry(ps, ks)

	reg.Register("custom", func() provider.Provider {
		return &mockProvider{name: "custom"}
	})

	adapter, err := reg.Create("custom", provider.ProviderConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if adapter.ID() != "custom" {
		t.Fatalf("expected custom, got %s", adapter.ID())
	}
}

func TestProviderRegistry_SendReceive(t *testing.T) {
	ps := providers.NewStore()
	ks := keys.NewStore()
	reg := NewProviderRegistry(ps, ks)

	reg.Register("test", func() provider.Provider {
		return &mockProvider{name: "test"}
	})

	adapter, err := reg.Create("test", provider.ProviderConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	payload := []byte("hello world")
	if err := adapter.Send(context.Background(), payload); err != nil {
		t.Fatalf("send: %v", err)
	}

	received, err := adapter.Receive(context.Background())
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if string(received) != "hello world" {
		t.Fatalf("expected 'hello world', got %q", string(received))
	}
}

func TestBuildCarrierBindingsWithRegistry(t *testing.T) {
	ps := providers.NewStore()
	ks := keys.NewStore()
	reg := NewProviderRegistry(ps, ks)

	reg.Register("test-carrier", func() provider.Provider {
		return &mockProvider{name: "test-carrier"}
	})

	cfg := config.Config{
		EnabledCarriers: []string{"test-carrier"},
		CarrierConfigs: []config.CarrierConfig{
			{
				ID:       "test-carrier",
				Endpoint: config.EndpointConfig{Address: "test-address"},
				FileMailbox: &config.FileMailboxConfig{
					Dir: t.TempDir(),
				},
			},
		},
	}

	bindings, err := BuildCarrierBindingsWithRegistry(cfg, reg)
	if err != nil {
		t.Fatalf("BuildCarrierBindingsWithRegistry: %v", err)
	}

	binding, ok := bindings["test-carrier"]
	if !ok {
		t.Fatal("expected test-carrier binding")
	}
	if binding.Carrier.Descriptor().ID != "test-carrier" {
		t.Fatalf("expected test-carrier descriptor ID, got %s", binding.Carrier.Descriptor().ID)
	}
	if binding.Endpoint.Address != "test-address" {
		t.Fatalf("expected test-address endpoint, got %s", binding.Endpoint.Address)
	}
}

func TestBuildCarrierBindingsLegacyStillWorks(t *testing.T) {
	// Legacy path should still work unchanged.
	cfg := config.Config{
		EnabledCarriers: []string{carriers.CarrierFileMailbox},
		CarrierConfigs: []config.CarrierConfig{
			{
				ID: carriers.CarrierFileMailbox,
				Endpoint: config.EndpointConfig{
					Address: "test-dir",
				},
				FileMailbox: &config.FileMailboxConfig{
					Dir: t.TempDir(),
				},
			},
		},
	}

	bindings, err := BuildCarrierBindings(cfg)
	if err != nil {
		t.Fatalf("BuildCarrierBindings: %v", err)
	}

	binding, ok := bindings[carriers.CarrierFileMailbox]
	if !ok {
		t.Fatal("expected file.mailbox binding")
	}
	if binding.Carrier.Descriptor().ID != carriers.CarrierFileMailbox {
		t.Fatalf("expected file.mailbox descriptor")
	}
}

func TestBuildCarrierBindingsWithRegistryKeepsLegacyConfigErrors(t *testing.T) {
	ps := providers.NewStore()
	ks := keys.NewStore()
	reg := NewProviderRegistry(ps, ks)

	cfg := config.Config{
		EnabledCarriers: []string{carriers.CarrierVKMessages},
		CarrierConfigs: []config.CarrierConfig{
			{
				ID:       carriers.CarrierVKMessages,
				Endpoint: config.EndpointConfig{Address: "123"},
				VKMessages: &config.VKMessagesConfig{
					Token: "",
				},
			},
		},
	}

	_, err := BuildCarrierBindingsWithRegistry(cfg, reg)
	if err == nil {
		t.Fatal("expected vk messages config error")
	}
	if strings.Contains(err.Error(), "unknown adapter") {
		t.Fatalf("expected legacy config error, got registry fallback error: %v", err)
	}
	if !strings.Contains(err.Error(), "vk messages token is required") {
		t.Fatalf("expected vk messages token error, got: %v", err)
	}
}

func TestBuildCarrierBindingsWithRegistryFallback(t *testing.T) {
	// Registered carriers should work. Known legacy carriers with missing config
	// should still fail from the legacy path, then fall through to registry.
	ps := providers.NewStore()
	ks := keys.NewStore()
	reg := NewProviderRegistry(ps, ks)

	cfg := config.Config{
		EnabledCarriers: []string{carriers.CarrierVKMessages},
		CarrierConfigs:  nil,
	}

	_, err := BuildCarrierBindingsWithRegistry(cfg, reg)
	if err == nil {
		t.Fatal("expected error for enabled carrier with no config")
	}
}

type mockProvider struct {
	name     string
	messages [][]byte
}

func (m *mockProvider) ID() string                                  { return m.name }
func (m *mockProvider) Type() provider.Type                         { return provider.TypeMessaging }
func (m *mockProvider) Category() provider.Category                 { return provider.CategoryOther }
func (m *mockProvider) Version() string                             { return "1.0.0" }
func (m *mockProvider) Configure(cfg provider.ProviderConfig) error { return nil }
func (m *mockProvider) GetSchema() provider.Schema                  { return provider.Schema{} }
func (m *mockProvider) Send(_ context.Context, payload []byte) error {
	m.messages = append(m.messages, payload)
	return nil
}
func (m *mockProvider) Receive(_ context.Context) ([]byte, error) {
	if len(m.messages) == 0 {
		return nil, nil
	}
	msg := m.messages[0]
	m.messages = m.messages[1:]
	return msg, nil
}
func (m *mockProvider) Health() provider.Health        { return provider.Health{} }
func (m *mockProvider) GetLimits() provider.Limits     { return provider.Limits{} }
func (m *mockProvider) GetMetrics() provider.Metrics   { return provider.Metrics{} }
func (m *mockProvider) UpdateMetrics(provider.Metrics) {}
func (m *mockProvider) Load() error                    { return nil }
func (m *mockProvider) Unload() error                  { return nil }
