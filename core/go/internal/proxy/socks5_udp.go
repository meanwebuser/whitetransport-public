package proxy

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/session"
)

// handleUDPAssociate owns the local SOCKS UDP relay and one carrier-backed
// packet connection. The TCP control connection only bounds the association;
// datagrams never use it as a network fallback.
func (s *Server) handleUDPAssociate(ctx context.Context, client net.Conn, request socksRequest) {
	if s.EgressPacketDialer == nil && (!s.DNSOverStreamFallback || s.EgressDialer == nil) {
		_ = writeReply(client, 0x01, nil)
		s.logf("socks5 UDP associate rejected remote=%s: no packet egress dialer", client.RemoteAddr())
		return
	}
	association, err := newUDPAssociationClient(request.targetAddr, client.RemoteAddr())
	if err != nil {
		_ = writeReply(client, 0x08, nil)
		s.logf("socks5 UDP associate rejected remote=%s err=%v", client.RemoteAddr(), err)
		return
	}

	relayNetwork, relayAddress := association.RelayListenAddress()
	relay, err := net.ListenPacket(relayNetwork, relayAddress)
	if err != nil {
		_ = writeReply(client, replyCode(err), nil)
		s.logf("socks5 UDP relay listen failed remote=%s err=%v", client.RemoteAddr(), err)
		return
	}

	flowID := fmt.Sprintf("udp-%s-%d", sanitizeFlowPart(client.RemoteAddr().String()), time.Now().UnixNano())
	associationTTL := s.UDPAssociationTTL
	if associationTTL <= 0 {
		associationTTL = defaultUDPAssociationTTL
	}
	metadata := session.PacketMetadata{
		FlowID:     flowID,
		SourceAddr: association.SourceAddr(),
		ExpiresAt:  time.Now().Add(associationTTL),
	}
	var packetConn net.PacketConn
	var route string
	if s.EgressPacketDialer != nil {
		packetConn, route, err = s.EgressPacketDialer(ctx, metadata)
	} else {
		err = errors.New("no packet egress dialer")
	}
	if err != nil && s.DNSOverStreamFallback && s.EgressDialer != nil {
		s.logf("socks5 UDP packet egress unavailable remote=%s route=%s; using DNS-over-stream", client.RemoteAddr(), route)
		packetConn = newDNSOverStreamPacketConn(ctx, s.EgressDialer)
		route = "dns-over-stream"
		err = nil
	}
	if err != nil {
		_ = relay.Close()
		_ = writeReply(client, replyCode(err), nil)
		s.logf("socks5 UDP carrier open failed remote=%s route=%s err=%v", client.RemoteAddr(), route, err)
		return
	}
	assocCtx, cancel := context.WithCancel(ctx)
	lifecycle := &udpAssociationLifecycle{cancel: cancel, relay: relay, packetConn: packetConn}
	defer lifecycle.Close()

	if err := client.SetDeadline(time.Time{}); err != nil {
		s.logf("socks5 UDP clear deadline error remote=%s err=%v", client.RemoteAddr(), err)
		return
	}
	if err := writeReply(client, 0x00, relay.LocalAddr()); err != nil {
		s.logf("socks5 UDP success reply error remote=%s route=%s err=%v", client.RemoteAddr(), route, err)
		return
	}

	idleTimeout := s.UDPIdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = defaultUDPIdleTimeout
	}
	activity := make(chan struct{}, 1)
	markActivity := func() {
		select {
		case activity <- struct{}{}:
		default:
		}
	}
	workerDone := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(3)
	go func() {
		defer workers.Done()
		defer cancel()
		s.readSOCKSUDP(assocCtx, relay, packetConn, association, markActivity)
	}()
	go func() {
		defer workers.Done()
		defer cancel()
		s.readCarrierUDP(assocCtx, relay, packetConn, association, markActivity)
	}()
	go func() {
		defer workers.Done()
		defer cancel()
		monitorUDPControl(assocCtx, client)
	}()
	go func() {
		workers.Wait()
		close(workerDone)
	}()

	ttlTimer := time.NewTimer(associationTTL)
	defer ttlTimer.Stop()
	idleTimer := time.NewTimer(idleTimeout)
	defer idleTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			lifecycle.Close()
		case <-workerDone:
			lifecycle.Close()
			return
		case <-ttlTimer.C:
			s.logf("socks5 UDP association expired remote=%s flow=%s", client.RemoteAddr(), flowID)
			lifecycle.Close()
		case <-idleTimer.C:
			s.logf("socks5 UDP association idle timeout remote=%s flow=%s", client.RemoteAddr(), flowID)
			lifecycle.Close()
		case <-activity:
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(idleTimeout)
		}
		if assocCtx.Err() != nil {
			lifecycle.Close()
			<-workerDone
			return
		}
	}
}

// udpAssociationLifecycle is the sole owner of both datagram resources. Every
// shutdown trigger converges here; workers only cancel the association context
// and are joined by handleUDPAssociate before its deferred Close can run.
type udpAssociationLifecycle struct {
	once       sync.Once
	cancel     context.CancelFunc
	relay      net.PacketConn
	packetConn net.PacketConn
}

