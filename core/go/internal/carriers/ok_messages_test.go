package carriers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

func TestOKMessagesCarrierWritesEnvelopeThroughGraphMessages(t *testing.T) {
	var seenPath string
	var seenToken string
	var seenBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		seenToken = r.URL.Query().Get("access_token")
		if err := json.NewDecoder(r.Body).Decode(&seenBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()
	carrier, err := NewOKMessagesCarrier(OKMessagesConfig{Token: "token", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	envelope := fabric.NewEnvelope("env-1", fabric.TrafficControl, "session.offer", []byte("secret-bytes"))

	if err := carrier.Write(context.Background(), Endpoint{ID: "ok", Address: "chat:C123"}, envelope); err != nil {
		t.Fatal(err)
	}

	if seenPath != "/me/messages" || seenToken != "token" {
		t.Fatalf("bad OK request path/token: %s %s", seenPath, seenToken)
	}
	recipient := seenBody["recipient"].(map[string]any)
	if recipient["chat_id"] != "chat:C123" {
		t.Fatalf("bad OK recipient: %+v", recipient)
	}
	message := seenBody["message"].(map[string]any)
	decoded, err := decodeMailboxEnvelope(message["text"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ID != envelope.ID || string(decoded.Payload) != "secret-bytes" {
		t.Fatalf("bad encoded envelope: %+v", decoded)
	}
}

func TestOKMessagesCarrierReadsNewEnvelopesInAscendingMessageOrder(t *testing.T) {
	first := fabric.NewEnvelope("env-1", fabric.TrafficControl, "control.frame", []byte("one"))
	second := fabric.NewEnvelope("env-2", fabric.TrafficControl, "control.frame", []byte("two"))
	encodedFirst, err := encodeMailboxEnvelope(first)
	if err != nil {
		t.Fatal(err)
	}
	encodedSecond, err := encodeMailboxEnvelope(second)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		if r.URL.Query().Get("chat_id") != "chat:C123" {
			t.Fatalf("missing chat_id query: %s", r.URL.RawQuery)
		}
		payload := map[string]any{
			"items": []map[string]any{
				{"id": 12, "message": map[string]string{"text": encodedSecond}},
				{"id": 11, "text": "ignore me"},
				{"id": 10, "text": encodedFirst},
				{"id": 9, "text": encodedFirst},
			},
		}
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Fatal(err)
		}
	}))
	defer server.Close()
	carrier, err := NewOKMessagesCarrier(OKMessagesConfig{Token: "token", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}

	read, err := carrier.Read(context.Background(), Endpoint{ID: "ok", Address: "chat:C123"}, Cursor("9"))
	if err != nil {
		t.Fatal(err)
	}

	if read.Cursor != "12" {
		t.Fatalf("expected cursor 12, got %s", read.Cursor)
	}
	if len(read.Envelopes) != 2 {
		t.Fatalf("expected two envelopes, got %d", len(read.Envelopes))
	}
	if read.Envelopes[0].ID != "env-1" || read.Envelopes[1].ID != "env-2" {
		t.Fatalf("expected ascending OK message order, got %+v", read.Envelopes)
	}
}

func TestOKMessagesCarrierReportsGraphErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"error_code":401,"error":"auth failed"}`))
	}))
	defer server.Close()
	carrier, err := NewOKMessagesCarrier(OKMessagesConfig{Token: "token", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}

	err = carrier.Write(context.Background(), Endpoint{ID: "ok", Address: "chat:C123"}, fabric.NewEnvelope("env", fabric.TrafficControl, "x", nil))
	if err == nil || !strings.Contains(err.Error(), "error_code") {
		t.Fatalf("expected OK error, got %v", err)
	}
}

func TestOKMessagesCarrierRequiresTokenAndRecipient(t *testing.T) {
	if _, err := NewOKMessagesCarrier(OKMessagesConfig{}); err == nil {
		t.Fatal("expected missing token error")
	}
	carrier, err := NewOKMessagesCarrier(OKMessagesConfig{Token: "token"})
	if err != nil {
		t.Fatal(err)
	}
	if err := carrier.Write(context.Background(), Endpoint{}, fabric.NewEnvelope("env", fabric.TrafficControl, "x", nil)); err == nil {
		t.Fatal("expected missing recipient error")
	}
}

func TestOKMessagesCarrierRejectsOversizedEnvelopeBeforeAPICall(t *testing.T) {
	carrier, err := NewOKMessagesCarrier(OKMessagesConfig{
		Token:   "token",
		BaseURL: "http://127.0.0.1:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	// 3500 bytes of payload -> JSON ~3600 -> base64 ~4800 -> exceeds 4096 limit.
	envelope := fabric.NewEnvelope("too-large", fabric.TrafficControl, "control.frame", bytes.Repeat([]byte("x"), 3500))

	err = carrier.Write(context.Background(), Endpoint{ID: "ok", Address: "chat:C123"}, envelope)
	if err == nil || !strings.Contains(err.Error(), "exceeds carrier limit") {
		t.Fatalf("expected local size limit error, got %v", err)
	}
}

func TestOKMessagesCarrierEncryptionRoundTrip(t *testing.T) {
	key := fabric.DeriveBootstrapKey("test-secret-token")
	cipher, err := fabric.NewSessionCipher(key)
	if err != nil {
		t.Fatal(err)
	}

	var sentBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&sentBody); err != nil {
				t.Fatal(err)
			}
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()

	carrier, err := NewOKMessagesCarrier(OKMessagesConfig{Token: "token", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	carrier.SetCipher(cipher)

	envelope := fabric.NewEnvelope("enc-1", fabric.TrafficControl, "session.offer", []byte("secret-payload"))
	if err := carrier.Write(context.Background(), Endpoint{ID: "ok", Address: "chat:C123"}, envelope); err != nil {
		t.Fatal(err)
	}

	message := sentBody["message"].(map[string]any)
	sentText := message["text"].(string)

	// The sent message should use the encrypted prefix.
	if strings.HasPrefix(sentText, "wtmsg1.") {
		t.Fatal("encrypted carrier should not send plain mailbox envelopes")
	}
	if !strings.HasPrefix(sentText, "wtenc1.") {
		t.Fatalf("expected wtenc1. prefix, got %q", sentText[:20])
	}

	decoded, err := decodeMailboxEnvelope(sentText, cipher)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ID != envelope.ID || string(decoded.Payload) != "secret-payload" {
		t.Fatalf("bad decrypted envelope: %+v", decoded)
	}
}
