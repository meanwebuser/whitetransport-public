package carriers

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DNSTXTServer is the authoritative exit for the dns.txt carrier. It answers
// TXT queries for one delegated zone (for example wt.example.com delegated to
// the exit host) and keeps per-mailbox envelope queues in memory. Clients
// reach it through any recursive resolver — Yandex 77.88.8.8 under whitelist
// censorship — so the exit itself never needs direct client reachability.
//
// Wire layout (labels left of the zone, parsed right to left):
//
//	<mailbox>.h.ZONE                                              hello/probe
//	<mailbox>.p.ZONE                                              poll queue summary
//	<data labels>.<msgid>.<seq>.<total>.<mailbox>.u.ZONE          upload chunk
//	<seq>.<msgid>.<mailbox>.g.ZONE                                fetch downstream chunk
//	<msgid>.<mailbox>.a.ZONE                                      acknowledge message
//
// Mailbox names, message ids and chunk payloads are carrier-visible routing
// metadata, exactly like VK peer ids; envelope confidentiality comes from the
// fabric wtenc1 session cipher.
const (
	dnsTXTMaxMailboxLen    = 24
	dnsTXTMsgIDLen         = 8
	dnsTXTMaxUpDataLabels  = 8
	dnsTXTMaxQueuePerBox   = 32
	dnsTXTDownChunkBytes   = 170
	dnsTXTMaxPollEntries   = 6
	dnsTXTIncomingTTL      = 2 * time.Minute
	dnsTXTQueueTTL         = 15 * time.Minute
	dnsTXTAckMemoryTTL     = 30 * time.Minute
	dnsTXTSweepInterval    = time.Minute
	dnsTXTMaxPacketSize    = 1232
	dnsTXTMaxMessageStored = 8 << 10
)

// DNSTXTServerConfig configures the authoritative exit.
type DNSTXTServerConfig struct {
	// Zone is the delegated suffix, e.g. "wt.example.com".
	Zone string
	// LogFn receives one-line diagnostics; nil uses the log package.
	LogFn func(format string, args ...any)
	// Clock overrides time.Now in tests.
	Now func() time.Time
}

// DNSTXTServer answers TXT queries for its zone.
type DNSTXTServer struct {
	zone       string
	zoneLabels int
	store      *dnsMailboxStore
	logFn      func(format string, args ...any)
	now        func() time.Time
}

type dnsIncomingMsg struct {
	total     int
	chunks    map[int]string
	updatedAt time.Time
}

type dnsQueuedMsg struct {
	msgid      string
	chunks     [][]byte
	enqueuedAt time.Time
}

type dnsMailboxStore struct {
	mu       sync.Mutex
	incoming map[string]map[string]*dnsIncomingMsg
	queue    map[string][]*dnsQueuedMsg
	acked    map[string]map[string]time.Time
}

func newDNSMailboxStore() *dnsMailboxStore {
	return &dnsMailboxStore{
		incoming: map[string]map[string]*dnsIncomingMsg{},
		queue:    map[string][]*dnsQueuedMsg{},
		acked:    map[string]map[string]time.Time{},
	}
}

