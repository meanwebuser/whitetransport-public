package sessionssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

type fakeProcess struct {
	mu        sync.Mutex
	stopCount int
	done      chan error
}

func newFakeProcess() *fakeProcess {
	return &fakeProcess{done: make(chan error)}
}

func (p *fakeProcess) Stop(context.Context) error {
	p.mu.Lock()
	p.stopCount++
	p.mu.Unlock()
	select {
	case <-p.done:
	default:
		close(p.done)
	}
	return nil
}

func (p *fakeProcess) Done() <-chan error { return p.done }

func (p *fakeProcess) exit() {
	select {
	case <-p.done:
	default:
		close(p.done)
	}
}

func (p *fakeProcess) stops() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopCount
}

type fakeStarter struct {
	mu        sync.Mutex
	processes []*fakeProcess
	configs   []string
	args      [][]string
}

func (s *fakeStarter) start(_ string, args []string, configPath string) (ManagedProcess, error) {
	p := newFakeProcess()
	s.mu.Lock()
	s.processes = append(s.processes, p)
	s.configs = append(s.configs, configPath)
	s.args = append(s.args, append([]string(nil), args...))
	s.mu.Unlock()
	return p, nil
}

func writeTestHostKey(t *testing.T, dir string) string {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(privateKey, "test-host")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ssh_host_ed25519_key")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testConfig(t *testing.T, baseDir string, starter *fakeStarter) Config {
	t.Helper()
	return Config{
		BaseDir:        baseDir,
		SSHDPath:       "/usr/sbin/sshd",
		Username:       "wt-egress",
		ListenHost:     "127.0.0.1",
		AdvertiseHost:  "192.0.2.88",
		PortMin:        22000,
		PortMax:        22020,
		HostKeyFiles:   []string{writeTestHostKey(t, t.TempDir())},
		DefaultTTL:     time.Minute,
		StartupTimeout: time.Second,
		StartProcess:   starter.start,
		WaitListener:   func(context.Context, string) error { return nil },
	}
}

