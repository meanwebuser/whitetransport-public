// Command socksprobe is a deterministic, low-level tester for the WhiteTransport
// SOCKS5 egress tunnel. It avoids curl/HTTP-client quirks by speaking SOCKS5 and
// the target protocol by hand, with full control over every read/write.
//
// Modes:
//
//	-mode echo : start a local TCP echo server, tunnel to it via SOCKS5, send a
//	             payload, and verify the exact bytes come back. Pure byte-pipe
//	             test with no internet dependency.
//	-mode packet : send a length-prefixed custom TCP packet through the same
//	              echo path and verify the framed bytes come back unchanged.
//	-mode http : open a SOCKS5 tunnel to -target and perform a raw HTTP/1.1 GET,
//	             printing the raw response bytes regardless of HTTP version.
package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	var (
		mode    = flag.String("mode", "echo", "echo | http")
		socks   = flag.String("socks", "127.0.0.1:1080", "SOCKS5 proxy address")
		bind    = flag.String("bind", "127.0.0.1", "local IP for echo server (use LAN IP for cross-machine tests)")
		target  = flag.String("target", "ifconfig.me:80", "target host:port (http mode)")
		host    = flag.String("host", "ifconfig.me", "HTTP Host header (http mode)")
		payload = flag.String("payload", "PING-whitetransport-1234567890", "echo payload")
		size    = flag.Int("size", 0, "if >0, echo payload of this many bytes overrides -payload")
		timeout = flag.Duration("timeout", 30*time.Second, "overall timeout")
	)
	flag.Parse()

	var err error
	switch *mode {
	case "echo":
		err = runEcho(*socks, *bind, *payload, *size, *timeout)
	case "packet":
		err = runPacket(*socks, *bind, *payload, *size, *timeout)
	case "http":
		err = runHTTP(*socks, *target, *host, *timeout)
	default:
		err = fmt.Errorf("unknown mode %q", *mode)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "[probe] FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("[probe] OK")
}

func runEcho(socks, bind, payload string, size int, timeout time.Duration) error {
	if size > 0 {
		b := make([]byte, size)
		for i := range b {
			b[i] = byte('A' + (i % 26))
		}
		payload = string(b)
	}

	ln, err := net.Listen("tcp", bind+":0")
	if err != nil {
		return fmt.Errorf("listen echo: %w", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				buf := make([]byte, 4096)
				for {
					n, err := conn.Read(buf)
					if n > 0 {
						if _, werr := conn.Write(buf[:n]); werr != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}(c)
		}
	}()

	target := ln.Addr().String()
	fmt.Printf("[probe] echo server at %s, tunneling via %s\n", target, socks)

	conn, err := dialSocks(socks, target, timeout)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	start := time.Now()
	if _, err := conn.Write([]byte(payload)); err != nil {
		return fmt.Errorf("write payload: %w", err)
	}
	fmt.Printf("[probe] sent %d bytes\n", len(payload))

	got := make([]byte, 0, len(payload))
	buf := make([]byte, 4096)
	for len(got) < len(payload) {
		n, err := conn.Read(buf)
		if n > 0 {
			got = append(got, buf[:n]...)
			fmt.Printf("[probe] recv chunk %d (total %d/%d)\n", n, len(got), len(payload))
		}
		if err != nil {
			return fmt.Errorf("read echo after %d/%d bytes: %w", len(got), len(payload), err)
		}
	}
	if !bytes.Equal(got, []byte(payload)) {
		return fmt.Errorf("echo mismatch: sent %q got %q", payload, string(got))
	}
	fmt.Printf("[probe] echo verified %d bytes in %s\n", len(got), time.Since(start))
	return nil
}

func runPacket(socks, bind, payload string, size int, timeout time.Duration) error {
	if size > 0 {
		b := make([]byte, size)
		for i := range b {
			b[i] = byte('a' + (i % 26))
		}
		payload = string(b)
	}

	ln, err := net.Listen("tcp", bind+":0")
	if err != nil {
		return fmt.Errorf("listen packet echo: %w", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				for {
					var n uint32
					if err := binary.Read(conn, binary.BigEndian, &n); err != nil {
						return
					}
					buf := make([]byte, n)
					if _, err := io.ReadFull(conn, buf); err != nil {
						return
					}
					if err := binary.Write(conn, binary.BigEndian, n); err != nil {
						return
					}
					if _, err := conn.Write(buf); err != nil {
						return
					}
				}
			}(c)
		}
	}()

	target := ln.Addr().String()
	fmt.Printf("[probe] packet echo server at %s, tunneling via %s\n", target, socks)

	conn, err := dialSocks(socks, target, timeout)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	start := time.Now()
	if err := binary.Write(conn, binary.BigEndian, uint32(len(payload))); err != nil {
		return fmt.Errorf("packet write len: %w", err)
	}
	if _, err := conn.Write([]byte(payload)); err != nil {
		return fmt.Errorf("packet write payload: %w", err)
	}
	var n uint32
	if err := binary.Read(conn, binary.BigEndian, &n); err != nil {
		return fmt.Errorf("packet read len: %w", err)
	}
	got := make([]byte, n)
	if _, err := io.ReadFull(conn, got); err != nil {
		return fmt.Errorf("packet read payload: %w", err)
	}
	if !bytes.Equal(got, []byte(payload)) {
		return fmt.Errorf("packet mismatch: sent %q got %q", payload, string(got))
	}
	fmt.Printf("[probe] packet verified %d bytes in %s\n", len(got), time.Since(start))
	return nil
}