// NewDNSTXTServer creates the authoritative exit for one zone.
func NewDNSTXTServer(cfg DNSTXTServerConfig) (*DNSTXTServer, error) {
	zone := strings.ToLower(strings.Trim(strings.TrimSpace(cfg.Zone), "."))
	if zone == "" || !strings.Contains(zone, ".") {
		return nil, fmt.Errorf("dns txt server requires a delegated zone with at least two labels, got %q", cfg.Zone)
	}
	logFn := cfg.LogFn
	if logFn == nil {
		logFn = log.Printf
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &DNSTXTServer{
		zone:       zone,
		zoneLabels: len(strings.Split(zone, ".")),
		store:      newDNSMailboxStore(),
		logFn:      logFn,
		now:        now,
	}, nil
}

// Zone returns the configured zone (no trailing dot).
func (s *DNSTXTServer) Zone() string { return s.zone }

// HandleQuery processes one raw DNS query and returns the raw response. It
// never returns an error; malformed queries get FORMERR/SERVFAIL responses.
func (s *DNSTXTServer) HandleQuery(query []byte) []byte {
	defer func() {
		if r := recover(); r != nil {
			s.logFn("[dnstxt] panic recovered: %v", r)
		}
	}()
	response, handled := s.handleQuery(query)
	if !handled {
		return nil
	}
	return response
}

func (s *DNSTXTServer) handleQuery(query []byte) ([]byte, bool) {
	if len(query) < 12 {
		return nil, false
	}
	qdCount := int(query[4])<<8 | int(query[5])
	if qdCount != 1 {
		return dnsBuildTXTResponse(query, nil, dnsRcodeFormatErr), true
	}
	nameEnd, err := dnsSkipName(query, 12)
	if err != nil || nameEnd+4 > len(query) {
		return dnsBuildTXTResponse(query, nil, dnsRcodeFormatErr), true
	}
	qType := int(query[nameEnd])<<8 | int(query[nameEnd+1])
	if qType != dnsTypeTXT {
		return dnsBuildTXTResponse(query, nil, dnsRcodeOK), true
	}
	wireName := query[12:nameEnd]
	labels := dnsWireLabels(wireName)
	if len(labels) <= s.zoneLabels {
		return dnsBuildTXTResponse(query, nil, dnsRcodeRefused), true
	}
	relative := labels[:len(labels)-s.zoneLabels]
	for i, label := range relative {
		relative[i] = strings.ToLower(label)
	}
	zone := strings.ToLower(strings.Join(labels[len(labels)-s.zoneLabels:], "."))
	if zone != s.zone {
		return dnsBuildTXTResponse(query, nil, dnsRcodeRefused), true
	}
	op := relative[len(relative)-1]
	args := relative[:len(relative)-1]
	var answer []string
	var rcode = dnsRcodeOK
	switch op {
	case "h":
		answer = s.handleHello(args)
	case "p":
		answer = s.handlePoll(args)
	case "u":
		answer, rcode = s.handleUpload(args)
	case "g":
		answer, rcode = s.handleGet(args)
	case "a":
		answer, rcode = s.handleAck(args)
	default:
		// Unknown op inside the zone: NOERROR with no answer keeps
		// recursive resolvers from retry-storming a REFUSED response.
		return dnsBuildTXTResponse(query, nil, dnsRcodeOK), true
	}
	return dnsBuildTXTResponse(query, answer, rcode), true
}

func (s *DNSTXTServer) handleHello(args []string) []string {
	if len(args) != 1 {
		return nil
	}
	return []string{fmt.Sprintf("%s ok maxmsg=%d downchunk=%d", dnsTXTHelloPrefix, dnsTXTMaxMessageStored, dnsTXTDownChunkBytes)}
}

func (s *DNSTXTServer) handlePoll(args []string) []string {
	if len(args) != 1 {
		return nil
	}
	mailbox := dnsTXTMailboxLabel(args[0])
	entries := s.store.poll(mailbox, s.now())
	if len(entries) == 0 {
		return []string{dnsTXTHelloPrefix + " 0"}
	}
	parts := make([]string, 0, len(entries)+2)
	parts = append(parts, dnsTXTHelloPrefix, strconv.Itoa(len(entries)))
	for _, entry := range entries {
		parts = append(parts, fmt.Sprintf("%s:%d", entry.msgid, entry.total))
	}
	return []string{strings.Join(parts, " ")}
}

func (s *DNSTXTServer) handleUpload(args []string) ([]string, int) {
	// args: <data...> <msgid> <seq> <total> <mailbox>
	if len(args) < 5 || len(args) > dnsTXTMaxUpDataLabels+4 {
		return nil, dnsRcodeFormatErr
	}
	mailbox := dnsTXTMailboxLabel(args[len(args)-1])
	total, totalErr := strconv.Atoi(args[len(args)-2])
	seq, seqErr := strconv.Atoi(args[len(args)-3])
	msgid := strings.ToLower(args[len(args)-4])
	data := strings.Join(args[:len(args)-4], "")
	if total <= 0 || total > 4096 || seq < 0 || seq >= total || totalErr != nil || seqErr != nil {
		return nil, dnsRcodeFormatErr
	}
	if len(msgid) != dnsTXTMsgIDLen {
		return nil, dnsRcodeFormatErr
	}
	if _, err := hex.DecodeString(msgid); err != nil {
		return nil, dnsRcodeFormatErr
	}
	if _, err := dnsDecodeUpChunk(data); err != nil {
		return nil, dnsRcodeFormatErr
	}
	dup := s.store.uploadChunk(mailbox, msgid, seq, total, data, s.now())
	if dup {
		return []string{"dup"}, dnsRcodeOK
	}
	return []string{"ok"}, dnsRcodeOK
}

func (s *DNSTXTServer) handleGet(args []string) ([]string, int) {
	// args: <seq> <msgid> <mailbox>
	if len(args) != 3 {
		return nil, dnsRcodeFormatErr
	}
	mailbox := dnsTXTMailboxLabel(args[2])
	msgid := strings.ToLower(args[1])
	seq, err := strconv.Atoi(args[0])
	if err != nil || seq < 0 {
		return nil, dnsRcodeFormatErr
	}
	chunk, ok := s.store.getChunk(mailbox, msgid, seq, s.now())
	if !ok {
		return nil, dnsRcodeNXDomain
	}
	return []string{dnsEncodeDownChunk(chunk)}, dnsRcodeOK
}

func (s *DNSTXTServer) handleAck(args []string) ([]string, int) {
	// args: <msgid> <mailbox>
	if len(args) != 2 {
		return nil, dnsRcodeFormatErr
	}
	mailbox := dnsTXTMailboxLabel(args[1])
	msgid := strings.ToLower(args[0])
	s.store.ack(mailbox, msgid, s.now())
	return []string{"ok"}, dnsRcodeOK
}

// poll/queue/incoming plumbing with TTL semantics.

type dnsTXTQueueEntry struct {
	msgid string
	total int
}

func (st *dnsMailboxStore) poll(mailbox string, now time.Time) []dnsTXTQueueEntry {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.sweepLocked(now)
	queued := st.queue[mailbox]
	if len(queued) == 0 {
		return nil
	}
	entries := make([]dnsTXTQueueEntry, 0, len(queued))
	for _, msg := range queued {
		entries = append(entries, dnsTXTQueueEntry{msgid: msg.msgid, total: len(msg.chunks)})
		if len(entries) == dnsTXTMaxPollEntries {
			break
		}
	}
	return entries
}

func (st *dnsMailboxStore) uploadChunk(mailbox, msgid string, seq, total int, data string, now time.Time) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.sweepLocked(now)
	if st.acked[mailbox] != nil {
		if _, done := st.acked[mailbox][msgid]; done {
			return true
		}
	}
	for _, queued := range st.queue[mailbox] {
		if queued.msgid == msgid {
			return true
		}
	}
	box := st.incoming[mailbox]
	if box == nil {
		box = map[string]*dnsIncomingMsg{}
		st.incoming[mailbox] = box
	}
	msg := box[msgid]
	if msg == nil {
		msg = &dnsIncomingMsg{total: total, chunks: map[int]string{}}
		box[msgid] = msg
	}
	msg.updatedAt = now
	msg.chunks[seq] = data
	if len(msg.chunks) < msg.total {
		return false
	}
	// All chunks arrived: reassemble, decode and enqueue for delivery.
	// Each chunk carries its own base32 stream, so chunks are decoded
	// individually — concatenated base32 of arbitrary-length chunks would
	// not decode as one stream.
	var assembled []byte
	for i := 0; i < msg.total; i++ {
		decoded, err := dnsDecodeUpChunk(msg.chunks[i])
		if err != nil {
			delete(box, msgid)
			return false
		}
		assembled = append(assembled, decoded...)
	}
	raw := assembled
	delete(box, msgid)
	if len(raw) == 0 || len(raw) > dnsTXTMaxMessageStored {
		return false
	}
	queued := &dnsQueuedMsg{msgid: msgid, enqueuedAt: now}
	for offset := 0; offset < len(raw); offset += dnsTXTDownChunkBytes {
		end := offset + dnsTXTDownChunkBytes
		if end > len(raw) {
			end = len(raw)
		}
		queued.chunks = append(queued.chunks, raw[offset:end])
	}
	st.queue[mailbox] = append(st.queue[mailbox], queued)
	if len(st.queue[mailbox]) > dnsTXTMaxQueuePerBox {
		st.queue[mailbox] = st.queue[mailbox][len(st.queue[mailbox])-dnsTXTMaxQueuePerBox:]
	}
	return false
}

