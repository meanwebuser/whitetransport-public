package vkcall

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	pionrt "whitelist-bypass/relay/pion"
	joiner "whitelist-bypass/relay/pion/headless-joiner-common"
	"whitelist-bypass/relay/tunnel"
	upstream "whitelist-bypass/relay/vkcall"

	"github.com/meanwebuser/whitetransport/core/internal/provider"
)

const (
	defaultAppID      = "6287487"
	defaultAPIVersion = "5.282"
)

// Provider is a thin WhiteTransport wrapper around the upstream VK Call HTTP
// API and headless Pion joiner. It never accepts browser cookies from the
// config structure; the runtime supplies them through a TokenStore binding.
type Provider struct {
	mu      sync.RWMutex
	config  provider.ProviderConfig
	sessCfg sessionConfig
	hj      vkJoiner
	tunnel  tunnel.DataTunnel
	recvCh  chan []byte
	onData  func([]byte)
	onClose func()
	metrics provider.Metrics
	health  provider.Health

	callClientFactory func(sessionConfig) (callClient, error)
	joinerFactory     func(func(tunnel.DataTunnel)) vkJoiner
}

type sessionConfig struct {
	Cookie          string
	JoinLink        string
	PeerID          string
	AppID           string
	APIVersion      string
	AppVersion      string
	ProtocolVersion string
	TunnelMode      string
	VP8FPS          int
	VP8Batch        int
	DualTrack       bool
}

type callClient interface {
	JoinExisting(context.Context, string, string) (*upstream.CallInfo, error)
	CreateAndJoin(context.Context, string, string) (*upstream.CallInfo, error)
}

type vkJoiner interface {
	RunWithParams(string)
	Close()
}

func (p *Provider) ID() string                  { return "vkcall" }
func (p *Provider) Type() provider.Type         { return provider.TypeVideoCall }
func (p *Provider) Category() provider.Category { return provider.CategoryVideo }
func (p *Provider) Version() string             { return "1.0.0" }

func (p *Provider) Configure(cfg provider.ProviderConfig) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	sc := sessionConfig{
		AppID:      defaultAppID,
		APIVersion: defaultAPIVersion,
		TunnelMode: "video",
		VP8FPS:     2,
		VP8Batch:   1,
	}
	sc.Cookie = strings.TrimSpace(cfg.Credentials["cookie"])
	sc.JoinLink = strings.TrimSpace(cfg.Credentials["join_link"])
	if sc.JoinLink == "" {
		sc.JoinLink = strings.TrimSpace(cfg.Endpoints["join_link"])
	}
	sc.PeerID = strings.TrimSpace(cfg.Credentials["peer_id"])
	if sc.PeerID == "" {
		sc.PeerID = strings.TrimSpace(cfg.Endpoints["address"])
	}
	if value, ok := cfg.Settings["app_id"]; ok {
		sc.AppID = fmt.Sprint(value)
	}
	if value, ok := cfg.Settings["api_version"]; ok {
		sc.APIVersion = fmt.Sprint(value)
	}
	if value, ok := cfg.Settings["app_version"]; ok {
		sc.AppVersion = fmt.Sprint(value)
	}
	if value, ok := cfg.Settings["protocol_version"]; ok {
		sc.ProtocolVersion = fmt.Sprint(value)
	}
	if value, ok := cfg.Settings["tunnel_mode"]; ok {
		sc.TunnelMode = fmt.Sprint(value)
	}
	if value, ok := cfg.Settings["vp8_fps"]; ok {
		sc.VP8FPS = intFromSetting(value, sc.VP8FPS)
	}
	if value, ok := cfg.Settings["vp8_batch"]; ok {
		sc.VP8Batch = intFromSetting(value, sc.VP8Batch)
	}
	if raw, ok := cfg.Settings["dual_track"]; ok {
		if value, ok := raw.(bool); ok {
			sc.DualTrack = value
		}
	}
	if sc.Cookie == "" {
		return fmt.Errorf("vkcall provider: TokenStore cookie credential is required")
	}

	p.config = cfg
	p.sessCfg = sc
	p.tunnel = nil
	p.hj = nil
	p.recvCh = make(chan []byte, 64)
	recvCh := p.recvCh
	p.onData = func(data []byte) {
		if len(data) == 0 || recvCh == nil {
			return
		}
		copyData := append([]byte(nil), data...)
		select {
		case recvCh <- copyData:
		default:
		}
	}
	p.onClose = nil
	p.metrics = provider.Metrics{}
	p.health = provider.Health{LastCheck: time.Now()}
	return nil
}

