package carriers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"golang.org/x/crypto/ssh"
)

const (
	sshFabricChannelType = "wt.fabric.v1"
	sshFabricMaxJSON     = 8 << 20
)

type sshFabricOperation string

const (
	sshFabricAppend sshFabricOperation = "append"
	sshFabricRead   sshFabricOperation = "read"
)

type sshFabricRequest struct {
	Operation sshFabricOperation `json:"operation"`
	Mailbox   string             `json:"mailbox"`
	Cursor    Cursor             `json:"cursor,omitempty"`
	Envelope  *fabric.Envelope   `json:"envelope,omitempty"`
}

type sshFabricResponse struct {
	Envelopes []fabric.Envelope `json:"envelopes,omitempty"`
	Cursor    Cursor            `json:"cursor,omitempty"`
	Error     string            `json:"error,omitempty"`
}

// SSHFabricCarrier multiplexes retained control envelopes and direct TCP
// streams over one host-key-pinned SSH connection.
type SSHFabricCarrier struct {
	desc Descriptor
	cfg  SSHConfig

	mu      sync.Mutex
	address string
	client  *ssh.Client
	closed  bool

	connectionAddressOverride func(string) string
}

// NewSSHFabricCarrier creates a public-key-authenticated SSH fabric carrier.
// A server host-key pin is mandatory; the carrier never accepts an unknown key.
func NewSSHFabricCarrier(cfg SSHConfig) (*SSHFabricCarrier, error) {
	if strings.TrimSpace(cfg.Username) == "" {
		return nil, fmt.Errorf("ssh.fabric: username is required")
	}
	if len(cfg.HostKeys) == 0 {
		return nil, fmt.Errorf("ssh.fabric: at least one pinned server host key is required")
	}
	if strings.TrimSpace(cfg.Password) != "" {
		return nil, fmt.Errorf("ssh.fabric: password authentication is not supported")
	}
	if strings.TrimSpace(cfg.PrivateKey) == "" && strings.TrimSpace(cfg.PrivateKeyPath) != "" {
		privateKey, err := os.ReadFile(cfg.PrivateKeyPath)
		if err != nil {
			return nil, fmt.Errorf("ssh.fabric: read private key: %w", err)
		}
		cfg.PrivateKey = string(privateKey)
	}
	if strings.TrimSpace(cfg.PrivateKey) == "" {
		return nil, fmt.Errorf("ssh.fabric: private key is required")
	}
	if _, err := sshFabricClientConfig(cfg); err != nil {
		return nil, err
	}
	desc, err := FindStandardDescriptor(CarrierSSHFabric)
	if err != nil {
		return nil, err
	}
	return &SSHFabricCarrier{desc: desc, cfg: cfg}, nil
}

// Descriptor returns the policy-facing SSH fabric capabilities.
func (c *SSHFabricCarrier) Descriptor() Descriptor { return c.desc }

// IsNative marks SSH fabric as a native Go carrier.
func (c *SSHFabricCarrier) IsNative() {}

// Write appends one encrypted fabric envelope to the endpoint mailbox.
func (c *SSHFabricCarrier) Write(ctx context.Context, endpoint Endpoint, envelope fabric.Envelope) error {
	response, err := c.request(ctx, endpoint, sshFabricRequest{
		Operation: sshFabricAppend,
		Mailbox:   endpoint.ID,
		Envelope:  &envelope,
	})
	if err != nil {
		return err
	}
	if response.Error != "" {
		return fmt.Errorf("ssh.fabric: append rejected: %s", response.Error)
	}
	return nil
}

// Read returns retained envelopes after cursor and advances the mailbox cursor.
func (c *SSHFabricCarrier) Read(ctx context.Context, endpoint Endpoint, cursor Cursor) (ReadResult, error) {
	response, err := c.request(ctx, endpoint, sshFabricRequest{
		Operation: sshFabricRead,
		Mailbox:   endpoint.ID,
		Cursor:    cursor,
	})
	if err != nil {
		return ReadResult{}, err
	}
	if response.Error != "" {
		return ReadResult{}, fmt.Errorf("ssh.fabric: read rejected: %s", response.Error)
	}
	return ReadResult{Envelopes: response.Envelopes, Cursor: response.Cursor}, nil
}

