package tunnel

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func TestSSHTunnelDialsTargetThroughDirectTCPIP(t *testing.T) {
	echoAddr, stopEcho := startTCPEchoServer(t)
	defer stopEcho()

	sshAddr, hostKey, stopSSH := startDirectTCPIPSSHServer(t)
	defer stopSSH()

	carrier, err := carriers.NewSSHCarrier(carriers.SSHConfig{
		Username: "root",
		Password: "password",
		HostKeys: []string{hostKey},
	})
	if err != nil {
		t.Fatal(err)
	}
	bindings := map[string]policy.CarrierBinding{
		carriers.CarrierSSHTCP: {
			Carrier: carrier,
			Endpoint: carriers.Endpoint{
				ID:      "ssh-egress",
				Carrier: carriers.CarrierSSHTCP,
				Address: sshAddr,
			},
		},
	}
	tunnel := NewSSHTunnel(bindings)
	if tunnel == nil {
		t.Fatal("expected ssh tunnel")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := tunnel.DialContext(ctx, bindings[carriers.CarrierSSHTCP].Endpoint, echoAddr)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("expected ping echo, got %q", string(buf))
	}
}

func TestSSHTunnelDialsTargetUsingSSHAgent(t *testing.T) {
	echoAddr, stopEcho := startTCPEchoServer(t)
	defer stopEcho()

	clientKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	clientSigner, err := ssh.NewSignerFromKey(clientKey)
	if err != nil {
		t.Fatal(err)
	}
	agentSocket, stopAgent := startSSHAgent(t, clientKey)
	defer stopAgent()

	sshAddr, hostKey, stopSSH := startDirectTCPIPPublicKeySSHServer(t, clientSigner.PublicKey())
	defer stopSSH()

	carrier, err := carriers.NewSSHCarrier(carriers.SSHConfig{
		Username:        "root",
		UseAgent:        true,
		AgentSocketPath: agentSocket,
		HostKeys:        []string{hostKey},
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := carriers.Endpoint{ID: "ssh-egress", Carrier: carriers.CarrierSSHTCP, Address: sshAddr}
	tunnel := NewSSHTunnel(map[string]policy.CarrierBinding{
		carriers.CarrierSSHTCP: {Carrier: carrier, Endpoint: endpoint},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := tunnel.DialContext(ctx, endpoint, echoAddr)
	if err != nil {
		t.Fatalf("DialContext through SSH agent: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("agent")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len("agent"))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "agent" {
		t.Fatalf("echo = %q, want agent", string(buf))
	}
}

func TestRealSSHTunnelEgress(t *testing.T) {
	if os.Getenv("WT_SSH_REAL") != "1" {
		t.Skip("set WT_SSH_REAL=1 to test a real SSH exit node")
	}
	sshAddress := strings.TrimSpace(os.Getenv("WT_SSH_REAL_ADDRESS"))
	hostKey := strings.TrimSpace(os.Getenv("WT_SSH_REAL_HOST_KEY"))
	privateKeyPath := strings.TrimSpace(os.Getenv("WT_SSH_REAL_PRIVATE_KEY_PATH"))
	agentSocket := strings.TrimSpace(os.Getenv("WT_SSH_REAL_AGENT_SOCKET"))
	if sshAddress == "" || hostKey == "" {
		t.Fatal("WT_SSH_REAL_ADDRESS and WT_SSH_REAL_HOST_KEY are required")
	}
	if privateKeyPath == "" && agentSocket == "" {
		t.Fatal("WT_SSH_REAL_PRIVATE_KEY_PATH or WT_SSH_REAL_AGENT_SOCKET is required")
	}

	hostKeys := strings.FieldsFunc(hostKey, func(r rune) bool { return r == '\n' || r == '\r' })
	carrier, err := carriers.NewSSHCarrier(carriers.SSHConfig{
		Username:        strings.TrimSpace(os.Getenv("WT_SSH_REAL_USERNAME")),
		PrivateKeyPath:  privateKeyPath,
		UseAgent:        agentSocket != "",
		AgentSocketPath: agentSocket,
		HostKeys:        hostKeys,
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := carriers.Endpoint{ID: "real-ssh-agent", Carrier: carriers.CarrierSSHTCP, Address: sshAddress}
	tunnel := NewSSHTunnel(map[string]policy.CarrierBinding{
		carriers.CarrierSSHTCP: {Carrier: carrier, Endpoint: endpoint},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := tunnel.DialContext(ctx, endpoint, "api.ipify.org:443")
	if err != nil {
		t.Fatalf("dial api.ipify.org through SSH: %v", err)
	}
	defer conn.Close()

	tlsConn := tls.Client(conn, &tls.Config{ServerName: "api.ipify.org", MinVersion: tls.VersionTLS12})
	defer tlsConn.Close()
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		t.Fatalf("TLS through SSH: %v", err)
	}
	if _, err := fmt.Fprint(tlsConn, "GET / HTTP/1.1\r\nHost: api.ipify.org\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatalf("write request through SSH: %v", err)
	}
	response, err := http.ReadResponse(bufio.NewReader(tlsConn), &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("read response through SSH: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 128))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || net.ParseIP(strings.TrimSpace(string(body))) == nil {
		t.Fatalf("SSH exit response status=%d body=%q, want external IP", response.StatusCode, string(body))
	}
}

func startSSHAgent(t *testing.T, privateKey any) (string, func()) {
	t.Helper()
	// Darwin's sockaddr_un path is short enough that testing.T.TempDir can
	// exceed it before the socket basename is appended. Keep the fixture under
	// the stable short /tmp alias and clean it through the test lifecycle.
	dir, err := os.MkdirTemp("/tmp", "wt-ssh-agent-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "agent.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: privateKey}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go func() { _ = agent.ServeAgent(keyring, conn) }()
		}
	}()
	return socketPath, func() {
		_ = listener.Close()
		<-done
	}
}

func startTCPEchoServer(t *testing.T) (string, func()) {
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
		<-done
	}
}

func startDirectTCPIPSSHServer(t *testing.T) (string, string, func()) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if conn.User() == "root" && string(password) == "password" {
				return nil, nil
			}
			return nil, fmt.Errorf("rejected")
		},
	}
	config.AddHostKey(signer)

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
			go handleSSHConn(conn, config)
		}
	}()

	hostKey := string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	return listener.Addr().String(), hostKey, func() {
		_ = listener.Close()
		<-done
	}
}

func startDirectTCPIPPublicKeySSHServer(t *testing.T, authorizedKey ssh.PublicKey) (string, string, func()) {
	t.Helper()
	hostKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) == string(authorizedKey.Marshal()) {
				return nil, nil
			}
			return nil, fmt.Errorf("rejected")
		},
	}
	config.AddHostKey(hostSigner)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go handleSSHConn(conn, config)
		}
	}()
	return listener.Addr().String(), string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey())), func() {
		_ = listener.Close()
		<-done
	}
}

