package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/carriers/adminrelay"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/router"
	"github.com/meanwebuser/whitetransport/core/internal/session"
	"github.com/meanwebuser/whitetransport/core/internal/tokens"
	"github.com/meanwebuser/whitetransport/core/pkg/runtimeapi"
)

// VideoTunnelAdapter is implemented by provider adapters that manage a
// video-call egress tunnel (WBStream, Telemost, DION). Each adapter wraps
// an upstream relay provider from whitelist-bypass/relay/.
type VideoTunnelAdapter interface {
	CreateAndStartEgress(ctx context.Context) (string, error)
	StartEgressAddr(ctx context.Context, addr string) error
	Close() error
}

const (
	// BootstrapKeyCapability announces that the node can use the dedicated
	// bootstrap secret while retaining legacy provider-token decryption.
	BootstrapKeyCapability         = "bootstrap-key-v2"
	incompatibleProductVersionCode = "incompatible_product_version"
	bootstrapKeyMetadata           = "bootstrap_key"
	bootstrapKeyV2                 = "v2"

	statusStateDisconnected = "disconnected"
	statusStateConnecting   = "connecting"
	statusStateConnected    = "connected"
	statusStateDegraded     = "degraded"
	statusStateReconnecting = "reconnecting"

	ackTimeout           = 6 * time.Second
	maxBusyRetries       = 3
	staleNodeThreshold   = 5 * time.Minute
	reAdvertiseInterval  = 2 * time.Minute
	heartbeatInterval    = 60 * time.Second
	maxReconnectAttempts = 3
	dialEgressBatchSize  = 3
	dialEgressTimeout    = 60 * time.Second
	releaseSendTimeout   = 3 * time.Second
	// VK/OK control delivery can legitimately be delayed by several minutes
	// while the provider cursor catches up. Keep the offer alive for the same
	// window used by the client answer wait so a delayed answer remains valid.
	sessionOfferTTL      = 5 * time.Minute
	sessionAnswerTimeout = 5 * time.Minute
)

var (
	debugEnabled bool
	debugOnce    sync.Once
)

func init() {
	debugOnce.Do(func() {
		// Enable debug by default (can be disabled with WT_DEBUG=0)
		debugEnabled = os.Getenv("WT_DEBUG") != "0"
		if debugEnabled {
			log.Println("[DEBUG] WT_DEBUG enabled - verbose logging active")
		}
	})
}

func dbg(format string, args ...any) {
	if debugEnabled {
		log.Printf("[DBG] "+format, args...)
	}
}

// NodeView is the API-facing summary of a discovered node.
type NodeView struct {
	NodeID       string    `json:"node_id"`
	Label        string    `json:"label"`
	Country      string    `json:"country,omitempty"`
	Region       string    `json:"region,omitempty"`
	Capabilities []string  `json:"capabilities,omitempty"`
	Available    bool      `json:"available"`
	LastSeenAt   time.Time `json:"last_seen_at,omitempty"`
}

// StatusView is the API-facing status of the local runtime daemon.
type StatusView struct {
	Role                      config.Role                    `json:"role"`
	State                     string                         `json:"state"`
	NodeID                    string                         `json:"node_id,omitempty"`
	ActiveNodeID              string                         `json:"active_node_id,omitempty"`
	SessionID                 string                         `json:"session_id,omitempty"`
	SessionActive             bool                           `json:"session_active"`
	SocksListen               string                         `json:"socks_listen,omitempty"`
	EgressEndpoints           []carriers.Endpoint            `json:"egress_endpoints,omitempty"`
	SelectedEgressEndpointID  string                         `json:"selected_egress_endpoint_id,omitempty"`
	AutomaticEgressEndpointID string                         `json:"automatic_egress_endpoint_id,omitempty"`
	UpstreamProxy             string                         `json:"upstream_proxy,omitempty"`
	DiscoveredNodes           int                            `json:"discovered_nodes"`
	AvailableNodes            int                            `json:"available_nodes"`
	ReconnectAttempts         int                            `json:"reconnect_attempts,omitempty"`
	LastError                 string                         `json:"last_error,omitempty"`
	SystemVPNProfile          *runtimeapi.SystemVPNProfile   `json:"system_vpn_profile,omitempty"`
	SystemVPNProfileReadiness *runtimeapi.SystemVPNReadiness `json:"system_vpn_profile_readiness,omitempty"`
}

type activeSession struct {
	NodeID                    string
	SessionID                 string
	ControlEndpoint           carriers.Endpoint
	ControlBinding            policy.CarrierBinding
	EgressEndpoints           []carriers.Endpoint
	SelectedEgressEndpointID  string
	AutomaticEgressEndpointID string
	UpdatedAt                 time.Time
	ExpiresAt                 time.Time
}

var (
	// ErrNoBootstrapCarrier means a node has no executable carrier for bootstrap.
	ErrNoBootstrapCarrier = errors.New("node role requires at least one bootstrap-capable carrier binding")
	// ErrNoControlCarrier means a client has no executable carrier for control.
	ErrNoControlCarrier = errors.New("client role requires at least one control-capable carrier binding")
)

type discoveredNode struct {
	Advertisement session.NodeAdvertisement
	LastSeenAt    time.Time
	Withdrawn     bool // true after node.withdraw received
}

type carrierRef struct {
	ID         string
	Descriptor carriers.Descriptor
	Binding    policy.CarrierBinding
}

// endpointWithBindingIdentity keeps a configured route alias on wire while
// the carrier descriptor continues to identify its shared implementation.
// Without this, two file.mailbox or sing-box bindings collapse into one
// nondeterministic map lookup during control-plane replies.
func endpointWithBindingIdentity(ref carrierRef) carriers.Endpoint {
	endpoint := ref.Binding.Endpoint
	if ref.ID != ref.Descriptor.ID {
		endpoint.Carrier = ref.ID
	}
	return endpoint
}

// pendingAckState tracks the state of an offer ACK for a pending session.
type pendingAckState struct {
	ch       chan session.OfferAck
	received bool
	ack      session.OfferAck
}

// ControlPlane manages discovery, session establishment, and the active egress
// route used by local SOCKS clients. All egress traffic goes through carrier
// tunnels — no direct TCP, HTTP CONNECT, or upstream proxy fallback.
type ControlPlane struct {
	cfg                   config.Config
	productVersion        string
	engine                session.Engine
	bindings              map[string]policy.CarrierBinding
	bootstrap             []carrierRef
	control               []carrierRef
	egress                []carrierRef
	replyEndpoints        []carriers.Endpoint
	advertisePoints       []carriers.Endpoint
	policy                policy.CarrierPolicy
	tunnel                session.CarrierTunnel
	bootstrapCipher       *fabric.EnvelopeCipher
	bootstrapSecretCipher *fabric.EnvelopeCipher
	legacyBootstrapCipher *fabric.EnvelopeCipher
	carrierRouter         *router.CarrierRouter
	carrierHealth         *router.CarrierHealth
	sendQueue             *router.SendQueue
	tokenStore            *tokens.Store
	stateFile             string
	daemonInstanceID      string
	profileRevision       uint64
	actualSocksListen     string
	profileBuilder        *SystemVPNProfileBuilder

	mu                    sync.RWMutex
	nodeAutoHealMu        sync.Mutex
	cursors               map[string]carriers.Cursor
	nodes                 map[string]discoveredNode
	pending               map[string]chan session.Answer
	pendingErrors         map[string]chan session.SessionError
	pendingAcks           map[string]*pendingAckState
	pendingTargetNodes    map[string]string // sessionID → expected NodeID for answer filtering
	active                *activeSession
	advertised            bool
	nodeBusy              bool
	nodeSessionID         string
	nodeSessionClientID   string
	sessionTimer          *time.Timer
	idleTimer             *time.Timer
	reconnectAttempts     int
	lastError             string
	state                 string
	nodeSessionEndpoints  []carriers.Endpoint
	videoTunnelCarrierIDs map[string]bool // carrier IDs backed by video tunnel adapters
	publishedMessageIDs   map[string]int  // carrierRef.ID → message ID for deletion
	stopCh                chan struct{}
	egressRecovery        *policy.EgressRecoveryTracker
	egressRouteStreams    *routeStreamRegistry
	recoveryCancel        context.CancelFunc
	recoveryWG            sync.WaitGroup
	clientRoomEndpoint    string // set when client created the egress room locally (role reversal)
	clientRoomCarrier     string // carrier ID of the client-created room binding
	postSessionControl    *config.AdminRelayConfig
	postSessionCancel     context.CancelFunc
	postSessionWG         sync.WaitGroup
	sessionSSHIssuer      sessionSSHIssuer
	nodeSessionSSH        *issuedSessionSSH
	tunnelTransitionDepth int // protects the expected close of a replaced video tunnel during role reversal
	systemVPNProfile      *runtimeapi.SystemVPNProfile
	systemVPNReadiness    *runtimeapi.SystemVPNReadiness

	pollInterval   time.Duration
	busyRetryAfter time.Duration
}

// NewControlPlane builds the runtime discovery/session controller for a daemon.
func NewControlPlane(cfg config.Config, bindings map[string]policy.CarrierBinding, carrierPolicy policy.CarrierPolicy, tunnel session.CarrierTunnel) (*ControlPlane, error) {
	return NewControlPlaneWithTokens(cfg, bindings, carrierPolicy, tunnel, nil)
}

// NewControlPlaneWithTokens builds a ControlPlane with optional token store
// for usage tracking and health reporting.
func NewControlPlaneWithTokens(cfg config.Config, bindings map[string]policy.CarrierBinding, carrierPolicy policy.CarrierPolicy, tunnel session.CarrierTunnel, ts *tokens.Store) (*ControlPlane, error) {
	daemonInstanceID, err := NewDaemonInstanceID()
	if err != nil {
		return nil, err
	}
	control := &ControlPlane{
		cfg:                 cfg,
		productVersion:      config.Version,
		engine:              session.NewEngine(cfg.Identity()),
		bindings:            bindings,
		cursors:             make(map[string]carriers.Cursor, len(bindings)),
		nodes:               make(map[string]discoveredNode),
		pending:             make(map[string]chan session.Answer),
		pendingErrors:       make(map[string]chan session.SessionError),
		pendingAcks:         make(map[string]*pendingAckState),
		pendingTargetNodes:  make(map[string]string),
		policy:              carrierPolicy,
		state:               statusStateDisconnected,
		tunnel:              tunnel,
		tokenStore:          ts,
		stateFile:           cfg.StateFile,
		daemonInstanceID:    daemonInstanceID,
		profileRevision:     1,
		profileBuilder:      NewSystemVPNProfileBuilder(nil, nil),
		systemVPNReadiness:  &runtimeapi.SystemVPNReadiness{Ready: false, Provenance: "runtime/session", Reason: "disconnected"},
		publishedMessageIDs: make(map[string]int),
		stopCh:              make(chan struct{}),
		pollInterval:        2 * time.Second,
		busyRetryAfter:      30 * time.Second,
	}
	if cfg.SessionSSH.Enabled {
		issuer, err := newOpenSSHSessionIssuer(cfg.SessionSSH)
		if err != nil {
			return nil, err
		}
		control.sessionSSHIssuer = issuer
	}

	if control.stateFile != "" {
		if err := control.loadCursors(); err != nil {
			log.Printf("warning: failed to load cursors from %s: %v", control.stateFile, err)
		}
	}

	scorer := carrierPolicy.Scorer
	for id, binding := range bindings {
		desc := binding.Carrier.Descriptor()
		ref := carrierRef{
			ID:         id,
			Descriptor: desc,
			Binding:    binding,
		}
		// Skip bindings the failure tracker has temporarily auto-disabled
		// from the routing tables. They are still available for ad-hoc
		// recovery attempts, but the runtime should not rely on them.
		if ft := carrierPolicy.FailureTracker; ft != nil && ft.IsAutoDisabled(desc.ID) {
			dbg("NewControlPlaneWithTokens: skip auto-disabled carrier id=%s desc=%s", id, desc.ID)
			continue
		}
		// Video tunnel carriers (WBStream, Telemost, DION) wrap stream-based
		// adapters and are intentionally NOT routed for bootstrap/control
		// traffic. Their descriptors advertise CapMailbox for backwards
		// compatibility, so the authoritative scorer accepts them as
		// control-eligible; explicitly skip them here so the poll loop does
		// not hammer a non-receiving adapter (which would trip the auto-
		// disable threshold and take down the egress route as well).
		isVT := isVideoTunnelCarrier(binding)
		// Role-aware classification: any binding carrying an explicit role is
		// authoritative. Per-channel roles are only constructed when the
		// channel-bindings feature is enabled; top-level roles are explicit
		// runtime policy and must not silently disappear behind that flag.
		if binding.Role != "" {
			classifyByRole(control, ref, binding.Role)
		} else {
			if !isVT && supportsTrafficScored(desc, fabric.TrafficBootstrap, scorer) {
				control.bootstrap = append(control.bootstrap, ref)
				control.advertisePoints = append(control.advertisePoints, endpointWithBindingIdentity(ref))
			}
			if !isVT && (supportsTrafficScored(desc, fabric.TrafficControl, scorer) || supportsTrafficScored(desc, fabric.TrafficBootstrap, scorer)) {
				control.control = append(control.control, ref)
				control.replyEndpoints = append(control.replyEndpoints, endpointWithBindingIdentity(ref))
			}
			if supportsEgressScored(desc, scorer) {
				control.egress = append(control.egress, ref)
			}
		}
		if isVT {
			if control.videoTunnelCarrierIDs == nil {
				control.videoTunnelCarrierIDs = make(map[string]bool)
			}
			control.videoTunnelCarrierIDs[id] = true
		}
	}
	if scorer != nil {
		sortEgressRefsScored(control.egress, scorer)
	} else {
		sortEgressRefs(control.egress)
	}
	log.Printf("control plane bindings role=%s ids=%v egress=%v", cfg.Role, bindingIDs(bindings), carrierRefIDs(control.egress))

	if cfg.Role == config.RoleNode && len(control.bootstrap) == 0 {
		return nil, ErrNoBootstrapCarrier
	}
	if cfg.Role == config.RoleClient && len(control.control) == 0 && len(bindings) > 0 {
		return nil, ErrNoControlCarrier
	}
	if hookable, ok := tunnel.(interface{ SetOnIdle(func()) }); ok {
		hookable.SetOnIdle(func() {
			control.onTunnelIdle(context.Background())
		})
	}
	if hookable, ok := tunnel.(interface{ SetOnClosed(func()) }); ok {
		hookable.SetOnClosed(func() {
			control.onTunnelClosed(context.Background())
		})
	}
	if hookable, ok := tunnel.(interface{ SetOnActive(func()) }); ok {
		hookable.SetOnActive(func() {
			control.cancelIdleRelease()
		})
	}

	// Dedicated v2 bootstrap encryption is independent from provider auth. Keep
	// the legacy provider-token cipher as a fallback for mixed-version peers.
	if secret := strings.TrimSpace(cfg.BootstrapSecret); secret != "" {
		key := fabric.DeriveBootstrapSecretKey(secret)
		if cipher, err := fabric.NewSessionCipher(key); err == nil {
			control.bootstrapSecretCipher = cipher
		}
	}
	if token := extractBootstrapTokenWithStore(cfg, ts); token != "" {
		key := fabric.DeriveBootstrapKey(token)
		if cipher, err := fabric.NewSessionCipher(key); err == nil {
			control.legacyBootstrapCipher = cipher
		}
	}
	control.bootstrapCipher = control.legacyBootstrapCipher
	if control.bootstrapSecretCipher != nil {
		control.bootstrapCipher = control.bootstrapSecretCipher
	}

	return control, nil
}

func (c *ControlPlane) bootstrapCipherForNode(nodeID string) *fabric.EnvelopeCipher {
	c.mu.RLock()
	node, ok := c.nodes[nodeID]
	c.mu.RUnlock()
	if ok && slices.Contains(node.Advertisement.Capabilities, BootstrapKeyCapability) && c.bootstrapSecretCipher != nil {
		return c.bootstrapSecretCipher
	}
	return c.legacyBootstrapCipher
}

// SetRouter attaches a CarrierRouter for unified read path management.
// When set, the ControlPlane registers its control/bootstrap readers with the
// router instead of using its own poll loop.
func (c *ControlPlane) SetRouter(r *router.CarrierRouter, health *router.CarrierHealth, sq *router.SendQueue) {
	c.carrierRouter = r
	c.carrierHealth = health
	c.sendQueue = sq
}

// ConfigurePostSessionControl enables a send-only HTTP control fallback over
// the active carrier egress. It does not register admin.relay as a bootstrap or
// egress binding and does not provide relay receive polling or ACK semantics.
func (c *ControlPlane) ConfigurePostSessionControl(cfg config.AdminRelayConfig) error {
	validated, err := ValidatePostSessionControlConfig(cfg)
	if err != nil {
		return err
	}
	cfg = validated
	if strings.TrimSpace(cfg.Identity) == "" {
		cfg.Identity = c.cfg.Identity()
	}
	c.mu.Lock()
	c.postSessionControl = &cfg
	c.mu.Unlock()
	return nil
}

// ValidatePostSessionControlConfig checks relay configuration without needing
// a live control plane. Blocked runtimes use it before exposing the local API.
func ValidatePostSessionControlConfig(cfg config.AdminRelayConfig) (config.AdminRelayConfig, error) {
	if !cfg.Enabled {
		return cfg, errors.New("post-session HTTP control must be enabled")
	}
	adminURL := strings.TrimSpace(cfg.AdminURL)
	if adminURL == "" {
		return cfg, errors.New("post-session HTTP control requires admin URL")
	}
	parsedURL, err := url.ParseRequestURI(adminURL)
	if err != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return cfg, errors.New("post-session HTTP control requires an absolute HTTP(S) admin URL")
	}
	cfg.AdminURL = adminURL
	channels := cfg.EffectiveChannels()
	if !slices.Contains(channels, "control") {
		return cfg, errors.New("post-session HTTP control requires the control channel")
	}
	cfg.Channels = append([]string(nil), channels...)
	return cfg, nil
}

// extractBootstrapTokenWithStore returns the first available carrier token for
// bootstrap key derivation. When a token store is available, it resolves via
// ts.ResolveOne(). Falls back to inline config values.
func extractBootstrapTokenWithStore(cfg config.Config, ts *tokens.Store) string {
	// Resolve the configured bootstrap endpoints first. Production TokenStore
	// bindings are intentionally scoped to real VK/OK peer IDs; asking only
	// for channel "*" makes both peers lose their session key and prevents
	// encrypted egress profiles from ever being delivered.
	if ts != nil {
		for _, carrierConfig := range cfg.CarrierConfigs {
			platform, channels := bootstrapBindingChannels(carrierConfig)
			for _, channelID := range channels {
				tok, err := ts.ResolveOne(platform, "messages", channelID)
				if err == nil && tok.Value != "" {
					return tok.Value
				}
			}
		}
		// A wildcard TokenStore binding remains valid for configurations that do
		// not declare a concrete bootstrap peer.
		for _, platform := range []string{"vk", "ok"} {
			tok, err := ts.ResolveOne(platform, "messages", "*")
			if err == nil && tok.Value != "" {
				return tok.Value
			}
		}
	}
	// Legacy path.
	return extractBootstrapToken(cfg)
}

// bootstrapBindingChannels returns the configured control-plane channel IDs
// for one provider binding. The endpoint is preferred because it is the
// exact runtime routing target; channel lists cover legacy expanded configs.
func bootstrapBindingChannels(carrierConfig config.CarrierConfig) (string, []string) {
	channels := make([]string, 0, 1)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		for _, existing := range channels {
			if existing == value {
				return
			}
		}
		channels = append(channels, value)
	}
	switch {
	case carrierConfig.VKMessages != nil:
		add(carrierConfig.Endpoint.Address)
		for _, channel := range carrierConfig.VKMessages.Channels {
			add(channel.PeerID)
		}
		return "vk", channels
	case carrierConfig.OKMessages != nil:
		add(carrierConfig.Endpoint.Address)
		for _, channel := range carrierConfig.OKMessages.Channels {
			add(channel.ChatID)
		}
		return "ok", channels
	case carrierConfig.FileMailbox != nil:
		// Deterministic local integration may use an explicit non-production
		// TokenStore credential to prove encrypted session traffic. The file
		// mailbox path itself is never treated as key material.
		add(carrierConfig.Endpoint.Address)
		return "local", channels
	default:
		return "", nil
	}
}

// extractBootstrapToken returns the first available carrier token from config
// for bootstrap key derivation. Both client and node derive the same key from
// the same carrier token (e.g., VK token).
func extractBootstrapToken(cfg config.Config) string {
	for _, cc := range cfg.CarrierConfigs {
		if cc.VKMessages != nil {
			if token := secretValue(cc.VKMessages.Token, cc.VKMessages.TokenEnv); token != "" {
				return token
			}
		}
		if cc.OKMessages != nil {
			if token := secretValue(cc.OKMessages.Token, cc.OKMessages.TokenEnv); token != "" {
				return token
			}
		}
	}
	return ""
}

