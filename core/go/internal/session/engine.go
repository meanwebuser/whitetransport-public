package session

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

const (
	PayloadNodeAdvertise = "node.advertise"
	PayloadNodeWithdraw  = "node.withdraw"
	PayloadNodeHeartbeat = "node.heartbeat"
	PayloadSessionOffer  = "session.offer"
	PayloadSessionAnswer = "session.answer"
	// PayloadSessionAnswerCompressed is emitted only for answers that exceed
	// the compact control budget. Older small answers retain session.answer.
	PayloadSessionAnswerCompressed = "session.answer.gzip"
	PayloadSessionAnswerChunk      = PayloadSessionAnswer + ".chunk"
	PayloadSessionAnswerGzipChunk  = PayloadSessionAnswerCompressed + ".chunk"
	PayloadSessionOfferAck         = "session.offer.ack"
	PayloadSessionRelease          = "session.release"
	PayloadSessionError            = "session.error"
)

const sessionAnswerCompressionThreshold = 1024

const (
	// sessionAnswerChunkPayloadBytes is deliberately below the nominal VK/OK
	// message budget: mailbox encoding encrypts and base64-wraps an envelope.
	sessionAnswerChunkPayloadBytes = 1024
	maxSessionAnswerChunks         = 64
	maxPendingSessionAnswerGroups  = 32
	maxSessionAnswerBytes          = maxSessionAnswerChunks * sessionAnswerChunkPayloadBytes
)

// Engine coordinates bootstrap and session messages over arbitrary carriers.
type Engine struct {
	identity       string
	answerChunks   map[string]*answerChunkGroup
	answerChunksMu sync.Mutex
}

type answerChunkGroup struct {
	baseID       string
	version      int
	sessionID    string
	source       string
	destination  string
	trafficClass fabric.TrafficClass
	payloadType  string
	chunkTotal   int
	ttl          time.Duration
	createdAt    time.Time
	chunks       map[int][]byte
	totalBytes   int
}

// NewEngine creates a session engine for a client or node identity.
func NewEngine(identity string) Engine {
	return Engine{identity: identity, answerChunks: make(map[string]*answerChunkGroup)}
}

// PublishAdvertisement writes a node advertisement to a bootstrap carrier.
func (e Engine) PublishAdvertisement(ctx context.Context, carrier carriers.Carrier, endpoint carriers.Endpoint, ad NodeAdvertisement) error {
	payload, err := EncodePayload(ad)
	if err != nil {
		return err
	}

	envelope := fabric.NewEnvelope(ad.NodeID+":advertise", fabric.TrafficBootstrap, PayloadNodeAdvertise, payload)
	envelope.Source = e.identity
	return carrier.Write(ctx, endpoint, envelope)
}

// PublishWithdrawal writes a node withdrawal to a bootstrap carrier, signalling
// that the node is no longer accepting new sessions.
func (e Engine) PublishWithdrawal(ctx context.Context, carrier carriers.Carrier, endpoint carriers.Endpoint, nodeID string) error {
	payload, err := EncodePayload(NodeWithdrawal{NodeID: nodeID})
	if err != nil {
		return err
	}
	envelope := fabric.NewEnvelope(nodeID+":withdraw", fabric.TrafficBootstrap, PayloadNodeWithdraw, payload)
	envelope.Source = e.identity
	return carrier.Write(ctx, endpoint, envelope)
}

// ReadAdvertisements reads node advertisements from a bootstrap carrier.
func (e Engine) ReadAdvertisements(ctx context.Context, carrier carriers.Carrier, endpoint carriers.Endpoint, cursor carriers.Cursor) ([]NodeAdvertisement, carriers.Cursor, error) {
	out, next, err := readPayloads[NodeAdvertisement](ctx, carrier, endpoint, cursor, PayloadNodeAdvertise)
	return out, next, err
}

