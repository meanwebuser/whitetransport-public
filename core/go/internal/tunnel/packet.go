package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/session"
)

const (
	packetReadInterval              = 500 * time.Millisecond
	packetOpenTimeout               = 3 * time.Second
	packetCarrierWriteTimeout       = 2 * time.Second
	packetMaxDatagramSize           = 4096
	packetMaxAssociationsPerSession = 64
	packetMaxFlowsPerSession        = 1024
	packetDefaultSessionTTL         = 2 * time.Minute
)

type packetOpenPayload struct {
	FlowID     string    `json:"flow_id"`
	SessionID  string    `json:"session_id"`
	SourceAddr string    `json:"source_addr,omitempty"`
	ExpiresAt  time.Time `json:"expires_at,omitempty"`
}

type packetStatusPayload struct {
	Error string `json:"error,omitempty"`
}

type packetDataPayload struct {
	DestinationAddr string `json:"destination_addr,omitempty"`
	SourceAddr      string `json:"source_addr,omitempty"`
	Payload         []byte `json:"payload"`
}

type packetSessionState struct {
	peerID    string
	expiresAt time.Time
	seenFlows map[string]struct{}
	closing   bool
}

// packetClientReader owns the single mailbox cursor shared by all client-side
// packet associations on one endpoint. A reader per association would multiply
// provider polling linearly with the number of UDP flows.
type packetClientReader struct {
	ctx     context.Context
	binding policy.CarrierBinding
	cancel  context.CancelFunc
}

type packetServer struct {
	ctx       context.Context
	conns     map[string]net.PacketConn
	binding   policy.CarrierBinding
	peer      string
	metadata  packetOpenPayload
	cancel    context.CancelFunc
	closeOnce sync.Once

	clientSequence uint64
	serverSequence uint64
	sourceAddr     string
}

// SetPacketSession registers the only peer allowed to open packet flows for a
// session. The binding is process-local and is never taken from packet data.
func (t *CarrierTunnel) SetPacketSession(sessionID string, peerID string, expiresAt time.Time) {
	sessionID = strings.TrimSpace(sessionID)
	peerID = strings.TrimSpace(peerID)
	if sessionID == "" || peerID == "" {
		return
	}
	if expiresAt.IsZero() {
		expiresAt = time.Now().UTC().Add(packetDefaultSessionTTL)
	}
	t.mu.Lock()
	previousSessions := make([]string, 0, len(t.packetSessions))
	for previousSessionID := range t.packetSessions {
		previousSessions = append(previousSessions, previousSessionID)
	}
	t.mu.Unlock()
	for _, previousSessionID := range previousSessions {
		t.ClosePacketSession(previousSessionID)
	}
	t.mu.Lock()
	t.packetSessions[sessionID] = &packetSessionState{peerID: peerID, expiresAt: expiresAt, seenFlows: make(map[string]struct{})}
	t.mu.Unlock()
}

// ClosePacketSession closes client and node packet associations while the
// caller still owns the session cipher. Repeated calls are safe.
func (t *CarrierTunnel) ClosePacketSession(sessionID string) {
	t.mu.Lock()
	state, exists := t.packetSessions[sessionID]
	if exists {
		state.closing = true
	}
	clients := make([]*carrierPacketConn, 0)
	for _, packet := range t.clientPackets {
		if packet.metadata.SessionID == sessionID {
			clients = append(clients, packet)
		}
	}
	servers := make([]*packetServer, 0)
	for flowID, packet := range t.activePackets {
		if packet.metadata.SessionID == sessionID {
			delete(t.activePackets, flowID)
			servers = append(servers, packet)
		}
	}
	delete(t.packetSessions, sessionID)
	t.mu.Unlock()
	for _, packet := range clients {
		packet.shutdown(true)
	}
	for _, packet := range servers {
		packet.shutdown()
	}
}

