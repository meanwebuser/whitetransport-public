package tunnel

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// SSHTunnel dials target TCP addresses through SSH direct-tcpip.
type SSHTunnel struct {
	bindings map[string]policy.CarrierBinding
}

func NewSSHTunnel(bindings map[string]policy.CarrierBinding) *SSHTunnel {
	sshBindings := make(map[string]policy.CarrierBinding)
	for id, binding := range bindings {
		if binding.Carrier.Descriptor().ID == carriers.CarrierSSHTCP {
			sshBindings[id] = binding
		}
	}
	if len(sshBindings) == 0 {
		return nil
	}
	return &SSHTunnel{bindings: sshBindings}
}

func (t *SSHTunnel) SupportsEndpoint(endpoint carriers.Endpoint) bool {
	if endpoint.Carrier != carriers.CarrierSSHTCP {
		return false
	}
	_, ok := t.bindings[endpoint.Carrier]
	return ok
}

func (t *SSHTunnel) DialContext(ctx context.Context, endpoint carriers.Endpoint, targetAddr string) (net.Conn, error) {
	binding, ok := t.bindings[endpoint.Carrier]
	if !ok {
		return nil, fmt.Errorf("ssh tunnel: no binding for %s", endpoint.Carrier)
	}
	carrier, ok := binding.Carrier.(*carriers.SSHCarrier)
	if !ok {
		return nil, fmt.Errorf("ssh tunnel: binding %s is not SSHCarrier", endpoint.Carrier)
	}
	sshAddr := strings.TrimSpace(endpoint.Address)
	if sshAddr == "" {
		sshAddr = strings.TrimSpace(binding.Endpoint.Address)
	}
	if sshAddr == "" {
		return nil, fmt.Errorf("ssh tunnel: endpoint address is required")
	}
	client, err := dialSSH(ctx, sshAddr, carrier.Config())
	if err != nil {
		return nil, err
	}
	conn, err := client.Dial("tcp", targetAddr)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ssh tunnel: dial target %s: %w", targetAddr, err)
	}
	return &sshTunnelConn{Conn: conn, client: client}, nil
}

type sshTunnelConn struct {
	net.Conn
	client *ssh.Client
}

func (c *sshTunnelConn) Close() error {
	connErr := c.Conn.Close()
	clientErr := c.client.Close()
	if connErr != nil {
		return connErr
	}
	return clientErr
}

func dialSSH(ctx context.Context, addr string, cfg carriers.SSHConfig) (*ssh.Client, error) {
	auth, agentConnection, err := sshAuthMethods(cfg)
	if err != nil {
		return nil, err
	}
	if agentConnection != nil {
		defer agentConnection.Close()
	}
	hostKeyCallback := ssh.InsecureIgnoreHostKey()
	if len(cfg.HostKeys) > 0 {
		callback, err := knownHostKeysCallback(cfg.HostKeys)
		if err != nil {
			return nil, err
		}
		hostKeyCallback = callback
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
		return nil, fmt.Errorf("ssh tunnel: dial ssh %s: %w", addr, err)
	}
	conn, chans, reqs, err := ssh.NewClientConn(raw, addr, clientConfig)
	if err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("ssh tunnel: handshake %s: %w", addr, err)
	}
	return ssh.NewClient(conn, chans, reqs), nil
}

func sshAuthMethods(cfg carriers.SSHConfig) ([]ssh.AuthMethod, io.Closer, error) {
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
			return nil, nil, fmt.Errorf("ssh tunnel: parse private key: %w", err)
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
		return nil, nil, fmt.Errorf("ssh tunnel: SSH agent is enabled but no socket is configured")
	}
	connection, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, nil, fmt.Errorf("ssh tunnel: connect agent socket: %w", err)
	}
	methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(connection).Signers))
	return methods, connection, nil
}

func knownHostKeysCallback(hostKeys []string) (ssh.HostKeyCallback, error) {
	allowed := make(map[string]ssh.PublicKey, len(hostKeys))
	for _, raw := range hostKeys {
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(raw)))
		if err != nil {
			return nil, fmt.Errorf("ssh tunnel: parse host key: %w", err)
		}
		allowed[string(key.Marshal())] = key
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if _, ok := allowed[string(key.Marshal())]; ok {
			return nil
		}
		return fmt.Errorf("ssh tunnel: host key rejected for %s (%s)", hostname, remote.String())
	}, nil
}