// Start launches discovery/session loops and the node-side egress handler.
// Nodes advertise once on startup and re-advertise periodically.
func (c *ControlPlane) Start(ctx context.Context) {
	if c.cfg.Role == config.RoleNode && len(c.bootstrap) > 0 {
		go func() {
			c.publishAdvertisement(ctx)
			c.mu.Lock()
			c.advertised = true
			c.mu.Unlock()
		}()
		go c.reAdvertiseLoop(ctx)
		go c.heartbeatLoop(ctx)
	}
	if len(c.control) > 0 {
		if c.carrierRouter != nil {
			// Register control readers with the unified router.
			c.registerWithRouter(ctx)
		} else {
			go c.runPollLoop(ctx)
		}
	}
	c.startEgressHandler(ctx)
	c.startPostSessionControlReceiver(ctx)
	c.startEgressRecoveryLoop(ctx)
}

// startPostSessionControlReceiver starts the node half of post-session HTTP
// control. Clients reach the relay through their active egress; the exit node
// polls it directly so the receive path does not recurse through that session.
func (c *ControlPlane) startPostSessionControlReceiver(ctx context.Context) {
	if c.cfg.Role != config.RoleNode {
		return
	}
	c.mu.Lock()
	if c.postSessionControl == nil || c.postSessionCancel != nil {
		c.mu.Unlock()
		return
	}
	cfg := *c.postSessionControl
	cfg.Channels = append([]string(nil), c.postSessionControl.Channels...)
	receiverCtx, cancel := context.WithCancel(ctx)
	c.postSessionCancel = cancel
	interval := c.pollInterval
	if cfg.PollIntervalSec > 0 {
		interval = time.Duration(cfg.PollIntervalSec) * time.Second
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}
	c.postSessionWG.Add(1)
	c.mu.Unlock()

	relay := adminrelay.New(cfg, log.Printf)
	endpoint := carriers.Endpoint{
		ID:      "post-session-control",
		Carrier: adminrelay.CarrierID,
		Address: "control",
		Metadata: map[string]string{
			"channel":   "control",
			"recipient": cfg.Identity,
		},
	}
	go func() {
		defer c.postSessionWG.Done()
		c.runPostSessionControlReceiver(receiverCtx, relay, endpoint, interval)
	}()
}

// runPostSessionControlReceiver applies a fetched page before acknowledging
// its cursor. A crash after apply but before ACK causes safe redelivery because
// session.release handling is idempotent against the current session state.
func (c *ControlPlane) runPostSessionControlReceiver(ctx context.Context, relay *adminrelay.Carrier, endpoint carriers.Endpoint, interval time.Duration) {
	var cursor carriers.Cursor
	for {
		read, err := relay.Read(ctx, endpoint, cursor)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.setError(fmt.Errorf("post-session HTTP control read: %w", err))
		} else if read.Cursor != "" {
			for _, envelope := range read.Envelopes {
				if err := envelope.Validate(); err != nil {
					c.setError(fmt.Errorf("post-session HTTP control envelope %q: %w", envelope.ID, err))
					continue
				}
				if envelope.PayloadType != session.PayloadSessionRelease {
					continue
				}
				release, err := session.DecodePayload[session.Release](envelope.Payload)
				if err != nil {
					c.setError(fmt.Errorf("post-session HTTP control release %q: %w", envelope.ID, err))
					continue
				}
				c.handleRelease(ctx, release)
			}
			if err := relay.Ack(ctx, endpoint, read.Cursor); err != nil {
				if ctx.Err() != nil {
					return
				}
				c.setError(fmt.Errorf("post-session HTTP control acknowledgement: %w", err))
			} else {
				cursor = read.Cursor
			}
		}

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

// registerWithRouter registers all control/bootstrap carrier readers with
// the CarrierRouter for unified polling.
func (c *ControlPlane) registerWithRouter(ctx context.Context) {
	for _, ref := range c.control {
		key := fmt.Sprintf("control:%s", ref.ID)
		handler := func(ctx context.Context, carrierID, endpointID string, envelope fabric.Envelope) {
			c.handleControlEnvelope(ctx, envelope, ref)
		}
		err := c.carrierRouter.RegisterScope(key, ref.Binding.Carrier, ref.Binding.Endpoint, handler, 2*time.Second, router.ScopeDiscovery)
		if err != nil {
			c.setError(fmt.Errorf("register %s with router: %w", key, err))
		}
		if err := c.carrierRouter.SetCursorHandler(key, func(carrierID, endpointID string, cursor carriers.Cursor) {
			c.setCursor(ref.ID, cursor)
		}); err != nil {
			c.setError(fmt.Errorf("set cursor handler for %s: %w", key, err))
		}
	}
}

// handleControlEnvelope dispatches a single envelope from a carrier read.
// This replaces the switch in pollOnce for router-based polling.
func (c *ControlPlane) handleControlEnvelope(ctx context.Context, envelope fabric.Envelope, ref carrierRef) {
	if !c.validateCarrierControlEnvelope(envelope) {
		return
	}
	switch envelope.PayloadType {
	case "node.advertise":
		ad, err := session.DecodePayload[session.NodeAdvertisement](envelope.Payload)
		if err != nil {
			c.setError(err)
			return
		}
		c.storeAdvertisement(ad)
	case "node.withdraw":
		w, err := session.DecodePayload[session.NodeWithdrawal](envelope.Payload)
		if err != nil {
			c.setError(err)
			return
		}
		c.storeWithdrawal(w.NodeID)
	case "node.heartbeat":
		hb, err := session.DecodePayload[session.NodeHeartbeat](envelope.Payload)
		if err != nil {
			c.setError(err)
			return
		}
		c.storeHeartbeat(hb)
	case session.PayloadSessionOffer:
		if c.cfg.Role != config.RoleNode {
			return
		}
		offer, err := session.DecodePayload[session.Offer](envelope.Payload)
		if err != nil {
			c.setError(err)
			return
		}
		c.handleOffer(ctx, offer, ref)
	case session.PayloadSessionAnswer, session.PayloadSessionAnswerCompressed, session.PayloadSessionAnswerChunk, session.PayloadSessionAnswerGzipChunk:
		if c.cfg.Role != config.RoleClient {
			return
		}
		answer, handled, err := c.engine.DecodeAnswerEnvelope(envelope)
		if err != nil {
			c.setError(err)
			return
		}
		if !handled {
			return
		}
		c.handleAnswer(answer)
	case session.PayloadSessionOfferAck:
		if c.cfg.Role != config.RoleClient {
			return
		}
		ack, err := session.DecodePayload[session.OfferAck](envelope.Payload)
		if err != nil {
			c.setError(err)
			return
		}
		c.handleOfferAck(ack)
	case session.PayloadSessionRelease:
		if c.cfg.Role != config.RoleNode {
			return
		}
		release, err := session.DecodePayload[session.Release](envelope.Payload)
		if err != nil {
			c.setError(err)
			return
		}
		c.handleRelease(ctx, release)
	case session.PayloadSessionError:
		sessErr, err := session.DecodePayload[session.SessionError](envelope.Payload)
		if err != nil {
			c.setError(err)
			return
		}
		c.handleSessionError(sessErr)
	}
	// Stale node detection on every envelope processed.
	c.expireStaleNodes()
}

// startEgressHandler starts the node-side egress polling loop if the tunnel
// exposes a ServeEgress method and the config has egress-capable carriers.
func (c *ControlPlane) startEgressHandler(ctx context.Context) {
	if c.cfg.Role != config.RoleNode || len(c.egress) == 0 {
		return
	}
	server, ok := c.tunnel.(interface {
		ServeEgress(context.Context, map[string]policy.CarrierBinding) error
	})
	if !ok {
		return
	}
	egressBindings := make(map[string]policy.CarrierBinding, len(c.egress))
	for _, ref := range c.egress {
		egressBindings[ref.ID] = ref.Binding
	}
	go func() {
		if err := server.ServeEgress(ctx, egressBindings); err != nil && err != context.Canceled {
			c.setError(err)
		}
	}()
}

func (c *ControlPlane) Stop() {
	c.mu.Lock()
	if c.sessionTimer != nil {
		c.sessionTimer.Stop()
		c.sessionTimer = nil
	}
	if c.idleTimer != nil {
		c.idleTimer.Stop()
		c.idleTimer = nil
	}
	active := c.active
	nodeSessionID := c.nodeSessionID
	nodeSessionSSH := c.nodeSessionSSH
	c.nodeSessionSSH = nil
	sessionSSHIssuer := c.sessionSSHIssuer
	postSessionCancel := c.postSessionCancel
	c.postSessionCancel = nil
	recoveryCancel := c.recoveryCancel
	c.recoveryCancel = nil
	routeStreams := c.egressRouteStreams
	c.egressRouteStreams = nil
	c.active = nil
	c.state = statusStateDisconnected
	c.invalidateSystemVPNProfileLocked("disconnected")
	c.mu.Unlock()
	if routeStreams != nil {
		sessionID := ""
		if active != nil {
			sessionID = active.SessionID
		}
		routeStreams.shutdownSession(sessionID)
	}
	if recoveryCancel != nil {
		recoveryCancel()
	}
	if active != nil {
		c.closePacketSession(active.SessionID)
	}
	c.closePacketSession(nodeSessionID)
	if postSessionCancel != nil {
		postSessionCancel()
		c.postSessionWG.Wait()
	}
	if nodeSessionSSH != nil {
		_ = nodeSessionSSH.Revoke(context.Background())
	}
	if sessionSSHIssuer != nil {
		_ = sessionSSHIssuer.Close(context.Background())
	}

	if active != nil {
		for _, ref := range c.egress {
			if vta, ok := ref.Binding.Carrier.(interface{ Close() error }); ok {
				_ = vta.Close()
			}
		}
	}
	select {
	case <-c.stopCh:
	default:
		close(c.stopCh)
	}
	c.recoveryWG.Wait()
}

// CarrierHealthSnapshot returns the carrier health data if a router is attached.
func (c *ControlPlane) CarrierHealthSnapshot() map[string]router.CarrierSnapshot {
	if c.carrierHealth != nil {
		return c.carrierHealth.Snapshot()
	}
	return nil
}

// ListNodes returns discovered available nodes ordered by availability then label.
func (c *ControlPlane) ListNodes() []NodeView {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]NodeView, 0, len(c.nodes))
	for _, discovered := range c.nodes {
		ad := discovered.Advertisement
		if c.cfg.Role == config.RoleNode && ad.NodeID == c.cfg.NodeID {
			continue
		}
		out = append(out, NodeView{
			NodeID:       ad.NodeID,
			Label:        nodeLabel(ad),
			Country:      ad.Country,
			Region:       ad.Region,
			Capabilities: append([]string(nil), ad.Capabilities...),
			Available:    !discovered.Withdrawn,
			LastSeenAt:   discovered.LastSeenAt,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Available != out[j].Available {
			return out[i].Available
		}
		return out[i].Label < out[j].Label
	})
	return out
}

// Status returns the local runtime status snapshot.
func (c *ControlPlane) Status() StatusView {
	c.mu.RLock()
	now := time.Now().UTC()
	if c.profileBuilder != nil && c.profileBuilder.clock != nil {
		now = c.profileBuilder.clock.Now().UTC()
	}
	refreshProfile := c.cfg.Role == config.RoleClient && c.active != nil && c.state == statusStateConnected && c.systemVPNProfile != nil && !c.systemVPNProfile.ExpiresAt.After(now.Add(30*time.Second))
	c.mu.RUnlock()
	if refreshProfile {
		// Refresh synchronously while the still-valid snapshot has a safety
		// margin. Returning a stale profile would make the host either recurse
		// provider traffic into the tunnel or keep routes past DNS authority.
		c.refreshSystemVPNProfile()
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.statusLocked()
}

// Connect opens a session to a chosen node or the best reachable node.
// prepareClientEgress attempts to create an egress room locally using the
// client's own platform credentials. This implements the role-reversal flow:
// the client acts as room host and the node joins as guest. Returns the room
// endpoint string (e.g. "wbstream://room-xyz") on success, or empty string if
// no video tunnel carrier with local credentials is available. Client tokens
// never leave the device — only the room endpoint is sent in the offer.
func (c *ControlPlane) prepareClientEgress(ctx context.Context) (string, string, bool) {
	for id, binding := range c.bindings {
		vta := resolveVideoTunnelAdapter(binding)
		if vta == nil {
			continue
		}
		roomAddr, err := vta.CreateAndStartEgress(ctx)
		if err != nil {
			dbg("prepareClientEgress: CreateAndStartEgress failed carrier=%s err=%v", id, err)
			continue
		}
		dbg("prepareClientEgress: room created carrier=%s addr=%s", id, roomAddr)
		ep := carriers.Endpoint{
			ID:      fmt.Sprintf("%s:%d", id, time.Now().UTC().UnixNano()),
			Carrier: id,
			Address: roomAddr,
		}
		sessionBinding := binding
		sessionBinding.Endpoint = ep
		c.setSessionTunnelBinding(ep, sessionBinding)
		return roomAddr, id, true
	}
	return "", "", false
}

// Connect attempts to establish a session. A non-empty node ID is a hard pin:
// only that node's contact endpoints are attempted. An empty node ID permits
// failover across all available nodes. If a session was previously active and
// gets disconnected, ConnectWithRetry reconnects to the same node.
func (c *ControlPlane) Connect(ctx context.Context, nodeID string) (StatusView, error) {
	c.mu.RLock()
	hadActiveSession := c.active != nil
	c.mu.RUnlock()

	if hadActiveSession {
		// Reconnection to the same node — use retry logic
		return c.ConnectWithRetry(ctx, nodeID, maxReconnectAttempts)
	}

	// First-time connect — try all nodes/carriers without retry
	return c.connectWithFailover(ctx, nodeID)
}

// ConnectWithRetry attempts to reconnect to a specific node with exponential
// backoff. Used when a previously active session gets disconnected.
func (c *ControlPlane) ConnectWithRetry(ctx context.Context, nodeID string, maxRetries int) (StatusView, error) {
	if c.cfg.Role != config.RoleClient {
		return c.Status(), errors.New("session connect requires client role")
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 2s, 4s, 8s, ...
			backoff := time.Duration(1<<uint(attempt-1)) * 2 * time.Second
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
			log.Printf("reconnect: attempt %d/%d after %v (last error: %v)",
				attempt, maxRetries, backoff, lastErr)

			c.mu.Lock()
			c.state = statusStateReconnecting
			c.reconnectAttempts = attempt
			c.mu.Unlock()

			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				c.setError(ctx.Err())
				c.setState(statusStateDisconnected)
				return c.Status(), ctx.Err()
			}
		}

		status, err := c.connectOnce(ctx, nodeID)
		if err == nil {
			// Success — reset reconnect counter
			c.mu.Lock()
			c.reconnectAttempts = 0
			c.mu.Unlock()
			return status, nil
		}

		lastErr = err
		log.Printf("reconnect: attempt %d failed: %v", attempt+1, err)

		// Don't retry on certain errors
		if isNonRetriableError(err) {
			log.Printf("reconnect: non-retriable error, stopping retries: %v", err)
			break
		}
	}

	c.setError(fmt.Errorf("reconnect failed after %d attempts: %w", maxRetries+1, lastErr))
	c.setState(statusStateDisconnected)
	return c.Status(), lastErr
}

// connectWithFailover tries the explicitly pinned node, or every available node
// when nodeID is empty. It attempts each selected node's contact endpoints in
// order without retrying an endpoint.
func (c *ControlPlane) connectWithFailover(ctx context.Context, nodeID string) (StatusView, error) {
	return c.connectWithFailoverExcluding(ctx, nodeID, "")
}

// connectWithFailoverExcluding tries the explicitly pinned node, or every
// discovered node except excludedNodeID when nodeID is empty. Node-level
// auto-heal uses the exclusion after every egress route in the active session
// has failed, so it cannot immediately recreate the failed session.
func (c *ControlPlane) connectWithFailoverExcluding(ctx context.Context, nodeID string, excludedNodeID string) (StatusView, error) {
	nodes := c.ListNodes()
	if excludedNodeID != "" {
		filtered := nodes[:0]
		for _, node := range nodes {
			if node.NodeID != excludedNodeID {
				filtered = append(filtered, node)
			}
		}
		nodes = filtered
	}
	if len(nodes) == 0 {
		err := errors.New("no discovered nodes")
		c.setError(err)
		c.setState(statusStateDisconnected)
		return c.Status(), err
	}

	var orderedNodes []NodeView
	if nodeID != "" {
		for _, node := range nodes {
			if node.NodeID == nodeID {
				orderedNodes = append(orderedNodes, node)
				break
			}
		}
		if len(orderedNodes) == 0 {
			err := fmt.Errorf("selected node %q was not discovered", nodeID)
			c.setError(err)
			c.setState(statusStateDisconnected)
			return c.Status(), err
		}
	} else {
		orderedNodes = append(orderedNodes, nodes...)
	}

	var lastErr error
	for _, nodeView := range orderedNodes {
		c.mu.RLock()
		wasDisconnected := c.state == statusStateDisconnected
		c.mu.RUnlock()
		if !wasDisconnected {
			// Another goroutine connected while we were iterating
			return c.Status(), nil
		}

		// Get all contact endpoints for this node (not just the first one)
		contacts := c.selectAllContacts(nodeView.NodeID)
		if len(contacts) == 0 {
			log.Printf("connect: node=%s has no compatible contact endpoints, skipping", nodeView.NodeID)
			continue
		}

		log.Printf("connect: trying node=%s with %d contact endpoint(s)", nodeView.NodeID, len(contacts))

		// Try each contact endpoint for this node
		for _, contactInfo := range contacts {
			status, err := c.connectOnceWithContact(ctx, nodeView.NodeID, contactInfo.endpoint, contactInfo.binding)
			if err == nil {
				log.Printf("connect: successfully connected to node=%s via %s", nodeView.NodeID, contactInfo.endpoint.Carrier)
				return status, nil
			}

			lastErr = err
			log.Printf("connect: node=%s via %s failed: %v, trying next contact...", nodeView.NodeID, contactInfo.endpoint.Carrier, err)

			// Don't try other contacts on auth errors
			if isNonRetriableError(err) {
				log.Printf("connect: non-retriable error on node=%s via %s, skipping node: %v", nodeView.NodeID, contactInfo.endpoint.Carrier, err)
				break
			}
		}

		// Check if we should stop trying other nodes
		if lastErr != nil && isNonRetriableError(lastErr) {
			log.Printf("connect: non-retriable error on node=%s, stopping: %v", nodeView.NodeID, lastErr)
			break
		}
	}

	errPrefix := "all nodes and contacts exhausted"
	if nodeID != "" {
		errPrefix = fmt.Sprintf("selected node %s contacts exhausted", nodeID)
	}
	err := fmt.Errorf("connect failed: %s, last error: %w", errPrefix, lastErr)
	c.setError(err)
	c.setState(statusStateDisconnected)
	return c.Status(), err
}

// contactInfo holds a contact endpoint and its binding for connection attempts.
type contactInfo struct {
	endpoint carriers.Endpoint
	binding  policy.CarrierBinding
}

// prioritizeReplyEndpoint keeps the control path that accepted the offer first.
// A client can advertise several role-qualified reply channels, but the node's
// credential and mailbox state are authoritative for the channel it just read.
// Preserving that path first avoids sending a valid answer through a secondary
// channel that the client cannot currently poll, while retaining every fallback.
func prioritizeReplyEndpoint(preferred carriers.Endpoint, endpoints []carriers.Endpoint) []carriers.Endpoint {
	if len(endpoints) == 0 {
		return nil
	}
	preferredCarrier := policy.CarrierIDFromBindingKey(preferred.Carrier)
	ordered := make([]carriers.Endpoint, 0, len(endpoints))
	matchIndex := -1
	for _, endpoint := range endpoints {
		carrierID := policy.CarrierIDFromBindingKey(endpoint.Carrier)
		matches := preferredCarrier != "" && carrierID == preferredCarrier &&
			strings.TrimSpace(preferred.Address) != "" && endpoint.Address == preferred.Address
		if matchIndex < 0 && matches {
			matchIndex = len(ordered)
		}
		ordered = append(ordered, endpoint)
	}
	if matchIndex < 0 {
		return appendLegacyReplyAliases(ordered)
	}
	// The matching endpoint was appended in place above. Rotate it to index 0
	// without rebuilding the remainder or changing its advertised order.
	matched := ordered[matchIndex]
	copy(ordered[1:matchIndex+1], ordered[:matchIndex])
	ordered[0] = matched

	return appendLegacyReplyAliases(ordered)
}

// appendLegacyReplyAliases adds each role-qualified Android node-client mailbox
// under its base carrier ID when that exact delivery address is not already in
// the offer. Older nodes can then send the answer without treating another
// role-qualified binding as an interchangeable control channel.
func appendLegacyReplyAliases(endpoints []carriers.Endpoint) []carriers.Endpoint {
	for _, endpoint := range endpoints {
		carrierID, role := policy.ParseBindingKey(endpoint.Carrier)
		if role != "node-client" || carrierID == "" {
			continue
		}
		alreadyAdvertised := false
		for _, candidate := range endpoints {
			if candidate.Carrier == carrierID && candidate.Address == endpoint.Address {
				alreadyAdvertised = true
				break
			}
		}
		if alreadyAdvertised {
			continue
		}
		legacy := endpoint
		legacy.Carrier = carrierID
		endpoints = append(endpoints, legacy)
	}
	return endpoints
}

