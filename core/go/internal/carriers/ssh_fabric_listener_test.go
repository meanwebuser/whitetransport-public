package carriers

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"golang.org/x/crypto/ssh"
)

func TestSSHFabricListenerAuthenticatesExactKeyAndAllowsOnlyExactTarget(t *testing.T) {
	echoAddress, stopEcho := startSSHFabricEcho(t)
	defer stopEcho()
	reservedListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	localListenerAddress := reservedListener.Addr().String()
	if err := reservedListener.Close(); err != nil {
		t.Fatal(err)
	}

	hostSigner, hostPrivateKey := generateEncryptedSSHFabricKey(t, "host-passphrase")
	authorizedSigner, authorizedPrivateKey := generateSSHFabricSigner(t)
	_, unauthorizedPrivateKey := generateSSHFabricSigner(t)
	listenerConfig := SSHFabricListenerConfig{
		ListenAddress:            localListenerAddress,
		LocalClientAddress:       localListenerAddress,
		HostPrivateKey:           hostPrivateKey,
		HostPrivateKeyPassphrase: "host-passphrase",
		AuthorizedClientKeys:     []string{string(ssh.MarshalAuthorizedKey(authorizedSigner.PublicKey()))},
		RetentionLimit:           16,
		AllowedTargets:           []string{echoAddress},
	}
	carrier, err := NewSSHFabricCarrierWithListener(SSHConfig{
		Username:   "wt-client",
		PrivateKey: authorizedPrivateKey,
		HostKeys:   []string{string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey()))},
	}, listenerConfig)
	if err != nil {
		t.Fatalf("construct listener carrier: %v", err)
	}
	var _ ListenerCarrier = carrier

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := carrier.StartListener(ctx, Endpoint{ID: "ssh-listener", Carrier: CarrierSSHFabric}); err != nil {
		t.Fatalf("start listener: %v", err)
	}
	defer carrier.StopListener(context.Background())
	address := carrier.ListenerAddress()
	// The node advertises a public address, but its own control reads/writes
	// must use the bound local listener instead of relying on NAT hairpinning.
	endpoint := Endpoint{ID: "ssh-control", Carrier: CarrierSSHFabric, Address: "public.invalid:22"}

	connection, err := carrier.DialStream(ctx, endpoint, echoAddress)
	if err != nil {
		t.Fatalf("authorized exact target: %v", err)
	}
	assertSSHFabricEcho(t, connection, "listener-nonce")
	if err := carrier.Write(ctx, endpoint, fabric.NewEnvelope("local-control", fabric.TrafficControl, "health", []byte("ok"))); err != nil {
		t.Fatalf("node-side local control with public advertised address: %v", err)
	}
	if endpoint.Address != "public.invalid:22" {
		t.Fatalf("local dial override mutated advertised endpoint: %+v", endpoint)
	}

	unauthorized, err := NewSSHFabricCarrier(SSHConfig{
		Username:   "wt-client",
		PrivateKey: unauthorizedPrivateKey,
		HostKeys:   []string{string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey()))},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unauthorized.Close()
	unauthorizedEndpoint := endpoint
	unauthorizedEndpoint.Address = address
	if _, err := unauthorized.Probe(ctx, unauthorizedEndpoint); err == nil {
		t.Fatal("listener accepted an unauthorized client key")
	}

	host, rawPort, err := net.SplitHostPort(echoAddress)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}
	forbiddenNeighbor := net.JoinHostPort(host, strconv.Itoa(port+1))
	if _, err := carrier.DialStream(ctx, endpoint, forbiddenNeighbor); err == nil || !strings.Contains(err.Error(), "administratively prohibited") {
		t.Fatalf("neighbor target error = %v, want SSH prohibition", err)
	}

	if health := carrier.ListenerHealth(); !health.Healthy || !health.Ready {
		t.Fatalf("running listener health = %+v", health)
	}
	if err := carrier.StopListener(context.Background()); err != nil {
		t.Fatalf("stop listener: %v", err)
	}
	if health := carrier.ListenerHealth(); health.Ready {
		t.Fatalf("stopped listener health = %+v", health)
	}
	probe, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
	if err == nil {
		_ = probe.Close()
		t.Fatal("listener still accepts TCP after StopListener")
	}
}

