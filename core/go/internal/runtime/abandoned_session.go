package runtime

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/session"
)

const (
	maxAbandonedSessions    = 8
	maxPendingOffersPerNode = 8
)

type abandonedSession struct {
	activeSession
	// A release may overtake a delayed offer. Retain it until the answer
	// confirms acceptance, so that late answer triggers another release.
	PendingOffer bool
}

func (c *ControlPlane) pruneAbandonedSessionsLocked() {
	now := time.Now()
	for id, active := range c.abandonedSessions {
		if !active.ExpiresAt.After(now) {
			delete(c.abandonedSessions, id)
		}
	}
}

// Keep only release coordinates until the old node's lease expires. Losing
// the client path does not mean the node stopped owning the abandoned session.
func (c *ControlPlane) rememberAbandonedSessionLocked(active *activeSession) {
	c.rememberAbandonedLocked(active, false)
}

func (c *ControlPlane) rememberAbandonedLocked(active *activeSession, pendingOffer bool) bool {
	if active == nil || active.SessionID == "" || active.NodeID == "" {
		return false
	}
	c.pruneAbandonedSessionsLocked()
	if c.abandonedSessions == nil {
		c.abandonedSessions = make(map[string]*abandonedSession)
	}
	expires := active.ExpiresAt
	if expires.IsZero() {
		expires = time.Now().Add(2 * time.Minute)
	}
	if !expires.After(time.Now()) {
		return false
	}
	pendingForNode, confirmedCount, oldestConfirmed := 0, 0, ""
	for id, existing := range c.abandonedSessions {
		if id == active.SessionID {
			continue
		}
		if existing.PendingOffer {
			if existing.NodeID == active.NodeID {
				pendingForNode++
			}
			continue
		}
		confirmedCount++
		if oldestConfirmed == "" || existing.ExpiresAt.Before(c.abandonedSessions[oldestConfirmed].ExpiresAt) {
			oldestConfirmed = id
		}
	}
	if pendingOffer && pendingForNode >= maxPendingOffersPerNode {
		return false
	}
	// Confirmed old sessions remain globally bounded. Unconfirmed published
	// offers cannot be evicted: their late answers still require a release.
	if !pendingOffer && confirmedCount >= maxAbandonedSessions {
		delete(c.abandonedSessions, oldestConfirmed)
	}
	c.abandonedSessions[active.SessionID] = &abandonedSession{activeSession: activeSession{
		NodeID: active.NodeID, SessionID: active.SessionID, ExpiresAt: expires,
		ControlEndpoint: active.ControlEndpoint, ControlBinding: active.ControlBinding,
	}, PendingOffer: pendingOffer}
	return true
}

// Reserve a per-node cleanup slot before a potentially ambiguous provider
// write. A saturated node cannot force publication of an untracked offer or
// prevent another node from accepting an independent connection.
func (c *ControlPlane) reservePendingOffer(ctx context.Context, offer session.Offer, contact carriers.Endpoint, binding policy.CarrierBinding) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !c.rememberAbandonedLocked(&activeSession{NodeID: offer.TargetNodeID, SessionID: offer.SessionID,
		ExpiresAt: offer.ExpiresAt.Add(2 * time.Minute), ControlEndpoint: contact, ControlBinding: binding}, true) {
		return fmt.Errorf("pending offer cleanup capacity reached for node %s", offer.TargetNodeID)
	}
	return nil
}

// Retry the existing release protocol only when reconnecting to that SAME
// node, using the currently reachable control contact. Independent node
// failover never waits for an unreachable old node's release.
func (c *ControlPlane) releaseAbandonedSession(ctx context.Context, nodeID string, contact carriers.Endpoint, binding policy.CarrierBinding) error {
	c.mu.Lock()
	c.pruneAbandonedSessionsLocked()
	var pending []*abandonedSession
	for _, active := range c.abandonedSessions {
		if active.NodeID == nodeID && c.pending[active.SessionID] == nil && (c.active == nil || c.active.SessionID != active.SessionID) {
			pending = append(pending, active)
		}
	}
	c.mu.Unlock()
	sort.Slice(pending, func(i, j int) bool { return pending[i].SessionID < pending[j].SessionID })
	releaseCtx, cancel := context.WithTimeout(ctx, releaseSendTimeout)
	defer cancel()
	for _, active := range pending {
		if err := c.releaseAbandonedRecord(releaseCtx, active, contact, binding); err != nil {
			return err
		}
	}
	return nil
}

func (c *ControlPlane) releaseAbandonedRecord(ctx context.Context, active *abandonedSession, contact carriers.Endpoint, binding policy.CarrierBinding) error {
	c.mu.RLock()
	inUse := c.pending[active.SessionID] != nil || (c.active != nil && c.active.SessionID == active.SessionID)
	c.mu.RUnlock()
	if inUse || binding.Carrier == nil {
		return nil
	}
	if err := c.engine.SendRelease(ctx, binding.Carrier, contact, session.Release{SessionID: active.SessionID, ClientID: c.cfg.Identity(), NodeID: active.NodeID, Reason: "route_recovery"}); err != nil {
		return err
	}
	c.mu.Lock()
	if !active.PendingOffer && c.abandonedSessions[active.SessionID] == active {
		delete(c.abandonedSessions, active.SessionID)
	}
	c.mu.Unlock()
	return nil
}

// Cancellation must not wait for a provider request. Each cleanup is bounded
// and uses the existing exact-session release, which cannot revoke a new one.
func (c *ControlPlane) releaseAbandonedSessionAsync(nodeID string, contact carriers.Endpoint, binding policy.CarrierBinding) {
	if binding.Carrier == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), releaseSendTimeout)
		defer cancel()
		_ = c.releaseAbandonedSession(ctx, nodeID, contact, binding)
	}()
}

func (c *ControlPlane) releaseAbandonedAnswer(answer session.Answer) {
	c.mu.Lock()
	old := c.abandonedSessions[answer.SessionID]
	if old == nil || old.NodeID != answer.NodeID || (c.active != nil && c.active.SessionID == answer.SessionID) {
		c.mu.Unlock()
		return
	}
	confirmed := *old
	confirmed.PendingOffer = false
	if answer.ExpiresAt.After(time.Now()) && answer.ExpiresAt.Before(confirmed.ExpiresAt) {
		confirmed.ExpiresAt = answer.ExpiresAt
	}
	c.rememberAbandonedLocked(&confirmed.activeSession, false)
	retained := c.abandonedSessions[answer.SessionID]
	c.mu.Unlock()
	if retained == nil {
		return
	}
	// Capture this exact confirmation before scheduling. A burst of late
	// answers may evict a confirmed retry record, but must not skip its first
	// release attempt just because the bounded sweep no longer contains it.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), releaseSendTimeout)
		defer cancel()
		_ = c.releaseAbandonedRecord(ctx, retained, retained.ControlEndpoint, retained.ControlBinding)
	}()
}
