package whitelist

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"whitelist-bypass/relay/tunnel"
	"whitelist-bypass/relay/wbstream"

	"github.com/meanwebuser/whitetransport/core/internal/provider"
)

var newWBStreamSession = wbstream.NewSession

var startWBStreamSession = func(s *wbstream.Session) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("wbstream start panic: %v", r)
		}
	}()
	return s.Start()
}

type Health struct {
	VPNRateLimited    bool      `json:"vpn_rate_limited"`
	VPNQuotaExhausted bool      `json:"vpn_quota_exhausted"`
	VPNSessionActive  bool      `json:"vpn_session_active"`
	VPNEstablished    time.Time `json:"vpn_established,omitempty"`
}

type sessionConfig struct {
	DisplayName string
	ServerURL   string
	RoomID      string
	RoomToken   string
	AccessToken string
	TunnelMode  string
	Reliable    bool
}

type Provider struct {
	mu             sync.RWMutex
	config         provider.ProviderConfig
	sessCfg        sessionConfig
	session        *wbstream.Session
	tunnel         tunnel.DataTunnel
	lastRoomID     string   // tracks the room ID of the last session started
	networkOrigins []string // remote ICE peer origins observed for system-VPN bypass
	onDataFn       func([]byte)
	onCloseFn      func()
	metrics        provider.Metrics
	health         provider.Health
	wlHealth       Health
	done           chan struct{}
}

func (p *Provider) ID() string                  { return "wbstream" }
func (p *Provider) Type() provider.Type         { return provider.TypeVideoCall }
func (p *Provider) Category() provider.Category { return provider.CategoryVideo }
func (p *Provider) Version() string             { return "1.0.0" }

func (p *Provider) Configure(cfg provider.ProviderConfig) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	sc := sessionConfig{
		DisplayName: "WhiteTransport",
		TunnelMode:  "dc",
	}
	if dn, ok := cfg.Settings["display_name"]; ok {
		sc.DisplayName = fmt.Sprint(dn)
	}
	if token, ok := cfg.Credentials["room_token"]; ok {
		sc.RoomToken = token
	}
	if url, ok := cfg.Endpoints["server_url"]; ok {
		sc.ServerURL = url
	}
	if roomID, ok := cfg.Endpoints["room_id"]; ok {
		sc.RoomID = roomID
	}
	if accessToken, ok := cfg.Credentials["access_token"]; ok {
		sc.AccessToken = accessToken
	}
	if mode, ok := cfg.Settings["tunnel_mode"]; ok {
		sc.TunnelMode = fmt.Sprint(mode)
	}
	if reliable, ok := cfg.Settings["reliable"].(bool); ok {
		sc.Reliable = reliable
	}

	p.config = cfg
	p.sessCfg = sc
	p.done = make(chan struct{})
	p.metrics = provider.Metrics{}
	p.health = provider.Health{LastCheck: time.Now()}
	p.wlHealth = Health{}
	return nil
}

func (p *Provider) GetSchema() provider.Schema {
	return provider.Schema{
		Name:        "WBStream",
		Description: "WBStream relay tunnel provider (LiveKit DataChannel)",
		Version:     "1.0.0",
		Fields: []provider.Field{
			{Name: "room_token", Type: "string", Required: false, Description: "WBStream room token"},
			{Name: "access_token", Type: "string", Required: false, Description: "WBStream access token"},
			{Name: "server_url", Type: "string", Required: true, Description: "LiveKit server URL"},
			{Name: "room_id", Type: "string", Required: false, Description: "Room ID for existing room"},
			{Name: "display_name", Type: "string", Required: false, Description: "Display name", Default: "WhiteTransport"},
			{Name: "tunnel_mode", Type: "string", Required: false, Description: "Tunnel mode (dc, video)", Default: "dc"},
			{Name: "reliable", Type: "boolean", Required: false, Description: "Enable upstream reliable MultiTrack KCP when a multi-track WBStream tunnel is negotiated", Default: false},
		},
	}
}

// Start starts the adapter with the config stored during Configure().
// If the adapter is configured for egress (via StartEgress), use that instead.
func (p *Provider) Start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.startLocked(ctx)
}

