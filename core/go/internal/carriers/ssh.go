package carriers

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// SSHConfig contains the local credentials for an SSH direct-tcpip outbound.
type SSHConfig struct {
	Username                string
	Password                string
	PrivateKey              string
	PrivateKeyPath          string
	PrivateKeyPassphrase    string
	UseAgent                bool
	AgentSocketPath         string
	HostKeys                []string
	ServerAliveIntervalSecs int
}

// SSHCarrier is an egress-only carrier. It advertises SSH routing capability;
// actual TCP dialing is handled by tunnel.SSHTunnel.
type SSHCarrier struct {
	desc Descriptor
	cfg  SSHConfig
}

// NewSSHCarrier creates an SSH egress carrier descriptor with validated auth.
func NewSSHCarrier(cfg SSHConfig) (*SSHCarrier, error) {
	if strings.TrimSpace(cfg.Username) == "" {
		cfg.Username = "root"
	}
	if strings.TrimSpace(cfg.PrivateKey) == "" && strings.TrimSpace(cfg.PrivateKeyPath) != "" {
		key, err := os.ReadFile(cfg.PrivateKeyPath)
		if err != nil {
			return nil, fmt.Errorf("read ssh private key: %w", err)
		}
		cfg.PrivateKey = string(key)
	}
	desc, err := FindStandardDescriptor(CarrierSSHTCP)
	if err != nil {
		return nil, err
	}
	return &SSHCarrier{desc: desc, cfg: cfg}, nil
}

func (c *SSHCarrier) Descriptor() Descriptor { return c.desc }
func (c *SSHCarrier) IsNative()              {}

// Config returns a copy of the local SSH credentials for the tunnel dialer.
func (c *SSHCarrier) Config() SSHConfig { return c.cfg }

func (c *SSHCarrier) Write(context.Context, Endpoint, fabric.Envelope) error {
	return fmt.Errorf("ssh.tcp is egress-only and does not support envelope writes")
}

func (c *SSHCarrier) Read(context.Context, Endpoint, Cursor) (ReadResult, error) {
	return ReadResult{}, fmt.Errorf("ssh.tcp is egress-only and does not support envelope reads")
}

func (c *SSHCarrier) Probe(context.Context, Endpoint) (Metrics, error) {
	return c.desc.Metrics, nil
}

func (c *SSHCarrier) DeleteMessage(context.Context, Endpoint, string) error {
	return fmt.Errorf("ssh.tcp is egress-only and does not support message deletion")
}

// DialStream opens a TCP connection to targetAddr through the SSH server
// identified by the endpoint. It implements the StreamDialer interface,
// making SSH a first-class egress carrier discoverable by the unified tunnel.
func (c *SSHCarrier) DialStream(ctx context.Context, endpoint Endpoint, targetAddr string) (net.Conn, error) {
	sshAddr := strings.TrimSpace(endpoint.Address)
	if sshAddr == "" {
		return nil, fmt.Errorf("ssh stream dialer: endpoint address is required")
	}
	client, err := sshDial(ctx, sshAddr, c.cfg)
	if err != nil {
		return nil, err
	}
	conn, err := client.Dial("tcp", targetAddr)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ssh stream dialer: dial target %s: %w", targetAddr, err)
	}
	return &sshStreamConn{Conn: conn, client: client}, nil
}

// sshStreamConn wraps a net.Conn and its parent SSH client so both are
// closed together.
type sshStreamConn struct {
	net.Conn
	client *ssh.Client
}

func (c *sshStreamConn) Close() error {
	connErr := c.Conn.Close()
	clientErr := c.client.Close()
	if connErr != nil {
		return connErr
	}
	return clientErr
}

// sshDial establishes an SSH client connection to addr using the carrier config.
func sshDial(ctx context.Context, addr string, cfg SSHConfig) (*ssh.Client, error) {
	auth, agentConnection, err := sshAuthMethods(cfg)
	if err != nil {
		return nil, err
	}
	if agentConnection != nil {
		defer agentConnection.Close()
	}
	// When HostKeys is empty we still accept the presented server key, but we
	// log a loud warning so operators know the carrier is exposed to MITM.
	// This is "best effort": do not block egress when keys are unconfigured,
	// but make the gap visible.
	var hostKeyCallback ssh.HostKeyCallback
	if len(cfg.HostKeys) > 0 {
		callback, err := sshKnownHostKeysCallback(cfg.HostKeys)
		if err != nil {
			return nil, err
		}
		hostKeyCallback = callback
	} else {
		hostKeyCallback = func(_ string, _ net.Addr, key ssh.PublicKey) error {
			log.Printf("[ssh] WARNING: ssh.tcp carrier %q running without host key verification (HostKeys empty). MITM possible. addr=%s fingerprint=%s", cfg.Username, addr, ssh.FingerprintSHA256(key))
			return nil
		}
	}
	clientConfig := &ssh.ClientConfig{
		User:            cfg.Username,
		Auth:            auth,
		HostKeyCallback: hostKeyCallback,
		Timeout:         10 * time.Second,
	}
	dialer := net.Dialer{Timeout: clientConfig.Timeout}
	raw, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("ssh stream dialer: dial ssh %s: %w", addr, err)
	}
	conn, chans, reqs, err := ssh.NewClientConn(raw, addr, clientConfig)
	if err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("ssh stream dialer: handshake %s: %w", addr, err)
	}
	return ssh.NewClient(conn, chans, reqs), nil
}

func sshAuthMethods(cfg SSHConfig) ([]ssh.AuthMethod, io.Closer, error) {
	methods := make([]ssh.AuthMethod, 0, 2)
	if strings.TrimSpace(cfg.Password) != "" {
		methods = append(methods, ssh.Password(cfg.Password))
	}
	if strings.TrimSpace(cfg.PrivateKey) != "" {
		var (
			signer ssh.Signer
			err    error
		)
		if strings.TrimSpace(cfg.PrivateKeyPassphrase) != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(cfg.PrivateKey), []byte(cfg.PrivateKeyPassphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(cfg.PrivateKey))
		}
		if err != nil {
			return nil, nil, fmt.Errorf("ssh stream dialer: parse private key: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if !cfg.UseAgent {
		return methods, nil, nil
	}
	socketPath := strings.TrimSpace(cfg.AgentSocketPath)
	if socketPath == "" {
		socketPath = strings.TrimSpace(os.Getenv("SSH_AUTH_SOCK"))
	}
	if socketPath == "" {
		return nil, nil, fmt.Errorf("ssh stream dialer: SSH agent is enabled but no socket is configured")
	}
	connection, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, nil, fmt.Errorf("ssh stream dialer: connect agent socket: %w", err)
	}
	methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(connection).Signers))
	return methods, connection, nil
}

func sshKnownHostKeysCallback(hostKeys []string) (ssh.HostKeyCallback, error) {
	allowed := make(map[string]ssh.PublicKey, len(hostKeys))
	for _, raw := range hostKeys {
		line := strings.TrimSpace(raw)
		// ssh-keyscan writes server-identification comments next to public-key
		// lines. Managed profiles preserve both, while only key lines are pins.
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			return nil, fmt.Errorf("ssh stream dialer: parse host key: %w", err)
		}
		allowed[string(key.Marshal())] = key
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("ssh stream dialer: no host keys configured")
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if _, ok := allowed[string(key.Marshal())]; ok {
			return nil
		}
		return fmt.Errorf("ssh stream dialer: host key rejected for %s (%s)", hostname, remote.String())
	}, nil
}
