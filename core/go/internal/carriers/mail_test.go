package carriers

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

func TestMailCarrierMIMEAndOversizePreflight(t *testing.T) {
	smtpClient := &fakeMailSMTPClient{}
	carrier := newTestMailCarrier(t, smtpClient, &fakeMailIMAPClient{})
	endpoint := Endpoint{ID: "mail.primary", Address: "egress-primary"}
	envelope := fabric.NewEnvelope("mail-env-1", fabric.TrafficEgress, "test.payload", []byte("encrypted-envelope-placeholder"))
	if err := carrier.Write(context.Background(), endpoint, envelope); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(smtpClient.messages) != 1 {
		t.Fatalf("SMTP submissions = %d, want 1", len(smtpClient.messages))
	}
	raw := string(smtpClient.messages[0])
	if strings.Contains(raw, "encrypted-envelope-placeholder") {
		t.Fatal("MIME wire exposed the envelope body without transfer encoding")
	}
	if !strings.Contains(raw, "Content-Transfer-Encoding: base64\r\n") {
		t.Fatalf("MIME message lacks base64 transfer encoding:\n%s", raw)
	}
	parts := strings.SplitN(raw, "\r\n\r\n", 2)
	if len(parts) != 2 {
		t.Fatalf("MIME separator missing: %q", raw)
	}
	for _, line := range strings.Split(strings.TrimSpace(parts[1]), "\r\n") {
		if len(line) > 76 {
			t.Fatalf("base64 body line length = %d, want <= 76", len(line))
		}
	}
	compactBody := strings.NewReplacer("\r", "", "\n", "").Replace(parts[1])
	wireRecord, err := base64.StdEncoding.DecodeString(compactBody)
	if err != nil || !strings.HasPrefix(string(wireRecord), mailRecordPrefix) {
		t.Fatalf("MIME body is not a wtmail1 record: record=%q err=%v", wireRecord, err)
	}
	if strings.Contains(string(wireRecord), "encrypted-envelope-placeholder") {
		t.Fatal("wtmail1 record exposed plaintext envelope payload")
	}
	decoded, marker, err := decodeMailEnvelope(smtpClient.messages[0], carrier.cipher)
	if err != nil {
		t.Fatalf("decode MIME: %v", err)
	}
	if decoded.ID != envelope.ID || string(decoded.Payload) != string(envelope.Payload) || marker != mailEndpointMarker(endpoint) {
		t.Fatalf("decoded envelope=%+v marker=%q", decoded, marker)
	}
	wrongConfig := testMailConfig()
	wrongConfig.IMAPPassword = "different-fixture-password"
	wrongCipher, err := mailEnvelopeCipher(wrongConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := decodeMailEnvelope(smtpClient.messages[0], wrongCipher); err == nil {
		t.Fatal("wtmail1 record decrypted with a different TokenStore credential set")
	}
	tampered := []byte(strings.Replace(raw, "X-WhiteTransport-Endpoint: "+mailEndpointMarker(endpoint), "X-WhiteTransport-Endpoint: 000000000000000000000000", 1))
	if _, _, err := decodeMailEnvelope(tampered, carrier.cipher); err == nil {
		t.Fatal("wtmail1 record accepted a tampered endpoint marker")
	}

	oversize := fabric.NewEnvelope("mail-too-large", fabric.TrafficEgress, "test.payload", make([]byte, mailMaxEnvelopeBytes+1))
	if err := carrier.Write(context.Background(), endpoint, oversize); err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("oversize Write error = %v", err)
	}
	if len(smtpClient.messages) != 1 {
		t.Fatal("oversize envelope reached SMTP")
	}
}

func TestIMAPReadLineRejectsOversizedResponse(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		_, _ = server.Write([]byte(strings.Repeat("x", mailMaxIMAPLineBytes+1) + "\r\n"))
	}()
	session := &imapSession{conn: client, in: bufio.NewReader(client), out: bufio.NewWriter(client)}
	if _, err := session.readLine(); err == nil || !strings.Contains(err.Error(), "IMAP line exceeds") {
		t.Fatalf("oversized IMAP line error = %v", err)
	}
	client.Close()
	<-done
}

