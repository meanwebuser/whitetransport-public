package e2eharness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/proxy"
)

type SOCKSServer struct {
	Addr string
	once sync.Once
	stop func()
}

func (s *SOCKSServer) Close() {
	if s != nil && s.stop != nil {
		s.once.Do(s.stop)
	}
}

func StartDirectSOCKS(t testing.TB) *SOCKSServer {
	t.Helper()

	listenAddr := reserveTCPAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	server := proxy.Server{
		ListenAddr: listenAddr,
		EgressDialer: func(ctx context.Context, targetAddr string) (net.Conn, string, error) {
			dialer := &net.Dialer{Timeout: 5 * time.Second}
			conn, err := dialer.DialContext(ctx, "tcp", targetAddr)
			return conn, "local-direct", err
		},
		Logf: t.Logf,
	}
	go func() {
		errCh <- server.ListenAndServe(ctx)
	}()
	waitForTCP(t, listenAddr, 5*time.Second)

	s := &SOCKSServer{
		Addr: listenAddr,
		stop: func() {
			cancel()
			select {
			case err := <-errCh:
				if err != nil {
					t.Fatalf("socks server stopped with error: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("timed out stopping socks server at %s", listenAddr)
			}
		},
	}
	t.Cleanup(s.Close)
	return s
}

type EchoServer struct {
	Addr string
	once sync.Once
	stop func()
}

func (s *EchoServer) Close() {
	if s != nil && s.stop != nil {
		s.once.Do(s.stop)
	}
}

func StartEchoServer(t testing.TB) *EchoServer {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo server: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()

	s := &EchoServer{
		Addr: ln.Addr().String(),
		stop: func() {
			_ = ln.Close()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatalf("timed out stopping echo server at %s", ln.Addr().String())
			}
		},
	}
	t.Cleanup(s.Close)
	return s
}

func StartHTTPServer(t testing.TB, body string) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprintf(w, "%s method=%s path=%s", body, r.Method, r.URL.Path)
	}))
	t.Cleanup(server.Close)
	return server
}

func ExchangeEcho(t testing.TB, socksAddr, targetAddr string, payload []byte, timeout time.Duration) []byte {
	t.Helper()

	conn := DialSOCKS(t, socksAddr, targetAddr, timeout)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write echo payload: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read echo payload: %v", err)
	}
	return got
}

func GetHTTPViaSOCKS(t testing.TB, socksAddr, targetAddr, host, path string, timeout time.Duration) []byte {
	t.Helper()

	conn := DialSOCKS(t, socksAddr, targetAddr, timeout)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	req := "GET " + path + " HTTP/1.1\r\nHost: " + host + "\r\nUser-Agent: wt-e2e-harness/1\r\nConnection: close\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write HTTP request: %v", err)
	}
	raw, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read HTTP response: %v", err)
	}
	return raw
}

func DialSOCKS(t testing.TB, socksAddr, targetAddr string, timeout time.Duration) net.Conn {
	t.Helper()

	host, portText, err := net.SplitHostPort(targetAddr)
	if err != nil {
		t.Fatalf("split target address %q: %v", targetAddr, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse target port %q: %v", portText, err)
	}
	if len(host) > 255 {
		t.Fatalf("SOCKS domain host is too long: %d", len(host))
	}

	conn, err := net.DialTimeout("tcp", socksAddr, timeout)
	if err != nil {
		t.Fatalf("dial socks server %s: %v", socksAddr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		conn.Close()
		t.Fatalf("write socks greeting: %v", err)
	}
	method := make([]byte, 2)
	if _, err := io.ReadFull(conn, method); err != nil {
		conn.Close()
		t.Fatalf("read socks method: %v", err)
	}
	if !bytes.Equal(method, []byte{0x05, 0x00}) {
		conn.Close()
		t.Fatalf("unexpected socks method reply: %v", method)
	}

	var req bytes.Buffer
	req.Write([]byte{0x05, 0x01, 0x00, 0x03, byte(len(host))})
	req.WriteString(host)
	_ = binary.Write(&req, binary.BigEndian, uint16(port))
	if _, err := conn.Write(req.Bytes()); err != nil {
		conn.Close()
		t.Fatalf("write socks connect: %v", err)
	}

	reader := bufio.NewReader(conn)
	head := make([]byte, 4)
	if _, err := io.ReadFull(reader, head); err != nil {
		conn.Close()
		t.Fatalf("read socks reply: %v", err)
	}
	if head[0] != 0x05 || head[1] != 0x00 {
		conn.Close()
		t.Fatalf("socks connect failed: reply=%v", head)
	}
	consumeBoundAddress(t, reader, head[3])
	return &bufferedConn{Conn: conn, reader: reader}
}

func reserveTCPAddr(t testing.TB) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve tcp address: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close reserved listener: %v", err)
	}
	return addr
}

func waitForTCP(t testing.TB, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for tcp listener %s", addr)
}

func consumeBoundAddress(t testing.TB, reader io.Reader, atyp byte) {
	t.Helper()
	length := 0
	switch atyp {
	case 0x01:
		length = net.IPv4len + 2
	case 0x04:
		length = net.IPv6len + 2
	case 0x03:
		var size [1]byte
		if _, err := io.ReadFull(reader, size[:]); err != nil {
			t.Fatalf("read bound domain length: %v", err)
		}
		length = int(size[0]) + 2
	default:
		t.Fatalf("unsupported bound address type %d", atyp)
	}
	if _, err := io.ReadFull(reader, make([]byte, length)); err != nil {
		t.Fatalf("read bound address: %v", err)
	}
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}
