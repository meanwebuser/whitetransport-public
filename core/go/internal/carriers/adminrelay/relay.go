// Package adminrelay implements a Carrier that exchanges envelopes through
// the admin panel's HTTP relay API. This enables NAT-traversal when both
// the node and client can reach the admin panel but cannot connect directly.
//
// Channels: "discovery", "control", "logs", "relay"
// - discovery: node.advertise, node.withdraw, node.heartbeat
// - control:   session.offer, session.answer, session.ack, session.release
// - logs:      log upload from nodes
// - relay:     arbitrary relay traffic for NAT traversal (relay-internet)
package adminrelay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

// CarrierID is the canonical ID for the admin relay carrier.
const CarrierID = "admin.relay"

// Carrier implements the carriers.Carrier interface by posting/reading
// envelopes through the admin panel's /api/relay/messages endpoint.
type Carrier struct {
	cfg    config.AdminRelayConfig
	client *http.Client
	logf   func(format string, args ...any)

	mu        sync.Mutex
	lastReads map[string]string // channel → last message ID cursor
}

// New creates a new admin relay carrier.
func New(cfg config.AdminRelayConfig, logf func(format string, args ...any)) *Carrier {
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	return NewWithDialContext(cfg, logf, dialer.DialContext)
}

// NewWithDialContext creates an admin relay carrier whose HTTP connections
// exclusively use dialContext. This lets any stream-capable egress provide the
// TCP path to the HTTP control relay without a direct-network fallback.
func NewWithDialContext(cfg config.AdminRelayConfig, logf func(format string, args ...any), dialContext func(context.Context, string, string) (net.Conn, error)) *Carrier {
	if logf == nil {
		logf = log.Printf
	}
	if dialContext == nil {
		dialContext = func(context.Context, string, string) (net.Conn, error) {
			return nil, fmt.Errorf("admin relay dial context is required")
		}
	}
	transport := &http.Transport{
		DialContext:       dialContext,
		ForceAttemptHTTP2: true,
	}
	return &Carrier{
		cfg:       cfg,
		client:    &http.Client{Timeout: 15 * time.Second, Transport: transport},
		logf:      logf,
		lastReads: make(map[string]string),
	}
}

func (c *Carrier) Descriptor() carriers.Descriptor {
	return carriers.Descriptor{
		ID:       CarrierID,
		Provider: "admin",
		Mode:     carriers.DeliveryMailbox,
		TrafficClasses: []fabric.TrafficClass{
			fabric.TrafficBootstrap,
			fabric.TrafficControl,
			fabric.TrafficLog,
			fabric.TrafficAdmin,
			fabric.TrafficEgress,
		},
		Capabilities: []carriers.Capability{
			carriers.CapMailbox,
			carriers.CapDuplex,
			carriers.CapPoll,
			carriers.CapRendezvous,
			carriers.CapRetrospective,
		},
		Limits: carriers.Limits{
			MaxPayloadBytes: 65536,
			PollsPerMinute:  120,
			SendsPerMinute:  60,
		},
	}
}

