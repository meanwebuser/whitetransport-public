package punch

import (
	"net"
	"testing"
	"time"
)

// fakeSTUNServer answers binding requests with the client's own source
// address as the XOR-MAPPED-ADDRESS.
func fakeSTUNServer(t *testing.T) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("fake stun listen: %v", err)
	}
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n < 20 {
				continue
			}
			mapped := from
			ip4 := mapped.IP.To4()

			attr := []byte{0x00, 0x01} // family IPv4
			xport := uint16(mapped.Port) ^ uint16(stunMagicCookie>>16)
			attr = append(attr, byte(xport>>8), byte(xport))
			for i := 0; i < 4; i++ {
				attr = append(attr, ip4[i]^byte(stunMagicCookie>>(8*(3-i))))
			}

			resp := []byte{0x01, 0x01, 0x00, byte(4 + len(attr))}
			resp = append(resp, buf[4:8]...)  // cookie
			resp = append(resp, buf[8:20]...) // transaction
			resp = append(resp, 0x00, 0x20, 0x00, byte(len(attr)))
			resp = append(resp, attr...)
			if _, err := conn.WriteToUDP(resp, from); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestSTUNQueryReturnsMappedLoopback(t *testing.T) {
	server := fakeSTUNServer(t)
	client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("client listen: %v", err)
	}
	defer client.Close()

	addr, err := stunQuery(client, server.LocalAddr().String(), time.Second)
	if err != nil {
		t.Fatalf("stunQuery: %v", err)
	}
	local := client.LocalAddr().(*net.UDPAddr)
	if !addr.IP.Equal(local.IP) || addr.Port != local.Port {
		t.Fatalf("mapped %v != local %v", addr, local)
	}
}

func TestSTUNParseMappedRejectsForeignTransaction(t *testing.T) {
	_, txn, err := stunBindingRequest()
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp := []byte{
		0x01, 0x01, 0x00, 0x00, // type, len
		0x21, 0x12, 0xA4, 0x42, // cookie
	}
	resp = append(resp, make([]byte, 12)...) // wrong (zero) txn
	if _, err := stunParseMapped(resp, txn); err == nil {
		t.Fatal("expected transaction mismatch error")
	}
}

func TestSTUNParseMappedFallbackPlainAddress(t *testing.T) {
	_, txn, _ := stunBindingRequest()
	// MAPPED-ADDRESS (0x0001), family 1, 127.0.0.1:4711
	val := []byte{0x00, 0x01, 0x12, 0x67, 127, 0, 0, 1}
	resp := []byte{0x01, 0x01, 0x00, byte(4 + len(val))}
	resp = append(resp, 0x21, 0x12, 0xA4, 0x42)
	resp = append(resp, txn[:]...)
	resp = append(resp, 0x00, 0x01, 0x00, byte(len(val)))
	resp = append(resp, val...)
	addr, err := stunParseMapped(resp, txn)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if addr.Port != 4711 || !addr.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("unexpected addr %v", addr)
	}
}

func TestSTUNAttributeOverrunRejected(t *testing.T) {
	_, txn, _ := stunBindingRequest()
	resp := []byte{0x01, 0x01, 0x00, 0x08} // claims 8 body bytes...
	resp = append(resp, 0x21, 0x12, 0xA4, 0x42)
	resp = append(resp, txn[:]...)
	resp = append(resp, 0x00, 0x20, 0x00, 0x08) // ...but only 4 present
	resp = append(resp, 0xAA, 0xBB, 0xCC, 0xDD)
	if _, err := stunParseMapped(resp, txn); err == nil {
		t.Fatal("expected attribute overrun error")
	}
}

func TestHostCandidatesIncludeLoopbackWithPort(t *testing.T) {
	candidates := hostCandidates(4711)
	found := false
	for _, c := range candidates {
		if c.Typ != "host" {
			t.Fatalf("unexpected type %q", c.Typ)
		}
		addr, err := net.ResolveUDPAddr("udp4", c.Addr)
		if err != nil {
			t.Fatalf("bad candidate addr %q: %v", c.Addr, err)
		}
		if addr.Port != 4711 {
			t.Fatalf("candidate port %d != 4711", addr.Port)
		}
		if addr.IP.IsLoopback() {
			found = true
		}
	}
	if !found {
		t.Fatal("loopback candidate missing")
	}
}

func TestDefaultServersMatchVerifiedList(t *testing.T) {
	got := DefaultServers()
	if len(got) != 2 || got[0] != "stun.sipnet.ru:3478" || got[1] != "stun.cloudflare.com:3478" {
		t.Fatalf("unexpected default servers %v", got)
	}
}
