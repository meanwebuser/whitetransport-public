package tunnel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
)

const (
	tunnelOpen  = "tunnel.open"
	tunnelData  = "tunnel.data"
	tunnelClose = "tunnel.close"
	packetOpen  = "tunnel.packet.open"
	packetReady = "tunnel.packet.ready"
	packetData  = "tunnel.packet.data"
	packetClose = "tunnel.packet.close"

	carrierPollInterval = 250 * time.Millisecond
	// egressReadInterval paces the per-tunnel response read loop. Stream
	// carriers (WBStream) block internally until data arrives, so this only
	// bounds busy-spin for immediate-return mailbox carriers; keep it small to
	// avoid adding latency to interactive/TLS traffic.
	egressReadInterval = 2 * time.Second
	keepaliveInterval  = 15 * time.Second
	// writeBufSize bounds the per-envelope payload read from the local pipe.
	// WBStream rides an SCTP DataChannel whose reliable max message size is
	// ~16 KiB; after JSON+base64 envelope framing the wire form is ~1.4x the
	// payload, so keep raw chunks well under that to avoid silently dropped
	// oversized messages. 8 KiB payload -> ~11 KiB wire, safely under the cap.
	writeBufSize = 8192
)

// CarrierTunnel implements session.CarrierTunnel by multiplexing TCP streams
// over carrier Write/Read (the same envelope adapters used for discovery and
// control). Client side: DialContext returns a net.Conn backed by carrier
// envelopes. Node side: ServeEgress runs the polling/forwarding loop.
type CarrierTunnel struct {
	identity string
	bindings map[string]policy.CarrierBinding
	proxyURL string // upstream HTTP proxy URL for node-side egress (empty = direct)

	mu             sync.Mutex
	active         map[string]*tunnelConn
	activePackets  map[string]*packetServer
	clientPackets  map[string]*carrierPacketConn
	packetReaders  map[string]*packetClientReader
	packetSessions map[string]*packetSessionState
	packetResolver func(context.Context, string) (*net.UDPAddr, error)
	// packetExitDomainResolutions is incremented only by the node-side packet
	// egress handler after a domain has been resolved successfully.
	packetExitDomainResolutions uint64
	sessionBindings             map[string]policy.CarrierBinding
	cipher                      *fabric.EnvelopeCipher
	onIdle                      func()
	onActive                    func()
}

type tunnelConn struct {
	tunnelID   string
	targetConn net.Conn
	buf        bytes.Buffer
	closed     bool
}

// NewCarrierTunnel builds a tunnel that writes/reads egress data through the
// same carrier adapters used for control messages.
func NewCarrierTunnel(identity string, bindings map[string]policy.CarrierBinding) *CarrierTunnel {
	return &CarrierTunnel{
		identity:        identity,
		bindings:        bindings,
		active:          make(map[string]*tunnelConn),
		activePackets:   make(map[string]*packetServer),
		clientPackets:   make(map[string]*carrierPacketConn),
		packetReaders:   make(map[string]*packetClientReader),
		packetSessions:  make(map[string]*packetSessionState),
		packetResolver:  resolvePacketAddr,
		sessionBindings: make(map[string]policy.CarrierBinding),
	}
}

func (t *CarrierTunnel) SupportsEndpoint(ep carriers.Endpoint) bool {
	if _, ok := t.bindingForEndpoint(ep); !ok {
		return false
	}
	return true
}

// SupportsPacketEndpoint reports whether an endpoint transports framed
// datagrams. It deliberately excludes stream-only outbounds such as
// sing-box.vless and ssh.tcp even though those endpoints support TCP egress.
func (t *CarrierTunnel) SupportsPacketEndpoint(ep carriers.Endpoint) bool {
	binding, ok := t.bindingForEndpoint(ep)
	if !ok {
		return false
	}
	descriptor := binding.Carrier.Descriptor()
	packetCapable := carriers.HasCapability(descriptor, carriers.CapDatagram) ||
		carriers.HasCapability(descriptor, carriers.CapMailbox) ||
		carriers.HasCapability(descriptor, carriers.CapBulk)
	if !packetCapable {
		return false
	}
	if maxPacketPayloadForBinding(binding) <= 0 {
		return false
	}
	for _, trafficClass := range append(append([]fabric.TrafficClass(nil), descriptor.TrafficClasses...), carriers.DeriveTrafficClasses(descriptor.Capabilities)...) {
		if trafficClass == fabric.TrafficEgress {
			return true
		}
	}
	return false
}

