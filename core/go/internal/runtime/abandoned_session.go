package runtime

import (
	"context"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/session"
)

const maxAbandonedSessions = 8

func (c *ControlPlane) pruneAbandonedSessionsLocked() {
	now := time.Now()
	for node, active := range c.abandonedSessions {
		if !active.ExpiresAt.After(now) {
			delete(c.abandonedSessions, node)
		}
	}
}

// Keep only release coordinates until the old node's lease expires. Losing
// the client path does not mean the node stopped owning the abandoned session.
func (c *ControlPlane) rememberAbandonedSessionLocked(active *activeSession) {
	if active == nil || active.SessionID == "" || active.NodeID == "" {
		return
	}
	c.pruneAbandonedSessionsLocked()
	if c.abandonedSessions == nil {
		c.abandonedSessions = make(map[string]*activeSession)
	}
	if _, exists := c.abandonedSessions[active.NodeID]; !exists && len(c.abandonedSessions) >= maxAbandonedSessions {
		oldest := ""
		for node, pending := range c.abandonedSessions {
			if oldest == "" || pending.ExpiresAt.Before(c.abandonedSessions[oldest].ExpiresAt) {
				oldest = node
			}
		}
		delete(c.abandonedSessions, oldest)
	}
	expires := active.ExpiresAt
	if expires.IsZero() {
		expires = time.Now().Add(2 * time.Minute)
	}
	if !expires.After(time.Now()) {
		return
	}
	c.abandonedSessions[active.NodeID] = &activeSession{NodeID: active.NodeID, SessionID: active.SessionID, ExpiresAt: expires}
}

// Retry the existing release protocol only when reconnecting to that SAME
// node, using the currently reachable control contact. Independent node
// failover never waits for an unreachable old node's release.
func (c *ControlPlane) releaseAbandonedSession(ctx context.Context, nodeID string, contact carriers.Endpoint, binding policy.CarrierBinding) error {
	c.mu.Lock()
	c.pruneAbandonedSessionsLocked()
	active := c.abandonedSessions[nodeID]
	c.mu.Unlock()
	if active == nil {
		return nil
	}
	releaseCtx, cancel := context.WithTimeout(ctx, releaseSendTimeout)
	defer cancel()
	if err := c.engine.SendRelease(releaseCtx, binding.Carrier, contact, session.Release{SessionID: active.SessionID, ClientID: c.cfg.Identity(), NodeID: nodeID, Reason: "route_recovery"}); err != nil {
		return err
	}
	c.mu.Lock()
	if c.abandonedSessions[nodeID] == active {
		delete(c.abandonedSessions, nodeID)
	}
	c.mu.Unlock()
	return nil
}