func (st *dnsMailboxStore) getChunk(mailbox, msgid string, seq int, now time.Time) ([]byte, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.sweepLocked(now)
	for _, msg := range st.queue[mailbox] {
		if msg.msgid != msgid {
			continue
		}
		if seq >= len(msg.chunks) {
			return nil, false
		}
		return msg.chunks[seq], true
	}
	return nil, false
}

func (st *dnsMailboxStore) ack(mailbox, msgid string, now time.Time) {
	st.mu.Lock()
	defer st.mu.Unlock()
	queued := st.queue[mailbox]
	for i, msg := range queued {
		if msg.msgid == msgid {
			st.queue[mailbox] = append(queued[:i:i], queued[i+1:]...)
			break
		}
	}
	if st.acked[mailbox] == nil {
		st.acked[mailbox] = map[string]time.Time{}
	}
	st.acked[mailbox][msgid] = now
}

// sweepLocked drops stale reassemblies, expired queue entries and old ack
// memories. Callers must hold st.mu.
func (st *dnsMailboxStore) sweepLocked(now time.Time) {
	for mailbox, box := range st.incoming {
		for msgid, msg := range box {
			if now.Sub(msg.updatedAt) > dnsTXTIncomingTTL {
				delete(box, msgid)
			}
		}
		if len(box) == 0 {
			delete(st.incoming, mailbox)
		}
	}
	for mailbox, queued := range st.queue {
		kept := queued[:0]
		for _, msg := range queued {
			if now.Sub(msg.enqueuedAt) <= dnsTXTQueueTTL {
				kept = append(kept, msg)
			}
		}
		if len(kept) == 0 {
			delete(st.queue, mailbox)
		} else {
			st.queue[mailbox] = kept
		}
	}
	for mailbox, acks := range st.acked {
		for msgid, at := range acks {
			if now.Sub(at) > dnsTXTAckMemoryTTL {
				delete(acks, msgid)
			}
		}
		if len(acks) == 0 {
			delete(st.acked, mailbox)
		}
	}
}

