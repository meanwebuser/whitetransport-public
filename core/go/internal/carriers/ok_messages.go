package carriers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

// OKMessagesConfig contains runtime inputs for the OK Graph retained mailbox.
// Tokens must come from env or secret stores, never committed config files.
type OKMessagesConfig struct {
	Token      string
	BaseURL    string
	SendPath   string
	ReadPath   string
	HTTPClient HTTPDoer
}

// OKMessagesCarrier moves envelopes through the OK Graph messages API.
type OKMessagesCarrier struct {
	token      string
	baseURL    string
	sendPath   string
	readPath   string
	httpClient HTTPDoer
	desc       Descriptor
	cipher     *fabric.EnvelopeCipher
}

// NewOKMessagesCarrier creates an OK retained mailbox carrier.
func NewOKMessagesCarrier(cfg OKMessagesConfig) (*OKMessagesCarrier, error) {
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, fmt.Errorf("ok messages token is required")
	}
	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.ok.ru/graph"
	}
	sendPath := cfg.SendPath
	if sendPath == "" {
		sendPath = "/me/messages"
	}
	readPath := cfg.ReadPath
	if readPath == "" {
		readPath = "/me/messages"
	}
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	desc, err := FindStandardDescriptor(CarrierOKMessages)
	if err != nil {
		return nil, err
	}
	return &OKMessagesCarrier{
		token:      cfg.Token,
		baseURL:    baseURL,
		sendPath:   sendPath,
		readPath:   readPath,
		httpClient: client,
		desc:       desc,
	}, nil
}

func (c *OKMessagesCarrier) Descriptor() Descriptor { return c.desc }
func (c *OKMessagesCarrier) IsNative()              {}

// SetCipher enables AES-256-GCM encryption for all mailbox envelopes written
// and read through this carrier.
func (c *OKMessagesCarrier) SetCipher(cipher *fabric.EnvelopeCipher) { c.cipher = cipher }

// ClearCipher disables envelope encryption.
func (c *OKMessagesCarrier) ClearCipher() { c.cipher = nil }

func (c *OKMessagesCarrier) Write(ctx context.Context, endpoint Endpoint, envelope fabric.Envelope) error {
	recipient, err := okRecipient(endpoint)
	if err != nil {
		return err
	}
	message, err := encodeMailboxEnvelope(envelope, c.cipher)
	if err != nil {
		return err
	}
	if c.desc.Limits.MaxPayloadBytes > 0 && len(message) > c.desc.Limits.MaxPayloadBytes {
		return fmt.Errorf("ok messages envelope size %d exceeds carrier limit %d", len(message), c.desc.Limits.MaxPayloadBytes)
	}
	response, err := c.callJSON(ctx, http.MethodPost, c.sendPath, map[string]any{
		"recipient": recipient,
		"message": map[string]string{
			"text": message,
		},
	})
	if err != nil {
		return err
	}
	if err := checkOKGraphResponse("messages.send", response); err != nil {
		return err
	}
	return nil
}

