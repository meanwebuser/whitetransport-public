package transport

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/admindiscovery"
	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/keys"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/provider"
	"github.com/meanwebuser/whitetransport/core/internal/providers"
	wtproxy "github.com/meanwebuser/whitetransport/core/internal/proxy"
	"github.com/meanwebuser/whitetransport/core/internal/router"
	"github.com/meanwebuser/whitetransport/core/internal/runtime"
	"github.com/meanwebuser/whitetransport/core/internal/tokens"
	"github.com/meanwebuser/whitetransport/core/internal/tunnel"
)

var (
	socksListenPollAttempts = 50
	socksListenPollInterval = 100 * time.Millisecond
)

type Transport struct {
	cfg              config.Config
	cancel           context.CancelFunc
	control          *runtime.ControlPlane
	proxy            *wtproxy.Server
	router           *router.CarrierRouter
	health           *router.CarrierHealth
	sendQueue        *router.SendQueue
	tokenStore       *tokens.Store
	listenerCarriers []carriers.ListenerCarrier
	bindingFailures  []runtime.CarrierBindingFailure
	bindingCount     int
	blockedReason    string

	mu        sync.Mutex
	started   bool
	socksAddr string
	stopOnce  sync.Once
	stopDone  chan struct{}
	stopErr   error
}

