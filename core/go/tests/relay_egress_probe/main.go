// Command relay_egress_probe proves that admin-relay HTTP traffic traverses
// an injected real stream carrier rather than the local host network.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/carriers/adminrelay"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

type probeConfig struct {
	RelayURL    string
	ClientToken string
	NodeToken   string
	EnvelopeID  string
	Payload     []byte
}

type probeResult struct {
	Transport     string          `json:"transport"`
	EnvelopeID    string          `json:"envelopeId"`
	EnvelopeExact bool            `json:"envelopeExact"`
	Cursor        carriers.Cursor `json:"cursor"`
	Acknowledged  bool            `json:"acknowledged"`
	PayloadSHA256 string          `json:"payloadSha256"`
}

func runProbe(ctx context.Context, cfg probeConfig, dialContext func(context.Context, string, string) (net.Conn, error)) (probeResult, error) {
	if strings.TrimSpace(cfg.RelayURL) == "" || strings.TrimSpace(cfg.ClientToken) == "" || strings.TrimSpace(cfg.NodeToken) == "" || strings.TrimSpace(cfg.EnvelopeID) == "" {
		return probeResult{}, errors.New("relay URL, client token, node token, and envelope ID are required")
	}
	writer := adminrelay.NewWithDialContext(config.AdminRelayConfig{AdminURL: cfg.RelayURL, Token: cfg.ClientToken, Identity: "client"}, nil, dialContext)
	reader := adminrelay.NewWithDialContext(config.AdminRelayConfig{AdminURL: cfg.RelayURL, Token: cfg.NodeToken, Identity: "node"}, nil, dialContext)
	endpoint := carriers.Endpoint{ID: "control", Address: "control", Metadata: map[string]string{"channel": "control", "recipient": "node"}}
	want := fabric.NewEnvelope(cfg.EnvelopeID, fabric.TrafficControl, "session.release", cfg.Payload)
	if err := writer.Write(ctx, endpoint, want); err != nil {
		return probeResult{}, fmt.Errorf("POST relay envelope: %w", err)
	}
	read, err := reader.Read(ctx, endpoint, "")
	if err != nil {
		return probeResult{}, fmt.Errorf("GET relay envelope: %w", err)
	}
	if len(read.Envelopes) != 1 || !reflect.DeepEqual(read.Envelopes[0], want) {
		return probeResult{}, fmt.Errorf("relay envelope mismatch: got %#v want %#v", read.Envelopes, want)
	}
	if strings.TrimSpace(string(read.Cursor)) == "" {
		return probeResult{}, errors.New("relay GET returned empty cursor")
	}
	if err := reader.Ack(ctx, endpoint, read.Cursor); err != nil {
		return probeResult{}, fmt.Errorf("ACK relay cursor: %w", err)
	}
	hash := sha256.Sum256(cfg.Payload)
	return probeResult{
		Transport: "ssh.fabric", EnvelopeID: cfg.EnvelopeID, EnvelopeExact: true,
		Cursor: read.Cursor, Acknowledged: true, PayloadSHA256: hex.EncodeToString(hash[:]),
	}, nil
}

func main() {
	var relayURL, clientTokenFile, nodeTokenFile, serverAddress, username, privateKeyPath, hostKeyPath, envelopeID, payload string
	flag.StringVar(&relayURL, "relay-url", "", "remote relay base URL")
	flag.StringVar(&clientTokenFile, "client-token-file", "", "path to an ephemeral client relay token")
	flag.StringVar(&nodeTokenFile, "node-token-file", "", "path to an ephemeral node relay token")
	flag.StringVar(&serverAddress, "server-address", "", "ssh.fabric listener address")
	flag.StringVar(&username, "username", "", "ssh.fabric username")
	flag.StringVar(&privateKeyPath, "private-key", "", "ephemeral SSH client private key")
	flag.StringVar(&hostKeyPath, "host-key", "", "pinned SSH host public key")
	flag.StringVar(&envelopeID, "envelope-id", "", "unique envelope ID")
	flag.StringVar(&payload, "payload", "", "exact envelope payload")
	flag.Parse()

	clientTokenBytes, err := os.ReadFile(clientTokenFile)
	if err != nil {
		exitError(fmt.Errorf("read client token file: %w", err))
	}
	nodeTokenBytes, err := os.ReadFile(nodeTokenFile)
	if err != nil {
		exitError(fmt.Errorf("read node token file: %w", err))
	}
	hostKeyBytes, err := os.ReadFile(hostKeyPath)
	if err != nil {
		exitError(fmt.Errorf("read host key: %w", err))
	}
	sshCarrier, err := carriers.NewSSHFabricCarrier(carriers.SSHConfig{
		Username: username, PrivateKeyPath: privateKeyPath, HostKeys: []string{strings.TrimSpace(string(hostKeyBytes))}, ServerAliveIntervalSecs: 5,
	})
	if err != nil {
		exitError(fmt.Errorf("create ssh.fabric carrier: %w", err))
	}
	defer sshCarrier.Close()
	sshEndpoint := carriers.Endpoint{ID: "ssh.fabric", Carrier: carriers.CarrierSSHFabric, Address: serverAddress}
	dialContext := func(ctx context.Context, _ string, address string) (net.Conn, error) {
		return sshCarrier.DialStream(ctx, sshEndpoint, address)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := runProbe(ctx, probeConfig{
		RelayURL: relayURL, ClientToken: strings.TrimSpace(string(clientTokenBytes)), NodeToken: strings.TrimSpace(string(nodeTokenBytes)),
		EnvelopeID: envelopeID, Payload: []byte(payload),
	}, dialContext)
	if err != nil {
		exitError(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		exitError(fmt.Errorf("encode result: %w", err))
	}
}

func exitError(err error) {
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"exit": 1, "error": err.Error()})
	os.Exit(1)
}
