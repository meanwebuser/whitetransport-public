package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/carriers/adminrelay"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/session"
)

// TestControlPlaneReleaseFallsBackToHTTPRelayOverActiveEgress protects the
// post-session control contract: once bootstrap is unavailable, the active
// egress route must carry HTTP control without a direct-network escape hatch.
func TestControlPlaneReleaseFallsBackToHTTPRelayOverActiveEgress(t *testing.T) {
	const (
		sessionID = "session-over-egress-control"
		clientID  = "postsession-client"
		nodeID    = "postsession-node"
		relayHost = "relay-that-must-not-resolve.invalid:8181"
	)

	nodeControl := &ControlPlane{
		cfg:                 config.Config{Role: config.RoleNode, NodeID: nodeID},
		nodeBusy:            true,
		nodeSessionID:       sessionID,
		nodeSessionClientID: clientID,
	}

	var (
		receivedMu       sync.Mutex
		receivedEnvelope fabric.Envelope
	)
	relayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/relay/messages" {
			http.Error(w, "unexpected relay request", http.StatusNotFound)
			return
		}
		var request struct {
			Channel string `json:"channel"`
			Payload string `json:"payload"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if request.Channel != "control" {
			http.Error(w, "release was not sent on control channel", http.StatusBadRequest)
			return
		}
		var envelope fabric.Envelope
		if err := json.Unmarshal([]byte(request.Payload), &envelope); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		release, err := session.DecodePayload[session.Release](envelope.Payload)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		receivedMu.Lock()
		receivedEnvelope = envelope
		receivedMu.Unlock()
		nodeControl.handleRelease(r.Context(), release)
		w.WriteHeader(http.StatusCreated)
	}))
	defer relayServer.Close()

	egressEndpoint := carriers.Endpoint{
		ID:      "active-egress",
		Carrier: carriers.CarrierVKDocs1024,
		Address: "session://active-egress",
	}
	tunnel := &postSessionHTTPTestTunnel{
		expectedEndpoint: egressEndpoint,
		expectedTarget:   relayHost,
		actualTarget:     relayServer.Listener.Addr().String(),
	}
	bootstrapEndpoint := carriers.Endpoint{
		ID:      carriers.CarrierVKMessages,
		Carrier: carriers.CarrierVKMessages,
		Address: "memory://bootstrap-now-broken",
	}
	bootstrapCarrier := &writeFailingCarrier{memoryCarrier: newMemoryCarrierWithDescriptor(t, "postsession-bootstrap", carriers.CarrierVKMessages)}
	egressCarrier := newMemoryCarrierWithDescriptor(t, "postsession-egress", carriers.CarrierVKDocs1024)
	bindings := map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {Carrier: bootstrapCarrier, Endpoint: bootstrapEndpoint},
		carriers.CarrierVKDocs1024: {Carrier: egressCarrier, Endpoint: egressEndpoint},
	}
	clientControl, err := newTestControlPlane(
		config.Config{Role: config.RoleClient, ClientID: clientID, SocksListen: "127.0.0.1:0"},
		bindings,
		policy.DefaultAdaptivePolicy(),
		tunnel,
	)
	if err != nil {
		t.Fatalf("new client control plane: %v", err)
	}
	clientControl.active = &activeSession{
		NodeID:          nodeID,
		SessionID:       sessionID,
		ControlEndpoint: bootstrapEndpoint,
		ControlBinding:  bindings[carriers.CarrierVKMessages],
		EgressEndpoints: []carriers.Endpoint{egressEndpoint},
		UpdatedAt:       time.Now().UTC(),
	}

	if err := clientControl.ConfigurePostSessionControl(config.AdminRelayConfig{
		Enabled:  true,
		AdminURL: "http://" + relayHost,
		Identity: clientID,
		Channels: []string{"control"},
	}); err != nil {
		t.Fatalf("configure post-session HTTP control: %v", err)
	}
	clientControl.Disconnect()

	if got := bootstrapCarrier.writeCount(session.PayloadSessionRelease); got != 1 {
		t.Fatalf("bootstrap session.release attempts = %d, want one failed primary attempt", got)
	}
	if got := tunnel.dialCount.Load(); got != 1 {
		t.Fatalf("egress-injected relay dials = %d, want exactly one", got)
	}
	if got := tunnel.directFallbackCount.Load(); got != 0 {
		t.Fatalf("direct relay fallbacks = %d, want zero", got)
	}
	receivedMu.Lock()
	gotEnvelope := receivedEnvelope
	receivedMu.Unlock()
	if gotEnvelope.PayloadType != session.PayloadSessionRelease || gotEnvelope.SessionID != sessionID {
		t.Fatalf("relay envelope = type %q session %q, want %q for %q", gotEnvelope.PayloadType, gotEnvelope.SessionID, session.PayloadSessionRelease, sessionID)
	}
	nodeControl.mu.RLock()
	nodeStillBusy := nodeControl.nodeBusy
	nodeSessionID := nodeControl.nodeSessionID
	nodeControl.mu.RUnlock()
	if nodeStillBusy || nodeSessionID != "" {
		t.Fatalf("node did not handle relayed release: busy=%v session=%q", nodeStillBusy, nodeSessionID)
	}
}

// TestAdminRelayIsNeverAdvertisedAsSessionEgress prevents a control-over-
// egress relay from recursively becoming the egress route it depends on.
func TestAdminRelayIsNeverAdvertisedAsSessionEgress(t *testing.T) {
	bootstrap := newMemoryCarrierWithCustomDescriptor("postsession-bootstrap", fabric.TrafficBootstrap, fabric.TrafficControl)
	bootstrapEndpoint := carriers.Endpoint{ID: "bootstrap", Carrier: "bootstrap", Address: "memory://postsession-bootstrap"}
	relay := adminrelay.NewWithDialContext(config.AdminRelayConfig{
		Enabled:  true,
		AdminURL: "http://relay-that-must-not-resolve.invalid:8181",
		Identity: "postsession-node",
	}, nil, nil)
	relayEndpoint := carriers.Endpoint{ID: adminrelay.CarrierID, Carrier: adminrelay.CarrierID, Address: "control"}
	control, err := newTestControlPlane(
		config.Config{Role: config.RoleNode, NodeID: "postsession-node", SocksListen: "127.0.0.1:0"},
		map[string]policy.CarrierBinding{
			"bootstrap":          {Carrier: bootstrap, Endpoint: bootstrapEndpoint},
			adminrelay.CarrierID: {Carrier: relay, Endpoint: relayEndpoint},
		},
		policy.DefaultAdaptivePolicy(),
		nil,
	)
	if err != nil {
		t.Fatalf("new node control plane: %v", err)
	}
	for _, endpoint := range control.buildEgressEndpoints(context.Background()) {
		if endpoint.Carrier == adminrelay.CarrierID || endpoint.ID == adminrelay.CarrierID {
			t.Fatalf("admin relay leaked into egress endpoints: %+v", endpoint)
		}
	}
}

func TestConfigurePostSessionControlRejectsUnsafeConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.AdminRelayConfig
	}{
		{name: "disabled", cfg: config.AdminRelayConfig{AdminURL: "https://relay.example.invalid"}},
		{name: "missing URL", cfg: config.AdminRelayConfig{Enabled: true}},
		{name: "non HTTP URL", cfg: config.AdminRelayConfig{Enabled: true, AdminURL: "file:///tmp/relay"}},
		{name: "missing control channel", cfg: config.AdminRelayConfig{Enabled: true, AdminURL: "https://relay.example.invalid", Channels: []string{"logs"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			control := &ControlPlane{cfg: config.Config{Role: config.RoleClient, ClientID: "postsession-client"}}
			if err := control.ConfigurePostSessionControl(test.cfg); err == nil {
				t.Fatalf("ConfigurePostSessionControl(%+v) unexpectedly succeeded", test.cfg)
			}
			if control.postSessionControl != nil {
				t.Fatal("invalid post-session control config was retained")
			}
		})
	}
}

type postSessionHTTPTestTunnel struct {
	expectedEndpoint    carriers.Endpoint
	expectedTarget      string
	actualTarget        string
	dialCount           atomic.Int32
	directFallbackCount atomic.Int32
}

func (t *postSessionHTTPTestTunnel) SupportsEndpoint(endpoint carriers.Endpoint) bool {
	return sameSessionEndpoint(endpoint, t.expectedEndpoint)
}

func (t *postSessionHTTPTestTunnel) DialContext(ctx context.Context, endpoint carriers.Endpoint, targetAddr string) (net.Conn, error) {
	if !sameSessionEndpoint(endpoint, t.expectedEndpoint) {
		return nil, fmt.Errorf("unexpected egress endpoint: %+v", endpoint)
	}
	if targetAddr != t.expectedTarget {
		t.directFallbackCount.Add(1)
		return nil, fmt.Errorf("unexpected relay target %q", targetAddr)
	}
	t.dialCount.Add(1)
	return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", t.actualTarget)
}