// selectAllContacts returns ALL compatible contact endpoints for a node,
// ordered by preference (non-video-tunnel first, then video-tunnel as fallback).
func (c *ControlPlane) selectAllContacts(nodeID string) []contactInfo {
	c.mu.RLock()
	node, ok := c.nodes[nodeID]
	c.mu.RUnlock()
	if !ok {
		return nil
	}

	var primary []contactInfo
	var fallback []contactInfo

	for _, endpoint := range node.Advertisement.Carriers {
		binding, bindingKey := c.findBindingByCarrier(endpoint.Carrier)
		if bindingKey == "" {
			continue
		}

		info := contactInfo{endpoint: endpoint, binding: binding}
		if isVideoTunnelCarrier(binding) {
			fallback = append(fallback, info)
		} else {
			primary = append(primary, info)
		}
	}

	// Primary contacts first (VK messages, OK messages, etc.), then video-tunnel fallbacks
	return append(primary, fallback...)
}

// connectOnce performs a single connection attempt without retry logic.
func (c *ControlPlane) connectOnce(ctx context.Context, nodeID string) (StatusView, error) {
	if c.cfg.Role != config.RoleClient {
		return c.Status(), errors.New("session connect requires client role")
	}

	node, err := c.selectNode(nodeID)
	if err != nil {
		c.setError(err)
		return c.Status(), err
	}

	contact, binding, err := c.selectContact(node)
	if err != nil {
		c.setError(err)
		return c.Status(), err
	}

	return c.connectOnceWithContact(ctx, nodeID, contact, binding)
}

// connectOnceWithContact performs a single connection attempt with a specific
// contact endpoint (used by connectWithFailover to try multiple contacts).
func (c *ControlPlane) connectOnceWithContact(ctx context.Context, nodeID string, contact carriers.Endpoint, binding policy.CarrierBinding) (StatusView, error) {
	if len(c.replyEndpoints) == 0 {
		err := errors.New("no reply endpoints available for client session")
		c.setError(err)
		return c.Status(), err
	}

	sessionID := fmt.Sprintf("%s-%d", c.cfg.Identity(), time.Now().UTC().UnixNano())
	answerCh := make(chan session.Answer, 1)
	errorCh := make(chan session.SessionError, 1)
	ackState := &pendingAckState{ch: make(chan session.OfferAck, 1)}

	c.mu.Lock()
	c.pending[sessionID] = answerCh
	c.pendingErrors[sessionID] = errorCh
	c.pendingAcks[sessionID] = ackState
	c.pendingTargetNodes[sessionID] = nodeID
	c.state = statusStateConnecting
	c.lastError = ""
	c.mu.Unlock()

	defer func() {
		dbg("[connect] DEFER CLEANUP session=%s (Connect returning)", sessionID)
		c.mu.Lock()
		delete(c.pending, sessionID)
		delete(c.pendingErrors, sessionID)
		delete(c.pendingAcks, sessionID)
		delete(c.pendingTargetNodes, sessionID)
		roomEndpoint := c.clientRoomEndpoint
		connected := c.state == statusStateConnected
		if !connected && roomEndpoint != "" {
			c.clientRoomEndpoint = ""
			c.clientRoomCarrier = ""
		}
		c.mu.Unlock()
		if !connected && roomEndpoint != "" {
			for _, ref := range c.egress {
				if vta := resolveVideoTunnelAdapter(ref.Binding); vta != nil {
					vta.Close()
				}
			}
		}
	}()

	// Attempt role-reversal: create the egress room locally using the client's
	// own credentials so the node can join as guest. Best-effort: if no local
	// credentials are available, falls back to legacy node-creates-room flow.
	// If singbox.vless is available as egress, skip room creation — the node
	// will create its own room and singbox provides the TCP tunnel.
	clientRoomAddr, clientRoomCarrier, clientHasRoom := "", "", false
	if c.cfg.ClientRoomCreation {
		hasSingbox := false
		for _, ref := range c.egress {
			if ref.Descriptor.ID == carriers.CarrierSingBoxVLESS {
				hasSingbox = true
				break
			}
		}
		if !hasSingbox {
			if preparedAddr, preparedCarrier, ok := c.prepareClientEgress(ctx); ok {
				clientRoomAddr = preparedAddr
				clientRoomCarrier = preparedCarrier
				clientHasRoom = true
				log.Printf("connect: client created egress room carrier=%s addr=%s (role reversal)", clientRoomCarrier, clientRoomAddr)
			}
		} else {
			log.Printf("connect: singbox.vless available, skipping room creation (role reversal via VLESS)")
		}
	}

	offer := session.Offer{
		SessionID:      sessionID,
		ClientID:       c.cfg.Identity(),
		ProductVersion: c.localProductVersion(),
		TargetNodeID:   nodeID,
		Wanted:         []string{"egress"},
		ReplyEndpoints: prioritizeReplyEndpoint(contact, c.replyEndpoints),
		ExpiresAt:      time.Now().UTC().Add(sessionOfferTTL),
	}

	if clientHasRoom {
		offer.ClientRoomEndpoint = clientRoomAddr
		c.mu.Lock()
		c.clientRoomEndpoint = clientRoomAddr
		c.clientRoomCarrier = clientRoomCarrier
		c.mu.Unlock()
	}
	// The node does not currently consume client carrier descriptors during
	// session negotiation. Leaving them out keeps the offer under VK message
	// size limits so control-plane setup can complete end-to-end.

	// Generate session key and encrypt with the negotiated bootstrap context.
	// v2 is selected only when the node advertised support; this keeps old
	// nodes on the legacy provider-token derivation path.
	bootstrapCipher := c.bootstrapCipherForNode(nodeID)
	var sessionKey [32]byte
	if bootstrapCipher != nil {
		var err error
		sessionKey, err = fabric.GenerateSessionKey()
		if err != nil {
			c.setError(err)
			c.setState(statusStateDisconnected)
			return c.Status(), err
		}
		encrypted, err := session.EncryptSessionKey(bootstrapCipher, sessionKey[:])
		if err != nil {
			c.setError(fmt.Errorf("encrypt session key: %w", err))
			c.setState(statusStateDisconnected)
			return c.Status(), err
		}
		offer.SessionKey = encrypted
		if bootstrapCipher == c.bootstrapSecretCipher {
			if offer.Metadata == nil {
				offer.Metadata = make(map[string]string)
			}
			offer.Metadata[bootstrapKeyMetadata] = bootstrapKeyV2
		}
	}

	log.Printf("connect: sending offer session=%s via carrier=%s addr=%s", offer.SessionID, contact.Carrier, contact.Address)
	if err := c.engine.SendOffer(ctx, binding.Carrier, contact, offer); err != nil {
		log.Printf("connect: SendOffer error: %v", err)
		c.recordCarrierUsage(contact.Carrier, contact.Address, 1, 0, err)
		c.setError(err)
		c.setState(statusStateDisconnected)
		return c.Status(), err
	}
	log.Printf("connect: offer sent ok session=%s", offer.SessionID)
	c.recordCarrierUsage(contact.Carrier, contact.Address, 1, 0, nil)

	// Wait for ACK first (short timeout).
	ackTimer := time.NewTimer(ackTimeout)
	defer ackTimer.Stop()

	select {
	case ack := <-ackState.ch:
		ackTimer.Stop()
		if ack.Status == "busy" {
			// Retry with capped backoff.
			for attempt := 1; attempt <= maxBusyRetries; attempt++ {
				backoff := c.busyRetryAfter
				if ack.RetryAfter > 0 && ack.RetryAfter < c.busyRetryAfter {
					backoff = ack.RetryAfter
				}
				select {
				case <-time.After(backoff):
				case <-ctx.Done():
					c.setError(ctx.Err())
					c.setState(statusStateDisconnected)
					return c.Status(), ctx.Err()
				}
				// Re-send offer.
				sessionID = fmt.Sprintf("%s-%d", c.cfg.Identity(), time.Now().UTC().UnixNano())
				offer.SessionID = sessionID
				c.mu.Lock()
				c.pending[sessionID] = answerCh
				c.pendingErrors[sessionID] = errorCh
				c.pendingAcks[sessionID] = ackState
				c.pendingTargetNodes[sessionID] = nodeID
				c.mu.Unlock()
				if err := c.engine.SendOffer(ctx, binding.Carrier, contact, offer); err != nil {
					c.recordCarrierUsage(contact.Carrier, contact.Address, 1, 0, err)
					c.setError(err)
					c.setState(statusStateDisconnected)
					return c.Status(), err
				}
				c.recordCarrierUsage(contact.Carrier, contact.Address, 1, 0, nil)
				// Wait for ACK again.
				ackTimer.Reset(ackTimeout)
				select {
				case ack = <-ackState.ch:
					ackTimer.Stop()
					if ack.Status != "busy" {
						goto waitForAnswer
					}
				case <-ackTimer.C:
					// No ACK, proceed to answer wait.
					goto waitForAnswer
				case sessErr := <-errorCh:
					err := sessionErrorConnectError(sessErr)
					c.setError(err)
					c.setState(statusStateDisconnected)
					return c.Status(), err
				case <-ctx.Done():
					c.setError(ctx.Err())
					c.setState(statusStateDisconnected)
					return c.Status(), ctx.Err()
				}
			}
			err := fmt.Errorf("node %s is busy after %d retries", nodeID, maxBusyRetries)
			c.setError(err)
			c.setState(statusStateDisconnected)
			return c.Status(), err
		}
		if ack.Status == "error" {
			err := fmt.Errorf("node rejected offer: %s", ack.Error)
			c.setError(err)
			c.setState(statusStateDisconnected)
			return c.Status(), err
		}
		// Status "received" — proceed to wait for answer.
	case <-ackTimer.C:
		// No ACK received, proceed to answer wait anyway.
	case sessErr := <-errorCh:
		err := sessionErrorConnectError(sessErr)
		c.setError(err)
		c.setState(statusStateDisconnected)
		return c.Status(), err
	case <-ctx.Done():
		dbg("[connect] context cancelled during ACK wait session=%s err=%v", sessionID, ctx.Err())
		c.setError(ctx.Err())
		c.setState(statusStateDisconnected)
		return c.Status(), ctx.Err()
	}

	dbg("[connect] entering answer wait session=%s", sessionID)
waitForAnswer:
	timeout := time.NewTimer(sessionAnswerTimeout)
	defer timeout.Stop()

	select {
	case answer := <-answerCh:
		// When the node joined the client's pre-created room (role reversal),
		// EgressEndpoints is empty and JoinedClientRoom is true. The client is
		// already hosting the room via CreateAndStartEgress, so we use the
		// stored room endpoint and skip StartEgressAddr (guest join).
		if answer.JoinedClientRoom && c.clientRoomEndpoint != "" {
			profiles, err := session.OpenEgressProfiles(answer.EgressProfilesCiphertext, answer.SessionID, answer.NodeID, sessionKey)
			log.Printf("connect: joined client room egress profiles ciphertext_bytes=%d profiles=%d", len(answer.EgressProfilesCiphertext), len(profiles))
			if len(answer.EgressProfilesCiphertext) > 0 && err != nil {
				c.setError(fmt.Errorf("open server egress profiles: %w", err))
				c.setState(statusStateDisconnected)
				return c.Status(), err
			}
			profileByEndpoint := make(map[string]session.EgressProfile, len(profiles))
			for _, profile := range profiles {
				profileByEndpoint[profile.EndpointID] = profile
			}
			c.mu.RLock()
			roomCarrier := c.clientRoomCarrier
			roomAddr := c.clientRoomEndpoint
			c.mu.RUnlock()
			log.Printf("connect: node joined client room session=%s carrier=%s addr=%s", answer.SessionID, roomCarrier, roomAddr)
			clientEP := carriers.Endpoint{
				ID:      fmt.Sprintf("%s:%d", roomCarrier, time.Now().UTC().UnixNano()),
				Carrier: roomCarrier,
				Address: roomAddr,
			}
			allEndpoints := append([]carriers.Endpoint{clientEP}, answer.EgressEndpoints...)
			for _, ep := range answer.EgressEndpoints {
				baseBinding, bindingKey := c.findBindingByCarrier(ep.Carrier)
				if profile, ok := profileByEndpoint[ep.ID]; ok {
					profileBinding, profileErr := sessionBindingFromEgressProfileWithRuntime(ep, profile, c.cfg.SessionEgress)
					if profileErr != nil {
						return c.Status(), fmt.Errorf("server profile %s: %w", ep.ID, profileErr)
					}
					baseBinding = profileBinding
					bindingKey = ep.Carrier
				}
				if bindingKey != "" {
					sessionBinding := baseBinding
					sessionBinding.Endpoint = ep
					c.setSessionTunnelBinding(ep, sessionBinding)
				}
			}
			c.mu.Lock()
			c.active = &activeSession{
				NodeID:          answer.NodeID,
				SessionID:       answer.SessionID,
				ControlEndpoint: contact,
				ControlBinding:  binding,
				EgressEndpoints: allEndpoints,
				UpdatedAt:       time.Now().UTC(),
				ExpiresAt:       answer.ExpiresAt,
			}
			c.egressRecovery = nil
			c.initializeAutomaticEgressRouteLocked()
			c.profileRevision++
			c.systemVPNProfile = nil
			c.systemVPNReadiness = &runtimeapi.SystemVPNReadiness{Ready: false, Provenance: "runtime/session", Reason: "profile_refresh_required"}
			c.state = statusStateConnected
			c.reconnectAttempts = 0
			c.lastError = ""
			c.mu.Unlock()
			c.refreshSystemVPNProfile()

			c.activatePacketSession(answer.SessionID, answer.NodeID, answer.ExpiresAt, sessionKey, sessionKey != [32]byte{})
			go c.monitorSessionLiveness(ctx, answer)
			go c.clientSessionTimeoutMonitor(ctx, answer)
			return c.Status(), nil
		}

		if len(answer.EgressEndpoints) == 0 {
			err := fmt.Errorf("session answer from %s did not include egress endpoints", answer.NodeID)
			c.setError(err)
			c.setState(statusStateDisconnected)
			return c.Status(), err
		}
		sessionEndpoints := make([]carriers.Endpoint, 0, len(answer.EgressEndpoints))
		profiles, err := session.OpenEgressProfiles(answer.EgressProfilesCiphertext, answer.SessionID, answer.NodeID, sessionKey)
		log.Printf("connect: egress profile envelope ciphertext_bytes=%d profiles=%d endpoints=%d", len(answer.EgressProfilesCiphertext), len(profiles), len(answer.EgressEndpoints))
		if len(answer.EgressProfilesCiphertext) > 0 && err != nil {
			c.setError(fmt.Errorf("open server egress profiles: %w", err))
			c.setState(statusStateDisconnected)
			return c.Status(), err
		}
		profileByEndpoint := make(map[string]session.EgressProfile, len(profiles))
		for _, profile := range profiles {
			profileByEndpoint[profile.EndpointID] = profile
		}
		var egressSetupErrors []error
		for _, endpoint := range answer.EgressEndpoints {
			sessionEndpoint := endpoint
			dbg("PROCESSING endpoint: carrier=%s address=%s", endpoint.Carrier, endpoint.Address)
			// Lookup binding: try exact key first, then platform-prefix fallback
			// (nodes may send "wbstream" while client binding key is "wbstream.vp8").
			baseBinding, bindingKey := c.findBindingByCarrier(endpoint.Carrier)
			if profile, ok := profileByEndpoint[endpoint.ID]; ok {
				profileBinding, profileErr := sessionBindingFromEgressProfileWithRuntime(endpoint, profile, c.cfg.SessionEgress)
				if profileErr != nil {
					return c.Status(), fmt.Errorf("server profile %s: %w", endpoint.ID, profileErr)
				}
				baseBinding = profileBinding
				bindingKey = endpoint.Carrier
			}
			if bindingKey != "" {
				dbg("endpoint carrier=%s found in bindings as %s", endpoint.Carrier, bindingKey)
				sessionEndpoint = normalizeSessionEgressEndpoint(endpoint, bindingKey, baseBinding)
				sessionBinding := baseBinding
				sessionBinding.Endpoint = sessionEndpoint
				c.setSessionTunnelBinding(sessionEndpoint, sessionBinding)
			} else {
				dbg("endpoint carrier=%s NOT found in bindings", endpoint.Carrier)
			}
			if c.isVideoTunnelCarrierByID(endpoint.Carrier) {
				dbg("endpoint carrier=%s IS in videoTunnelCarrierIDs", endpoint.Carrier)
				if bindingKey != "" {
					vta := resolveVideoTunnelAdapter(baseBinding)
					if vta != nil {
						dbg("calling StartEgressAddr for carrier=%s address=%s", endpoint.Carrier, endpoint.Address)
						if err := vta.StartEgressAddr(ctx, endpoint.Address); err != nil {
							log.Printf("video tunnel StartEgressAddr FAILED carrier=%s err=%v", endpoint.Carrier, err)
							egressSetupErrors = append(egressSetupErrors, fmt.Errorf("video tunnel client egress: %w", err))
						} else {
							dbg("StartEgressAddr OK carrier=%s", endpoint.Carrier)
						}
					} else {
						dbg("resolveVideoTunnelAdapter returned nil for carrier=%s", endpoint.Carrier)
					}
				} else {
					dbg("baseBinding not found for video tunnel carrier=%s", endpoint.Carrier)
				}
			} else {
				dbg("endpoint carrier=%s NOT in videoTunnelCarrierIDs", endpoint.Carrier)
			}
			sessionEndpoints = append(sessionEndpoints, sessionEndpoint)
		}
		c.mu.Lock()
		c.active = &activeSession{
			NodeID:          answer.NodeID,
			SessionID:       answer.SessionID,
			ControlEndpoint: contact,
			ControlBinding:  binding,
			EgressEndpoints: sessionEndpoints,
			UpdatedAt:       time.Now().UTC(),
			ExpiresAt:       answer.ExpiresAt,
		}
		c.egressRecovery = nil
		c.initializeAutomaticEgressRouteLocked()
		c.profileRevision++
		c.systemVPNProfile = nil
		c.systemVPNReadiness = &runtimeapi.SystemVPNReadiness{Ready: false, Provenance: "runtime/session", Reason: "profile_refresh_required"}
		setupErr := errors.Join(egressSetupErrors...)
		if setupErr != nil {
			c.state = statusStateDegraded
			c.lastError = setupErr.Error()
		} else {
			c.state = statusStateConnected
			c.lastError = ""
		}
		c.reconnectAttempts = 0
		c.mu.Unlock()
		if setupErr == nil {
			c.refreshSystemVPNProfile()
		}

		// Set session cipher on tunnel if we generated a session key.
		c.activatePacketSession(answer.SessionID, answer.NodeID, answer.ExpiresAt, sessionKey, sessionKey != [32]byte{})

		// Start WBStream reconnect monitor.
		go c.monitorSessionLiveness(ctx, answer)
		go c.clientSessionTimeoutMonitor(ctx, answer)

		return c.Status(), nil
	case sessErr := <-errorCh:
		err := sessionErrorConnectError(sessErr)
		c.setError(err)
		c.setState(statusStateDisconnected)
		return c.Status(), err
	case <-timeout.C:
		dbg("[connect] answer wait TIMEOUT session=%s", sessionID)
		err := fmt.Errorf("timed out waiting for session answer from %s", nodeID)
		c.setError(err)
		c.setState(statusStateDisconnected)
		return c.Status(), err
	case <-ctx.Done():
		dbg("[connect] context cancelled during answer wait session=%s err=%v", sessionID, ctx.Err())
		err := ctx.Err()
		c.setError(err)
		c.setState(statusStateDisconnected)
		return c.Status(), err
	}
}

