package runtime

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/keys"
	"github.com/meanwebuser/whitetransport/core/internal/provider"
	"github.com/meanwebuser/whitetransport/core/internal/providers"
	"github.com/meanwebuser/whitetransport/core/internal/tokens"
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

func TestProviderRegistryCreatesVKCallAdapter(t *testing.T) {
	reg := NewProviderRegistry(providers.NewStore(), keys.NewStore())
	adapter, err := reg.Create("vkcall", provider.ProviderConfig{
		Credentials: map[string]string{"cookie": "test-cookie", "peer_id": "2000000001"},
	})
	if err != nil {
		t.Fatalf("Create vkcall: %v", err)
	}
	if adapter.ID() != "vkcall" || adapter.Type() != provider.TypeVideoCall {
		t.Fatalf("adapter = %s/%s", adapter.ID(), adapter.Type())
	}
}

func TestProviderRegistryCreatesMAXAdapter(t *testing.T) {
	reg := NewProviderRegistry(providers.NewStore(), keys.NewStore())
	adapter, err := reg.Create("max", provider.ProviderConfig{
		Credentials: map[string]string{"max_token": "test-token"},
		Settings:    map[string]any{"role": "node"},
	})
	if err != nil {
		t.Fatalf("Create max: %v", err)
	}
	if adapter.ID() != "max" || adapter.Type() != provider.TypeVideoCall {
		t.Fatalf("adapter = %s/%s", adapter.ID(), adapter.Type())
	}
}

func TestBuildCarrierBindingsWithRegistryCreatesMAXProviderCarrierFromRoleScopedToken(t *testing.T) {
	store := tokens.NewStore()
	store.Set(&tokens.Token{
		ID: "max-client", Platform: "max", Kind: tokens.KindAPIKey,
		Lifecycle: tokens.LifecycleEmbedded, Status: tokens.StatusActive,
		Parts: map[string]string{"max_token": "test-token"},
	})
	store.AddBinding(tokens.Binding{
		TokenID: "max-client", Platform: "max", ConnectionType: "calls",
		ChannelID: "42", Role: "client", Enabled: true,
	})
	cfg := config.Config{
		Role:                   config.RoleClient,
		BootstrapSecret:        "test-max-bootstrap-secret",
		ProviderWireEncryption: true,
		EnabledCarriers:        []string{"max"},
		CarrierConfigs: []config.CarrierConfig{{
			ID:       "max",
			Endpoint: config.EndpointConfig{Address: "42"},
			MAX:      &config.MAXConfig{PeerID: "42"},
		}},
	}
	bindings, err := BuildCarrierBindingsWithRegistryAndTokens(cfg, NewProviderRegistry(providers.NewStore(), keys.NewStore()), store)
	if err != nil {
		t.Fatalf("BuildCarrierBindingsWithRegistryAndTokens: %v", err)
	}
	binding, ok := bindings["max"]
	if !ok {
		t.Fatalf("max binding missing: %v", bindings)
	}
	if binding.Carrier.Descriptor().Provider != "max" || binding.Carrier.Descriptor().Mode != carriers.DeliveryStream {
		t.Fatalf("MAX binding descriptor = %+v", binding.Carrier.Descriptor())
	}
}

func TestMAXProviderBindingRejectsPlaintextProviderWire(t *testing.T) {
	store := tokens.NewStore()
	store.Set(&tokens.Token{ID: "max-client", Platform: "max", Kind: tokens.KindAPIKey, Lifecycle: tokens.LifecycleEmbedded, Status: tokens.StatusActive, Value: "test-token"})
	store.AddBinding(tokens.Binding{TokenID: "max-client", Platform: "max", ConnectionType: "calls", ChannelID: "42", Role: "client", Enabled: true})
	cfg := config.Config{
		Role:            config.RoleClient,
		EnabledCarriers: []string{"max"},
		CarrierConfigs: []config.CarrierConfig{{
			ID:       "max",
			Endpoint: config.EndpointConfig{Address: "42"},
			MAX:      &config.MAXConfig{PeerID: "42"},
		}},
	}
	_, err := BuildCarrierBindingsWithRegistryAndTokens(cfg, NewProviderRegistry(providers.NewStore(), keys.NewStore()), store)
	if err == nil || !strings.Contains(err.Error(), "requires provider_wire_encryption") {
		t.Fatalf("MAX plaintext binding error = %v", err)
	}
}

