// Package punch implements STUN-assisted UDP hole punching between two
// WhiteTransport nodes. Signaling (candidate exchange) rides sealed fabric
// envelopes over any mailbox carrier — including the ssh.fabric hub — while
// the data path is direct node-to-node UDP. When the punch fails, callers
// keep using the existing carrier tunnels, which makes the hub the de facto
// TURN fallback without any new relay code.
package punch

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// Candidate is one UDP address a peer can target, in ICE shorthand.
type Candidate struct {
	Addr string `json:"addr"`
	Typ  string `json:"typ"` // "host" or "srflx"
}

// Offer is the initiator's punch description. Key travels only inside the
// sealed fabric envelope; on the wire it never leaves AES-256-GCM.
type Offer struct {
	SessionID  []byte      `json:"session_id"`
	Key        []byte      `json:"key"`
	Candidates []Candidate `json:"candidates"`
	CreatedAt  time.Time   `json:"created_at"`
}

// Answer is the responder's punch description. The session key already
// arrived with the offer, so only candidates travel back.
type Answer struct {
	SessionID  []byte      `json:"session_id"`
	Candidates []Candidate `json:"candidates"`
	CreatedAt  time.Time   `json:"created_at"`
}

// Signaling moves offers and answers between the two nodes. Production
// wiring is FabricSignaling over a mailbox carrier; tests use channels.
type Signaling interface {
	SendOffer(ctx context.Context, offer Offer) error
	RecvOffer(ctx context.Context) (Offer, error)
	SendAnswer(ctx context.Context, answer Answer) error
	RecvAnswer(ctx context.Context, sessionID []byte) (Answer, error)
}

// Config tunes gathering and punching. Zero fields are filled by
// normalize; an explicitly empty STUNServers disables STUN (host
// candidates only), which keeps tests offline.
type Config struct {
	STUNServers   []string
	STUNTimeout   time.Duration
	PunchTimeout  time.Duration
	ProbeInterval time.Duration
	Keepalive     time.Duration
	// Port fixes the local UDP port of the punch socket (0 = random).
	// Useful when a firewall must pre-authorize inbound probes.
	Port int
	Log  func(format string, args ...any)
}

func DefaultConfig() Config {
	return Config{
		STUNServers:   DefaultServers(),
		STUNTimeout:   2 * time.Second,
		PunchTimeout:  15 * time.Second,
		ProbeInterval: 200 * time.Millisecond,
		Keepalive:     4 * time.Second,
		Log:           func(string, ...any) {},
	}
}

func (c *Config) normalize() {
	d := DefaultConfig()
	if c.STUNServers == nil {
		c.STUNServers = d.STUNServers
	} // an explicitly empty, non-nil slice opts out of STUN (offline tests)
	if c.STUNTimeout == 0 {
		c.STUNTimeout = d.STUNTimeout
	}
	if c.PunchTimeout == 0 {
		c.PunchTimeout = d.PunchTimeout
	}
	if c.ProbeInterval == 0 {
		c.ProbeInterval = d.ProbeInterval
	}
	if c.Keepalive == 0 {
		c.Keepalive = d.Keepalive
	}
	if c.Log == nil {
		c.Log = func(string, ...any) {}
	}
}

// Session is an established direct UDP path between two nodes. Datagrams
// are HMAC-authenticated with the session key; probes keep the NAT mapping
// alive while the session is open.
type Session struct {
	conn      *net.UDPConn
	sessionID []byte
	key       []byte

	mu      sync.Mutex
	remote  *net.UDPAddr
	seq     uint64
	started time.Time

	dataCh   chan []byte
	estCh    chan struct{} // closed once, on first verified inbound frame
	stopCh   chan struct{}
	doneCh   chan struct{} // closed when the reader exits

	closeOnce sync.Once
	sendMu    sync.Mutex
	log       func(string, ...any)
}

// RemoteAddr returns the punched peer address, or "" before establishment.
func (s *Session) RemoteAddr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.remote == nil {
		return ""
	}
	return s.remote.String()
}

// EstablishedAt returns when the first verified inbound frame arrived.
func (s *Session) EstablishedAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started
}

