package carriers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/mail"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

const (
	mailMaxEnvelopeBytes  = 48 * 1024
	mailChunkPayloadBytes = 24 * 1024
	mailCursorVersion     = 1
	mailWireVersion       = "1"
	mailRecordPrefix      = "wtmail1."
	mailIMAPScanPageSize  = 64
)

// MailIMAPSMTPConfig contains non-secret connection metadata plus credentials
// resolved by the runtime from one binding-scoped composite TokenStore entry.
type MailIMAPSMTPConfig struct {
	SMTPAddress   string
	IMAPAddress   string
	AccountID     string
	Mailbox       string
	FromAddress   string
	ToAddress     string
	TLSServerName string
	CAFile        string
	SMTPUsername  string
	SMTPPassword  string
	IMAPUsername  string
	IMAPPassword  string
	Timeout       time.Duration
}

type mailSMTPClient interface {
	Submit(ctx context.Context, message []byte) error
	Probe(ctx context.Context) error
}

type mailIMAPClient interface {
	Scan(ctx context.Context, mailbox string, expectedUIDValidity, highestScannedUID uint32) (mailScanResult, error)
	Probe(ctx context.Context, mailbox string) error
}

type mailIMAPMessage struct {
	uid uint32
	raw []byte
}

type mailScanResult struct {
	uidValidity       uint32
	highestScannedUID uint32
	messages          []mailIMAPMessage
}

type mailCursorState struct {
	Version           int             `json:"v"`
	Scope             string          `json:"scope"`
	UIDValidity       uint32          `json:"uid_validity"`
	HighestScannedUID uint32          `json:"highest_scanned_uid"`
	DeliveredIDs      map[string]bool `json:"delivered_ids"`
}

// MailIMAPSMTPCarrier is a retained network mailbox. SMTP submits one MIME
// message per fabric envelope; IMAP scans the local account by UID.
type MailIMAPSMTPCarrier struct {
	cfg    MailIMAPSMTPConfig
	desc   Descriptor
	smtp   mailSMTPClient
	imap   mailIMAPClient
	cipher *fabric.EnvelopeCipher
	mu     sync.Mutex
}

func NewMailIMAPSMTPCarrier(cfg MailIMAPSMTPConfig) (*MailIMAPSMTPCarrier, error) {
	if err := validateMailConfig(cfg); err != nil {
		return nil, err
	}
	tlsConfig, err := newMailTLSConfig(cfg.TLSServerName, cfg.CAFile)
	if err != nil {
		return nil, err
	}
	return newMailIMAPSMTPCarrierWithClients(
		cfg,
		newSMTPMailClient(cfg, tlsConfig),
		newIMAPMailClient(cfg, tlsConfig),
	)
}

func newMailIMAPSMTPCarrierWithClients(cfg MailIMAPSMTPConfig, smtpClient mailSMTPClient, imapClient mailIMAPClient) (*MailIMAPSMTPCarrier, error) {
	if err := validateMailConfig(cfg); err != nil {
		return nil, err
	}
	if smtpClient == nil || imapClient == nil {
		return nil, fmt.Errorf("mail imap/smtp: SMTP and IMAP clients are required")
	}
	cipher, err := mailEnvelopeCipher(cfg)
	if err != nil {
		return nil, err
	}
	desc, err := FindStandardDescriptor(CarrierMailIMAPSMTP)
	if err != nil {
		desc = Descriptor{
			ID:             CarrierMailIMAPSMTP,
			Provider:       "mail",
			Mode:           DeliveryMailbox,
			TrafficClasses: []fabric.TrafficClass{fabric.TrafficBootstrap, fabric.TrafficControl, fabric.TrafficAdmin, fabric.TrafficHealth, fabric.TrafficLog, fabric.TrafficBulk, fabric.TrafficRepair, fabric.TrafficEgress},
			Capabilities:   []Capability{CapRendezvous, CapMailbox, CapRetained, CapRetrospective, CapPoll, CapAppendOnly, CapDurable, CapOrdered, CapBulk},
			Limits:         Limits{MaxPayloadBytes: mailMaxEnvelopeBytes, ChunkPayloadBytes: mailChunkPayloadBytes},
			Metrics:        Metrics{Healthy: true, Latency: 2 * time.Second, BandwidthBPS: 16 * 1024, Reliability: 0.85, QuotaRemaining: -1},
			Notes:          "TLS-authenticated SMTP submission plus retained IMAP UID polling; message-body carrier.",
		}
	}
	return &MailIMAPSMTPCarrier{cfg: cfg, desc: desc, smtp: smtpClient, imap: imapClient, cipher: cipher}, nil
}