// SendOffer writes a session offer to a node's selected contact endpoint.
// If the offer has a SessionKey and a bootstrapKey is provided, the key is
// encrypted with AES-256-GCM before sending.
func (e Engine) SendOffer(ctx context.Context, carrier carriers.Carrier, endpoint carriers.Endpoint, offer Offer) error {
	if offer.SessionID == "" {
		return errors.New("session offer requires session id")
	}

	payload, err := EncodePayload(offer)
	if err != nil {
		return err
	}

	envelope := fabric.NewEnvelope(offer.SessionID+":offer", fabric.TrafficControl, PayloadSessionOffer, payload)
	envelope.SessionID = offer.SessionID
	envelope.Source = e.identity
	return carrier.Write(ctx, endpoint, envelope)
}

// EncryptSessionKey encrypts a raw session key with the bootstrap cipher.
// Returns the encrypted key bytes suitable for inclusion in Offer.SessionKey.
func EncryptSessionKey(bootstrapCipher *fabric.EnvelopeCipher, sessionKey []byte) ([]byte, error) {
	if bootstrapCipher == nil {
		return sessionKey, nil
	}
	// We encrypt the raw key bytes by wrapping them in a minimal envelope.
	env := fabric.Envelope{
		Version:     1,
		ID:          "session-key",
		PayloadType: "session.key",
		Payload:     sessionKey,
		CreatedAt:   time.Now().UTC(),
	}
	return bootstrapCipher.Seal(env)
}

// DecryptSessionKey decrypts an encrypted session key from an offer.
func DecryptSessionKey(bootstrapCipher *fabric.EnvelopeCipher, encrypted []byte) ([]byte, error) {
	if bootstrapCipher == nil {
		return encrypted, nil
	}
	env, err := bootstrapCipher.Open(encrypted)
	if err != nil {
		return nil, err
	}
	return env.Payload, nil
}

// ReadOffers reads session offers from a control carrier.
func (e Engine) ReadOffers(ctx context.Context, carrier carriers.Carrier, endpoint carriers.Endpoint, cursor carriers.Cursor) ([]Offer, carriers.Cursor, error) {
	out, next, err := readPayloads[Offer](ctx, carrier, endpoint, cursor, PayloadSessionOffer)
	return out, next, err
}

// SendAnswer writes a session answer to a client's reply endpoint.
func (e *Engine) SendAnswer(ctx context.Context, carrier carriers.Carrier, endpoint carriers.Endpoint, answer Answer) error {
	if answer.SessionID == "" {
		return errors.New("session answer requires session id")
	}

	payload, err := EncodePayload(answer)
	if err != nil {
		return err
	}
	payloadType := PayloadSessionAnswer
	if len(payload) > sessionAnswerCompressionThreshold {
		payload, err = compressSessionAnswer(payload)
		if err != nil {
			return err
		}
		payloadType = PayloadSessionAnswerCompressed
	}

	if len(payload) <= sessionAnswerChunkPayloadBytes {
		return carrier.Write(ctx, endpoint, e.answerEnvelope(answer, payloadType, payload))
	}
	chunkTotal := (len(payload) + sessionAnswerChunkPayloadBytes - 1) / sessionAnswerChunkPayloadBytes
	if chunkTotal > maxSessionAnswerChunks {
		return fmt.Errorf("session answer requires %d chunks, exceeds limit %d", chunkTotal, maxSessionAnswerChunks)
	}
	for chunkIndex := 0; chunkIndex < chunkTotal; chunkIndex++ {
		start := chunkIndex * sessionAnswerChunkPayloadBytes
		end := min(start+sessionAnswerChunkPayloadBytes, len(payload))
		envelope := e.answerEnvelope(answer, payloadType+".chunk", payload[start:end])
		envelope.ID = fmt.Sprintf("%s:answer.%d", answer.SessionID, chunkIndex)
		envelope.ChunkIndex = chunkIndex
		envelope.ChunkTotal = chunkTotal
		if err := carrier.Write(ctx, endpoint, envelope); err != nil {
			return fmt.Errorf("write session answer chunk %d/%d: %w", chunkIndex+1, chunkTotal, err)
		}
	}
	return nil
}