// Probe verifies that the pinned SSH session can be established.
func (c *SSHFabricCarrier) Probe(ctx context.Context, endpoint Endpoint) (Metrics, error) {
	started := time.Now()
	if _, err := c.connection(ctx, endpoint.Address); err != nil {
		return Metrics{Healthy: false, FailureReason: err.Error()}, err
	}
	metrics := c.desc.Metrics
	metrics.Healthy = true
	metrics.Latency = time.Since(started)
	metrics.LastOK = time.Now()
	return metrics, nil
}

// DeleteMessage is intentionally unsupported by the append-only SSH mailbox.
func (c *SSHFabricCarrier) DeleteMessage(context.Context, Endpoint, string) error {
	return fmt.Errorf("ssh.fabric is append-only and does not support message deletion")
}

// DialStream opens direct-tcpip through the same persistent SSH connection
// used by control requests.
func (c *SSHFabricCarrier) DialStream(ctx context.Context, endpoint Endpoint, targetAddr string) (net.Conn, error) {
	client, err := c.connection(ctx, endpoint.Address)
	if err != nil {
		return nil, err
	}
	conn, err := client.Dial("tcp", targetAddr)
	if err != nil {
		c.invalidate(client)
		return nil, fmt.Errorf("ssh.fabric: dial target %s: %w", targetAddr, err)
	}
	return conn, nil
}

// Close terminates the persistent SSH connection. The carrier cannot be reused.
func (c *SSHFabricCarrier) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.client == nil {
		return nil
	}
	err := c.client.Close()
	c.client = nil
	c.address = ""
	return err
}

func (c *SSHFabricCarrier) request(ctx context.Context, endpoint Endpoint, request sshFabricRequest) (sshFabricResponse, error) {
	if strings.TrimSpace(endpoint.ID) == "" {
		return sshFabricResponse{}, fmt.Errorf("ssh.fabric: endpoint ID is required")
	}
	client, err := c.connection(ctx, endpoint.Address)
	if err != nil {
		return sshFabricResponse{}, err
	}
	channel, requests, err := client.OpenChannel(sshFabricChannelType, nil)
	if err != nil {
		c.invalidate(client)
		return sshFabricResponse{}, fmt.Errorf("ssh.fabric: open control channel: %w", err)
	}
	defer channel.Close()
	go ssh.DiscardRequests(requests)
	if err := json.NewEncoder(channel).Encode(request); err != nil {
		return sshFabricResponse{}, fmt.Errorf("ssh.fabric: encode control request: %w", err)
	}
	if err := channel.CloseWrite(); err != nil {
		return sshFabricResponse{}, fmt.Errorf("ssh.fabric: finish control request: %w", err)
	}
	var response sshFabricResponse
	if err := json.NewDecoder(io.LimitReader(channel, sshFabricMaxJSON)).Decode(&response); err != nil {
		return sshFabricResponse{}, fmt.Errorf("ssh.fabric: decode control response: %w", err)
	}
	select {
	case <-ctx.Done():
		return sshFabricResponse{}, ctx.Err()
	default:
		return response, nil
	}
}

func (c *SSHFabricCarrier) connection(ctx context.Context, address string) (*ssh.Client, error) {
	if c.connectionAddressOverride != nil {
		address = c.connectionAddressOverride(address)
	}
	address = strings.TrimSpace(address)
	if address == "" {
		return nil, fmt.Errorf("ssh.fabric: endpoint address is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, fmt.Errorf("ssh.fabric: carrier is closed")
	}
	if c.client != nil && c.address == address {
		return c.client, nil
	}
	if c.client != nil {
		_ = c.client.Close()
		c.client = nil
		c.address = ""
	}
	clientConfig, err := sshFabricClientConfig(c.cfg)
	if err != nil {
		return nil, err
	}
	dialer := net.Dialer{Timeout: clientConfig.Timeout}
	raw, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("ssh.fabric: dial server %s: %w", address, err)
	}
	connection, channels, requests, err := ssh.NewClientConn(raw, address, clientConfig)
	if err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("ssh.fabric: handshake %s: %w", address, err)
	}
	c.client = ssh.NewClient(connection, channels, requests)
	c.address = address
	return c.client, nil
}

func (c *SSHFabricCarrier) invalidate(client *ssh.Client) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != client {
		return
	}
	_ = c.client.Close()
	c.client = nil
	c.address = ""
}

