// Package sessionssh issues isolated, short-lived OpenSSH endpoints for one
// WhiteTransport session at a time.
package sessionssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	defaultStartupTimeout = 10 * time.Second
	processStopTimeout    = 3 * time.Second
)

var usernamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// ManagedProcess is an sshd child whose platform-specific ownership can be stopped.
type ManagedProcess interface {
	Stop(context.Context) error
	Done() <-chan error
}

// ProcessStarter starts sshd with the generated configuration.
type ProcessStarter func(path string, args []string, configPath string) (ManagedProcess, error)

// ListenerWaiter waits until sshd accepts TCP connections at address.
type ListenerWaiter func(ctx context.Context, address string) error

// Config defines the host-owned boundaries for session SSH leases.
type Config struct {
	BaseDir       string
	SSHDPath      string
	Username      string
	ListenHost    string
	AdvertiseHost string
	// AllowWildcardListen permits an explicitly configured unspecified IP.
	// Keep false when one concrete interface address is sufficient.
	AllowWildcardListen bool
	PortMin             int
	PortMax             int
	HostKeyFiles        []string
	DefaultTTL          time.Duration
	StartupTimeout      time.Duration
	StartProcess        ProcessStarter
	WaitListener        ListenerWaiter
}

// IssueRequest identifies one authenticated WhiteTransport session.
type IssueRequest struct {
	SessionID string
	TTL       time.Duration
}

// Lease contains the client material and pinned host keys for one SSH endpoint.
// PrivateKey is never written into Directory.
type Lease struct {
	Address        string
	Username       string
	PrivateKey     string
	HostPublicKeys []string
	ExpiresAt      time.Time
	Directory      string

	issuer      *Issuer
	id          string
	process     ManagedProcess
	revokeOnce  sync.Once
	revokeMu    sync.Mutex
	revokeError error
}

// Issuer owns all sshd children and filesystem state below one managed base.
type Issuer struct {
	cfg            Config
	hostPublicKeys []string

	mu        sync.Mutex
	closed    bool
	leases    map[string]*Lease
	reserved  map[string]struct{}
	usedPorts map[int]struct{}
}

// New validates configuration, clears stale state only below BaseDir, and
// prepares an empty issuer.
func New(cfg Config) (*Issuer, error) {
	if err := validateConfig(&cfg); err != nil {
		return nil, err
	}
	hostPublicKeys, err := loadHostPublicKeys(cfg.HostKeyFiles)
	if err != nil {
		return nil, err
	}
	if err := resetManagedBase(cfg.BaseDir); err != nil {
		return nil, err
	}
	return &Issuer{
		cfg:            cfg,
		hostPublicKeys: hostPublicKeys,
		leases:         make(map[string]*Lease),
		reserved:       make(map[string]struct{}),
		usedPorts:      make(map[int]struct{}),
	}, nil
}

