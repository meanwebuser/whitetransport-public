package proxy

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/session"
)

func TestSOCKS5ServerViaEgressDialer(t *testing.T) {
	t.Parallel()

	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fmt.Fprintf(writer, "carrier-path path=%s", request.URL.Path)
	}))
	defer target.Close()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen socks5: %v", err)
	}
	listenAddr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close probe listener: %v", err)
	}

	server := Server{
		ListenAddr: listenAddr,
		EgressDialer: func(ctx context.Context, targetAddr string) (net.Conn, string, error) {
			dialer := &net.Dialer{Timeout: 5 * time.Second}
			conn, err := dialer.DialContext(ctx, "tcp", targetAddr)
			return conn, "test-carrier", err
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe(ctx)
	}()
	waitForTCP(t, listenAddr)

	targetURL, err := url.Parse(target.URL)
	if err != nil {
		t.Fatalf("parse target url: %v", err)
	}

	conn, err := net.DialTimeout("tcp", listenAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial socks5: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte{socksVersion5, 1, socksNoAuth}); err != nil {
		t.Fatalf("write socks greeting: %v", err)
	}
	methodReply := make([]byte, 2)
	if _, err := io.ReadFull(conn, methodReply); err != nil {
		t.Fatalf("read socks greeting reply: %v", err)
	}
	if methodReply[0] != socksVersion5 || methodReply[1] != socksNoAuth {
		t.Fatalf("unexpected socks greeting reply: %v", methodReply)
	}

	port := mustPort(t, targetURL)
	request, err := buildSOCKSConnectRequest(targetURL.Hostname(), port)
	if err != nil {
		t.Fatalf("build socks connect request: %v", err)
	}
	if _, err := conn.Write(request); err != nil {
		t.Fatalf("write socks connect request: %v", err)
	}

	reply := make([]byte, 4)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("read socks connect reply: %v", err)
	}
	if reply[1] != 0x00 {
		t.Fatalf("socks connect failed with code %d", reply[1])
	}
	if _, err := discardBoundAddress(conn, reply[3]); err != nil {
		t.Fatalf("discard socks bind address: %v", err)
	}

	if _, err := fmt.Fprintf(conn, "GET /relay HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", targetURL.Host); err != nil {
		t.Fatalf("write tunneled request: %v", err)
	}
	body, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read tunneled response: %v", err)
	}
	if !strings.Contains(string(body), "carrier-path path=/relay") {
		t.Fatalf("unexpected tunneled response: %s", string(body))
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("server shutdown error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for socks server shutdown")
	}
}

func TestSOCKS5ServerFailsWithoutEgressDialer(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen socks5: %v", err)
	}
	listenAddr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close probe listener: %v", err)
	}

	server := Server{
		ListenAddr: listenAddr,
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe(ctx)
	}()
	waitForTCP(t, listenAddr)

	conn, err := net.DialTimeout("tcp", listenAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial socks5: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte{socksVersion5, 1, socksNoAuth}); err != nil {
		t.Fatalf("write socks greeting: %v", err)
	}
	methodReply := make([]byte, 2)
	if _, err := io.ReadFull(conn, methodReply); err != nil {
		t.Fatalf("read socks greeting reply: %v", err)
	}

	request := []byte{socksVersion5, socksCmdConnect, 0x00, socksAtypIPv4, 127, 0, 0, 1, 0, 80}
	if _, err := conn.Write(request); err != nil {
		t.Fatalf("write socks connect request: %v", err)
	}

	reply := make([]byte, 4)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("read socks connect reply: %v", err)
	}
	if reply[1] != 0x01 {
		t.Fatalf("expected general failure reply code, got %d", reply[1])
	}

	cancel()
	<-errCh
}