// OpenPacketConn opens an encrypted datagram association over the selected
// carrier. It waits for an authenticated node acknowledgement so policy
// failures cannot masquerade as a usable UDP route.
func (t *CarrierTunnel) OpenPacketConn(ctx context.Context, endpoint carriers.Endpoint, metadata session.PacketMetadata) (net.PacketConn, error) {
	if !t.SupportsPacketEndpoint(endpoint) {
		return nil, fmt.Errorf("carrier packet egress: endpoint %s is not packet-capable", endpoint.ID)
	}
	binding, ok := t.bindingForEndpoint(endpoint)
	if !ok {
		return nil, fmt.Errorf("carrier packet egress: no binding for %s", endpoint.Carrier)
	}
	if t.getCipher() == nil {
		return nil, errors.New("carrier packet egress: encrypted session cipher is unavailable")
	}
	flowID := strings.TrimSpace(metadata.FlowID)
	if flowID == "" {
		flowID = fmt.Sprintf("pkt-%s-%d", t.identity, time.Now().UnixNano())
	}
	metadata.FlowID = flowID
	metadata.SessionID = strings.TrimSpace(metadata.SessionID)
	metadata.PeerID = strings.TrimSpace(metadata.PeerID)
	if metadata.SessionID == "" || metadata.PeerID == "" {
		return nil, errors.New("carrier packet egress: session and expected peer are required")
	}

	t.mu.Lock()
	state, sessionExists := t.packetSessions[metadata.SessionID]
	if !sessionExists || state.closing || state.peerID != metadata.PeerID || packetSessionExpired(state.expiresAt) {
		t.mu.Unlock()
		return nil, errors.New("carrier packet egress: packet session is not active for the expected peer")
	}
	if _, duplicate := t.clientPackets[flowID]; duplicate {
		t.mu.Unlock()
		return nil, fmt.Errorf("carrier packet egress: duplicate flow %s", flowID)
	}
	if _, replayed := state.seenFlows[flowID]; replayed {
		t.mu.Unlock()
		return nil, fmt.Errorf("carrier packet egress: flow %s was already used in this session", flowID)
	}
	if len(state.seenFlows) >= packetMaxFlowsPerSession {
		t.mu.Unlock()
		return nil, fmt.Errorf("carrier packet egress: session flow history quota %d exceeded", packetMaxFlowsPerSession)
	}
	if packetClientCountLocked(t, metadata.SessionID) >= packetMaxAssociationsPerSession {
		t.mu.Unlock()
		return nil, fmt.Errorf("carrier packet egress: session association quota %d exceeded", packetMaxAssociationsPerSession)
	}
	metadata.ExpiresAt = state.expiresAt
	state.seenFlows[flowID] = struct{}{}
	packetCtx, cancel := packetContext(ctx, state.expiresAt)
	conn := &carrierPacketConn{
		parent:       t,
		id:           flowID,
		binding:      binding,
		metadata:     metadata,
		ctx:          packetCtx,
		cancel:       cancel,
		incoming:     make(chan packetDatagram, 32),
		done:         make(chan struct{}),
		ready:        make(chan error, 1),
		local:        packetAddress{network: "white/packet", address: flowID},
		sendSequence: 1,
	}
	t.clientPackets[flowID] = conn
	reader, startReader := t.ensurePacketReaderLocked(binding)
	t.mu.Unlock()

	if startReader {
		go t.packetReadLoop(reader)
	}
	go conn.watchContext()
	open := packetOpenPayload{FlowID: flowID, SessionID: metadata.SessionID, SourceAddr: metadata.SourceAddr, ExpiresAt: metadata.ExpiresAt}
	payload, err := json.Marshal(open)
	if err != nil {
		conn.shutdown(false)
		return nil, fmt.Errorf("carrier packet egress: marshal open: %w", err)
	}
	envelope := fabric.NewEnvelope(flowID, fabric.TrafficEgress, packetOpen, payload)
	envelope.Source = t.identity
	envelope.Destination = metadata.PeerID
	envelope.SessionID = metadata.SessionID
	envelope.Sequence = 1
	if err := t.writeEncryptedEnvelope(packetCtx, binding.Carrier, binding.Endpoint, envelope); err != nil {
		conn.shutdown(false)
		return nil, fmt.Errorf("carrier packet egress: write open: %w", err)
	}

	timer := time.NewTimer(packetOpenTimeout)
	defer timer.Stop()
	select {
	case readyErr := <-conn.ready:
		if readyErr != nil {
			conn.shutdown(false)
			return nil, readyErr
		}
		return conn, nil
	case <-packetCtx.Done():
		conn.shutdown(false)
		return nil, fmt.Errorf("carrier packet egress: open cancelled: %w", packetCtx.Err())
	case <-timer.C:
		conn.shutdown(true)
		return nil, errors.New("carrier packet egress: timed out waiting for node acknowledgement")
	}
}