// Issue generates one ephemeral client key and starts one isolated sshd.
func (i *Issuer) Issue(ctx context.Context, request IssueRequest) (*Lease, error) {
	if ctx == nil {
		return nil, errors.New("session SSH issue context is required")
	}
	if strings.TrimSpace(request.SessionID) == "" {
		return nil, errors.New("session SSH session ID is required")
	}
	ttl := request.TTL
	if ttl <= 0 {
		ttl = i.cfg.DefaultTTL
	}
	if ttl <= 0 {
		return nil, errors.New("session SSH TTL must be positive")
	}

	id := hashSessionID(request.SessionID)
	port, directory, err := i.reserve(id)
	if err != nil {
		return nil, err
	}
	cleanupReservation := func() {
		i.releaseReservation(id, port)
		_ = os.RemoveAll(directory)
	}

	expiresAt := time.Now().UTC().Add(ttl)
	privateKey, publicKey, err := generateClientKey(id)
	if err != nil {
		cleanupReservation()
		return nil, err
	}
	if err := os.Mkdir(directory, 0o711); err != nil {
		cleanupReservation()
		return nil, fmt.Errorf("create session SSH directory: %w", err)
	}
	authorizedKeysPath := filepath.Join(directory, "authorized_keys")
	// OpenSSH 8.9 accepts YYYYMMDDHHMM[SS] in the server's local timezone;
	// unlike newer parsers it rejects the otherwise common trailing Z suffix.
	authorizedExpiry := expiresAt.Local().Format("20060102150405")
	authorizedLine := fmt.Sprintf("restrict,port-forwarding,expiry-time=\"%s\" %s wt:%s\n", authorizedExpiry, publicKey, id)
	if err := writeAtomic(authorizedKeysPath, []byte(authorizedLine), 0o644); err != nil {
		cleanupReservation()
		return nil, fmt.Errorf("write session authorized keys: %w", err)
	}
	configPath := filepath.Join(directory, "sshd_config")
	configText := i.sshdConfig(port, directory, authorizedKeysPath)
	if err := writeAtomic(configPath, []byte(configText), 0o600); err != nil {
		cleanupReservation()
		return nil, fmt.Errorf("write session sshd config: %w", err)
	}

	args := []string{"-D", "-e", "-f", configPath}
	process, err := i.cfg.StartProcess(i.cfg.SSHDPath, args, configPath)
	if err != nil {
		cleanupReservation()
		return nil, fmt.Errorf("start session sshd: %w", err)
	}
	startupCtx, cancel := context.WithTimeout(ctx, i.cfg.StartupTimeout)
	err = i.cfg.WaitListener(startupCtx, net.JoinHostPort(i.cfg.ListenHost, strconv.Itoa(port)))
	cancel()
	if err != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), processStopTimeout)
		_ = process.Stop(stopCtx)
		stopCancel()
		cleanupReservation()
		return nil, fmt.Errorf("wait for session sshd listener: %w", err)
	}

	lease := &Lease{
		Address:        net.JoinHostPort(i.cfg.AdvertiseHost, strconv.Itoa(port)),
		Username:       i.cfg.Username,
		PrivateKey:     privateKey,
		HostPublicKeys: append([]string(nil), i.hostPublicKeys...),
		ExpiresAt:      expiresAt,
		Directory:      directory,
		issuer:         i,
		id:             id,
		process:        process,
	}
	i.mu.Lock()
	if i.closed {
		i.mu.Unlock()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), processStopTimeout)
		_ = process.Stop(stopCtx)
		stopCancel()
		cleanupReservation()
		return nil, errors.New("session SSH issuer is closed")
	}
	i.leases[id] = lease
	delete(i.reserved, id)
	i.mu.Unlock()

	go lease.watch(ctx, time.Until(expiresAt))
	return lease, nil
}

// Revoke stops the leased sshd process and removes only this lease.
func (l *Lease) Revoke(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.revokeOnce.Do(func() {
		if ctx == nil {
			ctx = context.Background()
		}
		l.issuer.detach(l.id, leasePort(l.Address))
		processErr := l.process.Stop(ctx)
		removeErr := os.RemoveAll(l.Directory)
		l.revokeMu.Lock()
		l.revokeError = errors.Join(processErr, removeErr)
		l.revokeMu.Unlock()
	})
	l.revokeMu.Lock()
	defer l.revokeMu.Unlock()
	return l.revokeError
}

func (l *Lease) watch(ctx context.Context, ttl time.Duration) {
	timer := time.NewTimer(ttl)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	case <-l.process.Done():
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), processStopTimeout)
	defer cancel()
	_ = l.Revoke(stopCtx)
}

