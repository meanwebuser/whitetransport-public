package carriers

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const mailMaxIMAPLineBytes = 64 * 1024

var (
	imapLiteralPattern     = regexp.MustCompile(`\{([0-9]+)\}$`)
	imapUIDValidityPattern = regexp.MustCompile(`(?i)UIDVALIDITY[ ]+([0-9]+)`)
	imapUIDNextPattern     = regexp.MustCompile(`(?i)UIDNEXT[ ]+([0-9]+)`)
	imapFetchUIDPattern    = regexp.MustCompile(`(?i)UID[ ]+([0-9]+)`)
)

type imapMailClient struct {
	cfg       MailIMAPSMTPConfig
	tlsConfig *tls.Config
}

type imapResponse struct {
	line    string
	literal []byte
}

type imapSession struct {
	ctx     context.Context
	conn    net.Conn
	in      *bufio.Reader
	out     *bufio.Writer
	timeout time.Duration
	next    uint64
}

func newIMAPMailClient(cfg MailIMAPSMTPConfig, tlsConfig *tls.Config) mailIMAPClient {
	return &imapMailClient{cfg: cfg, tlsConfig: tlsConfig.Clone()}
}

func (c *imapMailClient) Probe(ctx context.Context, mailbox string) error {
	session, err := c.connect(ctx)
	if err != nil {
		return err
	}
	defer session.conn.Close()
	if _, err := session.command("CAPABILITY"); err != nil {
		return fmt.Errorf("IMAP CAPABILITY: %w", err)
	}
	if _, err := session.command("EXAMINE " + imapQuote(mailbox)); err != nil {
		return fmt.Errorf("IMAP EXAMINE: %w", err)
	}
	if _, err := session.command("LOGOUT"); err != nil {
		return fmt.Errorf("IMAP LOGOUT: %w", err)
	}
	return nil
}

func (c *imapMailClient) Scan(ctx context.Context, mailbox string, expectedUIDValidity, highestScannedUID uint32) (mailScanResult, error) {
	session, err := c.connect(ctx)
	if err != nil {
		return mailScanResult{}, err
	}
	defer session.conn.Close()
	responses, err := session.command("EXAMINE " + imapQuote(mailbox))
	if err != nil {
		return mailScanResult{}, fmt.Errorf("IMAP EXAMINE: %w", err)
	}
	uidValidity, err := parseIMAPUIDValidity(responses)
	if err != nil {
		return mailScanResult{}, err
	}
	uidNext, err := parseIMAPUIDNext(responses)
	if err != nil {
		return mailScanResult{}, err
	}
	startUID, endUID, pending, err := mailIMAPScanWindow(expectedUIDValidity, uidValidity, highestScannedUID, uidNext)
	if err != nil {
		return mailScanResult{}, err
	}
	result := mailScanResult{uidValidity: uidValidity, highestScannedUID: highestScannedUID}
	if expectedUIDValidity != uidValidity {
		result.highestScannedUID = 0
	}
	if !pending {
		if _, err := session.command("LOGOUT"); err != nil {
			return mailScanResult{}, fmt.Errorf("IMAP LOGOUT: %w", err)
		}
		return result, nil
	}
	searchResponses, err := session.command(fmt.Sprintf("UID SEARCH UID %d:%d", startUID, endUID))
	if err != nil {
		return mailScanResult{}, fmt.Errorf("IMAP UID SEARCH: %w", err)
	}
	uids, err := parseIMAPSearchUIDs(searchResponses)
	if err != nil {
		return mailScanResult{}, err
	}
	// Some IMAP servers interpret a reversed sequence set (N:* when N is
	// above the current max UID) in both directions. Sorting plus this strict
	// client-side filter is therefore mandatory even after UID SEARCH.
	effectiveHighest := highestScannedUID
	if expectedUIDValidity != uidValidity {
		effectiveHighest = 0
	}
	filtered := uids[:0]
	for _, uid := range uids {
		if uid > effectiveHighest && uid >= startUID && uid <= endUID {
			filtered = append(filtered, uid)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i] < filtered[j] })
	result.highestScannedUID = endUID
	result.messages = make([]mailIMAPMessage, 0, len(filtered))
	for _, uid := range filtered {
		fetchResponses, err := session.command(fmt.Sprintf("UID FETCH %d (UID BODY.PEEK[])", uid))
		if err != nil {
			return mailScanResult{}, fmt.Errorf("IMAP UID FETCH %d: %w", uid, err)
		}
		raw, err := parseIMAPFetchLiteral(fetchResponses, uid)
		if err != nil {
			return mailScanResult{}, err
		}
		result.messages = append(result.messages, mailIMAPMessage{uid: uid, raw: raw})
	}
	if _, err := session.command("LOGOUT"); err != nil {
		return mailScanResult{}, fmt.Errorf("IMAP LOGOUT: %w", err)
	}
	return result, nil
}