func (lifecycle *udpAssociationLifecycle) Close() {
	lifecycle.once.Do(func() {
		lifecycle.cancel()
		_ = lifecycle.relay.Close()
		_ = lifecycle.packetConn.Close()
	})
}

func (s *Server) readSOCKSUDP(ctx context.Context, relay net.PacketConn, packetConn net.PacketConn, association *udpAssociationClient, markActivity func()) {
	buffer := make([]byte, maxUDPDatagramSize)
	for {
		if err := relay.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			return
		}
		n, addr, err := relay.ReadFrom(buffer)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			return
		}
		if n == 0 {
			continue
		}
		destination, payload, err := decodeSOCKSUDPDatagram(buffer[:n])
		if err != nil {
			// RFC 1928 requires fragmented datagrams (FRAG != 0) to be
			// rejected. Drop malformed/fragmented packets without touching the
			// carrier session.
			s.logf("socks5 UDP datagram dropped source=%s err=%v", addr, err)
			continue
		}
		if len(payload) > maxCarrierUDPDatagramSize {
			s.logf("socks5 UDP datagram dropped source=%s bytes=%d: carrier payload limit", addr, len(payload))
			continue
		}
		if !association.Accept(addr) {
			continue
		}
		if updater, ok := packetConn.(session.PacketSourceUpdater); ok {
			updater.SetPacketSource(addr.String())
		}
		if _, err := packetConn.WriteTo(payload, packetDestinationAddr(destination)); err != nil {
			s.logf("socks5 UDP carrier write source=%s destination=%s err=%v", addr, destination, err)
			return
		}
		markActivity()
	}
}

func (s *Server) readCarrierUDP(ctx context.Context, relay net.PacketConn, packetConn net.PacketConn, association *udpAssociationClient, markActivity func()) {
	buffer := make([]byte, maxUDPDatagramSize)
	for {
		if err := packetConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			return
		}
		n, source, err := packetConn.ReadFrom(buffer)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			return
		}
		if n == 0 {
			continue
		}
		destination := association.Addr()
		if destination == nil {
			continue
		}
		frame, err := encodeSOCKSUDPDatagram(source.String(), buffer[:n])
		if err != nil {
			s.logf("socks5 UDP reverse frame dropped source=%s err=%v", source, err)
			continue
		}
		if _, err := relay.WriteTo(frame, destination); err != nil {
			return
		}
		markActivity()
	}
}

// udpAssociationClient enforces RFC 1928's requested UDP source address. A
// zero requested port is atomically pinned by the first datagram from the TCP
// control peer's IP; a non-zero port is fixed before carrier egress opens.
type udpAssociationClient struct {
	mu   sync.Mutex
	ip   net.IP
	port int
}

func newUDPAssociationClient(requestedAddress string, controlRemote net.Addr) (*udpAssociationClient, error) {
	remoteHost, _, err := net.SplitHostPort(controlRemote.String())
	if err != nil {
		return nil, fmt.Errorf("split UDP control peer %q: %w", controlRemote, err)
	}
	remoteIP := net.ParseIP(remoteHost)
	if remoteIP == nil {
		return nil, fmt.Errorf("UDP control peer %q is not an IP address", remoteHost)
	}
	requestedHost, requestedPortText, err := net.SplitHostPort(requestedAddress)
	if err != nil {
		return nil, fmt.Errorf("split requested UDP source %q: %w", requestedAddress, err)
	}
	if strings.TrimSpace(requestedHost) == "" {
		return nil, errors.New("requested UDP source host is empty")
	}
	requestedPort, err := strconv.Atoi(requestedPortText)
	if err != nil || requestedPort < 0 || requestedPort > 65535 {
		return nil, fmt.Errorf("invalid requested UDP source port %q", requestedPortText)
	}
	requestedIP := net.ParseIP(requestedHost)
	if requestedIP == nil {
		resolved, resolveErr := net.LookupIP(requestedHost)
		if resolveErr != nil {
			return nil, fmt.Errorf("resolve requested UDP source host %q: %w", requestedHost, resolveErr)
		}
		for _, candidate := range resolved {
			if candidate.Equal(remoteIP) {
				requestedIP = remoteIP
				break
			}
		}
		if requestedIP == nil {
			return nil, fmt.Errorf("requested UDP source host %q does not resolve to TCP control peer %s", requestedHost, remoteIP)
		}
	}
	if !requestedIP.IsUnspecified() && !requestedIP.Equal(remoteIP) {
		return nil, fmt.Errorf("requested UDP source %s does not match TCP control peer %s", requestedIP, remoteIP)
	}
	return &udpAssociationClient{ip: append(net.IP(nil), remoteIP...), port: requestedPort}, nil
}

func (client *udpAssociationClient) RelayListenAddress() (string, string) {
	if client.ip.To4() != nil {
		return "udp4", "127.0.0.1:0"
	}
	return "udp6", "[::1]:0"
}