func (p *Provider) GetSchema() provider.Schema {
	return provider.Schema{
		Name:        "VK Call",
		Description: "VK Call video provider via the upstream whitelist-bypass relay",
		Version:     p.Version(),
		Fields: []provider.Field{
			{Name: "join_link", Type: "string", Required: false, Description: "Existing VK Call link for joiner mode"},
			{Name: "peer_id", Type: "string", Required: false, Description: "Peer ID used to create a fresh call"},
			{Name: "cookie", Type: "string", Required: true, Description: "Role-scoped TokenStore browser cookie"},
			{Name: "vp8_fps", Type: "number", Required: false, Default: 2},
			{Name: "vp8_batch", Type: "number", Required: false, Default: 1},
		},
	}
}

// CreateAndStartEgress creates a fresh VK Call and starts the node-side
// headless tunnel. The returned link is passed as the session egress address.
func (p *Provider) CreateAndStartEgress(ctx context.Context) (string, error) {
	p.mu.RLock()
	sc := p.sessCfg
	p.mu.RUnlock()
	if sc.JoinLink != "" {
		client, err := p.newCallClient(sc)
		if err != nil {
			return "", err
		}
		info, err := client.JoinExisting(ctx, sc.Cookie, sc.JoinLink)
		if err != nil {
			return "", fmt.Errorf("vkcall join existing for node: %w", err)
		}
		if info == nil {
			return "", fmt.Errorf("vkcall provider: existing call returned no call info")
		}
		if err := p.startJoiner(ctx, info, false); err != nil {
			return "", err
		}
		return sc.JoinLink, nil
	}
	if sc.PeerID == "" {
		return "", fmt.Errorf("vkcall provider: peer_id is required to create a call")
	}
	client, err := p.newCallClient(sc)
	if err != nil {
		return "", err
	}
	info, err := client.CreateAndJoin(ctx, sc.Cookie, sc.PeerID)
	if err != nil {
		return "", fmt.Errorf("vkcall create and join: %w", err)
	}
	if info == nil {
		return "", fmt.Errorf("vkcall provider: create returned no call info")
	}
	if strings.TrimSpace(info.JoinLink) == "" {
		return "", fmt.Errorf("vkcall provider: created call has no join link")
	}
	if err := p.startJoiner(ctx, info, false); err != nil {
		return "", err
	}
	return info.JoinLink, nil
}

// StartEgressAddr joins a session-provided VK Call address as the client side.
func (p *Provider) StartEgressAddr(ctx context.Context, addr string) error {
	joinLink := strings.TrimSpace(addr)
	if joinLink == "" {
		p.mu.RLock()
		joinLink = p.sessCfg.JoinLink
		p.mu.RUnlock()
	}
	if joinLink == "" {
		return fmt.Errorf("vkcall provider: join_link is required")
	}
	p.mu.RLock()
	sc := p.sessCfg
	p.mu.RUnlock()
	client, err := p.newCallClient(sc)
	if err != nil {
		return err
	}
	info, err := client.JoinExisting(ctx, sc.Cookie, joinLink)
	if err != nil {
		return fmt.Errorf("vkcall join existing: %w", err)
	}
	return p.startJoiner(ctx, info, true)
}

func (p *Provider) newCallClient(sc sessionConfig) (callClient, error) {
	p.mu.RLock()
	factory := p.callClientFactory
	p.mu.RUnlock()
	if factory != nil {
		return factory(sc)
	}
	return upstream.New(upstream.Config{
		AppID:           sc.AppID,
		APIVersion:      sc.APIVersion,
		AppVersion:      sc.AppVersion,
		ProtocolVersion: sc.ProtocolVersion,
	})
}