// Close revokes every active lease without touching sibling filesystem paths.
func (i *Issuer) Close(ctx context.Context) error {
	i.mu.Lock()
	if i.closed {
		i.mu.Unlock()
		return nil
	}
	i.closed = true
	leases := make([]*Lease, 0, len(i.leases))
	for _, lease := range i.leases {
		leases = append(leases, lease)
	}
	i.mu.Unlock()
	var revokeErrors []error
	for _, lease := range leases {
		if err := lease.Revoke(ctx); err != nil {
			revokeErrors = append(revokeErrors, err)
		}
	}
	return errors.Join(revokeErrors...)
}

func (i *Issuer) reserve(id string) (int, string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return 0, "", errors.New("session SSH issuer is closed")
	}
	if _, exists := i.leases[id]; exists {
		return 0, "", errors.New("session SSH lease already exists")
	}
	if _, exists := i.reserved[id]; exists {
		return 0, "", errors.New("session SSH lease is already being issued")
	}
	directory := filepath.Join(i.cfg.BaseDir, id)
	if _, err := os.Stat(directory); err == nil {
		return 0, "", errors.New("session SSH lease directory already exists")
	} else if !os.IsNotExist(err) {
		return 0, "", fmt.Errorf("inspect session SSH directory: %w", err)
	}
	for port := i.cfg.PortMin; port <= i.cfg.PortMax; port++ {
		if _, used := i.usedPorts[port]; used {
			continue
		}
		listener, err := net.Listen("tcp", net.JoinHostPort(i.cfg.ListenHost, strconv.Itoa(port)))
		if err != nil {
			continue
		}
		_ = listener.Close()
		i.reserved[id] = struct{}{}
		i.usedPorts[port] = struct{}{}
		return port, directory, nil
	}
	return 0, "", errors.New("no free session SSH port")
}

func (i *Issuer) releaseReservation(id string, port int) {
	i.mu.Lock()
	defer i.mu.Unlock()
	delete(i.leases, id)
	delete(i.reserved, id)
	delete(i.usedPorts, port)
}

func (i *Issuer) detach(id string, port int) {
	i.releaseReservation(id, port)
}

func (i *Issuer) sshdConfig(port int, directory string, authorizedKeysPath string) string {
	lines := []string{
		"Port " + strconv.Itoa(port),
		"ListenAddress " + i.cfg.ListenHost,
		"Protocol 2",
		"PidFile " + quoteConfigPath(filepath.Join(directory, "sshd.pid")),
		"AuthorizedKeysFile " + quoteConfigPath(authorizedKeysPath),
		"StrictModes yes",
		"PubkeyAuthentication yes",
		"PasswordAuthentication no",
		"KbdInteractiveAuthentication no",
		"PermitRootLogin no",
		"UsePAM no",
		"AllowUsers " + i.cfg.Username,
		"AllowTcpForwarding local",
		"AllowStreamLocalForwarding no",
		"GatewayPorts no",
		"PermitOpen any",
		"PermitListen none",
		"PermitTunnel no",
		"X11Forwarding no",
		"AllowAgentForwarding no",
		"PermitTTY no",
		"PermitUserRC no",
		"ForceCommand /usr/sbin/nologin",
		"LogLevel ERROR",
	}
	for _, hostKeyFile := range i.cfg.HostKeyFiles {
		lines = append(lines, "HostKey "+quoteConfigPath(hostKeyFile))
	}
	return strings.Join(lines, "\n") + "\n"
}