// Disconnect clears the active session route.
func (c *ControlPlane) Disconnect() StatusView {
	c.mu.Lock()
	active := c.active
	routeStreams := c.egressRouteStreams
	c.egressRouteStreams = nil
	c.active = nil
	c.state = statusStateDisconnected
	c.clientRoomEndpoint = ""
	c.clientRoomCarrier = ""
	c.invalidateSystemVPNProfileLocked("disconnected")
	c.mu.Unlock()
	if routeStreams != nil {
		sessionID := ""
		if active != nil {
			sessionID = active.SessionID
		}
		routeStreams.shutdownSession(sessionID)
	}
	if active != nil {
		c.closePacketSession(active.SessionID)
		c.sendRelease(active)
		c.clearSessionTunnelBindings(active.EgressEndpoints)
		c.closeVideoTunnelSessions(active.EgressEndpoints)
		// Clear cipher on disconnect.
		if tunnelCipher, ok := c.tunnel.(interface{ ClearCipher() }); ok {
			tunnelCipher.ClearCipher()
		}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.statusLocked()
}

// SelectEgressEndpoint pins one active-session endpoint for an explicit
// operator diagnostic. Normal sessions retain adaptive multi-route failover.
func (c *ControlPlane) SelectEgressEndpoint(endpointID string) (StatusView, error) {
	requestedID := strings.TrimSpace(endpointID)
	if requestedID == "" {
		return c.Status(), errors.New("selected egress endpoint ID is required")
	}

	c.mu.Lock()
	if c.active == nil {
		status := c.statusLocked()
		c.mu.Unlock()
		return status, errors.New("cannot select egress endpoint without an active session")
	}
	if requestedID == "auto" {
		if c.active.SelectedEgressEndpointID != "" {
			c.active.AutomaticEgressEndpointID = c.active.SelectedEgressEndpointID
		}
		c.active.SelectedEgressEndpointID = ""
		if c.active.AutomaticEgressEndpointID == "" {
			c.initializeAutomaticEgressRouteLocked()
		}
		c.invalidateSystemVPNProfileLocked("egress_selection_changed")
		c.mu.Unlock()
		c.refreshSystemVPNProfile()
		return c.Status(), nil
	}
	for _, endpoint := range c.active.EgressEndpoints {
		if endpoint.ID != requestedID {
			continue
		}
		if sameSessionEndpoint(endpoint, c.active.ControlEndpoint) && !c.combinedControlEndpointSupportsEgress(endpoint) {
			status := c.statusLocked()
			c.mu.Unlock()
			return status, fmt.Errorf("cannot select control endpoint %q for SOCKS egress", requestedID)
		}
		c.active.SelectedEgressEndpointID = requestedID
		c.invalidateSystemVPNProfileLocked("egress_selection_changed")
		c.mu.Unlock()
		c.refreshSystemVPNProfile()
		return c.Status(), nil
	}
	status := c.statusLocked()
	c.mu.Unlock()
	return status, fmt.Errorf("unknown active egress endpoint %q", requestedID)
}

// DialEgress opens an egress connection using the active carrier session.
// Endpoints are sorted by policy score (best first) and dialed in parallel
// batches of up to 3, preferring diverse carrier platforms per batch.
// Each dial has a per-endpoint timeout to bound worst-case latency.
func (c *ControlPlane) DialEgress(ctx context.Context, targetAddr string) (net.Conn, string, error) {
	active := c.activeSessionSnapshot()
	if active != nil && active.SelectedEgressEndpointID == "" {
		return c.dialAutomaticEgressWithLiveness(ctx, active, targetAddr)
	}
	return c.dialActiveEgressOnce(ctx, targetAddr)
}

// requiresSynchronousNodeLivenessProbe identifies retained mailbox egress.
// These carriers can accept an outbound envelope after the node process has
// died, so their SOCKS dial needs a control-path proof before replying. Stream
// and realtime tunnels own their liveness lifecycle and must not be forced
// through mailbox offer/ACK polling.
func (c *ControlPlane) requiresSynchronousNodeLivenessProbe(active *activeSession) bool {
	if active == nil {
		return false
	}
	for _, endpoint := range active.EgressEndpoints {
		binding, key := c.findBindingByCarrier(endpoint.Carrier)
		if key == "" || binding.Carrier == nil {
			continue
		}
		desc := binding.Carrier.Descriptor()
		if desc.ID == carriers.CarrierFileMailbox && carriers.HasCapability(desc, carriers.CapRetained) {
			return true
		}
	}
	return false
}

// dialAutomaticEgressWithLiveness makes node loss observable before a SOCKS
// handler receives an upstream connection. Retained file.mailbox routes first
// prove control-plane liveness; every other automatic route retries exactly
// once through another node after a typed carrier failure. Healthy dials stay
// concurrent; only replacement is coalesced so waiters use the new session.
func (c *ControlPlane) dialAutomaticEgressWithLiveness(ctx context.Context, expected *activeSession, targetAddr string) (net.Conn, string, error) {
	active := c.activeSessionSnapshot()
	if active == nil {
		return c.dialActiveEgressOnce(ctx, targetAddr)
	}
	if active.SelectedEgressEndpointID != "" {
		return c.dialActiveEgressOnce(ctx, targetAddr)
	}
	if expected != nil && active.SessionID != expected.SessionID {
		// Another concurrent caller completed replacement while this one waited.
		return c.dialActiveEgressOnce(ctx, targetAddr)
	}

	if c.requiresSynchronousNodeLivenessProbe(active) {
		probeCtx, cancelProbe := context.WithTimeout(ctx, nodeLivenessProbeTimeout)
		livenessErr := c.probeActiveNodeLiveness(probeCtx, active)
		cancelProbe()
		if livenessErr != nil {
			return c.recoverAutomaticNodeAfterFailure(ctx, active, targetAddr, livenessErr)
		}
	}

	conn, route, err := c.dialActiveEgressOnce(ctx, targetAddr)
	if err != nil && session.IsCarrierFailure(err) {
		return c.recoverAutomaticNodeAfterFailure(ctx, active, targetAddr, err)
	}
	return conn, route, err
}

func (c *ControlPlane) recoverAutomaticNodeAfterFailure(ctx context.Context, failed *activeSession, targetAddr string, failure error) (net.Conn, string, error) {
	c.nodeAutoHealMu.Lock()
	current := c.activeSessionSnapshot()
	if current == nil || current.SessionID != failed.SessionID || current.SelectedEgressEndpointID != "" {
		c.nodeAutoHealMu.Unlock()
		return c.dialActiveEgressOnce(ctx, targetAddr)
	}

	reselectCtx, cancelReselect := context.WithTimeout(ctx, nodeReselectionTimeout)
	_, _, reselectErr := c.reselectNodeAfterCarrierExhaustionLocked(reselectCtx, failed, "")
	cancelReselect()
	c.nodeAutoHealMu.Unlock()
	if reselectErr != nil {
		if errors.Is(failure, ErrNodeLiveness) {
			return nil, "carrier-session", fmt.Errorf("%w; replacement failed: %w", failure, reselectErr)
		}
		return nil, "carrier-session", reselectErr
	}
	return c.dialActiveEgressOnce(ctx, targetAddr)
}

func (c *ControlPlane) activeSessionSnapshot() *activeSession {
	c.mu.Lock()
	if c.active != nil && c.active.SelectedEgressEndpointID == "" && c.active.AutomaticEgressEndpointID == "" {
		c.initializeAutomaticEgressRouteLocked()
	}
	c.promoteRecoveredEgressLocked()
	active := c.active
	if active != nil {
		activeSnapshot := *active
		activeSnapshot.EgressEndpoints = append([]carriers.Endpoint(nil), active.EgressEndpoints...)
		active = &activeSnapshot
	}
	c.mu.Unlock()
	return active
}

// dialActiveEgressOnce performs exactly one carrier-only dial over the current
// session. It never opens a direct socket and never starts node reselection.
func (c *ControlPlane) dialActiveEgressOnce(ctx context.Context, targetAddr string) (net.Conn, string, error) {
	c.mu.Lock()
	if c.active != nil && c.active.SelectedEgressEndpointID == "" && c.active.AutomaticEgressEndpointID == "" {
		c.initializeAutomaticEgressRouteLocked()
	}
	c.promoteRecoveredEgressLocked()
	active := c.active
	tunnel := c.tunnel
	recovery := c.egressRecovery
	if c.egressRouteStreams == nil {
		c.egressRouteStreams = newRouteStreamRegistry(defaultRouteInterruptAfter, nil)
	}
	routeStreams := c.egressRouteStreams
	if active != nil {
		activeSnapshot := *active
		activeSnapshot.EgressEndpoints = append([]carriers.Endpoint(nil), active.EgressEndpoints...)
		active = &activeSnapshot
	}
	c.mu.Unlock()
	conn, route, err := c.dialActiveSessionEgressWithRegistry(ctx, active, tunnel, recovery, routeStreams, targetAddr)
	if active != nil {
		c.recordEgressRouteResult(active.SessionID, route, err)
	}
	return conn, route, err
}

// reselectNodeAfterCarrierExhaustion replaces a failed automatic session only
// after DialEgress exhausted every route it advertised with a carrier failure.
// It deliberately does not run for a manual endpoint selection or a target
// error: neither is evidence that the selected node has failed.
func (c *ControlPlane) reselectNodeAfterCarrierExhaustion(ctx context.Context, failed *activeSession, targetAddr string) (net.Conn, string, error) {
	c.nodeAutoHealMu.Lock()
	defer c.nodeAutoHealMu.Unlock()
	return c.reselectNodeAfterCarrierExhaustionLocked(ctx, failed, targetAddr)
}

// reselectNodeAfterCarrierExhaustionLocked is called with nodeAutoHealMu held.
func (c *ControlPlane) reselectNodeAfterCarrierExhaustionLocked(ctx context.Context, failed *activeSession, targetAddr string) (net.Conn, string, error) {
	c.mu.Lock()
	if c.active == nil {
		c.mu.Unlock()
		return nil, "carrier-session", errors.New("active session disappeared during node auto-heal")
	}
	if c.active.SessionID != failed.SessionID {
		c.mu.Unlock()
		return nil, "carrier-session", errors.New("active session changed during node auto-heal")
	}
	if c.active.SelectedEgressEndpointID != "" {
		c.mu.Unlock()
		return nil, "carrier-session", errors.New("manual egress selection prevents node auto-heal")
	}

	active := c.active
	routeStreams := c.egressRouteStreams
	c.active = nil
	c.egressRouteStreams = nil
	c.state = statusStateDisconnected
	c.lastError = fmt.Sprintf("carrier routes exhausted for node %s; selecting another node", active.NodeID)
	c.clientRoomEndpoint = ""
	c.clientRoomCarrier = ""
	c.invalidateSystemVPNProfileLocked("node_autoheal")
	c.mu.Unlock()

	if routeStreams != nil {
		routeStreams.shutdownSession(active.SessionID)
	}
	c.closePacketSession(active.SessionID)
	c.clearSessionTunnelBindings(active.EgressEndpoints)
	c.closeVideoTunnelSessions(active.EgressEndpoints)
	if tunnelCipher, ok := c.tunnel.(interface{ ClearCipher() }); ok {
		tunnelCipher.ClearCipher()
	}

	log.Printf("egress node auto-heal: carrier routes exhausted node=%s; selecting another node", active.NodeID)
	if _, err := c.connectWithFailoverExcluding(ctx, "", active.NodeID); err != nil {
		return nil, "carrier-session", fmt.Errorf("node auto-heal after %s: %w", active.NodeID, err)
	}
	if targetAddr == "" {
		return nil, "", nil
	}

	c.mu.Lock()
	replacement := c.active
	replacementTunnel := c.tunnel
	replacementRecovery := c.egressRecovery
	if c.egressRouteStreams == nil {
		c.egressRouteStreams = newRouteStreamRegistry(defaultRouteInterruptAfter, nil)
	}
	replacementStreams := c.egressRouteStreams
	if replacement != nil {
		snapshot := *replacement
		snapshot.EgressEndpoints = append([]carriers.Endpoint(nil), replacement.EgressEndpoints...)
		replacement = &snapshot
	}
	c.mu.Unlock()

	conn, route, err := c.dialActiveSessionEgressWithRegistry(ctx, replacement, replacementTunnel, replacementRecovery, replacementStreams, targetAddr)
	if replacement != nil {
		c.recordEgressRouteResult(replacement.SessionID, route, err)
	}
	if err != nil {
		return nil, route, fmt.Errorf("node auto-heal egress via replacement session: %w", err)
	}
	log.Printf("egress node auto-heal: replaced node=%s with node=%s route=%s", active.NodeID, replacement.NodeID, route)
	return conn, route, nil
}

// ErrNodeLiveness identifies a bounded control-path liveness failure. It is
// wrapped in session.CarrierFailureError so a failed retained mailbox probe
// cannot be confused with an application target error.
var ErrNodeLiveness = errors.New("node liveness probe failed")

func nodeLivenessFailure(nodeID string, cause error) error {
	if cause == nil {
		cause = ErrNodeLiveness
	}
	return session.NewCarrierFailure(fmt.Errorf("%w: node %s: %w", ErrNodeLiveness, nodeID, cause))
}

// probeActiveNodeLiveness asks the currently busy node to acknowledge a fresh
// offer through its existing control endpoint. A busy/received ACK proves that
// the node is alive without changing the active session. This reuses the
// established session.offer/ack wire format instead of creating a new
// liveness protocol.
func (c *ControlPlane) probeActiveNodeLiveness(ctx context.Context, active *activeSession) error {
	if active == nil || active.NodeID == "" || active.ControlBinding.Carrier == nil {
		return nodeLivenessFailure("", errors.New("active control endpoint is unavailable"))
	}
	probeID := fmt.Sprintf("%s-liveness-%d", c.cfg.Identity(), time.Now().UTC().UnixNano())
	ackState := &pendingAckState{ch: make(chan session.OfferAck, 1)}

	c.mu.Lock()
	if c.active == nil || c.active.SessionID != active.SessionID || c.active.NodeID != active.NodeID {
		c.mu.Unlock()
		return nodeLivenessFailure(active.NodeID, errors.New("active session changed during liveness probe"))
	}
	c.pendingAcks[probeID] = ackState
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pendingAcks, probeID)
		c.mu.Unlock()
	}()

	offer := session.Offer{
		SessionID:      probeID,
		ClientID:       c.cfg.Identity(),
		ProductVersion: c.localProductVersion(),
		TargetNodeID:   active.NodeID,
		Wanted:         []string{"egress"},
		ReplyEndpoints: append([]carriers.Endpoint(nil), c.replyEndpoints...),
		ExpiresAt:      time.Now().UTC().Add(nodeLivenessProbeTimeout),
	}
	if bootstrapCipher := c.bootstrapCipherForNode(active.NodeID); bootstrapCipher != nil {
		sessionKey, err := fabric.GenerateSessionKey()
		if err != nil {
			return nodeLivenessFailure(active.NodeID, err)
		}
		encrypted, err := session.EncryptSessionKey(bootstrapCipher, sessionKey[:])
		if err != nil {
			return nodeLivenessFailure(active.NodeID, err)
		}
		offer.SessionKey = encrypted
		if bootstrapCipher == c.bootstrapSecretCipher {
			offer.Metadata = map[string]string{bootstrapKeyMetadata: bootstrapKeyV2}
		}
	}

	if err := c.engine.SendOffer(ctx, active.ControlBinding.Carrier, active.ControlEndpoint, offer); err != nil {
		return nodeLivenessFailure(active.NodeID, err)
	}
	select {
	case <-ackState.ch:
		return nil
	case <-ctx.Done():
		return nodeLivenessFailure(active.NodeID, ctx.Err())
	}
}

// OpenPacketEgress opens a UDP association on the active carrier session. It
// follows the selected egress endpoint and never creates a host-direct UDP
// socket on the client side.
func (c *ControlPlane) OpenPacketEgress(ctx context.Context, metadata session.PacketMetadata) (net.PacketConn, string, error) {
	c.mu.Lock()
	if c.active != nil && c.active.SelectedEgressEndpointID == "" && c.active.AutomaticEgressEndpointID == "" {
		c.initializeAutomaticEgressRouteLocked()
	}
	c.promoteRecoveredEgressLocked()
	active := c.active
	tunnel := c.tunnel
	recovery := c.egressRecovery
	if active != nil {
		snapshot := *active
		snapshot.EgressEndpoints = append([]carriers.Endpoint(nil), active.EgressEndpoints...)
		active = &snapshot
	}
	c.mu.Unlock()
	if active == nil {
		return nil, "", errors.New("no active carrier session")
	}
	packetEgress, ok := tunnel.(session.PacketEgress)
	if !ok {
		return nil, "carrier-session", errors.New("carrier packet egress is not implemented for the active session")
	}

	var endpoints []carriers.Endpoint
	for _, endpoint := range active.EgressEndpoints {
		if active.SelectedEgressEndpointID != "" && endpoint.ID != active.SelectedEgressEndpointID {
			continue
		}
		if active.SelectedEgressEndpointID == "" && active.AutomaticEgressEndpointID != endpoint.ID && recovery != nil && !recovery.CanDial(policy.EgressEndpointKey(endpoint)) {
			continue
		}
		if endpoint.Carrier == adminrelay.CarrierID || endpoint.ID == adminrelay.CarrierID {
			continue
		}
		if sameSessionEndpoint(endpoint, active.ControlEndpoint) && !c.combinedControlEndpointSupportsEgress(endpoint) {
			continue
		}
		if packetEgress.SupportsPacketEndpoint(endpoint) {
			endpoints = append(endpoints, endpoint)
		}
		if active.SelectedEgressEndpointID == "" && active.AutomaticEgressEndpointID != "" {
			sort.SliceStable(endpoints, func(i, j int) bool {
				return endpoints[i].ID == active.AutomaticEgressEndpointID && endpoints[j].ID != active.AutomaticEgressEndpointID
			})
		}
	}
	if len(endpoints) == 0 {
		return nil, "carrier-session", errors.New("no packet-capable egress endpoint in active session")
	}
	// Session and peer identity are authoritative control-plane state. Never
	// accept caller-supplied values that could retarget a packet association.
	metadata.SessionID = active.SessionID
	metadata.PeerID = active.NodeID
	metadata.ExpiresAt = active.ExpiresAt

	var errs []error
	for index, endpoint := range endpoints {
		attemptMetadata := metadata
		if len(endpoints) > 1 && attemptMetadata.FlowID != "" {
			// A failed/ambiguous carrier write must not make the next route reuse
			// a flow ID whose encrypted acknowledgement could later be replayed.
			attemptMetadata.FlowID = fmt.Sprintf("%s-route-%d", attemptMetadata.FlowID, index)
		}
		conn, err := packetEgress.OpenPacketConn(ctx, endpoint, attemptMetadata)
		if err == nil {
			c.recordEgressRouteResult(active.SessionID, endpoint.ID, nil)
			return conn, endpoint.ID, nil
		}
		if active.SelectedEgressEndpointID == "" && endpoint.ID == active.AutomaticEgressEndpointID {
			c.recordAutomaticEgressFailure(active.SessionID, endpoint, err)
		}
		errs = append(errs, fmt.Errorf("%s/%s: %w", endpoint.Carrier, endpoint.ID, err))
	}
	err := fmt.Errorf("all packet egress endpoints failed: %w", errors.Join(errs...))
	c.recordEgressResult(active.SessionID, err)
	return nil, "carrier-session", err
}

func (c *ControlPlane) setPacketSession(sessionID string, peerID string, expiresAt time.Time) {
	if lifecycle, ok := c.tunnel.(session.PacketSessionLifecycle); ok {
		lifecycle.SetPacketSession(sessionID, peerID, expiresAt)
	}
}

func (c *ControlPlane) activatePacketSession(sessionID string, peerID string, expiresAt time.Time, sessionKey [32]byte, encrypted bool) {
	if encrypted {
		if cipher, err := fabric.NewSessionCipher(sessionKey); err == nil {
			if tunnelCipher, ok := c.tunnel.(interface{ SetCipher(*fabric.EnvelopeCipher) }); ok {
				tunnelCipher.SetCipher(cipher)
			}
		}
	} else if tunnelCipher, ok := c.tunnel.(interface{ ClearCipher() }); ok {
		tunnelCipher.ClearCipher()
	}
	c.setPacketSession(sessionID, peerID, expiresAt)
}

func (c *ControlPlane) closePacketSession(sessionID string) {
	if lifecycle, ok := c.tunnel.(session.PacketSessionLifecycle); ok && sessionID != "" {
		lifecycle.ClosePacketSession(sessionID)
	}
}

// recordEgressResult updates product-visible health only when the dial belongs
// to the session that is still active. A completed dial from an old session
// must never overwrite the status of a newer connection.
func (c *ControlPlane) recordEgressResult(sessionID string, dialErr error) {
	c.recordEgressRouteResult(sessionID, "", dialErr)
}

func (c *ControlPlane) recordEgressRouteResult(sessionID string, routeID string, dialErr error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil || c.active.SessionID != sessionID {
		return
	}
	if dialErr != nil {
		c.state = statusStateDegraded
		c.lastError = dialErr.Error()
		return
	}
	c.state = statusStateConnected
	c.lastError = ""
	if routeID != "" {
		endpoint, found := endpointByID(c.active.EgressEndpoints, routeID)
		if c.carrierHealth != nil {
			c.carrierHealth.RecordConstructed(routeID)
		}
		if c.policy.FailureTracker != nil {
			c.policy.FailureTracker.RecordSuccess(routeID)
		}
		if found && c.egressRecovery != nil {
			c.egressRecovery.RecordDialSuccess(policy.EgressEndpointKey(endpoint))
		}
		if c.active.AutomaticEgressEndpointID == "" {
			c.active.AutomaticEgressEndpointID = routeID
		} else if current, ok := endpointByID(c.active.EgressEndpoints, c.active.AutomaticEgressEndpointID); ok && c.egressRecovery != nil && !c.egressRecovery.CanDial(policy.EgressEndpointKey(current)) {
			c.active.AutomaticEgressEndpointID = routeID
		}
	}
}

// dialActiveSessionEgress dials from an immutable active-session snapshot.
// Disconnect uses it after clearing c.active so post-session control cannot
// accidentally fall back to the host's direct network path.
func (c *ControlPlane) dialActiveSessionEgress(ctx context.Context, active *activeSession, tunnel session.CarrierTunnel, recovery *policy.EgressRecoveryTracker, targetAddr string) (net.Conn, string, error) {
	return c.dialActiveSessionEgressWithRegistry(ctx, active, tunnel, recovery, nil, targetAddr)
}

