package runtime

import (
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/config"
)

func TestStatusReportsBoundSocksEndpoint(t *testing.T) {
	c := &ControlPlane{cfg: config.Config{Role: config.RoleClient, SocksListen: "127.0.0.1:0"}}
	if got := c.statusLocked().SocksListen; got != "" {
		t.Fatalf("unbound listener advertised as %q", got)
	}
	c.SetActualSocksAddr("127.0.0.1:43123")
	if got := c.statusLocked().SocksListen; got != "127.0.0.1:43123" {
		t.Fatalf("status endpoint = %q, want actual bound listener", got)
	}
	c.SetActualSocksAddr("")
	if got := c.statusLocked().SocksListen; got != "" {
		t.Fatalf("cleared listener advertised as %q", got)
	}
}
