package runtime

import (
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/config"
)

func TestNodeStatusReportsActiveSession(t *testing.T) {
	control := &ControlPlane{
		cfg:           config.Config{Role: config.RoleNode, NodeID: "node-1"},
		state:         "running",
		nodeBusy:      true,
		nodeSessionID: "node-session-1",
	}

	status := control.Status()

	if !status.SessionActive {
		t.Fatal("session_active = false for busy node")
	}
	if status.SessionID != "node-session-1" {
		t.Fatalf("node session_id = %q, want %q", status.SessionID, "node-session-1")
	}
}