// TestServerUDPAssociateThroughCarrier is the RED contract for packet egress.
// The current SOCKS implementation rejects command 0x03 before a carrier can
// be selected; once packet egress is wired, the nonce must make a full
// round-trip through the carrier-backed UDP association.
func TestServerUDPAssociateThroughCarrier(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen socks5: %v", err)
	}
	listenAddr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close probe listener: %v", err)
	}

	carrier := newFakePacketCarrier()
	var openedMetadata session.PacketMetadata
	server := Server{ListenAddr: listenAddr, EgressPacketDialer: func(_ context.Context, metadata session.PacketMetadata) (net.PacketConn, string, error) {
		openedMetadata = metadata
		return carrier, "fake-carrier", nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe(ctx) }()
	waitForTCP(t, listenAddr)

	conn, err := net.DialTimeout("tcp", listenAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial socks5: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte{socksVersion5, 1, socksNoAuth}); err != nil {
		t.Fatalf("write socks greeting: %v", err)
	}
	methodReply := make([]byte, 2)
	if _, err := io.ReadFull(conn, methodReply); err != nil {
		t.Fatalf("read socks greeting reply: %v", err)
	}
	udpClient, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen UDP client: %v", err)
	}
	defer udpClient.Close()
	udpClientAddr := udpClient.LocalAddr().(*net.UDPAddr)
	associateRequest := []byte{socksVersion5, socksCmdUDPAssociate, 0x00, socksAtypIPv4, 127, 0, 0, 1}
	associateRequest = binary.BigEndian.AppendUint16(associateRequest, uint16(udpClientAddr.Port))
	if _, err := conn.Write(associateRequest); err != nil {
		t.Fatalf("write socks UDP associate request: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	reply := make([]byte, 4)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("RED: UDP ASSOCIATE currently rejected before carrier echo: %v", err)
	}
	if reply[1] != 0x00 {
		t.Fatalf("UDP ASSOCIATE reply code = %d, want success", reply[1])
	}
	bound := make([]byte, 6)
	if _, err := io.ReadFull(conn, bound); err != nil {
		t.Fatalf("read UDP relay address: %v", err)
	}
	relayAddr := net.JoinHostPort(net.IP(bound[:4]).String(), strconv.Itoa(int(binary.BigEndian.Uint16(bound[4:]))))
	attacker, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen attacker UDP client: %v", err)
	}
	defer attacker.Close()
	nonce := []byte("udp-carrier-nonce")
	frame := buildSOCKSUDPDatagram("127.0.0.1", 5353, nonce)
	if _, err := attacker.WriteTo(frame, mustUDPAddr(t, relayAddr)); err != nil {
		t.Fatalf("write attacker SOCKS UDP datagram: %v", err)
	}
	if err := attacker.SetReadDeadline(time.Now().Add(250 * time.Millisecond)); err != nil {
		t.Fatalf("set attacker UDP deadline: %v", err)
	}
	if n, _, err := attacker.ReadFrom(make([]byte, 2048)); err == nil {
		t.Fatalf("UDP ASSOCIATE accepted an unrequested source port and echoed %d bytes", n)
	}
	if _, err := udpClient.WriteTo(frame, mustUDPAddr(t, relayAddr)); err != nil {
		t.Fatalf("write SOCKS UDP datagram: %v", err)
	}
	if err := udpClient.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set UDP client deadline: %v", err)
	}
	response := make([]byte, 2048)
	n, _, err := udpClient.ReadFrom(response)
	if err != nil {
		t.Fatalf("read carrier UDP echo: %v", err)
	}
	destination, payload, err := decodeSOCKSUDPDatagram(response[:n])
	if err != nil {
		t.Fatalf("decode carrier UDP echo: %v", err)
	}
	if destination != "127.0.0.1:5353" || string(payload) != string(nonce) {
		t.Fatalf("carrier UDP echo destination=%q payload=%q", destination, payload)
	}
	if openedMetadata.SessionID != "" {
		t.Fatalf("SOCKS layer forged packet session ID %q", openedMetadata.SessionID)
	}
	if source := carrier.packetSource(); source != udpClient.LocalAddr().String() {
		t.Fatalf("packet source metadata=%q, want actual UDP source %q", source, udpClient.LocalAddr())
	}

	cancel()
	<-errCh
	if closes := carrier.closeCount(); closes != 1 {
		t.Fatalf("packet carrier closes = %d, want exactly one", closes)
	}
}

