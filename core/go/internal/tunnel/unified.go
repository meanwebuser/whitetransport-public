package tunnel

import (
	"context"
	"fmt"
	"log"
	"net"
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
	envelopeTunnel *CarrierTunnel
	singBoxTunnel  *SingBoxTunnel
}

var _ session.CarrierTunnel = (*UnifiedCarrierTunnel)(nil)
var _ session.PacketEgress = (*UnifiedCarrierTunnel)(nil)

func NewUnifiedCarrierTunnel(identity string, bindings map[string]policy.CarrierBinding) *UnifiedCarrierTunnel {
	return &UnifiedCarrierTunnel{
		identity:       identity,
		bindings:       bindings,
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
	return t.DialContextForSession(ctx, "", endpoint, targetAddr)
}

func (t *UnifiedCarrierTunnel) DialContextForSession(ctx context.Context, sessionID string, endpoint carriers.Endpoint, targetAddr string) (net.Conn, error) {
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
	if _, ok := binding.Carrier.(carriers.ManagedStreamCarrier); ok {
		// Keep every managed sidecar on SingBoxTunnel's lifecycle registry so
		// idle reconciliation can restart it after an unexpected child exit.
		binding.Endpoint = endpoint
		t.singBoxTunnel.SetSessionBinding(endpoint, binding)
		return t.singBoxTunnel.DialContext(ctx, endpoint, targetAddr)
	}
	if dialer, ok := binding.Carrier.(carriers.StreamDialer); ok {
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
	return t.envelopeTunnel.DialContextForSession(ctx, sessionID, endpoint, targetAddr)
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
func (t *UnifiedCarrierTunnel) SetSessionCipher(id string, c *fabric.EnvelopeCipher) {
	t.envelopeTunnel.SetSessionCipher(id, c)
}
func (t *UnifiedCarrierTunnel) ClearSessionCipher(id string) {
	t.envelopeTunnel.ClearSessionCipher(id)
}
func (t *UnifiedCarrierTunnel) SetOnIdle(fn func())   { t.envelopeTunnel.SetOnIdle(fn) }
func (t *UnifiedCarrierTunnel) SetOnActive(fn func()) { t.envelopeTunnel.SetOnActive(fn) }
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

func (t *UnifiedCarrierTunnel) Reconcile(ctx context.Context) error {
	return t.singBoxTunnel.Reconcile(ctx)
}

func (t *UnifiedCarrierTunnel) Close() error {
	return t.singBoxTunnel.Close()
}

// WireSingBoxStreamDialer is a test helper.
func WireSingBoxStreamDialer(sb *carriers.SingBoxVLESSCarrier, localListen string) {
	sb.StreamDialFunc = func(ctx context.Context, _ carriers.Endpoint, tgt string) (net.Conn, error) {
		return socks5Connect(ctx, localListen, tgt)
	}
}
