package proxy

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/session"
)

const (
	socksVersion5             = 0x05
	socksNoAuth               = 0x00
	socksUserPassAuth         = 0x02
	socksCmdConnect           = 0x01
	socksCmdUDPAssociate      = 0x03
	socksAtypIPv4             = 0x01
	socksAtypDomainName       = 0x03
	socksAtypIPv6             = 0x04
	defaultUDPAssociationTTL  = 2 * time.Minute
	defaultUDPIdleTimeout     = 30 * time.Second
	maxUDPDatagramSize        = 64 * 1024
	maxCarrierUDPDatagramSize = 4096
)

// Server exposes a minimal no-auth SOCKS5 proxy that routes all egress through
// a carrier EgressDialer. No fallback to direct TCP, HTTP CONNECT, or upstream
// proxy is available — all traffic must go through the carrier tunnel.
type Server struct {
	ListenAddr   string
	EgressDialer func(ctx context.Context, targetAddr string) (net.Conn, string, error)
	// EgressPacketDialer opens a packet association through the active
	// WhiteTransport session. It is intentionally separate from EgressDialer so
	// UDP can never fall through to a direct host socket.
	EgressPacketDialer func(ctx context.Context, metadata session.PacketMetadata) (net.PacketConn, string, error)
	// UDPAssociationTTL bounds a UDP ASSOCIATE even when the TCP control
	// connection remains open. UDPIdleTimeout reclaims associations with no
	// datagrams in either direction.
	UDPAssociationTTL time.Duration
	UDPIdleTimeout    time.Duration
	Logf              func(format string, args ...any)

	mu        sync.Mutex
	socksAddr string
}

// ListenAndServe starts the proxy and blocks until the context is cancelled or an error occurs.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.socksAddr
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen socks5 %s: %w", s.ListenAddr, err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	watcherDone := make(chan struct{})
	var handlers sync.WaitGroup
	var activeMu sync.Mutex
	active := make(map[net.Conn]struct{})
	closeActive := func() {
		activeMu.Lock()
		defer activeMu.Unlock()
		for conn := range active {
			_ = conn.Close()
		}
	}
	defer func() {
		cancel()
		<-watcherDone
		closeActive()
		handlers.Wait()
	}()

	s.logf("whitetransportd socks5 listening on %s", listener.Addr().String())

	s.mu.Lock()
	s.socksAddr = listener.Addr().String()
	s.mu.Unlock()

	go func() {
		defer close(watcherDone)
		<-runCtx.Done()
		_ = listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if runCtx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Temporary() {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			return fmt.Errorf("accept socks5: %w", err)
		}
		activeMu.Lock()
		active[conn] = struct{}{}
		activeMu.Unlock()
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			defer func() {
				activeMu.Lock()
				delete(active, conn)
				activeMu.Unlock()
			}()
			s.handleConn(runCtx, conn)
		}()
	}
}

func (s *Server) handleConn(ctx context.Context, client net.Conn) {
	defer client.Close()

	if err := client.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		s.logf("socks5 set deadline error remote=%s err=%v", client.RemoteAddr(), err)
		return
	}

	request, err := handshake(client)
	if err != nil {
		s.logf("socks5 handshake error remote=%s err=%v", client.RemoteAddr(), err)
		return
	}
	if request.command == socksCmdUDPAssociate {
		s.handleUDPAssociate(ctx, client, request)
		return
	}

	upstreamConn, route, err := s.dialTarget(ctx, request.targetAddr)
	if err != nil {
		replyErr := writeReply(client, replyCode(err), nil)
		if replyErr != nil {
			s.logf("socks5 reply error remote=%s err=%v", client.RemoteAddr(), replyErr)
		}
		s.logf("socks5 connect error remote=%s target=%s route=%s err=%v", client.RemoteAddr(), request.targetAddr, route, err)
		return
	}
	defer upstreamConn.Close()

	if err := client.SetDeadline(time.Time{}); err != nil {
		s.logf("socks5 clear deadline error remote=%s err=%v", client.RemoteAddr(), err)
		return
	}

	if err := writeReply(client, 0x00, upstreamConn.LocalAddr()); err != nil {
		s.logf("socks5 success reply error remote=%s target=%s err=%v", client.RemoteAddr(), request.targetAddr, err)
		return
	}

	s.logf("socks5 connected remote=%s target=%s route=%s", client.RemoteAddr(), request.targetAddr, route)
	s.pipeBothWays(client, upstreamConn)
}

func (s *Server) dialTarget(ctx context.Context, targetAddr string) (net.Conn, string, error) {
	if s.EgressDialer == nil {
		return nil, "", errors.New("socks5 server has no egress dialer configured")
	}
	return s.EgressDialer(ctx, targetAddr)
}

type socksRequest struct {
	command    byte
	targetAddr string
}