func (t *CarrierTunnel) SetSessionBinding(endpoint carriers.Endpoint, binding policy.CarrierBinding) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sessionBindings[endpoint.ID] = binding
}

func (t *CarrierTunnel) ClearSessionBinding(endpoint carriers.Endpoint) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.sessionBindings, endpoint.ID)
}

func (t *CarrierTunnel) SetOnIdle(fn func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.onIdle = fn
}

// SetOnActive registers a callback fired when a new tunnel is opened. The node
// uses it to cancel any pending idle session-release grace timer so a session
// serving many short-lived TCP tunnels is not torn down between connections.
func (t *CarrierTunnel) SetOnActive(fn func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.onActive = fn
}

// SetCipher enables AES-256-GCM encryption for all tunnel envelope writes/reads.
func (t *CarrierTunnel) SetCipher(cipher *fabric.EnvelopeCipher) {
	t.mu.Lock()
	current := t.cipher
	sessionIDs := make([]string, 0, len(t.packetSessions))
	if current != nil && current != cipher {
		for sessionID := range t.packetSessions {
			sessionIDs = append(sessionIDs, sessionID)
		}
	}
	t.mu.Unlock()
	for _, sessionID := range sessionIDs {
		t.ClosePacketSession(sessionID)
	}
	t.mu.Lock()
	t.cipher = cipher
	t.mu.Unlock()
}

// ClearCipher closes packet sessions before disabling envelope encryption so
// an association can never survive by silently downgrading to plaintext.
func (t *CarrierTunnel) ClearCipher() {
	t.mu.Lock()
	sessionIDs := make([]string, 0, len(t.packetSessions))
	for sessionID := range t.packetSessions {
		sessionIDs = append(sessionIDs, sessionID)
	}
	t.mu.Unlock()
	for _, sessionID := range sessionIDs {
		t.ClosePacketSession(sessionID)
	}
	t.mu.Lock()
	t.cipher = nil
	t.mu.Unlock()
}

// SetProxyURL configures an upstream HTTP proxy for node-side egress connections.
func (t *CarrierTunnel) SetProxyURL(proxyURL string) {
	t.mu.Lock()
	t.proxyURL = proxyURL
	sessionIDs := make([]string, 0)
	if strings.TrimSpace(proxyURL) != "" {
		for sessionID := range t.packetSessions {
			sessionIDs = append(sessionIDs, sessionID)
		}
	}
	t.mu.Unlock()
	for _, sessionID := range sessionIDs {
		t.ClosePacketSession(sessionID)
	}
}

func (t *CarrierTunnel) getCipher() *fabric.EnvelopeCipher {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cipher
}

// writeEnvelope writes an envelope, sealing it with the cipher if available.
func (t *CarrierTunnel) writeEnvelope(ctx context.Context, carrier carriers.Carrier, endpoint carriers.Endpoint, env fabric.Envelope) error {
	if c := t.getCipher(); c != nil {
		sealed, err := c.Seal(env)
		if err != nil {
			return fmt.Errorf("seal envelope: %w", err)
		}
		// Write sealed bytes as a raw envelope payload.
		raw := fabric.Envelope{
			Version:     1,
			ID:          env.ID,
			PayloadType: "encrypted",
			Source:      env.Source,
			Destination: env.Destination,
			Sequence:    env.Sequence,
			CreatedAt:   env.CreatedAt,
			Payload:     sealed,
		}
		return carrier.Write(ctx, endpoint, raw)
	}
	return carrier.Write(ctx, endpoint, env)
}

