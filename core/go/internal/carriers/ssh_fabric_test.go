package carriers

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"golang.org/x/crypto/ssh"
)

func TestSSHFabricCarriesRetainedControlAndDirectTCPIPOnOnePinnedSession(t *testing.T) {
	echoAddr, stopEcho := startSSHFabricEcho(t)
	defer stopEcho()

	serverSigner, _ := generateSSHFabricSigner(t)
	clientSigner, clientKey := generateSSHFabricSigner(t)
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
	broker, err := NewSSHFabricServer(listener, SSHFabricServerConfig{
		SSHConfig:      serverConfig,
		RetentionLimit: 32,
		AllowTarget: func(address string) error {
			if address != echoAddr {
				return fmt.Errorf("target denied")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveDone := make(chan error, 1)
	go func() { serveDone <- broker.Serve(ctx) }()
	defer func() {
		_ = broker.Close()
		<-serveDone
	}()

	carrier, err := NewSSHFabricCarrier(SSHConfig{
		Username:   "wt-client",
		PrivateKey: clientKey,
		HostKeys:   []string{string(ssh.MarshalAuthorizedKey(serverSigner.PublicKey()))},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer carrier.Close()

	endpoint := Endpoint{ID: "node-control", Carrier: CarrierSSHFabric, Address: listener.Addr().String()}
	envelope := fabric.NewEnvelope("advertise-1", fabric.TrafficControl, "node.advertise", []byte("node-88"))
	if err := carrier.Write(ctx, endpoint, envelope); err != nil {
		t.Fatalf("write control envelope: %v", err)
	}
	first, err := carrier.Read(ctx, endpoint, "")
	if err != nil {
		t.Fatalf("read retained control envelope: %v", err)
	}
	if len(first.Envelopes) != 1 || first.Envelopes[0].ID != envelope.ID {
		t.Fatalf("unexpected retained envelopes: %+v", first.Envelopes)
	}
	second, err := carrier.Read(ctx, endpoint, first.Cursor)
	if err != nil {
		t.Fatalf("read after cursor: %v", err)
	}
	if len(second.Envelopes) != 0 {
		t.Fatalf("cursor replayed envelopes: %+v", second.Envelopes)
	}

	stream, err := carrier.DialStream(ctx, endpoint, echoAddr)
	if err != nil {
		t.Fatalf("dial direct-tcpip: %v", err)
	}
	defer stream.Close()
	if _, err := stream.Write([]byte("remote-nonce")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len("remote-nonce"))
	if _, err := io.ReadFull(stream, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != "remote-nonce" {
		t.Fatalf("unexpected direct-tcpip payload %q", response)
	}
	if got := broker.ConnectionCount(); got != 1 {
		t.Fatalf("control and egress used %d SSH handshakes, want one persistent session", got)
	}
}

func TestSSHFabricRejectsMissingHostKeyPin(t *testing.T) {
	_, err := NewSSHFabricCarrier(SSHConfig{Username: "wt-client", PrivateKey: "unused"})
	if err == nil {
		t.Fatal("ssh.fabric must reject an unpinned server host key")
	}
}

func generateSSHFabricSigner(t *testing.T) (ssh.Signer, string) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyBlock, err := ssh.MarshalPrivateKey(privateKey, "wt-test")
	if err != nil {
		t.Fatal(err)
	}
	return signer, string(pem.EncodeToMemory(privateKeyBlock))
}

func startSSHFabricEcho(t *testing.T) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener.Addr().String(), func() {
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("echo server did not stop")
		}
	}
}
