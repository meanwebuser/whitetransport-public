package carriers

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"golang.org/x/crypto/ssh"
)

func TestSSHFabricWriteRejectsMismatchedPinnedHostKey(t *testing.T) {
	serverSigner, _ := generateSSHFabricSigner(t)
	clientSigner, clientKey := generateSSHFabricSigner(t)
	wrongServerSigner, _ := generateSSHFabricSigner(t)
	listener, broker := startSSHFabricSecurityBroker(t, serverSigner, clientSigner, func(string) error { return nil })

	carrier, err := NewSSHFabricCarrier(SSHConfig{
		Username:   "wt-client",
		PrivateKey: clientKey,
		HostKeys:   []string{string(ssh.MarshalAuthorizedKey(wrongServerSigner.PublicKey()))},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = carrier.Close() })

	endpoint := Endpoint{ID: "security-control", Carrier: CarrierSSHFabric, Address: listener.Addr().String()}
	envelope := fabric.NewEnvelope("security-1", fabric.TrafficControl, "node.advertise", []byte("node-88"))
	err = carrier.Write(context.Background(), endpoint, envelope)
	if err == nil {
		t.Fatal("write with a mismatched pinned server key succeeded")
	}
	if !strings.Contains(err.Error(), "host key") && !strings.Contains(err.Error(), "knownhosts") {
		t.Fatalf("write failed outside host-key verification: %v", err)
	}
	if got := broker.ConnectionCount(); got != 0 {
		t.Fatalf("broker counted %d authenticated connections after pin mismatch, want 0", got)
	}
}

func TestSSHFabricAllowTargetRejectsForbiddenDirectTCPIP(t *testing.T) {
	serverSigner, _ := generateSSHFabricSigner(t)
	clientSigner, clientKey := generateSSHFabricSigner(t)
	forbiddenTarget := "127.0.0.1:9"
	allowedChecks := make(chan string, 1)
	listener, _ := startSSHFabricSecurityBroker(t, serverSigner, clientSigner, func(address string) error {
		allowedChecks <- address
		return fmt.Errorf("forbidden target")
	})

	carrier, err := NewSSHFabricCarrier(SSHConfig{
		Username:   "wt-client",
		PrivateKey: clientKey,
		HostKeys:   []string{string(ssh.MarshalAuthorizedKey(serverSigner.PublicKey()))},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = carrier.Close() })

	endpoint := Endpoint{ID: "security-egress", Carrier: CarrierSSHFabric, Address: listener.Addr().String()}
	stream, err := carrier.DialStream(context.Background(), endpoint, forbiddenTarget)
	if err == nil {
		_ = stream.Close()
		t.Fatal("direct-tcpip to a forbidden target succeeded")
	}
	select {
	case checked := <-allowedChecks:
		if checked != forbiddenTarget {
			t.Fatalf("AllowTarget checked %q, want %q", checked, forbiddenTarget)
		}
	case <-time.After(time.Second):
		t.Fatal("AllowTarget was not consulted")
	}
}

func TestSSHFabricBrokerRejectsInteractiveAndHTTPSidechannels(t *testing.T) {
	serverSigner, _ := generateSSHFabricSigner(t)
	clientSigner, clientKey := generateSSHFabricSigner(t)
	listener, _ := startSSHFabricSecurityBroker(t, serverSigner, clientSigner, func(string) error { return nil })
	address := listener.Addr().String()

	clientConfig, err := sshFabricClientConfig(SSHConfig{
		Username:   "wt-client",
		PrivateKey: clientKey,
		HostKeys:   []string{string(ssh.MarshalAuthorizedKey(serverSigner.PublicKey()))},
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := ssh.Dial("tcp", address, clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	for _, channelType := range []string{"session", "shell"} {
		channel, _, openErr := client.OpenChannel(channelType, nil)
		if openErr == nil {
			_ = channel.Close()
			t.Fatalf("broker accepted forbidden %q channel", channelType)
		}
	}

	raw, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := raw.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Write([]byte("GET /health HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 256)
	n, readErr := raw.Read(response)
	if readErr != nil && !isSSHFabricExpectedHTTPProbeError(readErr) {
		t.Fatalf("read raw HTTP probe: %v", readErr)
	}
	if got := string(response[:n]); strings.HasPrefix(got, "HTTP/") || strings.Contains(got, "\r\nContent-") {
		t.Fatalf("broker exposed an HTTP sidechannel: %q", got)
	}
}

func startSSHFabricSecurityBroker(t *testing.T, serverSigner ssh.Signer, clientSigner ssh.Signer, allowTarget func(string) error) (net.Listener, *SSHFabricServer) {
	t.Helper()
	serverConfig := &ssh.ServerConfig{
		PublicKeyCallback: func(metadata ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if metadata.User() != "wt-client" || string(key.Marshal()) != string(clientSigner.PublicKey().Marshal()) {
				return nil, fmt.Errorf("unauthorised client")
			}
			return nil, nil
		},
	}
	serverConfig.AddHostKey(serverSigner)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	broker, err := NewSSHFabricServer(listener, SSHFabricServerConfig{SSHConfig: serverConfig, RetentionLimit: 8, AllowTarget: allowTarget})
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- broker.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = broker.Close()
		select {
		case err := <-serveDone:
			if err != nil {
				t.Errorf("serve SSH fabric broker: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("SSH fabric broker did not stop")
		}
	})
	return listener, broker
}

func isSSHFabricExpectedHTTPProbeError(err error) bool {
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		return true
	}
	return strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "connection reset")
}