func validateMailConfig(cfg MailIMAPSMTPConfig) error {
	required := []struct {
		name  string
		value string
	}{
		{"smtp_address", cfg.SMTPAddress},
		{"imap_address", cfg.IMAPAddress},
		{"account_id", cfg.AccountID},
		{"mailbox", cfg.Mailbox},
		{"from_address", cfg.FromAddress},
		{"to_address", cfg.ToAddress},
		{"tls_server_name", cfg.TLSServerName},
		{"ca_file", cfg.CAFile},
		{"smtp_username", cfg.SMTPUsername},
		{"smtp_password", cfg.SMTPPassword},
		{"imap_username", cfg.IMAPUsername},
		{"imap_password", cfg.IMAPPassword},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			if strings.Contains(field.name, "username") || strings.Contains(field.name, "password") {
				return fmt.Errorf("mail imap/smtp: credential %s is required", field.name)
			}
			return fmt.Errorf("mail imap/smtp: %s is required", field.name)
		}
	}
	if _, err := mail.ParseAddress(cfg.FromAddress); err != nil {
		return fmt.Errorf("mail imap/smtp: invalid from_address: %w", err)
	}
	if _, err := mail.ParseAddress(cfg.ToAddress); err != nil {
		return fmt.Errorf("mail imap/smtp: invalid to_address: %w", err)
	}
	return nil
}

func (c *MailIMAPSMTPCarrier) Descriptor() Descriptor { return c.desc }
func (c *MailIMAPSMTPCarrier) IsNative()              {}

func (c *MailIMAPSMTPCarrier) Write(ctx context.Context, endpoint Endpoint, envelope fabric.Envelope) error {
	raw, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("mail imap/smtp: marshal envelope: %w", err)
	}
	if len(raw) > mailMaxEnvelopeBytes {
		return fmt.Errorf("mail imap/smtp: envelope size %d exceeds limit %d", len(raw), mailMaxEnvelopeBytes)
	}
	message, err := encodeMailMessage(c.cfg, endpoint, envelope, c.cipher)
	if err != nil {
		return err
	}
	if err := c.smtp.Submit(ctx, message); err != nil {
		return fmt.Errorf("mail imap/smtp: SMTP submit: %w", err)
	}
	return nil
}

func (c *MailIMAPSMTPCarrier) Read(ctx context.Context, endpoint Endpoint, cursor Cursor) (ReadResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	scope := c.cursorScope(endpoint)
	state, err := parseMailCursor(cursor, scope)
	if err != nil {
		return ReadResult{}, err
	}
	scan, err := c.imap.Scan(ctx, c.cfg.Mailbox, state.UIDValidity, state.HighestScannedUID)
	if err != nil {
		return ReadResult{}, fmt.Errorf("mail imap/smtp: IMAP scan: %w", err)
	}
	if scan.uidValidity == 0 {
		return ReadResult{}, fmt.Errorf("mail imap/smtp: IMAP scan returned zero UIDVALIDITY")
	}
	effectiveHighest := state.HighestScannedUID
	if state.UIDValidity != 0 && state.UIDValidity != scan.uidValidity {
		effectiveHighest = 0
	}
	if state.DeliveredIDs == nil {
		state.DeliveredIDs = make(map[string]bool)
	}
	sort.Slice(scan.messages, func(i, j int) bool { return scan.messages[i].uid < scan.messages[j].uid })
	envelopes := make([]fabric.Envelope, 0, len(scan.messages))
	highest := effectiveHighest
	if scan.highestScannedUID > highest {
		highest = scan.highestScannedUID
	}
	wantMarker := mailEndpointMarker(endpoint)
	for _, message := range scan.messages {
		if message.uid <= effectiveHighest {
			continue
		}
		if message.uid > highest {
			highest = message.uid
		}
		deliveryID, deliveryErr := mailDeliveryID(message.raw)
		envelope, marker, decodeErr := decodeMailEnvelope(message.raw, c.cipher)
		if deliveryErr != nil || decodeErr != nil || marker != wantMarker {
			// Unrelated and poison messages advance scan state so they cannot
			// starve later valid envelopes in the retained mailbox.
			continue
		}
		if state.DeliveredIDs[deliveryID] {
			continue
		}
		state.DeliveredIDs[deliveryID] = true
		envelopes = append(envelopes, envelope)
	}
	state.Version = mailCursorVersion
	state.Scope = scope
	state.UIDValidity = scan.uidValidity
	state.HighestScannedUID = highest
	encoded, err := json.Marshal(state)
	if err != nil {
		return ReadResult{}, fmt.Errorf("mail imap/smtp: encode cursor: %w", err)
	}
	return ReadResult{Envelopes: envelopes, Cursor: Cursor(encoded)}, nil
}

