package carriers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	mathrand "math/rand"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

// dns.txt carrier: an iodine-style DNS TXT mailbox. Upstream data rides in
// base32 query-name labels, downstream data comes back as TTL-0 TXT answers,
// and every query flows through a whitelisted recursive resolver (Yandex
// 77.88.8.8 by default) to the authoritative exit. It is a last-resort
// bootstrap/control carrier: slow by design, survivable by design.

const (
	defaultDNSTXTResolverAddr     = "77.88.8.8:53"
	defaultDNSTXTQueryTimeout     = 4 * time.Second
	defaultDNSTXTMinQueryInterval = 100 * time.Millisecond
	defaultDNSTXTMaxUpChunkBytes  = 90
	defaultDNSTXTMaxMessageBytes  = 4096
	dnsTXTQueryAttempts           = 3
	dnsTXTMaxFetchRetries         = 2
	dnsTXTCursorDoneLimit         = 64
)

// DNSTXTExchanger transports one raw DNS query to a resolver or authoritative
// server. Production uses UDP with TCP fallback; tests inject fakes.
type DNSTXTExchanger interface {
	Exchange(ctx context.Context, query []byte) ([]byte, error)
}

// DNSTXTConfig contains runtime inputs for the DNS TXT carrier.
type DNSTXTConfig struct {
	// Zone is the delegated suffix that points at the WhiteTransport DNS
	// exit, e.g. "wt.example.com".
	Zone string
	// ResolverAddr is the recursive resolver used for every query. The
	// Yandex default is reachable under whitelist censorship.
	ResolverAddr string
	// DirectAddr bypasses recursion and talks straight to the authoritative
	// exit (exit-side or fast-path deployments).
	DirectAddr string
	// QueryTimeout bounds one DNS exchange.
	QueryTimeout time.Duration
	// MinQueryInterval spaces queries to stay polite to the resolver.
	MinQueryInterval time.Duration
	// MaxUpChunkBytes is the raw payload per upstream query; the
	// constructor shrinks it to fit the 253-byte query-name budget.
	MaxUpChunkBytes int
	// MaxMessageBytes caps one envelope before fabric-level chunking.
	MaxMessageBytes int
	// Exchange overrides the network transport (tests).
	Exchange DNSTXTExchanger
	// Rand drives 0x20 case randomization (tests).
	Rand *mathrand.Rand
}

// DNSTXTCarrier moves envelopes through DNS TXT records.
type DNSTXTCarrier struct {
	BaseCarrier
	zone         string
	resolverAddr string
	directAddr   string
	timeout      time.Duration
	minInterval  time.Duration
	maxUpChunk   int
	maxMessage   int
	exchange     DNSTXTExchanger
	rng          *mathrand.Rand
	cipher       *fabric.EnvelopeCipher
	mu           sync.Mutex
	lastQueryAt  time.Time
	desc         Descriptor
}

type dnsTXTCursor struct {
	Done []string `json:"done,omitempty"`
}

// NewDNSTXTCarrier creates the DNS TXT mailbox carrier.
func NewDNSTXTCarrier(cfg DNSTXTConfig) (*DNSTXTCarrier, error) {
	zone := strings.ToLower(strings.Trim(strings.TrimSpace(cfg.Zone), "."))
	if zone == "" || !strings.Contains(zone, ".") {
		return nil, fmt.Errorf("dns txt carrier requires a zone with at least two labels, got %q", cfg.Zone)
	}
	resolver := strings.TrimSpace(cfg.ResolverAddr)
	if resolver == "" {
		resolver = defaultDNSTXTResolverAddr
	}
	if cfg.Exchange == nil && cfg.DirectAddr == "" && !strings.Contains(resolver, ":") {
		resolver = net.JoinHostPort(resolver, "53")
	}
	timeout := cfg.QueryTimeout
	if timeout <= 0 {
		timeout = defaultDNSTXTQueryTimeout
	}
	interval := cfg.MinQueryInterval
	if interval < 0 {
		interval = 0
	}
	maxMessage := cfg.MaxMessageBytes
	if maxMessage <= 0 {
		maxMessage = defaultDNSTXTMaxMessageBytes
	}
	maxUpChunk := cfg.MaxUpChunkBytes
	if maxUpChunk <= 0 {
		maxUpChunk = defaultDNSTXTMaxUpChunkBytes
	}
	if maxUpChunk > dnsTXTMaxUpDataLabels*dnsTXTMaxLabel*5/8 {
		maxUpChunk = dnsTXTMaxUpDataLabels * dnsTXTMaxLabel * 5 / 8
	}
	var exchange DNSTXTExchanger = cfg.Exchange
	if exchange == nil {
		target := resolver
		if cfg.DirectAddr != "" {
			target = cfg.DirectAddr
		}
		exchange = &dnsUDPExchanger{addr: target, timeout: timeout}
	}
	rng := cfg.Rand
	if rng == nil {
		seed, err := rand.Int(rand.Reader, big.NewInt(1<<62))
		if err != nil {
			return nil, fmt.Errorf("dns txt carrier seed: %w", err)
		}
		rng = mathrand.New(mathrand.NewSource(seed.Int64()))
	}
	desc, err := FindStandardDescriptor(CarrierDNSTXT)
	if err != nil {
		return nil, err
	}
	desc.Limits.MaxPayloadBytes = maxMessage
	carrier := &DNSTXTCarrier{
		zone:         zone,
		resolverAddr: resolver,
		directAddr:   cfg.DirectAddr,
		timeout:      timeout,
		minInterval:  interval,
		maxUpChunk:   maxUpChunk,
		maxMessage:   maxMessage,
		exchange:     exchange,
		rng:          rng,
		desc:         desc,
	}
	carrier.maxUpChunk = carrier.fitUpChunk()
	return carrier, nil
}