func TestSMTPAuthenticateRefreshesDeadlinePerPhase(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	tracked := &deadlineCountingConn{Conn: clientConn}
	serverDone := make(chan error, 1)
	go func() {
		defer serverConn.Close()
		reader := bufio.NewReader(serverConn)
		writer := bufio.NewWriter(serverConn)
		if _, err := writer.WriteString("220 mail.example.test ESMTP ready\r\n"); err != nil {
			serverDone <- err
			return
		}
		if err := writer.Flush(); err != nil {
			serverDone <- err
			return
		}
		if line, err := reader.ReadString('\n'); err != nil || !strings.HasPrefix(strings.ToUpper(line), "EHLO ") {
			serverDone <- errors.New("expected EHLO")
			return
		}
		_, _ = writer.WriteString("250-mail.example.test\r\n250 AUTH PLAIN\r\n")
		if err := writer.Flush(); err != nil {
			serverDone <- err
			return
		}
		if line, err := reader.ReadString('\n'); err != nil || !strings.HasPrefix(strings.ToUpper(line), "AUTH PLAIN ") {
			serverDone <- errors.New("expected AUTH PLAIN")
			return
		}
		_, _ = writer.WriteString("235 2.7.0 authenticated\r\n")
		serverDone <- writer.Flush()
	}()

	cfg := testMailConfig()
	// net.Pipe is intentionally plaintext; smtp.PlainAuth permits localhost for
	// protocol unit tests while production connect always supplies a TLS conn.
	cfg.TLSServerName = "localhost"
	client := &smtpMailClient{cfg: cfg}
	session, err := client.authenticate(context.Background(), tracked)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	_ = session.Close()
	if err := <-serverDone; err != nil {
		t.Fatalf("SMTP fixture: %v", err)
	}
	if got := tracked.deadlineCount(); got != 3 {
		t.Fatalf("deadline refreshes = %d, want greeting + EHLO + AUTH", got)
	}
}

func TestMailCarrierUIDCursorPoisonSkipAndNoReplayAcrossEpoch(t *testing.T) {
	endpoint := Endpoint{ID: "mail.control", Address: "control"}
	first := fabric.NewEnvelope("mail-first", fabric.TrafficControl, "test.first", []byte("one"))
	second := fabric.NewEnvelope("mail-second", fabric.TrafficControl, "test.second", []byte("two"))
	cipher, err := mailEnvelopeCipher(testMailConfig())
	if err != nil {
		t.Fatal(err)
	}
	firstRaw, err := encodeMailMessage(testMailConfig(), endpoint, first, cipher)
	if err != nil {
		t.Fatal(err)
	}
	secondRaw, err := encodeMailMessage(testMailConfig(), endpoint, second, cipher)
	if err != nil {
		t.Fatal(err)
	}
	unrelated := []byte("From: other@example.test\r\nTo: client@example.test\r\n\r\nordinary mail\r\n")
	malformed := []byte("X-WhiteTransport-Version: 1\r\nX-WhiteTransport-Endpoint: " + mailEndpointMarker(endpoint) + "\r\n\r\nnot-base64\r\n")
	imapClient := &fakeMailIMAPClient{scans: []mailScanResult{
		{uidValidity: 41, messages: []mailIMAPMessage{{uid: 7, raw: secondRaw}, {uid: 2, raw: unrelated}, {uid: 5, raw: firstRaw}, {uid: 3, raw: malformed}}},
		{uidValidity: 41, messages: []mailIMAPMessage{{uid: 7, raw: secondRaw}}},
		// UIDVALIDITY rollover gives both old IDs new UIDs. The retained ID
		// set must suppress both replays.
		{uidValidity: 42, messages: []mailIMAPMessage{{uid: 1, raw: secondRaw}, {uid: 2, raw: firstRaw}}},
	}}
	carrier := newTestMailCarrier(t, &fakeMailSMTPClient{}, imapClient)

	result, err := carrier.Read(context.Background(), endpoint, "")
	if err != nil {
		t.Fatalf("first Read: %v", err)
	}
	if got := envelopeIDs(result.Envelopes); strings.Join(got, ",") != "mail-first,mail-second" {
		t.Fatalf("first IDs = %v", got)
	}
	state, err := parseMailCursor(result.Cursor, carrier.cursorScope(endpoint))
	if err != nil {
		t.Fatalf("parse cursor: %v", err)
	}
	if state.UIDValidity != 41 || state.HighestScannedUID != 7 || len(state.DeliveredIDs) != 2 {
		t.Fatalf("cursor state = %+v", state)
	}

	noReplay, err := carrier.Read(context.Background(), endpoint, result.Cursor)
	if err != nil || len(noReplay.Envelopes) != 0 {
		t.Fatalf("same epoch replay err=%v envelopes=%+v", err, noReplay.Envelopes)
	}
	rollover, err := carrier.Read(context.Background(), endpoint, noReplay.Cursor)
	if err != nil || len(rollover.Envelopes) != 0 {
		t.Fatalf("rollover replay err=%v envelopes=%+v", err, rollover.Envelopes)
	}
	rolledState, err := parseMailCursor(rollover.Cursor, carrier.cursorScope(endpoint))
	if err != nil || rolledState.UIDValidity != 42 || rolledState.HighestScannedUID != 2 || len(rolledState.DeliveredIDs) != 2 {
		t.Fatalf("rollover cursor err=%v state=%+v", err, rolledState)
	}
	if len(imapClient.highestRequests) != 3 || imapClient.highestRequests[0] != 0 || imapClient.highestRequests[1] != 7 || imapClient.highestRequests[2] != 7 {
		t.Fatalf("highest UID requests = %v", imapClient.highestRequests)
	}
}

