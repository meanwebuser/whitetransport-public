package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
)

func TestRunProbeWritesReadsAndAcknowledgesExactEnvelope(t *testing.T) {
	t.Parallel()

	const clientToken = "fixture-client-token"
	const nodeToken = "fixture-node-token"
	var mu sync.Mutex
	var payload string
	requests := make([]string, 0, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantToken := clientToken
		if r.Method == http.MethodGet || r.URL.Path == "/api/relay/acks" {
			wantToken = nodeToken
		}
		if r.Header.Get("Authorization") != "Bearer "+wantToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/relay/messages":
			var request struct {
				Payload string `json:"payload"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			mu.Lock()
			payload = request.Payload
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodGet && r.URL.Path == "/api/relay/messages":
			mu.Lock()
			stored := payload
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "messages": []map[string]any{{
				"id": "relay-message-1", "sender": "client", "recipient": "node", "payload": stored,
			}}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/relay/acks":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	target := server.Listener.Addr().String()
	dialer := (&net.Dialer{}).DialContext
	result, err := runProbe(context.Background(), probeConfig{
		RelayURL:    "http://relay.invalid:8181",
		ClientToken: clientToken,
		NodeToken:   nodeToken,
		EnvelopeID:  "probe-envelope-1",
		Payload:     []byte(`{"session_id":"session-1"}`),
	}, func(ctx context.Context, _ string, _ string) (net.Conn, error) {
		return dialer(ctx, "tcp", target)
	})
	if err != nil {
		t.Fatalf("run probe: %v", err)
	}
	if !result.EnvelopeExact || result.Cursor != carriers.Cursor("relay-message-1") || !result.Acknowledged {
		t.Fatalf("result = %+v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"POST /api/relay/messages", "GET /api/relay/messages", "POST /api/relay/acks"}
	if len(requests) != len(want) {
		t.Fatalf("requests = %v, want %v", requests, want)
	}
	for index := range want {
		if requests[index] != want[index] {
			t.Fatalf("requests = %v, want %v", requests, want)
		}
	}
}
