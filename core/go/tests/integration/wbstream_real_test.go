//go:build integration

package tests

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"whitelist-bypass/relay/tunnel"
	"whitelist-bypass/relay/wbstream"
)

// TestRealWB_DataTunnelEcho creates two WBStream sessions (node=creator,
// client=joiner) using real WBStream API credentials and verifies
// bidirectional data flow through the DataTunnel DataChannel.
//
// This test exercises the REAL WBStack:
//   - Real WBStream room creation and joining (wbstream API)
//   - Real LiveKit WebRTC signaling and ICE connectivity
//   - Real DCTunnel frame encoding/decoding (tunnel.EncodeFrame/DecodeFrames)
//   - Real DataChannel message delivery via SetOnData callback
//
// Requires: WB_NODE_ACCESS_TOKEN and WB_CLIENT_ACCESS_TOKEN env vars
// (source from secrets/production/load-env.sh).
func TestRealWB_DataTunnelEcho(t *testing.T) {
	nodeToken := os.Getenv("WB_NODE_ACCESS_TOKEN")
	clientToken := os.Getenv("WB_CLIENT_ACCESS_TOKEN")
	if nodeToken == "" || clientToken == "" {
		t.Skip("skip: WB_NODE_ACCESS_TOKEN and WB_CLIENT_ACCESS_TOKEN required (source secrets/production/load-env.sh)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// ── Node: create room ──────────────────────────────────────────
	t.Log("node: creating WBStream room...")
	roomID, err := wbstream.CreateRoom(nil, nodeToken)
	if err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}
	t.Logf("node: room created: %s", roomID)

	if err := wbstream.JoinRoom(nil, nodeToken, roomID); err != nil {
		t.Fatalf("node JoinRoom: %v", err)
	}
	nodeRoomToken, nodeServerURL, err := wbstream.GetConnectionDetails(nil, nodeToken, roomID, "NodeTest")
	if err != nil {
		t.Fatalf("node GetConnectionDetails: %v", err)
	}
	t.Logf("node: serverURL=%s", nodeServerURL)

	// ── Client: join room ──────────────────────────────────────────
	t.Log("client: joining WBStream room...")
	if err := wbstream.JoinRoom(nil, clientToken, roomID); err != nil {
		t.Fatalf("client JoinRoom: %v", err)
	}
	clientRoomToken, clientServerURL, err := wbstream.GetConnectionDetails(nil, clientToken, roomID, "ClientTest")
	if err != nil {
		t.Fatalf("client GetConnectionDetails: %v", err)
	}
	t.Logf("client: serverURL=%s", clientServerURL)

	// ── Node session ───────────────────────────────────────────────
	nodeConnected := make(chan tunnel.DataTunnel, 1)
	nodeSess := wbstream.NewSession(wbstream.SessionConfig{
		ServerURL:   nodeServerURL,
		RoomID:      roomID,
		RoomToken:   nodeRoomToken,
		DisplayName: "NodeTest",
		TunnelMode:  "dc",
		LogFn:       func(f string, a ...any) { t.Logf("[node] "+f, a...) },
	})
	nodeSess.OnConnected = func(dt tunnel.DataTunnel) {
		t.Logf("[node] OnConnected fired: %T", dt)
		nodeConnected <- dt
	}
	if err := nodeSess.Start(); err != nil {
		t.Fatalf("node Session.Start: %v", err)
	}
	defer nodeSess.Close()

	// ── Client session ─────────────────────────────────────────────
	clientConnected := make(chan tunnel.DataTunnel, 1)
	clientSess := wbstream.NewSession(wbstream.SessionConfig{
		ServerURL:   clientServerURL,
		RoomID:      roomID,
		RoomToken:   clientRoomToken,
		DisplayName: "ClientTest",
		TunnelMode:  "dc",
		IsJoiner:    true,
		LogFn:       func(f string, a ...any) { t.Logf("[client] "+f, a...) },
	})
	clientSess.OnConnected = func(dt tunnel.DataTunnel) {
		t.Logf("[client] OnConnected fired: %T", dt)
		clientConnected <- dt
	}
	if err := clientSess.Start(); err != nil {
		t.Fatalf("client Session.Start: %v", err)
	}
	defer clientSess.Close()

	// ── Wait for both tunnels ──────────────────────────────────────
	t.Log("waiting for both tunnels to connect...")
	var nodeDT, clientDT tunnel.DataTunnel
	timeout := time.After(30 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case nodeDT = <-nodeConnected:
			t.Log("node tunnel connected")
		case clientDT = <-clientConnected:
			t.Log("client tunnel connected")
		case <-timeout:
			t.Fatalf("tunnel connect timeout (node=%v client=%v)", nodeDT != nil, clientDT != nil)
		case <-ctx.Done():
			t.Fatal("context cancelled")
		}
	}

	// ── Wire SetOnData: node echoes client messages back ───────────
	var wg sync.WaitGroup
	wg.Add(1)
	received := make(chan []byte, 1)

	nodeDT.SetOnData(func(data []byte) {
		t.Logf("[node] SetOnData received %d bytes — echoing back", len(data))
		nodeDT.SendData(data)
	})

	clientDT.SetOnData(func(data []byte) {
		t.Logf("[client] SetOnData received %d bytes", len(data))
		cp := make([]byte, len(data))
		copy(cp, data)
		select {
		case received <- cp:
		default:
		}
		wg.Done()
	})

	// ── Send encoded frame from client, expect echo ────────────────
	testPayload := []byte("hello-wbstream-echo")
	encodedFrame := tunnel.EncodeFrame(1, tunnel.MsgData, testPayload)
	t.Logf("client sending encoded frame (%d bytes, payload=%q)", len(encodedFrame), testPayload)
	clientDT.SendData(encodedFrame)

	select {
	case echo := <-received:
		t.Logf("received echo: %d bytes", len(echo))
		tunnel.DecodeFrames(echo, func(connID uint32, msgType byte, payload []byte) {
			if string(payload) != string(testPayload) {
				t.Fatalf("echo mismatch: got %q, want %q", string(payload), string(testPayload))
			}
			t.Logf("PASS: echo verified — connID=%d msgType=%d payload=%q", connID, msgType, string(payload))
		})
	case <-time.After(15 * time.Second):
		t.Fatal("echo timeout (15s) — SetOnData never fired on client side. DataChannel data flow is broken.")
	case <-ctx.Done():
		t.Fatal("context cancelled")
	}

	// Let WebRTC cleanup goroutines finish before test exits.
	time.Sleep(500 * time.Millisecond)
}