func TestWriteReplyEncodesIPv6BoundAddress(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	written := make(chan error, 1)
	go func() {
		written <- writeReply(server, 0x00, &net.UDPAddr{IP: net.ParseIP("::1"), Port: 43210})
	}()
	reply := make([]byte, 4+net.IPv6len+2)
	if _, err := io.ReadFull(client, reply); err != nil {
		t.Fatalf("read IPv6 SOCKS reply: %v", err)
	}
	if err := <-written; err != nil {
		t.Fatalf("write IPv6 SOCKS reply: %v", err)
	}
	if reply[3] != socksAtypIPv6 {
		t.Fatalf("SOCKS reply ATYP = %d, want IPv6", reply[3])
	}
	if ip := net.IP(reply[4 : 4+net.IPv6len]); !ip.Equal(net.ParseIP("::1")) {
		t.Fatalf("SOCKS reply IP = %s, want ::1", ip)
	}
	if port := binary.BigEndian.Uint16(reply[4+net.IPv6len:]); port != 43210 {
		t.Fatalf("SOCKS reply port = %d, want 43210", port)
	}
}

func TestServerUDPAssociateThroughCarrierIPv6(t *testing.T) {
	probe, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	_ = probe.Close()
	udpClient, err := net.ListenPacket("udp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 UDP loopback unavailable: %v", err)
	}
	defer udpClient.Close()

	carrier := newFakePacketCarrier()
	server := Server{ListenAddr: "[::1]:0", EgressPacketDialer: func(_ context.Context, _ session.PacketMetadata) (net.PacketConn, string, error) {
		return carrier, "fake-carrier-ipv6", nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe(ctx) }()
	waitForServerAddr(t, &server)

	control, err := net.DialTimeout("tcp6", server.Addr(), 5*time.Second)
	if err != nil {
		cancel()
		t.Fatalf("dial IPv6 SOCKS control: %v", err)
	}
	defer control.Close()
	if _, err := control.Write([]byte{socksVersion5, 1, socksNoAuth}); err != nil {
		t.Fatalf("write IPv6 SOCKS greeting: %v", err)
	}
	methodReply := make([]byte, 2)
	if _, err := io.ReadFull(control, methodReply); err != nil || methodReply[1] != socksNoAuth {
		t.Fatalf("read IPv6 SOCKS greeting reply=%v err=%v", methodReply, err)
	}
	udpAddress := udpClient.LocalAddr().(*net.UDPAddr)
	request := []byte{socksVersion5, socksCmdUDPAssociate, 0x00, socksAtypIPv6}
	request = append(request, udpAddress.IP.To16()...)
	request = binary.BigEndian.AppendUint16(request, uint16(udpAddress.Port))
	if _, err := control.Write(request); err != nil {
		t.Fatalf("write IPv6 UDP ASSOCIATE: %v", err)
	}
	reply := make([]byte, 4)
	if _, err := io.ReadFull(control, reply); err != nil {
		t.Fatalf("read IPv6 UDP ASSOCIATE reply: %v", err)
	}
	if reply[1] != 0 || reply[3] != socksAtypIPv6 {
		t.Fatalf("IPv6 UDP ASSOCIATE reply=%v, want success with IPv6 ATYP", reply)
	}
	bound := make([]byte, net.IPv6len+2)
	if _, err := io.ReadFull(control, bound); err != nil {
		t.Fatalf("read IPv6 UDP relay address: %v", err)
	}
	relayIP := net.IP(bound[:net.IPv6len])
	relayPort := binary.BigEndian.Uint16(bound[net.IPv6len:])
	if !relayIP.Equal(net.ParseIP("::1")) || relayPort == 0 {
		t.Fatalf("IPv6 UDP relay = [%s]:%d, want actual loopback bind", relayIP, relayPort)
	}
	relay := &net.UDPAddr{IP: relayIP, Port: int(relayPort)}
	nonce := []byte("udp-carrier-ipv6-nonce")
	frame := buildSOCKSUDPDatagram("::1", 5353, nonce)
	if _, err := udpClient.WriteTo(frame, relay); err != nil {
		t.Fatalf("write IPv6 SOCKS UDP datagram: %v", err)
	}
	if err := udpClient.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set IPv6 UDP client deadline: %v", err)
	}
	response := make([]byte, 2048)
	n, _, err := udpClient.ReadFrom(response)
	if err != nil {
		t.Fatalf("read IPv6 carrier UDP echo: %v", err)
	}
	destination, payload, err := decodeSOCKSUDPDatagram(response[:n])
	if err != nil {
		t.Fatalf("decode IPv6 carrier UDP echo: %v", err)
	}
	if destination != "[::1]:5353" || string(payload) != string(nonce) {
		t.Fatalf("IPv6 carrier UDP echo destination=%q payload=%q", destination, payload)
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("IPv6 SOCKS shutdown: %v", err)
	}
	if closes := carrier.closeCount(); closes != 1 {
		t.Fatalf("IPv6 packet carrier closes = %d, want exactly one", closes)
	}
}