func handleSSHConn(raw net.Conn, config *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(raw, config)
	if err != nil {
		_ = raw.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	go func() {
		defer conn.Close()
		for newChannel := range chans {
			if newChannel.ChannelType() != "direct-tcpip" {
				_ = newChannel.Reject(ssh.UnknownChannelType, "unsupported channel")
				continue
			}
			var payload struct {
				DestAddr       string
				DestPort       uint32
				OriginatorAddr string
				OriginatorPort uint32
			}
			if err := ssh.Unmarshal(newChannel.ExtraData(), &payload); err != nil {
				_ = newChannel.Reject(ssh.ConnectionFailed, err.Error())
				continue
			}
			target, err := net.Dial("tcp", net.JoinHostPort(payload.DestAddr, fmt.Sprintf("%d", payload.DestPort)))
			if err != nil {
				_ = newChannel.Reject(ssh.ConnectionFailed, err.Error())
				continue
			}
			channel, requests, err := newChannel.Accept()
			if err != nil {
				_ = target.Close()
				continue
			}
			go ssh.DiscardRequests(requests)
			go proxySSHChannel(channel, target)
		}
	}()
}

func proxySSHChannel(channel ssh.Channel, target net.Conn) {
	defer channel.Close()
	defer target.Close()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(channel, target)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(target, channel)
		done <- struct{}{}
	}()
	<-done
}
