package main

import (
	"errors"
	"fmt"
)

// refreshState changes only physical exclusions and profile metadata. The owned
// process and tunnel routes remain installed, including while SOCKS recovers.
func refreshState(current State, cfg Config, plan Plan, add, remove func(Route) error, save func(State) error) (State, error) {
	plan = bindOwnedTunPeer(plan, current.TunPeer)
	old := current.Config
	if cfg.SOCKSHost != old.SOCKSHost || cfg.SOCKSPort != old.SOCKSPort || cfg.Mode != old.Mode || cfg.MTU != old.MTU || cfg.Tun2SocksPath != old.Tun2SocksPath || cfg.StatePath != old.StatePath || cfg.StatusPath != old.StatusPath || cfg.LogPath != old.LogPath {
		return current, fmt.Errorf("changed tunnel configuration requires explicit reconnect; existing tunnel retained")
	}
	if cfg.DaemonInstanceID != old.DaemonInstanceID || cfg.ProfileRevision < old.ProfileRevision {
		return current, fmt.Errorf("refresh profile must belong to the current daemon and cannot move backwards")
	}
	key := func(route Route) string {
		return route.CIDR + "|" + route.Via + "|" + darwinRouteNextHop(route, current.Gateway)
	}
	before, after := make(map[string]Route), make(map[string]Route)
	byCIDR := make(map[string]string)
	for _, route := range current.Routes {
		before[key(route)] = route
		byCIDR[route.CIDR] = key(route)
	}
	for _, route := range plan.Routes {
		if previous, exists := byCIDR[route.CIDR]; exists && previous != key(route) {
			return current, fmt.Errorf("changed next hop for existing prefix requires explicit reconnect")
		}
		after[key(route)] = route
	}
	var additions, removals []Route
	for _, route := range plan.Routes {
		if _, exists := before[key(route)]; !exists {
			if route.Via != "gateway" {
				return current, fmt.Errorf("changed tunnel routes require explicit reconnect")
			}
			additions = append(additions, route)
		}
	}
	for _, route := range current.Routes {
		if _, exists := after[key(route)]; !exists {
			if route.Via != "gateway" {
				return current, fmt.Errorf("changed tunnel routes require explicit reconnect")
			}
			removals = append(removals, route)
		}
	}
	var added, removed []Route
	rollback := func(cause error) (State, error) {
		for i := len(removed) - 1; i >= 0; i-- {
			cause = errors.Join(cause, add(removed[i]))
		}
		for i := len(added) - 1; i >= 0; i-- {
			cause = errors.Join(cause, remove(added[i]))
		}
		return current, cause
	}
	for _, route := range additions {
		if err := add(route); err != nil {
			return rollback(fmt.Errorf("add refreshed bypass: %w", err))
		}
		added = append(added, route)
	}
	for _, route := range removals {
		if err := remove(route); err != nil {
			return rollback(fmt.Errorf("remove stale bypass: %w", err))
		}
		removed = append(removed, route)
	}
	next := current
	next.Config, next.Routes = cfg, plan.Routes
	if err := save(next); err != nil {
		_, rollbackErr := rollback(err)
		return current, errors.Join(rollbackErr, save(current))
	}
	return next, nil
}
