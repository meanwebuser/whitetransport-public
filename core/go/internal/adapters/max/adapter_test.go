package max

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/meanwebuser/whitetransport/core/internal/provider"
	"github.com/pierrec/lz4/v4"
)

func TestProviderConfigureFailsClosedWithoutRoleScopedCredentials(t *testing.T) {
	p := &Provider{}
	if err := p.Configure(provider.ProviderConfig{Settings: map[string]any{"role": "client", "peer_id": "42"}}); err == nil || !strings.Contains(err.Error(), "max_token") {
		t.Fatalf("client without token error = %v, want max_token requirement", err)
	}
	if err := p.Configure(provider.ProviderConfig{Credentials: map[string]string{"max_token": "test-token"}, Settings: map[string]any{"role": "client"}}); err == nil || !strings.Contains(err.Error(), "peer_id") {
		t.Fatalf("client without peer ID error = %v, want peer_id requirement", err)
	}
	if err := p.Configure(provider.ProviderConfig{Credentials: map[string]string{"max_token": "test-token"}, Settings: map[string]any{"role": "node"}}); err != nil {
		t.Fatalf("node config unexpectedly rejected: %v", err)
	}
}

func TestClientRoleMovesOpaquePayloadThroughMAXSignaling(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	seen := make(chan []byte, 1)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rpc":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrade rpc: %v", err)
				return
			}
			defer conn.Close()
			for {
				var request rpcPacket
				if err := conn.ReadJSON(&request); err != nil {
					return
				}
				payload := any(map[string]any{})
				if request.Opcode == maxStartCallOpcode {
					params, _ := json.Marshal(map[string]any{"id": map[string]any{"internal": 42}, "endpoint": wsURL(server.URL) + "/signal"})
					payload = map[string]any{"internalCallerParams": string(params)}
				}
				writeRPCFixture(t, conn, request, payload)
			}
		case "/signal":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrade signal: %v", err)
				return
			}
			defer conn.Close()
			var frame struct {
				Command string `json:"command"`
				Data    struct {
					Candidate struct {
						Candidate string `json:"candidate"`
					} `json:"candidate"`
				} `json:"data"`
			}
			if err := conn.ReadJSON(&frame); err != nil {
				t.Errorf("read client signal: %v", err)
				return
			}
			if frame.Command != "transmit-data" {
				t.Errorf("signal command = %q", frame.Command)
				return
			}
			payload, err := base64.StdEncoding.DecodeString(frame.Data.Candidate.Candidate)
			if err != nil {
				t.Errorf("decode candidate payload: %v", err)
				return
			}
			seen <- payload
			_ = conn.WriteJSON(map[string]any{
				"conversation": map[string]any{"participants": []map[string]any{{"id": 42, "roles": []string{"MEMBER"}}}},
				"data":         map[string]any{"candidate": map[string]string{"candidate": base64.StdEncoding.EncodeToString([]byte("node-reply"))}},
			})
		}
	}))
	defer server.Close()

	p := &Provider{deadline: time.Second}
	if err := p.Configure(provider.ProviderConfig{
		Credentials: map[string]string{"max_token": "test-token"},
		Endpoints:   map[string]string{"websocket_url": wsURL(server.URL) + "/rpc"},
		Settings:    map[string]any{"role": "client", "peer_id": "99", "allow_insecure_test_endpoint": true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.Load(); err != nil {
		t.Fatal(err)
	}
	defer p.Unload()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.Send(ctx, []byte("opaque-provider-wire")); err != nil {
		t.Fatal(err)
	}
	select {
	case payload := <-seen:
		if string(payload) != "opaque-provider-wire" {
			t.Fatalf("provider-visible payload = %q", payload)
		}
	case <-ctx.Done():
		t.Fatal("MAX signal server did not receive payload")
	}
	reply, err := p.Receive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(reply) != "node-reply" {
		t.Fatalf("MAX reply = %q", reply)
	}
}

func TestNodeRoleAcceptsInviteAndPreservesEarlyPayload(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	received := make(chan []byte, 1)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rpc":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrade rpc: %v", err)
				return
			}
			defer conn.Close()
			for {
				var request rpcPacket
				if err := conn.ReadJSON(&request); err != nil {
					return
				}
				writeRPCFixture(t, conn, request, map[string]any{})
				if request.Opcode == maxLoginOpcode {
					invite := makeInviteFixture(t, "node-conversation", wsURL(server.URL)+"/call/ws1")
					_ = conn.WriteJSON(rpcPacket{Version: maxRPCVersion, Opcode: maxIncomingOpcode, Payload: invite})
				}
			}
		case "/call/ws2":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrade node signal: %v", err)
				return
			}
			defer conn.Close()
			var accepted map[string]any
			if err := conn.ReadJSON(&accepted); err != nil {
				t.Errorf("read accept-call: %v", err)
				return
			}
			if accepted["command"] != "accept-call" {
				t.Errorf("node command = %v, want accept-call", accepted["command"])
				return
			}
			_ = conn.WriteJSON(map[string]any{"data": map[string]any{"candidate": map[string]string{"candidate": base64.StdEncoding.EncodeToString([]byte("client-early"))}}})
			var outbound struct {
				Data struct {
					Candidate struct {
						Candidate string `json:"candidate"`
					} `json:"candidate"`
				} `json:"data"`
			}
			if err := conn.ReadJSON(&outbound); err != nil {
				t.Errorf("read node payload: %v", err)
				return
			}
			payload, err := base64.StdEncoding.DecodeString(outbound.Data.Candidate.Candidate)
			if err != nil {
				t.Errorf("decode node payload: %v", err)
				return
			}
			received <- payload
		}
	}))
	defer server.Close()

	p := &Provider{deadline: time.Second}
	if err := p.Configure(provider.ProviderConfig{
		Credentials: map[string]string{"max_token": "node-token"},
		Endpoints:   map[string]string{"websocket_url": wsURL(server.URL) + "/rpc"},
		Settings:    map[string]any{"role": "node", "allow_insecure_test_endpoint": true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.Load(); err != nil {
		t.Fatal(err)
	}
	defer p.Unload()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	inbound, err := p.Receive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(inbound) != "client-early" {
		t.Fatalf("node early payload = %q", inbound)
	}
	if err := p.Send(ctx, []byte("node-response")); err != nil {
		t.Fatal(err)
	}
	select {
	case payload := <-received:
		if string(payload) != "node-response" {
			t.Fatalf("node provider payload = %q", payload)
		}
	case <-ctx.Done():
		t.Fatal("node signal server did not receive response")
	}
}

func writeRPCFixture(t *testing.T, conn *websocket.Conn, request rpcPacket, payload any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal fixture payload: %v", err)
	}
	if err := conn.WriteJSON(rpcPacket{Version: maxRPCVersion, Command: 0, Sequence: request.Sequence, Opcode: request.Opcode, Payload: raw}); err != nil {
		t.Errorf("write RPC response: %v", err)
	}
}

func makeInviteFixture(t *testing.T, conversationID, endpoint string) json.RawMessage {
	t.Helper()
	details, err := json.Marshal(map[string]string{"tkn": "call-token", "wse": endpoint, "trnu": "scope:77"})
	if err != nil {
		t.Fatal(err)
	}
	packed := make([]byte, lz4.CompressBlockBound(len(details)))
	n, err := lz4.CompressBlock(details, packed, nil)
	if err != nil || n == 0 {
		t.Fatalf("compress invite: n=%d err=%v", n, err)
	}
	payload, err := json.Marshal(map[string]string{"conversationId": conversationID, "vcp": fmt.Sprintf("%03d:%s", len(details), base64.StdEncoding.EncodeToString(packed[:n]))})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func wsURL(httpURL string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http")
}
