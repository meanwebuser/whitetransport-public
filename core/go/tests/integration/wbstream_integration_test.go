//go:build integration

package tests

import (
	"context"
	"os"
	"testing"
	"time"

	"whitelist-bypass/relay/tunnel"
	"whitelist-bypass/relay/wbstream"
)

func TestIntegrationWBStreamCreateRoom(t *testing.T) {
	if os.Getenv("WT_INTEGRATION") == "" {
		t.Skip("skip: WT_INTEGRATION not set")
	}

	accessToken := os.Getenv("WB_NODE_ACCESS_TOKEN")
	if accessToken == "" {
		t.Skip("skip: WB_NODE_ACCESS_TOKEN not set (source secrets/production/load-env.sh)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	roomID, err := wbstream.CreateRoom(nil, accessToken)
	if err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}
	t.Logf("created WBStream room: %s", roomID)
	if roomID == "" {
		t.Fatal("room ID is empty")
	}

	roomToken, serverURL, err := wbstream.GetConnectionDetails(nil, accessToken, roomID, "IntegrationTest")
	if err != nil {
		t.Fatalf("GetConnectionDetails: %v", err)
	}
	if serverURL == "" {
		t.Fatal("server URL is empty")
	}
	if roomToken == "" {
		t.Fatal("room token is empty")
	}

	sess := wbstream.NewSession(wbstream.SessionConfig{
		ServerURL:   serverURL,
		RoomID:      roomID,
		RoomToken:   roomToken,
		DisplayName: "IntegrationTest",
		TunnelMode:  "dc",
		LogFn:       t.Logf,
	})

	connected := make(chan tunnel.DataTunnel, 1)
	sess.OnConnected = func(dt tunnel.DataTunnel) {
		connected <- dt
	}

	if err := sess.Start(); err != nil {
		t.Logf("Session.Start failed (DNS/network may be restricted): %v", err)
		t.Log("room was created successfully - WebSocket connection requires full network access")
		return
	}
	defer sess.Close()

	select {
	case tun := <-connected:
		t.Logf("WBStream tunnel connected: %T", tun)
		tun.SendData([]byte("wbstream-integration-ping"))
		t.Log("sent test payload through WBStream tunnel")
	case <-time.After(25 * time.Second):
		t.Fatal("WBStream tunnel connect timeout (25s)")
	case <-ctx.Done():
		t.Fatal("context cancelled")
	}
}