func (c *ControlPlane) dialActiveSessionEgressWithRegistry(ctx context.Context, active *activeSession, tunnel session.CarrierTunnel, recovery *policy.EgressRecoveryTracker, routeStreams *routeStreamRegistry, targetAddr string) (net.Conn, string, error) {
	log.Printf("[control] DialEgress: active=%v tunnel=%v", active != nil, tunnel != nil)
	if active == nil {
		return nil, "", errors.New("no active carrier session")
	}
	if tunnel == nil {
		return nil, "carrier-session", errors.New("carrier egress tunnel is not implemented for the active session")
	}
	selectedEndpointID := active.SelectedEgressEndpointID

	// Reject endpoints before scoring. A session answer is remote input: it must
	// not turn the discovery/control endpoint that delivered the answer into a
	// SOCKS route when the advertised egress profile is unavailable locally.
	eligibleEndpoints := make([]carriers.Endpoint, 0, len(active.EgressEndpoints))
	var rejectedEndpoints []error
	var quarantinedCarrierFailures []error
	for _, ep := range active.EgressEndpoints {
		if selectedEndpointID != "" && ep.ID != selectedEndpointID {
			continue
		}
		if selectedEndpointID == "" && active.AutomaticEgressEndpointID != ep.ID && recovery != nil && !recovery.CanDial(policy.EgressEndpointKey(ep)) {
			continue
		}
		if routeStreams != nil {
			routeKey := sessionRouteKey(active.SessionID, ep)
			if !routeStreams.canDial(routeKey) {
				if cause := routeStreams.quarantineCause(routeKey); session.IsCarrierFailure(cause) {
					quarantinedCarrierFailures = append(quarantinedCarrierFailures, cause)
				}
				continue
			}
		}
		if ep.Carrier == adminrelay.CarrierID || ep.ID == adminrelay.CarrierID {
			rejectedEndpoints = append(rejectedEndpoints, fmt.Errorf("%s/%s: post-session control relay cannot be used as its own egress", ep.Carrier, ep.ID))
			continue
		}
		if sameSessionEndpoint(ep, active.ControlEndpoint) && !c.combinedControlEndpointSupportsEgress(ep) {
			rejectedEndpoints = append(rejectedEndpoints, fmt.Errorf("%s/%s: control endpoint cannot be used for egress", ep.Carrier, ep.ID))
			continue
		}
		if !tunnel.SupportsEndpoint(ep) {
			rejectedEndpoints = append(rejectedEndpoints, fmt.Errorf("%s/%s: unsupported by loaded egress tunnel", ep.Carrier, ep.ID))
			continue
		}
		eligibleEndpoints = append(eligibleEndpoints, ep)
	}
	if selectedEndpointID != "" && len(eligibleEndpoints) == 0 {
		if len(rejectedEndpoints) > 0 {
			return nil, "carrier-session", fmt.Errorf("selected egress endpoint %q is not locally usable: %w", selectedEndpointID, errors.Join(rejectedEndpoints...))
		}
		return nil, "carrier-session", fmt.Errorf("selected egress endpoint %q is not in the active session", selectedEndpointID)
	}
	if len(eligibleEndpoints) == 0 && len(rejectedEndpoints) > 0 {
		return nil, "carrier-session", fmt.Errorf("no locally supported egress endpoint in active session: %w", errors.Join(rejectedEndpoints...))
	}
	if selectedEndpointID == "" && len(eligibleEndpoints) == 0 && len(quarantinedCarrierFailures) > 0 {
		return nil, "carrier-session", session.NewCarrierFailure(fmt.Errorf("all automatic egress routes are quarantined after carrier failure: %w", errors.Join(quarantinedCarrierFailures...)))
	}

	// Pre-compute scores (descriptor lookup done once per endpoint, not per comparison).
	type scoredEP struct {
		ep    carriers.Endpoint
		score float64
	}
	scored := make([]scoredEP, len(eligibleEndpoints))
	for i, ep := range eligibleEndpoints {
		score := -1.0
		if c.policy.Scorer != nil {
			desc, err := egressDescriptor(ep)
			if err != nil {
				log.Printf("[control] DialEgress: unknown carrier %q, scoring last", ep.Carrier)
			} else {
				score = c.policy.Scorer.Score(desc, fabric.TrafficEgress)
			}
		}
		scored[i] = scoredEP{ep: ep, score: score}
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].score > scored[j].score })

	// Build endpoint list preferring diverse platforms within each batch.
	remaining := make([]carriers.Endpoint, len(scored))
	for i, s := range scored {
		remaining[i] = s.ep
	}

	var allErrors []error
	// A successful fallback becomes the automatic route for new connections.
	// Try it alone first so a degraded higher-ranked route is not needlessly
	// retried in every diverse parallel batch. A manual selection remains an
	// exclusive diagnostic override and bypasses this adaptive preference.
	if selectedEndpointID == "" && active.AutomaticEgressEndpointID != "" {
		for index, endpoint := range remaining {
			if endpoint.ID != active.AutomaticEgressEndpointID {
				continue
			}
			remaining = append(remaining[:index:index], remaining[index+1:]...)
			conn, route, err := dialBatchWithRegistry(ctx, tunnel, []carriers.Endpoint{endpoint}, targetAddr, dialEgressTimeout, active.SessionID, routeStreams, c.recordQuarantinedEgressFailure, c.recordQuarantinedEgressStreamFailure)
			if err == nil {
				return conn, route, nil
			}
			c.recordAutomaticEgressFailure(active.SessionID, endpoint, err)
			allErrors = append(allErrors, err)
			break
		}
	}
	for len(remaining) > 0 {
		batch := pickDiverseBatch(remaining, dialEgressBatchSize)
		// pickDiverseBatch may select a lower-ranked endpoint to gain platform
		// diversity. Remove the endpoints that were actually attempted instead
		// of slicing by count; otherwise a same-platform fallback can be dropped
		// without ever being dialed.
		remaining = removeEgressBatch(remaining, batch)

		conn, route, err := dialBatchWithRegistry(ctx, tunnel, batch, targetAddr, dialEgressTimeout, active.SessionID, routeStreams, c.recordQuarantinedEgressFailure, c.recordQuarantinedEgressStreamFailure)
		if err == nil {
			return conn, route, nil
		}
		allErrors = append(allErrors, err)
	}
	if len(allErrors) > 0 {
		return nil, "carrier-session", fmt.Errorf("all egress endpoints failed: %w", errors.Join(allErrors...))
	}
	log.Printf("[control] DialEgress: no compatible egress endpoint found")
	return nil, "carrier-session", errors.New("no compatible egress endpoint in active session")
}

// egressDescriptor maps a per-profile Xray endpoint back to its standard
// sing-box capabilities for policy scoring. The dynamic ID must be retained
// for sidecar routing, but it is not a separate transport implementation.
func egressDescriptor(endpoint carriers.Endpoint) (carriers.Descriptor, error) {
	carrierID := strings.TrimSpace(endpoint.Carrier)
	descriptor, err := carriers.FindStandardDescriptor(carrierID)
	if err == nil {
		return descriptor, nil
	}
	if strings.HasPrefix(carrierID, xraySingBoxEndpointIDPrefix) || strings.HasPrefix(strings.TrimSpace(endpoint.ID), xraySingBoxEndpointIDPrefix) {
		return carriers.FindStandardDescriptor(carriers.CarrierSingBoxVLESS)
	}
	return carriers.Descriptor{}, err
}

// sameSessionEndpoint compares the stable endpoint identity used by control
// and session answers. The address fallback covers endpoints that did not
// receive an explicit ID from an older peer.
func sameSessionEndpoint(left carriers.Endpoint, right carriers.Endpoint) bool {
	if left.Carrier == "" || right.Carrier == "" || left.Carrier != right.Carrier {
		return false
	}
	if left.ID != "" && right.ID != "" {
		return left.ID == right.ID
	}
	return left.Address != "" && right.Address != "" && left.Address == right.Address
}

// dialBatch dials up to len(batch) endpoints in parallel with a per-dial
// timeout. The first successful connection wins; losing connections are
// closed. Returns a joined error when every dial fails.
func dialBatch(ctx context.Context, tunnel session.CarrierTunnel, batch []carriers.Endpoint, targetAddr string, perDialTimeout time.Duration) (net.Conn, string, error) {
	return dialBatchWithRegistry(ctx, tunnel, batch, targetAddr, perDialTimeout, "", nil, nil, nil)
}

func dialBatchWithRegistry(ctx context.Context, tunnel session.CarrierTunnel, batch []carriers.Endpoint, targetAddr string, perDialTimeout time.Duration, sessionID string, routeStreams *routeStreamRegistry, onRouteFailure func(string, carriers.Endpoint, error), onStreamFailure func(string, carriers.Endpoint, error)) (net.Conn, string, error) {
	type dialResult struct {
		conn  net.Conn
		route string
		err   error
	}

	// Keep results unbuffered so closing done makes every late winner close its
	// connection instead of parking it in a buffer after the caller returned.
	results := make(chan dialResult)
	done := make(chan struct{})
	defer close(done)
	for _, ep := range batch {
		go func(ep carriers.Endpoint) {
			var lease routeDialLease
			if routeStreams != nil {
				var eligible bool
				lease, eligible = routeStreams.beginDial(sessionRouteKey(sessionID, ep))
				if !eligible {
					select {
					case results <- dialResult{err: fmt.Errorf("%s/%s: route quarantined", ep.Carrier, ep.ID)}:
					case <-done:
					}
					return
				}
			}
			if !tunnel.SupportsEndpoint(ep) {
				select {
				case results <- dialResult{err: fmt.Errorf("%s/%s: unsupported", ep.Carrier, ep.ID)}:
				case <-done:
				}
				return
			}
			// Use the parent ctx for DialContext so the returned
			// connection's lifetime is not tied to a batch-level
			// cancellation. The per-batch deadline is enforced by
			// the timer race below.
			conn, err := tunnel.DialContext(ctx, ep, targetAddr)
			if routeStreams != nil && session.IsCarrierFailure(err) {
				routeStreams.quarantine(lease.routeKey, err)
				if onRouteFailure != nil {
					onRouteFailure(sessionID, ep, err)
				}
			}
			if routeStreams != nil && err == nil {
				var registered bool
				conn, registered = routeStreams.register(lease, conn, func(streamErr error) {
					if onStreamFailure != nil {
						onStreamFailure(sessionID, ep, streamErr)
					}
				})
				if !registered {
					err = ErrStaleEgressGeneration
				}
			}
			result := dialResult{conn: conn, route: ep.ID, err: err}
			select {
			case results <- result:
			case <-done:
				if conn != nil {
					_ = conn.Close()
				}
			}
		}(ep)
	}

	// Race: first result wins, or batch-level timeout fires.
	timer := time.NewTimer(perDialTimeout)
	defer timer.Stop()

	var errs []error
	for range batch {
		select {
		case r := <-results:
			if r.err != nil {
				errs = append(errs, r.err)
				continue
			}
			// Return the first usable route immediately. Closing done wakes every
			// losing sender; any connection that completes later is closed there.
			return r.conn, r.route, nil
		case <-timer.C:
			errs = append(errs, fmt.Errorf("dial batch: per-batch timeout (%v)", perDialTimeout))
			return nil, "", errors.Join(errs...)
		case <-ctx.Done():
			errs = append(errs, ctx.Err())
			return nil, "", errors.Join(errs...)
		}
	}
	return nil, "", errors.Join(errs...)
}

func sessionRouteKey(sessionID string, endpoint carriers.Endpoint) string {
	return sessionID + "\x00" + policy.EgressEndpointKey(endpoint)
}

// pickDiverseBatch selects up to n endpoints from the front of remaining,
// preferring endpoints from different carrier platforms.
func pickDiverseBatch(remaining []carriers.Endpoint, n int) []carriers.Endpoint {
	if len(remaining) <= n {
		return remaining
	}
	batch := []carriers.Endpoint{remaining[0]}
	seen := map[string]bool{dialEgressPlatform(remaining[0].Carrier): true}
	rest := remaining[1:]
	for _, ep := range rest {
		if len(batch) >= n {
			break
		}
		platform := dialEgressPlatform(ep.Carrier)
		if !seen[platform] {
			batch = append(batch, ep)
			seen[platform] = true
		}
	}
	// Fill remaining slots from the front (highest score) if diversity is exhausted.
	if len(batch) < n {
		for _, ep := range rest {
			if len(batch) >= n {
				break
			}
			if !containsEndpoint(batch, ep) {
				batch = append(batch, ep)
			}
		}
	}
	return batch
}

// dialEgressPlatform extracts the platform prefix from a carrier ID
// (e.g. "vk" from "vk.docs.1024").
func dialEgressPlatform(carrierID string) string {
	if idx := strings.Index(carrierID, "."); idx > 0 {
		return carrierID[:idx]
	}
	return carrierID
}

// containsEndpoint checks whether the slice already contains the given endpoint.
func containsEndpoint(eps []carriers.Endpoint, ep carriers.Endpoint) bool {
	for _, e := range eps {
		if e.ID == ep.ID {
			return true
		}
	}
	return false
}

// removeEgressBatch retains the candidates that were not included in a dial
// batch. Endpoint IDs are session-stable and therefore identify a candidate
// even when multiple routes share the same carrier implementation.
func removeEgressBatch(remaining []carriers.Endpoint, batch []carriers.Endpoint) []carriers.Endpoint {
	if len(batch) == 0 {
		return remaining
	}
	selected := make(map[string]struct{}, len(batch))
	for _, endpoint := range batch {
		selected[endpoint.ID] = struct{}{}
	}
	kept := make([]carriers.Endpoint, 0, len(remaining)-len(batch))
	for _, endpoint := range remaining {
		if _, ok := selected[endpoint.ID]; !ok {
			kept = append(kept, endpoint)
		}
	}
	return kept
}

func (c *ControlPlane) publishAdvertisement(ctx context.Context) {
	capabilities := []string{"egress", "control"}
	if c.bootstrapSecretCipher != nil {
		capabilities = append(capabilities, BootstrapKeyCapability)
	}
	ad := session.NodeAdvertisement{
		NodeID:       c.cfg.NodeID,
		Role:         session.RoleNode,
		Label:        c.cfg.DisplayLabel(),
		Country:      strings.TrimSpace(c.cfg.Country),
		Region:       strings.TrimSpace(c.cfg.Region),
		Capabilities: capabilities,
		Carriers:     append([]carriers.Endpoint(nil), c.replyEndpoints...),
	}

	for _, ref := range c.bootstrap {
		endpoint := endpointWithBindingIdentity(ref)
		// Check if this is a discovery-only carrier that supports reading and mutating past messages
		desc := ref.Binding.Carrier.Descriptor()
		hasRetrospective := false
		hasMutable := false
		for _, cap := range desc.Capabilities {
			if cap == carriers.CapRetrospective {
				hasRetrospective = true
			}
			if cap == carriers.CapMutable {
				hasMutable = true
			}
		}

		// For discovery-only carriers that can read and delete past messages,
		// use WriteWithResult to track the message ID for later deletion
		if hasRetrospective && hasMutable {
			if resultCarrier, ok := ref.Binding.Carrier.(carriers.CarrierWithWriteResult); ok {
				payload, err := session.EncodePayload(ad)
				if err != nil {
					c.recordCarrierUsage(ref.ID, endpoint.Address, 1, 0, err)
					c.setError(err)
					continue
				}
				result, err := resultCarrier.WriteWithResult(ctx, endpoint, fabric.NewEnvelope(ad.NodeID+":advertise", fabric.TrafficBootstrap, session.PayloadNodeAdvertise, payload))
				if err != nil {
					c.recordCarrierUsage(ref.ID, endpoint.Address, 1, 0, err)
					c.setError(err)
				} else {
					c.recordCarrierUsage(ref.ID, endpoint.Address, 1, 0, nil)
					// Store the message ID for deletion when a client connects
					c.mu.Lock()
					c.publishedMessageIDs[ref.ID] = result.MessageID
					c.mu.Unlock()
				}
				continue // Don't re-advertise for discovery-only carriers with deletion capability
			}
		}

		// Use the standard PublishAdvertisement for other carriers
		if err := c.engine.PublishAdvertisement(ctx, ref.Binding.Carrier, endpoint, ad); err != nil {
			c.recordCarrierUsage(ref.ID, endpoint.Address, 1, 0, err)
			c.setError(err)
		} else {
			c.recordCarrierUsage(ref.ID, endpoint.Address, 1, 0, nil)
		}
	}
}

func (c *ControlPlane) runPollLoop(ctx context.Context) {
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()

	for {
		dbg("poll loop tick")
		c.pollOnce(ctx)

		select {
		case <-ctx.Done():
			dbg("poll loop context done, exiting")
			return
		case <-ticker.C:
		}
	}
}

func (c *ControlPlane) pollOnce(ctx context.Context) {
	for _, ref := range c.control {
		cursor := c.cursor(ref.ID)
		dbg("pollOnce: carrier=%s cursor=%s endpoint=%s", ref.ID, cursor, ref.Binding.Endpoint.Address)
		read, err := ref.Binding.Carrier.Read(ctx, ref.Binding.Endpoint, cursor)
		if err != nil {
			c.setError(err)
			c.recordCarrierUsage(ref.ID, ref.Binding.Endpoint.Address, 0, int64(len(read.Envelopes)), err)
			dbg("pollOnce: carrier=%s read error: %v", ref.ID, err)
			continue
		}
		dbg("pollOnce: carrier=%s got %d envelopes, new cursor=%s", ref.ID, len(read.Envelopes), read.Cursor)
		c.setCursor(ref.ID, read.Cursor)
		c.recordCarrierUsage(ref.ID, ref.Binding.Endpoint.Address, 0, int64(len(read.Envelopes)), nil)

		for _, envelope := range read.Envelopes {
			if !c.validateCarrierControlEnvelope(envelope) {
				continue
			}
			dbg("pollOnce: envelope type=%s session=%s from=%s", envelope.PayloadType, envelope.SessionID, envelope.Source)
			switch envelope.PayloadType {
			case "node.advertise":
				ad, err := session.DecodePayload[session.NodeAdvertisement](envelope.Payload)
				if err != nil {
					c.setError(err)
					dbg("pollOnce: node.advertise decode error: %v", err)
					continue
				}
				dbg("pollOnce: node.advertise from node_id=%s carriers=%d", ad.NodeID, len(ad.Carriers))
				c.storeAdvertisement(ad)
			case "node.withdraw":
				wd, err := session.DecodePayload[session.NodeWithdrawal](envelope.Payload)
				if err != nil {
					c.setError(err)
					continue
				}
				dbg("pollOnce: node.withdraw node_id=%s", wd.NodeID)
				c.storeWithdrawal(wd.NodeID)
			case "node.heartbeat":
				hb, err := session.DecodePayload[session.NodeHeartbeat](envelope.Payload)
				if err != nil {
					c.setError(err)
					continue
				}
				dbg("pollOnce: node.heartbeat node_id=%s", hb.NodeID)
				c.storeHeartbeat(hb)
			case session.PayloadSessionOffer:
				if c.cfg.Role != config.RoleNode {
					dbg("pollOnce: session.offer ignored - not a node")
					continue
				}
				offer, err := session.DecodePayload[session.Offer](envelope.Payload)
				if err != nil {
					c.setError(err)
					continue
				}
				dbg("pollOnce: session.offer session=%s client=%s target_node=%s expires=%s from=%s", offer.SessionID, offer.ClientID, offer.TargetNodeID, offer.ExpiresAt.Format(time.RFC3339), envelope.Source)
				c.handleOffer(ctx, offer, ref)
			case session.PayloadSessionAnswer, session.PayloadSessionAnswerCompressed, session.PayloadSessionAnswerChunk, session.PayloadSessionAnswerGzipChunk:
				answer, handled, err := c.engine.DecodeAnswerEnvelope(envelope)
				if err != nil {
					c.setError(err)
					continue
				}
				if !handled {
					continue
				}
				dbg("pollOnce: session.answer session=%s node=%s egress=%d", answer.SessionID, answer.NodeID, len(answer.EgressEndpoints))
				c.handleAnswer(answer)
			case session.PayloadSessionOfferAck:
				ack, err := session.DecodePayload[session.OfferAck](envelope.Payload)
				if err != nil {
					c.setError(err)
					continue
				}
				c.handleOfferAck(ack)
			case session.PayloadSessionRelease:
				if c.cfg.Role != config.RoleNode {
					continue
				}
				release, err := session.DecodePayload[session.Release](envelope.Payload)
				if err != nil {
					c.setError(err)
					continue
				}
				c.handleRelease(ctx, release)
			case session.PayloadSessionError:
				sessErr, err := session.DecodePayload[session.SessionError](envelope.Payload)
				if err != nil {
					c.setError(err)
					continue
				}
				c.handleSessionError(sessErr)
			}
		}
	}
	// Stale node detection: mark nodes as withdrawn if not seen recently.
	c.expireStaleNodes()
}