// writeEncryptedEnvelope is the fail-closed writer for session packet data.
// Packet traffic must never fall back to plaintext after session teardown.
func (t *CarrierTunnel) writeEncryptedEnvelope(ctx context.Context, carrier carriers.Carrier, endpoint carriers.Endpoint, env fabric.Envelope) error {
	cipher := t.getCipher()
	if cipher == nil {
		return errors.New("carrier packet egress: encrypted session cipher is unavailable")
	}
	sealed, err := cipher.Seal(env)
	if err != nil {
		return fmt.Errorf("seal packet envelope: %w", err)
	}
	raw := fabric.Envelope{
		Version:     1,
		ID:          env.ID,
		PayloadType: "encrypted",
		Source:      env.Source,
		Destination: env.Destination,
		Sequence:    env.Sequence,
		CreatedAt:   env.CreatedAt,
		Payload:     sealed,
	}
	return carrier.Write(ctx, endpoint, raw)
}

// readEnvelope reads and optionally decrypts an envelope from carrier read results.
func (t *CarrierTunnel) readEnvelope(env fabric.Envelope) (fabric.Envelope, error) {
	if env.PayloadType == "encrypted" {
		if c := t.getCipher(); c != nil {
			return c.Open(env.Payload)
		}
		return env, fmt.Errorf("encrypted envelope but no cipher available")
	}
	return env, nil
}

func (t *CarrierTunnel) bindingForEndpoint(ep carriers.Endpoint) (policy.CarrierBinding, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if binding, ok := t.sessionBindings[ep.ID]; ok {
		return binding, true
	}
	binding, ok := t.bindings[ep.Carrier]
	return binding, ok
}

// DialContext opens a tunnel to targetAddr through the carrier identified by
// ep. Returns a net.Conn that sends data as tunnel.data envelopes and receives
// response data by polling the carrier.
func (t *CarrierTunnel) DialContext(ctx context.Context, ep carriers.Endpoint, targetAddr string) (net.Conn, error) {
	binding, ok := t.bindingForEndpoint(ep)
	if !ok {
		return nil, fmt.Errorf("carrier tunnel: no binding for %s", ep.Carrier)
	}

	tunnelID := fmt.Sprintf("tun-%s-%d", t.identity, time.Now().UnixNano())

	openPayload, err := json.Marshal(tunnelOpenPayload{TargetAddr: targetAddr})
	if err != nil {
		return nil, fmt.Errorf("carrier tunnel: marshal open: %w", err)
	}
	openEnvelope := fabric.NewEnvelope(tunnelID, fabric.TrafficEgress, tunnelOpen, openPayload)
	openEnvelope.Source = t.identity
	if err := t.writeEnvelope(ctx, binding.Carrier, binding.Endpoint, openEnvelope); err != nil {
		return nil, fmt.Errorf("carrier tunnel: write open: %w", err)
	}
	log.Printf("[tunnel] dial open sent id=%s target=%s endpoint=%s", tunnelID, targetAddr, binding.Endpoint.ID)

	client, server := net.Pipe()
	go t.writeLoop(ctx, tunnelID, server, binding)
	go t.readLoop(ctx, tunnelID, server, binding)
	return client, nil
}

func (t *CarrierTunnel) writeLoop(ctx context.Context, tunnelID string, server net.Conn, binding policy.CarrierBinding) {
	defer server.Close()
	seq := uint64(1)
	buf := make([]byte, writeBufSize)
	lastWrite := time.Now()
	keepalive := time.NewTicker(keepaliveInterval)
	defer keepalive.Stop()
	for {
		// Use a deadline on the server read so we can check the keepalive ticker.
		if d, ok := server.(interface{ SetReadDeadline(time.Time) error }); ok {
			_ = d.SetReadDeadline(time.Now().Add(keepaliveInterval))
		}
		select {
		case <-keepalive.C:
			if time.Since(lastWrite) >= keepaliveInterval {
				keepaliveEnv := fabric.NewEnvelope(tunnelID, fabric.TrafficEgress, tunnelData, nil)
				keepaliveEnv.Source = t.identity
				keepaliveEnv.Sequence = 0 // zero seq marks keepalive
				if err := t.writeEnvelope(ctx, binding.Carrier, binding.Endpoint, keepaliveEnv); err != nil {
					return
				}
				lastWrite = time.Now()
			}
		default:
		}
		n, err := server.Read(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			log.Printf("[tunnel] writeLoop server.Read closing id=%s err=%v", tunnelID, err)
			closePayload, _ := json.Marshal(tunnelClosePayload{})
			closeEnv := fabric.NewEnvelope(tunnelID, fabric.TrafficEgress, tunnelClose, closePayload)
			closeEnv.Source = t.identity
			_ = t.writeEnvelope(ctx, binding.Carrier, binding.Endpoint, closeEnv)
			return
		}
		if n == 0 {
			continue
		}
		chunk := make([]byte, n)
		copy(chunk, buf[:n])
		pv := chunk
		if len(pv) > 24 {
			pv = pv[:24]
		}
		log.Printf("[tunnel] writeLoop send %d bytes id=%s preview=%x", n, tunnelID, pv)
		env := fabric.NewEnvelope(tunnelID, fabric.TrafficEgress, tunnelData, chunk)
		env.Source = t.identity
		env.Sequence = seq
		seq++
		if err := t.writeEnvelope(ctx, binding.Carrier, binding.Endpoint, env); err != nil {
			return
		}
		lastWrite = time.Now()
	}
}