type packetDatagram struct {
	payload []byte
	addr    net.Addr
}

type carrierPacketConn struct {
	parent   *CarrierTunnel
	id       string
	binding  policy.CarrierBinding
	metadata session.PacketMetadata
	ctx      context.Context
	cancel   context.CancelFunc
	incoming chan packetDatagram
	done     chan struct{}
	ready    chan error
	local    net.Addr

	mu              sync.Mutex
	readDeadline    time.Time
	writeDeadline   time.Time
	sourceAddr      string
	sendSequence    uint64
	receiveSequence uint64
	closeErr        error
	closeOnce       sync.Once
	readyOnce       sync.Once
}

var _ net.PacketConn = (*carrierPacketConn)(nil)
var _ session.PacketEgress = (*CarrierTunnel)(nil)
var _ session.PacketSessionLifecycle = (*CarrierTunnel)(nil)
var _ session.PacketSourceUpdater = (*carrierPacketConn)(nil)

func (c *carrierPacketConn) ReadFrom(buffer []byte) (int, net.Addr, error) {
	deadline := c.getReadDeadline()
	var timer *time.Timer
	if !deadline.IsZero() {
		duration := time.Until(deadline)
		if duration <= 0 {
			return 0, nil, timeoutError{}
		}
		timer = time.NewTimer(duration)
		defer timer.Stop()
	}
	select {
	case datagram := <-c.incoming:
		n := copy(buffer, datagram.payload)
		if n < len(datagram.payload) {
			return n, datagram.addr, ioErrMessageTooLong{}
		}
		return n, datagram.addr, nil
	case <-c.done:
		return 0, nil, net.ErrClosed
	case <-timerChan(timer):
		return 0, nil, timeoutError{}
	}
}

func (c *carrierPacketConn) WriteTo(payload []byte, addr net.Addr) (int, error) {
	if addr == nil {
		return 0, errors.New("carrier packet egress: destination address is required")
	}
	if len(payload) > maxPacketPayloadForBinding(c.binding) {
		return 0, fmt.Errorf("carrier packet egress: datagram size %d exceeds carrier limit", len(payload))
	}
	select {
	case <-c.done:
		return 0, net.ErrClosed
	default:
	}
	deadline := c.getWriteDeadline()
	if !deadline.IsZero() && !time.Now().Before(deadline) {
		return 0, timeoutError{}
	}
	c.mu.Lock()
	c.sendSequence++
	sequence := c.sendSequence
	sourceAddr := c.sourceAddr
	if sourceAddr == "" {
		sourceAddr = c.metadata.SourceAddr
	}
	c.mu.Unlock()
	data, err := json.Marshal(packetDataPayload{DestinationAddr: addr.String(), SourceAddr: sourceAddr, Payload: append([]byte(nil), payload...)})
	if err != nil {
		return 0, fmt.Errorf("carrier packet egress: marshal data: %w", err)
	}
	envelope := fabric.NewEnvelope(c.id, fabric.TrafficEgress, packetData, data)
	envelope.Source = c.parent.identity
	envelope.Destination = c.metadata.PeerID
	envelope.SessionID = c.metadata.SessionID
	envelope.Sequence = sequence
	writeCtx, cancel := packetWriteContext(c.ctx, deadline)
	defer cancel()
	if err := c.parent.writeEncryptedEnvelope(writeCtx, c.binding.Carrier, c.binding.Endpoint, envelope); err != nil {
		return 0, fmt.Errorf("carrier packet egress: write data: %w", err)
	}
	return len(payload), nil
}