func (c *ControlPlane) handleOffer(ctx context.Context, offer session.Offer, ref carrierRef) {
	dbg("handleOffer: session=%s client=%s target_node=%s expires=%s", offer.SessionID, offer.ClientID, offer.TargetNodeID, offer.ExpiresAt.Format(time.RFC3339))
	if offer.ExpiresAt.IsZero() || time.Now().UTC().After(offer.ExpiresAt) {
		log.Printf("session offer ignored session=%s reason=expired expires_at=%s", offer.SessionID, offer.ExpiresAt.Format(time.RFC3339Nano))
		return
	}

	// Filter by TargetNodeID — if set, only the targeted node should respond.
	if offer.TargetNodeID != "" && offer.TargetNodeID != c.cfg.NodeID {
		log.Printf("session offer ignored session=%s reason=wrong_target target=%s self=%s", offer.SessionID, offer.TargetNodeID, c.cfg.NodeID)
		return
	}
	if c.bootstrapCipher != nil && len(offer.SessionKey) == 0 {
		log.Printf("session offer ignored session=%s reason=missing_session_key", offer.SessionID)
		return
	}
	if err := validateProductCompatibility(c.localProductVersion(), offer.ProductVersion); err != nil {
		log.Printf("session offer rejected session=%s reason=incompatible_product_version remote=%q err=%v", offer.SessionID, offer.ProductVersion, err)
		reply, binding, selectErr := c.selectReplyEndpoint(offer.ReplyEndpoints)
		if selectErr != nil {
			reply = ref.Binding.Endpoint
			binding = ref.Binding
		}
		sessErr := session.SessionError{
			SessionID: offer.SessionID,
			SenderID:  c.cfg.NodeID,
			Error:     err.Error(),
			Code:      incompatibleProductVersionCode,
		}
		if sendErr := c.engine.SendSessionError(ctx, binding.Carrier, reply, sessErr); sendErr != nil {
			log.Printf("handleOffer: failed to send product-version error session=%s err=%v", offer.SessionID, sendErr)
		}
		return
	}

	// Send ACK immediately before processing.
	c.mu.Lock()
	busy := c.nodeBusy
	c.mu.Unlock()

	if busy {
		log.Printf("session offer busy session=%s client=%s", offer.SessionID, offer.ClientID)
		// Node is busy — send busy ACK instead of silently dropping.
		ack := session.OfferAck{
			SessionID:  offer.SessionID,
			Status:     "busy",
			RetryAfter: 30 * time.Second,
		}
		if err := c.engine.SendOfferAck(ctx, ref.Binding.Carrier, ref.Binding.Endpoint, ack); err != nil {
			c.recordCarrierUsage(ref.ID, ref.Binding.Endpoint.Address, 1, 0, err)
			c.setError(fmt.Errorf("send busy ack: %w", err))
		} else {
			c.recordCarrierUsage(ref.ID, ref.Binding.Endpoint.Address, 1, 0, nil)
		}
		return
	}
	log.Printf("session offer received session=%s client=%s", offer.SessionID, offer.ClientID)
	dbg("handleOffer: sending received ACK for session=%s", offer.SessionID)

	// Send "received" ACK.
	receivedAck := session.OfferAck{
		SessionID: offer.SessionID,
		Status:    "received",
	}
	if err := c.engine.SendOfferAck(ctx, ref.Binding.Carrier, ref.Binding.Endpoint, receivedAck); err != nil {
		c.recordCarrierUsage(ref.ID, ref.Binding.Endpoint.Address, 1, 0, err)
		c.setError(fmt.Errorf("send received ack: %w", err))
	} else {
		c.recordCarrierUsage(ref.ID, ref.Binding.Endpoint.Address, 1, 0, nil)
	}

	// Delete advertisement messages for discovery-only carriers that support deletion
	// This implements the "delete after connect" feature to prevent stale ads
	if desc := ref.Binding.Carrier.Descriptor(); supportsTraffic(desc, fabric.TrafficBootstrap) {
		hasRetrospective := false
		hasMutable := false
		for _, cap := range desc.Capabilities {
			if cap == carriers.CapRetrospective {
				hasRetrospective = true
			}
			if cap == carriers.CapMutable {
				hasMutable = true
			}
		}
		if hasRetrospective && hasMutable {
			c.mu.Lock()
			if messageID, exists := c.publishedMessageIDs[ref.ID]; exists {
				c.mu.Unlock()
				if deleteCarrier, ok := ref.Binding.Carrier.(carriers.CarrierWithWriteResult); ok {
					if err := deleteCarrier.DeleteMessage(ctx, ref.Binding.Endpoint, strconv.Itoa(messageID)); err != nil {
						log.Printf("handleOffer: failed to delete advertisement message carrier=%s message_id=%d err=%v", ref.ID, messageID, err)
					} else {
						log.Printf("handleOffer: deleted advertisement message carrier=%s message_id=%d", ref.ID, messageID)
					}
				} else {
					log.Printf("handleOffer: carrier does not support deletion carrier=%s", ref.ID)
				}
			} else {
				c.mu.Unlock()
			}
		}
	}

	c.mu.Lock()
	c.nodeBusy = true
	c.nodeSessionID = offer.SessionID
	c.nodeSessionClientID = offer.ClientID
	c.mu.Unlock()
	dbg("handleOffer: nodeBusy=true")

	reply, binding, err := c.selectReplyEndpoint(offer.ReplyEndpoints)
	if err != nil {
		c.setError(err)
		// Send error to client instead of silently failing
		sessErr := session.SessionError{
			SessionID: offer.SessionID,
			SenderID:  c.cfg.NodeID,
			Error:     fmt.Sprintf("failed to select reply endpoint: %v", err),
			Code:      "no_reply_path",
		}
		if sendErr := c.engine.SendSessionError(ctx, ref.Binding.Carrier, ref.Binding.Endpoint, sessErr); sendErr != nil {
			log.Printf("handleOffer: failed to send error notification session=%s err=%v", offer.SessionID, sendErr)
		}
		c.releaseNodeSession(ctx, nil)
		return
	}
	dbg("handleOffer: selected reply carrier=%s endpoint=%s", binding.Carrier.Descriptor().ID, reply.Address)

	c.publishWithdrawal(ctx)
	c.setAdvertised(false)
	answerExpiresAt := time.Now().UTC().Add(2 * time.Minute)
	sessionKey, encryptedDelivery, keyErr := c.offerSessionKey(offer)
	if keyErr != nil {
		log.Printf("handleOffer: invalid encrypted session key session=%s err=%v", offer.SessionID, keyErr)
		sessErr := session.SessionError{
			SessionID: offer.SessionID,
			SenderID:  c.cfg.NodeID,
			Error:     keyErr.Error(),
			Code:      "invalid_session_key",
		}
		if sendErr := c.engine.SendSessionError(ctx, binding.Carrier, reply, sessErr); sendErr != nil {
			log.Printf("handleOffer: failed to send invalid-session-key error session=%s err=%v", offer.SessionID, sendErr)
		}
		c.releaseNodeSession(ctx, nil)
		return
	}
	issuedSSH, issueSSHErr := c.issueSessionSSHEgress(ctx, offer.SessionID, answerExpiresAt, encryptedDelivery)
	if issueSSHErr != nil {
		log.Printf("session SSH egress unavailable session=%s err=%v", offer.SessionID, issueSSHErr)
		c.setError(issueSSHErr)
	}

	// Role reversal: if the client created the egress room locally, the node
	// joins as guest instead of creating its own room. Falls back to legacy
	// node-creates-room flow if guest join fails.
	if offer.ClientRoomEndpoint != "" {
		joined, joinErr := c.joinClientRoom(ctx, offer.ClientRoomEndpoint)
		if joinErr != nil {
			log.Printf("handleOffer: joinClientRoom failed session=%s endpoint=%s err=%v — falling back to legacy", offer.SessionID, offer.ClientRoomEndpoint, joinErr)
		}
		if joined {
			log.Printf("handleOffer: node joined client room session=%s endpoint=%s", offer.SessionID, offer.ClientRoomEndpoint)
			// Build additional egress endpoints (singbox.VLESS, VK, OK etc).
			// Skip VTA carriers — they already joined the client room.
			extraEgress := make([]carriers.Endpoint, 0)
			for _, ref := range c.egress {
				if ft := c.policy.FailureTracker; ft != nil && ft.IsAutoDisabled(ref.Descriptor.ID) {
					continue
				}
				if resolveVideoTunnelAdapter(ref.Binding) != nil {
					continue
				}
				ep := endpointWithBindingIdentity(ref)
				if c.isControlEndpoint(ep) && !combinedControlRefSupportsEgress(ref) {
					continue
				}
				if ref.Descriptor.ID == carriers.CarrierSingBoxVLESS {
					ep.Carrier = ref.ID
				}
				sessionBinding := ref.Binding
				sessionBinding.Endpoint = ep
				c.setSessionTunnelBinding(ep, sessionBinding)
				extraEgress = append(extraEgress, ep)
			}
			if issuedSSH != nil {
				extraEgress = append(extraEgress, issuedSSH.Endpoint)
			}
			answer := session.Answer{
				SessionID:        offer.SessionID,
				NodeID:           c.cfg.NodeID,
				ProductVersion:   c.localProductVersion(),
				Label:            c.cfg.DisplayLabel(),
				Country:          strings.TrimSpace(c.cfg.Country),
				Region:           strings.TrimSpace(c.cfg.Region),
				Endpoints:        append([]carriers.Endpoint(nil), c.replyEndpoints...),
				EgressEndpoints:  extraEgress,
				JoinedClientRoom: true,
				ExpiresAt:        answerExpiresAt,
			}
			if encryptedDelivery {
				profiles := c.sessionEgressProfiles(extraEgress)
				if issuedSSH != nil {
					profiles = append(profiles, issuedSSH.Profile)
				}
				if len(profiles) > 0 {
					sealed, sealErr := session.SealEgressProfiles(offer.SessionID, c.cfg.NodeID, answer.ExpiresAt, profiles, sessionKey)
					if sealErr != nil {
						c.setError(fmt.Errorf("seal egress profiles: %w", sealErr))
						c.releaseNodeSession(ctx, nil)
						return
					}
					answer.EgressProfilesCiphertext = sealed
				}
			}
			c.activatePacketSession(offer.SessionID, offer.ClientID, answer.ExpiresAt, sessionKey, encryptedDelivery)
			c.armNodeSessionExpiry(ctx, answer.ExpiresAt, nil)
			reply, sendErr := c.sendAnswerWithFailover(ctx, offer.ReplyEndpoints, answer)
			if sendErr != nil {
				log.Printf("session answer send failed session=%s err=%v", offer.SessionID, sendErr)
				c.setError(sendErr)
				c.releaseNodeSession(ctx, nil)
				return
			}
			log.Printf("session answer sent session=%s reply=%s joined_client_room=true", offer.SessionID, reply.ID)

			return
		}
	}

	egressEndpoints := c.buildEgressEndpoints(ctx)
	if issuedSSH != nil {
		egressEndpoints = append(egressEndpoints, issuedSSH.Endpoint)
	}
	if len(egressEndpoints) == 0 {
		log.Printf("session offer no egress endpoints session=%s", offer.SessionID)
		// Send error to client
		sessErr := session.SessionError{
			SessionID: offer.SessionID,
			SenderID:  c.cfg.NodeID,
			Error:     "no egress endpoints available",
			Code:      "no_egress",
		}
		if sendErr := c.engine.SendSessionError(ctx, binding.Carrier, reply, sessErr); sendErr != nil {
			log.Printf("handleOffer: failed to send error notification session=%s err=%v", offer.SessionID, sendErr)
		}
		c.releaseNodeSession(ctx, nil)
		return
	}
	log.Printf("session offer egress endpoints session=%s count=%d", offer.SessionID, len(egressEndpoints))
	for i, ep := range egressEndpoints {
		dbg("handleOffer: egress[%d] carrier=%s endpoint=%s", i, ep.ID, ep.Address)
	}

	c.mu.Lock()
	c.nodeSessionEndpoints = append([]carriers.Endpoint(nil), egressEndpoints...)
	c.mu.Unlock()

	answer := session.Answer{
		SessionID:       offer.SessionID,
		NodeID:          c.cfg.NodeID,
		ProductVersion:  c.localProductVersion(),
		Label:           c.cfg.DisplayLabel(),
		Country:         strings.TrimSpace(c.cfg.Country),
		Region:          strings.TrimSpace(c.cfg.Region),
		Endpoints:       append([]carriers.Endpoint(nil), c.replyEndpoints...),
		EgressEndpoints: egressEndpoints,
		ExpiresAt:       answerExpiresAt,
	}
	if encryptedDelivery {
		profiles := c.sessionEgressProfiles(egressEndpoints)
		if issuedSSH != nil {
			profiles = append(profiles, issuedSSH.Profile)
		}
		if len(profiles) > 0 {
			sealed, err := session.SealEgressProfiles(offer.SessionID, c.cfg.NodeID, answer.ExpiresAt, profiles, sessionKey)
			if err != nil {
				c.setError(fmt.Errorf("seal egress profiles: %w", err))
				c.releaseNodeSession(ctx, egressEndpoints)
				return
			}
			answer.EgressProfilesCiphertext = sealed
		}
	}
	log.Printf("session answer profile envelope session=%s endpoints=%d ciphertext_bytes=%d", offer.SessionID, len(answer.EgressEndpoints), len(answer.EgressProfilesCiphertext))
	c.activatePacketSession(offer.SessionID, offer.ClientID, answer.ExpiresAt, sessionKey, encryptedDelivery)
	c.armNodeSessionExpiry(ctx, answer.ExpiresAt, egressEndpoints)
	reply, sendErr := c.sendAnswerWithFailover(ctx, offer.ReplyEndpoints, answer)
	if sendErr != nil {
		log.Printf("session answer send failed session=%s err=%v", offer.SessionID, sendErr)
		// Send error to client
		sessErr := session.SessionError{
			SessionID: offer.SessionID,
			SenderID:  c.cfg.NodeID,
			Error:     fmt.Sprintf("failed to send answer: %v", sendErr),
			Code:      "answer_send_failed",
		}
		if sendErr := c.engine.SendSessionError(ctx, binding.Carrier, reply, sessErr); sendErr != nil {
			log.Printf("handleOffer: failed to send error notification session=%s err=%v", offer.SessionID, sendErr)
		}
		c.setError(sendErr)
		c.releaseNodeSession(ctx, egressEndpoints)
		return
	}
	log.Printf("session answer sent session=%s reply=%s egress=%d", offer.SessionID, reply.ID, len(egressEndpoints))

}

func (c *ControlPlane) validateCarrierControlEnvelope(envelope fabric.Envelope) bool {
	if err := envelope.Validate(); err != nil {
		c.setError(fmt.Errorf("carrier control envelope %q rejected: %w", envelope.ID, err))
		return false
	}
	return true
}

func (c *ControlPlane) offerSessionKey(offer session.Offer) ([32]byte, bool, error) {
	if len(offer.SessionKey) == 0 {
		return [32]byte{}, false, nil
	}
	ciphers := make([]*fabric.EnvelopeCipher, 0, 2)
	v2Offer := offer.Metadata[bootstrapKeyMetadata] == bootstrapKeyV2
	if c.bootstrapSecretCipher != nil {
		ciphers = append(ciphers, c.bootstrapSecretCipher)
	}
	if !v2Offer && c.legacyBootstrapCipher != nil && c.legacyBootstrapCipher != c.bootstrapSecretCipher {
		ciphers = append(ciphers, c.legacyBootstrapCipher)
	}
	if len(ciphers) == 0 {
		// A legacy node without any bootstrap credentials accepts the old raw
		// session-key path for mixed-version compatibility.
		return [32]byte{}, false, nil
	}
	var lastErr error
	for _, bootstrapCipher := range ciphers {
		rawKey, err := session.DecryptSessionKey(bootstrapCipher, offer.SessionKey)
		if err != nil {
			lastErr = err
			continue
		}
		if len(rawKey) != 32 {
			lastErr = fmt.Errorf("decrypted session key has length %d, want 32", len(rawKey))
			continue
		}
		var key [32]byte
		copy(key[:], rawKey)
		return key, true, nil
	}
	return [32]byte{}, false, fmt.Errorf("decrypt session key: %w", lastErr)
}

func (c *ControlPlane) sessionEgressProfiles(endpoints []carriers.Endpoint) []session.EgressProfile {
	profiles := make([]session.EgressProfile, 0)
	for _, endpoint := range endpoints {
		binding, key := c.findBindingByCarrier(endpoint.Carrier)
		if key == "" {
			// Role-reversal answers preserve the node binding ID as Carrier
			// (xray-de-*, xray-us-*). Resolve it from the egress table even
			// when the public endpoint carrier differs from the local key.
			for _, ref := range c.egress {
				if ref.ID == endpoint.Carrier || ref.Binding.Endpoint.ID == endpoint.ID {
					binding = ref.Binding
					key = ref.ID
					break
				}
			}
		}
		if key == "" {
			continue
		}
		switch carrier := binding.Carrier.(type) {
		case *carriers.SingBoxVLESSCarrier:
			if strings.TrimSpace(carrier.Config().URI) == "" {
				continue
			}
			profiles = append(profiles, session.EgressProfile{EndpointID: endpoint.ID, Carrier: endpoint.Carrier, URI: carrier.Config().URI})
		case *carriers.SSHCarrier:
			sshProfile, err := portableSSHEgressProfile(carrier)
			if err != nil {
				continue
			}
			profiles = append(profiles, session.EgressProfile{
				Version: session.EgressProfileVersion, EndpointID: endpoint.ID, Carrier: endpoint.Carrier,
				SSH: sshProfile,
			})
		}
	}
	return profiles
}

// joinClientRoom makes the node join a client-created egress room as a guest.
// The endpoint is in the format "<carrier>://<address>" (e.g.
// "wbstream://room-xyz"). The node uses its VTA adapter's StartEgressAddr,
// which reuses the existing RegisterGuest/JoinRoom guest-join logic.
// Returns (true, nil) on success.
func (c *ControlPlane) joinClientRoom(ctx context.Context, clientRoomEndpoint string) (bool, error) {
	parts := strings.SplitN(clientRoomEndpoint, "://", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false, fmt.Errorf("invalid client room endpoint: %s", clientRoomEndpoint)
	}
	carrierID := parts[0]
	roomAddr := parts[1]

	for _, ref := range c.egress {
		if !strings.EqualFold(ref.ID, carrierID) && !strings.EqualFold(ref.Descriptor.ID, carrierID) {
			continue
		}
		vta := resolveVideoTunnelAdapter(ref.Binding)
		if vta == nil {
			continue
		}
		dbg("joinClientRoom: joining as guest carrier=%s addr=%s", ref.ID, roomAddr)
		var joinErr error
		c.runRoleReversalTunnelTransition(func() {
			joinErr = vta.StartEgressAddr(ctx, roomAddr)
		})
		if joinErr != nil {
			return false, fmt.Errorf("node guest join carrier=%s: %w", ref.ID, joinErr)
		}
		dbg("joinClientRoom: joined OK carrier=%s", ref.ID)
		return true, nil
	}
	return false, fmt.Errorf("no video tunnel adapter found for carrier %s", carrierID)
}

// buildEgressEndpoints returns the egress endpoint list for a session answer.
// For WBStream it creates a fresh guest room; other carriers use static config.
func (c *ControlPlane) buildEgressEndpoints(ctx context.Context) []carriers.Endpoint {
	endpoints := make([]carriers.Endpoint, 0, len(c.egress))
	for _, ref := range c.egress {
		if ft := c.policy.FailureTracker; ft != nil && ft.IsAutoDisabled(ref.Descriptor.ID) {
			dbg("buildEgressEndpoints: skip auto-disabled carrier id=%s", ref.ID)
			continue
		}
		ep := endpointWithBindingIdentity(ref)
		if c.isControlEndpoint(ep) && !combinedControlRefSupportsEgress(ref) {
			continue
		}
		if sshCarrier, ok := ref.Binding.Carrier.(*carriers.SSHCarrier); ok {
			if _, err := portableSSHEgressProfile(sshCarrier); err != nil {
				log.Printf("[control] buildEgressEndpoints: skip non-portable SSH carrier=%s err=%v", ref.ID, err)
				continue
			}
		}
		vta := resolveVideoTunnelAdapter(ref.Binding)
		dbg("[control] buildEgressEndpoints: carrier=%s vta=%v", ref.ID, vta != nil)
		if vta != nil {
			roomAddr, err := vta.CreateAndStartEgress(ctx)
			if err != nil {
				log.Printf("[control] buildEgressEndpoints: CreateAndStartEgress FAILED carrier=%s err=%v", ref.ID, err)
				c.setError(fmt.Errorf("video tunnel egress: %w", err))
				continue
			}
			dbg("[control] buildEgressEndpoints: CreateAndStartEgress OK carrier=%s roomAddr=%s", ref.ID, roomAddr)
			ep = carriers.Endpoint{
				ID:      fmt.Sprintf("%s:%d", ref.ID, time.Now().UTC().UnixNano()),
				Carrier: ref.ID,
				Address: roomAddr,
			}
			sessionBinding := ref.Binding
			sessionBinding.Endpoint = ep
			c.setSessionTunnelBinding(ep, sessionBinding)
		}
		endpoints = append(endpoints, ep)
	}
	return endpoints
}

// isControlEndpoint prevents a carrier address assigned to discovery or
// reply traffic from being re-advertised as a SOCKS tunnel endpoint.
func (c *ControlPlane) isControlEndpoint(endpoint carriers.Endpoint) bool {
	for _, control := range c.replyEndpoints {
		if sameSessionEndpoint(endpoint, control) {
			return true
		}
	}
	return false
}