func TestSSHFabricClientOnlyCarrierDoesNotImplementListenerLifecycle(t *testing.T) {
	serverSigner, _ := generateSSHFabricSigner(t)
	_, clientPrivateKey := generateSSHFabricSigner(t)
	carrier, err := NewSSHFabricCarrier(SSHConfig{
		Username:   "wt-client",
		PrivateKey: clientPrivateKey,
		HostKeys:   []string{string(ssh.MarshalAuthorizedKey(serverSigner.PublicKey()))},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer carrier.Close()
	if _, ok := any(carrier).(ListenerCarrier); ok {
		t.Fatal("client-only ssh.fabric carrier must not implement ListenerCarrier")
	}
}

func TestSSHFabricListenerRejectsUnsafeStartupConfig(t *testing.T) {
	hostSigner, encryptedHostKey := generateEncryptedSSHFabricKey(t, "correct-passphrase")
	clientSigner, clientPrivateKey := generateSSHFabricSigner(t)
	validClient := SSHConfig{
		Username:   "wt-client",
		PrivateKey: clientPrivateKey,
		HostKeys:   []string{string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey()))},
	}
	validListener := SSHFabricListenerConfig{
		ListenAddress:            "127.0.0.1:0",
		HostPrivateKey:           encryptedHostKey,
		HostPrivateKeyPassphrase: "correct-passphrase",
		AuthorizedClientKeys:     []string{string(ssh.MarshalAuthorizedKey(clientSigner.PublicKey()))},
		RetentionLimit:           8,
		AllowedTargets:           []string{"127.0.0.1:18080"},
	}

	tests := []struct {
		name   string
		mutate func(*SSHFabricListenerConfig)
		want   string
	}{
		{name: "missing authorised keys", mutate: func(cfg *SSHFabricListenerConfig) { cfg.AuthorizedClientKeys = nil }, want: "authorized client key"},
		{name: "missing target allowlist", mutate: func(cfg *SSHFabricListenerConfig) { cfg.AllowedTargets = nil }, want: "allowed target"},
		{name: "wrong encrypted host key passphrase", mutate: func(cfg *SSHFabricListenerConfig) { cfg.HostPrivateKeyPassphrase = "wrong" }, want: "parse host private key"},
		{name: "hostname target is not exact IP", mutate: func(cfg *SSHFabricListenerConfig) { cfg.AllowedTargets = []string{"example.com:443"} }, want: "IP address"},
		{name: "invalid authorised key", mutate: func(cfg *SSHFabricListenerConfig) { cfg.AuthorizedClientKeys = []string{"not-a-key"} }, want: "parse authorized client key"},
		{name: "unspecified listener needs local dial address", mutate: func(cfg *SSHFabricListenerConfig) { cfg.ListenAddress = "0.0.0.0:0" }, want: "local client address"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			listener := validListener
			test.mutate(&listener)
			_, err := NewSSHFabricCarrierWithListener(validClient, listener)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestSSHFabricListenerLoadsPlainHostKeyFromPath(t *testing.T) {
	hostSigner, hostPrivateKey := generateSSHFabricSigner(t)
	clientSigner, clientPrivateKey := generateSSHFabricSigner(t)
	keyPath := t.TempDir() + "/host-key"
	if err := os.WriteFile(keyPath, []byte(hostPrivateKey), 0o600); err != nil {
		t.Fatal(err)
	}
	carrier, err := NewSSHFabricCarrierWithListener(SSHConfig{
		Username:   "wt-client",
		PrivateKey: clientPrivateKey,
		HostKeys:   []string{string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey()))},
	}, SSHFabricListenerConfig{
		ListenAddress:        "127.0.0.1:0",
		HostPrivateKeyPath:   keyPath,
		AuthorizedClientKeys: []string{string(ssh.MarshalAuthorizedKey(clientSigner.PublicKey()))},
		RetentionLimit:       4,
		AllowedTargets:       []string{"127.0.0.1:18080"},
	})
	if err != nil {
		t.Fatalf("plain host key path: %v", err)
	}
	if err := carrier.Close(); err != nil {
		t.Fatal(err)
	}
}

func generateEncryptedSSHFabricKey(t *testing.T, passphrase string) (ssh.Signer, string) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(privateKey, "wt-listener-test", []byte(passphrase))
	if err != nil {
		t.Fatal(err)
	}
	return signer, string(pem.EncodeToMemory(block))
}

func assertSSHFabricEcho(t *testing.T, connection net.Conn, payload string) {
	t.Helper()
	defer connection.Close()
	if _, err := io.WriteString(connection, payload); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len(payload))
	if _, err := io.ReadFull(connection, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != payload {
		t.Fatalf("echo = %q, want %q", response, payload)
	}
}