func (p *Provider) startLocked(ctx context.Context) error {
	if p.session != nil {
		// If a new room was configured (e.g. StartEgressAddr for a new session),
		// reset the old session so a fresh one is created. Otherwise the stale
		// tunnel from the previous session is returned by DataTunnel().
		if p.sessCfg.RoomID != "" && p.lastRoomID != "" && p.sessCfg.RoomID != p.lastRoomID {
			log.Printf("[wbstream] room changed %s -> %s, resetting session", p.lastRoomID, p.sessCfg.RoomID)
			p.session.Close()
			p.session = nil
			p.tunnel = nil
			p.networkOrigins = nil
			// Preserve onDataFn/onCloseFn across room transitions: the tunnel
			// layer sets these via SetOnData/SetOnClose and expects them to
			// persist across reconnections (see DataTunnelAdapter interface).
			p.wlHealth = Health{}
		} else {
			return nil // already started, same room
		}
	}

	sc := p.sessCfg
	wbCfg := wbstream.SessionConfig{
		ServerURL:   sc.ServerURL,
		RoomToken:   sc.RoomToken,
		DisplayName: sc.DisplayName,
		TunnelMode:  sc.TunnelMode,
		Reliable:    sc.Reliable,
		RoomID:      sc.RoomID,
		AccessToken: sc.AccessToken,
		LogFn:       log.Printf,
	}

	s := newWBStreamSession(wbCfg)
	s.OnRemoteCandidate = func(_ int, candidateOrSDP string) {
		p.recordRemoteCandidate(candidateOrSDP)
	}
	s.OnConnected = func(dt tunnel.DataTunnel) {
		p.mu.Lock()
		p.tunnel = dt
		if p.onDataFn != nil {
			dt.SetOnData(p.onDataFn)
		}
		if p.onCloseFn != nil {
			dt.SetOnClose(p.onCloseFn)
		}
		p.wlHealth.VPNSessionActive = true
		p.wlHealth.VPNEstablished = time.Now()
		p.mu.Unlock()
	}
	p.session = s
	p.lastRoomID = sc.RoomID
	if err := startWBStreamSession(s); err != nil {
		s.Close()
		p.session = nil
		p.tunnel = nil
		p.lastRoomID = ""
		p.wlHealth.VPNSessionActive = false
		return err
	}
	go p.watchSessionDone(s, sc.RoomID)
	return nil
}

func (p *Provider) watchSessionDone(s *wbstream.Session, roomID string) {
	<-s.Done()
	p.handleSessionDone(s, roomID)
}

func (p *Provider) handleSessionDone(s *wbstream.Session, roomID string) {
	p.mu.Lock()
	if p.session != s {
		p.mu.Unlock()
		return
	}
	p.session = nil
	p.tunnel = nil
	p.lastRoomID = ""
	p.networkOrigins = nil
	p.wlHealth.VPNSessionActive = false
	// Clear per-session fields (room ID/token) but preserve adapter-level
	// config (AccessToken, DisplayName) so the adapter can reconnect to a
	// different room without being re-Configure()'d.
	p.sessCfg.RoomID = ""
	p.sessCfg.RoomToken = ""
	p.sessCfg.ServerURL = ""
	onClose := p.onCloseFn
	p.mu.Unlock()

	log.Printf("[wbstream] session closed room=%s", roomID)
	if onClose != nil {
		onClose()
	}
}

// StartEgress starts the adapter as an egress endpoint by connecting to a
// specific WBStream room. Call on both node (creator of room) and client
// (joiner of room). Overrides the room params from Configure().
func (p *Provider) StartEgress(ctx context.Context, serverURL, roomID, roomToken string) error {
	p.mu.Lock()
	p.sessCfg.ServerURL = serverURL
	p.sessCfg.RoomID = roomID
	p.sessCfg.RoomToken = roomToken
	p.mu.Unlock()
	return p.Start(ctx)
}