func runHTTP(socks, target, host string, timeout time.Duration) error {
	fmt.Printf("[probe] http GET / via %s -> %s (Host: %s)\n", socks, target, host)
	conn, err := dialSocks(socks, target, timeout)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	req := "GET / HTTP/1.1\r\nHost: " + host + "\r\nUser-Agent: socksprobe/1\r\nAccept: */*\r\nConnection: close\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		return fmt.Errorf("write request: %w", err)
	}
	fmt.Printf("[probe] sent request %d bytes\n", len(req))

	raw, err := io.ReadAll(conn)
	if len(raw) > 0 {
		fmt.Printf("[probe] raw response %d bytes:\n--- BEGIN ---\n%s\n--- END ---\n", len(raw), string(raw))
	}
	if err != nil && !errors.Is(err, io.EOF) {
		// A timeout with data already received is still informative.
		if ne, ok := err.(net.Error); !(ok && ne.Timeout() && len(raw) > 0) {
			return fmt.Errorf("read response after %d bytes: %w", len(raw), err)
		}
	}
	if len(raw) == 0 {
		return errors.New("empty response")
	}
	if !strings.HasPrefix(string(raw), "HTTP/1.") {
		return fmt.Errorf("response does not start with HTTP/1.x: %q", firstLine(raw))
	}
	return nil
}

func firstLine(b []byte) string {
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		return strings.TrimRight(string(b[:i]), "\r")
	}
	if len(b) > 80 {
		return string(b[:80])
	}
	return string(b)
}

// dialSocks performs a minimal SOCKS5 no-auth CONNECT handshake and returns the
// established connection. host is resolved remotely (SOCKS5 domain address type)
// so DNS happens at the exit node.
func dialSocks(socks, target string, timeout time.Duration) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return nil, fmt.Errorf("split target %q: %w", target, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("bad port %q: %w", portStr, err)
	}

	conn, err := net.DialTimeout("tcp", socks, timeout)
	if err != nil {
		return nil, fmt.Errorf("dial socks %s: %w", socks, err)
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))

	// Greeting: VER=5, NMETHODS=1, METHOD=0 (no auth).
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("write greeting: %w", err)
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		conn.Close()
		return nil, fmt.Errorf("read method: %w", err)
	}
	if resp[0] != 0x05 || resp[1] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("unexpected method selection %v", resp)
	}

	// CONNECT request with domain address type.
	var req bytes.Buffer
	req.Write([]byte{0x05, 0x01, 0x00, 0x03})
	req.WriteByte(byte(len(host)))
	req.WriteString(host)
	_ = binary.Write(&req, binary.BigEndian, uint16(port))
	if _, err := conn.Write(req.Bytes()); err != nil {
		conn.Close()
		return nil, fmt.Errorf("write connect: %w", err)
	}

	reader := bufio.NewReader(conn)
	head := make([]byte, 4)
	if _, err := io.ReadFull(reader, head); err != nil {
		conn.Close()
		return nil, fmt.Errorf("read reply head: %w", err)
	}
	if head[0] != 0x05 {
		conn.Close()
		return nil, fmt.Errorf("bad reply version %d", head[0])
	}
	if head[1] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("socks connect failed code=%d", head[1])
	}
	// Consume bound address per ATYP.
	switch head[3] {
	case 0x01:
		if _, err := io.ReadFull(reader, make([]byte, 4+2)); err != nil {
			conn.Close()
			return nil, fmt.Errorf("read bound v4: %w", err)
		}
	case 0x04:
		if _, err := io.ReadFull(reader, make([]byte, 16+2)); err != nil {
			conn.Close()
			return nil, fmt.Errorf("read bound v6: %w", err)
		}
	case 0x03:
		l, err := reader.ReadByte()
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("read bound domain len: %w", err)
		}
		if _, err := io.ReadFull(reader, make([]byte, int(l)+2)); err != nil {
			conn.Close()
			return nil, fmt.Errorf("read bound domain: %w", err)
		}
	default:
		conn.Close()
		return nil, fmt.Errorf("unknown bound atyp %d", head[3])
	}

	fmt.Printf("[probe] SOCKS5 CONNECT established to %s\n", target)
	// Wrap so buffered bytes (if any) are not lost.
	return &bufferedConn{Conn: conn, r: reader}, nil
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) { return b.r.Read(p) }
