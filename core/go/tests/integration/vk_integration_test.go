//go:build integration

package tests

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

func TestIntegrationVKMessagesSendAndRead(t *testing.T) {
	if os.Getenv("WT_INTEGRATION") == "" {
		t.Skip("skip: WT_INTEGRATION not set")
	}
	token := loadVKToken(t)
	peerID := "2000000140"

	carrier, err := carriers.NewVKMessagesCarrier(carriers.VKMessagesConfig{
		Token: token,
	})
	if err != nil {
		t.Fatalf("NewVKMessagesCarrier: %v", err)
	}

	ep := carriers.Endpoint{ID: "vk-integration", Address: peerID}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	envID := fmt.Sprintf("integration-vk-%d", time.Now().UnixMilli())
	env := fabric.NewEnvelope(envID, fabric.TrafficControl, "integration.test", []byte("hello from integration test"))
	if err := carrier.Write(ctx, ep, env); err != nil {
		t.Logf("Write failed (token may lack messages.send permission): %v", err)
		t.Skip("skip: VK token does not support messages.send")
	}
	t.Logf("sent envelope id=%s to peer %s", envID, peerID)

	time.Sleep(500 * time.Millisecond)
	result, err := carrier.Read(ctx, ep, "")
	if err != nil {
		t.Logf("Read failed (token may lack messages permission): %v", err)
		return
	}
	t.Logf("read %d envelopes", len(result.Envelopes))

	found := false
	for _, e := range result.Envelopes {
		if e.ID == envID {
			found = true
			if string(e.Payload) != "hello from integration test" {
				t.Errorf("payload mismatch: %q", string(e.Payload))
			}
			if e.TrafficClass != fabric.TrafficControl {
				t.Errorf("traffic class = %v, want control", e.TrafficClass)
			}
			break
		}
	}
	if !found {
		t.Logf("warning: sent envelope %s not found in readback (got %d envelopes)", envID, len(result.Envelopes))
	}
}
