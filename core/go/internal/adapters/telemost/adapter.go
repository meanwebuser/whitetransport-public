package telemost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	pionrt "whitelist-bypass/relay/pion"
	joiner "whitelist-bypass/relay/pion/headless-joiner-common"
	"whitelist-bypass/relay/tunnel"

	"github.com/meanwebuser/whitetransport/core/internal/provider"
)

// Adapter wraps the upstream whitelist-bypass/relay Telemost provider as a
// thin WhiteTransport provider. It supports the joiner role via the headless
// joiner, and the creator role via the TelemostClient WS signaling handler.
type Provider struct {
	mu          sync.RWMutex
	config      provider.ProviderConfig
	sessCfg     sessionConfig
	hj          *joiner.TelemostHeadlessJoiner
	tunnel      tunnel.DataTunnel
	peerReady   chan struct{}
	recvCh      chan []byte
	onDataFn    func([]byte)
	onCloseFn   func()
	metrics     provider.Metrics
	health      provider.Health
	done        chan struct{}
	startEgress func(context.Context, string) error
}

type sessionConfig struct {
	JoinLink    string
	Cookie      string
	DisplayName string
	Role        string // "creator" or "joiner"
}

var errReceiverClosed = errors.New("telemost provider: receive channel closed")

const (
	telemostVP8FPS           = 2
	telemostVP8Batch         = 1
	telemostConnectTimeout   = 30 * time.Second
	telemostPeerReadyTimeout = 30 * time.Second
)

// CloneForSession returns an independent provider instance for a node-owned
// role-reversal session. Telemost keeps mutable joiner/tunnel state on the
// provider, so sharing the registry singleton would make a second client
// close or overwrite the first client's room.
func (p *Provider) CloneForSession() (provider.Provider, error) {
	p.mu.RLock()
	cfg := p.config
	p.mu.RUnlock()
	clone := &Provider{}
	if err := clone.Configure(cfg); err != nil {
		return nil, err
	}
	return clone, nil
}

func (p *Provider) ID() string                  { return "telemost" }
func (p *Provider) Type() provider.Type         { return provider.TypeVideoCall }
func (p *Provider) Category() provider.Category { return provider.CategoryVideo }
func (p *Provider) Version() string             { return "1.0.0" }

func (p *Provider) Configure(cfg provider.ProviderConfig) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	sc := sessionConfig{
		DisplayName: "WhiteTransport",
		Role:        "joiner",
	}
	if v, ok := cfg.Credentials["join_link"]; ok {
		sc.JoinLink = v
	}
	if v, ok := cfg.Endpoints["join_link"]; ok && sc.JoinLink == "" {
		sc.JoinLink = v
	}
	if v, ok := cfg.Credentials["cookie"]; ok {
		sc.Cookie = v
	}
	if v, ok := cfg.Settings["display_name"]; ok {
		sc.DisplayName = fmt.Sprint(v)
	}
	if v, ok := cfg.Settings["role"]; ok {
		sc.Role = fmt.Sprint(v)
	}

	p.config = cfg
	p.sessCfg = sc
	p.resetReceiverLocked()
	p.metrics = provider.Metrics{}
	p.health = provider.Health{LastCheck: time.Now()}
	p.startEgress = p.startEgressImpl
	return nil
}

func (p *Provider) resetReceiverLocked() {
	if p.done != nil {
		close(p.done)
	}
	p.done = make(chan struct{})
	p.recvCh = make(chan []byte, 64)
	recvCh := p.recvCh
	p.onDataFn = func(b []byte) {
		if len(b) == 0 || recvCh == nil {
			return
		}
		log.Printf("telemost-provider: enqueue inbound bytes=%d", len(b))
		cp := append([]byte(nil), b...)
		select {
		case recvCh <- cp:
		default:
		}
	}
}

func (p *Provider) GetSchema() provider.Schema {
	return provider.Schema{
		Name:        "Telemost",
		Description: "Yandex Telemost video call provider via whitelist-bypass upstream",
		Version:     "1.0.0",
		Fields: []provider.Field{
			{Name: "join_link", Type: "string", Required: true, Description: "Telemost conference join link or URL"},
			{Name: "cookie", Type: "string", Required: false, Description: "Yandex auth cookie for creator role"},
			{Name: "display_name", Type: "string", Required: false, Description: "Display name", Default: "WhiteTransport"},
			{Name: "role", Type: "string", Required: false, Description: "creator or joiner", Default: "joiner"},
		},
	}
}

// CreateAndStartEgress starts a Telemost session as creator.
// NOTE: The Telemost creator requires WebSocket signaling from a browser hook.
// This method starts the creator-side TelemostClient and returns a placeholder
// address. The actual signaling must be wired via HandleSignaling().
// For production use, prefer the joiner role or use the whitelist-bypass
// desktop app's browser hook for signaling.
func (p *Provider) CreateAndStartEgress(ctx context.Context) (string, error) {
	p.mu.RLock()
	joinLink := strings.TrimSpace(p.sessCfg.JoinLink)
	start := p.startEgress
	p.mu.RUnlock()
	if joinLink == "" {
		return "", fmt.Errorf("telemost provider: join_link required; upstream relay no longer creates conferences from a cookie")
	}
	if start == nil {
		start = p.startEgressImpl
	}
	if err := start(ctx, joinLink); err != nil {
		return "", err
	}
	return joinLink, nil
}

