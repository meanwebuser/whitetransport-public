package carriers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

// CarrierFileMailbox is the id for the local file-backed mailbox carrier. It is
// a deterministic, cross-process control/bootstrap mailbox used for local
// testing in place of VK/OK message carriers (which are rate-limited and
// replay backlog). Each endpoint maps to one append-only JSON-lines file in a
// shared directory; readers track progress with a line-count cursor.
const CarrierFileMailbox = "file.mailbox"

// FileMailboxConfig configures the file-backed mailbox carrier.
type FileMailboxConfig struct {
	// Dir is the shared directory holding per-endpoint mailbox files. Both the
	// node and client daemons must point at the same directory.
	Dir string
	// AllowEgress explicitly opts this deterministic test carrier into
	// frame-based local egress. It is false by default because a mailbox is
	// otherwise control-only and must never be advertised to real clients.
	AllowEgress bool
}

// FileMailboxCarrier is a deterministic mailbox carrier that persists envelopes
// to append-only files in a shared directory, enabling reliable cross-process
// bootstrap/control exchange without external providers.
type FileMailboxCarrier struct {
	dir  string
	desc Descriptor
	mu   sync.Mutex
}

// NewFileMailboxCarrier creates a file-backed mailbox carrier rooted at dir.
func NewFileMailboxCarrier(cfg FileMailboxConfig) (*FileMailboxCarrier, error) {
	dir := cfg.Dir
	if dir == "" {
		return nil, fmt.Errorf("file mailbox: dir is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("file mailbox: mkdir %s: %w", dir, err)
	}
	desc := Descriptor{
		ID:             CarrierFileMailbox,
		Provider:       "local",
		Mode:           DeliveryMailbox,
		TrafficClasses: []fabric.TrafficClass{fabric.TrafficBootstrap, fabric.TrafficControl, fabric.TrafficAdmin, fabric.TrafficHealth, fabric.TrafficLog},
		Capabilities:   []Capability{CapRendezvous, CapMailbox, CapRetained},
		Limits:         Limits{MaxPayloadBytes: 1 << 20, ChunkPayloadBytes: 1 << 20, SendsPerMinute: 6000, PollsPerMinute: 6000},
		Metrics:        Metrics{Healthy: true, Latency: time.Millisecond, BandwidthBPS: 100 * 1024 * 1024, Reliability: 1.0, QuotaRemaining: -1},
		Notes:          "Local file-backed mailbox for deterministic cross-process control testing; not for production.",
	}
	if cfg.AllowEgress {
		desc.TrafficClasses = append(desc.TrafficClasses, fabric.TrafficEgress)
		desc.Capabilities = append(desc.Capabilities, CapBulk)
		desc.Notes = "Local file-backed mailbox with explicitly enabled deterministic test egress; not for production."
	}
	return &FileMailboxCarrier{dir: dir, desc: desc}, nil
}

func (c *FileMailboxCarrier) Descriptor() Descriptor { return c.desc }

func (c *FileMailboxCarrier) pathFor(endpoint Endpoint) string {
	name := endpoint.Address
	if name == "" {
		name = endpoint.ID
	}
	if name == "" {
		name = "default"
	}
	return filepath.Join(c.dir, sanitizeMailboxName(name)+".jsonl")
}

func sanitizeMailboxName(name string) string {
	out := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

func (c *FileMailboxCarrier) Write(_ context.Context, endpoint Endpoint, envelope fabric.Envelope) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	raw, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("file mailbox: marshal: %w", err)
	}
	f, err := os.OpenFile(c.pathFor(endpoint), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("file mailbox: open: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(raw, '\n')); err != nil {
		return fmt.Errorf("file mailbox: write: %w", err)
	}
	return nil
}

func (c *FileMailboxCarrier) Read(_ context.Context, endpoint Endpoint, cursor Cursor) (ReadResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, err := os.ReadFile(c.pathFor(endpoint))
	if err != nil {
		if os.IsNotExist(err) {
			return ReadResult{Cursor: cursor}, nil
		}
		return ReadResult{}, fmt.Errorf("file mailbox: read: %w", err)
	}
	start := 0
	if cursor != "" {
		if parsed, err := strconv.Atoi(string(cursor)); err == nil && parsed > 0 {
			start = parsed
		}
	}
	lines := splitLines(data)
	if start > len(lines) {
		start = len(lines)
	}
	out := make([]fabric.Envelope, 0, len(lines)-start)
	for _, line := range lines[start:] {
		if len(line) == 0 {
			continue
		}
		var env fabric.Envelope
		if err := json.Unmarshal(line, &env); err != nil {
			continue
		}
		out = append(out, env)
	}
	return ReadResult{Envelopes: out, Cursor: Cursor(strconv.Itoa(len(lines)))}, nil
}

func (c *FileMailboxCarrier) Probe(context.Context, Endpoint) (Metrics, error) {
	return Metrics{Healthy: true, Latency: time.Millisecond}, nil
}

func (c *FileMailboxCarrier) SafeEgressRecoveryProbe(ctx context.Context, endpoint Endpoint) (Metrics, error) {
	return c.Probe(ctx, endpoint)
}

func (c *FileMailboxCarrier) DeleteMessage(ctx context.Context, endpoint Endpoint, messageID string) error {
	// File mailbox carrier doesn't support message deletion
	return fmt.Errorf("delete message not implemented for file mailbox carrier")
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
