package carriers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

const (
	mailboxMessagePrefix   = "wtmsg1."
	encryptedMessagePrefix = "wtenc1."
)

// HTTPDoer is the small subset of http.Client used by provider adapters.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// VKMessagesConfig contains non-secret and secret runtime inputs for VK
// messages. Secrets must be supplied by env/secret files, not committed config.
type VKMessagesConfig struct {
	Token        string
	APIVersion   string
	BaseURL      string
	HTTPClient   HTTPDoer
	HistoryCache *VKHistoryCache
}

// VKMessagesCarrier moves envelopes through VK messages.send/getHistory.
type VKMessagesCarrier struct {
	token      string
	apiVersion string
	baseURL    string
	httpClient HTTPDoer
	desc       Descriptor
	history    *VKHistoryCache
	cipher     *fabric.EnvelopeCipher
	fragments  socialWordFragmentAssembler
}

// NewVKMessagesCarrier creates a VK retained mailbox carrier.
func NewVKMessagesCarrier(cfg VKMessagesConfig) (*VKMessagesCarrier, error) {
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, fmt.Errorf("vk messages token is required")
	}
	apiVersion := cfg.APIVersion
	if apiVersion == "" {
		apiVersion = "5.199"
	}
	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	if baseURL == "" {
		baseURL = DefaultVKAPIBaseURL
	}
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	desc, err := FindStandardDescriptor(CarrierVKMessages)
	if err != nil {
		return nil, err
	}
	return &VKMessagesCarrier{
		token:      cfg.Token,
		apiVersion: apiVersion,
		baseURL:    baseURL,
		httpClient: client,
		desc:       desc,
		history:    cfg.HistoryCache,
	}, nil
}

func (c *VKMessagesCarrier) Descriptor() Descriptor { return c.desc }
func (c *VKMessagesCarrier) IsNative()              {}

// SetCipher enables AES-256-GCM encryption for all mailbox envelopes written
// and read through this carrier.
func (c *VKMessagesCarrier) SetCipher(cipher *fabric.EnvelopeCipher) { c.cipher = cipher }

// ClearCipher disables envelope encryption.
func (c *VKMessagesCarrier) ClearCipher() { c.cipher = nil }

// WriteResult contains information about a published message.
type WriteResult struct {
	MessageID int
}

func (c *VKMessagesCarrier) Write(ctx context.Context, endpoint Endpoint, envelope fabric.Envelope) error {
	result, err := c.WriteWithResult(ctx, endpoint, envelope)
	if err != nil {
		return err
	}
	// We could log the message ID here if needed for debugging
	_ = result // Suppress unused variable warning
	return nil
}

func (c *VKMessagesCarrier) WriteWithResult(ctx context.Context, endpoint Endpoint, envelope fabric.Envelope) (*WriteResult, error) {
	peerID, err := vkPeerID(endpoint)
	if err != nil {
		return nil, err
	}
	messages, err := encodeSocialMessageEnvelopes(envelope, c.desc.Limits.MaxPayloadBytes, c.cipher)
	if err != nil {
		return nil, err
	}
	var messageID int
	for _, message := range messages {
		if c.desc.Limits.MaxPayloadBytes > 0 && len(message) > c.desc.Limits.MaxPayloadBytes {
			return nil, fmt.Errorf("vk messages envelope size %d exceeds carrier limit %d", len(message), c.desc.Limits.MaxPayloadBytes)
		}
		response, err := c.call(ctx, "messages.send", url.Values{
			"peer_id":   {peerID},
			"random_id": {strconv.FormatInt(rand.New(rand.NewSource(time.Now().UnixNano())).Int63(), 10)},
			"message":   {message},
		})
		if err != nil {
			return nil, err
		}
		messageID, err = vkResponseMessageID(response)
		if err != nil {
			return nil, err
		}
	}
	// A fragmented envelope has several provider IDs; retain the final one for
	// the existing single-ID API contract.
	return &WriteResult{MessageID: messageID}, nil
}