// StartEgressAddr implements runtime.VideoTunnelAdapter by delegating to StartEgress.
func (p *Provider) StartEgressAddr(ctx context.Context, addr string) error {
	return p.StartEgress(ctx, addr)
}

// StartEgress joins a Telemost conference as a headless joiner.
// The addr can be a join link ID, telemost://<id>, or full URL.
func (p *Provider) StartEgress(ctx context.Context, addr string) error {
	return p.startEgressImpl(ctx, addr)
}

func (p *Provider) startEgressImpl(ctx context.Context, addr string) error {
	p.mu.Lock()
	if p.hj != nil {
		p.mu.Unlock()
		return fmt.Errorf("telemost provider: joiner already active")
	}
	if p.recvCh == nil {
		p.resetReceiverLocked()
	}

	joinLink := extractJoinLinkID(addr)
	if strings.HasPrefix(strings.TrimSpace(addr), "http://") || strings.HasPrefix(strings.TrimSpace(addr), "https://") {
		joinLink = strings.TrimSpace(addr)
	}
	displayName := p.sessCfg.DisplayName
	if displayName == "" {
		displayName = "WhiteTransport"
	}

	hj := joiner.NewTelemostHeadlessJoiner(
		log.Printf,
		func(hostname string) (string, error) {
			ips, err := net.LookupIP(hostname)
			if err != nil {
				return "", err
			}
			for _, ip := range ips {
				if v4 := ip.To4(); v4 != nil {
					return v4.String(), nil
				}
			}
			if len(ips) > 0 {
				return ips[0].String(), nil
			}
			return "", fmt.Errorf("telemost provider: no IPs for %s", hostname)
		},
		nopStatus{}, // status: discard
		nil,         // pcConfig: nil = default settings
		pionrt.AddTunnelTracks,
		pionrt.ReadTrack,
	)
	hj.OnConnected = func(tun tunnel.DataTunnel) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.hj == hj {
			p.setTunnelLocked(tun)
		}
	}

	p.hj = hj
	done := p.done
	p.mu.Unlock()

	params, _ := json.Marshal(map[string]interface{}{
		"joinLink":    joinLink,
		"displayName": displayName,
		"vp8Fps":      telemostVP8FPS,
		"vp8Batch":    telemostVP8Batch,
	})
	go hj.RunWithParams(string(params))

	// Wait briefly for tunnel connection.
	deadline := time.NewTimer(telemostConnectTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			p.closeRoom(hj)
			return fmt.Errorf("telemost: tunnel connect timeout")
		case <-ctx.Done():
			p.closeRoom(hj)
			return ctx.Err()
		case <-done:
			return errReceiverClosed
		case <-tick.C:
			p.mu.RLock()
			connected := p.hj == hj && p.tunnel != nil
			p.mu.RUnlock()
			if connected {
				return nil
			}
		}
	}
}

func (p *Provider) setTunnelLocked(tun tunnel.DataTunnel) {
	rejoined := p.tunnel != nil
	if p.peerReady != nil {
		close(p.peerReady)
	}
	p.tunnel = tun
	p.peerReady = make(chan struct{})
	tun.SetOnData(func(data []byte) { p.handleTunnelData(tun, data) })
	if p.onCloseFn != nil {
		tun.SetOnClose(p.onCloseFn)
	}
	if rejoined {
		// The upstream joiner remembers the previous ACK across its internal
		// reconnect. Confirm the replacement media path independently.
		go confirmRejoinedPeer(tun, p.peerReady, p.done)
	}
}

func confirmRejoinedPeer(tun tunnel.DataTunnel, ready, done <-chan struct{}) {
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ready:
			return
		case <-done:
			return
		case <-tick.C:
			tun.SendData(tunnel.EncodeVP8Config(telemostVP8FPS, telemostVP8Batch, 1))
		}
	}
}

func (p *Provider) handleTunnelData(tun tunnel.DataTunnel, data []byte) {
	p.mu.RLock()
	if p.tunnel != tun {
		p.mu.RUnlock()
		return
	}
	onData := p.onDataFn
	p.mu.RUnlock()
	if p.handleControlFrame(tun, data) {
		return
	}
	if onData != nil {
		onData(data)
	}
}