func (c *MailIMAPSMTPCarrier) Probe(ctx context.Context, endpoint Endpoint) (Metrics, error) {
	start := time.Now()
	if err := c.smtp.Probe(ctx); err != nil {
		return Metrics{Healthy: false, FailureReason: err.Error()}, fmt.Errorf("mail imap/smtp: SMTP probe: %w", err)
	}
	if err := c.imap.Probe(ctx, c.cfg.Mailbox); err != nil {
		return Metrics{Healthy: false, FailureReason: err.Error()}, fmt.Errorf("mail imap/smtp: IMAP probe: %w", err)
	}
	return Metrics{Healthy: true, LastOK: time.Now().UTC(), Latency: time.Since(start), Reliability: 0.85, QuotaRemaining: -1}, nil
}

func (c *MailIMAPSMTPCarrier) SafeEgressRecoveryProbe(ctx context.Context, endpoint Endpoint) (Metrics, error) {
	return c.Probe(ctx, endpoint)
}

func (c *MailIMAPSMTPCarrier) DeleteMessage(context.Context, Endpoint, string) error {
	return fmt.Errorf("mail imap/smtp: retained message deletion is unsupported")
}

func (c *MailIMAPSMTPCarrier) cursorScope(endpoint Endpoint) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{c.cfg.AccountID, c.cfg.Mailbox, mailEndpointMarker(endpoint)}, "\x00")))
	return hex.EncodeToString(sum[:16])
}

func parseMailCursor(cursor Cursor, scope string) (mailCursorState, error) {
	state := mailCursorState{Version: mailCursorVersion, Scope: scope, DeliveredIDs: make(map[string]bool)}
	if cursor == "" {
		return state, nil
	}
	if err := json.Unmarshal([]byte(cursor), &state); err != nil {
		return mailCursorState{}, fmt.Errorf("mail imap/smtp: invalid cursor: %w", err)
	}
	if state.Version != mailCursorVersion {
		return mailCursorState{}, fmt.Errorf("mail imap/smtp: unsupported cursor version %d", state.Version)
	}
	if state.Scope != scope {
		return mailCursorState{}, fmt.Errorf("mail imap/smtp: cursor scope mismatch")
	}
	if state.DeliveredIDs == nil {
		state.DeliveredIDs = make(map[string]bool)
	}
	return state, nil
}

func encodeMailMessage(cfg MailIMAPSMTPConfig, endpoint Endpoint, envelope fabric.Envelope, cipher *fabric.EnvelopeCipher) ([]byte, error) {
	raw, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("mail imap/smtp: marshal MIME envelope: %w", err)
	}
	if len(raw) > mailMaxEnvelopeBytes {
		return nil, fmt.Errorf("mail imap/smtp: envelope size %d exceeds limit %d", len(raw), mailMaxEnvelopeBytes)
	}
	from, err := mail.ParseAddress(cfg.FromAddress)
	if err != nil {
		return nil, fmt.Errorf("mail imap/smtp: invalid from address: %w", err)
	}
	to, err := mail.ParseAddress(cfg.ToAddress)
	if err != nil {
		return nil, fmt.Errorf("mail imap/smtp: invalid to address: %w", err)
	}
	marker := mailEndpointMarker(endpoint)
	// A tunnel reuses one envelope ID for open/data/close frames. Derive the
	// RFC5322 Message-ID from the complete serialized envelope so distinct
	// frames remain distinct while an exact ambiguous-SMTP retry is stable.
	idInput := make([]byte, 0, len(raw)+len(marker)+1)
	idInput = append(idInput, raw...)
	idInput = append(idInput, 0)
	idInput = append(idInput, marker...)
	idSum := sha256.Sum256(idInput)
	if cipher == nil {
		return nil, fmt.Errorf("mail imap/smtp: envelope cipher is required")
	}
	sealed, err := cipher.SealWithAAD(envelope, mailRecordAAD(marker))
	if err != nil {
		return nil, fmt.Errorf("mail imap/smtp: seal wtmail1 envelope: %w", err)
	}
	record := mailRecordPrefix + base64.RawURLEncoding.EncodeToString(sealed)
	encoded := base64.StdEncoding.EncodeToString([]byte(record))
	var body strings.Builder
	for len(encoded) > 76 {
		body.WriteString(encoded[:76])
		body.WriteString("\r\n")
		encoded = encoded[76:]
	}
	if encoded != "" {
		body.WriteString(encoded)
		body.WriteString("\r\n")
	}
	var message bytes.Buffer
	fmt.Fprintf(&message, "From: %s\r\n", from.String())
	fmt.Fprintf(&message, "To: %s\r\n", to.String())
	fmt.Fprintf(&message, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&message, "Message-ID: <%s@whitetransport.invalid>\r\n", hex.EncodeToString(idSum[:16]))
	fmt.Fprintf(&message, "Subject: WhiteTransport %s\r\n", marker)
	message.WriteString("MIME-Version: 1.0\r\n")
	message.WriteString("Content-Type: text/plain; charset=us-ascii\r\n")
	message.WriteString("Content-Transfer-Encoding: base64\r\n")
	fmt.Fprintf(&message, "X-WhiteTransport-Version: %s\r\n", mailWireVersion)
	fmt.Fprintf(&message, "X-WhiteTransport-Endpoint: %s\r\n", marker)
	message.WriteString("\r\n")
	message.WriteString(body.String())
	return message.Bytes(), nil
}

