package runtime

import (
	"context"
	"errors"
	"fmt"
	goruntime "runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/session"
)

func TestLateAnswerBurstReleasesEveryConfirmedSnapshot(t *testing.T) {
	previous := goruntime.GOMAXPROCS(1)
	defer goruntime.GOMAXPROCS(previous)
	ep := carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://late-burst"}
	carrier := &busyRetryRecordingCarrier{memoryCarrier: newMemoryCarrierWithDescriptor(t, "late-burst", ep.Carrier), releases: make(chan string, 32)}
	binding := policy.CarrierBinding{Carrier: carrier, Endpoint: ep}
	c := &ControlPlane{cfg: config.Config{Role: config.RoleClient, ClientID: "burst-client"}, engine: session.NewEngine("burst-client")}
	wanted := make(map[string]bool)
	for i := range 24 {
		id, node := fmt.Sprintf("offer-%d", i), fmt.Sprintf("node-%d", i)
		c.rememberAbandonedLocked(&activeSession{NodeID: node, SessionID: id, ExpiresAt: time.Now().Add(time.Minute), ControlEndpoint: ep, ControlBinding: binding}, true)
		wanted[id] = true
	}
	for i := range 24 {
		c.releaseAbandonedAnswer(session.Answer{SessionID: fmt.Sprintf("offer-%d", i), NodeID: fmt.Sprintf("node-%d", i), ExpiresAt: time.Now().Add(time.Minute)})
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for len(wanted) > 0 {
		select {
		case id := <-carrier.releases:
			delete(wanted, id)
		case <-timer.C:
			t.Fatalf("confirmed snapshot evicted before release attempt: %v", wanted)
		}
	}
}

type delayedOfferCarrier struct {
	*memoryCarrier
	mu           sync.Mutex
	offer        *fabric.Envelope
	offerWritten chan struct{}
	releases     chan session.Release
}

type busyRetryRecordingCarrier struct {
	*memoryCarrier
	offers   chan string
	releases chan string
	cancel   context.CancelFunc
	count    int
}

type offerCapacityCarrier struct {
	*memoryCarrier
	mu     sync.Mutex
	queued []session.Offer
}

func (c *offerCapacityCarrier) Write(ctx context.Context, ep carriers.Endpoint, envelope fabric.Envelope) error {
	if envelope.PayloadType == session.PayloadSessionOffer {
		offer, err := session.DecodePayload[session.Offer](envelope.Payload)
		if err != nil {
			return err
		}
		if offer.TargetNodeID != "healthy-node" {
			c.mu.Lock()
			c.queued = append(c.queued, offer)
			c.mu.Unlock()
			// The provider accepted the write but its receipt was lost.
			return errors.New("ambiguous provider write failure")
		}
	}
	return c.memoryCarrier.Write(ctx, ep, envelope)
}

func TestPendingOfferCapacityBackpressuresBeforePublishAndPreservesOtherNodes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ep := carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://offer-capacity"}
	carrier := &offerCapacityCarrier{memoryCarrier: newMemoryCarrierWithDescriptor(t, "offer-capacity", ep.Carrier)}
	binding := policy.CarrierBinding{Carrier: carrier, Endpoint: ep}
	egressEP := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "memory://capacity-egress"}
	bindings := map[string]policy.CarrierBinding{ep.Carrier: binding, egressEP.Carrier: {Carrier: newMemoryCarrierWithDescriptor(t, "capacity-egress", egressEP.Carrier), Endpoint: egressEP}}
	client, err := newTestControlPlane(config.Config{Role: config.RoleClient, ClientID: "capacity-client", SocksListen: "127.0.0.1:0"}, bindings, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Stop()
	for range 8 {
		if _, err := client.connectOnceWithContact(ctx, "blocked-node", ep, binding); err == nil {
			t.Fatal("expected ambiguous write failure")
		}
	}
	if _, err := client.connectOnceWithContact(ctx, "blocked-node", ep, binding); err == nil {
		t.Fatal("full pending quota must reject publication")
	}
	carrier.mu.Lock()
	published := len(carrier.queued)
	carrier.mu.Unlock()
	if published != 8 {
		t.Fatalf("published%d offers before quota rejection, want8 tracked offers", published)
	}
	for range 2 {
		if _, err := client.connectOnceWithContact(ctx, "another-delayed-node", ep, binding); err == nil {
			t.Fatal("expected delayed provider receipt")
		}
	}
	carrier.mu.Lock()
	queued := append([]session.Offer(nil), carrier.queued...)
	carrier.mu.Unlock()
	if len(queued) != 10 {
		t.Fatal("one exhausted node prevented independent node admission")
	}
	client.mu.RLock()
	for _, offer := range queued {
		if client.abandonedSessions[offer.SessionID] == nil {
			t.Errorf("unconfirmed published offer was evicted: %s", offer.SessionID)
		}
	}
	client.mu.RUnlock()
	if t.Failed() {
		return
	}
	healthy, err := newTestControlPlane(config.Config{Role: config.RoleNode, NodeID: "healthy-node", SocksListen: "127.0.0.1:0"}, bindings, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer healthy.Stop()
	healthy.Start(ctx)
	client.Start(ctx)
	waitForNodeVisible(t, client, "healthy-node", true)
	if _, err := client.Connect(ctx, "healthy-node"); err != nil {
		t.Fatalf("blocked node quota prevented healthy connection: %v", err)
	}
	active := client.activeSessionSnapshot()
	for _, offer := range queued {
		client.handleAnswer(session.Answer{SessionID: offer.SessionID, NodeID: offer.TargetNodeID, ExpiresAt: time.Now().Add(time.Minute)})
		deadline := time.Now().Add(time.Second)
		for {
			client.mu.RLock()
			retained := client.abandonedSessions[offer.SessionID] != nil
			client.mu.RUnlock()
			if !retained {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("late accepted offer did not complete cleanup")
			}
			time.Sleep(time.Millisecond)
		}
	}
	if current := client.activeSessionSnapshot(); current == nil || current.SessionID != active.SessionID {
		t.Fatal("late cleanup revoked the healthy active session")
	}
}

func (c *busyRetryRecordingCarrier) Write(ctx context.Context, ep carriers.Endpoint, envelope fabric.Envelope) error {
	if err := c.memoryCarrier.Write(ctx, ep, envelope); err != nil {
		return err
	}
	if envelope.PayloadType == session.PayloadSessionOffer {
		c.count++ // Offers are dispatched serially by one Connect call.
		c.offers <- envelope.SessionID
		engine := session.NewEngine("retry-node")
		if err := engine.SendOfferAck(ctx, c.memoryCarrier, ep, session.OfferAck{SessionID: envelope.SessionID, Status: "busy"}); err != nil {
			return err
		}
		if c.count == 3 {
			c.cancel()
		}
	}
	if envelope.PayloadType == session.PayloadSessionRelease {
		c.releases <- envelope.SessionID
	}
	return nil
}

func TestCancelledBusyRetryCleansEverySentOfferID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, cancelRequest := context.WithCancel(ctx)
	defer cancelRequest()
	ep := carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://busy-retry-cleanup"}
	carrier := &busyRetryRecordingCarrier{memoryCarrier: newMemoryCarrierWithDescriptor(t, "busy-retry-cleanup", ep.Carrier), offers: make(chan string, 10), releases: make(chan string, 10), cancel: cancelRequest}
	binding := policy.CarrierBinding{Carrier: carrier, Endpoint: ep}
	client, err := newTestControlPlane(config.Config{Role: config.RoleClient, ClientID: "retry-client", SocksListen: "127.0.0.1:0"}, map[string]policy.CarrierBinding{ep.Carrier: binding}, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Stop()
	client.busyRetryAfter = time.Millisecond
	client.Start(ctx)
	client.storeAdvertisement(session.NodeAdvertisement{NodeID: "retry-node", Carriers: []carriers.Endpoint{ep}})
	if _, err := client.Connect(request, "retry-node"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Connect error=%v, want canceled", err)
	}
	want := make(map[string]bool)
	for range 3 {
		select {
		case id := <-carrier.offers:
			want[id] = true
		default:
			t.Fatal("expected three distinct sent retry offers")
		}
	}
	if len(want) != 3 {
		t.Fatal("retry reused a session ID")
	}
	client.mu.RLock()
	pending := len(client.pending) + len(client.pendingErrors) + len(client.pendingAcks) + len(client.pendingTargetNodes)
	client.mu.RUnlock()
	if pending != 0 {
		t.Fatalf("cancelled retry leaked%d pending channel entries", pending)
	}
	for range 3 {
		select {
		case id := <-carrier.releases:
			if !want[id] {
				t.Fatalf("unexpected release session=%s", id)
			}
			delete(want, id)
		case <-ctx.Done():
			t.Fatalf("unreleased retry IDs=%v", want)
		}
	}
	// A request cancelled before dispatch must not invent another abandoned
	// offer or send a release for a session the node never saw.
	client.mu.Lock()
	client.abandonedSessions = nil
	client.mu.Unlock()
	if _, err := client.connectOnceWithContact(request, "retry-node", ep, binding); !errors.Is(err, context.Canceled) {
		t.Fatal("pre-cancelled connect did not stop")
	}
	client.mu.RLock()
	abandoned := len(client.abandonedSessions)
	client.mu.RUnlock()
	if abandoned != 0 {
		t.Fatal("pre-dispatch cancellation retained an unsent offer")
	}
}

func (c *delayedOfferCarrier) Write(ctx context.Context, ep carriers.Endpoint, envelope fabric.Envelope) error {
	if envelope.PayloadType == session.PayloadSessionOffer {
		c.mu.Lock()
		if c.offer == nil {
			copy := envelope
			c.offer = &copy
			close(c.offerWritten)
			c.mu.Unlock()
			return nil
		}
		c.mu.Unlock()
	}
	if err := c.memoryCarrier.Write(ctx, ep, envelope); err != nil {
		return err
	}
	if envelope.PayloadType == session.PayloadSessionRelease {
		release, err := session.DecodePayload[session.Release](envelope.Payload)
		if err == nil {
			select {
			case c.releases <- release:
			default:
			}
		}
	}
	return nil
}

func TestCancelledOfferReleaseOvertakingOfferIsRetriedOnLateAnswer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ep := carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://reordered-offer"}
	carrier := &delayedOfferCarrier{memoryCarrier: newMemoryCarrierWithDescriptor(t, "reordered-offer", ep.Carrier), offerWritten: make(chan struct{}), releases: make(chan session.Release, 10)}
	egressEP := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "memory://reordered-egress"}
	bindings := map[string]policy.CarrierBinding{ep.Carrier: {Carrier: carrier, Endpoint: ep}, egressEP.Carrier: {Carrier: newMemoryCarrierWithDescriptor(t, "reordered-egress", egressEP.Carrier), Endpoint: egressEP}}
	node, err := newTestControlPlane(config.Config{Role: config.RoleNode, NodeID: "reordered-node", SocksListen: "127.0.0.1:0"}, bindings, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}
	client, err := newTestControlPlane(config.Config{Role: config.RoleClient, ClientID: "reordered-client", SocksListen: "127.0.0.1:0"}, bindings, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer node.Stop()
	defer client.Stop()
	node.Start(ctx)
	client.Start(ctx)
	waitForNodeVisible(t, client, "reordered-node", true)
	result := make(chan error, 1)
	go func() { _, err := client.Connect(ctx, "reordered-node"); result <- err }()
	select {
	case <-carrier.offerWritten:
	case <-ctx.Done():
		t.Fatal("offer not dispatched")
	}
	client.Disconnect()
	select {
	case <-result:
	case <-ctx.Done():
		t.Fatal("cancelled connect did not return")
	}
	select {
	case <-carrier.releases:
	case <-ctx.Done():
		t.Fatal("cancelled offer not released")
	}
	carrier.mu.Lock()
	offer := *carrier.offer
	carrier.mu.Unlock()
	client.mu.RLock()
	retained := client.abandonedSessions[offer.SessionID]
	client.mu.RUnlock()
	if retained == nil {
		t.Fatal("release preceding the offer discarded its future cleanup")
	}
	// Mailbox ordering now guarantees release precedes offer, even if neither
	// has been polled yet. The node must release its later acceptance too.
	if err := carrier.memoryCarrier.Write(ctx, ep, offer); err != nil {
		t.Fatal(err)
	}
	select {
	case release := <-carrier.releases:
		if release.SessionID != offer.SessionID {
			t.Fatal("late cleanup released a different session")
		}
	case <-ctx.Done():
		t.Fatal("late accepted answer did not trigger another release")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		node.mu.RLock()
		busy := node.nodeBusy
		node.mu.RUnlock()
		if !busy {
			if client.Status().SessionActive {
				t.Fatal("late answer resurrected cancelled connection")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("late accepted offer kept node busy")
}

func TestAbandonedOfferIDsDoNotOverwriteOrReleaseNewActiveSession(t *testing.T) {
	ep := carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://offer-identities"}
	carrier := &delayedOfferCarrier{memoryCarrier: newMemoryCarrierWithDescriptor(t, "offer-identities", ep.Carrier), releases: make(chan session.Release, 10)}
	binding := policy.CarrierBinding{Carrier: carrier, Endpoint: ep}
	c := &ControlPlane{cfg: config.Config{Role: config.RoleClient, ClientID: "identity-client"}, engine: session.NewEngine("identity-client")}
	for _, id := range []string{"old-1", "old-2", "current", "in-flight"} {
		c.rememberAbandonedLocked(&activeSession{NodeID: "node", SessionID: id, ExpiresAt: time.Now().Add(time.Minute), ControlEndpoint: ep, ControlBinding: binding}, true)
	}
	c.active = &activeSession{NodeID: "node", SessionID: "current"}
	c.pending = map[string]chan session.Answer{"in-flight": make(chan session.Answer, 1)}
	if err := c.releaseAbandonedSession(context.Background(), "node", ep, binding); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"old-1", "old-2"} {
		select {
		case got := <-carrier.releases:
			if got.SessionID != want {
				t.Fatalf("release=%q want=%q", got.SessionID, want)
			}
		default:
			t.Fatal("a sent offer ID was overwritten")
		}
	}
	c.releaseAbandonedAnswer(session.Answer{SessionID: "old-1", NodeID: "foreign"})
	c.releaseAbandonedAnswer(session.Answer{SessionID: "current", NodeID: "node"})
	select {
	case got := <-carrier.releases:
		t.Fatalf("unexpected release after unrelated/active answer: %+v", got)
	default:
	}
}

type clientOutageCarrier struct {
	*memoryCarrier
	offline      atomic.Bool
	failedWrites atomic.Int32
	releases     atomic.Int32
}

func (c *clientOutageCarrier) Write(ctx context.Context, ep carriers.Endpoint, envelope fabric.Envelope) error {
	if envelope.Source == "outage-client" && c.offline.Load() {
		c.failedWrites.Add(1)
		return errors.New("client network unavailable")
	}
	if envelope.PayloadType == session.PayloadSessionRelease {
		c.releases.Add(1)
	}
	return c.memoryCarrier.Write(ctx, ep, envelope)
}

func TestClientNetworkReturnReleasesAbandonedLiveNodeSession(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		name := "recover"
		if disconnect {
			name = "cancel"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			ep := carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://client-outage"}
			control := &clientOutageCarrier{memoryCarrier: newMemoryCarrierWithDescriptor(t, "client-outage", ep.Carrier)}
			egressEP := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "memory://outage-egress"}
			bindings := map[string]policy.CarrierBinding{ep.Carrier: {Carrier: control, Endpoint: ep}, egressEP.Carrier: {Carrier: newMemoryCarrierWithDescriptor(t, "outage-egress", egressEP.Carrier), Endpoint: egressEP}}
			node, err := newTestControlPlane(config.Config{Role: config.RoleNode, NodeID: "live-node", SocksListen: "127.0.0.1:0"}, bindings, policy.DefaultAdaptivePolicy(), nil)
			if err != nil {
				t.Fatal(err)
			}
			client, err := newTestControlPlane(config.Config{Role: config.RoleClient, ClientID: "outage-client", SocksListen: "127.0.0.1:0"}, bindings, policy.DefaultAdaptivePolicy(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Stop()
			defer node.Stop()
			node.Start(ctx)
			client.Start(ctx)
			waitForNodeVisible(t, client, "live-node", true)
			if _, err := client.Connect(ctx, "live-node"); err != nil {
				t.Fatal(err)
			}
			old := client.activeSessionSnapshot()
			control.offline.Store(true)
			if _, _, err := client.reselectNodeAfterCarrierExhaustion(ctx, old, ""); err == nil {
				t.Fatal("expected no independent replacement")
			}
			deadline := time.Now().Add(2 * time.Second)
			for control.failedWrites.Load() == 0 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if control.failedWrites.Load() == 0 {
				t.Fatal("outage did not reach background attempt")
			}
			node.mu.RLock()
			stillBusy := node.nodeBusy && node.nodeSessionID == old.SessionID
			node.mu.RUnlock()
			if !stillBusy {
				t.Fatal("node must retain the abandoned session throughout client outage")
			}
			if disconnect {
				client.Disconnect()
			}
			control.offline.Store(false)
			deadline = time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				status := client.Status()
				if disconnect && status.SessionActive {
					t.Fatal("cancelled recovery resurrected session")
				}
				if !disconnect && status.SessionActive && status.SessionID != old.SessionID {
					if control.releases.Load() != 1 {
						t.Fatalf("old-session releases=%d, want1", control.releases.Load())
					}
					client.mu.RLock()
					pending := len(client.abandonedSessions)
					client.mu.RUnlock()
					if pending != 0 {
						t.Fatalf("successful release retained %d abandoned sessions", pending)
					}
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
			if !disconnect {
				t.Fatalf("network returned but live node remains busy with old session: %+v", client.Status())
			}
		})
	}
}

func TestAbandonedSessionRetentionIsBoundedByCountAndLease(t *testing.T) {
	c := &ControlPlane{}
	for i := 0; i < 100; i++ {
		c.rememberAbandonedSessionLocked(&activeSession{NodeID: fmt.Sprint(i), SessionID: fmt.Sprint(i), ExpiresAt: time.Now().Add(time.Minute)})
	}
	if len(c.abandonedSessions) != maxAbandonedSessions {
		t.Fatalf("retained session count=%d", len(c.abandonedSessions))
	}
	for _, pending := range c.abandonedSessions {
		pending.ExpiresAt = time.Now().Add(-time.Second)
	}
	c.pruneAbandonedSessionsLocked()
	if len(c.abandonedSessions) != 0 {
		t.Fatal("expired leases retained release work")
	}
}
