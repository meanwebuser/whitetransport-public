package main

import (
	"fmt"
	"net"
	"net/netip"
)

// chooseDirectTunPeer keeps local=peer, while avoiding the IPv4 prefixes of
// every other interface. Checking whole prefixes avoids competing subnet
// routes from another VPN that uses the same benchmark address range.
func chooseDirectTunPeer(occupied []netip.Prefix) (string, error) {
	for second := 18; second <= 19; second++ {
		for third := 0; third < 256; third++ {
			candidate := netip.AddrFrom4([4]byte{198, byte(second), byte(third), 1})
			available := true
			for _, prefix := range occupied {
				if prefix.Contains(candidate) {
					available = false
					break
				}
			}
			if available {
				return candidate.String(), nil
			}
		}
	}
	return "", fmt.Errorf("no unused direct-tunnel peer in 198.18.0.0/15; another interface occupies the address pool")
}

func allocateDirectTunAddresses(ownedInterface string) (local, peer string, err error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", "", fmt.Errorf("inspect existing interfaces: %w", err)
	}
	var occupied []netip.Prefix
	for _, iface := range interfaces {
		if iface.Name == ownedInterface {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			return "", "", fmt.Errorf("inspect interface %s addresses: %w", iface.Name, err)
		}
		for _, address := range addresses {
			prefix, err := netip.ParsePrefix(address.String())
			if err != nil {
				return "", "", fmt.Errorf("parse interface %s address: %w", iface.Name, err)
			}
			if prefix.Addr().Is4() {
				occupied = append(occupied, prefix)
			}
		}
	}
	peer, err = chooseDirectTunPeer(occupied)
	return peer, peer, err
}

func bindOwnedTunPeer(plan Plan, peer string) Plan {
	if peer == "" {
		return plan
	} // Existing state uses the original default.
	routes := append([]Route(nil), plan.Routes...)
	for i := range routes {
		if routes[i].Via == "utun" {
			routes[i].Gateway = peer
		}
	}
	plan.Routes = routes
	return plan
}
