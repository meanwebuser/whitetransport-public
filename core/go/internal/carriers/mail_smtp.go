package carriers

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"time"
)

type smtpMailClient struct {
	cfg       MailIMAPSMTPConfig
	tlsConfig *tls.Config
}

func newSMTPMailClient(cfg MailIMAPSMTPConfig, tlsConfig *tls.Config) mailSMTPClient {
	return &smtpMailClient{cfg: cfg, tlsConfig: tlsConfig.Clone()}
}

func newMailTLSConfig(serverName, caFile string) (*tls.Config, error) {
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("mail imap/smtp: read ca_file: %w", err)
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("mail imap/smtp: ca_file contains no certificates")
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName, RootCAs: roots}, nil
}

func (c *smtpMailClient) Probe(ctx context.Context) error {
	client, conn, err := c.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	defer client.Close()
	if err := setMailDeadline(ctx, conn, c.cfg.Timeout); err != nil {
		return fmt.Errorf("SMTP QUIT deadline: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("SMTP QUIT: %w", err)
	}
	return nil
}

func (c *smtpMailClient) Submit(ctx context.Context, message []byte) error {
	client, conn, err := c.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	defer client.Close()
	from, _ := mail.ParseAddress(c.cfg.FromAddress)
	to, _ := mail.ParseAddress(c.cfg.ToAddress)
	if err := setMailDeadline(ctx, conn, c.cfg.Timeout); err != nil {
		return fmt.Errorf("SMTP MAIL FROM deadline: %w", err)
	}
	if err := client.Mail(from.Address); err != nil {
		return fmt.Errorf("SMTP MAIL FROM: %w", err)
	}
	if err := setMailDeadline(ctx, conn, c.cfg.Timeout); err != nil {
		return fmt.Errorf("SMTP RCPT TO deadline: %w", err)
	}
	if err := client.Rcpt(to.Address); err != nil {
		return fmt.Errorf("SMTP RCPT TO: %w", err)
	}
	if err := setMailDeadline(ctx, conn, c.cfg.Timeout); err != nil {
		return fmt.Errorf("SMTP DATA deadline: %w", err)
	}
	data, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA: %w", err)
	}
	if err := setMailDeadline(ctx, conn, c.cfg.Timeout); err != nil {
		_ = data.Close()
		return fmt.Errorf("SMTP message deadline: %w", err)
	}
	if _, err := data.Write(message); err != nil {
		_ = data.Close()
		return fmt.Errorf("SMTP message write: %w", err)
	}
	// DotWriter.Close waits for the server's final response. If the server
	// accepted DATA but that response is lost, the caller receives an error and
	// may retry the same fabric envelope ID; IMAP-side replay suppression keeps
	// delivery exactly once.
	if err := setMailDeadline(ctx, conn, c.cfg.Timeout); err != nil {
		_ = data.Close()
		return fmt.Errorf("SMTP final DATA deadline: %w", err)
	}
	if err := data.Close(); err != nil {
		return fmt.Errorf("SMTP final DATA response: %w", err)
	}
	if err := setMailDeadline(ctx, conn, c.cfg.Timeout); err != nil {
		return fmt.Errorf("SMTP QUIT deadline: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("SMTP QUIT: %w", err)
	}
	return nil
}

func (c *smtpMailClient) connect(ctx context.Context) (*smtp.Client, net.Conn, error) {
	conn, err := dialMailTLS(ctx, c.cfg.SMTPAddress, c.tlsConfig, c.cfg.Timeout)
	if err != nil {
		return nil, nil, fmt.Errorf("SMTP TLS connect: %w", err)
	}
	client, err := c.authenticate(ctx, conn)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	return client, conn, nil
}

func (c *smtpMailClient) authenticate(ctx context.Context, conn net.Conn) (*smtp.Client, error) {
	if err := setMailDeadline(ctx, conn, c.cfg.Timeout); err != nil {
		return nil, fmt.Errorf("SMTP greeting deadline: %w", err)
	}
	client, err := smtp.NewClient(conn, c.cfg.TLSServerName)
	if err != nil {
		return nil, fmt.Errorf("SMTP greeting: %w", err)
	}
	if err := setMailDeadline(ctx, conn, c.cfg.Timeout); err != nil {
		client.Close()
		return nil, fmt.Errorf("SMTP EHLO deadline: %w", err)
	}
	if err := client.Hello("whitetransport.local"); err != nil {
		client.Close()
		return nil, fmt.Errorf("SMTP EHLO: %w", err)
	}
	auth := smtp.PlainAuth("", c.cfg.SMTPUsername, c.cfg.SMTPPassword, c.cfg.TLSServerName)
	if err := setMailDeadline(ctx, conn, c.cfg.Timeout); err != nil {
		client.Close()
		return nil, fmt.Errorf("SMTP AUTH deadline: %w", err)
	}
	if err := client.Auth(auth); err != nil {
		client.Close()
		return nil, fmt.Errorf("SMTP AUTH: %w", err)
	}
	return client, nil
}

func dialMailTLS(ctx context.Context, address string, tlsConfig *tls.Config, timeout time.Duration) (net.Conn, error) {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	dialer := &net.Dialer{Timeout: timeout}
	raw, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	if err := setMailDeadline(ctx, raw, timeout); err != nil {
		raw.Close()
		return nil, err
	}
	secure := tls.Client(raw, tlsConfig.Clone())
	if err := secure.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, err
	}
	return secure, nil
}

func setMailDeadline(ctx context.Context, conn net.Conn, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	deadline := time.Now().Add(timeout)
	if ctx != nil {
		if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
			deadline = ctxDeadline
		}
	}
	return conn.SetDeadline(deadline)
}
