package runtime

import (
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/config"
)

func TestConfiguredCarrierOriginUsesLoopbackForFileMailbox(t *testing.T) {
	carrier := config.CarrierConfig{
		ID:       "file.mailbox",
		Endpoint: config.EndpointConfig{ID: "control", Address: "control"},
		FileMailbox: &config.FileMailboxConfig{
			Dir:         t.TempDir(),
			AllowEgress: true,
		},
	}

	scheme, host, port, origin, err := configuredCarrierOrigin(carrier)
	if err != nil {
		t.Fatalf("configuredCarrierOrigin(file.mailbox): %v", err)
	}
	if scheme != "http" || host != "127.0.0.1" || port != 80 || origin != "http://127.0.0.1" {
		t.Fatalf("file.mailbox origin = (%q, %q, %d, %q), want loopback HTTP", scheme, host, port, origin)
	}
}
