package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/session"
)

// TestPostSessionHTTPRelayIsPolledAndDispatchedByNode protects the complete
// receive boundary. The HTTP handler only stores and returns messages; unlike
// the send-path test it never calls ControlPlane handlers directly.
func TestPostSessionHTTPRelayIsPolledAndDispatchedByNode(t *testing.T) {
	const (
		sessionID = "session-through-real-relay-poll"
		clientID  = "relay-poll-client"
		nodeID    = "relay-poll-node"
		relayHost = "relay-poll-that-must-not-resolve.invalid:8181"
	)

	type storedMessage struct {
		ID        string
		Sender    string
		Recipient string
		Payload   string
	}
	var (
		messagesMu sync.Mutex
		messages   []storedMessage
		postCount  atomic.Int32
		getCount   atomic.Int32
		fetchCount atomic.Int32
		ackCount   atomic.Int32
	)
	relayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer relay-test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/relay/messages":
			var request struct {
				Sender    string `json:"sender"`
				Recipient string `json:"recipient"`
				Payload   string `json:"payload"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			messagesMu.Lock()
			id := fmt.Sprintf("relay-%d", len(messages)+1)
			messages = append(messages, storedMessage{ID: id, Sender: request.Sender, Recipient: request.Recipient, Payload: request.Payload})
			messagesMu.Unlock()
			postCount.Add(1)
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodGet && r.URL.Path == "/api/relay/messages":
			getCount.Add(1)
			if r.URL.Query().Get("channel") != "control" || r.URL.Query().Get("recipient") != nodeID {
				http.Error(w, "wrong inbox", http.StatusBadRequest)
				return
			}
			if r.URL.Query().Has("mark_read") {
				http.Error(w, "fetch must not acknowledge", http.StatusBadRequest)
				return
			}
			recipient := r.URL.Query().Get("recipient")
			sinceID := r.URL.Query().Get("since_id")
			messagesMu.Lock()
			started := sinceID == ""
			result := make([]map[string]any, 0, len(messages))
			for _, message := range messages {
				if !started {
					started = message.ID == sinceID
					continue
				}
				if message.Recipient != "" && message.Recipient != recipient {
					continue
				}
				result = append(result, map[string]any{
					"id": message.ID, "sender": message.Sender, "recipient": message.Recipient, "payload": message.Payload,
				})
			}
			messagesMu.Unlock()
			if len(result) > 0 {
				fetchCount.Add(1)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "messages": result})
		case r.Method == http.MethodPost && r.URL.Path == "/api/relay/acks":
			var request struct {
				Channel   string `json:"channel"`
				Consumer  string `json:"consumer"`
				MessageID string `json:"message_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if request.Channel != "control" || request.Consumer != nodeID || request.MessageID == "" {
				http.Error(w, "invalid acknowledgement", http.StatusBadRequest)
				return
			}
			ackCount.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	defer relayServer.Close()

	nodeBootstrapEndpoint := carriers.Endpoint{ID: "node-bootstrap", Carrier: carriers.CarrierVKMessages, Address: "memory://node-relay-poll-bootstrap"}
	nodeBootstrapCarrier := newMemoryCarrierWithDescriptor(t, "node-relay-poll-bootstrap", carriers.CarrierVKMessages)
	nodeControl, err := newTestControlPlane(
		config.Config{Role: config.RoleNode, NodeID: nodeID},
		map[string]policy.CarrierBinding{
			"node-bootstrap": {Carrier: nodeBootstrapCarrier, Endpoint: nodeBootstrapEndpoint},
		},
		policy.DefaultAdaptivePolicy(), nil,
	)
	if err != nil {
		t.Fatalf("new node control plane: %v", err)
	}
	nodeControl.mu.Lock()
	nodeControl.nodeBusy = true
	nodeControl.nodeSessionID = sessionID
	nodeControl.nodeSessionClientID = clientID
	nodeControl.pollInterval = 25 * time.Millisecond
	nodeControl.mu.Unlock()
	if err := nodeControl.ConfigurePostSessionControl(config.AdminRelayConfig{
		Enabled: true, AdminURL: relayServer.URL, Token: "relay-test-token", Identity: nodeID, Channels: []string{"control"},
	}); err != nil {
		t.Fatalf("configure node relay: %v", err)
	}
	defer nodeControl.Stop()
	nodeControl.Start(context.Background())

	egressEndpoint := carriers.Endpoint{ID: "relay-poll-egress", Carrier: carriers.CarrierVKDocs1024, Address: "session://relay-poll-egress"}
	tunnel := &postSessionHTTPTestTunnel{
		expectedEndpoint: egressEndpoint,
		expectedTarget:   relayHost,
		actualTarget:     relayServer.Listener.Addr().String(),
	}
	bootstrapEndpoint := carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://relay-poll-bootstrap"}
	bootstrapCarrier := &writeFailingCarrier{memoryCarrier: newMemoryCarrierWithDescriptor(t, "relay-poll-bootstrap", carriers.CarrierVKMessages)}
	bindings := map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {Carrier: bootstrapCarrier, Endpoint: bootstrapEndpoint},
		carriers.CarrierVKDocs1024: {Carrier: newMemoryCarrierWithDescriptor(t, "relay-poll-egress", carriers.CarrierVKDocs1024), Endpoint: egressEndpoint},
	}
	clientControl, err := newTestControlPlane(
		config.Config{Role: config.RoleClient, ClientID: clientID, SocksListen: "127.0.0.1:0"}, bindings, policy.DefaultAdaptivePolicy(), tunnel,
	)
	if err != nil {
		t.Fatalf("new client control plane: %v", err)
	}
	clientControl.active = &activeSession{
		NodeID: nodeID, SessionID: sessionID, ControlEndpoint: bootstrapEndpoint,
		ControlBinding: bindings[carriers.CarrierVKMessages], EgressEndpoints: []carriers.Endpoint{egressEndpoint}, UpdatedAt: time.Now().UTC(),
	}
	if err := clientControl.ConfigurePostSessionControl(config.AdminRelayConfig{
		Enabled: true, AdminURL: "http://" + relayHost, Token: "relay-test-token", Identity: clientID, Channels: []string{"control"},
	}); err != nil {
		t.Fatalf("configure client relay: %v", err)
	}
	clientControl.Disconnect()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		nodeControl.mu.RLock()
		busy := nodeControl.nodeBusy
		nodeControl.mu.RUnlock()
		if !busy {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	nodeControl.mu.RLock()
	nodeStillBusy := nodeControl.nodeBusy
	nodeControl.mu.RUnlock()
	if nodeStillBusy {
		t.Fatalf("node did not poll and dispatch relayed %s", session.PayloadSessionRelease)
	}
	if getCount.Load() == 0 {
		t.Fatal("relay server received no node-side GET polling")
	}
	if postCount.Load() != 1 || fetchCount.Load() == 0 {
		t.Fatalf("relay lifecycle: posts=%d successful fetches=%d", postCount.Load(), fetchCount.Load())
	}
	if ackCount.Load() != 1 {
		t.Fatalf("relay delivery acknowledgements = %d, want one", ackCount.Load())
	}
	if tunnel.dialCount.Load() != 1 || tunnel.directFallbackCount.Load() != 0 {
		t.Fatalf("client relay path: injected dials=%d direct fallbacks=%d", tunnel.dialCount.Load(), tunnel.directFallbackCount.Load())
	}
}