func Start(ctx context.Context, cfg config.Config, tokenStore *tokens.Store) (_ *Transport, returnErr error) {
	if mode := strings.TrimSpace(cfg.Routing.Mode); mode != "" && mode != "all_proxy" && mode != config.RouteModeNone && mode != config.RouteModeBypass && mode != config.RouteModeOnly {
		return nil, fmt.Errorf("routing mode %q is unsupported: native/system VPN owns split routing", mode)
	}
	if _, err := cfg.Routing.NormalizedRouteMode(); err != nil {
		return nil, fmt.Errorf("routing config invalid: %w", err)
	}
	t := &Transport{
		cfg:        cfg,
		tokenStore: tokenStore,
		stopDone:   make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(ctx)
	t.cancel = cancel

	ps := providers.NewStore()
	ks := keys.NewStore()
	for _, cc := range cfg.CarrierConfigs {
		pType, pCat := providerTypeFromCarrierID(cc.ID)
		ps.Set(&providers.Model{
			ID:       cc.ID,
			Name:     cc.ID,
			Type:     pType,
			Category: pCat,
			Version:  "1.0.0",
		})
	}
	reg := runtime.NewProviderRegistry(ps, ks)

	buildResult, err := runtime.BuildCarrierBindingsWithRegistryAndTokensIsolated(cfg, reg, tokenStore)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("build carrier bindings: %w", err)
	}
	bindings := buildResult.Bindings
	t.bindingFailures = append([]runtime.CarrierBindingFailure(nil), buildResult.Failures...)
	t.bindingCount = len(bindings)
	listeners, err := startListenerCarriers(ctx, bindings)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("start carrier listeners: %w", err)
	}
	t.listenerCarriers = listeners
	startupComplete := false
	defer func() {
		if startupComplete {
			return
		}
		cancel()
		returnErr = errors.Join(returnErr, stopListenerCarriers(context.Background(), listeners))
	}()

	tun := tunnel.Dialer(cfg, bindings)

	adaptivePolicy := policy.DefaultAdaptivePolicy()
	if tokenStore != nil {
		adaptivePolicy.TokenChecker = tokenStore
	}

	var control *runtime.ControlPlane
	if len(bindings) == 0 {
		switch cfg.Role {
		case config.RoleNode:
			err = runtime.ErrNoBootstrapCarrier
		case config.RoleClient:
			err = runtime.ErrNoControlCarrier
		}
	} else {
		control, err = runtime.NewControlPlaneWithTokens(cfg, bindings, adaptivePolicy, tun, tokenStore)
	}
	if err != nil && !errors.Is(err, runtime.ErrNoBootstrapCarrier) && !errors.Is(err, runtime.ErrNoControlCarrier) {
		cancel()
		return nil, fmt.Errorf("create control plane: %w", err)
	}
	if err != nil {
		if errors.Is(err, runtime.ErrNoBootstrapCarrier) {
			t.blockedReason = "no executable bootstrap carrier"
		} else {
			t.blockedReason = "no executable control carrier"
		}
		log.Printf("warning: transport started blocked: %s", t.blockedReason)
		control = nil
	}
	t.control = control
	if cfg.AdminRelay.Enabled {
		adminRelayCfg, err := resolveAdminRelayConfig(cfg.AdminRelay, tokenStore, cfg.Identity())
		if err != nil {
			return nil, fmt.Errorf("resolve post-session HTTP control credential: %w", err)
		}
		if _, err := runtime.ValidatePostSessionControlConfig(adminRelayCfg); err != nil {
			return nil, fmt.Errorf("configure post-session HTTP control: %w", err)
		}
		if control != nil {
			if err := control.ConfigurePostSessionControl(adminRelayCfg); err != nil {
				return nil, fmt.Errorf("configure post-session HTTP control: %w", err)
			}
		}
	}

	carrierHealth := router.NewCarrierHealth()
	if tokenStore != nil {
		carrierHealth.TokenChecker = tokenStore
	}
	for bindingKey := range bindings {
		carrierHealth.RecordConstructed(bindingKey)
	}
	for _, failure := range buildResult.Failures {
		carrierHealth.RecordInitializationFailure(failure.BindingKey, string(failure.Stage), failure.Code, failure.Retryable, failure.ResourceGroup)
	}
	carrierRouter := router.NewWithHealth(carrierHealth)
	sendQueue := router.NewSendQueue(carrierHealth)
	t.router = carrierRouter
	t.health = carrierHealth
	t.sendQueue = sendQueue

	if control != nil {
		control.SetRouter(carrierRouter, carrierHealth, sendQueue)
		sendQueue.Start(ctx)
		if err := carrierRouter.Start(ctx); err != nil {
			log.Printf("warning: carrier router start: %v", err)
		}
		control.Start(ctx)
	}

	// Start admin-based discovery if configured: fetches node list from
	// the admin panel and injects into ControlPlane alongside carrier-discovered nodes.
	if control != nil && cfg.AdminDiscovery.Enabled {
		sink := func(nodes []admindiscovery.NodeInfo) {
			for _, n := range nodes {
				control.InjectAdminAdvertisement(n.ToAdvertisement())
			}
		}
		if err := admindiscovery.Start(ctx, cfg.AdminDiscovery, sink, log.Printf); err != nil {
			log.Printf("warning: admin discovery start failed: %v", err)
		} else {
			log.Printf("admin discovery: polling %s every %ds",
				cfg.AdminDiscovery.AdminURL, cfg.AdminDiscovery.PollIntervalSec)
		}
	}

	if strings.TrimSpace(cfg.SocksListen) != "" {
		if tun == nil {
			log.Printf("warning: no carrier bindings — SOCKS5 proxy at %s will reject all connections", cfg.SocksListen)
		}
		srv := &wtproxy.Server{
			ListenAddr:            cfg.SocksListen,
			DNSOverStreamFallback: true,
			Logf:                  log.Printf,
		}
		if control != nil {
			srv.EgressDialer = control.DialEgress
			srv.EgressPacketDialer = control.OpenPacketEgress
		}
		t.proxy = srv

		go func() {
			if err := srv.ListenAndServe(ctx); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, context.Canceled) {
				log.Printf("socks5 error: %v", err)
			}
		}()

		for i := 0; i < socksListenPollAttempts; i++ {
			if addr := srv.Addr(); addr != "" {
				t.mu.Lock()
				t.socksAddr = addr
				t.mu.Unlock()
				break
			}
			select {
			case <-ctx.Done():
				cancel()
				return nil, ctx.Err()
			case <-time.After(socksListenPollInterval):
			}
		}
		t.mu.Lock()
		started := t.socksAddr
		t.mu.Unlock()
		if started == "" {
			cancel()
			return nil, fmt.Errorf("socks5 proxy failed to listen on %s", cfg.SocksListen)
		}
		if control != nil {
			control.SetActualSocksAddr(started)
		}

	}

	t.mu.Lock()
	t.started = true
	t.mu.Unlock()
	startupComplete = true

	return t, nil
}

func (t *Transport) Stop() error {
	t.stopOnce.Do(func() {
		t.mu.Lock()
		cancel := t.cancel
		control := t.control
		listeners := t.listenerCarriers
		router := t.router
		sendQueue := t.sendQueue
		t.started = false
		t.socksAddr = ""
		t.listenerCarriers = nil
		t.mu.Unlock()

		if cancel != nil {
			cancel()
		}
		if control != nil {
			control.Stop()
		}
		listenerErr := stopListenerCarriers(context.Background(), listeners)
		if router != nil {
			router.Stop()
		}
		if sendQueue != nil {
			sendQueue.Stop()
		}
		t.stopErr = listenerErr
		close(t.stopDone)
	})
	<-t.stopDone
	return t.stopErr
}