func TestMailCarrierAdvancesCursorAcrossEmptyBoundedPages(t *testing.T) {
	endpoint := Endpoint{ID: "mail.control", Address: "control"}
	imapClient := &fakeMailIMAPClient{scans: []mailScanResult{
		{uidValidity: 41, highestScannedUID: 64},
		{uidValidity: 41, highestScannedUID: 128},
	}}
	carrier := newTestMailCarrier(t, &fakeMailSMTPClient{}, imapClient)

	first, err := carrier.Read(context.Background(), endpoint, "")
	if err != nil {
		t.Fatalf("first empty page: %v", err)
	}
	firstState, err := parseMailCursor(first.Cursor, carrier.cursorScope(endpoint))
	if err != nil || firstState.HighestScannedUID != 64 {
		t.Fatalf("first cursor err=%v state=%+v", err, firstState)
	}
	second, err := carrier.Read(context.Background(), endpoint, first.Cursor)
	if err != nil {
		t.Fatalf("second empty page: %v", err)
	}
	secondState, err := parseMailCursor(second.Cursor, carrier.cursorScope(endpoint))
	if err != nil || secondState.HighestScannedUID != 128 {
		t.Fatalf("second cursor err=%v state=%+v", err, secondState)
	}
	if got := imapClient.highestRequests; len(got) != 2 || got[0] != 0 || got[1] != 64 {
		t.Fatalf("highest UID requests = %v, want [0 64]", got)
	}
}

func TestIMAPScanWindowBoundsLargeSparseBacklog(t *testing.T) {
	start, end, pending, err := mailIMAPScanWindow(0, 41, 0, 10_001)
	if err != nil || !pending || start != 1 || end != mailIMAPScanPageSize {
		t.Fatalf("first window start=%d end=%d pending=%v err=%v", start, end, pending, err)
	}
	start, end, pending, err = mailIMAPScanWindow(41, 41, end, 10_001)
	if err != nil || !pending || start != mailIMAPScanPageSize+1 || end != mailIMAPScanPageSize*2 {
		t.Fatalf("second window start=%d end=%d pending=%v err=%v", start, end, pending, err)
	}
	start, end, pending, err = mailIMAPScanWindow(41, 42, 9_000, 10_001)
	if err != nil || !pending || start != 1 || end != mailIMAPScanPageSize {
		t.Fatalf("rollover window start=%d end=%d pending=%v err=%v", start, end, pending, err)
	}
	_, _, pending, err = mailIMAPScanWindow(41, 41, 10_000, 10_001)
	if err != nil || pending {
		t.Fatalf("drained mailbox pending=%v err=%v", pending, err)
	}
}

