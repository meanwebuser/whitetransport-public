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
	var sentText string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&sentBody); err != nil {
				t.Fatal(err)
			}
			message := sentBody["message"].(map[string]any)
			sentText = message["text"].(string)
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"items":   []map[string]any{{"id": 1, "message": map[string]string{"text": sentText}}},
		})
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
	sentText = message["text"].(string)

	// VK and OK share the message codec; the provider must see only words, not
	// the fabric ciphertext prefix or fixture plaintext.
	requireWordCiphertext(t, sentText, "secret-payload")

	decoded, err := decodeSocialMessageEnvelope(sentText, cipher)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ID != envelope.ID || string(decoded.Payload) != "secret-payload" {
		t.Fatalf("bad decrypted envelope: %+v", decoded)
	}
	read, err := carrier.Read(context.Background(), Endpoint{ID: "ok", Address: "chat:C123"}, Cursor("0"))
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Envelopes) != 1 || read.Envelopes[0].ID != envelope.ID || string(read.Envelopes[0].Payload) != "secret-payload" {
		t.Fatalf("OK provider roundtrip lost encrypted envelope: %+v", read)
	}
}

func TestOKMessagesCarrierFragmentsEncryptedEnvelope(t *testing.T) {
	key := fabric.DeriveBootstrapKey("ok-word-fragment-session")
	cipher, err := fabric.NewSessionCipher(key)
	if err != nil {
		t.Fatal(err)
	}

	var sentMessages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			message := body["message"].(map[string]any)
			sentMessages = append(sentMessages, message["text"].(string))
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		items := make([]map[string]any, 0, len(sentMessages))
		for index := len(sentMessages) - 1; index >= 0; index-- {
			items = append(items, map[string]any{"id": index + 1, "message": map[string]string{"text": sentMessages[index]}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "items": items})
	}))
	defer server.Close()

	carrier, err := NewOKMessagesCarrier(OKMessagesConfig{Token: "token", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	carrier.SetCipher(cipher)
	envelope := fabric.NewEnvelope("ok-fragmented", fabric.TrafficControl, "session.offer", bytes.Repeat([]byte("fragment-payload-"), 160))

	if err := carrier.Write(context.Background(), Endpoint{ID: "ok", Address: "chat:C123"}, envelope); err != nil {
		t.Fatal(err)
	}
	if len(sentMessages) < 2 {
		t.Fatalf("expected a multi-message word envelope, got %d provider messages", len(sentMessages))
	}
	for _, message := range sentMessages {
		requireWordCiphertext(t, message, "fragment-payload-")
	}

	read, err := carrier.Read(context.Background(), Endpoint{ID: "ok", Address: "chat:C123"}, Cursor("0"))
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Envelopes) != 1 || read.Envelopes[0].ID != envelope.ID || !bytes.Equal(read.Envelopes[0].Payload, envelope.Payload) {
		t.Fatalf("OK fragmented provider roundtrip lost encrypted envelope: %+v", read)
	}
}