// SystemVPNNetworkOrigins returns the credential-free network origin used by
// the currently configured/active LiveKit session. The host VPN must exclude
// this exact dependency from its own route to avoid recursive transport.
func (p *Provider) SystemVPNNetworkOrigins() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	serverURL := strings.TrimSpace(p.sessCfg.ServerURL)
	if serverURL == "" {
		return nil
	}
	// The WBStream join response supplies this ICE/TURN host separately from
	// the LiveKit signaling URL. A full-tunnel host route must bypass both;
	// otherwise enabling the system VPN recursively captures the WebRTC path
	// and the data tunnel dies even though signaling initially connected.
	origins := []string{serverURL, "https://wb-stream-turn-1.wb.ru"}
	origins = append(origins, p.networkOrigins...)
	return origins
}

// extractCandidateIPs returns IP addresses from one remote ICE candidate or
// SDP blob. Only the candidate address field is accepted, so credentials and
// unrelated SDP tokens never become route dependencies.
func extractCandidateIPs(candidateOrSDP string) []string {
	seen := make(map[string]struct{})
	addresses := make([]string, 0)
	for _, line := range strings.Split(candidateOrSDP, "\n") {
		fields := strings.Fields(line)
		for index, field := range fields {
			field = strings.TrimPrefix(field, "a=")
			if !strings.HasPrefix(field, "candidate:") || index+4 >= len(fields) {
				continue
			}
			candidateAddress := strings.Trim(fields[index+4], "[]")
			ip := net.ParseIP(candidateAddress)
			if ip == nil {
				continue
			}
			canonical := ip.String()
			if _, exists := seen[canonical]; exists {
				continue
			}
			seen[canonical] = struct{}{}
			addresses = append(addresses, canonical)
			break
		}
	}
	return addresses
}

func (p *Provider) recordRemoteCandidate(candidateOrSDP string) {
	for _, address := range extractCandidateIPs(candidateOrSDP) {
		originHost := address
		if strings.Contains(address, ":") {
			originHost = "[" + address + "]"
		}
		origin := "https://" + originHost
		p.mu.Lock()
		seen := false
		for _, existing := range p.networkOrigins {
			if existing == origin {
				seen = true
				break
			}
		}
		if !seen {
			p.networkOrigins = append(p.networkOrigins, origin)
		}
		p.mu.Unlock()
	}
}

// StartEgressAddr connects to a WBStream room given a wbstream://<roomID>
// address as the CLIENT/joiner. The client registers as a WBStream guest so it
// gets a participant identity that is unique and distinct from the node's
// account-bound identity. Joining with the same shared account token as the
// node would collide: the WBStream SFU enforces a unique participant identity
// per room and otherwise disconnects the node with `DUPLICATE_IDENTITY`,
// killing the egress DataTunnel before any SOCKS5 CONNECT frame is processed.
// The node (room creator/host) keeps using its account token via
// CreateAndStartEgress; only the client joiner uses a guest identity here.
func (p *Provider) StartEgressAddr(ctx context.Context, addr string) error {
	roomID := ExtractRoomID(addr)
	p.mu.RLock()
	displayName := p.sessCfg.DisplayName
	p.mu.RUnlock()
	if displayName == "" {
		displayName = "WhiteTransport"
	}

	// Register a fresh guest account so the client's room token carries a
	// unique identity. Never reuse the node's account token here: that would
	// collide with the node's identity and get the node kicked by the SFU.
	guestAccessToken, err := wbstream.RegisterGuest(nil, displayName)
	if err != nil {
		return fmt.Errorf("whitelist provider: register guest: %w", err)
	}
	if err := wbstream.JoinRoom(nil, guestAccessToken, roomID); err != nil {
		return fmt.Errorf("whitelist provider: join room: %w", err)
	}
	roomToken, surl, err := wbstream.GetConnectionDetails(nil, guestAccessToken, roomID, displayName)
	if err != nil {
		return fmt.Errorf("whitelist provider: get connection details: %w", err)
	}
	return p.StartEgress(ctx, surl, roomID, roomToken)
}