func (c *carrierPacketConn) Close() error {
	c.shutdown(true)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeErr
}

func (c *carrierPacketConn) shutdown(notify bool) {
	c.closeOnce.Do(func() {
		if notify {
			c.mu.Lock()
			c.sendSequence++
			sequence := c.sendSequence
			c.mu.Unlock()
			envelope := fabric.NewEnvelope(c.id, fabric.TrafficEgress, packetClose, nil)
			envelope.Source = c.parent.identity
			envelope.Destination = c.metadata.PeerID
			envelope.SessionID = c.metadata.SessionID
			envelope.Sequence = sequence
			closeCtx, cancel := context.WithTimeout(context.Background(), packetCarrierWriteTimeout)
			err := c.parent.writeEncryptedEnvelope(closeCtx, c.binding.Carrier, c.binding.Endpoint, envelope)
			cancel()
			if err != nil {
				c.mu.Lock()
				c.closeErr = err
				c.mu.Unlock()
			}
		}
		c.cancel()
		close(c.done)
		c.parent.unregisterClientPacket(c)
	})
}

func (c *carrierPacketConn) signalReady(err error) {
	c.readyOnce.Do(func() { c.ready <- err })
}

func (c *carrierPacketConn) LocalAddr() net.Addr { return c.local }

func (c *carrierPacketConn) SetDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.readDeadline = deadline
	c.writeDeadline = deadline
	c.mu.Unlock()
	return nil
}

func (c *carrierPacketConn) SetReadDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.readDeadline = deadline
	c.mu.Unlock()
	return nil
}

func (c *carrierPacketConn) SetWriteDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.writeDeadline = deadline
	c.mu.Unlock()
	return nil
}

func (c *carrierPacketConn) SetPacketSource(sourceAddr string) {
	c.mu.Lock()
	c.sourceAddr = strings.TrimSpace(sourceAddr)
	c.mu.Unlock()
}

func (c *carrierPacketConn) getReadDeadline() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.readDeadline
}

func (c *carrierPacketConn) getWriteDeadline() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeDeadline
}

func (c *carrierPacketConn) acceptSequence(sequence uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if sequence == 0 || sequence <= c.receiveSequence {
		return false
	}
	c.receiveSequence = sequence
	return true
}

func (c *carrierPacketConn) watchContext() {
	select {
	case <-c.ctx.Done():
		c.shutdown(false)
	case <-c.done:
	}
}

func (c *carrierPacketConn) handlePacketEnvelope(envelope fabric.Envelope) {
	if envelope.ID != c.id || envelope.Source != c.metadata.PeerID || envelope.Destination != c.parent.identity || envelope.SessionID != c.metadata.SessionID {
		return
	}
	if !isPacketEnvelopeType(envelope.PayloadType) || !c.acceptSequence(envelope.Sequence) {
		return
	}
	switch envelope.PayloadType {
	case packetReady:
		var status packetStatusPayload
		if err := json.Unmarshal(envelope.Payload, &status); err != nil {
			c.signalReady(fmt.Errorf("carrier packet egress: invalid node acknowledgement: %w", err))
			return
		}
		if status.Error != "" {
			c.signalReady(fmt.Errorf("carrier packet egress: node rejected association: %s", status.Error))
			return
		}
		c.signalReady(nil)
	case packetClose:
		c.shutdown(false)
	case packetData:
		var data packetDataPayload
		if err := json.Unmarshal(envelope.Payload, &data); err != nil || len(data.Payload) > packetMaxDatagramSize {
			return
		}
		// UDP is lossy by contract. Never let one unread association stall the
		// shared endpoint reader and therefore every other packet flow.
		select {
		case c.incoming <- packetDatagram{payload: append([]byte(nil), data.Payload...), addr: packetNetAddr(data.SourceAddr)}:
		case <-c.done:
		default:
		}
	}
}

