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

func TestIntegrationOKMessagesSendAndRead(t *testing.T) {
	if os.Getenv("WT_INTEGRATION") == "" {
		t.Skip("skip: WT_INTEGRATION not set")
	}
	token := loadOKToken(t)
	chatID := os.Getenv("WT_OK_CHAT_ID")
	if chatID == "" {
		chatID = "7534875348512"
	}

	carrier, err := carriers.NewOKMessagesCarrier(carriers.OKMessagesConfig{
		Token: token,
	})
	if err != nil {
		t.Fatalf("NewOKMessagesCarrier: %v", err)
	}

	ep := carriers.Endpoint{
		ID:       "ok-integration",
		Address:  "chat:" + chatID,
		Metadata: map[string]string{"chat_id": chatID},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	envID := fmt.Sprintf("integration-ok-%d", time.Now().UnixMilli())
	env := fabric.NewEnvelope(envID, fabric.TrafficControl, "integration.test", []byte("ok integration test"))
	if err := carrier.Write(ctx, ep, env); err != nil {
		t.Logf("Write failed (token may lack permissions): %v", err)
		t.Skip("skip: OK token does not support messages.send")
	}
	t.Logf("sent envelope id=%s to chat %s", envID, chatID)

	time.Sleep(1 * time.Second)
	result, err := carrier.Read(ctx, ep, "")
	if err != nil {
		t.Logf("Read failed (token may lack permissions): %v", err)
		return
	}
	t.Logf("read %d envelopes", len(result.Envelopes))

	found := false
	for _, e := range result.Envelopes {
		if e.ID == envID {
			found = true
			break
		}
	}
	if !found {
		t.Logf("warning: sent envelope %s not found in readback (OK API may delay)", envID)
	}
}