// CreateAndStartEgress creates a new WBStream room, starts the adapter as
// its creator, and returns the room URL (wbstream://<roomID>).
// Reads access token from the adapter's stored config; server URL and room
// token are fetched from the WBStream API (GetConnectionDetails).
func (p *Provider) CreateAndStartEgress(ctx context.Context) (string, error) {
	p.mu.RLock()
	displayName := p.sessCfg.DisplayName
	accessToken := p.sessCfg.AccessToken
	p.mu.RUnlock()

	if accessToken == "" {
		return "", fmt.Errorf("whitelist provider: access token required to create egress room")
	}
	roomID, err := wbstream.CreateRoom(nil, accessToken)
	if err != nil {
		return "", fmt.Errorf("whitelist provider: create room: %w", err)
	}

	if err := wbstream.JoinRoom(nil, accessToken, roomID); err != nil {
		return "", fmt.Errorf("whitelist provider: join room: %w", err)
	}

	roomToken, serverURL, err := wbstream.GetConnectionDetails(nil, accessToken, roomID, displayName)
	if err != nil {
		return "", fmt.Errorf("whitelist provider: get connection details: %w", err)
	}

	if err := p.StartEgress(ctx, serverURL, roomID, roomToken); err != nil {
		return "", fmt.Errorf("whitelist provider: start egress: %w", err)
	}

	return "wbstream://" + roomID, nil
}

// Close stops the underlying WBStream session and resets per-session state
// for reuse. Adapter-level config (AccessToken, DisplayName from Configure)
// is preserved so the adapter can start a new session without reconfiguration.
// Returns nil if already stopped or not started.
func (p *Provider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.session == nil {
		// Even if the session is already nil (e.g. handleSessionDone cleared
		// it), reset per-session fields so a stale RoomID/RoomToken from a
		// previous session does not leak into the next StartEgress call.
		p.sessCfg.RoomID = ""
		p.sessCfg.RoomToken = ""
		p.sessCfg.ServerURL = ""
		p.lastRoomID = ""
		p.networkOrigins = nil
		p.wlHealth = Health{}
		return nil
	}
	p.session.Close()
	p.session = nil
	p.tunnel = nil
	p.networkOrigins = nil
	p.onDataFn = nil
	p.onCloseFn = nil
	// Reset per-session fields but preserve AccessToken and DisplayName so
	// the adapter can reconnect to a different room. These are set once
	// during Configure() and must survive Close/Start cycles.
	p.sessCfg.RoomID = ""
	p.sessCfg.RoomToken = ""
	p.sessCfg.ServerURL = ""
	p.lastRoomID = ""
	p.wlHealth = Health{}
	return nil
}

// SetOnData sets the data callback on the underlying DataTunnel.
func (p *Provider) SetOnData(fn func([]byte)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onDataFn = fn
	if p.tunnel != nil {
		p.tunnel.SetOnData(fn)
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

func (p *Provider) DataTunnel() tunnel.DataTunnel {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.tunnel
}

func (p *Provider) SendData(data []byte) {
	p.mu.RLock()
	t := p.tunnel
	p.mu.RUnlock()
	if t != nil {
		t.SendData(data)
	}
}

func (p *Provider) Send(ctx context.Context, payload []byte) error {
	p.mu.RLock()
	t := p.tunnel
	p.mu.RUnlock()
	if t == nil {
		return fmt.Errorf("whitelist provider: tunnel not connected")
	}
	t.SendData(payload)
	return nil
}

func (p *Provider) Receive(ctx context.Context) ([]byte, error) {
	return nil, fmt.Errorf("whitelist provider: receive not supported, use SetOnData")
}

func (p *Provider) Health() provider.Health {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.health
}

func (p *Provider) WhitelistHealth() Health {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.wlHealth
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

func (p *Provider) Load() error { return nil }

func (p *Provider) Unload() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.session != nil {
		p.session.Close()
	}
	if p.done != nil {
		close(p.done)
	}
	return nil
}

// ExtractRoomID parses "wbstream://<roomID>" or a LiveKit room URL and
// returns the room ID.
func ExtractRoomID(roomURL string) string {
	roomURL = strings.TrimSpace(roomURL)
	if strings.HasPrefix(roomURL, "wbstream://") {
		return strings.TrimPrefix(roomURL, "wbstream://")
	}
	if u, err := url.Parse(roomURL); err == nil && u.Host != "" {
		return strings.TrimPrefix(u.Path, "/")
	}
	return roomURL
}