func handshake(client net.Conn) (socksRequest, error) {
	reader := bufio.NewReader(client)

	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil {
		return socksRequest{}, err
	}
	if header[0] != socksVersion5 {
		return socksRequest{}, fmt.Errorf("unsupported socks version %d", header[0])
	}

	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(reader, methods); err != nil {
		return socksRequest{}, err
	}
	if !supportsNoAuth(methods) {
		if _, err := client.Write([]byte{socksVersion5, 0xff}); err != nil {
			return socksRequest{}, err
		}
		return socksRequest{}, errors.New("client does not support no-auth socks5")
	}
	if _, err := client.Write([]byte{socksVersion5, socksNoAuth}); err != nil {
		return socksRequest{}, err
	}

	request := make([]byte, 4)
	if _, err := io.ReadFull(reader, request); err != nil {
		return socksRequest{}, err
	}
	if request[0] != socksVersion5 {
		return socksRequest{}, fmt.Errorf("request socks version %d", request[0])
	}
	if request[1] != socksCmdConnect && request[1] != socksCmdUDPAssociate {
		return socksRequest{}, fmt.Errorf("unsupported socks command %d", request[1])
	}

	host, err := readAddress(reader, request[3])
	if err != nil {
		return socksRequest{}, err
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(reader, portBytes); err != nil {
		return socksRequest{}, err
	}
	port := binary.BigEndian.Uint16(portBytes)
	return socksRequest{command: request[1], targetAddr: net.JoinHostPort(host, strconv.Itoa(int(port)))}, nil
}

func supportsNoAuth(methods []byte) bool {
	for _, method := range methods {
		if method == socksNoAuth {
			return true
		}
	}
	return false
}

func readAddress(reader *bufio.Reader, atyp byte) (string, error) {
	switch atyp {
	case socksAtypIPv4:
		addr := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(reader, addr); err != nil {
			return "", err
		}
		return net.IP(addr).String(), nil
	case socksAtypIPv6:
		addr := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(reader, addr); err != nil {
			return "", err
		}
		return net.IP(addr).String(), nil
	case socksAtypDomainName:
		length, err := reader.ReadByte()
		if err != nil {
			return "", err
		}
		if length == 0 {
			return "", errors.New("SOCKS domain name is empty")
		}
		addr := make([]byte, int(length))
		if _, err := io.ReadFull(reader, addr); err != nil {
			return "", err
		}
		return string(addr), nil
	default:
		return "", fmt.Errorf("unsupported address type %d", atyp)
	}
}

func discardBoundAddress(reader io.Reader, atyp byte) (int, error) {
	var length int
	switch atyp {
	case socksAtypIPv4:
		length = net.IPv4len
	case socksAtypIPv6:
		length = net.IPv6len
	case socksAtypDomainName:
		size := make([]byte, 1)
		if _, err := io.ReadFull(reader, size); err != nil {
			return 0, err
		}
		length = int(size[0])
	default:
		return 0, fmt.Errorf("unsupported bound address type %d", atyp)
	}
	buffer := make([]byte, length+2)
	_, err := io.ReadFull(reader, buffer)
	return len(buffer), err
}

func writeReply(client net.Conn, code byte, addr net.Addr) error {
	host := net.IP(net.IPv4zero.To4())
	port := 0
	switch boundAddr := addr.(type) {
	case *net.TCPAddr:
		if boundAddr != nil && boundAddr.IP != nil {
			host = append(net.IP(nil), boundAddr.IP...)
			port = boundAddr.Port
		}
	case *net.UDPAddr:
		if boundAddr != nil && boundAddr.IP != nil {
			host = append(net.IP(nil), boundAddr.IP...)
			port = boundAddr.Port
		}
	}
	atyp := byte(socksAtypIPv6)
	encodedHost := host.To16()
	if ipv4 := host.To4(); ipv4 != nil {
		atyp = socksAtypIPv4
		encodedHost = ipv4
	}
	if encodedHost == nil {
		return fmt.Errorf("SOCKS reply address %q is not an IP address", host)
	}
	reply := []byte{socksVersion5, code, 0x00, atyp}
	reply = append(reply, encodedHost...)
	reply = binary.BigEndian.AppendUint16(reply, uint16(port))
	_, err := client.Write(reply)
	return err
}

func replyCode(err error) byte {
	switch {
	case err == nil:
		return 0x00
	case errors.Is(err, context.DeadlineExceeded):
		return 0x06
	default:
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return 0x06
		}
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) {
			return 0x04
		}
		return 0x01
	}
}

func (s *Server) pipeBothWays(client net.Conn, upstream net.Conn) {
	var once sync.Once
	requestDone := make(chan struct{})
	closeBoth := func() {
		_ = client.Close()
		_ = upstream.Close()
	}

	// Request path: client -> upstream. When the client finishes sending
	// (commonly a write half-close after an HTTP request), do NOT tear down
	// the whole tunnel: the response is still pending. Half-close the upstream
	// write side if supported so the exit node sees end-of-request.
	go func() {
		defer close(requestDone)
		_, _ = io.Copy(upstream, client)
		if cw, ok := upstream.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()

	// Response path: upstream -> client. When this finishes the exchange is
	// complete (or failed); tear everything down.
	n, err := io.Copy(client, upstream)
	s.logf("socks5 response copy done bytes=%d err=%v", n, err)
	once.Do(closeBoth)
	<-requestDone
}

func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}
