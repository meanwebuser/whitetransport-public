package runtime

import (
	"context"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/pkg/runtimeapi"
)

var errRequestedTransportUnavailable = errors.New("selected egress transport unavailable on this node")

func matchingEgressEndpoints(endpoints []carriers.Endpoint, family string) []carriers.Endpoint {
	return slices.DeleteFunc(endpoints, func(endpoint carriers.Endpoint) bool {
		return runtimeapi.EgressTransportFamily(endpoint.Carrier) != family
	})
}

type clientRoomCandidatesContextKey struct{}

// Preserve the control negotiation window after bounded room preparation.
// The same candidate snapshot drives setup, so eligibility changes cannot
// add unbudgeted providers halfway through this recovery attempt.
func (c *ControlPlane) connectionRecoveryContext(parent context.Context) (context.Context, context.CancelFunc) {
	budget := nodeReselectionTimeout
	if c.cfg.ClientRoomCreation {
		candidates := c.clientRoomEgressCandidates()
		budget += time.Duration(len(candidates)) * clientRoomSetupTimeout
		parent = context.WithValue(parent, clientRoomCandidatesContextKey{}, candidates)
	}
	return context.WithTimeout(parent, budget)
}

// Retained control mailboxes survive process restart; their old, still-unexpired
// offers must not reserve the new process for a client that already abandoned
// that session. The client intent loop sends a fresh offer after discovery.
func (c *ControlPlane) offerPredatesNodeStart(envelope fabric.Envelope) bool {
	c.mu.RLock()
	started := c.controlStartedAt
	c.mu.RUnlock()
	return !started.IsZero() && !envelope.CreatedAt.IsZero() && envelope.CreatedAt.Before(started)
}

// A successful HTTP request is shorter-lived than the user's Connect intent.
// Only explicit Disconnect/Stop cancels that intent; transient sessions may end.
func (c *ControlPlane) cancelConnectionIntent() {
	c.mu.Lock()
	c.connectionIntentRevision++
	if c.clientIntentCancel != nil {
		c.clientIntentCancel()
	}
	c.clientIntentCancel = nil
	c.clientIntentCtx = nil
	c.clientIntentPolicy = runtimeapi.ConnectionPolicy{}
	c.connectionRetryAt = time.Time{}
	c.connectionRetryDelay = 0
	c.clientEndpointPinned = false
	c.mu.Unlock()
}

func (c *ControlPlane) connectionLifetimeContext(fallback context.Context) context.Context {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.clientIntentCtx != nil {
		return c.clientIntentCtx
	}
	return fallback
}

// Mutex wait must respect a SOCKS/request deadline while another caller heals.
func (c *ControlPlane) lockNodeRecovery(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if c.nodeAutoHealMu.TryLock() {
			return nil
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// recoverConnectionIntent runs from the existing daemon recovery loop, not an
// application request. One bounded attempt per backoff interval is sufficient.
func (c *ControlPlane) recoverConnectionIntent() {
	if !c.nodeAutoHealMu.TryLock() {
		return
	}
	defer c.nodeAutoHealMu.Unlock()
	c.mu.Lock()
	intent := c.clientIntentCtx
	c.pruneAbandonedSessionsLocked()
	if intent == nil || intent.Err() != nil || c.clientEndpointPinned || time.Now().Before(c.connectionRetryAt) {
		c.mu.Unlock()
		return
	}
	active := c.active
	if active != nil {
		if active.SelectedEgressEndpointID != "" {
			c.mu.Unlock()
			return
		}
		// Advertised but locally unsupported routes cannot keep a failed
		// session alive after every usable data route has been quarantined.
		exhausted := c.connectionCarrierFailed && c.egressRecovery != nil
		for _, ep := range c.rankedUsableEgressEndpoints(active) {
			if c.egressRecovery == nil || c.egressRecovery.CanDial(policy.EgressEndpointKey(ep)) {
				exhausted = false
				break
			}
		}
		if !exhausted && !c.connectionLivenessFailed {
			c.connectionRetryDelay = 0
			c.mu.Unlock()
			return
		}
	}
	attempt := c.reconnectAttempts
	pinnedNode := c.clientIntentPolicy.NodeID
	c.mu.Unlock()
	ctx, cancel := c.connectionRecoveryContext(intent)
	defer cancel()
	var err error
	if active != nil {
		_, _, err = c.reselectNodeAfterCarrierExhaustionLocked(ctx, active, "")
	} else if pinnedNode != "" {
		_, err = c.connectWithFailover(ctx, pinnedNode)
	} else {
		// A failed node is excluded only by the immediate replacement attempt.
		// Persistent recovery must revisit it after restart. Rotate bounded
		// attempts so a retained advertisement for a dead node cannot starve
		// another candidate; fresh advertisements reset the rotation below.
		nodes := c.ListNodes()
		sort.SliceStable(nodes, func(i, j int) bool { return nodes[i].LastSeenAt.After(nodes[j].LastSeenAt) })
		selected := ""
		if len(nodes) > 0 {
			selected = nodes[attempt%len(nodes)].NodeID
		}
		_, err = c.connectWithFailoverExcluding(ctx, selected, "")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.clientIntentCtx != intent || intent.Err() != nil {
		return
	}
	if err == nil {
		c.connectionRetryDelay = 0
		c.connectionRetryAt = time.Time{}
		return
	}
	if c.connectionRetryDelay == 0 {
		c.connectionRetryDelay = time.Second
	} else {
		c.connectionRetryDelay *= 2
	}
	if c.connectionRetryDelay > 15*time.Second {
		c.connectionRetryDelay = 15 * time.Second
	}
	c.connectionRetryAt = time.Now().Add(c.connectionRetryDelay)
	c.reconnectAttempts++
}