func (t *CarrierTunnel) readLoop(ctx context.Context, tunnelID string, client net.Conn, binding policy.CarrierBinding) {
	defer client.Close()
	cursor := carriers.Cursor("")
	poll := time.NewTicker(egressReadInterval)
	defer poll.Stop()
	errBackoff := 0
	for {
		select {
		case <-ctx.Done():
			log.Printf("[tunnel] readLoop ctx done id=%s", tunnelID)
			return
		case <-poll.C:
			if errBackoff > 0 {
				errBackoff--
				continue
			}
			read, err := binding.Carrier.Read(ctx, binding.Endpoint, cursor)
			if err != nil {
				log.Printf("[tunnel] readLoop carrier read err id=%s err=%v", tunnelID, err)
				errBackoff = 3
				continue
			}
			errBackoff = 0
			cursor = read.Cursor
			for _, env := range read.Envelopes {
				log.Printf("[tunnel] readLoop env id=%s type=%s source=%s payload=%d", env.ID, env.PayloadType, env.Source, len(env.Payload))
				// Decrypt if the envelope is encrypted.
				decrypted, err := t.readEnvelope(env)
				if err != nil {
					log.Printf("[tunnel] readLoop decrypt fail id=%s err=%v", tunnelID, err)
					continue
				}
				env = decrypted
				if env.ID != tunnelID {
					continue
				}
				// Mailbox carriers expose locally-written envelopes on the same
				// poll stream. Forwarding one would prepend the HTTP request to its
				// response and corrupt the local SOCKS byte stream.
				if env.Source == t.identity {
					continue
				}
				// Skip keepalive envelopes (seq=0, nil/empty payload).
				if env.Sequence == 0 && len(env.Payload) == 0 {
					continue
				}
				if env.PayloadType == tunnelData {
					preview := env.Payload
					if len(preview) > 48 {
						preview = preview[:48]
					}
					log.Printf("[tunnel] readLoop received %d bytes id=%s source=%s preview=%q", len(env.Payload), tunnelID, env.Source, preview)
					if _, err := client.Write(env.Payload); err != nil {
						log.Printf("[tunnel] readLoop client.Write err id=%s err=%v", tunnelID, err)
						return
					}
				}
				if env.PayloadType != tunnelData && env.PayloadType != tunnelClose {
					log.Printf("[tunnel] readLoop skip payload type=%s id=%s", env.PayloadType, tunnelID)
				}
				if env.PayloadType == tunnelClose {
					return
				}
			}
		}
	}
}

