package tunnel

import (
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

func TestEgressFailureIsBoundToActiveStreamAndSession(t *testing.T) {
	const sessionID, streamID = "fixture-session", "fixture-stream"
	node := NewCarrierTunnel("node", nil)
	client := NewCarrierTunnel("client", nil)
	cipher, err := fabric.NewSessionCipher(fabric.DeriveBootstrapSecretKey("fixture"))
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Minute)
	node.SetPacketSession(sessionID, "client", expires)
	node.SetSessionCipher(sessionID, cipher)
	client.SetPacketSession(sessionID, "node", expires)
	client.SetSessionCipher(sessionID, cipher)
	node.SetEgressFailureReply(sessionID, carriers.NewMemoryCarrier(carriers.CarrierVKMessages), carriers.Endpoint{Address: "memory://control"})
	notice, reply := node.prepareEgressFailure(t.Context(), sessionID, streamID, "client")
	if reply.Carrier == nil {
		t.Fatal("node did not prepare an encrypted error notice")
	}
	var notifications, unrelated int
	client.watchEgressFailure(sessionID, streamID, func() { notifications++ })
	client.watchEgressFailure(sessionID, "other-stream", func() { unrelated++ })
	modified := notice
	modified.ID = "other-stream"
	client.HandleEgressStreamFailure(modified)
	modified = notice
	modified.Payload = nil
	client.HandleEgressStreamFailure(modified)
	if notifications != 0 || unrelated != 0 {
		t.Fatal("modified notice affected a live stream")
	}
	client.HandleEgressStreamFailure(notice)
	client.HandleEgressStreamFailure(notice)
	if notifications != 1 || unrelated != 0 {
		t.Fatalf("valid notices=%d, other streams affected=%d", notifications, unrelated)
	}
	client.watchEgressFailure(sessionID, streamID, func() { notifications++ })
	client.ClosePacketSession(sessionID)
	client.HandleEgressStreamFailure(notice)
	if notifications != 1 {
		t.Fatal("notice affected a closed session")
	}
}
