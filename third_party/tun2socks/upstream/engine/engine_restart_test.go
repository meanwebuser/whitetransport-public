package engine

import (
	"testing"

	"github.com/xjasonlyu/tun2socks/v2/tunnel"
)

func TestStopReplacesClosedGlobalTunnelForNextStart(t *testing.T) {
	for cycle := 1; cycle <= 3; cycle++ {
		old := tunnel.T()
		if old == nil {
			t.Fatalf("global tunnel is nil before restart cycle %d", cycle)
		}
		old.Close()

		if err := Stop(); err != nil {
			t.Fatalf("stop closed engine in cycle %d: %v", cycle, err)
		}

		fresh := tunnel.T()
		if fresh == nil {
			t.Fatalf("global tunnel is nil after restart cycle %d", cycle)
		}
		if fresh == old {
			t.Fatalf("Stop retained the closed global tunnel in cycle %d; the next Start would block on its cancelled processor", cycle)
		}
	}
}
