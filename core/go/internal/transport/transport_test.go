package transport

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/runtime"
	"github.com/meanwebuser/whitetransport/core/internal/tokens"
)

func TestBuildTokenStorePreservesExpiry(t *testing.T) {
	expiresAt := "2030-01-02T03:04:05Z"
	store := BuildTokenStore(config.Config{
		TokenStore: &config.TokenStoreConfig{
			Tokens: []config.TokenEntry{{
				ID:        "vk-test",
				Platform:  "vk",
				Kind:      tokens.KindAPIKey,
				Lifecycle: tokens.LifecycleEmbedded,
				Value:     "secret",
				ExpiresAt: &expiresAt,
			}},
			Bindings: []config.BindingEntry{{
				TokenID:        "vk-test",
				Platform:       "vk",
				ConnectionType: "messages",
				ChannelID:      "*",
				Role:           "discovery",
				Priority:       10,
				Enabled:        true,
			}},
		},
	})
	if store == nil {
		t.Fatal("expected token store")
	}

	tok, err := store.ResolveOne("vk", "messages", "*")
	if err != nil {
		t.Fatalf("resolve token: %v", err)
	}
	if tok.ExpiresAt == nil {
		t.Fatal("expected ExpiresAt to be preserved")
	}
	if !tok.ExpiresAt.Equal(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("unexpected ExpiresAt: %v", tok.ExpiresAt)
	}
	if tok.CreatedAt.IsZero() {
		t.Fatal("expected CreatedAt to be set")
	}
}

func TestStartReturnsErrorWhenSocksListenUnavailable(t *testing.T) {
	oldAttempts := socksListenPollAttempts
	oldInterval := socksListenPollInterval
	socksListenPollAttempts = 2
	socksListenPollInterval = time.Millisecond
	t.Cleanup(func() {
		socksListenPollAttempts = oldAttempts
		socksListenPollInterval = oldInterval
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve tcp addr: %v", err)
	}
	defer ln.Close()

	_, err = Start(context.Background(), config.Config{
		Role:        config.RoleClient,
		ClientID:    "test-client",
		SocksListen: ln.Addr().String(),
	}, nil)
	if err == nil {
		t.Fatal("expected socks listen failure")
	}
	if !strings.Contains(err.Error(), "socks5 proxy failed to listen") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStartKeepsLocalRuntimeBlockedWhenAllCarrierConstructionFails(t *testing.T) {
	tp, err := Start(context.Background(), config.Config{
		Role:            config.RoleClient,
		ClientID:        "blocked-client",
		SocksListen:     "127.0.0.1:0",
		EnabledCarriers: []string{"broken.vk"},
		CarrierConfigs: []config.CarrierConfig{{
			ID:          "broken.vk",
			CarrierType: carriers.CarrierVKMessages,
			Endpoint:    config.EndpointConfig{Address: "2000000001"},
			VKMessages:  &config.VKMessagesConfig{},
		}},
	}, nil)
	if err != nil {
		t.Fatalf("blocked runtime must still start: %v", err)
	}
	t.Cleanup(func() { _ = tp.Stop() })

	status := tp.Status()
	if status.State != "blocked" || status.LastError != "no executable control carrier" || tp.GetSocksAddr() == "" {
		t.Fatalf("unexpected blocked status: %+v socks=%q", status, tp.GetSocksAddr())
	}
	broken := tp.CarrierHealthSnapshot()["broken.vk"]
	if broken.LifecycleState != "degraded" || broken.ErrorCode != "credential_missing" {
		t.Fatalf("unexpected carrier health: %+v", broken)
	}
	if _, err := tp.Connect(context.Background(), "node"); err == nil || !strings.Contains(err.Error(), "transport blocked") {
		t.Fatalf("blocked connect error = %v", err)
	}
}

func TestStartReportsBootstrapSpecificBlockedNode(t *testing.T) {
	tp, err := Start(context.Background(), config.Config{
		Role:            config.RoleNode,
		NodeID:          "blocked-node",
		EnabledCarriers: []string{"broken.vk"},
		CarrierConfigs: []config.CarrierConfig{{
			ID:          "broken.vk",
			CarrierType: carriers.CarrierVKMessages,
			Endpoint:    config.EndpointConfig{Address: "2000000001"},
			VKMessages:  &config.VKMessagesConfig{},
		}},
	}, nil)
	if err != nil {
		t.Fatalf("blocked node runtime must still start: %v", err)
	}
	t.Cleanup(func() { _ = tp.Stop() })

	status := tp.Status()
	if status.State != "blocked" || status.LastError != "no executable bootstrap carrier" {
		t.Fatalf("unexpected blocked node status: %+v", status)
	}
}

func TestStartRejectsNativeSplitRoutingModes(t *testing.T) {
	_, err := Start(context.Background(), config.Config{
		Role:     config.RoleClient,
		ClientID: "native-routing-owner-test",
		Routing:  config.RoutingConfig{Mode: "ru_direct"},
	}, nil)
	if err == nil {
		t.Fatal("Start succeeded with a system-owned split-routing mode")
	}
	if !strings.Contains(err.Error(), "native/system VPN owns split routing") {
		t.Fatalf("Start error = %v, want native/system VPN ownership guidance", err)
	}
}

func TestTransportStopIsConcurrentAndIdempotent(t *testing.T) {
	transport, err := Start(context.Background(), config.Config{
		Role:     config.RoleClient,
		ClientID: "concurrent-stop-test",
	}, nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	const callers = 8
	results := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() { results <- transport.Stop() }()
	}
	var first error
	for i := 0; i < callers; i++ {
		err := <-results
		if i == 0 {
			first = err
			continue
		}
		if (err == nil) != (first == nil) || (err != nil && err.Error() != first.Error()) {
			t.Fatalf("Stop result %v differs from first result %v", err, first)
		}
	}
	if transport.Started() {
		t.Fatal("transport remains started after concurrent Stop calls")
	}
}

// TestTransportExposesExplicitEgressSelection keeps the daemon API facade in
// lockstep with ControlPlane. Without this forwarding method, the endpoint
// exists in source but the packaged daemon returns HTTP 501 to the Mac UI.
func TestTransportExposesExplicitEgressSelection(t *testing.T) {
	control, err := runtime.NewControlPlane(
		config.Config{Role: config.RoleClient, ClientID: "transport-egress-test", SocksListen: "127.0.0.1:0"},
		nil,
		policy.DefaultAdaptivePolicy(),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	transport := &Transport{control: control}
	_, err = transport.SelectEgressEndpoint("xray-de-httpupgrade")
	if err == nil || !strings.Contains(err.Error(), "without an active session") {
		t.Fatalf("SelectEgressEndpoint error = %v, want active-session rejection", err)
	}
}