func (c *Carrier) Write(ctx context.Context, endpoint carriers.Endpoint, envelope fabric.Envelope) error {
	channel := endpointToChannel(endpoint)
	recipient := endpoint.Metadata["recipient"]

	payloadBytes, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}

	body := map[string]any{
		"channel":     channel,
		"sender":      c.identity(),
		"message_key": envelope.ID,
		"payload":     string(payloadBytes),
		"ttl_ms":      3_600_000, // 1 hour
	}
	if recipient != "" {
		body["recipient"] = recipient
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return err
	}

	reqURL := c.relayURL("")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(jsonBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token := c.cfg.TokenValue(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("relay write: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("relay write: status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

func (c *Carrier) Read(ctx context.Context, endpoint carriers.Endpoint, cursor carriers.Cursor) (carriers.ReadResult, error) {
	channel := endpointToChannel(endpoint)
	recipient := c.identity()

	params := url.Values{}
	params.Set("channel", channel)
	params.Set("recipient", recipient)
	params.Set("limit", "50")
	if string(cursor) != "" {
		params.Set("since_id", string(cursor))
	}

	reqURL := c.relayURL(params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return carriers.ReadResult{}, err
	}
	if token := c.cfg.TokenValue(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return carriers.ReadResult{}, fmt.Errorf("relay read: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return carriers.ReadResult{}, fmt.Errorf("relay read: status %d", resp.StatusCode)
	}

	var result struct {
		OK       bool `json:"ok"`
		Messages []struct {
			ID        string `json:"id"`
			Sender    string `json:"sender"`
			Recipient string `json:"recipient"`
			Payload   any    `json:"payload"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return carriers.ReadResult{}, fmt.Errorf("decode relay response: %w", err)
	}

	var envelopes []fabric.Envelope
	var lastID string
	for _, msg := range result.Messages {
		// Skip our own messages
		if msg.Sender == c.identity() {
			lastID = msg.ID
			continue
		}

		var payloadStr string
		switch v := msg.Payload.(type) {
		case string:
			payloadStr = v
		default:
			b, err := json.Marshal(v)
			if err != nil {
				continue
			}
			payloadStr = string(b)
		}

		var env fabric.Envelope
		if err := json.Unmarshal([]byte(payloadStr), &env); err != nil {
			// Not an envelope — skip
			lastID = msg.ID
			continue
		}
		envelopes = append(envelopes, env)
		lastID = msg.ID
	}

	newCursor := carriers.Cursor(lastID)
	if lastID != "" {
		c.mu.Lock()
		c.lastReads[channel] = lastID
		c.mu.Unlock()
	}

	return carriers.ReadResult{
		Envelopes: envelopes,
		Cursor:    newCursor,
	}, nil
}

// Ack durably acknowledges every relay message up to messageCursor after the
// caller has successfully applied the fetched page. Fetch never acknowledges
// implicitly because a process can fail between Read and local dispatch.
func (c *Carrier) Ack(ctx context.Context, endpoint carriers.Endpoint, messageCursor carriers.Cursor) error {
	messageID := strings.TrimSpace(string(messageCursor))
	if messageID == "" {
		return fmt.Errorf("relay acknowledgement requires message id")
	}
	body, err := json.Marshal(map[string]string{
		"channel":    endpointToChannel(endpoint),
		"consumer":   c.identity(),
		"message_id": messageID,
	})
	if err != nil {
		return fmt.Errorf("marshal relay acknowledgement: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.ackURL(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token := c.cfg.TokenValue(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("relay acknowledgement: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("relay acknowledgement: status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

func (c *Carrier) Probe(ctx context.Context, endpoint carriers.Endpoint) (carriers.Metrics, error) {
	start := time.Now()
	_, err := c.Read(ctx, endpoint, carriers.Cursor(""))
	latency := time.Since(start)
	if err != nil {
		return carriers.Metrics{
			Healthy:       false,
			FailureReason: err.Error(),
		}, nil
	}
	return carriers.Metrics{
		Healthy: true,
		Latency: latency,
		LastOK:  time.Now(),
	}, nil
}

func (c *Carrier) DeleteMessage(ctx context.Context, endpoint carriers.Endpoint, messageID string) error {
	// Admin relay does not support message deletion
	return fmt.Errorf("delete not supported for admin.relay carrier")
}

func (c *Carrier) identity() string {
	if c.cfg.Identity != "" {
		return c.cfg.Identity
	}
	return "anonymous"
}

func (c *Carrier) relayURL(query string) string {
	base := strings.TrimRight(c.cfg.AdminURL, "/")
	if query != "" {
		return base + "/api/relay/messages?" + query
	}
	return base + "/api/relay/messages"
}

func (c *Carrier) ackURL() string {
	return strings.TrimRight(c.cfg.AdminURL, "/") + "/api/relay/acks"
}

// endpointToChannel maps an endpoint to a relay channel name.
// The endpoint.Address or endpoint.ID is used as the channel name.
// Defaults to "control" if not specified.
func endpointToChannel(endpoint carriers.Endpoint) string {
	if endpoint.Metadata["channel"] != "" {
		return endpoint.Metadata["channel"]
	}
	if endpoint.Address != "" {
		return endpoint.Address
	}
	if endpoint.ID != "" {
		return endpoint.ID
	}
	return "control"
}