// dnsWireLabels splits a wire-format name into dotted labels, preserving the
// original case.
func dnsWireLabels(wireName []byte) []string {
	var labels []string
	offset := 0
	for offset < len(wireName) {
		length := int(wireName[offset])
		if length == 0 || offset+1+length > len(wireName) {
			break
		}
		labels = append(labels, string(wireName[offset+1:offset+1+length]))
		offset += 1 + length
	}
	return labels
}

// dnsTXTMailboxLabel normalizes an endpoint address into a DNS-safe mailbox
// label.
func dnsTXTMailboxLabel(address string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(address)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == ' ' || r == '/' || r == '_' || r == '.':
			b.WriteByte('-')
		}
	}
	label := strings.Trim(b.String(), "-")
	if label == "" {
		return "default"
	}
	if len(label) > dnsTXTMaxMailboxLen {
		label = label[:dnsTXTMaxMailboxLen-9] + "-" + hex.EncodeToString([]byte(address))[:8]
	}
	return label
}

// ServeUDP reads queries from a UDP socket until ctx is done.
func (s *DNSTXTServer) ServeUDP(ctx context.Context, conn net.PacketConn) error {
	defer conn.Close()
	buf := make([]byte, dnsTXTMaxPacketSize*2)
	go s.sweepLoop(ctx)
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if n > dnsTXTMaxPacketSize || n < 12 {
			continue
		}
		response := s.HandleQuery(buf[:n])
		if response == nil {
			continue
		}
		if _, err := conn.WriteTo(response, addr); err != nil {
			s.logFn("[dnstxt] udp write failed: %v", err)
		}
	}
}

// ServeTCP serves DNS-over-TCP frames (2-byte length prefix) until ctx is
// done. TCP is the fallback path when a resolver sees the TC bit.
func (s *DNSTXTServer) ServeTCP(ctx context.Context, listener net.Listener) error {
	defer listener.Close()
	go s.sweepLoop(ctx)
	for {
		if err := ctx.Err(); err != nil {
			return ctx.Err()
		}
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		go func() {
			defer conn.Close()
			for {
				if ctx.Err() != nil {
					return
				}
				query, err := dnsReadTCPFrame(conn)
				if err != nil {
					return
				}
				response := s.HandleQuery(query)
				if response == nil {
					return
				}
				if err := dnsWriteTCPFrame(conn, response); err != nil {
					return
				}
			}
		}()
	}
}

func (s *DNSTXTServer) sweepLoop(ctx context.Context) {
	ticker := time.NewTicker(dnsTXTSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.store.mu.Lock()
			s.store.sweepLocked(s.now())
			s.store.mu.Unlock()
		}
	}
}

func dnsReadTCPFrame(conn net.Conn) ([]byte, error) {
	var lengthBuf [2]byte
	if _, err := io.ReadFull(conn, lengthBuf[:]); err != nil {
		return nil, err
	}
	length := int(binary.BigEndian.Uint16(lengthBuf[:]))
	if length == 0 || length > 65535 {
		return nil, fmt.Errorf("invalid dns tcp frame length %d", length)
	}
	frame := make([]byte, length)
	if _, err := io.ReadFull(conn, frame); err != nil {
		return nil, err
	}
	return frame, nil
}

func dnsWriteTCPFrame(conn net.Conn, frame []byte) error {
	if len(frame) > 65535 {
		return fmt.Errorf("dns tcp frame too large: %d", len(frame))
	}
	var lengthBuf [2]byte
	binary.BigEndian.PutUint16(lengthBuf[:], uint16(len(frame)))
	if _, err := conn.Write(lengthBuf[:]); err != nil {
		return err
	}
	_, err := conn.Write(frame)
	return err
}