func (c *imapMailClient) connect(ctx context.Context) (*imapSession, error) {
	conn, err := dialMailTLS(ctx, c.cfg.IMAPAddress, c.tlsConfig, c.cfg.Timeout)
	if err != nil {
		return nil, fmt.Errorf("IMAP TLS connect: %w", err)
	}
	session := &imapSession{ctx: ctx, conn: conn, in: bufio.NewReader(conn), out: bufio.NewWriter(conn), timeout: c.cfg.Timeout}
	greeting, err := session.readLine()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("IMAP greeting: %w", err)
	}
	if !strings.HasPrefix(strings.ToUpper(greeting), "* OK") {
		conn.Close()
		return nil, fmt.Errorf("IMAP greeting rejected: %s", greeting)
	}
	if _, err := session.command("CAPABILITY"); err != nil {
		conn.Close()
		return nil, fmt.Errorf("IMAP CAPABILITY: %w", err)
	}
	if _, err := session.command("LOGIN " + imapQuote(c.cfg.IMAPUsername) + " " + imapQuote(c.cfg.IMAPPassword)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("IMAP LOGIN: %w", err)
	}
	return session, nil
}

func (s *imapSession) command(command string) ([]imapResponse, error) {
	if strings.ContainsAny(command, "\r\n") {
		return nil, fmt.Errorf("IMAP command contains a newline")
	}
	if err := setMailDeadline(s.ctx, s.conn, s.timeout); err != nil {
		return nil, fmt.Errorf("set IMAP command deadline: %w", err)
	}
	s.next++
	tag := fmt.Sprintf("A%04d", s.next)
	if _, err := fmt.Fprintf(s.out, "%s %s\r\n", tag, command); err != nil {
		return nil, err
	}
	if err := s.out.Flush(); err != nil {
		return nil, err
	}
	responses := make([]imapResponse, 0, 4)
	for {
		line, err := s.readLine()
		if err != nil {
			return nil, err
		}
		upper := strings.ToUpper(line)
		if strings.HasPrefix(upper, strings.ToUpper(tag)+" ") {
			if !strings.HasPrefix(upper, strings.ToUpper(tag)+" OK") {
				return responses, fmt.Errorf("%s", line)
			}
			return responses, nil
		}
		response := imapResponse{line: line}
		if match := imapLiteralPattern.FindStringSubmatch(line); len(match) == 2 {
			length, parseErr := strconv.Atoi(match[1])
			if parseErr != nil || length < 0 || length > mailMaxEnvelopeBytes*4 {
				return nil, fmt.Errorf("invalid IMAP literal length %q", match[1])
			}
			response.literal = make([]byte, length)
			if _, err := io.ReadFull(s.in, response.literal); err != nil {
				return nil, fmt.Errorf("read IMAP literal: %w", err)
			}
			if _, err := s.readLine(); err != nil {
				return nil, fmt.Errorf("read IMAP literal terminator: %w", err)
			}
		}
		responses = append(responses, response)
	}
}

