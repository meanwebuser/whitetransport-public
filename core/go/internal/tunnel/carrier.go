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
	"github.com/meanwebuser/whitetransport/core/internal/session"
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
	// egressResponseLivenessTimeout bounds a stream that has sent application
	// bytes but received no carrier response. The runtime confirms node
	// liveness on its control path before treating this as node loss, so a
	// slow target cannot by itself cause node reselection.
	egressResponseLivenessTimeout        = 6 * time.Second
	mailboxEgressResponseLivenessTimeout = 30 * time.Second
	vkDocsWriteBurstWindow               = 15 * time.Millisecond
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
	failureStreams map[string]egressFailureStream
	activePackets  map[string]*packetServer
	clientPackets  map[string]*carrierPacketConn
	packetReaders  map[string]*packetClientReader
	packetSessions map[string]*packetSessionState
	sessionCiphers map[string]*fabric.EnvelopeCipher
	packetResolver func(context.Context, string) (*net.UDPAddr, error)
	// packetExitDomainResolutions is incremented only by the node-side packet
	// egress handler after a domain has been resolved successfully.
	packetExitDomainResolutions uint64
	sessionBindings             map[string]policy.CarrierBinding
	cipher                      *fabric.EnvelopeCipher
	onIdle                      func(sessionID string)
	onActive                    func()
}

type carrierFailureState struct {
	mu     sync.Mutex
	err    error
	closed bool
}

type egressResponseWatch struct {
	mu           sync.Mutex
	lastRequest  time.Time
	lastResponse time.Time
}

func (w *egressResponseWatch) noteRequest(now time.Time) {
	w.mu.Lock()
	w.lastRequest = now
	w.mu.Unlock()
}

func (w *egressResponseWatch) noteResponse(now time.Time) {
	w.mu.Lock()
	w.lastResponse = now
	w.mu.Unlock()
}

func (w *egressResponseWatch) responseDeadline(timeout time.Duration) time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.lastRequest.IsZero() || !w.lastResponse.Before(w.lastRequest) {
		return time.Time{}
	}
	return w.lastRequest.Add(timeout)
}

func (s *carrierFailureState) set(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	if !s.closed && s.err == nil {
		if session.IsCarrierFailure(err) {
			s.err = err
		} else {
			s.err = session.NewCarrierFailure(err)
		}
	}
	s.mu.Unlock()
}

func (s *carrierFailureState) markGraceful() {
	s.mu.Lock()
	if s.err == nil {
		s.closed = true
	}
	s.mu.Unlock()
}

func (s *carrierFailureState) suppressExplicitClose() {
	s.mu.Lock()
	s.closed = true
	s.err = nil
	s.mu.Unlock()
}

func (s *carrierFailureState) get() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

type carrierTunnelConn struct {
	net.Conn
	failure    *carrierFailureState
	stopReader context.CancelFunc
}

func (c *carrierTunnelConn) FailureCause() error { return c.failure.get() }

func (c *carrierTunnelConn) Close() error {
	c.failure.suppressExplicitClose()
	if c.stopReader != nil {
		c.stopReader()
	}
	return c.Conn.Close()
}

type tunnelConn struct {
	tunnelID     string
	sessionID    string
	targetConn   net.Conn
	writeMu      sync.Mutex
	buf          bytes.Buffer
	firstPayload chan struct{}
	cancel       context.CancelFunc
}