func validateConfig(cfg *Config) error {
	if strings.TrimSpace(cfg.BaseDir) == "" {
		return errors.New("session SSH base directory is required")
	}
	absBase, err := filepath.Abs(cfg.BaseDir)
	if err != nil {
		return fmt.Errorf("resolve session SSH base directory: %w", err)
	}
	if absBase == string(filepath.Separator) {
		return errors.New("session SSH base directory cannot be filesystem root")
	}
	if len(strings.Split(strings.Trim(absBase, string(filepath.Separator)), string(filepath.Separator))) < 3 {
		return errors.New("session SSH base directory must be a dedicated path at least three levels deep")
	}
	cfg.BaseDir = filepath.Clean(absBase)
	if strings.TrimSpace(cfg.SSHDPath) == "" || strings.ContainsAny(cfg.SSHDPath, "\r\n") {
		return errors.New("valid sshd path is required")
	}
	if !usernamePattern.MatchString(cfg.Username) {
		return errors.New("valid dedicated SSH username is required")
	}
	listenIP := net.ParseIP(cfg.ListenHost)
	if listenIP == nil {
		return errors.New("session SSH listen host must be a literal IP address")
	}
	if listenIP.IsUnspecified() && !cfg.AllowWildcardListen {
		return errors.New("wildcard session SSH listen host requires explicit opt-in")
	}
	if strings.TrimSpace(cfg.AdvertiseHost) == "" || strings.ContainsAny(cfg.AdvertiseHost, "\r\n") {
		return errors.New("session SSH advertise host is required")
	}
	if cfg.PortMin < 1024 || cfg.PortMax > 65535 || cfg.PortMin > cfg.PortMax {
		return errors.New("session SSH port range must contain valid high ports")
	}
	if len(cfg.HostKeyFiles) == 0 {
		return errors.New("at least one SSH host key file is required")
	}
	for _, path := range cfg.HostKeyFiles {
		if !filepath.IsAbs(path) || strings.ContainsAny(path, "\r\n") {
			return errors.New("SSH host key paths must be absolute and single-line")
		}
	}
	if cfg.DefaultTTL <= 0 {
		return errors.New("default session SSH TTL must be positive")
	}
	if cfg.StartupTimeout <= 0 {
		cfg.StartupTimeout = defaultStartupTimeout
	}
	if cfg.StartProcess == nil {
		cfg.StartProcess = startOpenSSH
	}
	if cfg.WaitListener == nil {
		cfg.WaitListener = waitForListener
	}
	return nil
}

func resetManagedBase(baseDir string) error {
	if info, err := os.Lstat(baseDir); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("session SSH base directory cannot be a symlink")
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("inspect session SSH base directory: %w", err)
	}
	if err := os.RemoveAll(baseDir); err != nil {
		return fmt.Errorf("clear stale session SSH state: %w", err)
	}
	if err := os.MkdirAll(baseDir, 0o711); err != nil {
		return fmt.Errorf("create session SSH base directory: %w", err)
	}
	return nil
}

func loadHostPublicKeys(paths []string) ([]string, error) {
	keys := make([]string, 0, len(paths))
	for _, path := range paths {
		payload, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read SSH host key %s: %w", path, err)
		}
		signer, err := ssh.ParsePrivateKey(payload)
		if err != nil {
			return nil, fmt.Errorf("parse SSH host key %s: %w", path, err)
		}
		keys = append(keys, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))))
	}
	sort.Strings(keys)
	return keys, nil
}

func generateClientKey(comment string) (string, string, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate ephemeral SSH key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(privateKey, "wt-session-"+comment)
	if err != nil {
		return "", "", fmt.Errorf("marshal ephemeral SSH private key: %w", err)
	}
	sshPublicKey, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		return "", "", fmt.Errorf("marshal ephemeral SSH public key: %w", err)
	}
	return string(pem.EncodeToMemory(block)), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPublicKey))), nil
}

func hashSessionID(sessionID string) string {
	digest := sha256.Sum256([]byte(sessionID))
	return hex.EncodeToString(digest[:])
}

func writeAtomic(path string, payload []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".sessionssh-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func quoteConfigPath(path string) string {
	escaped := strings.ReplaceAll(path, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

func leasePort(address string) int {
	_, rawPort, err := net.SplitHostPort(address)
	if err != nil {
		return 0
	}
	port, _ := strconv.Atoi(rawPort)
	return port
}

func waitForListener(ctx context.Context, address string) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		connection, err := (&net.Dialer{Timeout: 250 * time.Millisecond}).DialContext(ctx, "tcp", address)
		if err == nil {
			_ = connection.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
