package adminrelay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

func TestCarrierUsesStableMessageKeyAndAcknowledgesProcessedPage(t *testing.T) {
	const envelopeID = "stable-release-message-id"
	var postedMessageKey string
	var acknowledgedMessageID string
	var acknowledgedConsumer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/relay/messages":
			var request struct {
				MessageKey string `json:"message_key"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			postedMessageKey = request.MessageKey
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost && r.URL.Path == "/api/relay/acks":
			var request struct {
				MessageID string `json:"message_id"`
				Consumer  string `json:"consumer"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			acknowledgedMessageID = request.MessageID
			acknowledgedConsumer = request.Consumer
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	defer server.Close()

	carrier := New(config.AdminRelayConfig{AdminURL: server.URL, Identity: "client"}, nil)
	endpoint := carriers.Endpoint{ID: "control", Metadata: map[string]string{"channel": "control", "recipient": "node"}}
	envelope := fabric.NewEnvelope(envelopeID, fabric.TrafficControl, "session.release", []byte(`{"session_id":"session-1"}`))
	if err := carrier.Write(context.Background(), endpoint, envelope); err != nil {
		t.Fatalf("write relay envelope: %v", err)
	}
	if err := carrier.Ack(context.Background(), endpoint, carriers.Cursor("server-message-1")); err != nil {
		t.Fatalf("acknowledge relay page: %v", err)
	}
	if postedMessageKey != envelopeID {
		t.Fatalf("posted message_key = %q, want %q", postedMessageKey, envelopeID)
	}
	if acknowledgedMessageID != "server-message-1" {
		t.Fatalf("acknowledged message_id = %q, want server-message-1", acknowledgedMessageID)
	}
	if acknowledgedConsumer != "client" {
		t.Fatalf("acknowledged consumer = %q, want client", acknowledgedConsumer)
	}
}
