package punch

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

// PayloadTypes carried by punch signaling envelopes. They ride the bootstrap
// traffic class, so any carrier able to move control envelopes (ssh.fabric
// hub, VK/OK messages, file.mailbox) can also move punch signaling — the
// hub stays the signaling plane while data goes direct.
const (
	PayloadTypeOffer  = "punch.offer.v1"
	PayloadTypeAnswer = "punch.answer.v1"
)

const (
	signalPollInterval = 200 * time.Millisecond
	signalMaxOfferAge  = 2 * time.Minute
)

// FabricSignaling exchanges punch offers and answers as sealed fabric
// envelopes through any mailbox carrier. The shared token derives the
// AES-256-GCM key via fabric.DeriveBootstrapKey, so it doubles as the
// pairwise punch secret: candidates and the session key are never visible
// to the carrier or the hub relaying the envelopes.
type FabricSignaling struct {
	carrier carriers.Carrier
	cipher  *fabric.EnvelopeCipher
	selfID  string
	peerID  string

	poll        time.Duration
	maxOfferAge time.Duration
	log         func(format string, args ...any)

	mu     sync.Mutex
	cursor carriers.Cursor
}

// NewFabricSignaling builds signaling over carrier for the node pair
// selfID/peerID. Both sides construct it with the same token and mirrored
// ids.
func NewFabricSignaling(carrier carriers.Carrier, token, selfID, peerID string, logf func(string, ...any)) (*FabricSignaling, error) {
	if carrier == nil {
		return nil, fmt.Errorf("punch signal: carrier is required")
	}
	if token == "" {
		return nil, fmt.Errorf("punch signal: token is required")
	}
	if selfID == "" || peerID == "" {
		return nil, fmt.Errorf("punch signal: self and peer ids are required")
	}
	cipher, err := fabric.NewSessionCipher(fabric.DeriveBootstrapKey(token))
	if err != nil {
		return nil, fmt.Errorf("punch signal: cipher: %w", err)
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &FabricSignaling{
		carrier:     carrier,
		cipher:      cipher,
		selfID:      selfID,
		peerID:      peerID,
		poll:        signalPollInterval,
		maxOfferAge: signalMaxOfferAge,
		log:         logf,
	}, nil
}

// mailboxFor names the per-node mailbox that carries punch signaling. The
// "punch-" prefix keeps it apart from control-plane mailboxes on the same
// carrier.
func mailboxFor(nodeID string) carriers.Endpoint {
	return carriers.Endpoint{ID: nodeID, Address: "punch-" + nodeID}
}

func randomEnvelopeID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "punch-" + hex.EncodeToString(b)
}

// send seals payload as an inner envelope and writes the outer envelope to
// the peer's mailbox.
func (f *FabricSignaling) send(ctx context.Context, payloadType string, payload []byte) error {
	inner := fabric.NewEnvelope(randomEnvelopeID(), fabric.TrafficBootstrap, payloadType, payload)
	sealed, err := f.cipher.Seal(inner)
	if err != nil {
		return fmt.Errorf("punch signal: seal: %w", err)
	}
	outer := fabric.NewEnvelope(randomEnvelopeID(), fabric.TrafficBootstrap, payloadType, sealed)
	outer.Source = f.selfID
	outer.Destination = f.peerID
	if err := outer.Validate(); err != nil {
		return fmt.Errorf("punch signal: envelope: %w", err)
	}
	return f.carrier.Write(ctx, mailboxFor(f.peerID), outer)
}

// pollFor returns the inner payload of the next envelope matching
// payloadType (and sessionID when non-nil). Unrelated envelopes are
// skipped; the read cursor advances past everything consumed.
func (f *FabricSignaling) pollFor(ctx context.Context, payloadType string, sessionID []byte) ([]byte, error) {
	own := mailboxFor(f.selfID)
	for {
		res, err := f.carrier.Read(ctx, own, f.currentCursor())
		if err != nil {
			return nil, fmt.Errorf("punch signal: read: %w", err)
		}
		f.setCursor(res.Cursor)
		for _, env := range res.Envelopes {
			if env.PayloadType != payloadType {
				continue
			}
			if env.Destination != "" && env.Destination != f.selfID {
				continue
			}
			inner, err := f.cipher.Open(env.Payload)
			if err != nil {
				f.log("punch signal: cannot open %s envelope: %v", payloadType, err)
				continue
			}
			if sessionID != nil {
				var probe struct {
					SessionID []byte `json:"session_id"`
				}
				if err := json.Unmarshal(inner.Payload, &probe); err != nil || !bytes.Equal(probe.SessionID, sessionID) {
					continue // answer belonging to a different punch session
				}
			}
			return inner.Payload, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(f.poll):
		}
	}
}

func (f *FabricSignaling) currentCursor() carriers.Cursor {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cursor
}

func (f *FabricSignaling) setCursor(c carriers.Cursor) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cursor = c
}

// SendOffer seals and writes one punch offer.
func (f *FabricSignaling) SendOffer(ctx context.Context, offer Offer) error {
	payload, err := json.Marshal(offer)
	if err != nil {
		return fmt.Errorf("punch signal: marshal offer: %w", err)
	}
	return f.send(ctx, PayloadTypeOffer, payload)
}

// RecvOffer waits for the next fresh offer, dropping stale ones.
func (f *FabricSignaling) RecvOffer(ctx context.Context) (Offer, error) {
	for {
		payload, err := f.pollFor(ctx, PayloadTypeOffer, nil)
		if err != nil {
			return Offer{}, err
		}
		var offer Offer
		if err := json.Unmarshal(payload, &offer); err != nil {
			return Offer{}, fmt.Errorf("punch signal: unmarshal offer: %w", err)
		}
		if age := time.Since(offer.CreatedAt); age > f.maxOfferAge {
			f.log("punch signal: dropping stale offer (age %s)", age.Truncate(time.Second))
			continue
		}
		return offer, nil
	}
}

// SendAnswer seals and writes one punch answer.
func (f *FabricSignaling) SendAnswer(ctx context.Context, answer Answer) error {
	payload, err := json.Marshal(answer)
	if err != nil {
		return fmt.Errorf("punch signal: marshal answer: %w", err)
	}
	return f.send(ctx, PayloadTypeAnswer, payload)
}

// RecvAnswer waits for the answer matching sessionID.
func (f *FabricSignaling) RecvAnswer(ctx context.Context, sessionID []byte) (Answer, error) {
	payload, err := f.pollFor(ctx, PayloadTypeAnswer, sessionID)
	if err != nil {
		return Answer{}, err
	}
	var answer Answer
	if err := json.Unmarshal(payload, &answer); err != nil {
		return Answer{}, fmt.Errorf("punch signal: unmarshal answer: %w", err)
	}
	return answer, nil
}
