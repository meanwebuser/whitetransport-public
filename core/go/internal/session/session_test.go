package session

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

var _ = time.Now // keep time import for Answer.ExpiresAt in test below

func TestBootstrapAdvertisementAndOfferOverMemoryCarrier(t *testing.T) {
	carrier := carriers.NewMemoryCarrier("memory-bootstrap")
	endpoint := carriers.Endpoint{ID: "bootstrap", Carrier: "memory-bootstrap", Address: "memory://bootstrap"}
	engine := NewEngine("example-exit-node")

	ad := NodeAdvertisement{
		NodeID:       "example-exit-node",
		Role:         RoleNode,
		Capabilities: []string{"egress"},
		Carriers:     []carriers.Endpoint{endpoint},
	}
	payload, err := EncodePayload(ad)
	if err != nil {
		t.Fatal(err)
	}
	envelope := fabric.NewEnvelope("ad-1", fabric.TrafficBootstrap, "node.advertise", payload)
	if err := carrier.Write(context.Background(), endpoint, envelope); err != nil {
		t.Fatal(err)
	}

	read, err := carrier.Read(context.Background(), endpoint, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Envelopes) != 1 {
		t.Fatalf("expected one envelope, got %d", len(read.Envelopes))
	}
	decoded, err := DecodePayload[NodeAdvertisement](read.Envelopes[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.NodeID != "example-exit-node" || decoded.Role != RoleNode {
		t.Fatalf("bad advertisement: %+v", decoded)
	}

	if err := engine.PublishAdvertisement(context.Background(), carrier, endpoint, ad); err != nil {
		t.Fatal(err)
	}
	ads, _, err := engine.ReadAdvertisements(context.Background(), carrier, endpoint, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ads) != 2 {
		t.Fatalf("expected two readable advertisements, got %d", len(ads))
	}
}

func TestOversizedEncryptedSessionAnswerFragmentsOverVKMessages(t *testing.T) {
	var messages []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		switch request.URL.Path {
		case "/messages.send":
			messages = append(messages, request.PostForm.Get("message"))
			_, _ = writer.Write([]byte(`{"response":1}`))
		case "/messages.getHistory":
			items := make([]map[string]any, 0, len(messages))
			for index := len(messages) - 1; index >= 0; index-- {
				items = append(items, map[string]any{"id": index + 1, "text": messages[index]})
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{"response": map[string]any{"items": items}})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	key := fabric.DeriveBootstrapKey("fragmented-session-answer")
	cipher, err := fabric.NewSessionCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	carrier, err := carriers.NewVKMessagesCarrier(carriers.VKMessagesConfig{Token: "token", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	carrier.SetCipher(cipher)
	endpoint := carriers.Endpoint{ID: "vk", Carrier: carriers.CarrierVKMessages, Address: "2000000001"}
	answer := Answer{SessionID: "oversized-answer", NodeID: "node", ExpiresAt: time.Now().Add(time.Minute)}
	answer.EgressProfilesCiphertext = make([]byte, 7000)
	if _, err := rand.Read(answer.EgressProfilesCiphertext); err != nil {
		t.Fatal(err)
	}

	node := NewEngine("node")
	client := NewEngine("client")
	if err := node.SendAnswer(context.Background(), carrier, endpoint, answer); err != nil {
		t.Fatalf("SendAnswer: %v", err)
	}
	if len(messages) < 2 {
		t.Fatalf("message count = %d, want fragmented answer", len(messages))
	}
	for index, message := range messages {
		if len(message) > 4096 {
			t.Fatalf("message %d has %d bytes, exceeds VK limit", index, len(message))
		}
	}
	answers, _, err := client.ReadAnswers(context.Background(), carrier, endpoint, "")
	if err != nil {
		t.Fatalf("ReadAnswers: %v", err)
	}
	if len(answers) != 1 || string(answers[0].EgressProfilesCiphertext) != string(answer.EgressProfilesCiphertext) {
		t.Fatalf("reassembled answers = %+v, want original encrypted profile", answers)
	}
}

func TestSessionAnswerFragmentsReassembleOutOfOrderAndIgnoreDuplicate(t *testing.T) {
	answer := Answer{SessionID: "fragment-session", NodeID: "node", EgressProfilesCiphertext: make([]byte, 1800)}
	if _, err := rand.Read(answer.EgressProfilesCiphertext); err != nil {
		t.Fatal(err)
	}
	payload, err := EncodePayload(answer)
	if err != nil {
		t.Fatal(err)
	}
	split := len(payload) / 2
	chunks := []fabric.Envelope{
		{Version: fabric.CurrentVersion, ID: "fragment-session:answer.1", SessionID: answer.SessionID, Source: "node", TrafficClass: fabric.TrafficControl, PayloadType: PayloadSessionAnswerChunk, ChunkIndex: 1, ChunkTotal: 2, CreatedAt: time.Now().UTC(), Payload: payload[split:]},
		{Version: fabric.CurrentVersion, ID: "fragment-session:answer.0", SessionID: answer.SessionID, Source: "node", TrafficClass: fabric.TrafficControl, PayloadType: PayloadSessionAnswerChunk, ChunkIndex: 0, ChunkTotal: 2, CreatedAt: time.Now().UTC(), Payload: payload[:split]},
	}
	client := NewEngine("client")
	if _, handled, err := client.DecodeAnswerEnvelope(chunks[0]); err != nil || handled {
		t.Fatalf("first out-of-order chunk handled=%v err=%v, want incomplete", handled, err)
	}
	if _, handled, err := client.DecodeAnswerEnvelope(chunks[0]); err != nil || handled {
		t.Fatalf("duplicate chunk handled=%v err=%v, want ignored", handled, err)
	}
	decoded, handled, err := client.DecodeAnswerEnvelope(chunks[1])
	if err != nil || !handled {
		t.Fatalf("final chunk handled=%v err=%v", handled, err)
	}
	if string(decoded.EgressProfilesCiphertext) != string(answer.EgressProfilesCiphertext) {
		t.Fatal("out-of-order fragments did not reconstruct the original answer")
	}
}

func TestSessionOfferAnswerOverSelectedMailbox(t *testing.T) {
	carrier := carriers.NewMemoryCarrier("memory-control")
	endpoint := carriers.Endpoint{ID: "session-mailbox", Carrier: "memory-control", Address: "memory://session"}
	client := NewEngine("android-client")
	node := NewEngine("example-exit-node")

	offer := Offer{
		SessionID: "session-1",
		ClientID:  "android-client",
		Wanted:    []string{"egress.socks5"},
		UsableCarriers: []carriers.Descriptor{
			carrier.Descriptor(),
		},
	}
	if err := client.SendOffer(context.Background(), carrier, endpoint, offer); err != nil {
		t.Fatal(err)
	}
	answer := Answer{
		SessionID: "session-1",
		NodeID:    "example-exit-node",
		Endpoints: []carriers.Endpoint{
			{ID: "wbstream-1", Carrier: "wbstream-vp8", Address: "wbstream://room"},
			{ID: "fallback-1", Carrier: "yandex-disk", Address: "yadisk://folder"},
		},
		ExpiresAt: time.Now().Add(time.Minute),
	}
	if err := node.SendAnswer(context.Background(), carrier, endpoint, answer); err != nil {
		t.Fatal(err)
	}

	read, err := carrier.Read(context.Background(), endpoint, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Envelopes) != 2 {
		t.Fatalf("expected offer and answer, got %d", len(read.Envelopes))
	}
	if read.Envelopes[0].PayloadType != "session.offer" || read.Envelopes[1].PayloadType != "session.answer" {
		t.Fatalf("unexpected payload order: %s %s", read.Envelopes[0].PayloadType, read.Envelopes[1].PayloadType)
	}
}

// TestLargeSessionAnswerUsesCompressedControlPayload ensures that a node can
// return all auto-discovered egress routes over a constrained control carrier
// instead of silently dropping profiles or exceeding the VK message limit.
func TestLargeSessionAnswerUsesCompressedControlPayload(t *testing.T) {
	carrier := carriers.NewMemoryCarrier("memory-compressed-answer")
	endpoint := carriers.Endpoint{ID: "session-mailbox", Carrier: "memory-compressed-answer", Address: "memory://compressed"}
	node := NewEngine("example-exit-node")
	client := NewEngine("mac-client")
	answer := Answer{SessionID: "compressed-session", NodeID: "example-exit-node", ExpiresAt: time.Now().Add(time.Minute)}
	for _, id := range []string{"cdn", "cdn2", "direct-ws", "grpc", "httpupgrade", "reality", "xhttp", "xhttp-h2"} {
		answer.EgressEndpoints = append(answer.EgressEndpoints, carriers.Endpoint{ID: "xray-de-" + id, Carrier: "xray-de-" + id, Address: "exit-node.example.invalid:443", Metadata: map[string]string{"auto_discovered": "xray", "xray_tag": id, "xray_network": "httpupgrade"}})
	}
	answer.EgressProfilesCiphertext = make([]byte, 900)
	if err := node.SendAnswer(context.Background(), carrier, endpoint, answer); err != nil {
		t.Fatalf("SendAnswer: %v", err)
	}
	read, err := carrier.Read(context.Background(), endpoint, "")
	if err != nil {
		t.Fatalf("read answer envelope: %v", err)
	}
	if len(read.Envelopes) != 1 || read.Envelopes[0].PayloadType != PayloadSessionAnswerCompressed {
		t.Fatalf("payload type = %q, want compressed session answer", read.Envelopes[0].PayloadType)
	}
	answers, _, err := client.ReadAnswers(context.Background(), carrier, endpoint, "")
	if err != nil {
		t.Fatalf("ReadAnswers: %v", err)
	}
	if len(answers) != 1 || len(answers[0].EgressEndpoints) != len(answer.EgressEndpoints) || len(answers[0].EgressProfilesCiphertext) != len(answer.EgressProfilesCiphertext) {
		t.Fatalf("decoded answer lost egress data: %+v", answers)
	}
}
