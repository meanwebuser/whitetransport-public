package dion

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"whitelist-bypass/relay/dion"
	joiner "whitelist-bypass/relay/pion/headless-joiner-common"
	"whitelist-bypass/relay/tunnel"

	"github.com/meanwebuser/whitetransport/core/internal/provider"
)

// Adapter wraps the upstream whitelist-bypass/relay/dion provider as a
// thin WhiteTransport provider. It supports both creator and joiner roles.
type Provider struct {
	mu        sync.RWMutex
	config    provider.ProviderConfig
	sessCfg   sessionConfig
	session   *dion.Session
	call      *dion.Call
	hj        *joiner.DionHeadlessJoiner
	tunnel    tunnel.DataTunnel
	recvCh    chan []byte
	onDataFn  func([]byte)
	onCloseFn func()
	metrics   provider.Metrics
	health    provider.Health
	done      chan struct{}

	// newRecoverySession is a test seam for the read-only background probe.
	// Production uses newDIONRecoverySession, which intentionally keeps all
	// credentials in an in-memory cookie jar and never loads a cookie file.
	newRecoverySession recoverySessionFactory
}

type sessionConfig struct {
	EventID      string
	AccessToken  string
	RefreshToken string
	CookiesFile  string
	DisplayName  string
	Role         string // "creator" or "joiner"
}

type recoverySession interface {
	EnsureValidToken() error
	WhoAmI() (json.RawMessage, error)
}

type recoverySessionFactory func(context.Context, sessionConfig) (recoverySession, error)

const defaultRecoveryProbeTimeout = 8 * time.Second

type recoveryProbeTransport struct {
	ctx  context.Context
	base http.RoundTripper
}

func (t recoveryProbeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.ctx.Err(); err != nil {
		return nil, err
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req.WithContext(t.ctx))
}

func (p *Provider) ID() string                  { return "dion" }
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
	if v, ok := cfg.Credentials["event_id"]; ok {
		sc.EventID = v
	}
	if v, ok := cfg.Endpoints["event_id"]; ok && sc.EventID == "" {
		sc.EventID = v
	}
	if v, ok := cfg.Credentials["access_token"]; ok {
		sc.AccessToken = v
	}
	if v, ok := cfg.Credentials["refresh_token"]; ok {
		sc.RefreshToken = v
	}
	if v, ok := cfg.Credentials["cookies_file"]; ok {
		sc.CookiesFile = v
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
		log.Printf("dion-adapter: enqueue inbound bytes=%d", len(b))
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
		Name:        "DION",
		Description: "DION video call provider via whitelist-bypass upstream",
		Version:     "1.0.0",
		Fields: []provider.Field{
			{Name: "event_id", Type: "string", Required: false, Description: "DION event slug or dion:// URL"},
			{Name: "access_token", Type: "string", Required: false, Description: "DION access token"},
			{Name: "cookies_file", Type: "string", Required: false, Description: "Path to cookies JSON file"},
			{Name: "display_name", Type: "string", Required: false, Description: "Display name", Default: "WhiteTransport"},
			{Name: "role", Type: "string", Required: false, Description: "creator or joiner", Default: "joiner"},
		},
	}
}

