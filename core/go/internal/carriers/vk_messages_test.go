package carriers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

func TestVKCarriersCallReachableDefaultAPI(t *testing.T) {
	client := vkDocsTestHTTPDoer(func(req *http.Request) (*http.Response, error) {
		if req.URL.Scheme != "https" || req.URL.Host != "api.vk.ru" || req.URL.Path != "/method/messages.getHistory" {
			t.Errorf("VK history request used %s", req.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"response":{"items":[]}}`)),
		}, nil
	})
	messages, err := NewVKMessagesCarrier(VKMessagesConfig{Token: "fixture", HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	documents, err := NewVKDocsCarrier(VKDocsConfig{Token: "fixture", HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := Endpoint{Address: "2000000001"}
	if _, err := messages.Read(t.Context(), endpoint, ""); err != nil {
		t.Fatalf("VK Messages history: %v", err)
	}
	if _, err := documents.Read(t.Context(), endpoint, ""); err != nil {
		t.Fatalf("VK Documents history: %v", err)
	}
}

func TestVKMessagesCarrierWritesEnvelopeThroughMessagesSend(t *testing.T) {
	var seenPath string
	var seenForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		seenForm = r.PostForm
		_, _ = w.Write([]byte(`{"response":123}`))
	}))
	defer server.Close()
	carrier, err := NewVKMessagesCarrier(VKMessagesConfig{Token: "token", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	envelope := fabric.NewEnvelope("env-1", fabric.TrafficControl, "session.offer", []byte("secret-bytes"))

	if err := carrier.Write(context.Background(), Endpoint{ID: "vk", Address: "2000000001"}, envelope); err != nil {
		t.Fatal(err)
	}

	if seenPath != "/messages.send" {
		t.Fatalf("expected messages.send path, got %s", seenPath)
	}
	if seenForm.Get("peer_id") != "2000000001" || seenForm.Get("access_token") != "token" {
		t.Fatalf("bad VK form: %v", seenForm)
	}
	decoded, err := decodeMailboxEnvelope(seenForm.Get("message"))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ID != envelope.ID || string(decoded.Payload) != "secret-bytes" {
		t.Fatalf("bad encoded envelope: %+v", decoded)
	}
}

func TestVKMessagesCarrierReadsNewEnvelopesInAscendingMessageOrder(t *testing.T) {
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
		if !strings.HasSuffix(r.URL.Path, "/messages.getHistory") {
			_, _ = w.Write([]byte(`{"response":1}`))
			return
		}
		payload := map[string]any{
			"response": map[string]any{
				"items": []map[string]any{
					{"id": 12, "text": encodedSecond},
					{"id": 11, "text": "ignore me"},
					{"id": 10, "text": encodedFirst},
					{"id": 9, "text": encodedFirst},
				},
			},
		}
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Fatal(err)
		}
	}))
	defer server.Close()
	carrier, err := NewVKMessagesCarrier(VKMessagesConfig{Token: "token", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}

	read, err := carrier.Read(context.Background(), Endpoint{ID: "vk", Address: "2000000001"}, Cursor("9"))
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
		t.Fatalf("expected ascending VK message order, got %+v", read.Envelopes)
	}
}

func TestVKMessagesCarrierReportsVKErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"error":{"error_code":5,"error_msg":"auth failed"}}`))
	}))
	defer server.Close()
	carrier, err := NewVKMessagesCarrier(VKMessagesConfig{Token: "token", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}

	err = carrier.Write(context.Background(), Endpoint{ID: "vk", Address: "2000000001"}, fabric.NewEnvelope("env", fabric.TrafficControl, "x", nil))
	if err == nil || !strings.Contains(err.Error(), "error_code") {
		t.Fatalf("expected VK error, got %v", err)
	}
}