// answerEnvelope constructs one authenticated answer frame before optional
// mailbox fragmentation.
func (e *Engine) answerEnvelope(answer Answer, payloadType string, payload []byte) fabric.Envelope {
	envelope := fabric.NewEnvelope(answer.SessionID+":answer", fabric.TrafficControl, payloadType, payload)
	envelope.SessionID = answer.SessionID
	envelope.Source = e.identity
	return envelope
}

// ReadAnswers reads session answers from a control carrier.
func (e *Engine) ReadAnswers(ctx context.Context, carrier carriers.Carrier, endpoint carriers.Endpoint, cursor carriers.Cursor) ([]Answer, carriers.Cursor, error) {
	read, err := carrier.Read(ctx, endpoint, cursor)
	if err != nil {
		return nil, cursor, err
	}
	answers := make([]Answer, 0, len(read.Envelopes))
	for _, envelope := range read.Envelopes {
		if err := envelope.Validate(); err != nil {
			return nil, read.Cursor, err
		}
		answer, handled, err := e.DecodeAnswerEnvelope(envelope)
		if err != nil {
			return nil, read.Cursor, err
		}
		if !handled {
			continue
		}
		answers = append(answers, answer)
	}
	return answers, read.Cursor, nil
}

// DecodeAnswerEnvelope reassembles a fragmented answer before decoding it.
// Incomplete groups are retained for a later mailbox poll and produce no
// answer until every authenticated fragment has arrived.
func (e *Engine) DecodeAnswerEnvelope(envelope fabric.Envelope) (Answer, bool, error) {
	if envelope.IsChunk() {
		if envelope.PayloadType != PayloadSessionAnswerChunk && envelope.PayloadType != PayloadSessionAnswerGzipChunk {
			return Answer{}, false, nil
		}
		if envelope.ChunkTotal > maxSessionAnswerChunks || envelope.ChunkIndex < 0 || envelope.ChunkIndex >= envelope.ChunkTotal {
			return Answer{}, true, fmt.Errorf("invalid session answer chunk %d/%d", envelope.ChunkIndex, envelope.ChunkTotal)
		}
		reassembled, complete, err := e.addAnswerChunk(envelope)
		if err != nil {
			return Answer{}, true, fmt.Errorf("reassemble session answer: %w", err)
		}
		if !complete {
			return Answer{}, false, nil
		}
		envelope = reassembled
	}
	return DecodeSessionAnswerPayload(envelope.PayloadType, envelope.Payload)
}

// addAnswerChunk collects a bounded, authenticated answer fragment group.
// It scopes each group by sender and session to prevent cross-session splicing.
func (e *Engine) addAnswerChunk(envelope fabric.Envelope) (fabric.Envelope, bool, error) {
	baseID, err := answerChunkBaseID(envelope)
	if err != nil {
		return fabric.Envelope{}, false, err
	}
	e.answerChunksMu.Lock()
	defer e.answerChunksMu.Unlock()
	e.evictExpiredAnswerChunks(time.Now().UTC())
	groupKey := envelope.Source + "\x00" + envelope.SessionID + "\x00" + baseID
	group := e.answerChunks[groupKey]
	if group == nil {
		if len(e.answerChunks) >= maxPendingSessionAnswerGroups {
			return fabric.Envelope{}, false, fmt.Errorf("too many incomplete session answers")
		}
		group = &answerChunkGroup{
			baseID: baseID, version: envelope.Version, sessionID: envelope.SessionID,
			source: envelope.Source, destination: envelope.Destination, trafficClass: envelope.TrafficClass,
			payloadType: envelope.PayloadType, chunkTotal: envelope.ChunkTotal, ttl: envelope.TTL, createdAt: envelope.CreatedAt,
			chunks: make(map[int][]byte, envelope.ChunkTotal),
		}
		e.answerChunks[groupKey] = group
	} else if !group.matches(envelope) {
		delete(e.answerChunks, groupKey)
		return fabric.Envelope{}, false, errors.New("conflicting session answer chunk metadata")
	}
	if existing, ok := group.chunks[envelope.ChunkIndex]; ok {
		if !bytes.Equal(existing, envelope.Payload) {
			delete(e.answerChunks, groupKey)
			return fabric.Envelope{}, false, fmt.Errorf("conflicting duplicate session answer chunk %d", envelope.ChunkIndex)
		}
		return fabric.Envelope{}, false, nil
	}
	if group.totalBytes+len(envelope.Payload) > maxSessionAnswerBytes {
		delete(e.answerChunks, groupKey)
		return fabric.Envelope{}, false, fmt.Errorf("session answer exceeds %d bytes", maxSessionAnswerBytes)
	}
	group.chunks[envelope.ChunkIndex] = append([]byte(nil), envelope.Payload...)
	group.totalBytes += len(envelope.Payload)
	if len(group.chunks) != envelope.ChunkTotal {
		return fabric.Envelope{}, false, nil
	}
	payload := make([]byte, 0, group.totalBytes)
	for index := 0; index < envelope.ChunkTotal; index++ {
		chunk, ok := group.chunks[index]
		if !ok {
			return fabric.Envelope{}, false, fmt.Errorf("missing session answer chunk %d", index)
		}
		payload = append(payload, chunk...)
	}
	delete(e.answerChunks, groupKey)
	return fabric.Envelope{
		Version: group.version, ID: group.baseID, SessionID: group.sessionID, Source: group.source,
		Destination: group.destination, TrafficClass: group.trafficClass,
		PayloadType: strings.TrimSuffix(group.payloadType, ".chunk"), TTL: group.ttl,
		CreatedAt: group.createdAt, Payload: payload,
	}, true, nil
}