func (p *Provider) startJoiner(ctx context.Context, info *upstream.CallInfo, waitForTunnel bool) error {
	if info == nil {
		return fmt.Errorf("vkcall provider: upstream call info is missing")
	}
	p.mu.Lock()
	if p.hj != nil {
		p.mu.Unlock()
		return fmt.Errorf("vkcall provider: joiner already active")
	}
	sc := p.sessCfg
	factory := p.joinerFactory
	if factory == nil {
		factory = p.defaultJoinerFactory
	}
	hj := factory(p.connected)
	p.hj = hj
	p.mu.Unlock()

	auth := info.JoinerAuth()
	params, err := json.Marshal(joiner.VKHeadlessAuthParams{
		SessionKey: auth.SessionKey, ApplicationKey: auth.ApplicationKey, APIBaseURL: auth.APIBaseURL,
		JoinLink: auth.JoinLink, AnonymToken: auth.AnonymToken, AppVersion: auth.AppVersion, ProtocolVersion: auth.ProtocolVersion,
		TunnelMode: sc.TunnelMode, VP8FPS: sc.VP8FPS, VP8Batch: sc.VP8Batch, DualTrack: sc.DualTrack,
	})
	if err != nil {
		p.stopJoiner(hj)
		return fmt.Errorf("vkcall provider: encode join parameters: %w", err)
	}
	go hj.RunWithParams(string(params))
	if !waitForTunnel {
		return nil
	}

	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(25 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			p.stopJoiner(hj)
			return ctx.Err()
		case <-deadline.C:
			p.stopJoiner(hj)
			return fmt.Errorf("vkcall provider: tunnel connect timeout")
		case <-poll.C:
			if p.DataTunnel() != nil {
				return nil
			}
		}
	}
}

func (p *Provider) defaultJoinerFactory(onConnected func(tunnel.DataTunnel)) vkJoiner {
	hj := joiner.NewVKHeadlessJoiner(log.Printf, resolveIP, nopStatus{}, nil, pionrt.AddTunnelTracks, pionrt.ReadTrack)
	hj.OnConnected = onConnected
	return hj
}

func (p *Provider) connected(tun tunnel.DataTunnel) {
	if tun == nil {
		return
	}
	p.mu.Lock()
	p.tunnel = tun
	if p.onData != nil {
		tun.SetOnData(p.onData)
	}
	if p.onClose != nil {
		tun.SetOnClose(p.onClose)
	}
	p.health.LastCheck = time.Now()
	p.mu.Unlock()
}

func (p *Provider) stopJoiner(hj vkJoiner) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.hj == hj {
		hj.Close()
		p.hj = nil
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
	p.onData = nil
	p.onClose = nil
	return nil
}

func (p *Provider) SetOnData(callback func([]byte)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onData = func(data []byte) {
		if len(data) > 0 && p.recvCh != nil {
			copyData := append([]byte(nil), data...)
			select {
			case p.recvCh <- copyData:
			default:
			}
		}
		if callback != nil {
			callback(data)
		}
	}
	if p.tunnel != nil {
		p.tunnel.SetOnData(p.onData)
	}
}

func (p *Provider) SetOnClose(callback func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onClose = callback
	if p.tunnel != nil {
		p.tunnel.SetOnClose(callback)
	}
}

func (p *Provider) Send(_ context.Context, payload []byte) error {
	tun := p.DataTunnel()
	if tun == nil {
		return fmt.Errorf("vkcall provider: tunnel not connected")
	}
	tun.SendData(payload)
	return nil
}

func (p *Provider) Receive(ctx context.Context) ([]byte, error) {
	p.mu.RLock()
	recv := p.recvCh
	p.mu.RUnlock()
	if recv == nil {
		return nil, fmt.Errorf("vkcall provider: receive channel closed")
	}
	select {
	case data := <-recv:
		return data, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *Provider) DataTunnel() tunnel.DataTunnel {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.tunnel
}

func (p *Provider) Health() provider.Health    { p.mu.RLock(); defer p.mu.RUnlock(); return p.health }
func (p *Provider) GetLimits() provider.Limits { return provider.Limits{MaxPayloadBytes: 32768} }
func (p *Provider) GetMetrics() provider.Metrics {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.metrics
}
func (p *Provider) UpdateMetrics(metrics provider.Metrics) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.metrics = metrics
}
func (p *Provider) Load() error   { return nil }
func (p *Provider) Unload() error { return p.Close() }

func intFromSetting(value any, fallback int) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return fallback
	}
}

func resolveIP(hostname string) (string, error) {
	ips, err := net.LookupIP(hostname)
	if err != nil {
		return "", err
	}
	for _, ip := range ips {
		if ipv4 := ip.To4(); ipv4 != nil {
			return ipv4.String(), nil
		}
	}
	if len(ips) != 0 {
		return ips[0].String(), nil
	}
	return "", fmt.Errorf("vkcall provider: no IPs for %s", hostname)
}

type nopStatus struct{}

func (nopStatus) EmitStatus(string)      {}
func (nopStatus) EmitStatusError(string) {}

var _ provider.Provider = (*Provider)(nil)