func sshFabricClientConfig(cfg SSHConfig) (*ssh.ClientConfig, error) {
	var (
		signer ssh.Signer
		err    error
	)
	if strings.TrimSpace(cfg.PrivateKeyPassphrase) == "" {
		signer, err = ssh.ParsePrivateKey([]byte(cfg.PrivateKey))
	} else {
		signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(cfg.PrivateKey), []byte(cfg.PrivateKeyPassphrase))
	}
	if err != nil {
		return nil, fmt.Errorf("ssh.fabric: parse private key: %w", err)
	}
	hostKeyCallback, err := sshKnownHostKeysCallback(cfg.HostKeys)
	if err != nil {
		return nil, fmt.Errorf("ssh.fabric: configure host key pins: %w", err)
	}
	return &ssh.ClientConfig{
		User:            cfg.Username,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: hostKeyCallback,
		Timeout:         10 * time.Second,
	}, nil
}

// SSHFabricServerConfig defines the authenticated SSH broker and its bounded
// retained-control and egress policy.
type SSHFabricServerConfig struct {
	SSHConfig      *ssh.ServerConfig
	RetentionLimit int
	AllowTarget    func(address string) error
}

type sshFabricRecord struct {
	sequence uint64
	envelope fabric.Envelope
}

type sshFabricMailbox struct {
	nextSequence uint64
	records      []sshFabricRecord
}

// SSHFabricServer accepts only wt.fabric.v1 and direct-tcpip channels.
type SSHFabricServer struct {
	listener net.Listener
	config   SSHFabricServerConfig

	mailboxMu sync.Mutex
	mailboxes map[string]*sshFabricMailbox

	connectionsMu sync.Mutex
	connections   map[*ssh.ServerConn]struct{}
	count         atomic.Uint64
	closeOnce     sync.Once
}

// NewSSHFabricServer creates a broker around an already-bound listener.
// Authentication and host keys must be supplied explicitly in SSHConfig.
func NewSSHFabricServer(listener net.Listener, config SSHFabricServerConfig) (*SSHFabricServer, error) {
	if listener == nil {
		return nil, fmt.Errorf("ssh.fabric server: listener is required")
	}
	if config.SSHConfig == nil {
		return nil, fmt.Errorf("ssh.fabric server: SSH config is required")
	}
	if config.SSHConfig.PublicKeyCallback == nil {
		return nil, fmt.Errorf("ssh.fabric server: public-key authentication is required")
	}
	if config.RetentionLimit <= 0 {
		return nil, fmt.Errorf("ssh.fabric server: positive retention limit is required")
	}
	if config.AllowTarget == nil {
		return nil, fmt.Errorf("ssh.fabric server: target allow callback is required")
	}
	return &SSHFabricServer{
		listener:    listener,
		config:      config,
		mailboxes:   make(map[string]*sshFabricMailbox),
		connections: make(map[*ssh.ServerConn]struct{}),
	}, nil
}

// Serve accepts authenticated SSH sessions until ctx is cancelled or Close is called.
func (s *SSHFabricServer) Serve(ctx context.Context) error {
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close()
		case <-stop:
		}
	}()
	defer close(stop)
	for {
		raw, err := s.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("ssh.fabric server: accept: %w", err)
		}
		go s.serveConnection(raw)
	}
}

// ConnectionCount reports completed SSH handshakes since server creation.
func (s *SSHFabricServer) ConnectionCount() uint64 { return s.count.Load() }

// Close stops accepting sessions and closes all authenticated connections.
func (s *SSHFabricServer) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		closeErr = s.listener.Close()
		s.connectionsMu.Lock()
		for connection := range s.connections {
			_ = connection.Close()
		}
		s.connectionsMu.Unlock()
	})
	if errors.Is(closeErr, net.ErrClosed) {
		return nil
	}
	return closeErr
}

func (s *SSHFabricServer) serveConnection(raw net.Conn) {
	connection, channels, requests, err := ssh.NewServerConn(raw, s.config.SSHConfig)
	if err != nil {
		_ = raw.Close()
		return
	}
	s.count.Add(1)
	s.connectionsMu.Lock()
	s.connections[connection] = struct{}{}
	s.connectionsMu.Unlock()
	defer func() {
		s.connectionsMu.Lock()
		delete(s.connections, connection)
		s.connectionsMu.Unlock()
		_ = connection.Close()
	}()
	go ssh.DiscardRequests(requests)
	for channel := range channels {
		switch channel.ChannelType() {
		case sshFabricChannelType:
			go s.serveControlChannel(channel)
		case "direct-tcpip":
			go s.serveDirectTCPIP(channel)
		default:
			_ = channel.Reject(ssh.UnknownChannelType, "channel type is not allowed")
		}
	}
}

