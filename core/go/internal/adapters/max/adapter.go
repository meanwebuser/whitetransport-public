// Package max implements the narrow MAX call-signaling provider used by
// WhiteTransport. It was written independently against the public wire
// contract: MAX RPC login/call setup and call signaling carry an opaque,
// already-authenticated WhiteTransport provider envelope. It contains no
// OpenFlux source or dependency.
package max

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/meanwebuser/whitetransport/core/internal/provider"
	"github.com/pierrec/lz4/v4"
)

const (
	defaultWebSocketURL = "wss://ws-api.oneme.ru/websocket"
	maxRPCVersion       = 11
	maxLoginOpcode      = 19
	maxDeviceOpcode     = 6
	maxStartCallOpcode  = 78
	maxIncomingOpcode   = 137
	maxQueueSize        = 256
)

// Provider adapts a MAX call's signaling channel to provider.Provider. It is
// intentionally a stream provider: the policy/runtime layer supplies the
// authenticated WhiteTransport envelope, rather than this adapter inventing a
// second application encryption format.
type Provider struct {
	mu       sync.RWMutex
	config   maxConfig
	client   *rpcClient
	session  *signalSession
	recvCh   chan []byte
	cancel   context.CancelFunc
	metrics  provider.Metrics
	health   provider.Health
	running  bool
	newRPC   func(string, bool) (*rpcClient, error)
	deadline time.Duration
}

type maxConfig struct {
	token         string
	role          string
	peerID        int64
	webSocketURL  string
	allowInsecure bool
}

func (p *Provider) ID() string                  { return "max" }
func (p *Provider) Type() provider.Type         { return provider.TypeVideoCall }
func (p *Provider) Category() provider.Category { return provider.CategoryVideo }
func (p *Provider) Version() string             { return "1.0.0" }

