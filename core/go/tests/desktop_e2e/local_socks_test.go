package desktop_e2e

import (
	"bytes"
	"io"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/tests/e2eharness"
)

func TestLocalSOCKSEchoAndHTTP(t *testing.T) {
	socks := e2eharness.StartDirectSOCKS(t)
	echo := e2eharness.StartEchoServer(t)

	payload := []byte("desktop-e2e-local-echo")
	if got := e2eharness.ExchangeEcho(t, socks.Addr, echo.Addr, payload, 5*time.Second); !bytes.Equal(got, payload) {
		t.Fatalf("echo mismatch: got %q want %q", string(got), string(payload))
	}

	httpTarget := e2eharness.StartHTTPServer(t, "desktop-e2e-local-http")
	parsed, err := url.Parse(httpTarget.URL)
	if err != nil {
		t.Fatalf("parse httptest URL: %v", err)
	}
	raw := e2eharness.GetHTTPViaSOCKS(t, socks.Addr, parsed.Host, parsed.Host, "/probe", 5*time.Second)
	text := string(raw)
	if !strings.HasPrefix(text, "HTTP/1.1 200 OK") {
		t.Fatalf("unexpected HTTP status through SOCKS:\n%s", text)
	}
	if !strings.Contains(text, "desktop-e2e-local-http method=GET path=/probe") {
		t.Fatalf("unexpected HTTP body through SOCKS:\n%s", text)
	}
}

func TestLocalSOCKSRejectsUnreachableTarget(t *testing.T) {
	socks := e2eharness.StartDirectSOCKS(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve unreachable target: %v", err)
	}
	unreachable := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close reserved target: %v", err)
	}

	conn, err := net.DialTimeout("tcp", socks.Addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial socks: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("write socks greeting: %v", err)
	}
	method := make([]byte, 2)
	if _, err := io.ReadFull(conn, method); err != nil {
		t.Fatalf("read socks method: %v", err)
	}

	host, portText, err := net.SplitHostPort(unreachable)
	if err != nil {
		t.Fatalf("split unreachable target: %v", err)
	}
	port, err := net.LookupPort("tcp", portText)
	if err != nil {
		t.Fatalf("parse unreachable port: %v", err)
	}
	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
	req = append(req, host...)
	req = append(req, byte(port>>8), byte(port))
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("write socks connect: %v", err)
	}
	reply := make([]byte, 4)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("read socks failure reply: %v", err)
	}
	if reply[0] != 0x05 || reply[1] == 0x00 {
		t.Fatalf("expected SOCKS failure reply, got %v", reply)
	}
}