// ServeEgress runs the node-side egress handler. Static and dynamic session
// aliases are reference-counted by physical provider/address identity so one
// mailbox never acquires duplicate readers or exceeds its advertised poll
// budget merely because a session installed another endpoint ID.
func (t *CarrierTunnel) ServeEgress(ctx context.Context, bindings map[string]policy.CarrierBinding) error {
	type egressRead struct {
		binding policy.CarrierBinding
		env     fabric.Envelope
	}
	type desiredReader struct {
		binding policy.CarrierBinding
		refs    int
	}
	type activeReader struct {
		cancel context.CancelFunc
		refs   int
	}

	reads := make(chan egressRead, 64)
	readerCtx, stopAllReaders := context.WithCancel(ctx)
	activeReaders := make(map[string]*activeReader)
	var readers sync.WaitGroup

	startReader := func(binding policy.CarrierBinding) context.CancelFunc {
		physicalCtx, cancel := context.WithCancel(readerCtx)
		readers.Add(1)
		go func() {
			defer readers.Done()
			cursor := carriers.Cursor("")
			pollInterval := boundedCarrierPollInterval(binding, carrierPollInterval)
			for {
				if physicalCtx.Err() != nil {
					return
				}
				read, err := binding.Carrier.Read(physicalCtx, binding.Endpoint, cursor)
				if err == nil {
					cursor = read.Cursor
					for _, envelope := range read.Envelopes {
						select {
						case reads <- egressRead{binding: binding, env: envelope}:
						case <-physicalCtx.Done():
							return
						}
					}
				} else if physicalCtx.Err() != nil {
					return
				}
				timer := time.NewTimer(pollInterval)
				select {
				case <-physicalCtx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}()
		return cancel
	}

	reconcileReaders := func() {
		desired := make(map[string]desiredReader)
		addBinding := func(binding policy.CarrierBinding) {
			if binding.Carrier == nil {
				return
			}
			key := physicalBindingKey(binding)
			entry := desired[key]
			if entry.refs == 0 {
				entry.binding = binding
			}
			entry.refs++
			desired[key] = entry
		}
		for _, binding := range bindings {
			addBinding(binding)
		}
		t.mu.Lock()
		for _, binding := range t.sessionBindings {
			addBinding(binding)
		}
		t.mu.Unlock()

		for key, wanted := range desired {
			if current := activeReaders[key]; current != nil {
				current.refs = wanted.refs
				continue
			}
			activeReaders[key] = &activeReader{cancel: startReader(wanted.binding), refs: wanted.refs}
		}
		for key, current := range activeReaders {
			if _, wanted := desired[key]; wanted {
				continue
			}
			current.cancel()
			delete(activeReaders, key)
		}
	}

	shutdownReaders := func() {
		stopAllReaders()
		for _, reader := range activeReaders {
			reader.cancel()
		}
		readers.Wait()
	}

	reconcileReaders()
	ticker := time.NewTicker(carrierPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			shutdownReaders()
			t.closeAll()
			return ctx.Err()
		case <-ticker.C:
			reconcileReaders()
		case read := <-reads:
			t.handleRawEgressEnvelope(ctx, read.binding, read.env)
		}
	}
}

func (t *CarrierTunnel) handleRawEgressEnvelope(ctx context.Context, binding policy.CarrierBinding, envelope fabric.Envelope) {
	// Recognizable packet types at the carrier boundary are plaintext and must
	// be rejected before dispatch. Encrypted envelopes reveal their type only
	// after authenticated decryption.
	if isPacketEnvelopeType(envelope.PayloadType) {
		return
	}
	decrypted, err := t.readEnvelope(envelope)
	if err != nil {
		return
	}
	t.processEgressEnvelope(ctx, binding, decrypted)
}

func (t *CarrierTunnel) processEgressEnvelope(ctx context.Context, binding policy.CarrierBinding, env fabric.Envelope) {
	// Skip envelopes we wrote ourselves — the node's poll loop would
	// otherwise try to forward its own response data back to targets.
	if env.Source == t.identity {
		return
	}
	// Skip keepalive envelopes (seq=0, nil/empty payload).
	if env.Sequence == 0 && len(env.Payload) == 0 {
		return
	}
	log.Printf("[tunnel] egress envelope id=%s type=%s source=%s payload_bytes=%d", env.ID, env.PayloadType, env.Source, len(env.Payload))
	switch env.PayloadType {
	case tunnelOpen:
		var payload tunnelOpenPayload
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			return
		}
		t.handleOpen(ctx, binding, env.ID, payload.TargetAddr, env.Source)
	case tunnelData:
		t.handleData(env.ID, env.Payload)
	case tunnelClose:
		t.handleClose(env.ID)
	case packetOpen:
		t.handlePacketOpen(ctx, binding, env)
	case packetData:
		t.handlePacketData(env)
	case packetClose:
		t.handlePacketCloseEnvelope(env)
	}
}

