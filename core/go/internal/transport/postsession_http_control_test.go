package transport

import (
	"context"
	"strings"
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/tokens"
)

// TestStartValidatesEnabledPostSessionHTTPControl proves Transport wires the
// typed relay configuration before starting runtime goroutines.
func TestStartValidatesEnabledPostSessionHTTPControl(t *testing.T) {
	base := config.Config{Role: config.RoleClient, ClientID: "postsession-transport-client"}

	disabled := base
	disabled.AdminRelay = config.AdminRelayConfig{Enabled: false, AdminURL: "not-a-url"}
	transport, err := Start(context.Background(), disabled, nil)
	if err != nil {
		t.Fatalf("disabled post-session HTTP control affected startup: %v", err)
	}
	if err := transport.Stop(); err != nil {
		t.Fatalf("stop disabled-config transport: %v", err)
	}

	enabledInvalid := base
	enabledInvalid.AdminRelay = config.AdminRelayConfig{Enabled: true, AdminURL: "not-a-url"}
	tokenStore := tokens.NewStore()
	tokenStore.Set(&tokens.Token{ID: "postsession-transport-client", Platform: "admin", Kind: tokens.KindAPIKey, Lifecycle: tokens.LifecycleEmbedded, Status: tokens.StatusActive, Value: "test-relay-principal"})
	tokenStore.AddBinding(tokens.Binding{TokenID: "postsession-transport-client", Platform: "admin", ConnectionType: "relay", ChannelID: base.ClientID, Role: "control", Priority: 10, Enabled: true})
	transport, err = Start(context.Background(), enabledInvalid, tokenStore)
	if transport != nil {
		_ = transport.Stop()
	}
	if err == nil {
		t.Fatal("enabled invalid post-session HTTP control unexpectedly started")
	}
	if !strings.Contains(err.Error(), "configure post-session HTTP control") || !strings.Contains(err.Error(), "absolute HTTP(S) admin URL") {
		t.Fatalf("enabled invalid post-session HTTP control error = %v", err)
	}
}