// combinedControlRefSupportsEgress distinguishes a genuine combined
// control+stream transport from a control-only mailbox. The descriptor makes
// the route policy-eligible, while StreamDialer proves the runtime can open a
// real byte stream instead of merely advertising capabilities it cannot use.
func combinedControlRefSupportsEgress(ref carrierRef) bool {
	if ref.Descriptor.ID == carriers.CarrierFileMailbox {
		// File mailbox reaches c.egress only when its explicit AllowEgress
		// configuration adds the bulk capability.
		return true
	}
	if !carriers.HasCapability(ref.Descriptor, carriers.CapStream) || !carriers.HasCapability(ref.Descriptor, carriers.CapDuplex) {
		return false
	}
	_, ok := ref.Binding.Carrier.(carriers.StreamDialer)
	return ok
}

func (c *ControlPlane) combinedControlEndpointSupportsEgress(endpoint carriers.Endpoint) bool {
	binding, bindingKey := c.findBindingByCarrier(endpoint.Carrier)
	if bindingKey == "" || binding.Carrier == nil {
		return false
	}
	return combinedControlRefSupportsEgress(carrierRef{
		ID:         bindingKey,
		Descriptor: binding.Carrier.Descriptor(),
		Binding:    binding,
	})
}

// closeVideoTunnelSessions closes video tunnel adapter sessions for the given
// endpoints. Each adapter (WBStream, Telemost, DION) manages its own teardown.
func (c *ControlPlane) closeVideoTunnelSessions(endpoints []carriers.Endpoint) {
	for _, ep := range endpoints {
		if !c.isVideoTunnelCarrierByID(ep.Carrier) {
			continue
		}
		binding, key := c.findBindingByCarrier(ep.Carrier)
		if key == "" {
			continue
		}
		if vta := resolveVideoTunnelAdapter(binding); vta != nil {
			vta.Close()
		}
	}
}

// publishWithdrawal sends node.withdraw on all bootstrap carriers.
func (c *ControlPlane) publishWithdrawal(ctx context.Context) {
	for _, ref := range c.bootstrap {
		if err := c.engine.PublishWithdrawal(ctx, ref.Binding.Carrier, ref.Binding.Endpoint, c.cfg.NodeID); err != nil {
			c.recordCarrierUsage(ref.ID, ref.Binding.Endpoint.Address, 1, 0, err)
			c.setError(err)
		} else {
			c.recordCarrierUsage(ref.ID, ref.Binding.Endpoint.Address, 1, 0, nil)
		}
	}
}

func (c *ControlPlane) setSessionTunnelBinding(endpoint carriers.Endpoint, binding policy.CarrierBinding) {
	if binder, ok := c.tunnel.(interface {
		SetSessionBinding(carriers.Endpoint, policy.CarrierBinding)
	}); ok {
		binder.SetSessionBinding(endpoint, binding)
	}
}

func (c *ControlPlane) clearSessionTunnelBindings(endpoints []carriers.Endpoint) {
	if binder, ok := c.tunnel.(interface {
		ClearSessionBinding(carriers.Endpoint)
	}); ok {
		for _, endpoint := range endpoints {
			binder.ClearSessionBinding(endpoint)
		}
	}
}

func (c *ControlPlane) armNodeSessionExpiry(ctx context.Context, expiresAt time.Time, endpoints []carriers.Endpoint) {
	c.mu.Lock()
	if c.sessionTimer != nil {
		c.sessionTimer.Stop()
	}
	duration := time.Until(expiresAt)
	if duration <= 0 {
		duration = time.Second
	}
	c.sessionTimer = time.AfterFunc(duration, func() {
		c.releaseNodeSession(ctx, endpoints)
	})
	c.mu.Unlock()
}

// idleReleaseGrace is how long the node keeps a session alive after its last
// active tunnel closes. A SOCKS client opens one tunnel per TCP connection, so
// brief idle gaps between connections are normal and must not tear down the
// WBStream room/session. The session is otherwise bounded by its expiry timer.
const idleReleaseGrace = 60 * time.Second

func (c *ControlPlane) onTunnelIdle(ctx context.Context) {
	if c.cfg.Role != config.RoleNode {
		return
	}
	c.mu.Lock()
	if !c.nodeBusy {
		c.mu.Unlock()
		return
	}
	if c.idleTimer != nil {
		c.idleTimer.Stop()
	}
	c.idleTimer = time.AfterFunc(idleReleaseGrace, func() {
		c.releaseNodeSession(ctx, nil)
	})
	c.mu.Unlock()
}

func (c *ControlPlane) onTunnelClosed(ctx context.Context) {
	if c.cfg.Role != config.RoleNode {
		return
	}
	c.mu.RLock()
	transitioning := c.tunnelTransitionDepth > 0
	c.mu.RUnlock()
	if transitioning {
		return
	}
	c.releaseNodeSession(ctx, nil)
}

// runRoleReversalTunnelTransition ignores only the synchronous close of the
// replaced DataTunnel while a node joins a client-created room. Later closes
// still release the session and revoke its scoped SSH lease.
func (c *ControlPlane) runRoleReversalTunnelTransition(fn func()) {
	c.mu.Lock()
	c.tunnelTransitionDepth++
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.tunnelTransitionDepth--
		c.mu.Unlock()
	}()
	fn()
}

func (c *ControlPlane) sendRelease(active *activeSession) {
	if active == nil || active.SessionID == "" || active.ControlBinding.Carrier == nil {
		return
	}
	release := session.Release{
		SessionID: active.SessionID,
		ClientID:  c.cfg.Identity(),
		NodeID:    active.NodeID,
		Reason:    "disconnect",
	}
	primaryCtx, primaryCancel := context.WithTimeout(context.Background(), releaseSendTimeout)
	primaryErr := c.engine.SendRelease(primaryCtx, active.ControlBinding.Carrier, active.ControlEndpoint, release)
	primaryCancel()
	if primaryErr == nil {
		c.recordCarrierUsage(active.ControlEndpoint.Carrier, active.ControlEndpoint.Address, 1, 0, nil)
		log.Printf("session release sent session=%s node=%s", active.SessionID, active.NodeID)
		return
	}
	c.recordCarrierUsage(active.ControlEndpoint.Carrier, active.ControlEndpoint.Address, 1, 0, primaryErr)
	log.Printf("session release primary control failed session=%s node=%s err=%v", active.SessionID, active.NodeID, primaryErr)

	c.mu.RLock()
	relayConfig := c.postSessionControl
	tunnel := c.tunnel
	recovery := c.egressRecovery
	c.mu.RUnlock()
	if relayConfig == nil {
		log.Printf("session release send failed session=%s node=%s err=%v", active.SessionID, active.NodeID, primaryErr)
		return
	}
	cfg := *relayConfig
	cfg.Channels = append([]string(nil), relayConfig.Channels...)
	relay := adminrelay.NewWithDialContext(cfg, log.Printf, func(ctx context.Context, _ string, targetAddr string) (net.Conn, error) {
		conn, route, err := c.dialActiveSessionEgress(ctx, active, tunnel, recovery, targetAddr)
		if err != nil {
			return nil, fmt.Errorf("post-session relay egress dial: %w", err)
		}
		log.Printf("post-session relay egress selected session=%s route=%s", active.SessionID, route)
		return conn, nil
	})
	relayEndpoint := carriers.Endpoint{
		ID:      "control",
		Carrier: adminrelay.CarrierID,
		Address: "control",
		Metadata: map[string]string{
			"channel":   "control",
			"recipient": active.NodeID,
		},
	}
	fallbackCtx, fallbackCancel := context.WithTimeout(context.Background(), releaseSendTimeout)
	fallbackErr := c.engine.SendRelease(fallbackCtx, relay, relayEndpoint, release)
	fallbackCancel()
	if fallbackErr != nil {
		log.Printf("session release fallback failed session=%s node=%s primary_err=%v fallback_err=%v", active.SessionID, active.NodeID, primaryErr, fallbackErr)
		return
	}
	log.Printf("session release sent via post-session HTTP control session=%s node=%s", active.SessionID, active.NodeID)
}

func (c *ControlPlane) handleRelease(ctx context.Context, release session.Release) {
	if c.cfg.Role != config.RoleNode {
		return
	}
	c.mu.RLock()
	active := c.nodeBusy
	sessionID := c.nodeSessionID
	clientID := c.nodeSessionClientID
	c.mu.RUnlock()
	if !active {
		return
	}
	if release.NodeID != "" && release.NodeID != c.cfg.NodeID {
		log.Printf("session release ignored session=%s reason=wrong_node target=%s self=%s", release.SessionID, release.NodeID, c.cfg.NodeID)
		return
	}
	if release.SessionID != sessionID || (release.ClientID != "" && release.ClientID != clientID) {
		log.Printf("session release ignored session=%s reason=not_active active=%s", release.SessionID, sessionID)
		return
	}
	log.Printf("session release received session=%s client=%s", release.SessionID, release.ClientID)
	c.releaseNodeSession(ctx, nil)
}

// cancelIdleRelease aborts a pending idle-release grace timer because a new
// tunnel just opened on the active session.
func (c *ControlPlane) cancelIdleRelease() {
	c.mu.Lock()
	if c.idleTimer != nil {
		c.idleTimer.Stop()
		c.idleTimer = nil
	}
	c.mu.Unlock()
}

func (c *ControlPlane) releaseNodeSession(ctx context.Context, endpoints []carriers.Endpoint) {
	if c.cfg.Role != config.RoleNode {
		return
	}
	c.mu.Lock()
	nodeSessionSSH := c.nodeSessionSSH
	sessionID := c.nodeSessionID
	c.nodeSessionSSH = nil
	if !c.nodeBusy {
		c.mu.Unlock()
		if nodeSessionSSH != nil {
			_ = nodeSessionSSH.Revoke(ctx)
		}
		return
	}
	c.nodeBusy = false
	c.nodeSessionID = ""
	c.nodeSessionClientID = ""
	if c.sessionTimer != nil {
		c.sessionTimer.Stop()
		c.sessionTimer = nil
	}
	if c.idleTimer != nil {
		c.idleTimer.Stop()
		c.idleTimer = nil
	}
	if endpoints == nil {
		endpoints = c.nodeSessionEndpoints
		c.nodeSessionEndpoints = nil
	} else {
		c.nodeSessionEndpoints = nil
	}
	c.mu.Unlock()
	c.closePacketSession(sessionID)
	if nodeSessionSSH != nil {
		if err := nodeSessionSSH.Revoke(ctx); err != nil {
			c.setError(fmt.Errorf("revoke session SSH egress: %w", err))
		}
	}
	c.clearSessionTunnelBindings(endpoints)
	c.closeVideoTunnelSessions(endpoints)
	// Clear cipher on session release.
	if tunnelCipher, ok := c.tunnel.(interface{ ClearCipher() }); ok {
		tunnelCipher.ClearCipher()
	}
	c.publishAdvertisement(ctx)
	c.setAdvertised(true)
}

func (c *ControlPlane) setAdvertised(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.advertised = v
}

func (c *ControlPlane) handleAnswer(answer session.Answer) {
	dbg("handleAnswer CALLED session=%s node=%s endpoints=%d", answer.SessionID, answer.NodeID, len(answer.EgressEndpoints))
	c.mu.RLock()
	ch := c.pending[answer.SessionID]
	expectedNode := c.pendingTargetNodes[answer.SessionID]
	pendingCount := len(c.pending)
	c.mu.RUnlock()
	dbg("handleAnswer: pending=%d looking_for=%s found=%v", pendingCount, answer.SessionID, ch != nil)
	if ch == nil {
		log.Printf("[control] handleAnswer no pending channel session=%s", answer.SessionID)
		return
	}

	// Filter answers from unexpected nodes (e.g., stale example-exit-node-runtime-canary).
	if expectedNode != "" && answer.NodeID != expectedNode {
		log.Printf("session answer ignored session=%s reason=wrong_node got=%s expected=%s", answer.SessionID, answer.NodeID, expectedNode)
		return
	}
	if err := validateProductCompatibility(c.localProductVersion(), answer.ProductVersion); err != nil {
		c.handleSessionError(session.SessionError{
			SessionID: answer.SessionID,
			SenderID:  answer.NodeID,
			Error:     err.Error(),
			Code:      incompatibleProductVersionCode,
		})
		return
	}

	select {
	case ch <- answer:
	default:
	}
}

func validateProductCompatibility(localVersion, remoteVersion string) error {
	localLine, err := config.CompatibilityLine(strings.TrimSpace(localVersion))
	if err != nil {
		return fmt.Errorf("invalid local product version: %w", err)
	}
	remoteLine, err := config.CompatibilityLine(strings.TrimSpace(remoteVersion))
	if err != nil {
		return fmt.Errorf("missing or invalid remote product version: %w", err)
	}
	if localLine != remoteLine {
		return fmt.Errorf("product compatibility mismatch: local=%s remote=%s", localLine, remoteLine)
	}
	return nil
}

func (c *ControlPlane) localProductVersion() string {
	if version := strings.TrimSpace(c.productVersion); version != "" {
		return version
	}
	return config.Version
}

func (c *ControlPlane) handleOfferAck(ack session.OfferAck) {
	c.mu.RLock()
	state := c.pendingAcks[ack.SessionID]
	c.mu.RUnlock()
	if state == nil {
		return
	}
	select {
	case state.ch <- ack:
	default:
	}
}

func (c *ControlPlane) handleSessionError(sessErr session.SessionError) {
	log.Printf("session error received session=%s from=%s code=%s error=%s",
		sessErr.SessionID, sessErr.SenderID, sessErr.Code, sessErr.Error)

	c.mu.RLock()
	ch := c.pendingErrors[sessErr.SessionID]
	c.mu.RUnlock()

	if ch == nil {
		log.Printf("session error ignored: no pending session=%s", sessErr.SessionID)
		return
	}

	// Publish the matching failure before returning from the poll loop. The
	// Connect caller waits on this session-local channel, so an unrelated
	// session.error cannot interrupt another in-flight offer.
	err := sessionErrorConnectError(sessErr)
	c.setError(err)
	c.setState(statusStateDisconnected)
	select {
	case ch <- sessErr:
	default:
	}
}

func sessionErrorConnectError(sessErr session.SessionError) error {
	return fmt.Errorf("session error from %s: %s [%s]", sessErr.SenderID, sessErr.Error, sessErr.Code)
}

func (c *ControlPlane) storeHeartbeat(hb session.NodeHeartbeat) {
	if strings.TrimSpace(hb.NodeID) == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.nodes[hb.NodeID]; ok {
		existing.LastSeenAt = hb.Timestamp
		existing.Withdrawn = false
		c.nodes[hb.NodeID] = existing
	}
}

func (c *ControlPlane) expireStaleNodes() {
	c.mu.Lock()
	defer c.mu.Unlock()
	cutoff := time.Now().UTC().Add(-staleNodeThreshold)
	for id, node := range c.nodes {
		if !node.Withdrawn && node.LastSeenAt.Before(cutoff) {
			node.Withdrawn = true
			c.nodes[id] = node
		}
	}
}

func (c *ControlPlane) reAdvertiseLoop(ctx context.Context) {
	ticker := time.NewTicker(reAdvertiseInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.mu.RLock()
			advertised := c.advertised
			c.mu.RUnlock()
			if advertised {
				c.publishAdvertisement(ctx)
			}
		}
	}
}

func (c *ControlPlane) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, ref := range c.bootstrap {
				if err := c.engine.PublishHeartbeat(ctx, ref.Binding.Carrier, ref.Binding.Endpoint, c.cfg.NodeID); err != nil {
					c.recordCarrierUsage(ref.ID, ref.Binding.Endpoint.Address, 1, 0, err)
					c.setError(err)
				} else {
					c.recordCarrierUsage(ref.ID, ref.Binding.Endpoint.Address, 1, 0, nil)
				}
			}
		}
	}
}

// monitorSessionLiveness is a placeholder for future tunnel health monitoring.
// Video tunnel adapters manage their own connection lifecycle and reconnect
// loops internally (e.g., DionHeadlessJoiner, TelemostHeadlessJoiner).
func (c *ControlPlane) monitorSessionLiveness(ctx context.Context, answer session.Answer) {
	// Poll the active tunnel for liveness every 10 seconds.
	// If the DataTunnel closes (e.g. LiveKit signaling WS dies), mark the
	// session as degraded so the next DialEgress attempt triggers a fresh
	// session rather than failing silently on a dead tunnel.
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	failCount := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.mu.RLock()
			active := c.active
			tunnel := c.tunnel
			c.mu.RUnlock()

			if active == nil || active.SessionID != answer.SessionID {
				// Session changed or cleared — stop monitoring.
				return
			}
			if tunnel == nil {
				continue
			}

			// Check if tunnel implements a liveness probe.
			type livenessChecker interface {
				IsAlive() bool
			}
			if lc, ok := tunnel.(livenessChecker); ok {
				if !lc.IsAlive() {
					failCount++
					log.Printf("[session] liveness check FAILED session=%s fails=%d", active.SessionID, failCount)
					if failCount >= 2 {
						log.Printf("[session] tunnel dead for session=%s, marking error", active.SessionID)
						c.setError(fmt.Errorf("tunnel liveness check failed %d times", failCount))
						c.mu.Lock()
						c.state = statusStateDegraded
						c.mu.Unlock()
						return
					}
				} else {
					failCount = 0
				}
			}
		}
	}
}

// clientSessionTimeoutMonitor tracks session expiry on the client side.
// When the answer expires, the client should clean up the session and
// optionally trigger a reconnect.
func (c *ControlPlane) clientSessionTimeoutMonitor(_ context.Context, answer session.Answer) {
	if answer.ExpiresAt.IsZero() {
		return
	}

	duration := time.Until(answer.ExpiresAt)
	if duration <= 0 {
		return
	}

	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-c.stopCh:
		return
	case <-timer.C:
		// Session expired — check if still active
		c.mu.RLock()
		active := c.active
		c.mu.RUnlock()

		if active != nil && active.SessionID == answer.SessionID {
			log.Printf("[session] client session expired session=%s node=%s", answer.SessionID, answer.NodeID)
			c.mu.Lock()
			if c.active == nil || c.active.SessionID != answer.SessionID {
				c.mu.Unlock()
				return
			}
			active = c.active
			routeStreams := c.egressRouteStreams
			c.egressRouteStreams = nil
			c.active = nil
			c.state = statusStateDisconnected
			c.invalidateSystemVPNProfileLocked("session_expired")
			c.mu.Unlock()
			if routeStreams != nil {
				routeStreams.shutdownSession(active.SessionID)
			}
			c.closePacketSession(active.SessionID)

			// Clean up client room if role reversal was used
			c.mu.RLock()
			roomEndpoint := c.clientRoomEndpoint
			c.mu.RUnlock()
			if roomEndpoint != "" {
				for _, ref := range c.egress {
					if vta := resolveVideoTunnelAdapter(ref.Binding); vta != nil {
						_ = vta.Close()
					}
				}
				c.mu.Lock()
				c.clientRoomEndpoint = ""
				c.clientRoomCarrier = ""
				c.mu.Unlock()
			}

			// Clear cipher only after all packet associations are closed.
			if tunnelCipher, ok := c.tunnel.(interface{ ClearCipher() }); ok {
				tunnelCipher.ClearCipher()
			}

			// Notify node about session end
			c.sendRelease(&activeSession{
				NodeID:          answer.NodeID,
				SessionID:       answer.SessionID,
				ControlEndpoint: active.ControlEndpoint,
				ControlBinding:  active.ControlBinding,
			})
		}
	}
}