func TestMailCarrierAmbiguousSMTPRetryDeliversEnvelopeOnce(t *testing.T) {
	endpoint := Endpoint{ID: "mail.primary", Address: "egress"}
	envelope := fabric.NewEnvelope("ambiguous-mail", fabric.TrafficEgress, "test.payload", []byte("ciphertext"))
	smtpClient := &fakeMailSMTPClient{submitErrors: []error{errors.New("connection lost before final 250"), nil}}
	imapClient := &fakeMailIMAPClient{}
	carrier := newTestMailCarrier(t, smtpClient, imapClient)
	if err := carrier.Write(context.Background(), endpoint, envelope); err == nil {
		t.Fatal("ambiguous first SMTP completion unexpectedly succeeded")
	}
	if err := carrier.Write(context.Background(), endpoint, envelope); err != nil {
		t.Fatalf("SMTP retry: %v", err)
	}
	if len(smtpClient.messages) != 2 {
		t.Fatalf("SMTP accepted copies = %d, want 2", len(smtpClient.messages))
	}
	imapClient.scans = []mailScanResult{{uidValidity: 9, messages: []mailIMAPMessage{{uid: 10, raw: smtpClient.messages[0]}, {uid: 11, raw: smtpClient.messages[1]}}}}
	result, err := carrier.Read(context.Background(), endpoint, "")
	if err != nil || len(result.Envelopes) != 1 || result.Envelopes[0].ID != envelope.ID {
		t.Fatalf("deduplicated Read err=%v envelopes=%+v", err, result.Envelopes)
	}
}

func TestMailCarrierKeepsDistinctTunnelFramesSharingEnvelopeID(t *testing.T) {
	endpoint := Endpoint{ID: "mail.primary", Address: "egress"}
	first := fabric.NewEnvelope("tunnel-1", fabric.TrafficEgress, "tunnel.data", []byte("frame-one"))
	second := fabric.NewEnvelope("tunnel-1", fabric.TrafficEgress, "tunnel.data", []byte("frame-two"))
	cipher, err := mailEnvelopeCipher(testMailConfig())
	if err != nil {
		t.Fatal(err)
	}
	firstRaw, err := encodeMailMessage(testMailConfig(), endpoint, first, cipher)
	if err != nil {
		t.Fatal(err)
	}
	secondRaw, err := encodeMailMessage(testMailConfig(), endpoint, second, cipher)
	if err != nil {
		t.Fatal(err)
	}
	imapClient := &fakeMailIMAPClient{scans: []mailScanResult{{
		uidValidity: 17,
		messages: []mailIMAPMessage{
			{uid: 1, raw: firstRaw},
			{uid: 2, raw: firstRaw}, // exact ambiguous-SMTP retry
			{uid: 3, raw: secondRaw},
		},
	}}}
	carrier := newTestMailCarrier(t, &fakeMailSMTPClient{}, imapClient)
	result, err := carrier.Read(context.Background(), endpoint, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Envelopes) != 2 || string(result.Envelopes[0].Payload) != "frame-one" || string(result.Envelopes[1].Payload) != "frame-two" {
		t.Fatalf("delivered frames = %+v, want two distinct payloads and no exact retry", result.Envelopes)
	}
}