type tunnelOpenPayload struct {
	TargetAddr string `json:"target_addr"`
}

type tunnelClosePayload struct{}

func (t *CarrierTunnel) handleOpen(ctx context.Context, binding policy.CarrierBinding, tunnelID, targetAddr, source string) {
	log.Printf("[tunnel] handleOpen id=%s target=%s source=%s", tunnelID, targetAddr, source)
	t.mu.Lock()
	if _, exists := t.active[tunnelID]; exists {
		t.mu.Unlock()
		log.Printf("[tunnel] handleOpen duplicate id=%s", tunnelID)
		return
	}
	tc := &tunnelConn{tunnelID: tunnelID}
	t.active[tunnelID] = tc
	onActive := t.onActive
	proxyURL := t.proxyURL
	t.mu.Unlock()
	if onActive != nil {
		onActive()
	}

	// Dial asynchronously so the egress dispatch loop is not blocked while the
	// target connection is established. Any tunnel.data that arrives before the
	// dial completes is buffered in tc.buf by handleData and flushed below.
	go func() {
		var target net.Conn
		var err error
		if proxyURL != "" {
			log.Printf("[tunnel] handleOpen dialing via proxy id=%s target=%s proxy=%s", tunnelID, targetAddr, proxyURL)
			target, err = dialViaHTTPProxy(targetAddr, proxyURL, 10*time.Second)
		} else {
			target, err = net.DialTimeout("tcp", targetAddr, 10*time.Second)
		}
		if err != nil {
			log.Printf("[tunnel] handleOpen dial failed id=%s target=%s err=%v", tunnelID, targetAddr, err)
			t.handleClose(tunnelID)
			closePayload, _ := json.Marshal(tunnelClosePayload{})
			closeEnv := fabric.NewEnvelope(tunnelID, fabric.TrafficEgress, tunnelClose, closePayload)
			closeEnv.Source = t.identity
			closeEnv.Destination = source
			_ = t.writeEnvelope(ctx, binding.Carrier, binding.Endpoint, closeEnv)
			return
		}
		log.Printf("[tunnel] handleOpen dialed OK id=%s target=%s", tunnelID, targetAddr)

		t.mu.Lock()
		tc.targetConn = target
		if tc.buf.Len() > 0 {
			bufBytes := tc.buf.Bytes()
			pv := bufBytes
			if len(pv) > 24 {
				pv = pv[:24]
			}
			log.Printf("[tunnel] handleOpen flush buffered %d bytes id=%s preview=%x", len(bufBytes), tunnelID, pv)
			_, _ = target.Write(bufBytes)
			tc.buf.Reset()
		}
		t.mu.Unlock()

		t.tunnelReader(ctx, tunnelID, target, binding, source)
	}()
}

func (t *CarrierTunnel) handleData(tunnelID string, payload []byte) {
	t.mu.Lock()
	tc, ok := t.active[tunnelID]
	t.mu.Unlock()
	if !ok {
		return
	}

	pv := payload
	if len(pv) > 24 {
		pv = pv[:24]
	}
	t.mu.Lock()
	if tc.targetConn != nil {
		log.Printf("[tunnel] handleData direct write %d bytes id=%s preview=%x", len(payload), tunnelID, pv)
		_, _ = tc.targetConn.Write(payload)
	} else {
		log.Printf("[tunnel] handleData buffer %d bytes id=%s preview=%x", len(payload), tunnelID, pv)
		tc.buf.Write(payload)
	}
	t.mu.Unlock()
}

func (t *CarrierTunnel) handleClose(tunnelID string) {
	t.mu.Lock()
	tc, ok := t.active[tunnelID]
	delete(t.active, tunnelID)
	onIdle := t.onIdle
	idle := len(t.active) == 0
	t.mu.Unlock()
	if !ok {
		return
	}
	if tc.targetConn != nil {
		tc.targetConn.Close()
	}
	if idle && onIdle != nil {
		go onIdle()
	}
}