func (c *ControlPlane) storeAdvertisement(ad session.NodeAdvertisement) {
	if strings.TrimSpace(ad.NodeID) == "" {
		return
	}
	if c.cfg.Role == config.RoleNode && ad.NodeID == c.cfg.NodeID {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nodes[ad.NodeID] = discoveredNode{
		Advertisement: ad,
		LastSeenAt:    time.Now().UTC(),
		Withdrawn:     false,
	}
}

// InjectAdminAdvertisement stores a node discovered via the admin panel
// HTTP discovery endpoint. Admin-discovered nodes are stored alongside
// carrier-discovered nodes and expire the same way.
func (c *ControlPlane) InjectAdminAdvertisement(ad session.NodeAdvertisement) {
	c.storeAdvertisement(ad)
}

func (c *ControlPlane) storeWithdrawal(nodeID string) {
	if strings.TrimSpace(nodeID) == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.nodes[nodeID]; ok {
		existing.Withdrawn = true
		c.nodes[nodeID] = existing
	}
}

func (c *ControlPlane) selectNode(nodeID string) (session.NodeAdvertisement, error) {
	nodes := c.ListNodes()
	if len(nodes) == 0 {
		return session.NodeAdvertisement{}, errors.New("no discovered nodes")
	}

	if strings.TrimSpace(nodeID) != "" {
		for _, node := range nodes {
			if node.NodeID == nodeID {
				// Allow explicit node selection even if marked withdrawn (stale VK messages).
				return c.lookupNode(node.NodeID)
			}
		}
		return session.NodeAdvertisement{}, fmt.Errorf("node %s not found", nodeID)
	}

	for _, node := range nodes {
		if node.Available {
			return c.lookupNode(node.NodeID)
		}
	}
	return session.NodeAdvertisement{}, errors.New("no available nodes")
}

func (c *ControlPlane) lookupNode(nodeID string) (session.NodeAdvertisement, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	discovered, ok := c.nodes[nodeID]
	if !ok {
		return session.NodeAdvertisement{}, fmt.Errorf("node %s not found", nodeID)
	}
	return discovered.Advertisement, nil
}

func (c *ControlPlane) selectContact(node session.NodeAdvertisement) (carriers.Endpoint, policy.CarrierBinding, error) {
	var fallback carriers.Endpoint
	var fallbackBinding policy.CarrierBinding
	for _, endpoint := range node.Carriers {
		// Try exact key first, then compound-key fallback (e.g. node sends
		// Carrier="vk.messages" but client binding is "vk.messages:discovery").
		binding, bindingKey := c.findBindingByCarrier(endpoint.Carrier)
		if bindingKey == "" {
			continue
		}
		if isVideoTunnelCarrier(binding) {
			if fallback.Carrier == "" {
				fallback, fallbackBinding = endpoint, binding
			}
			continue
		}
		return endpoint, binding, nil
	}
	if fallback.Carrier != "" {
		return fallback, fallbackBinding, nil
	}
	return carriers.Endpoint{}, policy.CarrierBinding{}, fmt.Errorf("node %s has no compatible contact endpoint", node.NodeID)
}

func (c *ControlPlane) selectReplyEndpoint(endpoints []carriers.Endpoint) (carriers.Endpoint, policy.CarrierBinding, error) {
	candidates := c.selectAllReplyEndpoints(endpoints)
	if len(candidates) > 0 {
		return candidates[0].endpoint, candidates[0].binding, nil
	}
	return carriers.Endpoint{}, policy.CarrierBinding{}, errors.New("offer did not provide a compatible reply endpoint")
}

// selectAllReplyEndpoints returns compatible client reply paths in preference
// order. Session answers use every candidate in turn because a provider can
// fail after discovery and offer delivery have already succeeded.
func (c *ControlPlane) selectAllReplyEndpoints(endpoints []carriers.Endpoint) []contactInfo {
	// Prefer non-video-tunnel carriers for control traffic (answer, ack)
	// Video tunnel carriers (WBStream, Telemost, DION) are for egress data, not control.
	var primary []contactInfo
	var fallback []contactInfo
	for _, endpoint := range endpoints {
		// Use compound-key-aware lookup instead of exact match.
		binding, bindingKey := c.findBindingByCarrier(endpoint.Carrier)
		if bindingKey == "" {
			continue
		}
		if ft := c.policy.FailureTracker; ft != nil && ft.IsAutoDisabled(binding.Carrier.Descriptor().ID) {
			dbg("selectReplyEndpoint: skip auto-disabled carrier id=%s", endpoint.Carrier)
			continue
		}
		// Skip video tunnel carriers for control replies
		if isVideoTunnelCarrier(binding) {
			fallback = append(fallback, contactInfo{endpoint: endpoint, binding: binding})
			continue
		}
		// Prefer control-capable carriers (VK messages, OK messages, file.mailbox)
		if supportsTrafficScored(binding.Carrier.Descriptor(), fabric.TrafficControl, c.policy.Scorer) {
			primary = append(primary, contactInfo{endpoint: endpoint, binding: binding})
			continue
		}
		fallback = append(fallback, contactInfo{endpoint: endpoint, binding: binding})
	}
	return append(primary, fallback...)
}

// sendAnswerWithFailover delivers a session answer through the first working
// client reply path. A single carrier outage must not discard a negotiated
// session when the client advertised another usable control endpoint.
func (c *ControlPlane) sendAnswerWithFailover(ctx context.Context, endpoints []carriers.Endpoint, answer session.Answer) (carriers.Endpoint, error) {
	candidates := c.selectAllReplyEndpoints(endpoints)
	if len(candidates) == 0 {
		return carriers.Endpoint{}, errors.New("offer did not provide a compatible reply endpoint")
	}

	var failures []error
	for _, candidate := range candidates {
		if err := c.engine.SendAnswer(ctx, candidate.binding.Carrier, candidate.endpoint, answer); err != nil {
			c.recordCarrierUsage(candidate.endpoint.Carrier, candidate.endpoint.Address, 1, 0, err)
			failures = append(failures, fmt.Errorf("%s: %w", candidate.endpoint.Carrier, err))
			continue
		}
		c.recordCarrierUsage(candidate.endpoint.Carrier, candidate.endpoint.Address, 1, 0, nil)
		return candidate.endpoint, nil
	}
	return carriers.Endpoint{}, fmt.Errorf("all session answer reply paths failed: %w", errors.Join(failures...))
}

func (c *ControlPlane) cursor(bindingID string) carriers.Cursor {
	c.mu.RLock()
	defer c.mu.RUnlock()
	cur := c.cursors[bindingID]
	dbg("cursor: get binding=%s cursor=%s", bindingID, cur)
	return cur
}

func (c *ControlPlane) setCursor(bindingID string, cursor carriers.Cursor) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cursors[bindingID] = cursor
	dbg("cursor: set binding=%s cursor=%s", bindingID, cursor)
	if c.stateFile != "" {
		c.saveCursorsLocked()
	}
}

func (c *ControlPlane) saveCursorsLocked() {
	if c.stateFile == "" {
		return
	}
	data := make(map[string]string, len(c.cursors))
	for k, v := range c.cursors {
		data[k] = string(v)
	}
	jsonData, err := json.Marshal(data)
	if err != nil {
		log.Printf("error: failed to marshal cursors: %v", err)
		return
	}
	if err := os.WriteFile(c.stateFile, jsonData, 0600); err != nil {
		log.Printf("error: failed to write cursors to %s: %v", c.stateFile, err)
	}
}

func (c *ControlPlane) loadCursors() error {
	if c.stateFile == "" {
		return nil
	}
	data, err := os.ReadFile(c.stateFile)
	if err != nil {
		if os.IsNotExist(err) {
			dbg("loadCursors: file %s does not exist, starting fresh", c.stateFile)
			return nil
		}
		return err
	}
	if len(data) == 0 {
		dbg("loadCursors: file %s is empty", c.stateFile)
		return nil
	}
	var loaded map[string]string
	if err := json.Unmarshal(data, &loaded); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range loaded {
		c.cursors[k] = carriers.Cursor(v)
		dbg("loadCursors: loaded binding=%s cursor=%s", k, v)
	}
	log.Printf("loaded %d cursors from %s", len(loaded), c.stateFile)
	return nil
}

func (c *ControlPlane) setError(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastError = err.Error()
}

func (c *ControlPlane) setState(state string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = state
	if state == statusStateDisconnected {
		c.systemVPNProfile = nil
		c.systemVPNReadiness = &runtimeapi.SystemVPNReadiness{Ready: false, Provenance: "runtime/session", Reason: "disconnected"}
	}
}

// isNonRetriableError checks if an error should not be retried.
func isNonRetriableError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	// Don't retry on authentication/token errors
	if strings.Contains(errStr, "401") || strings.Contains(errStr, "Unauthorized") ||
		strings.Contains(errStr, "unauthenticated") || strings.Contains(errStr, "invalid_token") {
		return true
	}
	// Don't retry on explicit rejection
	if strings.Contains(errStr, "node rejected offer") {
		return true
	}
	return false
}

func (c *ControlPlane) statusLocked() StatusView {
	discovered, available := c.nodeCountsLocked()
	status := StatusView{
		Role:              c.cfg.Role,
		State:             c.state,
		NodeID:            c.cfg.NodeID,
		SocksListen:       c.cfg.SocksListen,
		UpstreamProxy:     c.cfg.UpstreamProxyFor(fabric.TrafficEgress),
		DiscoveredNodes:   discovered,
		AvailableNodes:    available,
		ReconnectAttempts: c.reconnectAttempts,
		LastError:         c.lastError,
	}
	profileStale := false
	if c.systemVPNProfile != nil {
		now := time.Now().UTC()
		if c.profileBuilder != nil && c.profileBuilder.clock != nil {
			now = c.profileBuilder.clock.Now().UTC()
		}
		if c.systemVPNProfile.ExpiresAt.After(now) {
			status.SystemVPNProfile = c.systemVPNProfile.Clone()
		} else {
			profileStale = true
			status.SystemVPNProfileReadiness = &runtimeapi.SystemVPNReadiness{Ready: false, Provenance: "runtime/profile", Reason: "profile_stale"}
		}
	}
	if c.systemVPNReadiness != nil && !profileStale {
		readiness := *c.systemVPNReadiness
		status.SystemVPNProfileReadiness = &readiness
	}
	if c.active != nil {
		status.SessionActive = true
		status.ActiveNodeID = c.active.NodeID
		status.SessionID = c.active.SessionID
		status.EgressEndpoints = append([]carriers.Endpoint(nil), c.active.EgressEndpoints...)
		status.SelectedEgressEndpointID = c.active.SelectedEgressEndpointID
		status.AutomaticEgressEndpointID = c.active.AutomaticEgressEndpointID
	}
	if c.nodeBusy {
		status.SessionActive = true
		status.SessionID = c.nodeSessionID
	}
	return status
}

func (c *ControlPlane) nodeCountsLocked() (int, int) {
	discovered := 0
	available := 0
	for _, node := range c.nodes {
		if c.cfg.Role == config.RoleNode && node.Advertisement.NodeID == c.cfg.NodeID {
			continue
		}
		discovered++
		if !node.Withdrawn {
			available++
		}
	}
	return discovered, available
}

func nodeLabel(ad session.NodeAdvertisement) string {
	if strings.TrimSpace(ad.Label) != "" {
		return strings.TrimSpace(ad.Label)
	}
	return ad.NodeID
}

func supportsTraffic(descriptor carriers.Descriptor, traffic fabric.TrafficClass) bool {
	return slices.Contains(descriptor.TrafficClasses, traffic)
}

// classifyByRole maps an explicit binding role to the appropriate control
// plane slices. Channel-derived roles remain gated at binding construction;
// top-level carrier roles are always authoritative.
func classifyByRole(cp *ControlPlane, ref carrierRef, role string) {
	endpoint := endpointWithBindingIdentity(ref)
	switch role {
	case "discovery":
		cp.bootstrap = append(cp.bootstrap, ref)
		cp.advertisePoints = append(cp.advertisePoints, endpoint)
		cp.control = append(cp.control, ref)
		cp.replyEndpoints = append(cp.replyEndpoints, endpoint)
	case "node-client", "logs", "admin":
		cp.control = append(cp.control, ref)
		cp.replyEndpoints = append(cp.replyEndpoints, endpoint)
	case "node", "client", "egress", "flex", "bulk":
		cp.egress = append(cp.egress, ref)
	default:
		dbg("classifyByRole: unknown role %q for binding %s", role, ref.ID)
	}
}

// supportsTrafficScored checks traffic eligibility. When a scorer is present
// its verdict is authoritative: Score > 0 means eligible, Score < 0 means
// not eligible. The scorer is the single source of routing truth, so the
// legacy TrafficClasses check is intentionally NOT consulted. When no
// scorer is attached we fall back to the static TrafficClasses list so the
// legacy build path still compiles and works.
func supportsTrafficScored(descriptor carriers.Descriptor, traffic fabric.TrafficClass, scorer policy.Scorer) bool {
	if scorer != nil {
		return scorer.Score(descriptor, traffic) > 0
	}
	return supportsTraffic(descriptor, traffic)
}

func supportsEgressTunnel(descriptor carriers.Descriptor) bool {
	return slices.Contains(descriptor.Capabilities, carriers.CapStream) ||
		slices.Contains(descriptor.Capabilities, carriers.CapBulk)
}

// supportsEgressScored checks egress eligibility. When a scorer is present
// its verdict is authoritative: Score > 0 means eligible, Score < 0 means
// not eligible. Egress capability (can this carrier tunnel TCP traffic?) is
// captured by the scorer's capability requirements, so the legacy capability
// fallback is intentionally NOT consulted. When no scorer is attached we
// fall back to the static capability check so the legacy build path still
// compiles and works.
func supportsEgressScored(descriptor carriers.Descriptor, scorer policy.Scorer) bool {
	if scorer != nil {
		return scorer.Score(descriptor, fabric.TrafficEgress) > 0
	}
	return supportsEgressTunnel(descriptor)
}

func sortEgressRefs(refs []carrierRef) {
	sort.SliceStable(refs, func(i, j int) bool {
		leftRank := egressCarrierPriority(refs[i].Binding.Carrier.Descriptor().ID)
		rightRank := egressCarrierPriority(refs[j].Binding.Carrier.Descriptor().ID)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return refs[i].ID < refs[j].ID
	})
}

// sortEgressRefsScored sorts egress carriers by scorer score descending.
func sortEgressRefsScored(refs []carrierRef, scorer policy.Scorer) {
	sort.SliceStable(refs, func(i, j int) bool {
		// file.mailbox is an explicitly enabled deterministic test fallback,
		// never a preferred network route. Keep it usable when it is the only
		// survivor, but rank every real byte-moving carrier ahead of it.
		leftLocalFixture := refs[i].Descriptor.ID == carriers.CarrierFileMailbox
		rightLocalFixture := refs[j].Descriptor.ID == carriers.CarrierFileMailbox
		if leftLocalFixture != rightLocalFixture {
			return !leftLocalFixture
		}
		leftScore := scorer.Score(refs[i].Descriptor, fabric.TrafficEgress)
		rightScore := scorer.Score(refs[j].Descriptor, fabric.TrafficEgress)
		if leftScore != rightScore {
			return leftScore > rightScore
		}
		return refs[i].ID < refs[j].ID
	})
}

func egressCarrierPriority(carrierID string) int {
	switch carrierID {
	case carriers.CarrierSingBoxVLESS:
		return 0
	case carriers.CarrierSSHTCP:
		return 10
	case "wbstream", carriers.CarrierWBStreamVP8:
		return 20
	default:
		return 100
	}
}

func bindingIDs(bindings map[string]policy.CarrierBinding) []string {
	ids := make([]string, 0, len(bindings))
	for id := range bindings {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func carrierRefIDs(refs []carrierRef) []string {
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.ID)
	}
	sort.Strings(ids)
	return ids
}

// isVideoTunnelCarrier returns true if the binding wraps a video tunnel adapter
// (WBStream, Telemost, or DION via whitelist-bypass/relay/).
func isVideoTunnelCarrier(binding policy.CarrierBinding) bool {
	pca, ok := binding.Carrier.(*carriers.ProviderCarrier)
	if !ok {
		log.Printf("[control] isVideoTunnelCarrier: not ProviderCarrier carrier=%s type=%T", binding.Carrier.Descriptor().ID, binding.Carrier)
		return false
	}
	_, ok = pca.GetProvider().(VideoTunnelAdapter)
	log.Printf("[control] isVideoTunnelCarrier: carrier=%s => %v", binding.Carrier.Descriptor().ID, ok)
	return ok
}

// resolveVideoTunnelAdapter returns the VideoTunnelAdapter from a binding, or nil.
func resolveVideoTunnelAdapter(binding policy.CarrierBinding) VideoTunnelAdapter {
	pca, ok := binding.Carrier.(*carriers.ProviderCarrier)
	if !ok {
		return nil
	}
	vta, ok := pca.GetProvider().(VideoTunnelAdapter)
	if !ok {
		return nil
	}
	return vta
}

// findBindingByCarrier looks up a carrier binding by ID. It first tries an
// exact match, then falls back to prefix matching (e.g., "wbstream" matches
// binding key "wbstream.vp8", and "vk.messages" matches compound key
// "vk.messages:discovery"). This handles the case where a node sends a
// platform name as the endpoint carrier while the client uses a qualified key.
func (c *ControlPlane) findBindingByCarrier(carrier string) (policy.CarrierBinding, string) {
	if b, ok := c.bindings[carrier]; ok {
		return b, carrier
	}
	// Android defaults to role-aware channel bindings and therefore advertises
	// reply carriers such as "vk.messages:node-client". A legacy desktop/node
	// runtime may intentionally retain one exact base binding. Accept only a
	// recognized channel role and only that exact base binding; do not collapse
	// one expanded role into a different expanded role.
	if carrierID, role := policy.ParseBindingKey(carrier); role != "" && config.ValidChannelRoles[role] {
		if b, ok := c.bindings[carrierID]; ok && b.Carrier != nil {
			if b.Endpoint.Carrier == carrierID || b.Carrier.Descriptor().ID == carrierID {
				return b, carrierID
			}
		}
	}
	for id, b := range c.bindings {
		if b.Endpoint.Carrier == carrier || b.Carrier.Descriptor().ID == carrier {
			return b, id
		}
	}
	// Fallback: try binding keys that start with "<carrier>." or "<carrier>:".
	for id, b := range c.bindings {
		if strings.HasPrefix(id, carrier+".") || strings.HasPrefix(id, carrier+":") {
			return b, id
		}
	}
	return policy.CarrierBinding{}, ""
}

func normalizeSessionEgressEndpoint(endpoint carriers.Endpoint, bindingKey string, binding policy.CarrierBinding) carriers.Endpoint {
	normalized := endpoint
	if carrierID := strings.TrimSpace(policy.CarrierIDFromBindingKey(bindingKey)); carrierID != "" {
		normalized.Carrier = carrierID
		return normalized
	}
	if carrierID := strings.TrimSpace(binding.Endpoint.Carrier); carrierID != "" {
		normalized.Carrier = carrierID
		return normalized
	}
	if binding.Carrier != nil {
		if carrierID := strings.TrimSpace(binding.Carrier.Descriptor().ID); carrierID != "" {
			normalized.Carrier = carrierID
			return normalized
		}
	}
	return normalized
}

// isVideoTunnelCarrierByID checks if a carrier ID corresponds to a video tunnel
// carrier, with prefix fallback for platform-name-only endpoint carriers.
func (c *ControlPlane) isVideoTunnelCarrierByID(carrier string) bool {
	if c.videoTunnelCarrierIDs[carrier] {
		return true
	}
	for id := range c.videoTunnelCarrierIDs {
		if strings.HasPrefix(id, carrier+".") {
			return true
		}
	}
	return false
}

// recordCarrierUsage records token usage for a carrier call. Maps carrierID
// to (platform, connectionType) and resolves the token ID via the store.
// Also detects auth/rate-limit errors and reports them to the token store.
func (c *ControlPlane) recordCarrierUsage(carrierID, channelID string, sent, recv int64, requestErr error) {
	// Always update the failure tracker first. Success feeds the auto-
	// disable clear-on-success counter; failures feed the deprioritization
	// and auto-disable threshold counters. We do this even when the token
	// store is missing, because the policy needs the signal regardless of
	// whether the token store happens to be attached.
	if requestErr != nil {
		c.policy.RecordCarrierFailure(carrierID, requestErr.Error())
	} else if c.policy.FailureTracker != nil {
		c.policy.FailureTracker.RecordSuccess(carrierID)
	}
	if c.tokenStore == nil {
		return
	}
	platform := tokens.PlatformFromCarrierID(carrierID)
	if platform == "" {
		return
	}
	connType := connectionTypeFromCarrierID(carrierID)
	// Find the token that was used for this carrier.
	tok, err := c.tokenStore.ResolveOne(platform, connType, channelID)
	if err != nil {
		// Try wildcard channel.
		tok, err = c.tokenStore.ResolveOne(platform, connType, "*")
		if err != nil {
			return
		}
	}
	c.tokenStore.RecordUsage(tok.ID, connType, channelID, sent, recv, requestErr)

	// Detect auth/rate-limit errors and report to token health.
	if requestErr != nil {
		errMsg := requestErr.Error()
		event := tokens.TokenHealthEvent{
			TokenID:    tok.ID,
			ReporterID: c.cfg.Identity(),
			Error:      errMsg,
		}
		if isRateLimitError(errMsg) {
			event.RateLimitHit = true
			reset := time.Now().Add(60 * time.Second) // default 60s backoff
			event.RateLimitReset = &reset
		}
		if isAuthError(errMsg) {
			event.QuotaExhausted = true
		}
		c.tokenStore.ReportHealth(event)
	}
}

// isRateLimitError detects HTTP 429 or VK error_code 6 (too many requests).
func isRateLimitError(errMsg string) bool {
	return strings.Contains(errMsg, "HTTP 429") ||
		strings.Contains(errMsg, "error_code:6") ||
		strings.Contains(errMsg, "Too many requests")
}

// isAuthError detects HTTP 401/403 or VK error_code 5 (auth failed).
func isAuthError(errMsg string) bool {
	return strings.Contains(errMsg, "HTTP 401") ||
		strings.Contains(errMsg, "HTTP 403") ||
		strings.Contains(errMsg, "status 401") ||
		strings.Contains(errMsg, "status 403") ||
		strings.Contains(errMsg, "error_code:5") ||
		strings.Contains(errMsg, "authorization failed") ||
		strings.Contains(errMsg, "invalid access_token")
}

func connectionTypeFromCarrierID(carrierID string) string {
	// e.g. "vk.messages" → "messages", "wbstream.vp8" → "vp8"
	for i, ch := range carrierID {
		if ch == '.' {
			return carrierID[i+1:]
		}
	}
	return "messages"
}