// SafeEgressRecoveryProbe verifies explicitly configured DION credentials on
// a fresh, in-memory session. It deliberately never loads CookiesFile (which
// can be updated by upstream refresh code), creates a room, starts a call, or
// sends an egress payload. Cookie-only and guest flows fail closed.
func (p *Provider) SafeEgressRecoveryProbe(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	p.mu.RLock()
	cfg := p.sessCfg
	newSession := p.newRecoverySession
	p.mu.RUnlock()
	if cfg.AccessToken == "" || cfg.RefreshToken == "" {
		return fmt.Errorf("dion recovery probe requires explicit access and refresh tokens")
	}
	// Do not let an injected test factory accidentally normalize cookie-file
	// access into the production path; the preflight is strictly in-memory.
	cfg.CookiesFile = ""
	if newSession == nil {
		newSession = newDIONRecoverySession
	}

	sess, err := newSession(ctx, cfg)
	if err != nil {
		return fmt.Errorf("dion recovery session: %w", err)
	}
	if err := runRecoveryStep(ctx, sess.EnsureValidToken); err != nil {
		return fmt.Errorf("dion recovery token validation: %w", err)
	}
	if err := runRecoveryStep(ctx, func() error {
		_, err := sess.WhoAmI()
		return err
	}); err != nil {
		return fmt.Errorf("dion recovery identity: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func runRecoveryStep(ctx context.Context, run func() error) error {
	done := make(chan error, 1)
	go func() { done <- run() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func newDIONRecoverySession(ctx context.Context, cfg sessionConfig) (recoverySession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	timeout := defaultRecoveryProbeTimeout
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline)
		if timeout <= 0 {
			return nil, context.DeadlineExceeded
		}
	}
	return newDIONRecoverySessionWithHTTPClient(ctx, cfg, &http.Client{Timeout: timeout})
}

func newDIONRecoverySessionWithHTTPClient(ctx context.Context, cfg sessionConfig, httpClient *http.Client) (recoverySession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	client := *httpClient
	client.Transport = recoveryProbeTransport{ctx: ctx, base: httpClient.Transport}
	sess, err := dion.NewSession(&client)
	if err != nil {
		return nil, err
	}
	sess.AccessToken = cfg.AccessToken
	if exp, err := parseDionJWTExpiry(cfg.AccessToken); err == nil {
		sess.AccessTokenExp = exp
	}
	sess.SetCookieInJar("vc-access-token", cfg.AccessToken)
	sess.SetCookieInJar("vc-refresh-token", cfg.RefreshToken)
	return sess, nil
}

// CreateAndStartEgress creates a new DION room as creator and starts the call.
// Returns "dion://<slug>" address.
func (p *Provider) CreateAndStartEgress(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.call != nil {
		return "", fmt.Errorf("dion provider: call already active")
	}

	// Create authenticated session.
	sess, err := dion.NewSession(nil)
	if err != nil {
		return "", fmt.Errorf("dion session: %w", err)
	}

	if p.sessCfg.CookiesFile != "" {
		if err := sess.LoadCookiesFromFile(p.sessCfg.CookiesFile); err != nil {
			log.Printf("[dion] load cookies failed: %v", err)
		}
	}

	// Override cookie jar with fresh tokens from config (overrides expired cookies).
	if p.sessCfg.AccessToken != "" {
		sess.AccessToken = p.sessCfg.AccessToken
		if exp, err := parseDionJWTExpiry(p.sessCfg.AccessToken); err == nil {
			sess.AccessTokenExp = exp
		}
		sess.SetCookieInJar("vc-access-token", p.sessCfg.AccessToken)
	}
	if p.sessCfg.RefreshToken != "" {
		sess.SetCookieInJar("vc-refresh-token", p.sessCfg.RefreshToken)
	}

	// Ensure we have a valid token (refresh if needed).
	if sess.AccessToken == "" {
		if _, err := sess.RegisterAnonymousGuest(p.sessCfg.EventID, p.sessCfg.DisplayName); err != nil {
			return "", fmt.Errorf("dion anonymous guest: %w", err)
		}
	} else if err := sess.EnsureValidToken(); err != nil {
		log.Printf("[dion] EnsureValidToken failed: %v", err)
		return "", fmt.Errorf("dion ensure valid token: %w", err)
	}

	event, err := sess.CreateRoom()
	if err != nil {
		return "", fmt.Errorf("dion create room: %w", err)
	}

	obf, err := tunnel.NewTunnelObfuscator(tunnel.DeriveSecretFromJoinLink(event.Slug))
	if err != nil {
		return "", fmt.Errorf("dion obfuscator: %w", err)
	}

	call := dion.NewCall(dion.CallConfig{
		Auth:        sess,
		Event:       event,
		Obfuscator:  obf,
		DisplayName: p.sessCfg.DisplayName,
		LogFn:       log.Printf,
		Role:        dion.RoleCreator,
	})

	tunCh := make(chan tunnel.DataTunnel, 1)
	call.OnConnected = func(tun tunnel.DataTunnel) {
		tunCh <- tun
	}

	if err := call.Start(); err != nil {
		call.Close()
		return "", fmt.Errorf("dion call start: %w", err)
	}

	// Wait for tunnel or context cancellation.
	select {
	case tun := <-tunCh:
		p.session = sess
		p.call = call
		p.tunnel = tun
		if p.onDataFn != nil {
			tun.SetOnData(p.onDataFn)
		}
		if p.onCloseFn != nil {
			tun.SetOnClose(p.onCloseFn)
		}
		return "dion://" + event.Slug, nil
	case <-ctx.Done():
		call.Close()
		return "", ctx.Err()
	case <-time.After(30 * time.Second):
		call.Close()
		return "", fmt.Errorf("dion: tunnel connect timeout")
	}
}

// StartEgressAddr implements runtime.VideoTunnelAdapter by delegating to StartEgress.
func (p *Provider) StartEgressAddr(ctx context.Context, addr string) error {
	return p.StartEgress(ctx, addr)
}

// StartEgress joins an existing DION room as a guest via the upstream
// DionHeadlessJoiner. The addr can be a slug, dion://<slug>, or full URL.
func (p *Provider) StartEgress(ctx context.Context, addr string) error {
	p.mu.Lock()
	if p.hj != nil {
		p.mu.Unlock()
		return fmt.Errorf("dion provider: joiner already active")
	}

	slug := normalizeSlug(addr)
	displayName := p.sessCfg.DisplayName
	if displayName == "" {
		displayName = "WhiteTransport"
	}

	hj := joiner.NewDionHeadlessJoiner(log.Printf, nil, nopStatus{}, nil)
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

	params := fmt.Sprintf(`{"roomId":%q,"displayName":%q}`, slug, displayName)
	go hj.RunWithParams(params)

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
			return fmt.Errorf("dion: tunnel connect timeout")
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

	if p.call != nil {
		p.call.Close()
		p.call = nil
	}
	if p.hj != nil {
		p.hj.Close()
		p.hj = nil
	}
	p.tunnel = nil
	p.recvCh = nil
	p.onDataFn = nil
	p.onCloseFn = nil
	p.session = nil
	return nil
}

// Done returns a channel that closes when the underlying call ends.
func (p *Provider) Done() <-chan struct{} {
	p.mu.RLock()
	call := p.call
	p.mu.RUnlock()
	if call != nil {
		return call.Done()
	}
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
		return fmt.Errorf("dion provider: tunnel not connected")
	}
	t.SendData(payload)
	return nil
}

func (p *Provider) Receive(ctx context.Context) ([]byte, error) {
	p.mu.RLock()
	recvCh := p.recvCh
	p.mu.RUnlock()
	if recvCh == nil {
		return nil, fmt.Errorf("dion provider: receive channel closed")
	}
	select {
	case b := <-recvCh:
		log.Printf("dion-adapter: receive bytes=%d", len(b))
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

// normalizeSlug extracts the slug from various URL formats.
func normalizeSlug(input string) string {
	s := strings.TrimSpace(input)
	for _, prefix := range []string{"dion://", "https://", "http://"} {
		if strings.HasPrefix(s, prefix) {
			s = s[len(prefix):]
		}
	}
	if idx := strings.Index(s, "?"); idx >= 0 {
		s = s[:idx]
	}
	s = strings.TrimPrefix(s, "dion.vc/")
	s = strings.TrimPrefix(s, "event/")
	if idx := strings.Index(s, "/"); idx >= 0 {
		s = s[:idx]
	}
	return s
}

// nopStatus discards status updates from the headless joiner.
type nopStatus struct{}

func (nopStatus) EmitStatus(string)      {}
func (nopStatus) EmitStatusError(string) {}

// ExtractEventID parses "dion://<slug>" or a DION URL and returns the slug.
func ExtractEventID(addr string) string {
	return normalizeSlug(addr)
}

// parseDionJWTExpiry extracts the exp claim from a JWT token.
func parseDionJWTExpiry(token string) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}, fmt.Errorf("invalid JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("decode payload: %w", err)
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, fmt.Errorf("parse claims: %w", err)
	}
	if claims.Exp == 0 {
		return time.Time{}, fmt.Errorf("no exp claim")
	}
	return time.Unix(claims.Exp, 0), nil
}
