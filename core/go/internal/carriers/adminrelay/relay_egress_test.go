package adminrelay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

func TestCarrierWriteReadUsesInjectedDialContext(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var stored fabric.Envelope
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			var request struct {
				Payload string `json:"payload"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			var decoded fabric.Envelope
			if err := json.Unmarshal([]byte(request.Payload), &decoded); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			mu.Lock()
			stored = decoded
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
		case http.MethodGet:
			mu.Lock()
			payload := stored
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"messages": []map[string]any{{
					"id": "relay-message-1", "sender": "node", "recipient": "client", "payload": payload,
				}},
			})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	target := server.Listener.Addr().String()
	var dialedAddress string
	dialer := (&net.Dialer{Timeout: time.Second}).DialContext
	dialContext := func(ctx context.Context, network string, address string) (net.Conn, error) {
		dialedAddress = address
		return dialer(ctx, network, target)
	}
	carrier := NewWithDialContext(config.AdminRelayConfig{
		AdminURL: "http://relay-hostname-that-must-not-resolve.invalid:8181",
		Identity: "client",
	}, nil, dialContext)
	endpoint := carriers.Endpoint{ID: "control"}
	want := fabric.NewEnvelope("envelope-1", fabric.TrafficControl, "session.offer", []byte("exact-fabric-payload"))

	if err := carrier.Write(context.Background(), endpoint, want); err != nil {
		t.Fatalf("Write through injected dialer: %v", err)
	}
	result, err := carrier.Read(context.Background(), endpoint, "")
	if err != nil {
		t.Fatalf("Read through injected dialer: %v", err)
	}
	if dialedAddress != "relay-hostname-that-must-not-resolve.invalid:8181" {
		t.Fatalf("injected dialer address = %q, want unresolved relay address", dialedAddress)
	}
	if len(result.Envelopes) != 1 || !reflect.DeepEqual(result.Envelopes[0], want) {
		t.Fatalf("round-trip envelopes = %#v, want exact %#v", result.Envelopes, want)
	}
}

func TestCarrierInjectedDialContextErrorSurfaces(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("egress carrier unavailable")
	carrier := NewWithDialContext(config.AdminRelayConfig{
		AdminURL: "http://relay-hostname-that-must-not-resolve.invalid:8181",
		Identity: "client",
	}, nil, func(context.Context, string, string) (net.Conn, error) {
		return nil, wantErr
	})
	envelope := fabric.NewEnvelope("envelope-1", fabric.TrafficControl, "session.offer", []byte("payload"))

	err := carrier.Write(context.Background(), carriers.Endpoint{ID: "control"}, envelope)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Write error = %v, want wrapped injected dialer error %v", err, wantErr)
	}
	if err == nil || err.Error() != fmt.Sprintf("relay write: Post \"http://relay-hostname-that-must-not-resolve.invalid:8181/api/relay/messages\": %v", wantErr) {
		t.Fatalf("Write error = %q, want explicit relay write context", err)
	}
}

func TestCarrierNilInjectedDialContextDoesNotFallBackToDirectNetwork(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	carrier := NewWithDialContext(config.AdminRelayConfig{AdminURL: server.URL, Identity: "client"}, nil, nil)
	envelope := fabric.NewEnvelope("envelope-1", fabric.TrafficControl, "session.offer", []byte("payload"))
	err := carrier.Write(context.Background(), carriers.Endpoint{ID: "control"}, envelope)

	if err == nil || !strings.Contains(err.Error(), "dial context is required") {
		t.Fatalf("Write error = %v, want explicit missing dial context failure", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("direct relay requests = %d, want zero", requests.Load())
	}
}
