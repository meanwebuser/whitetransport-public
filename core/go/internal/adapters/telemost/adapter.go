package telemost

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	pionrt "whitelist-bypass/relay/pion"
	joiner "whitelist-bypass/relay/pion/headless-joiner-common"
	telemostapi "whitelist-bypass/relay/telemost"
	"whitelist-bypass/relay/tunnel"

	"github.com/meanwebuser/whitetransport/core/internal/provider"
)

// Adapter wraps the upstream whitelist-bypass/relay Telemost provider as a
// thin WhiteTransport provider. It supports the joiner role via the headless
// joiner, and the creator role via the TelemostClient WS signaling handler.
type Provider struct {
	mu        sync.RWMutex
	config    provider.ProviderConfig
	sessCfg   sessionConfig
	hj        *joiner.TelemostHeadlessJoiner
	tunnel    tunnel.DataTunnel
	recvCh    chan []byte
	onDataFn  func([]byte)
	onCloseFn func()
	metrics   provider.Metrics
	health    provider.Health
	done      chan struct{}
}

type sessionConfig struct {
	JoinLink    string
	Cookie      string
	DisplayName string
	Role        string // "creator" or "joiner"
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
	p.metrics = provider.Metrics{}
	p.health = provider.Health{LastCheck: time.Now()}
	return nil
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
	p.mu.Lock()
	defer p.mu.Unlock()

	joinLink := strings.TrimSpace(p.sessCfg.JoinLink)
	if joinLink != "" {
		return joinLink, nil
	}
	if strings.TrimSpace(p.sessCfg.Cookie) == "" {
		return "", fmt.Errorf("telemost provider: join_link or cookie required for creator role")
	}
	conferenceURI, err := telemostapi.CreateConference(p.sessCfg.Cookie)
	if err != nil {
		return "", fmt.Errorf("telemost provider: create conference: %w", err)
	}
	return conferenceURI, nil
}

// StartEgressAddr implements runtime.VideoTunnelAdapter by delegating to StartEgress.
func (p *Provider) StartEgressAddr(ctx context.Context, addr string) error {
	return p.StartEgress(ctx, addr)
}

// StartEgress joins a Telemost conference as a headless joiner.
// The addr can be a join link ID, telemost://<id>, or full URL.
func (p *Provider) StartEgress(ctx context.Context, addr string) error {
	p.mu.Lock()
	if p.hj != nil {
		p.mu.Unlock()
		return fmt.Errorf("telemost provider: joiner already active")
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
		p.tunnel = tun
		if p.onDataFn != nil {
			tun.SetOnData(p.onDataFn)
		}
		if p.onCloseFn != nil {
			tun.SetOnClose(p.onCloseFn)
		}
		p.mu.Unlock()
	}

	p.hj = hj
	p.mu.Unlock()

	params, _ := json.Marshal(map[string]interface{}{
		"joinLink":    joinLink,
		"displayName": displayName,
		"vp8Fps":      2,
		"vp8Batch":    1,
	})
	go hj.RunWithParams(string(params))

	// Wait briefly for tunnel connection.
	deadline := time.After(30 * time.Second)
	for {
		select {
		case <-deadline:
			p.mu.Lock()
			if p.hj == hj {
				hj.Close()
				p.hj = nil
			}
			p.mu.Unlock()
			return fmt.Errorf("telemost: tunnel connect timeout")
		case <-ctx.Done():
			p.mu.Lock()
			if p.hj == hj {
				hj.Close()
				p.hj = nil
			}
			p.mu.Unlock()
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
			p.mu.RLock()
			connected := p.tunnel != nil
			p.mu.RUnlock()
			if connected {
				return nil
			}
		}
	}
}

func (p *Provider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.hj != nil {
		p.hj.Close()
		p.hj = nil
	}
	p.tunnel = nil
	p.recvCh = nil
	p.onDataFn = nil
	p.onCloseFn = nil
	return nil
}

func (p *Provider) SetOnData(fn func([]byte)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onDataFn = func(b []byte) {
		if len(b) > 0 && p.recvCh != nil {
			cp := append([]byte(nil), b...)
			select {
			case p.recvCh <- cp:
			default:
			}
		}
		if fn != nil {
			fn(b)
		}
	}
	if p.tunnel != nil {
		p.tunnel.SetOnData(p.onDataFn)
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
	t := p.tunnel
	p.mu.RUnlock()
	if t == nil {
		return fmt.Errorf("telemost provider: tunnel not connected")
	}
	t.SendData(payload)
	return nil
}

func (p *Provider) Receive(ctx context.Context) ([]byte, error) {
	p.mu.RLock()
	recvCh := p.recvCh
	p.mu.RUnlock()
	if recvCh == nil {
		return nil, fmt.Errorf("telemost provider: receive channel closed")
	}
	select {
	case b := <-recvCh:
		log.Printf("telemost-provider: receive bytes=%d", len(b))
		return b, nil
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