// NewCarrierTunnel builds a tunnel that writes/reads egress data through the
// same carrier adapters used for control messages.
func NewCarrierTunnel(identity string, bindings map[string]policy.CarrierBinding) *CarrierTunnel {
	return &CarrierTunnel{
		identity:        identity,
		bindings:        bindings,
		active:          make(map[string]*tunnelConn),
		failureStreams:  make(map[string]egressFailureStream),
		activePackets:   make(map[string]*packetServer),
		clientPackets:   make(map[string]*carrierPacketConn),
		packetReaders:   make(map[string]*packetClientReader),
		packetSessions:  make(map[string]*packetSessionState),
		sessionCiphers:  make(map[string]*fabric.EnvelopeCipher),
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

func (t *CarrierTunnel) SetOnIdle(fn func(sessionID string)) {
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

// SetCipher enables the legacy process-wide AES-256-GCM key used by bootstrap
// and control envelopes. Session data should use SetSessionCipher instead.
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

// SetSessionCipher binds one authenticated data-plane key without disturbing
// other sessions. Session envelopes expose SessionID as routing metadata.
func (t *CarrierTunnel) SetSessionCipher(sessionID string, cipher *fabric.EnvelopeCipher) {
	if strings.TrimSpace(sessionID) == "" || cipher == nil {
		return
	}
	t.mu.Lock()
	t.sessionCiphers[sessionID] = cipher
	t.mu.Unlock()
}

// ClearSessionCipher ends this session's streams and packet associations.
func (t *CarrierTunnel) ClearSessionCipher(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	var targets []net.Conn
	t.mu.Lock()
	if state := t.packetSessions[sessionID]; state != nil {
		state.closing = true
	}
	for id, tc := range t.active {
		if tc.sessionID != sessionID {
			continue
		}
		tc.cancel()
		if tc.targetConn != nil {
			targets = append(targets, tc.targetConn)
		}
		delete(t.active, id)
	}
	delete(t.sessionCiphers, sessionID)
	t.mu.Unlock()
	for _, target := range targets {
		target.Close()
	}
	t.ClosePacketSession(sessionID)
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

func (t *CarrierTunnel) getSessionCipher(sessionID string) *fabric.EnvelopeCipher {
	t.mu.Lock()
	defer t.mu.Unlock()
	if cipher := t.sessionCiphers[sessionID]; cipher != nil {
		return cipher
	}
	return t.cipher
}

// writeEnvelope writes an envelope, sealing it with the cipher if available.
func (t *CarrierTunnel) writeEnvelope(ctx context.Context, carrier carriers.Carrier, endpoint carriers.Endpoint, env fabric.Envelope) error {
	prepared, err := t.prepareEnvelope(ctx, env)
	if err != nil {
		return err
	}
	return carrier.Write(ctx, endpoint, prepared)
}

func (t *CarrierTunnel) prepareEnvelope(ctx context.Context, env fabric.Envelope) (fabric.Envelope, error) {
	cipher := t.getSessionCipher(env.SessionID)
	// Session teardown cancels exit streams before removing their keys.
	if err := ctx.Err(); err != nil {
		return fabric.Envelope{}, err
	}
	if cipher == nil {
		return env, nil
	}
	return sealCarrierEnvelope(cipher, env)
}

// writeEncryptedEnvelope is the fail-closed writer for session packet data.
// Packet traffic must never fall back to plaintext after session teardown.
func (t *CarrierTunnel) writeEncryptedEnvelope(ctx context.Context, carrier carriers.Carrier, endpoint carriers.Endpoint, env fabric.Envelope) error {
	cipher := t.getSessionCipher(env.SessionID)
	if cipher == nil {
		return errors.New("carrier packet egress: encrypted session cipher is unavailable")
	}
	raw, err := sealCarrierEnvelope(cipher, env)
	if err != nil {
		return err
	}
	return carrier.Write(ctx, endpoint, raw)
}

func sealCarrierEnvelope(cipher *fabric.EnvelopeCipher, env fabric.Envelope) (fabric.Envelope, error) {
	sealed, err := cipher.Seal(env)
	if err != nil {
		return fabric.Envelope{}, fmt.Errorf("seal carrier envelope: %w", err)
	}
	return fabric.Envelope{
		Version:      1,
		ID:           env.ID,
		TrafficClass: env.TrafficClass,
		PayloadType:  "encrypted",
		Source:       env.Source,
		Destination:  env.Destination,
		Sequence:     env.Sequence,
		CreatedAt:    env.CreatedAt,
		SessionID:    env.SessionID,
		Payload:      sealed,
	}, nil
}

// readEnvelope reads and optionally decrypts an envelope from carrier read results.
func (t *CarrierTunnel) readEnvelope(env fabric.Envelope) (fabric.Envelope, error) {
	if env.PayloadType == "encrypted" {
		if c := t.getSessionCipher(env.SessionID); c != nil {
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
	return t.DialContextForSession(ctx, "", ep, targetAddr)
}

func (t *CarrierTunnel) DialContextForSession(ctx context.Context, sessionID string, ep carriers.Endpoint, targetAddr string) (net.Conn, error) {
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
	openEnvelope.SessionID = sessionID
	openEnvelope.Source = t.identity
	client, server := net.Pipe()
	failure := &carrierFailureState{}
	watch := &egressResponseWatch{}
	readCtx, stopReader := context.WithCancel(ctx)
	var responses *providerStreamResponse
	if usesProviderStreamReader(binding) {
		responses = t.registerProviderStream(binding, tunnelID, func(err error) {
			failure.set(err)
			_ = server.Close()
			stopReader()
		})
	}
	if err := t.writeEnvelope(ctx, binding.Carrier, binding.Endpoint, openEnvelope); err != nil {
		if responses != nil {
			t.unregisterProviderStream(responses)
		}
		stopReader()
		_ = server.Close()
		_ = client.Close()
		return nil, session.NewCarrierFailure(fmt.Errorf("carrier tunnel: write open: %w", err))
	}
	log.Printf("[tunnel] dial open sent id=%s target=%s endpoint=%s", tunnelID, targetAddr, binding.Endpoint.ID)

	t.watchEgressFailure(sessionID, tunnelID, func() {
		failure.set(session.ErrEgressReturnCarrier)
		_ = server.Close()
		stopReader()
	})
	go func() {
		defer stopReader()
		t.writeLoopWithFailure(ctx, tunnelID, sessionID, server, binding, failure, watch)
	}()
	go func() {
		defer stopReader()
		defer t.unwatchEgressFailure(tunnelID)
		if responses != nil {
			defer t.unregisterProviderStream(responses)
			t.readProviderStream(readCtx, responses, server, failure, watch)
			return
		}
		t.readLoopWithFailure(readCtx, tunnelID, server, binding, failure, watch)
	}()
	return &carrierTunnelConn{Conn: client, failure: failure, stopReader: stopReader}, nil
}

func (t *CarrierTunnel) writeLoop(ctx context.Context, tunnelID string, server net.Conn, binding policy.CarrierBinding) {
	t.writeLoopWithFailure(ctx, tunnelID, "", server, binding, &carrierFailureState{}, &egressResponseWatch{})
}

func (t *CarrierTunnel) writeLoopWithFailure(ctx context.Context, tunnelID, sessionID string, server net.Conn, binding policy.CarrierBinding, failure *carrierFailureState, watch *egressResponseWatch) {
	defer server.Close()
	seq := uint64(1)
	buf := make([]byte, egressWriteChunkLimit(binding.Carrier, t.getSessionCipher(sessionID) != nil))
	combineWrites := isVKDocsCarrier(binding.Carrier)
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
				keepaliveEnv.SessionID = sessionID
				keepaliveEnv.Source = t.identity
				keepaliveEnv.Sequence = 0 // zero seq marks keepalive
				if err := t.writeEnvelope(ctx, binding.Carrier, binding.Endpoint, keepaliveEnv); err != nil {
					failure.set(err)
					return
				}
				lastWrite = time.Now()
			}
		default:
		}
		n, err := server.Read(buf)
		if combineWrites && err == nil && n > 0 && n < len(buf) {
			// Adjacent TLS writes can share one VK upload. Use one fixed
			// deadline so a busy stream cannot keep extending the delay.
			if server.SetReadDeadline(time.Now().Add(vkDocsWriteBurstWindow)) == nil {
				for n < len(buf) && err == nil {
					var more int
					more, err = server.Read(buf[n:])
					n += more
				}
			}
		}
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			log.Printf("[tunnel] writeLoop send %d bytes id=%s", n, tunnelID)
			env := fabric.NewEnvelope(tunnelID, fabric.TrafficEgress, tunnelData, chunk)
			env.SessionID = sessionID
			env.Source = t.identity
			env.Sequence = seq
			seq++
			if writeErr := t.writeEnvelope(ctx, binding.Carrier, binding.Endpoint, env); writeErr != nil {
				failure.set(writeErr)
				return
			}
			lastWrite = time.Now()
			watch.noteRequest(lastWrite)
		}
		if err == nil {
			continue
		}
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			continue
		}
		log.Printf("[tunnel] writeLoop server.Read closing id=%s err=%v", tunnelID, err)
		closePayload, _ := json.Marshal(tunnelClosePayload{})
		closeEnv := fabric.NewEnvelope(tunnelID, fabric.TrafficEgress, tunnelClose, closePayload)
		closeEnv.SessionID = sessionID
		closeEnv.Source = t.identity
		_ = t.writeEnvelope(ctx, binding.Carrier, binding.Endpoint, closeEnv)
		return
	}
}

func (t *CarrierTunnel) readLoop(ctx context.Context, tunnelID string, client net.Conn, binding policy.CarrierBinding) {
	t.readLoopWithFailure(ctx, tunnelID, client, binding, &carrierFailureState{}, &egressResponseWatch{})
}

func (t *CarrierTunnel) readLoopWithFailure(ctx context.Context, tunnelID string, client net.Conn, binding policy.CarrierBinding, failure *carrierFailureState, watch *egressResponseWatch) {
	defer client.Close()
	cursor := carriers.Cursor("")
	poll := time.NewTicker(egressReadInterval)
	defer poll.Stop()
	livenessTimeout := egressResponseLivenessTimeout
	usesVKHistory := false
	if binding.Carrier != nil {
		desc := binding.Carrier.Descriptor()
		switch desc.ID {
		case carriers.CarrierVKMessages:
			usesVKHistory = true
		case carriers.CarrierVKDocs256, carriers.CarrierVKDocs1024:
			usesVKHistory = true
			ctx = carriers.WithVKDocsEnvelopeFilter(ctx, tunnelID)
		}
		if desc.Mode == carriers.DeliveryMailbox || desc.Mode == carriers.DeliveryBulk {
			// Upload/readback carriers can legitimately take several polling
			// intervals to deliver the first TLS response or a document chunk.
			livenessTimeout = mailboxEgressResponseLivenessTimeout
		}
	}
	pollCtx := ctx
	var cancelThrottle context.CancelFunc
	defer func() {
		if cancelThrottle != nil {
			cancelThrottle()
		}
	}()
	for {
		select {
		case <-pollCtx.Done():
			if ctx.Err() != nil {
				log.Printf("[tunnel] readLoop ctx done id=%s", tunnelID)
				failure.markGraceful()
			} else {
				err := context.Cause(pollCtx)
				log.Printf("[tunnel] readLoop history recovery expired id=%s err=%v", tunnelID, err)
				failure.set(err)
			}
			return
		case <-poll.C:
			if pollCtx.Err() != nil {
				continue
			}
			started := time.Now()
			responseDeadline := watch.responseDeadline(livenessTimeout)
			if !responseDeadline.IsZero() && !started.Before(responseDeadline) {
				failure.set(fmt.Errorf("carrier tunnel: no response within %s", livenessTimeout))
				return
			}
			readCtx := pollCtx
			var cancelRead context.CancelFunc
			if usesVKHistory && cancelThrottle == nil && !responseDeadline.IsZero() {
				// Even the first VK retry series shares the response deadline.
				readCtx, cancelRead = context.WithDeadline(pollCtx, responseDeadline)
			}
			read, err := binding.Carrier.Read(readCtx, binding.Endpoint, cursor)
			readTimedOut := readCtx.Err() == context.DeadlineExceeded
			if cancelRead != nil {
				cancelRead()
			}
			if pollCtx.Err() != nil {
				continue
			}
			var throttle *carriers.VKHistoryThrottleError
			throttled := errors.As(err, &throttle)
			if readTimedOut {
				if !throttled {
					err = fmt.Errorf("carrier tunnel: no response within %s", livenessTimeout)
				}
				log.Printf("[tunnel] readLoop response deadline id=%s err=%v", tunnelID, err)
				failure.set(err)
				return
			}
			if err != nil {
				if throttled {
					if cancelThrottle == nil {
						// An idle stream has no response deadline, so also bound the
						// whole run of throttled polls from the first failed poll.
						deadline := started.Add(livenessTimeout)
						if !responseDeadline.IsZero() && responseDeadline.Before(deadline) {
							deadline = responseDeadline
						}
						pollCtx, cancelThrottle = context.WithDeadlineCause(ctx, deadline, throttle)
						log.Printf("[tunnel] readLoop retrying history id=%s err=%v", tunnelID, throttle)
					}
					continue
				}
				log.Printf("[tunnel] readLoop carrier read err id=%s err=%v", tunnelID, err)
				failure.set(err)
				return
			}
			if cancelThrottle != nil {
				cancelThrottle()
				cancelThrottle = nil
				pollCtx = ctx
			}
			cursor = read.Cursor
			for _, env := range read.Envelopes {
				if !t.deliverStreamResponse(tunnelID, client, env, watch) {
					failure.markGraceful()
					return
				}
			}
		}
	}
}

func (t *CarrierTunnel) deliverStreamResponse(tunnelID string, client net.Conn, raw fabric.Envelope, watch *egressResponseWatch) bool {
	env, err := t.readEnvelope(raw)
	if err != nil {
		log.Printf("[tunnel] readLoop decrypt fail id=%s err=%v", tunnelID, err)
		return true
	}
	// Mailboxes can echo the locally-written request. Never deliver it as a response.
	if env.ID != tunnelID || env.Source == t.identity {
		return true
	}
	watch.noteResponse(time.Now())
	if env.Sequence == 0 && len(env.Payload) == 0 {
		return true
	}
	switch env.PayloadType {
	case tunnelData:
		log.Printf("[tunnel] readLoop received %d bytes id=%s source=%s", len(env.Payload), tunnelID, env.Source)
		if _, err := client.Write(env.Payload); err != nil {
			log.Printf("[tunnel] readLoop client.Write err id=%s err=%v", tunnelID, err)
			return false
		}
	case tunnelClose:
		return false
	default:
		log.Printf("[tunnel] readLoop skip payload type=%s id=%s", env.PayloadType, tunnelID)
	}
	return true
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
		switch binding.Carrier.Descriptor().ID {
		case carriers.CarrierVKDocs256, carriers.CarrierVKDocs1024:
			physicalCtx = carriers.WithVKDocsEnvelopeFilter(physicalCtx, "")
		}
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
		t.handleOpen(ctx, binding, env.ID, env.SessionID, payload.TargetAddr, env.Source)
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

func (t *CarrierTunnel) handleOpen(ctx context.Context, binding policy.CarrierBinding, tunnelID, sessionID, targetAddr, source string) {
	log.Printf("[tunnel] handleOpen id=%s target=%s source=%s", tunnelID, targetAddr, source)
	t.mu.Lock()
	if sessionID != "" {
		lease := t.packetSessions[sessionID]
		if lease == nil || lease.closing || lease.peerID != source || packetSessionExpired(lease.expiresAt) {
			t.mu.Unlock()
			return
		}
	}
	if _, exists := t.active[tunnelID]; exists {
		t.mu.Unlock()
		log.Printf("[tunnel] handleOpen duplicate id=%s", tunnelID)
		return
	}
	streamCtx, cancel := context.WithCancel(ctx)
	tc := &tunnelConn{tunnelID: tunnelID, sessionID: sessionID, cancel: cancel}
	if waitForVKDocsHTTPSPayload(binding.Carrier, targetAddr) {
		tc.firstPayload = make(chan struct{})
	}
	firstPayload := tc.firstPayload
	t.active[tunnelID] = tc
	onActive := t.onActive
	proxyURL := t.proxyURL
	t.mu.Unlock()
	if onActive != nil {
		onActive()
	}

	finish := func(carrierFailed bool) {
		if carrierFailed {
			notice, reply := t.prepareEgressFailure(streamCtx, sessionID, tunnelID, source)
			t.handleClose(tunnelID)
			if reply.Carrier != nil {
				t.sendEgressFailure(ctx, reply, notice)
			}
			return
		}
		closePayload, _ := json.Marshal(tunnelClosePayload{})
		closeEnv := fabric.NewEnvelope(tunnelID, fabric.TrafficEgress, tunnelClose, closePayload)
		closeEnv.SessionID = sessionID
		closeEnv.Source = t.identity
		closeEnv.Destination = source
		prepared, err := t.prepareEnvelope(streamCtx, closeEnv)
		t.handleClose(tunnelID)
		// Send the prepared frame after releasing the local socket.
		if err == nil && ctx.Err() == nil {
			_ = binding.Carrier.Write(ctx, binding.Endpoint, prepared)
		}
	}

	// Dial asynchronously so the egress dispatch loop is not blocked while the
	// target connection is established. Any tunnel.data that arrives before the
	// dial completes is buffered in tc.buf by handleData and flushed below.
	go func() {
		defer cancel()
		if firstPayload != nil {
			// HTTPS waits for a client hello. Opening TCP before a slow document
			// upload spends the origin's idle handshake timeout on the carrier.
			timer := time.NewTimer(mailboxEgressResponseLivenessTimeout)
			defer timer.Stop()
			log.Printf("[tunnel] waiting for first HTTPS payload id=%s", tunnelID)
			select {
			case <-firstPayload:
			case <-timer.C:
			case <-streamCtx.Done():
				return
			}
		}
		if streamCtx.Err() != nil {
			return
		}
		var target net.Conn
		var err error
		if proxyURL != "" {
			log.Printf("[tunnel] handleOpen dialing via proxy id=%s target=%s", tunnelID, targetAddr)
			target, err = dialViaHTTPProxy(streamCtx, targetAddr, proxyURL, 10*time.Second)
		} else {
			dialer := net.Dialer{Timeout: 10 * time.Second}
			target, err = dialer.DialContext(streamCtx, "tcp", targetAddr)
		}
		if err != nil {
			if streamCtx.Err() != nil {
				return
			}
			log.Printf("[tunnel] handleOpen dial failed id=%s target=%s err=%v", tunnelID, targetAddr, err)
			finish(false)
			return
		}
		// Serialize the first buffered write with subsequent writes on this
		// stream while allowing other sessions to use the registry.
		tc.writeMu.Lock()
		t.mu.Lock()
		if t.active[tunnelID] != tc || streamCtx.Err() != nil {
			t.mu.Unlock()
			tc.writeMu.Unlock()
			target.Close()
			return
		}
		log.Printf("[tunnel] handleOpen dialed OK id=%s target=%s", tunnelID, targetAddr)
		tc.targetConn = target
		buffered := tc.buf.Bytes()
		tc.buf = bytes.Buffer{}
		t.mu.Unlock()
		if len(buffered) > 0 {
			log.Printf("[tunnel] handleOpen flush buffered %d bytes id=%s", len(buffered), tunnelID)
			_, err = target.Write(buffered)
		}
		tc.writeMu.Unlock()
		if err != nil {
			if streamCtx.Err() == nil {
				finish(false)
			}
			return
		}

		t.tunnelReader(streamCtx, tunnelID, sessionID, target, binding, source, finish)
	}()
}

func isVKDocsCarrier(carrier carriers.Carrier) bool {
	if carrier == nil {
		return false
	}
	id := carrier.Descriptor().ID
	return id == carriers.CarrierVKDocs256 || id == carriers.CarrierVKDocs1024
}

func waitForVKDocsHTTPSPayload(carrier carriers.Carrier, targetAddr string) bool {
	if !isVKDocsCarrier(carrier) {
		return false
	}
	_, port, err := net.SplitHostPort(targetAddr)
	return err == nil && port == "443"
}

func (t *CarrierTunnel) handleData(tunnelID string, payload []byte) {
	t.mu.Lock()
	tc, ok := t.active[tunnelID]
	if !ok {
		t.mu.Unlock()
		return
	}

	if target := tc.targetConn; target != nil {
		t.mu.Unlock()
		tc.writeMu.Lock()
		defer tc.writeMu.Unlock()
		log.Printf("[tunnel] handleData direct write %d bytes id=%s", len(payload), tunnelID)
		_, _ = target.Write(payload)
		return
	}
	log.Printf("[tunnel] handleData buffer %d bytes id=%s", len(payload), tunnelID)
	tc.buf.Write(payload)
	if len(payload) > 0 && tc.firstPayload != nil {
		close(tc.firstPayload)
		tc.firstPayload = nil
	}
	t.mu.Unlock()
}

func (t *CarrierTunnel) handleClose(tunnelID string) {
	t.mu.Lock()
	tc, ok := t.active[tunnelID]
	delete(t.active, tunnelID)
	var target net.Conn
	if ok {
		if tc.cancel != nil {
			tc.cancel()
		}
		target = tc.targetConn
	}
	onIdle := t.onIdle
	idle := len(t.active) == 0 && len(t.activePackets) == 0
	t.mu.Unlock()
	if !ok {
		return
	}
	if target != nil {
		target.Close()
	}
	if idle && onIdle != nil {
		go onIdle(tc.sessionID)
	}
}

func (t *CarrierTunnel) tunnelReader(ctx context.Context, tunnelID, sessionID string, target net.Conn, binding policy.CarrierBinding, source string, finish func(carrierFailed bool)) {
	defer target.Close()
	defer t.handleClose(tunnelID)
	log.Printf("[tunnel] tunnelReader started id=%s source=%s endpoint=%s", tunnelID, source, binding.Endpoint.ID)
	buf := make([]byte, writeBufSize)
	chunkLimit := egressWriteChunkLimit(binding.Carrier, t.getSessionCipher(sessionID) != nil)
	sequence := uint64(1)
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
				keepaliveEnv.SessionID = sessionID
				keepaliveEnv.Source = t.identity
				keepaliveEnv.Destination = source
				keepaliveEnv.Sequence = 0
				if err := t.writeEnvelope(ctx, binding.Carrier, binding.Endpoint, keepaliveEnv); err != nil {
					finish(true)
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
			if ctx.Err() == nil {
				finish(false)
			}
			return
		}
		if n == 0 {
			continue
		}
		if n > 0 {
			log.Printf("[tunnel] tunnelReader read %d bytes from target id=%s", n, tunnelID)
		}
		for offset := 0; offset < n; {
			end := offset + chunkLimit
			if end > n {
				end = n
			}
			chunk := make([]byte, end-offset)
			copy(chunk, buf[offset:end])
			env := fabric.NewEnvelope(tunnelID, fabric.TrafficEgress, tunnelData, chunk)
			env.SessionID = sessionID
			env.Source = t.identity
			env.Destination = source
			env.Sequence = sequence
			sequence++
			if err := t.writeEnvelope(ctx, binding.Carrier, binding.Endpoint, env); err != nil {
				log.Printf("[tunnel] tunnelReader write error id=%s err=%v", tunnelID, err)
				finish(true)
				return
			}
			offset = end
		}
		lastWrite = time.Now()
	}
}

// egressWriteChunkLimit keeps the encoded carrier envelope under its provider
// payload limit. Mailbox adapters add JSON, base64 and provider framing around
// the raw tunnel bytes, so their safe raw chunk is deliberately below the
// advertised maximum. Stream and bulk carriers keep their native chunk budget.
func egressWriteChunkLimit(carrier carriers.Carrier, sealed bool) int {
	limit := writeBufSize
	if carrier == nil {
		return limit
	}
	descriptor := carrier.Descriptor()
	if descriptor.Limits.ChunkPayloadBytes > 0 && descriptor.Limits.ChunkPayloadBytes < limit {
		limit = descriptor.Limits.ChunkPayloadBytes
	}
	if descriptor.Mode == carriers.DeliveryMailbox && descriptor.Limits.MaxPayloadBytes > 0 {
		mailboxLimit := descriptor.Limits.MaxPayloadBytes / 2
		// A sealed envelope nests JSON and base64 inside the mailbox encoding.
		// Reserve space for the expanded payload and both sets of headers.
		if sealed {
			mailboxLimit = max(1, descriptor.Limits.MaxPayloadBytes/4)
		}
		if mailboxLimit > 0 && mailboxLimit < limit {
			limit = mailboxLimit
		}
	}
	if limit < 1 {
		return 1
	}
	return limit
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
		if tc.cancel != nil {
			tc.cancel()
		}
		if tc.targetConn != nil {
			tc.targetConn.Close()
		}
	}
	t.active = make(map[string]*tunnelConn)
	clear(t.failureStreams)
	packets := t.activePackets
	t.activePackets = make(map[string]*packetServer)
	clientPackets := t.clientPackets
	t.clientPackets = make(map[string]*carrierPacketConn)
	packetReaders := t.packetReaders
	t.packetReaders = make(map[string]*packetClientReader)
	for _, reader := range packetReaders {
		for _, stream := range reader.streams {
			stream.abort(nil)
		}
	}
	t.packetSessions = make(map[string]*packetSessionState)
	t.sessionCiphers = make(map[string]*fabric.EnvelopeCipher)
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
