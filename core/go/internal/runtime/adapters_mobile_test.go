//go:build mobile

package runtime

import (
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/keys"
	"github.com/meanwebuser/whitetransport/core/internal/provider"
	"github.com/meanwebuser/whitetransport/core/internal/providers"
	"github.com/meanwebuser/whitetransport/core/internal/tokens"
)

func TestMobileProviderRegistryResolvesClientWBStreamCompositeToken(t *testing.T) {
	ps := providers.NewStore()
	ks := keys.NewStore()
	registry := NewProviderRegistry(ps, ks)

	var captured provider.ProviderConfig
	registry.Register("wbstream", func() provider.Provider {
		return &capturingProvider{
			mockProvider: &mockProvider{name: "wbstream"},
			capture:      &captured,
		}
	})

	store := tokens.NewStore()
	store.Set(&tokens.Token{
		ID:        "wb-node",
		Platform:  "wbstream",
		Kind:      tokens.KindComposite,
		Lifecycle: tokens.LifecycleEmbedded,
		Status:    tokens.StatusActive,
		Parts: map[string]string{
			"access_token":  "node-access-token",
			"cookie_header": "node-cookie-header",
		},
	})
	store.Set(&tokens.Token{
		ID:        "wb-client",
		Platform:  "wbstream",
		Kind:      tokens.KindComposite,
		Lifecycle: tokens.LifecycleEmbedded,
		Status:    tokens.StatusActive,
		Parts: map[string]string{
			"access_token":  "client-access-token",
			"cookie_header": "client-cookie-header",
		},
	})
	store.AddBinding(tokens.Binding{
		TokenID:        "wb-node",
		Platform:       "wbstream",
		ConnectionType: "vp8",
		ChannelID:      "*",
		Role:           "node",
		Priority:       1,
		Enabled:        true,
	})
	store.AddBinding(tokens.Binding{
		TokenID:        "wb-client",
		Platform:       "wbstream",
		ConnectionType: "vp8",
		ChannelID:      "*",
		Role:           "client",
		Priority:       10,
		Enabled:        true,
	})

	cfg := config.Config{
		Role:            config.RoleClient,
		EnabledCarriers: []string{"wbstream"},
		CarrierConfigs: []config.CarrierConfig{{
			ID:       "wbstream",
			Endpoint: config.EndpointConfig{ID: "wbstream-egress", Metadata: map[string]string{"display_name": "Android"}},
			WhitelistBypass: &config.WhitelistBypassConfig{
				DisplayName: "Android",
			},
		}},
	}

	if _, err := BuildCarrierBindingsWithRegistryAndTokens(cfg, registry, store); err != nil {
		t.Fatalf("BuildCarrierBindingsWithRegistryAndTokens: %v", err)
	}
	if captured.Credentials["access_token"] != "client-access-token" {
		t.Fatalf("access_token = %q, want TokenStore client credential", captured.Credentials["access_token"])
	}
	if captured.Credentials["cookie_header"] != "client-cookie-header" {
		t.Fatalf("cookie_header = %q, want TokenStore client credential", captured.Credentials["cookie_header"])
	}
}

func TestMobileProviderRegistryRejectsClientWBStreamWithoutCompositeCredential(t *testing.T) {
	ps := providers.NewStore()
	ks := keys.NewStore()
	registry := NewProviderRegistry(ps, ks)
	store := tokens.NewStore()
	store.Set(&tokens.Token{
		ID:        "wb-node",
		Platform:  "wbstream",
		Kind:      tokens.KindComposite,
		Lifecycle: tokens.LifecycleEmbedded,
		Status:    tokens.StatusActive,
		Parts: map[string]string{
			"access_token":  "node-access-token",
			"cookie_header": "node-cookie-header",
		},
	})
	store.Set(&tokens.Token{
		ID:        "wb-client",
		Platform:  "wbstream",
		Kind:      tokens.KindComposite,
		Lifecycle: tokens.LifecycleEmbedded,
		Status:    tokens.StatusActive,
		Parts:     map[string]string{"access_token": "client-access-token"},
	})
	store.AddBinding(tokens.Binding{TokenID: "wb-node", Platform: "wbstream", ConnectionType: "vp8", ChannelID: "*", Role: "node", Priority: 1, Enabled: true})
	store.AddBinding(tokens.Binding{TokenID: "wb-client", Platform: "wbstream", ConnectionType: "vp8", ChannelID: "*", Role: "client", Enabled: true})

	cfg := config.Config{
		Role:            config.RoleClient,
		EnabledCarriers: []string{"wbstream"},
		CarrierConfigs: []config.CarrierConfig{{
			ID:              "wbstream",
			Endpoint:        config.EndpointConfig{ID: "wbstream-egress", Metadata: map[string]string{"display_name": "Android"}},
			WhitelistBypass: &config.WhitelistBypassConfig{DisplayName: "Android"},
		}},
	}

	if _, err := BuildCarrierBindingsWithRegistryAndTokens(cfg, registry, store); err == nil {
		t.Fatal("expected missing client cookie credential to fail closed")
	}
}

type capturingProvider struct {
	*mockProvider
	capture *provider.ProviderConfig
}

func (p *capturingProvider) Configure(cfg provider.ProviderConfig) error {
	*p.capture = cfg
	return nil
}