// Send writes one authenticated data datagram to the peer.
func (s *Session) Send(payload []byte) error {
	if len(payload) > MaxPayload {
		return fmt.Errorf("punch: payload %d exceeds %d", len(payload), MaxPayload)
	}
	s.mu.Lock()
	remote := s.remote
	s.mu.Unlock()
	if remote == nil {
		return errors.New("punch: session not established")
	}
	return s.sendFrame(frameData, payload, remote)
}

// Recv blocks until the next verified data datagram arrives. Probe and
// pong frames are answered and consumed internally.
func (s *Session) Recv() ([]byte, error) {
	select {
	case p := <-s.dataCh:
		return p, nil
	case <-s.doneCh:
		return nil, errors.New("punch: session closed")
	}
}

// Close tears the session down and releases its socket.
func (s *Session) Close() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.stopCh)
		err = s.conn.Close()
	})
	return err
}

func (s *Session) sendFrame(typ uint8, payload []byte, to *net.UDPAddr) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	s.seq++
	dg := frame{FrameType: typ, SessionID: s.sessionID, Seq: s.seq, Payload: payload}.seal(s.key)
	_, err := s.conn.WriteToUDP(dg, to)
	return err
}

func (s *Session) noteRemote(from *net.UDPAddr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.remote == nil {
		// The observed source can differ from every signaled candidate when
		// the NAT re-maps the flow (peer-reflexive); trust the wire.
		s.remote = from
		s.started = time.Now()
		close(s.estCh)
	}
}

func (s *Session) readLoop() {
	defer close(s.doneCh)
	buf := make([]byte, 2048)
	for {
		if err := s.conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
			return
		}
		n, from, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				select {
				case <-s.stopCh:
					return
				default:
					continue
				}
			}
			return // socket closed
		}
		f, err := openFrame(buf[:n], s.key)
		if err != nil || !bytes.Equal(f.SessionID, s.sessionID) {
			continue // junk or another punch session on the same port
		}
		switch f.FrameType {
		case frameProbe:
			if err := s.sendFrame(framePong, nil, from); err != nil {
				s.log("punch: pong: %v", err)
			}
		case framePong:
			// Proof of life only.
		case frameData:
			select {
			case s.dataCh <- f.Payload:
			default: // consumer lagging: drop, this is datagram semantics
			}
		}
		s.noteRemote(from)
	}
}

func (s *Session) probeLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		s.mu.Lock()
		remote := s.remote
		s.mu.Unlock()
		if remote != nil {
			if err := s.sendFrame(frameProbe, nil, remote); err != nil {
				s.log("punch: keepalive: %v", err)
			}
		}
		select {
		case <-ticker.C:
		case <-s.stopCh:
			return
		}
	}
}

// gatherCandidates binds nothing itself: it walks local interfaces and —
// unless disabled — queries each configured STUN server over the punch
// socket so the reflexive mapping matches the socket peers will target.
func gatherCandidates(conn *net.UDPConn, cfg Config) []Candidate {
	local, _ := conn.LocalAddr().(*net.UDPAddr)
	var out []Candidate
	if local != nil {
		out = append(out, hostCandidates(local.Port)...)
	}
	seen := map[string]bool{}
	for _, server := range cfg.STUNServers {
		addr, err := stunQuery(conn, server, cfg.STUNTimeout)
		if err != nil {
			cfg.Log("punch: %v", err)
			continue
		}
		cfg.Log("punch: stun %s -> %s", server, addr)
		key := addr.String()
		if seen[key] || (local != nil && key == local.String()) {
			continue
		}
		seen[key] = true
		out = append(out, Candidate{Addr: key, Typ: "srflx"})
	}
	return out
}

func listenUDP(port int) (*net.UDPConn, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: port})
	if err != nil {
		return nil, fmt.Errorf("punch: listen: %w", err)
	}
	return conn, nil
}

func newSession(conn *net.UDPConn, sessionID, key []byte, log func(string, ...any)) *Session {
	return &Session{
		conn:      conn,
		sessionID: sessionID,
		key:       key,
		dataCh:    make(chan []byte, 64),
		estCh:     make(chan struct{}),
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
		log:       log,
	}
}

