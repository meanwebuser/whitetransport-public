package tunnel

import (
	"context"
	"log"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/session"
)

type egressFailureStream struct {
	sessionID string
	fail      func()
}

func (t *CarrierTunnel) SetEgressFailureReply(sessionID string, carrier carriers.Carrier, endpoint carriers.Endpoint) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if lease := t.packetSessions[sessionID]; lease != nil && !lease.closing {
		lease.failureReply = policy.CarrierBinding{Carrier: carrier, Endpoint: endpoint}
	}
}

func (t *CarrierTunnel) watchEgressFailure(sessionID, streamID string, fail func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if lease := t.packetSessions[sessionID]; lease != nil && !lease.closing && t.sessionCiphers[sessionID] != nil {
		t.failureStreams[streamID] = egressFailureStream{sessionID: sessionID, fail: fail}
	}
}

func (t *CarrierTunnel) unwatchEgressFailure(streamID string) {
	t.mu.Lock()
	delete(t.failureStreams, streamID)
	t.mu.Unlock()
}

func (t *CarrierTunnel) prepareEgressFailure(ctx context.Context, sessionID, streamID, peerID string) (fabric.Envelope, policy.CarrierBinding) {
	t.mu.Lock()
	lease := t.packetSessions[sessionID]
	cipher := t.sessionCiphers[sessionID]
	if lease == nil || lease.closing || lease.peerID != peerID || packetSessionExpired(lease.expiresAt) || cipher == nil {
		t.mu.Unlock()
		return fabric.Envelope{}, policy.CarrierBinding{}
	}
	reply := lease.failureReply
	t.mu.Unlock()
	if reply.Carrier == nil || ctx.Err() != nil {
		return fabric.Envelope{}, policy.CarrierBinding{}
	}

	notice := fabric.NewEnvelope(streamID, fabric.TrafficControl, session.PayloadEgressStreamFailure, nil)
	notice.SessionID = sessionID
	notice.Source = t.identity
	notice.Destination = peerID
	sealed, err := cipher.Seal(notice)
	if err != nil {
		return fabric.Envelope{}, policy.CarrierBinding{}
	}
	notice.Payload = sealed
	return notice, reply
}

func (t *CarrierTunnel) sendEgressFailure(ctx context.Context, reply policy.CarrierBinding, notice fabric.Envelope) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := reply.Carrier.Write(ctx, reply.Endpoint, notice); err != nil && ctx.Err() == nil {
		log.Print("[tunnel] unable to deliver exit write failure over control")
	}
}

// HandleEgressStreamFailure accepts only a sealed notice from the owning
// session peer. Its authenticated fields must match the control envelope.
func (t *CarrierTunnel) HandleEgressStreamFailure(notice fabric.Envelope) {
	if notice.PayloadType != session.PayloadEgressStreamFailure || notice.TrafficClass != fabric.TrafficControl || notice.Destination != t.identity {
		return
	}
	t.mu.Lock()
	stream, ok := t.failureStreams[notice.ID]
	lease := t.packetSessions[notice.SessionID]
	cipher := t.sessionCiphers[notice.SessionID]
	if !ok || stream.sessionID != notice.SessionID || lease == nil || lease.closing || lease.peerID != notice.Source || packetSessionExpired(lease.expiresAt) || cipher == nil {
		t.mu.Unlock()
		return
	}
	t.mu.Unlock()

	inner, err := cipher.Open(notice.Payload)
	if err != nil || inner.ID != notice.ID || inner.SessionID != notice.SessionID || inner.Source != notice.Source || inner.Destination != notice.Destination || inner.PayloadType != notice.PayloadType || inner.TrafficClass != notice.TrafficClass {
		return
	}
	t.mu.Lock()
	_, stillWatching := t.failureStreams[notice.ID]
	if !stillWatching || t.packetSessions[notice.SessionID] != lease || t.sessionCiphers[notice.SessionID] != cipher {
		t.mu.Unlock()
		return
	}
	delete(t.failureStreams, notice.ID)
	t.mu.Unlock()
	stream.fail()
}