// fitUpChunk shrinks the upstream chunk so the worst-case upload query name
// (chunk labels + msgid + seq + total + mailbox + op + zone) stays inside the
// 253-character budget.
func (c *DNSTXTCarrier) fitUpChunk() int {
	for chunk := c.maxUpChunk; chunk >= 20; chunk -= 10 {
		encoded := dnsEncodeUpChunk(make([]byte, chunk))
		name := strings.Join(dnsSplitB32Labels(encoded), ".") +
			".aaaaaaaa.999.999." + strings.Repeat("m", dnsTXTMaxMailboxLen) + ".u." + c.zone
		if len(name) <= dnsTXTMaxQueryName {
			return chunk
		}
	}
	return 20
}

func (c *DNSTXTCarrier) Descriptor() Descriptor { return c.desc }
func (c *DNSTXTCarrier) IsNative()              {}

// SetCipher enables AES-256-GCM encryption of envelopes over the DNS medium.
func (c *DNSTXTCarrier) SetCipher(cipher *fabric.EnvelopeCipher) { c.cipher = cipher }

// Write fragments one envelope into upstream DNS queries addressed to the
// peer mailbox named by the endpoint.
func (c *DNSTXTCarrier) Write(ctx context.Context, endpoint Endpoint, envelope fabric.Envelope) error {
	text, err := encodeMailboxEnvelope(envelope, c.cipher)
	if err != nil {
		return err
	}
	if len(text) > c.maxMessage {
		return fmt.Errorf("dns txt envelope size %d exceeds carrier limit %d", len(text), c.maxMessage)
	}
	mailbox := dnsTXTMailboxLabel(endpoint.Address)
	msgid, err := dnsTXTNewMsgID()
	if err != nil {
		return err
	}
	raw := []byte(text)
	total := (len(raw) + c.maxUpChunk - 1) / c.maxUpChunk
	if total > 4096 {
		return fmt.Errorf("dns txt envelope needs %d chunks", total)
	}
	for seq := 0; seq < total; seq++ {
		start := seq * c.maxUpChunk
		end := start + c.maxUpChunk
		if end > len(raw) {
			end = len(raw)
		}
		encoded := dnsEncodeUpChunk(raw[start:end])
		labels := dnsSplitB32Labels(encoded)
		suffix := strings.Join([]string{msgid, strconv.Itoa(seq), strconv.Itoa(total), mailbox, "u", c.zone}, ".")
		name := strings.Join(append(labels, suffix), ".")
		if _, err := c.exchangeTXT(ctx, name, true); err != nil {
			return fmt.Errorf("dns txt upload %s chunk %d/%d: %w", msgid, seq+1, total, err)
		}
	}
	return nil
}

// Read polls the mailbox queue, fetches complete messages chunk by chunk and
// acknowledges everything it decoded.
func (c *DNSTXTCarrier) Read(ctx context.Context, endpoint Endpoint, cursor Cursor) (ReadResult, error) {
	cur, err := parseDNSTXTCursor(cursor)
	if err != nil {
		return ReadResult{}, err
	}
	mailbox := dnsTXTMailboxLabel(endpoint.Address)
	pollName := strings.Join([]string{mailbox, "p", c.zone}, ".")
	resp, err := c.exchangeTXT(ctx, pollName, false)
	if err != nil {
		return ReadResult{}, fmt.Errorf("dns txt poll %s: %w", mailbox, err)
	}
	entries, err := dnsTXTParsePoll(resp)
	if err != nil {
		return ReadResult{}, err
	}
	envelopes := make([]fabric.Envelope, 0, len(entries))
	for _, entry := range entries {
		if cur.has(entry.msgid) {
			continue
		}
		env, err := c.fetchMessage(ctx, mailbox, entry)
		if err != nil {
			// Incomplete fetches retry on the next Read; already
			// decoded messages are returned without stalling.
			if len(envelopes) > 0 {
				break
			}
			return ReadResult{}, err
		}
		if env != nil {
			envelopes = append(envelopes, *env)
		}
		cur.add(entry.msgid)
	}
	encodedCursor, err := json.Marshal(cur)
	if err != nil {
		return ReadResult{}, err
	}
	return ReadResult{Envelopes: envelopes, Cursor: Cursor(encodedCursor)}, nil
}