func decodeMailEnvelope(raw []byte, cipher *fabric.EnvelopeCipher) (fabric.Envelope, string, error) {
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return fabric.Envelope{}, "", fmt.Errorf("mail imap/smtp: parse RFC5322: %w", err)
	}
	if strings.TrimSpace(message.Header.Get("X-WhiteTransport-Version")) != mailWireVersion {
		return fabric.Envelope{}, "", fmt.Errorf("mail imap/smtp: unrelated message")
	}
	marker := strings.TrimSpace(message.Header.Get("X-WhiteTransport-Endpoint"))
	if marker == "" {
		return fabric.Envelope{}, "", fmt.Errorf("mail imap/smtp: endpoint marker missing")
	}
	body, err := io.ReadAll(io.LimitReader(message.Body, mailMaxEnvelopeBytes*2))
	if err != nil {
		return fabric.Envelope{}, "", fmt.Errorf("mail imap/smtp: read MIME body: %w", err)
	}
	compact := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n':
			return -1
		default:
			return r
		}
	}, string(body))
	record, err := base64.StdEncoding.DecodeString(compact)
	if err != nil {
		return fabric.Envelope{}, "", fmt.Errorf("mail imap/smtp: decode MIME base64: %w", err)
	}
	if !bytes.HasPrefix(record, []byte(mailRecordPrefix)) {
		return fabric.Envelope{}, "", fmt.Errorf("mail imap/smtp: wtmail1 record prefix missing")
	}
	sealed, err := base64.RawURLEncoding.DecodeString(string(record[len(mailRecordPrefix):]))
	if err != nil {
		return fabric.Envelope{}, "", fmt.Errorf("mail imap/smtp: decode wtmail1 record: %w", err)
	}
	if cipher == nil {
		return fabric.Envelope{}, "", fmt.Errorf("mail imap/smtp: envelope cipher is required")
	}
	envelope, err := cipher.OpenWithAAD(sealed, mailRecordAAD(marker))
	if err != nil {
		return fabric.Envelope{}, "", fmt.Errorf("mail imap/smtp: open wtmail1 envelope: %w", err)
	}
	if err := envelope.Validate(); err != nil {
		return fabric.Envelope{}, "", fmt.Errorf("mail imap/smtp: validate envelope: %w", err)
	}
	return envelope, marker, nil
}

func mailEnvelopeCipher(cfg MailIMAPSMTPConfig) (*fabric.EnvelopeCipher, error) {
	// Mail provider credentials never leave TokenStore-backed construction.
	// Domain-separate all four required parts so two accounts sharing one SMTP
	// or IMAP password still derive different at-rest carrier keys.
	material := strings.Join([]string{
		"mail.imap_smtp", cfg.AccountID, cfg.SMTPUsername, cfg.SMTPPassword,
		cfg.IMAPUsername, cfg.IMAPPassword,
	}, "\x00")
	key := fabric.DeriveBootstrapKey(material)
	cipher, err := fabric.NewSessionCipher(key)
	if err != nil {
		return nil, fmt.Errorf("mail imap/smtp: create envelope cipher: %w", err)
	}
	return cipher, nil
}

func mailRecordAAD(marker string) []byte {
	return []byte(mailWireVersion + "\x00" + marker)
}

func mailDeliveryID(raw []byte) (string, error) {
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("mail imap/smtp: parse delivery ID: %w", err)
	}
	id := strings.TrimSpace(message.Header.Get("Message-ID"))
	if id == "" {
		return "", fmt.Errorf("mail imap/smtp: Message-ID missing")
	}
	return id, nil
}

func mailEndpointMarker(endpoint Endpoint) string {
	sum := sha256.Sum256([]byte(endpoint.ID + "\x00" + endpoint.Address))
	return hex.EncodeToString(sum[:12])
}