func (p *Provider) handleControlFrame(tun tunnel.DataTunnel, data []byte) bool {
	controlOnly := false
	tunnel.DecodeFrames(data, func(connID uint32, msgType byte, payload []byte) {
		if connID != tunnel.ControlConnID {
			return
		}
		switch msgType {
		case tunnel.MsgConfig:
			controlOnly = true
			fps, batch, _, ok := tunnel.DecodeVP8Config(payload)
			if !ok {
				return
			}
			tun.Reconfigure(fps, batch)
			tun.SendData(tunnel.EncodeFrame(tunnel.ControlConnID, tunnel.MsgConfigAck, nil))
		case tunnel.MsgConfigAck:
			controlOnly = true
			p.mu.Lock()
			if p.tunnel == tun && p.peerReady != nil {
				if p.hj != nil {
					p.hj.MarkConfigAcked()
				}
				close(p.peerReady)
				p.peerReady = nil
				log.Print("telemost-provider: peer confirmed VP8 media")
			}
			p.mu.Unlock()
		}
	})
	return controlOnly
}

func (p *Provider) Close() error {
	p.closeRoom(nil)
	return nil
}

func (p *Provider) closeRoom(expected *joiner.TelemostHeadlessJoiner) {
	p.mu.Lock()
	if expected != nil && p.hj != expected {
		p.mu.Unlock()
		return
	}
	hj := p.hj
	p.hj = nil
	p.tunnel = nil
	p.peerReady = nil
	if p.done != nil {
		close(p.done)
		p.done = nil
	}
	p.recvCh = nil
	p.onDataFn = nil
	p.onCloseFn = nil
	p.mu.Unlock()
	if hj != nil {
		hj.Close()
	}
}

func (p *Provider) SetOnData(fn func([]byte)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	recvCh := p.recvCh
	p.onDataFn = func(b []byte) {
		if len(b) > 0 && recvCh != nil {
			cp := append([]byte(nil), b...)
			select {
			case recvCh <- cp:
			default:
			}
		}
		if fn != nil {
			fn(b)
		}
	}
	if p.tunnel != nil {
		tun := p.tunnel
		tun.SetOnData(func(data []byte) { p.handleTunnelData(tun, data) })
	}
}

func (p *Provider) SetOnClose(fn func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onCloseFn = fn
	if p.tunnel != nil {
		p.tunnel.SetOnClose(fn)
	}
}

func (p *Provider) Send(ctx context.Context, payload []byte) error {
	p.mu.RLock()
	tun, ready, done := p.tunnel, p.peerReady, p.done
	p.mu.RUnlock()
	if tun == nil {
		return fmt.Errorf("telemost provider: tunnel not connected")
	}
	if ready != nil {
		wait, cancel := context.WithTimeout(ctx, telemostPeerReadyTimeout)
		defer cancel()
		select {
		case <-ready:
		case <-done:
			return errReceiverClosed
		case <-wait.Done():
			return fmt.Errorf("telemost provider: waiting for peer VP8 acknowledgement: %w", wait.Err())
		}
		p.mu.RLock()
		current := p.tunnel == tun
		p.mu.RUnlock()
		if !current {
			return fmt.Errorf("telemost provider: VP8 tunnel changed before acknowledgement")
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tun.SendData(payload)
	return nil
}

func (p *Provider) Receive(ctx context.Context) ([]byte, error) {
	p.mu.RLock()
	recvCh := p.recvCh
	done := p.done
	p.mu.RUnlock()
	if recvCh == nil {
		return nil, errReceiverClosed
	}
	select {
	case b := <-recvCh:
		select {
		case <-done:
			return nil, errReceiverClosed
		default:
		}
		log.Printf("telemost-provider: receive bytes=%d", len(b))
		return b, nil
	case <-done:
		return nil, errReceiverClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *Provider) Health() provider.Health {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.health
}

func (p *Provider) GetLimits() provider.Limits {
	return provider.Limits{
		MaxPayloadBytes:  32768,
		MaxRatePerMinute: 0,
		MaxDailyBytes:    0,
	}
}

func (p *Provider) GetMetrics() provider.Metrics {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.metrics
}

func (p *Provider) UpdateMetrics(m provider.Metrics) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.metrics = m
}

func (p *Provider) Load() error   { return nil }
func (p *Provider) Unload() error { return p.Close() }

// extractJoinLinkID extracts the conference ID from various Telemost URL formats.
func extractJoinLinkID(input string) string {
	s := input
	for _, prefix := range []string{"telemost://", "https://", "http://"} {
		if len(s) > len(prefix) && s[:len(prefix)] == prefix {
			s = s[len(prefix):]
		}
	}
	s = trimPrefix(s, "telemost.yandex.ru/")
	s = trimPrefix(s, "meet.yandex.ru/")
	if idx := indexOf(s, "?"); idx >= 0 {
		s = s[:idx]
	}
	return s
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func trimPrefix(s, prefix string) string {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):]
	}
	return s
}

// nopStatus discards status updates from the headless joiner.
type nopStatus struct{}

func (nopStatus) EmitStatus(string)      {}
func (nopStatus) EmitStatusError(string) {}

// ExtractJoinLinkID is exported for use by other packages.
func ExtractJoinLinkID(addr string) string {
	return extractJoinLinkID(addr)
}

// Ensure webrtc import is used (for type references in deps).
var _ *webrtc.PeerConnection
