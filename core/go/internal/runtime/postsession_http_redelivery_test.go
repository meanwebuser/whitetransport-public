package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/session"
)

// TestPostSessionHTTPRelayRedeliversUntilAck verifies at-least-once delivery:
// an ACK failure must replay the release, while the state transition remains
// idempotent and publishes node availability only once.
func TestPostSessionHTTPRelayRedeliversUntilAck(t *testing.T) {
	const (
		sessionID = "relay-redelivery-session"
		clientID  = "relay-redelivery-client"
		nodeID    = "relay-redelivery-node"
		messageID = "relay-redelivery-message"
	)
	payload, err := session.EncodePayload(session.Release{SessionID: sessionID, ClientID: clientID, NodeID: nodeID, Reason: "disconnect"})
	if err != nil {
		t.Fatalf("encode release: %v", err)
	}
	envelope := fabric.NewEnvelope(sessionID+":release", fabric.TrafficControl, session.PayloadSessionRelease, payload)
	envelope.SessionID = sessionID
	envelope.Source = clientID

	var fetchCount atomic.Int32
	var ackAttempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/relay/messages":
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Query().Get("since_id") == messageID {
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "messages": []any{}})
				return
			}
			fetchCount.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"messages": []map[string]any{{
					"id": messageID, "sender": clientID, "recipient": nodeID, "payload": envelope,
				}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/relay/acks":
			attempt := ackAttempts.Add(1)
			if attempt == 1 {
				http.Error(w, "transient ack failure", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	defer server.Close()

	bootstrapEndpoint := carriers.Endpoint{ID: "redelivery-bootstrap", Carrier: carriers.CarrierVKMessages, Address: "memory://redelivery-bootstrap"}
	bootstrapCarrier := newMemoryCarrierWithDescriptor(t, "redelivery-bootstrap", carriers.CarrierVKMessages)
	node, err := newTestControlPlane(
		config.Config{Role: config.RoleNode, NodeID: nodeID},
		map[string]policy.CarrierBinding{"redelivery-bootstrap": {Carrier: bootstrapCarrier, Endpoint: bootstrapEndpoint}},
		policy.DefaultAdaptivePolicy(), nil,
	)
	if err != nil {
		t.Fatalf("new node control plane: %v", err)
	}
	node.mu.Lock()
	node.nodeBusy = true
	node.nodeSessionID = sessionID
	node.nodeSessionClientID = clientID
	node.pollInterval = 20 * time.Millisecond
	node.mu.Unlock()
	if err := node.ConfigurePostSessionControl(config.AdminRelayConfig{
		Enabled: true, AdminURL: server.URL, Identity: nodeID, Channels: []string{"control"},
	}); err != nil {
		t.Fatalf("configure node relay: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	node.Start(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && ackAttempts.Load() < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	node.Stop()
	if ackAttempts.Load() != 2 || fetchCount.Load() < 2 {
		t.Fatalf("redelivery lifecycle: fetches=%d ack attempts=%d", fetchCount.Load(), ackAttempts.Load())
	}
	if got := countPayloads(t, bootstrapCarrier, bootstrapEndpoint, session.PayloadNodeAdvertise); got != 2 {
		t.Fatalf("node advertisements = %d, want startup plus one idempotent release transition", got)
	}
}