// fetchMessage downloads all chunks of one queued message, decodes it and
// acknowledges it. A message that decodes to garbage is acked and skipped so
// a corrupted entry cannot wedge the mailbox.
func (c *DNSTXTCarrier) fetchMessage(ctx context.Context, mailbox string, entry dnsTXTQueueEntry) (*fabric.Envelope, error) {
	raw := make([]byte, 0, entry.total*dnsTXTDownChunkBytes)
	for seq := 0; seq < entry.total; seq++ {
		var chunk []byte
		var lastErr error
		for attempt := 0; attempt <= dnsTXTMaxFetchRetries; attempt++ {
			name := strings.Join([]string{strconv.Itoa(seq), entry.msgid, mailbox, "g", c.zone}, ".")
			resp, err := c.exchangeTXT(ctx, name, false)
			if err != nil {
				lastErr = err
				continue
			}
			if len(resp.Answers) == 0 || len(resp.Answers[0].Strings) == 0 {
				lastErr = fmt.Errorf("chunk %d of %s missing", seq, entry.msgid)
				continue
			}
			decoded, err := dnsDecodeDownChunk(resp.Answers[0].Strings[0])
			if err != nil {
				lastErr = err
				continue
			}
			chunk = decoded
			break
		}
		if chunk == nil {
			return nil, fmt.Errorf("dns txt fetch %s: %w", entry.msgid, lastErr)
		}
		raw = append(raw, chunk...)
	}
	ackName := strings.Join([]string{entry.msgid, mailbox, "a", c.zone}, ".")
	if _, err := c.exchangeTXT(ctx, ackName, false); err != nil {
		return nil, fmt.Errorf("dns txt ack %s: %w", entry.msgid, err)
	}
	env, err := decodeMailboxEnvelope(string(raw), c.cipher)
	if err != nil {
		// Poison pill: acked above, dropped, reported as skipped.
		return nil, nil
	}
	return &env, nil
}

// Probe checks tunnel reachability with a hello query.
func (c *DNSTXTCarrier) Probe(ctx context.Context, endpoint Endpoint) (Metrics, error) {
	mailbox := dnsTXTMailboxLabel(endpoint.Address)
	name := strings.Join([]string{mailbox, "h", c.zone}, ".")
	start := time.Now()
	resp, err := c.exchangeTXT(ctx, name, false)
	latency := time.Since(start)
	if err != nil {
		return Metrics{Healthy: false, FailureReason: err.Error()}, err
	}
	if len(resp.Answers) == 0 || !strings.HasPrefix(firstString(resp.Answers[0]), dnsTXTHelloPrefix) {
		reason := fmt.Sprintf("dns txt exit did not answer hello for zone %s", c.zone)
		return Metrics{Healthy: false, FailureReason: reason}, fmt.Errorf("%s", reason)
	}
	return Metrics{Healthy: true, Latency: latency, LastOK: time.Now(), BandwidthBPS: c.desc.Metrics.BandwidthBPS}, nil
}

// SafeEgressRecoveryProbe is safe for background use: a hello exchange moves
// no envelope payloads.
func (c *DNSTXTCarrier) SafeEgressRecoveryProbe(ctx context.Context, endpoint Endpoint) (Metrics, error) {
	return c.Probe(ctx, endpoint)
}