func TestBuildCarrierBindingsWithRegistryCreatesVKCallProviderCarrier(t *testing.T) {
	store := tokens.NewStore()
	store.Set(&tokens.Token{
		ID: "vk-call-node", Platform: "vk", Kind: tokens.KindCookies,
		Lifecycle: tokens.LifecycleEmbedded, Status: tokens.StatusActive,
		Value: "sid=node-cookie",
	})
	store.AddBinding(tokens.Binding{
		TokenID: "vk-call-node", Platform: "vk", ConnectionType: "calls",
		ChannelID: "2000000001", Role: "node", Enabled: true,
	})
	cfg := config.Config{
		Role:            config.RoleNode,
		EnabledCarriers: []string{"vkcall"},
		CarrierConfigs: []config.CarrierConfig{{
			ID: "vkcall", Endpoint: config.EndpointConfig{Address: "2000000001"},
			VKCall: &config.VKCallConfig{PeerID: "2000000001"},
		}},
	}
	bindings, err := BuildCarrierBindingsWithRegistryAndTokens(cfg, NewProviderRegistry(providers.NewStore(), keys.NewStore()), store)
	if err != nil {
		t.Fatalf("BuildCarrierBindingsWithRegistryAndTokens: %v", err)
	}
	if _, ok := bindings["vkcall"]; !ok {
		t.Fatalf("vkcall binding missing: %v", bindings)
	}
}

func TestRegistryProviderBindingEncryptsWireWithBootstrapSecret(t *testing.T) {
	transport := &mockProvider{name: "wire-provider"}
	reg := NewProviderRegistry(providers.NewStore(), keys.NewStore())
	reg.Register("wire-provider", func() provider.Provider { return transport })
	cfg := config.Config{
		BootstrapSecret:        "test-bootstrap-secret",
		ProviderWireEncryption: true,
		EnabledCarriers:        []string{"wire-provider"},
		CarrierConfigs: []config.CarrierConfig{{
			ID:       "wire-provider",
			Endpoint: config.EndpointConfig{Address: "room://test"},
		}},
	}

	bindings, err := BuildCarrierBindingsWithRegistry(cfg, reg)
	if err != nil {
		t.Fatalf("BuildCarrierBindingsWithRegistry: %v", err)
	}
	binding := bindings["wire-provider"]
	envelope := fabric.NewEnvelope("provider-wire-runtime", fabric.TrafficEgress, "tunnel.data", []byte("runtime-secret"))
	if err := binding.Carrier.Write(context.Background(), binding.Endpoint, envelope); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(transport.messages) != 1 || bytes.Contains(transport.messages[0], envelope.Payload) {
		t.Fatalf("registry provider received plaintext wire data: %q", transport.messages)
	}
	read, err := binding.Carrier.Read(context.Background(), binding.Endpoint, "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(read.Envelopes) != 1 || !bytes.Equal(read.Envelopes[0].Payload, envelope.Payload) {
		t.Fatalf("read envelopes = %#v, want payload %q", read.Envelopes, envelope.Payload)
	}
}