func (c *OKMessagesCarrier) Read(ctx context.Context, endpoint Endpoint, cursor Cursor) (ReadResult, error) {
	recipient, err := okRecipient(endpoint)
	if err != nil {
		return ReadResult{}, err
	}
	response, err := c.callJSON(ctx, http.MethodGet, c.readPath, map[string]any{"recipient": recipient})
	if err != nil {
		return ReadResult{}, err
	}
	if err := checkOKGraphResponse("messages.read", response); err != nil {
		return ReadResult{}, err
	}
	items, err := okMessageItems(response)
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
	for _, item := range items {
		id := intFromJSON(item["id"])
		if id <= after {
			continue
		}
		if id > maxID {
			maxID = id
		}
		text := okMessageText(item)
		envelope, err := decodeMailboxEnvelope(text, c.cipher)
		if err != nil {
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

func (c *OKMessagesCarrier) Probe(ctx context.Context, endpoint Endpoint) (Metrics, error) {
	started := time.Now()
	_, err := c.Read(ctx, endpoint, "")
	if err != nil {
		return Metrics{Healthy: false, FailureReason: err.Error()}, err
	}
	return Metrics{Healthy: true, Latency: time.Since(started), LastOK: time.Now().UTC()}, nil
}

func (c *OKMessagesCarrier) DeleteMessage(ctx context.Context, endpoint Endpoint, messageID string) error {
	// OK messages carrier doesn't support message deletion
	return fmt.Errorf("delete message not implemented for ok messages carrier")
}

func (c *OKMessagesCarrier) callJSON(ctx context.Context, method string, path string, body map[string]any) (map[string]any, error) {
	values := url.Values{"access_token": {c.token}}
	if method == http.MethodGet {
		addOKQueryValues(values, body)
	}
	endpoint := c.baseURL + path + "?" + values.Encode()
	var requestBody io.Reader
	if method != http.MethodGet {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		requestBody = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, requestBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json;charset=utf-8")
	req.Header.Set("User-Agent", "WhiteTransport Go OK messages carrier")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("ok graph %s HTTP %d", path, resp.StatusCode)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func addOKQueryValues(values url.Values, body map[string]any) {
	recipient, ok := body["recipient"].(map[string]string)
	if !ok {
		return
	}
	for key, value := range recipient {
		values.Set(key, value)
	}
}

func okRecipient(endpoint Endpoint) (map[string]string, error) {
	address := strings.TrimSpace(endpoint.Address)
	if address == "" && endpoint.Metadata != nil {
		switch {
		case strings.TrimSpace(endpoint.Metadata["chat_id"]) != "":
			address = strings.TrimSpace(endpoint.Metadata["chat_id"])
		case strings.TrimSpace(endpoint.Metadata["user_id"]) != "":
			address = strings.TrimSpace(endpoint.Metadata["user_id"])
		}
	}
	switch {
	case strings.HasPrefix(address, "chat:"):
		return map[string]string{"chat_id": address}, nil
	case strings.HasPrefix(address, "user:"):
		return map[string]string{"user_id": address}, nil
	case endpoint.Metadata != nil && strings.TrimSpace(endpoint.Metadata["chat_id"]) != "":
		return map[string]string{"chat_id": strings.TrimSpace(endpoint.Metadata["chat_id"])}, nil
	case endpoint.Metadata != nil && strings.TrimSpace(endpoint.Metadata["user_id"]) != "":
		return map[string]string{"user_id": strings.TrimSpace(endpoint.Metadata["user_id"])}, nil
	default:
		return nil, fmt.Errorf("ok messages endpoint requires chat: or user: address")
	}
}

func checkOKGraphResponse(action string, response map[string]any) error {
	if rawError, ok := response["error_code"]; ok {
		return fmt.Errorf("ok graph %s error_code: %v", action, rawError)
	}
	if rawError, ok := response["error"]; ok {
		return fmt.Errorf("ok graph %s error: %v", action, rawError)
	}
	if success, ok := response["success"].(bool); ok && !success {
		return fmt.Errorf("ok graph %s returned success=false", action)
	}
	return nil
}

func okMessageItems(response map[string]any) ([]map[string]any, error) {
	if items, ok := mapItems(response["items"]); ok {
		return items, nil
	}
	if messages, ok := mapItems(response["messages"]); ok {
		return messages, nil
	}
	container, ok := response["response"].(map[string]any)
	if ok {
		if items, ok := mapItems(container["items"]); ok {
			return items, nil
		}
		if messages, ok := mapItems(container["messages"]); ok {
			return messages, nil
		}
	}
	return nil, fmt.Errorf("ok graph messages.read missing items")
}

func mapItems(value any) ([]map[string]any, bool) {
	rawItems, ok := value.([]any)
	if !ok {
		return nil, false
	}
	items := make([]map[string]any, 0, len(rawItems))
	for _, raw := range rawItems {
		item, ok := raw.(map[string]any)
		if ok {
			items = append(items, item)
		}
	}
	return items, true
}

func okMessageText(item map[string]any) string {
	if text, ok := item["text"].(string); ok {
		return text
	}
	message, ok := item["message"].(map[string]any)
	if !ok {
		return ""
	}
	text, _ := message["text"].(string)
	return text
}
