package carriers

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHFabricListenerConfig defines a public-key-only SSH fabric listener.
// AllowedTargets is an exact IP:port allowlist; hostnames and wildcards are
// rejected so the listener cannot accidentally become an open proxy.
type SSHFabricListenerConfig struct {
	ListenAddress            string
	LocalClientAddress       string
	HostPrivateKey           string
	HostPrivateKeyPath       string
	HostPrivateKeyPassphrase string
	AuthorizedClientKeys     []string
	RetentionLimit           int
	AllowedTargets           []string
}

type validatedSSHFabricListenerConfig struct {
	listenAddress      string
	localClientAddress string
	serverConfig       *ssh.ServerConfig
	retentionLimit     int
	allowedTargets     map[string]struct{}
}

// SSHFabricListenerCarrier is the node-side SSH fabric carrier. Embedding the
// client preserves the Carrier and StreamDialer contracts while keeping
// listener lifecycle methods absent from client-only bindings.
type SSHFabricListenerCarrier struct {
	*SSHFabricCarrier

	listenerMu            sync.Mutex
	listenerConfig        *validatedSSHFabricListenerConfig
	listenerServer        *SSHFabricServer
	listenerDone          chan struct{}
	listenerAddress       string
	listenerClientAddress string
	listenerHealth        HealthStatus
}

// NewSSHFabricCarrierWithListener creates a client carrier that also owns an
// authenticated local listener. LocalClientAddress avoids relying on public
// endpoint hairpin routing for node-side mailbox reads and writes.
func NewSSHFabricCarrierWithListener(clientConfig SSHConfig, listenerConfig SSHFabricListenerConfig) (*SSHFabricListenerCarrier, error) {
	carrier, err := NewSSHFabricCarrier(clientConfig)
	if err != nil {
		return nil, err
	}
	validated, err := validateSSHFabricListenerConfig(listenerConfig)
	if err != nil {
		return nil, err
	}
	wrapper := &SSHFabricListenerCarrier{
		SSHFabricCarrier: carrier,
		listenerConfig:   validated,
		listenerHealth:   HealthStatus{Healthy: true, Ready: false, Message: "listener configured", LastChecked: time.Now()},
	}
	carrier.connectionAddressOverride = wrapper.effectiveConnectionAddress
	return wrapper, nil
}

// StartListener binds and starts the SSH broker before the endpoint may be advertised.
func (c *SSHFabricListenerCarrier) StartListener(ctx context.Context, _ Endpoint) error {
	c.listenerMu.Lock()
	defer c.listenerMu.Unlock()
	if c.listenerConfig == nil {
		return fmt.Errorf("ssh.fabric listener: listener config is required")
	}
	if c.listenerServer != nil {
		return fmt.Errorf("ssh.fabric listener: already started")
	}
	listener, err := net.Listen("tcp", c.listenerConfig.listenAddress)
	if err != nil {
		c.listenerHealth = HealthStatus{Healthy: false, Ready: false, Message: err.Error(), LastChecked: time.Now()}
		return fmt.Errorf("ssh.fabric listener: listen %s: %w", c.listenerConfig.listenAddress, err)
	}
	server, err := NewSSHFabricServer(listener, SSHFabricServerConfig{
		SSHConfig:      c.listenerConfig.serverConfig,
		RetentionLimit: c.listenerConfig.retentionLimit,
		AllowTarget:    c.listenerConfig.allowTarget,
	})
	if err != nil {
		_ = listener.Close()
		return err
	}
	localClientAddress := c.listenerConfig.localClientAddress
	if localClientAddress == "" {
		localClientAddress = listener.Addr().String()
	}
	done := make(chan struct{})
	c.listenerServer = server
	c.listenerDone = done
	c.listenerAddress = listener.Addr().String()
	c.listenerClientAddress = localClientAddress
	c.listenerHealth = HealthStatus{Healthy: true, Ready: true, Message: "listener ready", LastChecked: time.Now()}
	go func() {
		err := server.Serve(ctx)
		c.listenerMu.Lock()
		if c.listenerServer == server {
			message := "listener stopped"
			healthy := true
			if err != nil {
				message = err.Error()
				healthy = false
			}
			c.listenerHealth = HealthStatus{Healthy: healthy, Ready: false, Message: message, LastChecked: time.Now()}
		}
		c.listenerMu.Unlock()
		close(done)
	}()
	return nil
}

// StopListener closes the listening socket and all authenticated sessions.
func (c *SSHFabricListenerCarrier) StopListener(ctx context.Context) error {
	c.listenerMu.Lock()
	server := c.listenerServer
	done := c.listenerDone
	c.listenerMu.Unlock()
	if server == nil {
		return nil
	}
	closeErr := server.Close()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	c.listenerMu.Lock()
	if c.listenerServer == server {
		c.listenerServer = nil
		c.listenerDone = nil
		c.listenerAddress = ""
		c.listenerClientAddress = ""
		c.listenerHealth = HealthStatus{Healthy: true, Ready: false, Message: "listener stopped", LastChecked: time.Now()}
	}
	c.listenerMu.Unlock()
	return closeErr
}

// ListenerHealth reports whether the configured listener is accepting sessions.
func (c *SSHFabricListenerCarrier) ListenerHealth() HealthStatus {
	c.listenerMu.Lock()
	defer c.listenerMu.Unlock()
	return c.listenerHealth
}

// ListenerAddress returns the actual bound address, including an allocated port.
func (c *SSHFabricListenerCarrier) ListenerAddress() string {
	c.listenerMu.Lock()
	defer c.listenerMu.Unlock()
	return c.listenerAddress
}