func (c *VKMessagesCarrier) Read(ctx context.Context, endpoint Endpoint, cursor Cursor) (ReadResult, error) {
	peerID, err := vkPeerID(endpoint)
	if err != nil {
		return ReadResult{}, err
	}
	items, err := readVKHistory(ctx, c.history, c.token, c.apiVersion, c.baseURL, peerID, c.call)
	if err != nil {
		return ReadResult{}, err
	}
	after, _ := strconv.Atoi(string(cursor))
	maxID := after
	type decodedItem struct {
		id       int
		envelope fabric.Envelope
	}
	decoded := make([]decodedItem, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id := intFromJSON(item["id"])
		if id <= after {
			continue
		}
		if id > maxID {
			maxID = id
		}
		text, _ := item["text"].(string)
		envelope, complete, err := decodeSocialMessageEnvelopeWithFragments(text, socialMessageFragmentScope("vk", peerID), &c.fragments, c.cipher)
		if err != nil || !complete {
			continue
		}
		decoded = append(decoded, decodedItem{id: id, envelope: envelope})
	}
	for i := 1; i < len(decoded); i++ {
		current := decoded[i]
		j := i - 1
		for j >= 0 && decoded[j].id > current.id {
			decoded[j+1] = decoded[j]
			j--
		}
		decoded[j+1] = current
	}
	envelopes := make([]fabric.Envelope, 0, len(decoded))
	for _, item := range decoded {
		envelopes = append(envelopes, item.envelope)
	}
	return ReadResult{Envelopes: envelopes, Cursor: Cursor(strconv.Itoa(maxID))}, nil
}

func (c *VKMessagesCarrier) Probe(ctx context.Context, endpoint Endpoint) (Metrics, error) {
	started := time.Now()
	_, err := c.Read(ctx, endpoint, "")
	if err != nil {
		return Metrics{Healthy: false, FailureReason: err.Error()}, err
	}
	return Metrics{Healthy: true, Latency: time.Since(started), LastOK: time.Now().UTC()}, nil
}

func (c *VKMessagesCarrier) SafeEgressRecoveryProbe(ctx context.Context, endpoint Endpoint) (Metrics, error) {
	return c.Probe(ctx, endpoint)
}

func (c *VKMessagesCarrier) DeleteMessage(ctx context.Context, endpoint Endpoint, messageID string) error {
	peerID, err := vkPeerID(endpoint)
	if err != nil {
		return err
	}
	response, err := c.call(ctx, "messages.delete", url.Values{
		"peer_id":     {peerID},
		"message_ids": {messageID},
	})
	if err != nil {
		return err
	}
	if _, ok := response["response"]; !ok {
		return fmt.Errorf("vk messages.delete missing response")
	}
	return nil
}

func (c *VKMessagesCarrier) call(ctx context.Context, method string, values url.Values) (map[string]any, error) {
	return callVK(ctx, c.httpClient, c.baseURL, c.token, c.apiVersion, "WhiteTransport Go VK messages carrier", method, values)
}

func vkResponseMessageID(response map[string]any) (int, error) {
	responseValue, ok := response["response"]
	if !ok {
		return 0, fmt.Errorf("vk messages.send missing response")
	}

	// Handle both formats: {"response":[123]} and {"response":123}.
	switch typedResponse := responseValue.(type) {
	case []interface{}:
		if len(typedResponse) == 0 {
			return 0, fmt.Errorf("vk messages.send response array is empty")
		}
		if id, ok := typedResponse[0].(float64); ok {
			return int(id), nil
		}
		return 0, fmt.Errorf("vk messages.send response array element not a number")
	case float64:
		return int(typedResponse), nil
	case int:
		return typedResponse, nil
	default:
		return 0, fmt.Errorf("vk messages.send response message ID not a number: %v", typedResponse)
	}
}