func TestIssuerCreatesIsolatedLeaseAndRevokesIdempotently(t *testing.T) {
	baseDir := filepath.Join(t.TempDir(), "sessions")
	starter := &fakeStarter{}
	issuer, err := New(testConfig(t, baseDir, starter))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = issuer.Close(context.Background()) })

	lease, err := issuer.Issue(context.Background(), IssueRequest{SessionID: "../../escape", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(lease.Address, "192.0.2.88:") || lease.Username != "wt-egress" {
		t.Fatalf("unexpected lease endpoint: %+v", lease)
	}
	if !strings.Contains(lease.PrivateKey, "OPENSSH PRIVATE KEY") {
		t.Fatal("lease did not return an inline OpenSSH private key")
	}
	if len(lease.HostPublicKeys) != 1 || !strings.HasPrefix(lease.HostPublicKeys[0], "ssh-ed25519 ") {
		t.Fatalf("unexpected pinned host keys: %#v", lease.HostPublicKeys)
	}
	if strings.Contains(lease.Directory, "escape") || filepath.Dir(lease.Directory) != baseDir {
		t.Fatalf("session ID escaped or leaked into directory: %s", lease.Directory)
	}

	authorized, err := os.ReadFile(filepath.Join(lease.Directory, "authorized_keys"))
	if err != nil {
		t.Fatal(err)
	}
	authorizedText := string(authorized)
	for _, required := range []string{"restrict", "port-forwarding", "expiry-time=\""} {
		if !strings.Contains(authorizedText, required) {
			t.Fatalf("authorized_keys missing %q: %s", required, authorizedText)
		}
	}
	if strings.Contains(authorizedText, "Z\"") {
		t.Fatalf("OpenSSH 8.9 rejects a Z suffix in authorized_keys expiry-time: %s", authorizedText)
	}
	if strings.Contains(authorizedText, "PRIVATE KEY") {
		t.Fatal("private key was written to the lease directory")
	}

	config, err := os.ReadFile(filepath.Join(lease.Directory, "sshd_config"))
	if err != nil {
		t.Fatal(err)
	}
	configText := string(config)
	for _, required := range []string{"ListenAddress 127.0.0.1", "AllowUsers wt-egress", "AllowTcpForwarding local", "AllowStreamLocalForwarding no", "PermitTunnel no", "AuthorizedKeysFile "} {
		if !strings.Contains(configText, required) {
			t.Fatalf("sshd_config missing %q: %s", required, configText)
		}
	}

	process := starter.processes[0]
	if err := lease.Revoke(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := lease.Revoke(context.Background()); err != nil {
		t.Fatal(err)
	}
	if process.stops() != 1 {
		t.Fatalf("process stop count = %d, want 1", process.stops())
	}
	if _, err := os.Stat(lease.Directory); !os.IsNotExist(err) {
		t.Fatalf("lease directory remains after revoke: %v", err)
	}
}

func TestIssuerAllowsConcreteLANListenAddress(t *testing.T) {
	baseDir := filepath.Join(t.TempDir(), "sessions")
	starter := &fakeStarter{}
	cfg := testConfig(t, baseDir, starter)
	cfg.ListenHost = "192.0.2.88"
	issuer, err := New(cfg)
	if err != nil {
		t.Fatalf("concrete LAN listen address was rejected: %v", err)
	}
	_ = issuer.Close(context.Background())
}

func TestIssuerRequiresOptInForWildcardListenAddress(t *testing.T) {
	baseDir := filepath.Join(t.TempDir(), "sessions")
	starter := &fakeStarter{}
	cfg := testConfig(t, baseDir, starter)
	cfg.ListenHost = "0.0.0.0"
	if _, err := New(cfg); err == nil {
		t.Fatal("wildcard listen address was accepted without explicit opt-in")
	}
	cfg.AllowWildcardListen = true
	issuer, err := New(cfg)
	if err != nil {
		t.Fatalf("explicit wildcard listen address was rejected: %v", err)
	}
	_ = issuer.Close(context.Background())
}

func TestIssuerConcurrentLeasesAreIsolated(t *testing.T) {
	baseDir := filepath.Join(t.TempDir(), "sessions")
	starter := &fakeStarter{}
	issuer, err := New(testConfig(t, baseDir, starter))
	if err != nil {
		t.Fatal(err)
	}

	first, err := issuer.Issue(context.Background(), IssueRequest{SessionID: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := issuer.Issue(context.Background(), IssueRequest{SessionID: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Directory == second.Directory || first.Address == second.Address {
		t.Fatalf("leases are not isolated: first=%+v second=%+v", first, second)
	}
	if err := first.Revoke(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(second.Directory); err != nil {
		t.Fatalf("revoking first lease damaged second: %v", err)
	}
	if starter.processes[1].stops() != 0 {
		t.Fatal("revoking first lease stopped the second process")
	}
	if err := issuer.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if starter.processes[1].stops() != 1 {
		t.Fatal("issuer close did not stop the second process")
	}
}

func TestIssuerRejectsConcurrentDuplicateSession(t *testing.T) {
	baseDir := filepath.Join(t.TempDir(), "sessions")
	starter := &fakeStarter{}
	issuer, err := New(testConfig(t, baseDir, starter))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = issuer.Close(context.Background()) })
	first, err := issuer.Issue(context.Background(), IssueRequest{SessionID: "same-session"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := issuer.Issue(context.Background(), IssueRequest{SessionID: "same-session"}); err == nil {
		t.Fatal("duplicate active session lease was accepted")
	}
	if _, err := os.Stat(first.Directory); err != nil {
		t.Fatalf("duplicate request damaged active lease: %v", err)
	}
}

func TestIssuerAutoRevokesOnTTL(t *testing.T) {
	baseDir := filepath.Join(t.TempDir(), "sessions")
	starter := &fakeStarter{}
	issuer, err := New(testConfig(t, baseDir, starter))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = issuer.Close(context.Background()) })

	lease, err := issuer.Issue(context.Background(), IssueRequest{SessionID: "expires", TTL: 25 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(lease.Directory); os.IsNotExist(err) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(lease.Directory); !os.IsNotExist(err) {
		t.Fatalf("expired lease directory remains: %v", err)
	}
	if starter.processes[0].stops() != 1 {
		t.Fatal("expired lease did not stop its sshd")
	}
}

func TestIssuerAutoRevokesOnContextCancellation(t *testing.T) {
	baseDir := filepath.Join(t.TempDir(), "sessions")
	starter := &fakeStarter{}
	issuer, err := New(testConfig(t, baseDir, starter))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = issuer.Close(context.Background()) })
	ctx, cancel := context.WithCancel(context.Background())
	lease, err := issuer.Issue(ctx, IssueRequest{SessionID: "cancelled"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(lease.Directory); os.IsNotExist(err) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(lease.Directory); !os.IsNotExist(err) {
		t.Fatalf("cancelled lease directory remains: %v", err)
	}
}

func TestIssuerAutoRevokesWhenSSHDExits(t *testing.T) {
	baseDir := filepath.Join(t.TempDir(), "sessions")
	starter := &fakeStarter{}
	issuer, err := New(testConfig(t, baseDir, starter))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = issuer.Close(context.Background()) })
	lease, err := issuer.Issue(context.Background(), IssueRequest{SessionID: "process-exit"})
	if err != nil {
		t.Fatal(err)
	}
	starter.processes[0].exit()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(lease.Directory); os.IsNotExist(err) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(lease.Directory); !os.IsNotExist(err) {
		t.Fatalf("exited sshd lease directory remains: %v", err)
	}
}

func TestNewCleansOnlyManagedBaseDirectory(t *testing.T) {
	root := t.TempDir()
	baseDir := filepath.Join(root, "sessions")
	sibling := filepath.Join(root, "keep")
	if err := os.MkdirAll(baseDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, "stale"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sibling, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	starter := &fakeStarter{}
	issuer, err := New(testConfig(t, baseDir, starter))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = issuer.Close(context.Background()) })
	if _, err := os.Stat(filepath.Join(baseDir, "stale")); !os.IsNotExist(err) {
		t.Fatalf("stale managed state remains: %v", err)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Fatalf("constructor removed a sibling path: %v", err)
	}
}