// Initiate runs the initiator side: bind, gather candidates, exchange
// signaling, then probe the peer's candidates until a verified frame
// arrives. The returned Session is a direct UDP path to the peer.
func Initiate(ctx context.Context, cfg Config, sig Signaling) (*Session, error) {
	cfg.normalize()
	sessionID := make([]byte, sessionIDLen)
	key := make([]byte, keyLen)
	if _, err := rand.Read(sessionID); err != nil {
		return nil, fmt.Errorf("punch: session id: %w", err)
	}
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("punch: key: %w", err)
	}
	conn, err := listenUDP(cfg.Port)
	if err != nil {
		return nil, err
	}
	s := newSession(conn, sessionID, key, cfg.Log)

	// Gather before the read loop starts: both read from the same socket,
	// and the reader would swallow STUN responses.
	candidates := gatherCandidates(conn, cfg)
	for _, c := range candidates {
		cfg.Log("punch: local candidate %s (%s)", c.Addr, c.Typ)
	}
	go s.readLoop()

	offer := Offer{
		SessionID:  sessionID,
		Key:        key,
		Candidates: candidates,
		CreatedAt:  time.Now().UTC(),
	}
	if err := sig.SendOffer(ctx, offer); err != nil {
		s.Close()
		return nil, fmt.Errorf("punch: send offer: %w", err)
	}
	answer, err := sig.RecvAnswer(ctx, sessionID)
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("punch: recv answer: %w", err)
	}
	return s.probe(ctx, cfg, answer.Candidates)
}

// Respond runs the responder side of the same exchange.
func Respond(ctx context.Context, cfg Config, sig Signaling) (*Session, error) {
	cfg.normalize()
	offer, err := sig.RecvOffer(ctx)
	if err != nil {
		return nil, fmt.Errorf("punch: recv offer: %w", err)
	}
	if len(offer.Key) != keyLen || len(offer.SessionID) != sessionIDLen {
		return nil, fmt.Errorf("punch: malformed offer keying material")
	}
	conn, err := listenUDP(cfg.Port)
	if err != nil {
		return nil, err
	}
	s := newSession(conn, offer.SessionID, offer.Key, cfg.Log)

	// Gather before the read loop starts (see Offer).
	candidates := gatherCandidates(conn, cfg)
	for _, c := range candidates {
		cfg.Log("punch: local candidate %s (%s)", c.Addr, c.Typ)
	}
	go s.readLoop()

	if err := sig.SendAnswer(ctx, Answer{
		SessionID:  offer.SessionID,
		Candidates: candidates,
		CreatedAt:  time.Now().UTC(),
	}); err != nil {
		s.Close()
		return nil, fmt.Errorf("punch: send answer: %w", err)
	}
	return s.probe(ctx, cfg, offer.Candidates)
}

// probe sends authenticated probes at every remote candidate until the
// first verified inbound frame (probe or pong) establishes the session,
// then switches to keepalive-paced probes to hold the NAT mapping.
func (s *Session) probe(ctx context.Context, cfg Config, remoteCandidates []Candidate) (*Session, error) {
	if len(remoteCandidates) == 0 {
		s.Close()
		return nil, errors.New("punch: peer advertised no candidates")
	}
	pctx, cancel := context.WithTimeout(ctx, cfg.PunchTimeout)
	defer cancel()

	go func() {
		ticker := time.NewTicker(cfg.ProbeInterval)
		defer ticker.Stop()
		for {
			for _, c := range remoteCandidates {
				raddr, err := net.ResolveUDPAddr("udp4", c.Addr)
				if err != nil {
					s.log("punch: bad candidate %s: %v", c.Addr, err)
					continue
				}
				if err := s.sendFrame(frameProbe, nil, raddr); err != nil {
					s.log("punch: probe %s: %v", c.Addr, err)
				}
			}
			select {
			case <-ticker.C:
			case <-s.estCh:
				return
			case <-s.stopCh:
				return
			case <-pctx.Done():
				return
			}
		}
	}()

	select {
	case <-s.estCh:
		go s.probeLoop(cfg.Keepalive)
		s.log("punch: established, remote=%s", s.RemoteAddr())
		return s, nil
	case <-s.stopCh:
		return nil, errors.New("punch: session closed during punch")
	case <-pctx.Done():
		s.Close()
		return nil, fmt.Errorf("punch: no verified frame within %s (tried %d candidates)", cfg.PunchTimeout, len(remoteCandidates))
	}
}