func answerChunkBaseID(envelope fabric.Envelope) (string, error) {
	if envelope.ChunkTotal <= 0 || envelope.ChunkTotal > maxSessionAnswerChunks || envelope.ChunkIndex < 0 || envelope.ChunkIndex >= envelope.ChunkTotal {
		return "", fmt.Errorf("invalid session answer chunk %d/%d", envelope.ChunkIndex, envelope.ChunkTotal)
	}
	suffix := "." + strconv.Itoa(envelope.ChunkIndex)
	if !strings.HasSuffix(envelope.ID, suffix) || len(envelope.ID) == len(suffix) {
		return "", fmt.Errorf("invalid session answer chunk id %q", envelope.ID)
	}
	return strings.TrimSuffix(envelope.ID, suffix), nil
}

func (g *answerChunkGroup) matches(envelope fabric.Envelope) bool {
	return g.version == envelope.Version && g.sessionID == envelope.SessionID && g.source == envelope.Source &&
		g.destination == envelope.Destination && g.trafficClass == envelope.TrafficClass &&
		g.payloadType == envelope.PayloadType && g.chunkTotal == envelope.ChunkTotal && g.ttl == envelope.TTL
}

func (e *Engine) evictExpiredAnswerChunks(now time.Time) {
	for key, group := range e.answerChunks {
		if !group.createdAt.IsZero() && now.Sub(group.createdAt) > 2*time.Minute {
			delete(e.answerChunks, key)
		}
	}
}

// DecodeSessionAnswerPayload decodes both control-plane answer encodings and
// lets router-driven runtimes share the same wire contract as Engine readers.
func DecodeSessionAnswerPayload(payloadType string, payload []byte) (Answer, bool, error) {
	switch payloadType {
	case PayloadSessionAnswer:
	case PayloadSessionAnswerCompressed:
		var err error
		payload, err = decompressSessionAnswer(payload)
		if err != nil {
			return Answer{}, true, err
		}
	default:
		return Answer{}, false, nil
	}
	answer, err := DecodePayload[Answer](payload)
	if err != nil {
		return Answer{}, true, err
	}
	return answer, true, nil
}

func compressSessionAnswer(payload []byte) ([]byte, error) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(payload); err != nil {
		return nil, fmt.Errorf("compress session answer: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("finish session answer compression: %w", err)
	}
	return compressed.Bytes(), nil
}

func decompressSessionAnswer(payload []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("open compressed session answer: %w", err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		_ = reader.Close()
		return nil, fmt.Errorf("decompress session answer: %w", err)
	}
	if err := reader.Close(); err != nil {
		return nil, fmt.Errorf("close compressed session answer: %w", err)
	}
	return decoded, nil
}

