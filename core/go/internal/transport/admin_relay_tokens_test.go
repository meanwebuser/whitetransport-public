package transport

import (
	"strings"
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/tokens"
)

func TestResolveAdminRelayConfigUsesIdentityBoundTokenStoreCredential(t *testing.T) {
	store := tokens.NewStore()
	store.Set(&tokens.Token{ID: "relay-client-a", Platform: "admin", Kind: tokens.KindAPIKey, Lifecycle: tokens.LifecycleEmbedded, Status: tokens.StatusActive, Value: "client-a-relay-secret"})
	store.AddBinding(tokens.Binding{TokenID: "relay-client-a", Platform: "admin", ConnectionType: "relay", ChannelID: "client-a", Role: "control", Priority: 10, Enabled: true})

	resolved, err := resolveAdminRelayConfig(config.AdminRelayConfig{Enabled: true, AdminURL: "https://relay.example.invalid", Identity: "client-a"}, store, "ignored-default")
	if err != nil {
		t.Fatalf("resolve relay credential: %v", err)
	}
	if resolved.Token != "client-a-relay-secret" || resolved.Identity != "client-a" {
		t.Fatalf("resolved relay config = identity %q token %q", resolved.Identity, resolved.Token)
	}
	if resolved.TokenEnv != "" {
		t.Fatalf("resolved relay retained token env %q", resolved.TokenEnv)
	}
}

func TestResolveAdminRelayConfigRejectsMissingOrLegacyCredentials(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.AdminRelayConfig
		want string
	}{
		{name: "missing token store binding", cfg: config.AdminRelayConfig{Enabled: true, AdminURL: "https://relay.example.invalid", Identity: "client-a"}, want: "no active token"},
		{name: "legacy inline token", cfg: config.AdminRelayConfig{Enabled: true, AdminURL: "https://relay.example.invalid", Identity: "client-a", Token: "legacy"}, want: "TokenStore"},
		{name: "legacy environment token", cfg: config.AdminRelayConfig{Enabled: true, AdminURL: "https://relay.example.invalid", Identity: "client-a", TokenEnv: "WT_ADMIN_NODE_TOKEN"}, want: "TokenStore"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := resolveAdminRelayConfig(test.cfg, tokens.NewStore(), "client-a")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("resolve error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestResolveAdminRelayConfigUsesExplicitTokenReference(t *testing.T) {
	store := tokens.NewStore()
	store.Set(&tokens.Token{ID: "relay-node-a", Platform: "admin", Kind: tokens.KindAPIKey, Lifecycle: tokens.LifecycleEmbedded, Status: tokens.StatusActive, Value: "node-a-relay-secret"})

	resolved, err := resolveAdminRelayConfig(config.AdminRelayConfig{Enabled: true, AdminURL: "https://relay.example.invalid", TokenRef: "relay-node-a"}, store, "node-a")
	if err != nil {
		t.Fatalf("resolve explicit relay token: %v", err)
	}
	if resolved.Identity != "node-a" || resolved.Token != "node-a-relay-secret" {
		t.Fatalf("resolved explicit relay config = identity %q token %q", resolved.Identity, resolved.Token)
	}
}