func TestSOCKSAddressRejectsEmptyDomain(t *testing.T) {
	if _, _, err := decodeSOCKSAddress([]byte{socksAtypDomainName, 0}, 0); err == nil {
		t.Fatal("SOCKS UDP address decoder accepted an empty domain")
	}
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	handshakeDone := make(chan error, 1)
	go func() {
		_, err := handshake(server)
		handshakeDone <- err
	}()
	if _, err := client.Write([]byte{socksVersion5, 1, socksNoAuth}); err != nil {
		t.Fatalf("write empty-domain greeting: %v", err)
	}
	methodReply := make([]byte, 2)
	if _, err := io.ReadFull(client, methodReply); err != nil {
		t.Fatalf("read empty-domain greeting: %v", err)
	}
	request := []byte{socksVersion5, socksCmdUDPAssociate, 0x00, socksAtypDomainName, 0, 0, 53}
	if _, err := client.Write(request); err != nil {
		t.Fatalf("write empty-domain request: %v", err)
	}
	if err := <-handshakeDone; err == nil {
		t.Fatal("SOCKS handshake accepted an empty domain")
	}
}

type fakePacketCarrier struct {
	mu       sync.Mutex
	readDead time.Time
	source   string
	incoming chan fakePacket
	done     chan struct{}
	closed   bool
	closes   int
}

type fakePacket struct {
	payload []byte
	addr    net.Addr
}

func newFakePacketCarrier() *fakePacketCarrier {
	return &fakePacketCarrier{incoming: make(chan fakePacket, 8), done: make(chan struct{})}
}

func (f *fakePacketCarrier) ReadFrom(buffer []byte) (int, net.Addr, error) {
	f.mu.Lock()
	deadline := f.readDead
	f.mu.Unlock()
	var timer *time.Timer
	if !deadline.IsZero() {
		timer = time.NewTimer(time.Until(deadline))
		defer timer.Stop()
	}
	select {
	case packet := <-f.incoming:
		n := copy(buffer, packet.payload)
		return n, packet.addr, nil
	case <-f.done:
		return 0, nil, net.ErrClosed
	case <-fakeTimerChan(timer):
		return 0, nil, fakeTimeoutError{}
	}
}

func fakeTimerChan(timer *time.Timer) <-chan time.Time {
	if timer == nil {
		return nil
	}
	return timer.C
}

func (f *fakePacketCarrier) WriteTo(payload []byte, addr net.Addr) (int, error) {
	select {
	case f.incoming <- fakePacket{payload: append([]byte(nil), payload...), addr: addr}:
		return len(payload), nil
	case <-f.done:
		return 0, net.ErrClosed
	}
}

func (f *fakePacketCarrier) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes++
	if f.closed {
		panic("fakePacketCarrier closed more than once")
	}
	f.closed = true
	close(f.done)
	return nil
}

func (f *fakePacketCarrier) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closes
}

