//go:build integration

package tests

import (
	"net/http"
	"os"
	"testing"
	"time"

	"whitelist-bypass/relay/wbstream"
)

// TestRealWB_ClientCreatesNodeJoins proves the WBStream role assignment used
// by the native GUI: the client principal creates a room and the node
// principal joins it. Both principals must also receive connection details.
//
// Requires WT_INTEGRATION, WB_CLIENT_ACCESS_TOKEN, and WB_NODE_ACCESS_TOKEN.
func TestRealWB_ClientCreatesNodeJoins(t *testing.T) {
	if os.Getenv("WT_INTEGRATION") == "" {
		t.Skip("skip: WT_INTEGRATION not set")
	}

	clientToken := os.Getenv("WB_CLIENT_ACCESS_TOKEN")
	nodeToken := os.Getenv("WB_NODE_ACCESS_TOKEN")
	if clientToken == "" || nodeToken == "" {
		t.Skip("skip: WB_CLIENT_ACCESS_TOKEN and WB_NODE_ACCESS_TOKEN required")
	}

	httpClient := &http.Client{Timeout: 30 * time.Second}
	roomID, err := wbstream.CreateRoom(httpClient, clientToken)
	if err != nil {
		t.Fatalf("client principal CreateRoom: %v", err)
	}
	if roomID == "" {
		t.Fatal("client principal CreateRoom returned an empty room ID")
	}
	t.Cleanup(func() {
		if err := wbstream.DeleteRoom(httpClient, clientToken, roomID); err != nil {
			t.Errorf("client principal DeleteRoom cleanup: %v", err)
		}
	})

	if err := wbstream.JoinRoom(httpClient, clientToken, roomID); err != nil {
		t.Fatalf("client principal JoinRoom: %v", err)
	}
	clientRoomToken, clientServerURL, err := wbstream.GetConnectionDetails(httpClient, clientToken, roomID, "WhiteTransportClient")
	if err != nil {
		t.Fatalf("client principal GetConnectionDetails: %v", err)
	}
	if clientRoomToken == "" || clientServerURL == "" {
		t.Fatal("client principal received incomplete connection details")
	}

	if err := wbstream.JoinRoom(httpClient, nodeToken, roomID); err != nil {
		t.Fatalf("node principal JoinRoom: %v", err)
	}
	nodeRoomToken, nodeServerURL, err := wbstream.GetConnectionDetails(httpClient, nodeToken, roomID, "WhiteTransportNode")
	if err != nil {
		t.Fatalf("node principal GetConnectionDetails: %v", err)
	}
	if nodeRoomToken == "" || nodeServerURL == "" {
		t.Fatal("node principal received incomplete connection details")
	}

	t.Log("client-create/node-join WBStream role reversal verified")
}
