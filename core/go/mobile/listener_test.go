package mobile

import (
	"io"
	"net"
	"testing"
	"time"
)

func TestEmbeddedRuntimePublishesBoundSocksPort(t *testing.T) {
	StopTransport()
	t.Cleanup(StopTransport)
	if err := StartTransport(`{"role":"client","client_id":"dynamic-port-test","socks_listen":"127.0.0.1:0"}`); err != nil {
		t.Fatal(err)
	}
	address := GetSocksAddr()
	_, port, err := net.SplitHostPort(address)
	if err != nil || port == "0" || port == "" {
		t.Fatalf("unbound SOCKS address %q: %v", address, err)
	}
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	var reply [2]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil || reply != [2]byte{5, 0} {
		t.Fatalf("bound endpoint did not speak SOCKS: %v, %v", reply, err)
	}
	_ = conn.Close()
	StopTransport()
	if GetSocksAddr() != "" {
		t.Fatal("stopped embedded runtime retained endpoint")
	}
}