func (t *Transport) Connect(ctx context.Context, nodeID string) (runtime.StatusView, error) {
	if t.control == nil {
		return t.Status(), fmt.Errorf("transport blocked: %s", t.blockedReason)
	}
	return t.control.Connect(ctx, nodeID)
}

func (t *Transport) Disconnect() runtime.StatusView {
	if t.control == nil {
		return runtime.StatusView{State: "disconnected"}
	}
	return t.control.Disconnect()
}

// SelectEgressEndpoint pins an active session to one endpoint for an explicit
// local diagnostic. Normal session routing remains adaptive when it is not
// called.
func (t *Transport) SelectEgressEndpoint(endpointID string) (runtime.StatusView, error) {
	if t.control == nil {
		return runtime.StatusView{State: "disconnected"}, errors.New("transport not started")
	}
	return t.control.SelectEgressEndpoint(endpointID)
}

func (t *Transport) ListNodes() []runtime.NodeView {
	if t.control == nil {
		return nil
	}
	return t.control.ListNodes()
}

func (t *Transport) Status() runtime.StatusView {
	if t.control == nil {
		state := "disconnected"
		if t.blockedReason != "" {
			state = "blocked"
		}
		return runtime.StatusView{Role: t.cfg.Role, State: state, SocksListen: t.GetSocksAddr(), LastError: t.blockedReason}
	}
	status := t.control.Status()
	if len(t.bindingFailures) == 0 {
		return status
	}
	if !status.SessionActive {
		if t.bindingCount == 0 {
			status.State = "blocked"
		} else {
			status.State = "degraded"
		}
	}
	status.LastError = fmt.Sprintf("%d carrier binding(s) unavailable", len(t.bindingFailures))
	return status
}

func (t *Transport) GetSocksAddr() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.socksAddr
}

func (t *Transport) CarrierHealthSnapshot() map[string]router.CarrierSnapshot {
	if t.health != nil {
		return t.health.Snapshot()
	}
	return nil
}

func (t *Transport) Started() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.started
}

func BuildTokenStore(cfg config.Config) *tokens.Store {
	if cfg.TokenStore == nil {
		return nil
	}
	ts := tokens.NewStore()
	for _, te := range cfg.TokenStore.Tokens {
		tok := &tokens.Token{
			ID:                te.ID,
			Platform:          te.Platform,
			Kind:              te.Kind,
			Lifecycle:         te.Lifecycle,
			Status:            te.Status,
			Value:             te.Value,
			Parts:             te.Parts,
			Refresh:           te.Refresh,
			CanCreateChannels: te.CanCreateChannels,
			Tags:              te.Tags,
		}
		if tok.Status == "" {
			tok.Status = tokens.StatusActive
		}
		if te.ExpiresAt != nil {
			if t, err := time.Parse(time.RFC3339, *te.ExpiresAt); err == nil {
				tok.ExpiresAt = &t
			}
		}
		tok.CreatedAt = time.Now()
		ts.Set(tok)
	}
	for _, be := range cfg.TokenStore.Bindings {
		ts.AddBinding(tokens.Binding{
			TokenID:        be.TokenID,
			Platform:       be.Platform,
			ConnectionType: be.ConnectionType,
			ChannelID:      be.ChannelID,
			Role:           be.Role,
			Priority:       be.Priority,
			Enabled:        be.Enabled,
		})
	}
	log.Printf("token_store: loaded %d tokens, %d bindings",
		len(cfg.TokenStore.Tokens), len(cfg.TokenStore.Bindings))
	return ts
}

func providerTypeFromCarrierID(id string) (provider.Type, provider.Category) {
	switch id {
	case "vk.messages", "ok.messages", "file.mailbox":
		return provider.TypeMessaging, provider.CategorySocial
	case "vk.docs.256", "vk.docs.1024", "vk.photos":
		return provider.TypeFileTransfer, provider.CategoryCloud
	case "ok.docs.256", "ok.photos":
		return provider.TypeFileTransfer, provider.CategoryCloud
	case "wbstream":
		return provider.TypeVideoCall, provider.CategoryVideo
	default:
		return provider.TypeMessaging, provider.CategoryOther
	}
}