// exchangeTXT performs one case-randomized TXT query with retries and maps
// DNS rcodes onto carrier errors.
func (c *DNSTXTCarrier) exchangeTXT(ctx context.Context, name string, randomizeData bool) (dnsParsedResponse, error) {
	labels := strings.Split(name, ".")
	var lastErr error
	for attempt := 0; attempt < dnsTXTQueryAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return dnsParsedResponse{}, err
		}
		if err := c.waitQueryBudget(ctx); err != nil {
			return dnsParsedResponse{}, err
		}
		queryLabels := labels
		if randomizeData {
			queryLabels = make([]string, len(labels))
			for i, label := range labels {
				queryLabels[i] = dnsRandomCase(label, c.rng)
			}
		}
		queryName := strings.Join(queryLabels, ".")
		query, err := dnsBuildTXTQuery(uint16(c.rng.Intn(1<<16)), queryName, true)
		if err != nil {
			return dnsParsedResponse{}, err
		}
		responseBytes, err := c.exchange.Exchange(ctx, query)
		if err != nil {
			lastErr = err
			continue
		}
		response, err := dnsParseResponse(responseBytes)
		if err != nil {
			lastErr = err
			continue
		}
		switch response.RCode {
		case dnsRcodeOK:
			return response, nil
		case dnsRcodeNXDomain:
			return response, fmt.Errorf("dns txt zone %s not resolvable (NXDOMAIN): check NS delegation and resolver %s", c.zone, c.resolverAddr)
		case dnsRcodeRefused:
			return response, fmt.Errorf("dns txt exit refused query for zone %s", c.zone)
		default:
			lastErr = fmt.Errorf("dns txt query rcode %d", response.RCode)
		}
	}
	return dnsParsedResponse{}, fmt.Errorf("dns txt exchange failed after %d attempts: %w", dnsTXTQueryAttempts, lastErr)
}

func (c *DNSTXTCarrier) waitQueryBudget(ctx context.Context) error {
	if c.minInterval <= 0 {
		return nil
	}
	c.mu.Lock()
	wait := c.lastQueryAt.Add(c.minInterval).Sub(time.Now())
	if wait <= 0 {
		c.lastQueryAt = time.Now()
		c.mu.Unlock()
		return nil
	}
	c.lastQueryAt = time.Now().Add(wait)
	c.mu.Unlock()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func dnsTXTNewMsgID() (string, error) {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

func dnsTXTParsePoll(resp dnsParsedResponse) ([]dnsTXTQueueEntry, error) {
	if len(resp.Answers) == 0 || len(resp.Answers[0].Strings) == 0 {
		return nil, fmt.Errorf("dns txt poll returned no TXT answer")
	}
	tokens := strings.Fields(resp.Answers[0].Strings[0])
	if len(tokens) < 2 || tokens[0] != dnsTXTHelloPrefix {
		return nil, fmt.Errorf("dns txt poll answer not from a wtdns exit: %q", resp.Answers[0].Strings[0])
	}
	entries := make([]dnsTXTQueueEntry, 0, len(tokens)-2)
	for _, token := range tokens[2:] {
		msgid, totalText, ok := strings.Cut(token, ":")
		if !ok || len(msgid) != dnsTXTMsgIDLen {
			return nil, fmt.Errorf("dns txt poll entry malformed: %q", token)
		}
		total, err := strconv.Atoi(totalText)
		if err != nil || total <= 0 || total > 4096 {
			return nil, fmt.Errorf("dns txt poll entry total malformed: %q", token)
		}
		entries = append(entries, dnsTXTQueueEntry{msgid: strings.ToLower(msgid), total: total})
	}
	return entries, nil
}

func parseDNSTXTCursor(cursor Cursor) (dnsTXTCursor, error) {
	trimmed := strings.TrimSpace(string(cursor))
	if trimmed == "" {
		return dnsTXTCursor{}, nil
	}
	var out dnsTXTCursor
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return dnsTXTCursor{}, fmt.Errorf("invalid dns txt cursor: %w", err)
	}
	return out, nil
}

func (c dnsTXTCursor) has(msgid string) bool {
	for _, done := range c.Done {
		if done == msgid {
			return true
		}
	}
	return false
}

func (c *dnsTXTCursor) add(msgid string) {
	c.Done = append(c.Done, msgid)
	if len(c.Done) > dnsTXTCursorDoneLimit {
		c.Done = c.Done[len(c.Done)-dnsTXTCursorDoneLimit:]
	}
}

// dnsUDPExchanger sends queries over UDP with EDNS0 and falls back to TCP
// when the answer comes back truncated.
type dnsUDPExchanger struct {
	addr    string
	timeout time.Duration
}

func (e *dnsUDPExchanger) Exchange(ctx context.Context, query []byte) ([]byte, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", e.addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline := time.Now().Add(e.timeout)
	_ = conn.SetDeadline(deadline)
	if _, err := conn.Write(query); err != nil {
		return nil, err
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	response := buf[:n]
	if len(response) >= 4 && response[2]&byte(dnsFlagTC>>8) != 0 {
		return e.exchangeTCP(ctx, query)
	}
	return response, nil
}

func (e *dnsUDPExchanger) exchangeTCP(ctx context.Context, query []byte) ([]byte, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", e.addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(e.timeout))
	if err := dnsWriteTCPFrame(conn, query); err != nil {
		return nil, err
	}
	return dnsReadTCPFrame(conn)
}

func firstString(answer dnsTXTAnswer) string {
	if len(answer.Strings) == 0 {
		return ""
	}
	return answer.Strings[0]
}