func vkPeerID(endpoint Endpoint) (string, error) {
	if strings.TrimSpace(endpoint.Address) != "" {
		return strings.TrimSpace(endpoint.Address), nil
	}
	if endpoint.Metadata != nil {
		if value := strings.TrimSpace(endpoint.Metadata["peer_id"]); value != "" {
			return value, nil
		}
	}
	return "", fmt.Errorf("vk messages endpoint requires peer id address")
}

func encodeMailboxEnvelope(envelope fabric.Envelope, cipher ...*fabric.EnvelopeCipher) (string, error) {
	var c *fabric.EnvelopeCipher
	if len(cipher) > 0 {
		c = cipher[0]
	}
	if c != nil {
		sealed, err := c.Seal(envelope)
		if err != nil {
			return "", fmt.Errorf("seal mailbox envelope: %w", err)
		}
		return encryptedMessagePrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return "", err
	}
	return mailboxMessagePrefix + base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeMailboxEnvelope(text string, cipher ...*fabric.EnvelopeCipher) (fabric.Envelope, error) {
	var c *fabric.EnvelopeCipher
	if len(cipher) > 0 {
		c = cipher[0]
	}
	// Encrypted path: sealed ciphertext under wtenc1. prefix.
	if encIdx := strings.Index(text, encryptedMessagePrefix); encIdx >= 0 {
		if c == nil {
			return fabric.Envelope{}, fmt.Errorf("encrypted mailbox envelope but no cipher available")
		}
		encoded := strings.TrimSpace(text[encIdx+len(encryptedMessagePrefix):])
		if cut := strings.IndexAny(encoded, " \n\t\r"); cut >= 0 {
			encoded = encoded[:cut]
		}
		ciphertext, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			return fabric.Envelope{}, fmt.Errorf("decode encrypted mailbox: %w", err)
		}
		return c.Open(ciphertext)
	}
	// Plain path: JSON envelope under wtmsg1. prefix.
	idx := strings.Index(text, mailboxMessagePrefix)
	if idx < 0 {
		return fabric.Envelope{}, fmt.Errorf("missing mailbox envelope prefix")
	}
	encoded := strings.TrimSpace(text[idx+len(mailboxMessagePrefix):])
	if cut := strings.IndexAny(encoded, " \n\t\r"); cut >= 0 {
		encoded = encoded[:cut]
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return fabric.Envelope{}, err
	}
	var envelope fabric.Envelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fabric.Envelope{}, err
	}
	return envelope, nil
}

// encodeSocialMessageEnvelope keeps the provider-visible text of encrypted
// social mailboxes in the VKCipher Russian-word transport. Other text-like
// transports (notably DNS labels) retain their own wire encodings.
func encodeSocialMessageEnvelope(envelope fabric.Envelope, cipher ...*fabric.EnvelopeCipher) (string, error) {
	text, err := encodeMailboxEnvelope(envelope, cipher...)
	if err != nil {
		return "", err
	}
	if len(cipher) == 0 || cipher[0] == nil {
		return text, nil
	}
	return encodeVKCipherWords([]byte(text))
}

// decodeSocialMessageEnvelope accepts the current VKCipher word transport and
// the legacy prefixed envelope format so a rolling update does not strand a
// retained social mailbox. A successful word decode still has to pass the
// session-cipher authentication in decodeMailboxEnvelope.
func decodeSocialMessageEnvelope(text string, cipher ...*fabric.EnvelopeCipher) (fabric.Envelope, error) {
	var assembler socialWordFragmentAssembler
	envelope, complete, err := decodeSocialMessageEnvelopeWithFragments(text, "direct", &assembler, cipher...)
	if err != nil {
		return fabric.Envelope{}, err
	}
	if !complete {
		return fabric.Envelope{}, fmt.Errorf("incomplete social word fragment")
	}
	return envelope, nil
}

func intFromJSON(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case string:
		parsed, _ := strconv.Atoi(typed)
		return parsed
	default:
		return 0
	}
}