func (t *CarrierTunnel) ensurePacketReaderLocked(binding policy.CarrierBinding) (*packetClientReader, bool) {
	key := physicalBindingKey(binding)
	if reader, exists := t.packetReaders[key]; exists {
		return reader, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	reader := &packetClientReader{ctx: ctx, binding: binding, cancel: cancel}
	t.packetReaders[key] = reader
	return reader, true
}

func (t *CarrierTunnel) packetReadLoop(reader *packetClientReader) {
	cursor := carriers.Cursor("")
	pollInterval := boundedCarrierPollInterval(reader.binding, packetReadInterval)
	for {
		if reader.ctx.Err() != nil {
			return
		}
		read, err := reader.binding.Carrier.Read(reader.ctx, reader.binding.Endpoint, cursor)
		if err == nil {
			cursor = read.Cursor
			for _, raw := range read.Envelopes {
				if raw.PayloadType != "encrypted" {
					continue
				}
				envelope, decryptErr := t.readEnvelope(raw)
				if decryptErr != nil {
					continue
				}
				t.mu.Lock()
				packet := t.clientPackets[envelope.ID]
				t.mu.Unlock()
				if packet != nil && physicalBindingKey(packet.binding) == physicalBindingKey(reader.binding) {
					packet.handlePacketEnvelope(envelope)
				}
			}
		} else if reader.ctx.Err() != nil {
			return
		}

		timer := time.NewTimer(pollInterval)
		select {
		case <-reader.ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

func (t *CarrierTunnel) unregisterClientPacket(packet *carrierPacketConn) {
	key := physicalBindingKey(packet.binding)
	var cancel context.CancelFunc
	t.mu.Lock()
	if current := t.clientPackets[packet.id]; current == packet {
		delete(t.clientPackets, packet.id)
	}
	if reader := t.packetReaders[key]; reader != nil && !packetReaderInUseLocked(t, key) {
		delete(t.packetReaders, key)
		cancel = reader.cancel
	}
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func packetReaderInUseLocked(t *CarrierTunnel, key string) bool {
	for _, packet := range t.clientPackets {
		if physicalBindingKey(packet.binding) == key {
			return true
		}
	}
	return false
}

func (t *CarrierTunnel) handlePacketOpen(ctx context.Context, binding policy.CarrierBinding, envelope fabric.Envelope) {
	var metadata packetOpenPayload
	if err := json.Unmarshal(envelope.Payload, &metadata); err != nil || metadata.FlowID == "" || metadata.FlowID != envelope.ID || metadata.SessionID != envelope.SessionID || envelope.Sequence == 0 || !validPacketSourceAddr(metadata.SourceAddr, true) {
		return
	}

	t.mu.Lock()
	state, ok := t.packetSessions[envelope.SessionID]
	if !ok || state.closing || packetSessionExpired(state.expiresAt) || state.peerID != envelope.Source || envelope.Destination != t.identity {
		t.mu.Unlock()
		return
	}
	if _, replayed := state.seenFlows[envelope.ID]; replayed {
		t.mu.Unlock()
		return
	}
	if len(state.seenFlows) >= packetMaxFlowsPerSession {
		t.mu.Unlock()
		t.sendPacketStatus(ctx, binding, envelope.ID, envelope.SessionID, envelope.Source, "session flow history quota exceeded")
		return
	}
	state.seenFlows[envelope.ID] = struct{}{}
	if packetServerCountLocked(t, envelope.SessionID) >= packetMaxAssociationsPerSession {
		t.mu.Unlock()
		t.sendPacketStatus(ctx, binding, envelope.ID, envelope.SessionID, envelope.Source, "session association quota exceeded")
		return
	}
	proxyURL := strings.TrimSpace(t.proxyURL)
	t.mu.Unlock()
	if proxyURL != "" {
		t.sendPacketStatus(ctx, binding, envelope.ID, envelope.SessionID, envelope.Source, "UDP egress through the configured upstream proxy is unsupported")
		return
	}

	packetCtx, cancel := packetContext(ctx, state.expiresAt)
	server := &packetServer{
		ctx:            packetCtx,
		conns:          make(map[string]net.PacketConn),
		binding:        binding,
		peer:           envelope.Source,
		metadata:       metadata,
		cancel:         cancel,
		clientSequence: envelope.Sequence,
		serverSequence: 1,
		sourceAddr:     metadata.SourceAddr,
	}
	listenErrors := make([]error, 0, 2)
	if conn, err := net.ListenPacket("udp4", "0.0.0.0:0"); err == nil {
		server.conns["udp4"] = conn
	} else {
		listenErrors = append(listenErrors, err)
	}
	if conn, err := net.ListenPacket("udp6", "[::]:0"); err == nil {
		server.conns["udp6"] = conn
	} else {
		listenErrors = append(listenErrors, err)
	}
	if len(server.conns) == 0 {
		cancel()
		t.sendPacketStatus(ctx, binding, envelope.ID, envelope.SessionID, envelope.Source, fmt.Sprintf("open UDP exit sockets: %v", errors.Join(listenErrors...)))
		return
	}

	t.mu.Lock()
	current, stillActive := t.packetSessions[envelope.SessionID]
	quotaExceeded := packetServerCountLocked(t, envelope.SessionID) >= packetMaxAssociationsPerSession
	proxyConfigured := strings.TrimSpace(t.proxyURL) != ""
	if !stillActive || current != state || current.closing || packetSessionExpired(current.expiresAt) || quotaExceeded || proxyConfigured {
		t.mu.Unlock()
		server.shutdown()
		switch {
		case quotaExceeded:
			t.sendPacketStatus(ctx, binding, envelope.ID, envelope.SessionID, envelope.Source, "session association quota exceeded")
		case proxyConfigured:
			t.sendPacketStatus(ctx, binding, envelope.ID, envelope.SessionID, envelope.Source, "UDP egress through the configured upstream proxy is unsupported")
		}
		return
	}
	t.activePackets[envelope.ID] = server
	onActive := t.onActive
	t.mu.Unlock()
	if onActive != nil {
		onActive()
	}
	if !t.sendPacketStatus(ctx, binding, envelope.ID, envelope.SessionID, envelope.Source, "") {
		t.handlePacketClose(envelope.ID)
		return
	}
	for _, conn := range server.conns {
		go t.packetServerReader(packetCtx, envelope.ID, server, conn)
	}
	go func() {
		<-packetCtx.Done()
		t.handlePacketClose(envelope.ID)
	}()
}

func (t *CarrierTunnel) sendPacketStatus(ctx context.Context, binding policy.CarrierBinding, flowID string, sessionID string, peerID string, message string) bool {
	payload, err := json.Marshal(packetStatusPayload{Error: message})
	if err != nil {
		return false
	}
	envelope := fabric.NewEnvelope(flowID, fabric.TrafficEgress, packetReady, payload)
	envelope.Source = t.identity
	envelope.Destination = peerID
	envelope.SessionID = sessionID
	envelope.Sequence = 1
	writeCtx, cancel := packetWriteContext(ctx, time.Time{})
	defer cancel()
	return t.writeEncryptedEnvelope(writeCtx, binding.Carrier, binding.Endpoint, envelope) == nil
}

func (t *CarrierTunnel) handlePacketData(envelope fabric.Envelope) {
	var data packetDataPayload
	if err := json.Unmarshal(envelope.Payload, &data); err != nil || data.DestinationAddr == "" || len(data.Payload) > packetMaxDatagramSize {
		return
	}
	t.mu.Lock()
	server, ok := t.activePackets[envelope.ID]
	if !ok || !t.validPacketServerEnvelopeLocked(server, envelope) {
		t.mu.Unlock()
		return
	}
	if !validPacketSourceAddr(data.SourceAddr, true) {
		t.mu.Unlock()
		return
	}
	if server.sourceAddr == "" {
		server.sourceAddr = data.SourceAddr
	} else if data.SourceAddr != server.sourceAddr {
		t.mu.Unlock()
		return
	}
	server.clientSequence = envelope.Sequence
	t.mu.Unlock()

	resolveCtx, resolveCancel := packetWriteContext(server.ctx, time.Time{})
	resolver := t.packetResolver
	if resolver == nil {
		resolver = resolvePacketAddr
	}
	destination, err := resolver(resolveCtx, data.DestinationAddr)
	resolveCancel()
	if err != nil {
		t.handlePacketClose(envelope.ID)
		return
	}
	if packetDestinationUsesDomain(data.DestinationAddr) {
		t.mu.Lock()
		t.packetExitDomainResolutions++
		resolutionCount := t.packetExitDomainResolutions
		t.mu.Unlock()
		log.Printf("[tunnel] packet exit resolver success flow=%s count=%d", envelope.ID, resolutionCount)
	}
	family := "udp6"
	if destination.IP.To4() != nil {
		family = "udp4"
	}
	conn := server.conns[family]
	if conn == nil {
		t.handlePacketClose(envelope.ID)
		return
	}
	if err := conn.SetWriteDeadline(time.Now().Add(packetCarrierWriteTimeout)); err != nil {
		t.handlePacketClose(envelope.ID)
		return
	}
	if _, err := conn.WriteTo(data.Payload, destination); err != nil {
		t.handlePacketClose(envelope.ID)
	}
}

func (t *CarrierTunnel) handlePacketCloseEnvelope(envelope fabric.Envelope) {
	t.mu.Lock()
	server, ok := t.activePackets[envelope.ID]
	if !ok || !t.validPacketServerEnvelopeLocked(server, envelope) {
		t.mu.Unlock()
		return
	}
	server.clientSequence = envelope.Sequence
	t.mu.Unlock()
	t.handlePacketClose(envelope.ID)
}

func (t *CarrierTunnel) validPacketServerEnvelopeLocked(server *packetServer, envelope fabric.Envelope) bool {
	state, ok := t.packetSessions[server.metadata.SessionID]
	return ok && !state.closing && !packetSessionExpired(state.expiresAt) &&
		envelope.SessionID == server.metadata.SessionID && envelope.Source == server.peer &&
		envelope.Destination == t.identity && envelope.Sequence > server.clientSequence
}

func (t *CarrierTunnel) handlePacketClose(flowID string) {
	t.mu.Lock()
	server, ok := t.activePackets[flowID]
	delete(t.activePackets, flowID)
	onIdle := t.onIdle
	idle := len(t.activePackets) == 0 && len(t.active) == 0
	t.mu.Unlock()
	if !ok {
		return
	}
	server.shutdown()
	if idle && onIdle != nil {
		onIdle()
	}
}

func (server *packetServer) shutdown() {
	server.closeOnce.Do(func() {
		server.cancel()
		for _, conn := range server.conns {
			_ = conn.Close()
		}
	})
}

func (t *CarrierTunnel) packetServerReader(ctx context.Context, flowID string, server *packetServer, conn net.PacketConn) {
	buffer := make([]byte, packetMaxDatagramSize+1)
	for {
		if err := conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			return
		}
		n, addr, err := conn.ReadFrom(buffer)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				if ctx.Err() != nil {
					return
				}
				continue
			}
			return
		}
		if n > packetMaxDatagramSize {
			continue
		}
		payload, err := json.Marshal(packetDataPayload{SourceAddr: addr.String(), Payload: append([]byte(nil), buffer[:n]...)})
		if err != nil {
			continue
		}
		t.mu.Lock()
		current, ok := t.activePackets[flowID]
		state, sessionOK := t.packetSessions[server.metadata.SessionID]
		if !ok || current != server || !sessionOK || state.closing || packetSessionExpired(state.expiresAt) {
			t.mu.Unlock()
			return
		}
		server.serverSequence++
		sequence := server.serverSequence
		t.mu.Unlock()
		envelope := fabric.NewEnvelope(flowID, fabric.TrafficEgress, packetData, payload)
		envelope.Source = t.identity
		envelope.Destination = server.peer
		envelope.SessionID = server.metadata.SessionID
		envelope.Sequence = sequence
		writeCtx, cancel := packetWriteContext(ctx, time.Time{})
		err = t.writeEncryptedEnvelope(writeCtx, server.binding.Carrier, server.binding.Endpoint, envelope)
		cancel()
		if err != nil {
			t.handlePacketClose(flowID)
			return
		}
	}
}

func packetContext(parent context.Context, expiresAt time.Time) (context.Context, context.CancelFunc) {
	if expiresAt.IsZero() {
		ctx, cancel := context.WithCancel(parent)
		cancel()
		return ctx, func() {}
	}
	return context.WithDeadline(parent, expiresAt)
}

func packetWriteContext(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	if deadline.IsZero() || time.Until(deadline) > packetCarrierWriteTimeout {
		deadline = time.Now().Add(packetCarrierWriteTimeout)
	}
	return context.WithDeadline(parent, deadline)
}

func packetSessionExpired(expiresAt time.Time) bool {
	return expiresAt.IsZero() || !time.Now().Before(expiresAt)
}

func packetClientCountLocked(tunnel *CarrierTunnel, sessionID string) int {
	count := 0
	for _, packet := range tunnel.clientPackets {
		if packet.metadata.SessionID == sessionID {
			count++
		}
	}
	return count
}

func packetServerCountLocked(tunnel *CarrierTunnel, sessionID string) int {
	count := 0
	for _, packet := range tunnel.activePackets {
		if packet.metadata.SessionID == sessionID {
			count++
		}
	}
	return count
}

func maxPacketPayloadForBinding(binding policy.CarrierBinding) int {
	limit := binding.Carrier.Descriptor().Limits.MaxPayloadBytes
	if limit <= 0 {
		return packetMaxDatagramSize
	}
	// Inner JSON and outer encrypted-envelope base64 framing amplify payloads.
	// Keep a conservative bound for small mailbox carriers.
	maxPayload := (limit - 1024) * 3 / 8
	if maxPayload > packetMaxDatagramSize {
		maxPayload = packetMaxDatagramSize
	}
	if maxPayload < 1 {
		return 0
	}
	return maxPayload
}

func isPacketEnvelopeType(payloadType string) bool {
	switch payloadType {
	case packetOpen, packetReady, packetData, packetClose:
		return true
	default:
		return false
	}
}

func packetNetAddr(address string) net.Addr {
	if host, portText, err := net.SplitHostPort(address); err == nil {
		if port, err := strconv.Atoi(portText); err == nil {
			if ip := net.ParseIP(host); ip != nil {
				return &net.UDPAddr{IP: ip, Port: port}
			}
		}
	}
	return packetAddress{network: "udp", address: address}
}

func resolvePacketAddr(ctx context.Context, address string) (*net.UDPAddr, error) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("split packet destination %q: %w", address, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 0 || port > 65535 {
		return nil, fmt.Errorf("invalid packet destination port %q", portText)
	}
	if strings.TrimSpace(host) == "" {
		return nil, errors.New("packet destination host is empty")
	}
	if ip := net.ParseIP(host); ip != nil {
		return &net.UDPAddr{IP: ip, Port: port}, nil
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve packet destination %q: %w", address, err)
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("resolve packet destination %q: no addresses", address)
	}
	return &net.UDPAddr{IP: addresses[0].IP, Zone: addresses[0].Zone, Port: port}, nil
}

func packetDestinationUsesDomain(address string) bool {
	host, _, err := net.SplitHostPort(address)
	return err == nil && strings.TrimSpace(host) != "" && net.ParseIP(host) == nil
}

func validPacketSourceAddr(address string, allowEmpty bool) bool {
	address = strings.TrimSpace(address)
	if address == "" {
		return allowEmpty
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil || net.ParseIP(host) == nil {
		return false
	}
	port, err := strconv.Atoi(portText)
	return err == nil && port > 0 && port <= 65535
}

type packetAddress struct {
	network string
	address string
}

func (a packetAddress) Network() string { return a.network }
func (a packetAddress) String() string  { return a.address }

type ioErrMessageTooLong struct{}

func (ioErrMessageTooLong) Error() string   { return "packet too large for caller buffer" }
func (ioErrMessageTooLong) Timeout() bool   { return false }
func (ioErrMessageTooLong) Temporary() bool { return false }

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func timerChan(timer *time.Timer) <-chan time.Time {
	if timer == nil {
		return nil
	}
	return timer.C
}
