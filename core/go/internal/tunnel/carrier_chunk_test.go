package tunnel

import (
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
)

func TestEgressWriteChunkLimitLeavesMailboxEncodingHeadroom(t *testing.T) {
	carrier, err := carriers.NewVKMessagesCarrier(carriers.VKMessagesConfig{Token: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := egressWriteChunkLimit(carrier), 2048; got != want {
		t.Fatalf("VK mailbox chunk limit = %d, want %d", got, want)
	}
}
