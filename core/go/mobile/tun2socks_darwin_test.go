//go:build darwin

package mobile

import "testing"

func TestDarwinUsesProductionTun2SocksBinding(t *testing.T) {
	if !tun2SocksProductionBinding {
		t.Fatal("Darwin selected the tun2socks stub instead of the production engine")
	}
}