func (t *CarrierTunnel) tunnelReader(ctx context.Context, tunnelID string, target net.Conn, binding policy.CarrierBinding, source string) {
	defer target.Close()
	log.Printf("[tunnel] tunnelReader started id=%s source=%s endpoint=%s", tunnelID, source, binding.Endpoint.ID)
	buf := make([]byte, writeBufSize)
	lastWrite := time.Now()
	keepalive := time.NewTicker(keepaliveInterval)
	defer keepalive.Stop()
	for {
		// Use a deadline so we can send keepalive when idle.
		if d, ok := target.(interface{ SetReadDeadline(time.Time) error }); ok {
			_ = d.SetReadDeadline(time.Now().Add(keepaliveInterval))
		}
		select {
		case <-keepalive.C:
			if time.Since(lastWrite) >= keepaliveInterval {
				keepaliveEnv := fabric.NewEnvelope(tunnelID, fabric.TrafficEgress, tunnelData, nil)
				keepaliveEnv.Source = t.identity
				keepaliveEnv.Destination = source
				keepaliveEnv.Sequence = 0
				if err := t.writeEnvelope(ctx, binding.Carrier, binding.Endpoint, keepaliveEnv); err != nil {
					return
				}
				lastWrite = time.Now()
			}
		default:
		}
		n, err := target.Read(buf)
		if err != nil {
			// If it's a timeout, loop back to send keepalive.
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			log.Printf("[tunnel] tunnelReader target read error id=%s err=%v", tunnelID, err)
			t.handleClose(tunnelID)
			closePayload, _ := json.Marshal(tunnelClosePayload{})
			closeEnv := fabric.NewEnvelope(tunnelID, fabric.TrafficEgress, tunnelClose, closePayload)
			closeEnv.Source = t.identity
			closeEnv.Destination = source
			_ = t.writeEnvelope(ctx, binding.Carrier, binding.Endpoint, closeEnv)
			return
		}
		if n == 0 {
			continue
		}
		if n > 0 {
			log.Printf("[tunnel] tunnelReader read %d bytes from target id=%s", n, tunnelID)
		}
		chunk := make([]byte, n)
		copy(chunk, buf[:n])
		env := fabric.NewEnvelope(tunnelID, fabric.TrafficEgress, tunnelData, chunk)
		env.Source = t.identity
		env.Destination = source
		if err := t.writeEnvelope(ctx, binding.Carrier, binding.Endpoint, env); err != nil {
			log.Printf("[tunnel] tunnelReader write error id=%s err=%v", tunnelID, err)
			return
		}
		lastWrite = time.Now()
	}
}

// HandleEgressEnvelope processes a single egress envelope from a carrier poll.
func (t *CarrierTunnel) HandleEgressEnvelope(ctx context.Context, binding policy.CarrierBinding, env fabric.Envelope) {
	t.handleRawEgressEnvelope(ctx, binding, env)
}

// CloseAll terminates all active tunnel connections and clears session bindings.
func (t *CarrierTunnel) CloseAll() {
	t.closeAll()
}

func (t *CarrierTunnel) closeAll() {
	t.mu.Lock()
	for _, tc := range t.active {
		if tc.targetConn != nil {
			tc.targetConn.Close()
		}
	}
	t.active = make(map[string]*tunnelConn)
	packets := t.activePackets
	t.activePackets = make(map[string]*packetServer)
	clientPackets := t.clientPackets
	t.clientPackets = make(map[string]*carrierPacketConn)
	packetReaders := t.packetReaders
	t.packetReaders = make(map[string]*packetClientReader)
	t.packetSessions = make(map[string]*packetSessionState)
	t.sessionBindings = make(map[string]policy.CarrierBinding)
	t.cipher = nil
	t.mu.Unlock()
	for _, reader := range packetReaders {
		reader.cancel()
	}
	for _, packet := range clientPackets {
		packet.shutdown(false)
	}
	for _, packet := range packets {
		packet.shutdown()
	}
}