func TestVKMessagesCarrierRetriesFloodControl(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts == 1 {
			_, _ = w.Write([]byte(`{"error":{"error_code":9,"error_msg":"flood control"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"response":123}`))
	}))
	defer server.Close()
	carrier, err := NewVKMessagesCarrier(VKMessagesConfig{Token: "token", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := carrier.Write(context.Background(), Endpoint{ID: "vk", Address: "2000000001"}, fabric.NewEnvelope("env", fabric.TrafficControl, "x", nil)); err != nil {
		t.Fatalf("expected flood-control retry to succeed: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts=%d, want 2", attempts)
	}
}

func TestVKMessagesCarrierRequiresTokenAndPeerID(t *testing.T) {
	if _, err := NewVKMessagesCarrier(VKMessagesConfig{}); err == nil {
		t.Fatal("expected missing token error")
	}
	carrier, err := NewVKMessagesCarrier(VKMessagesConfig{Token: "token"})
	if err != nil {
		t.Fatal(err)
	}
	if err := carrier.Write(context.Background(), Endpoint{}, fabric.NewEnvelope("env", fabric.TrafficControl, "x", nil)); err == nil {
		t.Fatal("expected missing peer id error")
	}
}

func TestVKMessagesCarrierRejectsOversizedEnvelopeBeforeAPICall(t *testing.T) {
	carrier, err := NewVKMessagesCarrier(VKMessagesConfig{Token: "token", BaseURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	// 3500 bytes of payload -> JSON ~3600 -> base64 ~4800 -> exceeds 4096 limit.
	envelope := fabric.NewEnvelope("too-large", fabric.TrafficControl, "control.frame", bytes.Repeat([]byte("x"), 3500))

	err = carrier.Write(context.Background(), Endpoint{ID: "vk", Address: "2000000001"}, envelope)
	if err == nil || !strings.Contains(err.Error(), "exceeds carrier limit") {
		t.Fatalf("expected local size limit error, got %v", err)
	}
}

func TestVKMessagesCarrierEncryptionRoundTrip(t *testing.T) {
	key := fabric.DeriveBootstrapKey("test-secret-token")
	cipher, err := fabric.NewSessionCipher(key)
	if err != nil {
		t.Fatal(err)
	}

	var sentMessage string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/messages.getHistory" {
			_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{
				"items": []map[string]any{{"id": 1, "text": sentMessage}},
			}})
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		sentMessage = r.PostForm.Get("message")
		_, _ = w.Write([]byte(`{"response":123}`))
	}))
	defer server.Close()

	carrier, err := NewVKMessagesCarrier(VKMessagesConfig{Token: "token", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	carrier.SetCipher(cipher)

	envelope := fabric.NewEnvelope("enc-1", fabric.TrafficControl, "session.offer", []byte("secret-payload"))
	if err := carrier.Write(context.Background(), Endpoint{ID: "vk", Address: "2000000001"}, envelope); err != nil {
		t.Fatal(err)
	}

	// The social provider must see only VKCipher-style words, not a transport
	// prefix or any part of the fabric payload.
	requireWordCiphertext(t, sentMessage, "secret-payload")

	// Decode with the same cipher should recover the original envelope.
	decoded, err := decodeSocialMessageEnvelope(sentMessage, cipher)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ID != envelope.ID || string(decoded.Payload) != "secret-payload" {
		t.Fatalf("bad decrypted envelope: %+v", decoded)
	}
	read, err := carrier.Read(context.Background(), Endpoint{ID: "vk", Address: "2000000001"}, Cursor("0"))
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Envelopes) != 1 || read.Envelopes[0].ID != envelope.ID || string(read.Envelopes[0].Payload) != "secret-payload" {
		t.Fatalf("VK provider roundtrip lost encrypted envelope: %+v", read)
	}

	// Decode without cipher should fail.
	if _, err := decodeSocialMessageEnvelope(sentMessage); err == nil {
		t.Fatal("expected error decoding encrypted message without cipher")
	}
}

func TestVKMessagesCarrierFragmentsEncryptedEnvelope(t *testing.T) {
	key := fabric.DeriveBootstrapKey("vk-word-fragment-session")
	cipher, err := fabric.NewSessionCipher(key)
	if err != nil {
		t.Fatal(err)
	}

	var sentMessages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/messages.getHistory" {
			items := make([]map[string]any, 0, len(sentMessages))
			for index := len(sentMessages) - 1; index >= 0; index-- {
				items = append(items, map[string]any{"id": index + 1, "text": sentMessages[index]})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"items": items}})
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		sentMessages = append(sentMessages, r.PostForm.Get("message"))
		_, _ = w.Write([]byte(`{"response":123}`))
	}))
	defer server.Close()

	carrier, err := NewVKMessagesCarrier(VKMessagesConfig{Token: "token", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	carrier.SetCipher(cipher)
	envelope := fabric.NewEnvelope("vk-fragmented", fabric.TrafficControl, "session.offer", bytes.Repeat([]byte("fragment-payload-"), 160))

	if err := carrier.Write(context.Background(), Endpoint{ID: "vk", Address: "2000000001"}, envelope); err != nil {
		t.Fatal(err)
	}
	if len(sentMessages) < 2 {
		t.Fatalf("expected a multi-message word envelope, got %d provider messages", len(sentMessages))
	}
	for _, message := range sentMessages {
		requireWordCiphertext(t, message, "fragment-payload-")
	}

	read, err := carrier.Read(context.Background(), Endpoint{ID: "vk", Address: "2000000001"}, Cursor("0"))
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Envelopes) != 1 || read.Envelopes[0].ID != envelope.ID || !bytes.Equal(read.Envelopes[0].Payload, envelope.Payload) {
		t.Fatalf("VK fragmented provider roundtrip lost encrypted envelope: %+v", read)
	}
}
