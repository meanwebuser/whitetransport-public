package tunnel

import (
	"context"
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

// UnifiedCarrierTunnel dispatches by interface detection on the carrier.
type UnifiedCarrierTunnel struct {
	identity       string
	bindings       map[string]policy.CarrierBinding
	mu             sync.Mutex
	processes      map[string]singBoxProcessState
	envelopeTunnel *CarrierTunnel
	singBoxTunnel  *SingBoxTunnel
}

var _ session.CarrierTunnel = (*UnifiedCarrierTunnel)(nil)
var _ session.PacketEgress = (*UnifiedCarrierTunnel)(nil)

func NewUnifiedCarrierTunnel(identity string, bindings map[string]policy.CarrierBinding) *UnifiedCarrierTunnel {
	return &UnifiedCarrierTunnel{
		identity:       identity,
		bindings:       bindings,
		processes:      make(map[string]singBoxProcessState),
		envelopeTunnel: NewCarrierTunnel(identity, bindings),
		singBoxTunnel:  NewSingBoxTunnel(bindings),
	}
}

func (t *UnifiedCarrierTunnel) SupportsEndpoint(endpoint carriers.Endpoint) bool {
	if t.singBoxTunnel.SupportsEndpoint(endpoint) {
		return true
	}
	if _, ok := t.bindings[endpoint.Carrier]; ok {
		return true
	}
	return t.envelopeTunnel.SupportsEndpoint(endpoint)
}

func (t *UnifiedCarrierTunnel) DialContext(ctx context.Context, endpoint carriers.Endpoint, targetAddr string) (net.Conn, error) {
	if t.singBoxTunnel.SupportsEndpoint(endpoint) {
		return t.singBoxTunnel.DialContext(ctx, endpoint, targetAddr)
	}
	// Session endpoints commonly use a node-issued alias instead of the
	// canonical descriptor ID. Resolve through CarrierTunnel's binding table so
	// interface dispatch receives the exact advertised endpoint.
	binding, ok := t.envelopeTunnel.bindingForEndpoint(endpoint)
	if !ok {
		return t.envelopeTunnel.DialContext(ctx, endpoint, targetAddr)
	}
	if dialer, ok := binding.Carrier.(carriers.StreamDialer); ok {
		if sb, isSB := binding.Carrier.(*carriers.SingBoxVLESSCarrier); isSB {
			if err := t.ensureSingBoxDialer(ctx, endpoint, sb); err != nil {
				return nil, err
			}
		}
		log.Printf("[unified] StreamDialer carrier=%s target=%s", endpoint.Carrier, targetAddr)
		return dialer.DialStream(ctx, endpoint, targetAddr)
	}
	dtp, ok := binding.Carrier.(DataTunnelProvider)
	if !ok {
		// ProviderCarrier is the normal runtime wrapper for platform adapters.
		// The DataTunnelProvider capability belongs to the wrapped provider, not
		// to the envelope carrier facade, so unwrap it before falling back to
		// CarrierTunnel.
		if wrapped, wrappedOK := binding.Carrier.(*carriers.ProviderCarrier); wrappedOK {
			dtp, ok = wrapped.GetProvider().(DataTunnelProvider)
		}
	}
	if ok {
		log.Printf("[unified] DataTunnel carrier=%s target=%s", endpoint.Carrier, targetAddr)
		dte := NewDataTunnelEgress(t.identity, endpoint.Carrier, endpoint.ID, dtp)
		return dte.DialContext(ctx, endpoint, targetAddr)
	}
	log.Printf("[unified] envelope carrier=%s target=%s", endpoint.Carrier, targetAddr)
	return t.envelopeTunnel.DialContext(ctx, endpoint, targetAddr)
}

// OpenPacketConn keeps UDP on the envelope/session fabric. Carrier adapters
// that expose only stream APIs are rejected rather than silently bypassed.
func (t *UnifiedCarrierTunnel) OpenPacketConn(ctx context.Context, endpoint carriers.Endpoint, metadata session.PacketMetadata) (net.PacketConn, error) {
	if !t.envelopeTunnel.SupportsPacketEndpoint(endpoint) {
		return nil, fmt.Errorf("unified tunnel: packet egress unsupported for %s", endpoint.ID)
	}
	return t.envelopeTunnel.OpenPacketConn(ctx, endpoint, metadata)
}

func (t *UnifiedCarrierTunnel) SupportsPacketEndpoint(endpoint carriers.Endpoint) bool {
	return t.envelopeTunnel.SupportsPacketEndpoint(endpoint)
}

func (t *UnifiedCarrierTunnel) SetPacketSession(sessionID string, peerID string, expiresAt time.Time) {
	t.envelopeTunnel.SetPacketSession(sessionID, peerID, expiresAt)
}

func (t *UnifiedCarrierTunnel) ClosePacketSession(sessionID string) {
	t.envelopeTunnel.ClosePacketSession(sessionID)
}

func (t *UnifiedCarrierTunnel) SetProxyURL(u string)               { t.envelopeTunnel.SetProxyURL(u) }
func (t *UnifiedCarrierTunnel) SetCipher(c *fabric.EnvelopeCipher) { t.envelopeTunnel.SetCipher(c) }
func (t *UnifiedCarrierTunnel) ClearCipher()                       { t.envelopeTunnel.ClearCipher() }
func (t *UnifiedCarrierTunnel) SetOnIdle(fn func())                { t.envelopeTunnel.SetOnIdle(fn) }
func (t *UnifiedCarrierTunnel) SetOnActive(fn func())              { t.envelopeTunnel.SetOnActive(fn) }
func (t *UnifiedCarrierTunnel) SetSessionBinding(ep carriers.Endpoint, b policy.CarrierBinding) {
	t.envelopeTunnel.SetSessionBinding(ep, b)
	t.singBoxTunnel.SetSessionBinding(ep, b)
}
func (t *UnifiedCarrierTunnel) ClearSessionBinding(ep carriers.Endpoint) {
	t.envelopeTunnel.ClearSessionBinding(ep)
	t.singBoxTunnel.ClearSessionBinding(ep)
}
func (t *UnifiedCarrierTunnel) ServeEgress(ctx context.Context, b map[string]policy.CarrierBinding) error {
	return t.envelopeTunnel.ServeEgress(ctx, b)
}

func (t *UnifiedCarrierTunnel) Close() error {
	t.mu.Lock()
	var cerr error
	for k, s := range t.processes {
		if err := s.process.Close(); err != nil && cerr == nil {
			cerr = fmt.Errorf("unified tunnel: close %s: %w", k, err)
		}
		delete(t.processes, k)
	}
	t.mu.Unlock()
	if err := t.singBoxTunnel.Close(); err != nil && cerr == nil {
		cerr = fmt.Errorf("unified tunnel: close dynamic sing-box: %w", err)
	}
	return cerr
}

func (t *UnifiedCarrierTunnel) ensureSingBoxDialer(ctx context.Context, ep carriers.Endpoint, sb *carriers.SingBoxVLESSCarrier) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.processes[ep.Carrier]; ok {
		return nil
	}
	cfg := sb.Config()
	ll := strings.TrimSpace(cfg.LocalListen)
	if ll == "" {
		ll = defaultSingBoxLocalListen
	}
	proc, resolved, err := singBoxRunner.Start(ctx, sb, ep, ll)
	if err != nil {
		return fmt.Errorf("unified tunnel: start sing-box %s: %w", ep.Carrier, err)
	}
	t.processes[ep.Carrier] = singBoxProcessState{process: proc, localListen: resolved}
	listen := resolved
	sb.StreamDialFunc = func(dctx context.Context, _ carriers.Endpoint, tgt string) (net.Conn, error) {
		return socks5Connect(dctx, listen, tgt)
	}
	log.Printf("[unified] sing-box started carrier=%s listen=%s", ep.Carrier, resolved)
	return nil
}

// WireSingBoxStreamDialer is a test helper.
func WireSingBoxStreamDialer(sb *carriers.SingBoxVLESSCarrier, localListen string) {
	sb.StreamDialFunc = func(ctx context.Context, _ carriers.Endpoint, tgt string) (net.Conn, error) {
		return socks5Connect(ctx, localListen, tgt)
	}
}