func TestRegistryProviderBindingUsesAdapterIDAsWireScope(t *testing.T) {
	transport := &mockProvider{name: "wbstream"}
	reg := NewProviderRegistry(providers.NewStore(), keys.NewStore())
	reg.Register("wbstream", func() provider.Provider { return transport })
	cfg := config.Config{
		BootstrapSecret:        "test-bootstrap-secret",
		ProviderWireEncryption: true,
		EnabledCarriers:        []string{"wbstream.vp8"},
		CarrierConfigs: []config.CarrierConfig{{
			ID:       "wbstream.vp8",
			Endpoint: config.EndpointConfig{Address: "room://test"},
		}},
	}

	bindings, err := BuildCarrierBindingsWithRegistry(cfg, reg)
	if err != nil {
		t.Fatalf("BuildCarrierBindingsWithRegistry: %v", err)
	}
	binding := bindings["wbstream.vp8"]
	envelope := fabric.NewEnvelope("provider-wire-alias", fabric.TrafficEgress, "tunnel.data", []byte("alias-safe"))
	if err := binding.Carrier.Write(context.Background(), binding.Endpoint, envelope); err != nil {
		t.Fatalf("Write: %v", err)
	}

	peerTransport := &mockProvider{name: "wbstream", messages: [][]byte{transport.messages[0]}}
	peer, err := carriers.NewEncryptedProviderCarrier(peerTransport, binding.Endpoint, fabric.DeriveProviderWireKey("test-bootstrap-secret", "wbstream"))
	if err != nil {
		t.Fatalf("NewEncryptedProviderCarrier: %v", err)
	}
	read, err := peer.Read(context.Background(), binding.Endpoint, "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(read.Envelopes) != 1 || !bytes.Equal(read.Envelopes[0].Payload, envelope.Payload) {
		t.Fatalf("read envelopes = %#v, want payload %q", read.Envelopes, envelope.Payload)
	}
}

func TestRegistryProviderBindingKeepsLegacyWireWithoutBootstrapSecret(t *testing.T) {
	transport := &mockProvider{name: "legacy-wire-provider"}
	reg := NewProviderRegistry(providers.NewStore(), keys.NewStore())
	reg.Register("legacy-wire-provider", func() provider.Provider { return transport })
	cfg := config.Config{
		EnabledCarriers: []string{"legacy-wire-provider"},
		CarrierConfigs: []config.CarrierConfig{{
			ID:       "legacy-wire-provider",
			Endpoint: config.EndpointConfig{Address: "room://test"},
		}},
	}

	bindings, err := BuildCarrierBindingsWithRegistry(cfg, reg)
	if err != nil {
		t.Fatalf("BuildCarrierBindingsWithRegistry: %v", err)
	}
	binding := bindings["legacy-wire-provider"]
	envelope := fabric.NewEnvelope("provider-wire-legacy", fabric.TrafficEgress, "tunnel.data", []byte("legacy-plaintext"))
	if err := binding.Carrier.Write(context.Background(), binding.Endpoint, envelope); err != nil {
		t.Fatalf("Write: %v", err)
	}
	want, err := carriers.MarshalEnvelope(envelope)
	if err != nil {
		t.Fatalf("MarshalEnvelope: %v", err)
	}
	if len(transport.messages) != 1 || !bytes.Equal(transport.messages[0], want) {
		t.Fatalf("legacy provider wire behavior changed unexpectedly: %q", transport.messages)
	}
}

func TestRegistryProviderBindingRejectsWireEncryptionWithoutBootstrapSecret(t *testing.T) {
	reg := NewProviderRegistry(providers.NewStore(), keys.NewStore())
	reg.Register("wire-provider", func() provider.Provider { return &mockProvider{name: "wire-provider"} })
	cfg := config.Config{
		ProviderWireEncryption: true,
		EnabledCarriers:        []string{"wire-provider"},
		CarrierConfigs: []config.CarrierConfig{{
			ID:       "wire-provider",
			Endpoint: config.EndpointConfig{Address: "room://test"},
		}},
	}

	_, err := BuildCarrierBindingsWithRegistry(cfg, reg)
	if err == nil || !strings.Contains(err.Error(), "provider wire encryption requires bootstrap_secret") {
		t.Fatalf("BuildCarrierBindingsWithRegistry error = %v, want missing bootstrap secret", err)
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