func (s *imapSession) readLine() (string, error) {
	line := make([]byte, 0, 256)
	for {
		fragment, err := s.in.ReadSlice('\n')
		if len(line)+len(fragment) > mailMaxIMAPLineBytes {
			return "", fmt.Errorf("IMAP line exceeds %d bytes", mailMaxIMAPLineBytes)
		}
		line = append(line, fragment...)
		if err == nil {
			break
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return "", err
	}
	if !bytes.HasSuffix(line, []byte("\r\n")) {
		return "", fmt.Errorf("IMAP line lacks CRLF")
	}
	return string(line[:len(line)-2]), nil
}

func parseIMAPUIDValidity(responses []imapResponse) (uint32, error) {
	for _, response := range responses {
		match := imapUIDValidityPattern.FindStringSubmatch(response.line)
		if len(match) != 2 {
			continue
		}
		value, err := strconv.ParseUint(match[1], 10, 32)
		if err != nil || value == 0 {
			return 0, fmt.Errorf("invalid IMAP UIDVALIDITY %q", match[1])
		}
		return uint32(value), nil
	}
	return 0, fmt.Errorf("IMAP EXAMINE response lacks UIDVALIDITY")
}

func parseIMAPUIDNext(responses []imapResponse) (uint32, error) {
	for _, response := range responses {
		match := imapUIDNextPattern.FindStringSubmatch(response.line)
		if len(match) != 2 {
			continue
		}
		value, err := strconv.ParseUint(match[1], 10, 32)
		if err != nil || value == 0 {
			return 0, fmt.Errorf("invalid IMAP UIDNEXT %q", match[1])
		}
		return uint32(value), nil
	}
	return 0, fmt.Errorf("IMAP EXAMINE response lacks UIDNEXT")
}

func mailIMAPScanWindow(expectedUIDValidity, actualUIDValidity, highestScannedUID, uidNext uint32) (uint32, uint32, bool, error) {
	if actualUIDValidity == 0 {
		return 0, 0, false, fmt.Errorf("IMAP UIDVALIDITY is zero")
	}
	if uidNext == 0 {
		return 0, 0, false, fmt.Errorf("IMAP UIDNEXT is zero")
	}
	effectiveHighest := highestScannedUID
	if expectedUIDValidity != actualUIDValidity {
		effectiveHighest = 0
	}
	if effectiveHighest == ^uint32(0) {
		return 0, 0, false, nil
	}
	startUID := effectiveHighest + 1
	if startUID >= uidNext {
		return startUID, effectiveHighest, false, nil
	}
	lastExistingUID := uidNext - 1
	endUID := startUID + mailIMAPScanPageSize - 1
	if endUID < startUID || endUID > lastExistingUID {
		endUID = lastExistingUID
	}
	return startUID, endUID, true, nil
}

func parseIMAPSearchUIDs(responses []imapResponse) ([]uint32, error) {
	var uids []uint32
	for _, response := range responses {
		fields := strings.Fields(response.line)
		if len(fields) < 2 || fields[0] != "*" || !strings.EqualFold(fields[1], "SEARCH") {
			continue
		}
		for _, field := range fields[2:] {
			value, err := strconv.ParseUint(field, 10, 32)
			if err != nil || value == 0 {
				return nil, fmt.Errorf("invalid IMAP SEARCH UID %q", field)
			}
			uids = append(uids, uint32(value))
		}
	}
	return uids, nil
}

func parseIMAPFetchLiteral(responses []imapResponse, wantUID uint32) ([]byte, error) {
	for _, response := range responses {
		if len(response.literal) == 0 {
			continue
		}
		match := imapFetchUIDPattern.FindStringSubmatch(response.line)
		if len(match) != 2 {
			continue
		}
		value, err := strconv.ParseUint(match[1], 10, 32)
		if err != nil || uint32(value) != wantUID {
			continue
		}
		return append([]byte(nil), response.literal...), nil
	}
	return nil, fmt.Errorf("IMAP UID FETCH %d response lacks matching literal", wantUID)
}

func imapQuote(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}