// Configure accepts no inline fallback for production credentials. Runtime
// binds max_token from the role-specific TokenStore record before calling it.
func (p *Provider) Configure(cfg provider.ProviderConfig) error {
	token := strings.TrimSpace(cfg.Credentials["max_token"])
	if token == "" {
		return fmt.Errorf("max provider: max_token credential is required")
	}
	role := strings.TrimSpace(stringSetting(cfg.Settings, "role"))
	if role != "client" && role != "node" {
		return fmt.Errorf("max provider: role must be client or node")
	}
	peerID, err := int64Setting(cfg, "peer_id")
	if err != nil {
		return fmt.Errorf("max provider: %w", err)
	}
	if role == "client" && peerID <= 0 {
		return fmt.Errorf("max provider: peer_id is required for client role")
	}
	webSocketURL := strings.TrimSpace(cfg.Endpoints["websocket_url"])
	if webSocketURL == "" {
		webSocketURL = defaultWebSocketURL
	}
	allowInsecure := boolSetting(cfg.Settings, "allow_insecure_test_endpoint")
	if err := validateWebSocketURL(webSocketURL, allowInsecure); err != nil {
		return fmt.Errorf("max provider: websocket_url: %w", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running {
		return fmt.Errorf("max provider: cannot configure active provider")
	}
	p.config = maxConfig{token: token, role: role, peerID: peerID, webSocketURL: webSocketURL, allowInsecure: allowInsecure}
	p.recvCh = make(chan []byte, maxQueueSize)
	p.metrics = provider.Metrics{}
	p.health = provider.Health{LastCheck: time.Now().UTC()}
	return nil
}

func (p *Provider) GetSchema() provider.Schema {
	return provider.Schema{
		Name:        "MAX Call Signaling",
		Description: "Experimental MAX call signaling stream between WhiteTransport peers; uses a role-scoped TokenStore token.",
		Version:     p.Version(),
		Fields: []provider.Field{
			{Name: "max_token", Type: "string", Required: true, Description: "Role-scoped TokenStore credential"},
			{Name: "peer_id", Type: "number", Required: false, Description: "MAX peer ID; required only by the client role"},
		},
	}
}

func (p *Provider) Load() error {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return nil
	}
	if p.recvCh == nil {
		p.mu.Unlock()
		return fmt.Errorf("max provider: Configure must succeed before Load")
	}
	cfg := p.config
	factory := p.newRPC
	if factory == nil {
		factory = newRPCClient
	}
	timeout := p.deadline
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	p.mu.Unlock()

	client, err := factory(cfg.webSocketURL, cfg.allowInsecure)
	if err != nil {
		return err
	}
	// Install the node event handler before authentication. A MAX invite can
	// arrive immediately after the login reply; registering it afterwards
	// would drop that first event and leave the peer waiting indefinitely.
	runCtx, runCancel := context.WithCancel(context.Background())
	client.setEventHandler(func(packet rpcPacket) {
		if packet.Opcode != maxIncomingOpcode || cfg.role != "node" {
			return
		}
		go p.acceptInvite(runCtx, client, packet)
	})
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := client.connect(ctx); err != nil {
		runCancel()
		return fmt.Errorf("max provider: connect RPC: %w", err)
	}
	if err := client.login(ctx, cfg.token); err != nil {
		runCancel()
		_ = client.close()
		return fmt.Errorf("max provider: login: %w", err)
	}

	p.mu.Lock()
	p.client = client
	p.cancel = runCancel
	p.running = true
	p.health.LastCheck = time.Now().UTC()
	p.mu.Unlock()

	if cfg.role == "client" {
		session, err := client.startCall(ctx, cfg.peerID)
		if err != nil {
			_ = p.Unload()
			return fmt.Errorf("max provider: start call: %w", err)
		}
		p.attachSession(session)
	}
	return nil
}

func (p *Provider) acceptInvite(ctx context.Context, client *rpcClient, packet rpcPacket) {
	invite, err := decodeInvite(packet.Payload)
	if err != nil {
		p.recordError()
		return
	}
	session, err := client.acceptCall(ctx, invite)
	if err != nil {
		p.recordError()
		return
	}
	p.attachSession(session)
}

func (p *Provider) attachSession(session *signalSession) {
	if session == nil {
		return
	}
	p.mu.Lock()
	previous := p.session
	p.session = session
	p.health.LastCheck = time.Now().UTC()
	p.mu.Unlock()
	if previous != nil && previous != session {
		_ = previous.close()
	}
	go p.forwardSession(session)
}

func (p *Provider) forwardSession(session *signalSession) {
	for {
		select {
		case payload := <-session.inbound:
			copyPayload := append([]byte(nil), payload...)
			p.mu.RLock()
			recvCh := p.recvCh
			p.mu.RUnlock()
			if recvCh == nil {
				continue
			}
			select {
			case recvCh <- copyPayload:
			default:
				p.recordError()
			}
		case <-session.done:
			return
		}
	}
}

func (p *Provider) Unload() error {
	p.mu.Lock()
	if !p.running {
		p.mu.Unlock()
		return nil
	}
	cancel := p.cancel
	client := p.client
	session := p.session
	p.running = false
	p.cancel = nil
	p.client = nil
	p.session = nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if session != nil {
		_ = session.close()
	}
	if client != nil {
		return client.close()
	}
	return nil
}

func (p *Provider) Send(ctx context.Context, payload []byte) error {
	p.mu.RLock()
	session := p.session
	p.mu.RUnlock()
	if session == nil {
		return fmt.Errorf("max provider: call signaling session is not connected")
	}
	if err := session.send(ctx, payload); err != nil {
		p.recordError()
		return err
	}
	p.mu.Lock()
	p.metrics.SentBytes += int64(len(payload))
	p.metrics.MessagesSent++
	p.health.LastCheck = time.Now().UTC()
	p.mu.Unlock()
	return nil
}

func (p *Provider) Receive(ctx context.Context) ([]byte, error) {
	p.mu.RLock()
	recvCh := p.recvCh
	p.mu.RUnlock()
	if recvCh == nil {
		return nil, fmt.Errorf("max provider: receive channel is closed")
	}
	select {
	case payload := <-recvCh:
		p.mu.Lock()
		p.metrics.ReceivedBytes += int64(len(payload))
		p.metrics.MessagesRecv++
		p.health.LastCheck = time.Now().UTC()
		p.mu.Unlock()
		return payload, nil
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
	return provider.Limits{MaxPayloadBytes: 16 * 1024, MaxRatePerMinute: 120, MaxDailyBytes: 1 << 30}
}

func (p *Provider) GetMetrics() provider.Metrics {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.metrics
}

func (p *Provider) UpdateMetrics(metrics provider.Metrics) {
	p.mu.Lock()
	p.metrics = metrics
	p.mu.Unlock()
}

func (p *Provider) recordError() {
	p.mu.Lock()
	p.metrics.Errors++
	p.health.ErrorCount++
	p.health.LastCheck = time.Now().UTC()
	p.mu.Unlock()
}

func stringSetting(settings map[string]any, key string) string {
	if settings == nil {
		return ""
	}
	value, ok := settings[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func boolSetting(settings map[string]any, key string) bool {
	if settings == nil {
		return false
	}
	value, ok := settings[key].(bool)
	return ok && value
}

func int64Setting(cfg provider.ProviderConfig, key string) (int64, error) {
	value := strings.TrimSpace(cfg.Credentials[key])
	if value == "" {
		value = stringSetting(cfg.Settings, key)
	}
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return parsed, nil
}

func validateWebSocketURL(raw string, allowInsecure bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid URL")
	}
	if u.Scheme == "wss" {
		return nil
	}
	if u.Scheme == "ws" && allowInsecure {
		return nil
	}
	return fmt.Errorf("must use wss (ws is allowed only for a local test endpoint)")
}

type rpcPacket struct {
	Version  int             `json:"ver"`
	Command  int             `json:"cmd"`
	Sequence uint64          `json:"seq"`
	Opcode   int             `json:"opcode"`
	Payload  json.RawMessage `json:"payload"`
}

type rpcClient struct {
	endpoint      string
	allowInsecure bool
	dialer        *websocket.Dialer

	mu      sync.Mutex
	conn    *websocket.Conn
	pending map[uint64]chan rpcPacket
	onEvent func(rpcPacket)
	done    chan struct{}
	closed  sync.Once
	seq     atomic.Uint64
	device  string
}

func newRPCClient(endpoint string, allowInsecure bool) (*rpcClient, error) {
	if err := validateWebSocketURL(endpoint, allowInsecure); err != nil {
		return nil, err
	}
	device, err := randomDeviceID()
	if err != nil {
		return nil, err
	}
	return &rpcClient{endpoint: endpoint, allowInsecure: allowInsecure, dialer: websocket.DefaultDialer, pending: make(map[uint64]chan rpcPacket), done: make(chan struct{}), device: device}, nil
}

func (c *rpcClient) connect(ctx context.Context) error {
	header := http.Header{}
	header.Set("Origin", "https://web.max.ru")
	header.Set("User-Agent", "WhiteTransport MAX carrier")
	conn, _, err := c.dialer.DialContext(ctx, c.endpoint, header)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	go c.readLoop()
	return nil
}

func (c *rpcClient) setEventHandler(handler func(rpcPacket)) {
	c.mu.Lock()
	c.onEvent = handler
	c.mu.Unlock()
}

func (c *rpcClient) readLoop() {
	for {
		c.mu.Lock()
		conn := c.conn
		c.mu.Unlock()
		if conn == nil {
			return
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			_ = c.close()
			return
		}
		var packet rpcPacket
		if err := json.Unmarshal(raw, &packet); err != nil {
			continue
		}
		c.mu.Lock()
		pending := c.pending[packet.Sequence]
		if pending != nil {
			delete(c.pending, packet.Sequence)
		}
		handler := c.onEvent
		c.mu.Unlock()
		if pending != nil {
			select {
			case pending <- packet:
			default:
			}
			continue
		}
		if handler != nil {
			handler(packet)
		}
	}
}

func (c *rpcClient) invoke(ctx context.Context, opcode int, payload map[string]any) (rpcPacket, error) {
	sequence := c.seq.Add(1)
	result := make(chan rpcPacket, 1)
	c.mu.Lock()
	conn := c.conn
	if conn == nil {
		c.mu.Unlock()
		return rpcPacket{}, fmt.Errorf("MAX RPC is not connected")
	}
	c.pending[sequence] = result
	request, err := json.Marshal(rpcPacket{Version: maxRPCVersion, Command: 0, Sequence: sequence, Opcode: opcode, Payload: mustJSON(payload)})
	if err == nil {
		err = conn.WriteMessage(websocket.TextMessage, request)
	}
	if err != nil {
		delete(c.pending, sequence)
	}
	c.mu.Unlock()
	if err != nil {
		return rpcPacket{}, err
	}
	select {
	case packet := <-result:
		return packet, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, sequence)
		c.mu.Unlock()
		return rpcPacket{}, ctx.Err()
	case <-c.done:
		return rpcPacket{}, fmt.Errorf("MAX RPC connection closed")
	}
}

func (c *rpcClient) login(ctx context.Context, token string) error {
	if _, err := c.invoke(ctx, maxDeviceOpcode, map[string]any{
		"deviceId":  c.device,
		"userAgent": map[string]any{"deviceType": "WEB", "locale": "ru_RU", "osVersion": "WhiteTransport", "deviceName": "WhiteTransport", "appVersion": "1.0"},
	}); err != nil {
		return err
	}
	packet, err := c.invoke(ctx, maxLoginOpcode, map[string]any{
		"interactive": false, "token": token, "deviceId": c.device,
		"chatsSync": 0, "contactsSync": 0, "presenceSync": 0, "draftsSync": 0, "chatsCount": 0,
	})
	if err != nil {
		return err
	}
	var payload map[string]any
	if err := json.Unmarshal(packet.Payload, &payload); err != nil {
		return fmt.Errorf("invalid login payload: %w", err)
	}
	if providerError, ok := payload["error"]; ok {
		return fmt.Errorf("MAX rejected login: %v", providerError)
	}
	return nil
}

func (c *rpcClient) startCall(ctx context.Context, peerID int64) (*signalSession, error) {
	packet, err := c.invoke(ctx, maxStartCallOpcode, map[string]any{
		"conversationId": randomConversationID(), "calleeIds": []int64{peerID}, "isVideo": false,
		"internalParams": map[string]any{"deviceId": c.device, "platform": "WEB", "protocolVersion": 5},
	})
	if err != nil {
		return nil, err
	}
	var payload struct {
		InternalCallerParams string `json:"internalCallerParams"`
	}
	if err := json.Unmarshal(packet.Payload, &payload); err != nil {
		return nil, fmt.Errorf("decode call response: %w", err)
	}
	var params struct {
		ID struct {
			Internal int64 `json:"internal"`
		} `json:"id"`
		Endpoint string `json:"endpoint"`
	}
	if payload.InternalCallerParams == "" || json.Unmarshal([]byte(payload.InternalCallerParams), &params) != nil || strings.TrimSpace(params.Endpoint) == "" {
		return nil, fmt.Errorf("call response has no usable signaling endpoint")
	}
	endpoint, err := outgoingSignalURL(params.Endpoint)
	if err != nil {
		return nil, err
	}
	return c.openSignal(ctx, endpoint, params.ID.Internal, peerID, "client")
}

func (c *rpcClient) acceptCall(ctx context.Context, invite callInvite) (*signalSession, error) {
	endpoint, err := incomingSignalURL(invite)
	if err != nil {
		return nil, err
	}
	session, err := c.openSignal(ctx, endpoint, invite.userID, 0, "node")
	if err != nil {
		return nil, err
	}
	if err := session.accept(ctx); err != nil {
		_ = session.close()
		return nil, err
	}
	return session, nil
}

func (c *rpcClient) openSignal(ctx context.Context, endpoint string, localID, remoteID int64, role string) (*signalSession, error) {
	if err := validateWebSocketURL(endpoint, c.allowInsecure); err != nil {
		return nil, err
	}
	conn, _, err := c.dialer.DialContext(ctx, endpoint, nil)
	if err != nil {
		return nil, err
	}
	session := &signalSession{conn: conn, inbound: make(chan []byte, maxQueueSize), done: make(chan struct{}), localID: localID, remoteID: remoteID, role: role}
	go session.readLoop()
	return session, nil
}

func (c *rpcClient) close() error {
	var closeErr error
	c.closed.Do(func() {
		close(c.done)
		c.mu.Lock()
		conn := c.conn
		c.conn = nil
		c.pending = make(map[uint64]chan rpcPacket)
		c.mu.Unlock()
		if conn != nil {
			closeErr = conn.Close()
		}
	})
	return closeErr
}

type signalSession struct {
	mu       sync.RWMutex
	writeMu  sync.Mutex
	conn     *websocket.Conn
	inbound  chan []byte
	done     chan struct{}
	closed   sync.Once
	sequence uint64
	localID  int64
	remoteID int64
	role     string
}

func (s *signalSession) readLoop() {
	for {
		_, raw, err := s.conn.ReadMessage()
		if err != nil {
			_ = s.close()
			return
		}
		if string(raw) == "ping" {
			s.writeMu.Lock()
			_ = s.conn.WriteMessage(websocket.TextMessage, []byte("pong"))
			s.writeMu.Unlock()
			continue
		}
		var frame signalFrame
		if json.Unmarshal(raw, &frame) != nil {
			continue
		}
		s.learnParticipants(frame.Conversation.Participants)
		if frame.Data.Candidate.Candidate == "" {
			continue
		}
		payload, err := base64.StdEncoding.DecodeString(frame.Data.Candidate.Candidate)
		if err != nil {
			continue
		}
		select {
		case s.inbound <- payload:
		default:
		}
	}
}

func (s *signalSession) learnParticipants(participants []signalParticipant) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, participant := range participants {
		creator := false
		for _, role := range participant.Roles {
			if role == "CREATOR" {
				creator = true
			}
		}
		if s.role == "node" && creator {
			s.localID = participant.ID
		}
		if s.role == "client" && !creator {
			s.localID = participant.ID
		}
	}
}

func (s *signalSession) accept(ctx context.Context) error {
	return s.write(ctx, map[string]any{
		"command":       "accept-call",
		"mediaSettings": map[string]bool{"isAudioEnabled": true, "isVideoEnabled": false, "isScreenSharingEnabled": false, "isFastScreenSharingEnabled": false, "isAudioSharingEnabled": false, "isAnimojiEnabled": false},
	})
}

func (s *signalSession) send(ctx context.Context, payload []byte) error {
	s.mu.RLock()
	participantID := s.localID
	s.mu.RUnlock()
	if participantID <= 0 {
		return fmt.Errorf("max provider: signaling session has no local participant ID")
	}
	return s.write(ctx, map[string]any{
		"command": "transmit-data", "participantId": participantID, "participantType": "USER",
		"data": map[string]any{"candidate": map[string]string{"candidate": base64.StdEncoding.EncodeToString(payload)}},
	})
}

func (s *signalSession) write(ctx context.Context, frame map[string]any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return fmt.Errorf("max provider: signaling session is closed")
	default:
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.sequence++
	frame["sequence"] = s.sequence
	raw, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return s.conn.WriteMessage(websocket.TextMessage, raw)
}

func (s *signalSession) close() error {
	var closeErr error
	s.closed.Do(func() {
		close(s.done)
		closeErr = s.conn.Close()
	})
	return closeErr
}

type signalFrame struct {
	Conversation struct {
		Participants []signalParticipant `json:"participants"`
	} `json:"conversation"`
	Data struct {
		Candidate struct {
			Candidate string `json:"candidate"`
		} `json:"candidate"`
	} `json:"data"`
}

type signalParticipant struct {
	ID    int64    `json:"id"`
	Roles []string `json:"roles"`
}

type callInvite struct {
	conversationID string
	endpoint       string
	token          string
	userID         int64
}

func decodeInvite(raw json.RawMessage) (callInvite, error) {
	var event struct {
		ConversationID string `json:"conversationId"`
		VCP            string `json:"vcp"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return callInvite{}, err
	}
	if len(event.VCP) < 5 {
		return callInvite{}, fmt.Errorf("incoming call has no usable vcp")
	}
	size, err := strconv.Atoi(event.VCP[:3])
	if err != nil || size <= 0 || size > 64*1024 {
		return callInvite{}, fmt.Errorf("incoming call has invalid vcp size")
	}
	packed, err := base64.StdEncoding.DecodeString(event.VCP[4:])
	if err != nil {
		return callInvite{}, fmt.Errorf("decode vcp: %w", err)
	}
	decoded := make([]byte, size)
	n, err := lz4.UncompressBlock(packed, decoded)
	if err != nil || n != size {
		return callInvite{}, fmt.Errorf("decode vcp compression")
	}
	var details struct {
		Token             string `json:"tkn"`
		WebsocketEndpoint string `json:"wse"`
		TurnUsername      string `json:"trnu"`
	}
	if err := json.Unmarshal(decoded, &details); err != nil {
		return callInvite{}, fmt.Errorf("decode vcp JSON: %w", err)
	}
	if strings.TrimSpace(event.ConversationID) == "" || strings.TrimSpace(details.Token) == "" || strings.TrimSpace(details.WebsocketEndpoint) == "" {
		return callInvite{}, fmt.Errorf("incoming call is missing signaling data")
	}
	userID, err := trailingID(details.TurnUsername)
	if err != nil {
		return callInvite{}, fmt.Errorf("incoming call has invalid turn username: %w", err)
	}
	return callInvite{conversationID: event.ConversationID, endpoint: details.WebsocketEndpoint, token: details.Token, userID: userID}, nil
}

func outgoingSignalURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid outgoing signaling endpoint")
	}
	q := u.Query()
	q.Set("platform", "WEB")
	q.Set("clientType", "ONE_ME")
	q.Set("tgt", "start")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func incomingSignalURL(invite callInvite) (string, error) {
	u, err := url.Parse(invite.endpoint)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid incoming signaling endpoint")
	}
	path := strings.TrimSuffix(u.Path, "/ws1")
	if path == u.Path {
		path = strings.TrimSuffix(u.Path, "/ws")
	}
	if path == u.Path {
		return "", fmt.Errorf("unsupported incoming signaling endpoint path")
	}
	u.Path = strings.TrimRight(path, "/") + "/ws2"
	q := u.Query()
	q.Set("userId", strconv.FormatInt(invite.userID, 10))
	q.Set("entityType", "USER")
	q.Set("deviceIdx", "0")
	q.Set("conversationId", invite.conversationID)
	q.Set("token", invite.token)
	q.Set("platform", "WEB")
	q.Set("clientType", "ONE_ME")
	q.Set("tgt", "accept")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func trailingID(raw string) (int64, error) {
	part := raw
	if index := strings.LastIndexByte(raw, ':'); index >= 0 {
		part = raw[index+1:]
	}
	return strconv.ParseInt(part, 10, 64)
}

func randomDeviceID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	bytes[6] = bytes[6]&0x0f | 0x40
	bytes[8] = bytes[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", bytes[:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:]), nil
}

func randomConversationID() string {
	device, err := randomDeviceID()
	if err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return device
}

func mustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}
