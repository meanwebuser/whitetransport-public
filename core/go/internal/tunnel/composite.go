package tunnel

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/session"
)

// CompositeTunnel lets outbound-style egress carriers such as SSH coexist with
// fabric envelope tunnels in the same active session.
type CompositeTunnel struct {
	tunnels []session.CarrierTunnel
}

func NewCompositeTunnel(tunnels ...session.CarrierTunnel) *CompositeTunnel {
	return &CompositeTunnel{tunnels: tunnels}
}

func (t *CompositeTunnel) SupportsEndpoint(endpoint carriers.Endpoint) bool {
	for _, tunnel := range t.tunnels {
		if tunnel.SupportsEndpoint(endpoint) {
			return true
		}
	}
	return false
}

// OpenPacketConn selects the packet-capable tunnel for an endpoint. Stream-only
// tunnels are skipped explicitly; callers receive an error instead of a direct
// UDP fallback when no carrier implements packet egress.
func (t *CompositeTunnel) OpenPacketConn(ctx context.Context, endpoint carriers.Endpoint, metadata session.PacketMetadata) (net.PacketConn, error) {
	for _, tunnel := range t.tunnels {
		packetEgress, ok := tunnel.(session.PacketEgress)
		if !ok || !packetEgress.SupportsPacketEndpoint(endpoint) {
			continue
		}
		return packetEgress.OpenPacketConn(ctx, endpoint, metadata)
	}
	return nil, fmt.Errorf("composite tunnel: no packet egress for carrier %s endpoint %s", endpoint.Carrier, endpoint.ID)
}

func (t *CompositeTunnel) SupportsPacketEndpoint(endpoint carriers.Endpoint) bool {
	for _, tunnel := range t.tunnels {
		if packetEgress, ok := tunnel.(session.PacketEgress); ok && packetEgress.SupportsPacketEndpoint(endpoint) {
			return true
		}
	}
	return false
}

func (t *CompositeTunnel) SetPacketSession(sessionID string, peerID string, expiresAt time.Time) {
	for _, tunnel := range t.tunnels {
		if lifecycle, ok := tunnel.(session.PacketSessionLifecycle); ok {
			lifecycle.SetPacketSession(sessionID, peerID, expiresAt)
		}
	}
}

func (t *CompositeTunnel) ClosePacketSession(sessionID string) {
	for _, tunnel := range t.tunnels {
		if lifecycle, ok := tunnel.(session.PacketSessionLifecycle); ok {
			lifecycle.ClosePacketSession(sessionID)
		}
	}
}

func (t *CompositeTunnel) DialContext(ctx context.Context, endpoint carriers.Endpoint, targetAddr string) (net.Conn, error) {
	// A node may publish a per-route Xray identity (xray-de-reality) rather
	// than the canonical singbox.vless descriptor. Ask SingBoxTunnel first so
	// a dynamic session binding cannot fall through to CarrierTunnel.
	for _, tunnel := range t.tunnels {
		if _, isSB := tunnel.(*SingBoxTunnel); isSB && tunnel.SupportsEndpoint(endpoint) {
			return tunnel.DialContext(ctx, endpoint, targetAddr)
		}
	}
	// Resolve the session binding before falling back to envelope egress. This
	// keeps route aliases intact and supports every native stream carrier,
	// including both ssh.tcp and ssh.fabric, without concrete type checks.
	for _, tunnel := range t.tunnels {
		bindingResolver, ok := tunnel.(interface {
			bindingForEndpoint(carriers.Endpoint) (policy.CarrierBinding, bool)
		})
		if !ok {
			continue
		}
		binding, ok := bindingResolver.bindingForEndpoint(endpoint)
		if !ok {
			continue
		}
		if dialer, ok := binding.Carrier.(carriers.StreamDialer); ok {
			return dialer.DialStream(ctx, endpoint, targetAddr)
		}
	}
	// Route DataTunnel endpoints (wbstream) to DataTunnelEgress.
	for _, tunnel := range t.tunnels {
		if _, isDTE := tunnel.(*DataTunnelEgress); isDTE && tunnel.SupportsEndpoint(endpoint) {
			return tunnel.DialContext(ctx, endpoint, targetAddr)
		}
	}
	// Everything else (VK, OK) goes to CarrierTunnel.
	for _, tunnel := range t.tunnels {
		if _, isCT := tunnel.(*CarrierTunnel); isCT && tunnel.SupportsEndpoint(endpoint) {
			return tunnel.DialContext(ctx, endpoint, targetAddr)
		}
	}
	return nil, fmt.Errorf("composite tunnel: no tunnel supports carrier %s endpoint %s", endpoint.Carrier, endpoint.ID)
}

func (t *CompositeTunnel) ServeEgress(ctx context.Context, bindings map[string]policy.CarrierBinding) error {
	for _, tunnel := range t.tunnels {
		server, ok := tunnel.(interface {
			ServeEgress(context.Context, map[string]policy.CarrierBinding) error
		})
		if !ok {
			continue
		}
		go func() {
			_ = server.ServeEgress(ctx, bindings)
		}()
	}
	<-ctx.Done()
	return ctx.Err()
}

func (t *CompositeTunnel) SetSessionBinding(endpoint carriers.Endpoint, binding policy.CarrierBinding) {
	for _, tunnel := range t.tunnels {
		if binder, ok := tunnel.(interface {
			SetSessionBinding(carriers.Endpoint, policy.CarrierBinding)
		}); ok {
			binder.SetSessionBinding(endpoint, binding)
		}
	}
}

func (t *CompositeTunnel) ClearSessionBinding(endpoint carriers.Endpoint) {
	for _, tunnel := range t.tunnels {
		if binder, ok := tunnel.(interface{ ClearSessionBinding(carriers.Endpoint) }); ok {
			binder.ClearSessionBinding(endpoint)
		}
	}
}

func (t *CompositeTunnel) SetCipher(cipher *fabric.EnvelopeCipher) {
	for _, tunnel := range t.tunnels {
		if c, ok := tunnel.(interface{ SetCipher(*fabric.EnvelopeCipher) }); ok {
			c.SetCipher(cipher)
		}
	}
}

func (t *CompositeTunnel) ClearCipher() {
	for _, tunnel := range t.tunnels {
		if c, ok := tunnel.(interface{ ClearCipher() }); ok {
			c.ClearCipher()
		}
	}
}

func (t *CompositeTunnel) SetOnIdle(fn func()) {
	for _, tunnel := range t.tunnels {
		if hook, ok := tunnel.(interface{ SetOnIdle(func()) }); ok {
			hook.SetOnIdle(fn)
		}
	}
}

func (t *CompositeTunnel) SetOnActive(fn func()) {
	for _, tunnel := range t.tunnels {
		if hook, ok := tunnel.(interface{ SetOnActive(func()) }); ok {
			hook.SetOnActive(fn)
		}
	}
}

func (t *CompositeTunnel) SetOnClosed(fn func()) {
	for _, tunnel := range t.tunnels {
		if hook, ok := tunnel.(interface{ SetOnClosed(func()) }); ok {
			hook.SetOnClosed(fn)
		}
	}
}

func (t *CompositeTunnel) Close() error {
	var closeErr error
	for _, tunnel := range t.tunnels {
		closer, ok := tunnel.(interface{ Close() error })
		if !ok {
			continue
		}
		if err := closer.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	return closeErr
}