func (f *fakePacketCarrier) LocalAddr() net.Addr {
	return packetAddress{network: "fake/packet", address: "fake-carrier"}
}
func (f *fakePacketCarrier) SetDeadline(deadline time.Time) error {
	return f.SetReadDeadline(deadline)
}
func (f *fakePacketCarrier) SetReadDeadline(deadline time.Time) error {
	f.mu.Lock()
	f.readDead = deadline
	f.mu.Unlock()
	return nil
}
func (f *fakePacketCarrier) SetWriteDeadline(time.Time) error { return nil }

func (f *fakePacketCarrier) SetPacketSource(source string) {
	f.mu.Lock()
	f.source = source
	f.mu.Unlock()
}

func (f *fakePacketCarrier) packetSource() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.source
}

type fakeTimeoutError struct{}

func (fakeTimeoutError) Error() string   { return "i/o timeout" }
func (fakeTimeoutError) Timeout() bool   { return true }
func (fakeTimeoutError) Temporary() bool { return true }

func buildSOCKSUDPDatagram(host string, port int, payload []byte) []byte {
	ip := net.ParseIP(host)
	frame := []byte{0, 0, 0}
	if ipv4 := ip.To4(); ipv4 != nil {
		frame = append(frame, socksAtypIPv4)
		frame = append(frame, ipv4...)
	} else {
		frame = append(frame, socksAtypIPv6)
		frame = append(frame, ip.To16()...)
	}
	frame = binary.BigEndian.AppendUint16(frame, uint16(port))
	return append(frame, payload...)
}

func waitForServerAddr(t *testing.T, server *Server) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for server.Addr() == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if server.Addr() == "" {
		t.Fatal("timed out waiting for SOCKS server address")
	}
}

func mustUDPAddr(t *testing.T, address string) net.Addr {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp4", address)
	if err != nil {
		t.Fatalf("resolve UDP address %q: %v", address, err)
	}
	return addr
}

func TestSOCKS5ServerShutdownWaitsForConnectionHandlers(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen socks5: %v", err)
	}
	listenAddr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close probe listener: %v", err)
	}

	var serverReturned atomic.Bool
	lateLog := make(chan struct{}, 1)
	server := Server{
		ListenAddr: listenAddr,
		Logf: func(_ string, _ ...any) {
			if serverReturned.Load() {
				select {
				case lateLog <- struct{}{}:
				default:
				}
			}
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe(ctx)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for server.Addr() == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if server.Addr() == "" {
		t.Fatal("timed out waiting for socks5 listener")
	}

	conn, err := net.DialTimeout("tcp", server.Addr(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial socks5: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte{socksVersion5, 1, socksNoAuth}); err != nil {
		t.Fatalf("write socks greeting: %v", err)
	}
	methodReply := make([]byte, 2)
	if _, err := io.ReadFull(conn, methodReply); err != nil {
		t.Fatalf("read socks greeting reply: %v", err)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("server shutdown error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for socks5 server shutdown")
	}
	serverReturned.Store(true)
	if err := conn.Close(); err != nil {
		t.Fatalf("close client connection: %v", err)
	}
	select {
	case <-lateLog:
		t.Fatal("connection handler logged after ListenAndServe returned")
	case <-time.After(100 * time.Millisecond):
	}
}

func waitForTCP(t *testing.T, address string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", address)
}

func mustPort(t *testing.T, parsed *url.URL) int {
	t.Helper()

	port, err := net.LookupPort("tcp", parsed.Port())
	if err != nil {
		t.Fatalf("parse target port: %v", err)
	}
	return port
}

func buildSOCKSConnectRequest(host string, port int) ([]byte, error) {
	var payload []byte
	if ip := net.ParseIP(host); ip != nil {
		if ipv4 := ip.To4(); ipv4 != nil {
			payload = append(payload, socksAtypIPv4)
			payload = append(payload, ipv4...)
		} else {
			payload = append(payload, socksAtypIPv6)
			payload = append(payload, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return nil, fmt.Errorf("host name too long: %d", len(host))
		}
		payload = append(payload, socksAtypDomainName, byte(len(host)))
		payload = append(payload, host...)
	}
	request := []byte{socksVersion5, socksCmdConnect, 0x00}
	request = append(request, payload...)
	request = binary.BigEndian.AppendUint16(request, uint16(port))
	return request, nil
}
