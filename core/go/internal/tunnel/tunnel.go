package tunnel

import (
	"log"
	"os"

	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/session"
)

// Dialer returns the correct session.CarrierTunnel for the config and bindings.
// When carrier bindings exist it returns a CarrierTunnel or DataTunnelEgress
// that routes egress through carriers. Returns nil when no bindings are
// configured — the daemon will start in API-only mode without SOCKS5 egress.
//
// If a binding wraps a DataTunnelProvider adapter (e.g. whitelist-bypass) the
// returned tunnel uses the DataTunnel frame protocol (MsgConnect/MsgData/…)
// instead of the envelope-based CarrierTunnel.
func Dialer(cfg config.Config, bindings map[string]policy.CarrierBinding) session.CarrierTunnel {
	if len(bindings) == 0 {
		return nil
	}

	log.Printf("[tunnel] Dialer: %d bindings", len(bindings))
	for id, b := range bindings {
		log.Printf("[tunnel]   %s: desc=%s", id, b.Carrier.Descriptor().ID)
	}

	// When WT_CAPABILITY_SCORING=1, use the unified tunnel that dispatches
	// by interface detection (StreamDialer, DataTunnelProvider, envelope).
	if os.Getenv("WT_CAPABILITY_SCORING") == "1" {
		ut := NewUnifiedCarrierTunnel(cfg.Identity(), bindings)
		if proxyURL := cfg.UpstreamProxyFor(fabric.TrafficEgress); proxyURL != "" {
			ut.SetProxyURL(proxyURL)
		}
		return ut
	}

	// Legacy composite tunnel path.
	proxyURL := cfg.UpstreamProxyFor(fabric.TrafficEgress)
	tunnels := make([]session.CarrierTunnel, 0, 4)

	// Always include CarrierTunnel — it handles envelope-based TCP tunneling
	// for VK Messages, OK Messages, and any other mailbox carrier.
	ct := NewCarrierTunnel(cfg.Identity(), bindings)
	if proxyURL != "" {
		ct.SetProxyURL(proxyURL)
	}
	tunnels = append(tunnels, ct)

	// Add DataTunnelEgress for WBStream/DION/Telemost adapters.
	if adapter, carrierName, epID, ok := getDataTunnelAdapter(bindings); ok {
		dte := NewDataTunnelEgress(cfg.Identity(), carrierName, epID, adapter)
		if proxyURL != "" {
			dte.SetProxyURL(proxyURL)
		}
		tunnels = append(tunnels, dte)
	}
	if sshTunnel := NewSSHTunnel(bindings); sshTunnel != nil {
		tunnels = append(tunnels, sshTunnel)
	}
	// Keep a session-bindable SingBox tunnel even before a node sends its
	// encrypted Xray profile. Client-owned room creation often has no static
	// VLESS binding, but may receive one in session.answer.
	tunnels = append(tunnels, NewSingBoxTunnel(bindings))
	log.Printf("[tunnel] Dialer: added session-bindable SingBoxTunnel")
	log.Printf("[tunnel] Dialer: %d tunnels, returning %T", len(tunnels), tunnels[0])
	if len(tunnels) == 1 {
		return tunnels[0]
	}
	log.Printf("[tunnel] Dialer: returning CompositeTunnel with %d tunnels", len(tunnels))
	return NewCompositeTunnel(tunnels...)
}