// TestRealWB_SetOnDataPersistsAfterOnConnected specifically tests that
// SetOnData registered AFTER OnConnected fires is the one that gets
// called when data arrives. This tests the exact bug we're chasing:
// the adapter's SetOnData vs the session's internal handler.
func TestRealWB_SetOnDataPersistsAfterOnConnected(t *testing.T) {
	nodeToken := os.Getenv("WB_NODE_ACCESS_TOKEN")
	clientToken := os.Getenv("WB_CLIENT_ACCESS_TOKEN")
	if nodeToken == "" || clientToken == "" {
		t.Skip("skip: WB_NODE_ACCESS_TOKEN and WB_CLIENT_ACCESS_TOKEN required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Create and join room
	roomID, err := wbstream.CreateRoom(nil, nodeToken)
	if err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}
	wbstream.JoinRoom(nil, nodeToken, roomID)
	nodeRT, nodeURL, _ := wbstream.GetConnectionDetails(nil, nodeToken, roomID, "Node")
	wbstream.JoinRoom(nil, clientToken, roomID)
	clientRT, clientURL, _ := wbstream.GetConnectionDetails(nil, clientToken, roomID, "Client")

	// Node session
	nodeDTCh := make(chan tunnel.DataTunnel, 1)
	nodeSess := wbstream.NewSession(wbstream.SessionConfig{
		ServerURL: nodeURL, RoomID: roomID, RoomToken: nodeRT,
		DisplayName: "Node", TunnelMode: "dc",
		LogFn: func(f string, a ...any) { t.Logf("[node] "+f, a...) },
	})
	nodeSess.OnConnected = func(dt tunnel.DataTunnel) { nodeDTCh <- dt }
	nodeSess.Start()
	defer nodeSess.Close()

	// Client session
	clientDTCh := make(chan tunnel.DataTunnel, 1)
	clientSess := wbstream.NewSession(wbstream.SessionConfig{
		ServerURL: clientURL, RoomID: roomID, RoomToken: clientRT,
		DisplayName: "Client", TunnelMode: "dc", IsJoiner: true,
		LogFn: func(f string, a ...any) { t.Logf("[client] "+f, a...) },
	})
	clientSess.OnConnected = func(dt tunnel.DataTunnel) { clientDTCh <- dt }
	clientSess.Start()
	defer clientSess.Close()

	// Wait for both
	var nodeDT, clientDT tunnel.DataTunnel
	timeout := time.After(30 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case nodeDT = <-nodeDTCh:
		case clientDT = <-clientDTCh:
		case <-timeout:
			t.Fatalf("timeout waiting for tunnels")
		}
	}

	// KEY TEST: Register SetOnData AFTER OnConnected already fired.
	// This simulates what the adapter does: OnConnected fires first,
	// then setupFrameHandler calls SetOnData.
	handlerFired := make(chan []byte, 1)

	// Register handler on node AFTER OnConnected
	nodeDT.SetOnData(func(data []byte) {
		t.Logf("[node] HANDLER FIRED: %d bytes", len(data))
		cp := make([]byte, len(data))
		copy(cp, data)
		handlerFired <- cp
	})

	// Small sleep to ensure handler is registered
	time.Sleep(200 * time.Millisecond)

	// Send from client
	frame := tunnel.EncodeFrame(42, tunnel.MsgConnect, []byte("target.example.com:80"))
	t.Logf("client sending MsgConnect frame (%d bytes)", len(frame))
	clientDT.SendData(frame)

	// Verify node's handler fires
	select {
	case data := <-handlerFired:
		t.Logf("PASS: node handler received %d bytes", len(data))
		tunnel.DecodeFrames(data, func(connID uint32, msgType byte, payload []byte) {
			t.Logf("  decoded: connID=%d msgType=%d payload=%q", connID, msgType, string(payload))
			if connID != 42 {
				t.Errorf("connID mismatch: got %d, want 42", connID)
			}
			if msgType != tunnel.MsgConnect {
				t.Errorf("msgType mismatch: got %d, want %d", msgType, tunnel.MsgConnect)
			}
			if string(payload) != "target.example.com:80" {
				t.Errorf("payload mismatch: got %q", string(payload))
			}
		})
	case <-time.After(15 * time.Second):
		t.Fatal("FAIL: node SetOnData handler never fired (15s timeout). " +
			"This means SetOnData registered AFTER OnConnected does NOT receive DC messages. " +
			"The session.go may be overwriting the handler.")
	case <-ctx.Done():
		t.Fatal("context cancelled")
	}

	// Let WebRTC cleanup goroutines finish before test exits.
	time.Sleep(500 * time.Millisecond)
}