func (client *udpAssociationClient) Accept(address net.Addr) bool {
	udpAddress, ok := address.(*net.UDPAddr)
	if !ok || !udpAddress.IP.Equal(client.ip) {
		return false
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.port == 0 {
		client.port = udpAddress.Port
	}
	return client.port == udpAddress.Port
}

func (client *udpAssociationClient) Addr() net.Addr {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.port == 0 {
		return nil
	}
	return &net.UDPAddr{IP: append(net.IP(nil), client.ip...), Port: client.port}
}

func (client *udpAssociationClient) SourceAddr() string {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.port == 0 {
		return ""
	}
	return net.JoinHostPort(client.ip.String(), strconv.Itoa(client.port))
}

func monitorUDPControl(ctx context.Context, client net.Conn) {
	buffer := make([]byte, 1)
	for {
		if err := client.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			return
		}
		if _, err := client.Read(buffer); err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				if ctx.Err() != nil {
					return
				}
				continue
			}
			return
		}
	}
}

func decodeSOCKSUDPDatagram(frame []byte) (string, []byte, error) {
	if len(frame) < 4 || frame[0] != 0 || frame[1] != 0 {
		return "", nil, errors.New("invalid SOCKS UDP reserved bytes")
	}
	if frame[2] != 0 {
		return "", nil, fmt.Errorf("fragmented SOCKS UDP datagram FRAG=%d", frame[2])
	}
	host, offset, err := decodeSOCKSAddress(frame, 3)
	if err != nil {
		return "", nil, err
	}
	if len(frame) < offset+2 {
		return "", nil, io.ErrUnexpectedEOF
	}
	port := binary.BigEndian.Uint16(frame[offset : offset+2])
	return net.JoinHostPort(host, strconv.Itoa(int(port))), append([]byte(nil), frame[offset+2:]...), nil
}

func decodeSOCKSAddress(frame []byte, offset int) (string, int, error) {
	if len(frame) <= offset {
		return "", 0, io.ErrUnexpectedEOF
	}
	switch frame[offset] {
	case socksAtypIPv4:
		if len(frame) < offset+1+net.IPv4len {
			return "", 0, io.ErrUnexpectedEOF
		}
		return net.IP(frame[offset+1 : offset+1+net.IPv4len]).String(), offset + 1 + net.IPv4len, nil
	case socksAtypIPv6:
		if len(frame) < offset+1+net.IPv6len {
			return "", 0, io.ErrUnexpectedEOF
		}
		return net.IP(frame[offset+1 : offset+1+net.IPv6len]).String(), offset + 1 + net.IPv6len, nil
	case socksAtypDomainName:
		if len(frame) <= offset+1 {
			return "", 0, io.ErrUnexpectedEOF
		}
		length := int(frame[offset+1])
		if length == 0 {
			return "", 0, errors.New("SOCKS UDP domain name is empty")
		}
		if len(frame) < offset+2+length {
			return "", 0, io.ErrUnexpectedEOF
		}
		return string(frame[offset+2 : offset+2+length]), offset + 2 + length, nil
	default:
		return "", 0, fmt.Errorf("unsupported SOCKS UDP address type %d", frame[offset])
	}
}

func encodeSOCKSUDPDatagram(source string, payload []byte) ([]byte, error) {
	host, portText, err := net.SplitHostPort(source)
	if err != nil {
		return nil, fmt.Errorf("split UDP source %q: %w", source, err)
	}
	if strings.TrimSpace(host) == "" {
		return nil, errors.New("UDP source host is empty")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 0 || port > 65535 {
		return nil, fmt.Errorf("invalid UDP source port %q", portText)
	}
	var address []byte
	if ip := net.ParseIP(host); ip != nil {
		if ipv4 := ip.To4(); ipv4 != nil {
			address = append([]byte{socksAtypIPv4}, ipv4...)
		} else {
			address = append([]byte{socksAtypIPv6}, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return nil, fmt.Errorf("UDP source host too long: %d", len(host))
		}
		address = append([]byte{socksAtypDomainName, byte(len(host))}, host...)
	}
	frame := []byte{0, 0, 0}
	frame = append(frame, address...)
	frame = binary.BigEndian.AppendUint16(frame, uint16(port))
	return append(frame, payload...), nil
}

type packetAddress struct {
	network string
	address string
}

func (a packetAddress) Network() string { return a.network }
func (a packetAddress) String() string  { return a.address }

func packetDestinationAddr(address string) net.Addr {
	if host, portText, err := net.SplitHostPort(address); err == nil {
		if ip := net.ParseIP(host); ip != nil {
			if port, err := strconv.Atoi(portText); err == nil {
				return &net.UDPAddr{IP: ip, Port: port}
			}
		}
	}
	return packetAddress{network: "udp", address: address}
}

func samePacketAddr(left, right net.Addr) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Network() == right.Network() && left.String() == right.String()
}

func sanitizeFlowPart(value string) string {
	value = strings.NewReplacer("/", "_", ":", "_", "[", "_", "]", "_").Replace(value)
	if value == "" {
		return "client"
	}
	return value
}