func (c *SSHFabricListenerCarrier) effectiveConnectionAddress(advertisedAddress string) string {
	c.listenerMu.Lock()
	defer c.listenerMu.Unlock()
	if c.listenerServer != nil && c.listenerClientAddress != "" {
		return c.listenerClientAddress
	}
	return advertisedAddress
}

func (c *validatedSSHFabricListenerConfig) allowTarget(address string) error {
	normalized, err := normalizeSSHFabricExactAddress(address, false)
	if err != nil {
		return fmt.Errorf("target is not an exact IP:port: %w", err)
	}
	if _, ok := c.allowedTargets[normalized]; !ok {
		return fmt.Errorf("target %s is not allowed", normalized)
	}
	return nil
}

func validateSSHFabricListenerConfig(config SSHFabricListenerConfig) (*validatedSSHFabricListenerConfig, error) {
	listenAddress, err := normalizeSSHFabricExactAddress(config.ListenAddress, true)
	if err != nil {
		return nil, fmt.Errorf("ssh.fabric listener: listen address: %w", err)
	}
	localClientAddress := ""
	if strings.TrimSpace(config.LocalClientAddress) != "" {
		localClientAddress, err = normalizeSSHFabricExactAddress(config.LocalClientAddress, false)
		if err != nil {
			return nil, fmt.Errorf("ssh.fabric listener: local client address: %w", err)
		}
	}
	listenHost, _, err := net.SplitHostPort(listenAddress)
	if err != nil {
		return nil, fmt.Errorf("ssh.fabric listener: parse normalized listen address: %w", err)
	}
	if net.ParseIP(listenHost).IsUnspecified() && localClientAddress == "" {
		return nil, fmt.Errorf("ssh.fabric listener: local client address is required for an unspecified listen address")
	}
	hostPrivateKey := strings.TrimSpace(config.HostPrivateKey)
	if hostPrivateKey == "" && strings.TrimSpace(config.HostPrivateKeyPath) != "" {
		keyBytes, readErr := os.ReadFile(config.HostPrivateKeyPath)
		if readErr != nil {
			return nil, fmt.Errorf("ssh.fabric listener: read host private key: %w", readErr)
		}
		hostPrivateKey = string(keyBytes)
	}
	if hostPrivateKey == "" {
		return nil, fmt.Errorf("ssh.fabric listener: host private key is required")
	}
	var hostSigner ssh.Signer
	if strings.TrimSpace(config.HostPrivateKeyPassphrase) == "" {
		hostSigner, err = ssh.ParsePrivateKey([]byte(hostPrivateKey))
	} else {
		hostSigner, err = ssh.ParsePrivateKeyWithPassphrase([]byte(hostPrivateKey), []byte(config.HostPrivateKeyPassphrase))
	}
	if err != nil {
		return nil, fmt.Errorf("ssh.fabric listener: parse host private key: %w", err)
	}
	if len(config.AuthorizedClientKeys) == 0 {
		return nil, fmt.Errorf("ssh.fabric listener: at least one authorized client key is required")
	}
	authorizedKeys := make(map[string]struct{}, len(config.AuthorizedClientKeys))
	for index, rawKey := range config.AuthorizedClientKeys {
		key, _, _, _, parseErr := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(rawKey)))
		if parseErr != nil {
			return nil, fmt.Errorf("ssh.fabric listener: parse authorized client key %d: %w", index, parseErr)
		}
		authorizedKeys[string(key.Marshal())] = struct{}{}
	}
	if config.RetentionLimit <= 0 {
		return nil, fmt.Errorf("ssh.fabric listener: positive retention limit is required")
	}
	if len(config.AllowedTargets) == 0 {
		return nil, fmt.Errorf("ssh.fabric listener: at least one allowed target is required")
	}
	allowedTargets := make(map[string]struct{}, len(config.AllowedTargets))
	for index, rawAddress := range config.AllowedTargets {
		normalized, normalizeErr := normalizeSSHFabricExactAddress(rawAddress, false)
		if normalizeErr != nil {
			return nil, fmt.Errorf("ssh.fabric listener: allowed target %d: %w", index, normalizeErr)
		}
		allowedTargets[normalized] = struct{}{}
	}
	serverConfig := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if _, ok := authorizedKeys[string(key.Marshal())]; !ok {
				return nil, fmt.Errorf("unauthorized client key")
			}
			return nil, nil
		},
	}
	serverConfig.AddHostKey(hostSigner)
	return &validatedSSHFabricListenerConfig{
		listenAddress:      listenAddress,
		localClientAddress: localClientAddress,
		serverConfig:       serverConfig,
		retentionLimit:     config.RetentionLimit,
		allowedTargets:     allowedTargets,
	}, nil
}

func normalizeSSHFabricExactAddress(address string, allowZeroPort bool) (string, error) {
	host, rawPort, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return "", fmt.Errorf("split IP:port: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "", fmt.Errorf("host %q must be an IP address", host)
	}
	port, err := strconv.ParseUint(rawPort, 10, 16)
	if err != nil || (!allowZeroPort && port == 0) {
		return "", fmt.Errorf("port %q must be between 1 and 65535", rawPort)
	}
	return net.JoinHostPort(ip.String(), strconv.FormatUint(port, 10)), nil
}

// Close stops the listener and then closes the embedded persistent client.
func (c *SSHFabricListenerCarrier) Close() error {
	listenerErr := c.StopListener(context.Background())
	return errors.Join(listenerErr, c.SSHFabricCarrier.Close())
}

var _ ListenerCarrier = (*SSHFabricListenerCarrier)(nil)