// SendOfferAck writes a session offer acknowledgement back to the client.
func (e Engine) SendOfferAck(ctx context.Context, carrier carriers.Carrier, endpoint carriers.Endpoint, ack OfferAck) error {
	if ack.SessionID == "" {
		return errors.New("session offer ack needs session id")
	}
	payload, err := EncodePayload(ack)
	if err != nil {
		return err
	}
	envelope := fabric.NewEnvelope(ack.SessionID+":offer.ack", fabric.TrafficControl, PayloadSessionOfferAck, payload)
	envelope.SessionID = ack.SessionID
	envelope.Source = e.identity
	return carrier.Write(ctx, endpoint, envelope)
}

// SendRelease writes a best-effort client disconnect notice to the node's
// control endpoint so the node can free its busy slot immediately.
func (e Engine) SendRelease(ctx context.Context, carrier carriers.Carrier, endpoint carriers.Endpoint, release Release) error {
	if release.SessionID == "" {
		return errors.New("session release requires session id")
	}
	payload, err := EncodePayload(release)
	if err != nil {
		return err
	}
	envelope := fabric.NewEnvelope(release.SessionID+":release", fabric.TrafficControl, PayloadSessionRelease, payload)
	envelope.SessionID = release.SessionID
	envelope.Source = e.identity
	return carrier.Write(ctx, endpoint, envelope)
}

// SendSessionError writes a session error notification to inform the other
// party about a failure instead of relying on timeouts.
func (e Engine) SendSessionError(ctx context.Context, carrier carriers.Carrier, endpoint carriers.Endpoint, sessErr SessionError) error {
	if sessErr.SessionID == "" {
		return errors.New("session error requires session id")
	}
	payload, err := EncodePayload(sessErr)
	if err != nil {
		return err
	}
	envelope := fabric.NewEnvelope(sessErr.SessionID+":error", fabric.TrafficControl, PayloadSessionError, payload)
	envelope.SessionID = sessErr.SessionID
	envelope.Source = e.identity
	return carrier.Write(ctx, endpoint, envelope)
}

// ReadOfferAcks reads session offer acknowledgements from a control carrier.
func (e Engine) ReadOfferAcks(ctx context.Context, carrier carriers.Carrier, endpoint carriers.Endpoint, cursor carriers.Cursor) ([]OfferAck, carriers.Cursor, error) {
	out, next, err := readPayloads[OfferAck](ctx, carrier, endpoint, cursor, PayloadSessionOfferAck)
	return out, next, err
}

// PublishHeartbeat writes a node heartbeat to a bootstrap carrier.
func (e Engine) PublishHeartbeat(ctx context.Context, carrier carriers.Carrier, endpoint carriers.Endpoint, nodeID string) error {
	payload, err := EncodePayload(NodeHeartbeat{NodeID: nodeID, Timestamp: time.Now().UTC()})
	if err != nil {
		return err
	}
	envelope := fabric.NewEnvelope(nodeID+":heartbeat", fabric.TrafficHealth, PayloadNodeHeartbeat, payload)
	envelope.Source = e.identity
	return carrier.Write(ctx, endpoint, envelope)
}

func readPayloads[T any](ctx context.Context, carrier carriers.Carrier, endpoint carriers.Endpoint, cursor carriers.Cursor, payloadType string) ([]T, carriers.Cursor, error) {
	read, err := carrier.Read(ctx, endpoint, cursor)
	if err != nil {
		return nil, cursor, err
	}

	out := make([]T, 0, len(read.Envelopes))
	for _, envelope := range read.Envelopes {
		if err := envelope.Validate(); err != nil {
			return nil, read.Cursor, err
		}
		if envelope.PayloadType != payloadType {
			continue
		}
		value, err := DecodePayload[T](envelope.Payload)
		if err != nil {
			return nil, read.Cursor, err
		}
		out = append(out, value)
	}

	return out, read.Cursor, nil
}