func (s *SSHFabricServer) serveControlChannel(channelRequest ssh.NewChannel) {
	channel, requests, err := channelRequest.Accept()
	if err != nil {
		return
	}
	defer channel.Close()
	go ssh.DiscardRequests(requests)
	var request sshFabricRequest
	if err := json.NewDecoder(io.LimitReader(channel, sshFabricMaxJSON)).Decode(&request); err != nil {
		_ = json.NewEncoder(channel).Encode(sshFabricResponse{Error: "invalid control request"})
		return
	}
	response := s.handleControl(request)
	_ = json.NewEncoder(channel).Encode(response)
}

func (s *SSHFabricServer) handleControl(request sshFabricRequest) sshFabricResponse {
	if strings.TrimSpace(request.Mailbox) == "" {
		return sshFabricResponse{Error: "mailbox is required"}
	}
	s.mailboxMu.Lock()
	defer s.mailboxMu.Unlock()
	mailbox := s.mailboxes[request.Mailbox]
	if mailbox == nil {
		mailbox = &sshFabricMailbox{}
		s.mailboxes[request.Mailbox] = mailbox
	}
	switch request.Operation {
	case sshFabricAppend:
		if request.Envelope == nil {
			return sshFabricResponse{Error: "envelope is required"}
		}
		mailbox.nextSequence++
		mailbox.records = append(mailbox.records, sshFabricRecord{sequence: mailbox.nextSequence, envelope: *request.Envelope})
		if overflow := len(mailbox.records) - s.config.RetentionLimit; overflow > 0 {
			mailbox.records = append([]sshFabricRecord(nil), mailbox.records[overflow:]...)
		}
		return sshFabricResponse{Cursor: Cursor(strconv.FormatUint(mailbox.nextSequence, 10))}
	case sshFabricRead:
		cursor, err := parseSSHFabricCursor(request.Cursor)
		if err != nil {
			return sshFabricResponse{Error: err.Error()}
		}
		envelopes := make([]fabric.Envelope, 0, len(mailbox.records))
		for _, record := range mailbox.records {
			if record.sequence > cursor {
				envelopes = append(envelopes, record.envelope)
			}
		}
		return sshFabricResponse{
			Envelopes: envelopes,
			Cursor:    Cursor(strconv.FormatUint(mailbox.nextSequence, 10)),
		}
	default:
		return sshFabricResponse{Error: "unsupported operation"}
	}
}

func parseSSHFabricCursor(cursor Cursor) (uint64, error) {
	if strings.TrimSpace(string(cursor)) == "" {
		return 0, nil
	}
	value, err := strconv.ParseUint(string(cursor), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid cursor")
	}
	return value, nil
}

type sshDirectTCPIPRequest struct {
	DestinationAddress string
	DestinationPort    uint32
	OriginatorAddress  string
	OriginatorPort     uint32
}

func (s *SSHFabricServer) serveDirectTCPIP(channelRequest ssh.NewChannel) {
	var request sshDirectTCPIPRequest
	if err := ssh.Unmarshal(channelRequest.ExtraData(), &request); err != nil {
		_ = channelRequest.Reject(ssh.ConnectionFailed, "invalid direct-tcpip request")
		return
	}
	target := net.JoinHostPort(request.DestinationAddress, strconv.FormatUint(uint64(request.DestinationPort), 10))
	if err := s.config.AllowTarget(target); err != nil {
		_ = channelRequest.Reject(ssh.Prohibited, "target is not allowed")
		return
	}
	targetConnection, err := (&net.Dialer{Timeout: 10 * time.Second}).Dial("tcp", target)
	if err != nil {
		_ = channelRequest.Reject(ssh.ConnectionFailed, "target connection failed")
		return
	}
	channel, requests, err := channelRequest.Accept()
	if err != nil {
		_ = targetConnection.Close()
		return
	}
	go ssh.DiscardRequests(requests)
	go relaySSHFabricStream(channel, targetConnection)
}

func relaySSHFabricStream(channel ssh.Channel, target net.Conn) {
	defer channel.Close()
	defer target.Close()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(target, channel)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(channel, target)
		done <- struct{}{}
	}()
	<-done
}