func TestMailCarrierCredentialsAndConstructedPolicy(t *testing.T) {
	base := testMailConfig()
	cases := map[string]func(*MailIMAPSMTPConfig){
		"smtp_username": func(cfg *MailIMAPSMTPConfig) { cfg.SMTPUsername = "" },
		"smtp_password": func(cfg *MailIMAPSMTPConfig) { cfg.SMTPPassword = "" },
		"imap_username": func(cfg *MailIMAPSMTPConfig) { cfg.IMAPUsername = "" },
		"imap_password": func(cfg *MailIMAPSMTPConfig) { cfg.IMAPPassword = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := base
			mutate(&cfg)
			if _, err := NewMailIMAPSMTPCarrier(cfg); err == nil || !strings.Contains(err.Error(), name+" is required") {
				t.Fatalf("credential error = %v", err)
			}
		})
	}
	carrier := newTestMailCarrier(t, &fakeMailSMTPClient{}, &fakeMailIMAPClient{})
	descriptor := carrier.Descriptor()
	if !HasCapability(descriptor, CapBulk) {
		t.Fatalf("Mail capabilities = %v, want %s", descriptor.Capabilities, CapBulk)
	}
	derived := DeriveTrafficClasses(descriptor.Capabilities)
	for _, traffic := range []fabric.TrafficClass{fabric.TrafficBootstrap, fabric.TrafficControl, fabric.TrafficBulk, fabric.TrafficEgress} {
		if !mailContainsTrafficClass(derived, traffic) {
			t.Fatalf("derived traffic = %v, want %s", derived, traffic)
		}
	}
}

func testMailConfig() MailIMAPSMTPConfig {
	return MailIMAPSMTPConfig{
		SMTPAddress:   "smtp.example.test:465",
		IMAPAddress:   "imap.example.test:993",
		AccountID:     "client-account",
		Mailbox:       "INBOX",
		FromAddress:   "client@example.test",
		ToAddress:     "node@example.test",
		TLSServerName: "mail.example.test",
		CAFile:        "/test-ca.pem",
		SMTPUsername:  "client-smtp",
		SMTPPassword:  "smtp-pass",
		IMAPUsername:  "client-imap",
		IMAPPassword:  "imap-pass",
	}
}

func newTestMailCarrier(t *testing.T, smtpClient mailSMTPClient, imapClient mailIMAPClient) *MailIMAPSMTPCarrier {
	t.Helper()
	carrier, err := newMailIMAPSMTPCarrierWithClients(testMailConfig(), smtpClient, imapClient)
	if err != nil {
		t.Fatalf("new Mail carrier: %v", err)
	}
	return carrier
}

type fakeMailSMTPClient struct {
	messages     [][]byte
	submitErrors []error
	probeCalls   int
}

func (f *fakeMailSMTPClient) Submit(_ context.Context, message []byte) error {
	f.messages = append(f.messages, append([]byte(nil), message...))
	if len(f.submitErrors) == 0 {
		return nil
	}
	err := f.submitErrors[0]
	f.submitErrors = f.submitErrors[1:]
	return err
}

func (f *fakeMailSMTPClient) Probe(context.Context) error {
	f.probeCalls++
	return nil
}

type fakeMailIMAPClient struct {
	scans           []mailScanResult
	highestRequests []uint32
	probeCalls      int
}

type deadlineCountingConn struct {
	net.Conn
	mu        sync.Mutex
	deadlines int
}

func (c *deadlineCountingConn) SetDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.deadlines++
	c.mu.Unlock()
	return c.Conn.SetDeadline(deadline)
}

func (c *deadlineCountingConn) deadlineCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.deadlines
}

func (f *fakeMailIMAPClient) Scan(_ context.Context, _ string, _ uint32, highest uint32) (mailScanResult, error) {
	f.highestRequests = append(f.highestRequests, highest)
	if len(f.scans) == 0 {
		return mailScanResult{uidValidity: 1}, nil
	}
	result := f.scans[0]
	f.scans = f.scans[1:]
	return result, nil
}

func (f *fakeMailIMAPClient) Probe(context.Context, string) error {
	f.probeCalls++
	return nil
}

func envelopeIDs(envelopes []fabric.Envelope) []string {
	ids := make([]string, 0, len(envelopes))
	for _, envelope := range envelopes {
		ids = append(ids, envelope.ID)
	}
	return ids
}

func mailContainsTrafficClass(classes []fabric.TrafficClass, want fabric.TrafficClass) bool {
	for _, class := range classes {
		if class == want {
			return true
		}
	}
	return false
}
