package mobile

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/transport"
)

var (
	mu      sync.Mutex
	current *transport.Transport
	cancel  context.CancelFunc
)

// LocalSession is a short-lived provider session supplied by a platform-owned
// secure store. It is deliberately separate from TokenStore: callers pass it
// only into StartTransportWithLocalSession and it is never serialized or
// persisted by the Go runtime.
type LocalSession struct {
	Platform     string `json:"platform"`
	AccessToken  string `json:"access_token"`
	CookieHeader string `json:"cookie_header"`
}

func StartTransport(configJSON string) error {
	return startTransport(configJSON, nil)
}

// StartTransportWithLocalSession starts a client runtime with one local
// WBStream session kept in process memory. Platform adapters must decrypt the
// session from their own secure store immediately before this call.
func StartTransportWithLocalSession(configJSON, localSessionJSON string) error {
	var session LocalSession
	if err := json.Unmarshal([]byte(localSessionJSON), &session); err != nil {
		return fmt.Errorf("parse local session: %w", err)
	}
	return startTransport(configJSON, &session)
}

func startTransport(configJSON string, session *LocalSession) error {
	mu.Lock()
	defer mu.Unlock()

	if current != nil {
		current.Stop()
		if cancel != nil {
			cancel()
		}
		current = nil
		cancel = nil
	}

	var cfg config.Config
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return err
	}
	applyMobileRuntimePaths(&cfg)
	if session != nil {
		if err := applyLocalSession(&cfg, *session); err != nil {
			return err
		}
	}

	ts := transport.BuildTokenStore(cfg)
	ctx, c := context.WithCancel(context.Background())
	cancel = c

	tp, err := transport.Start(ctx, cfg, ts)
	if err != nil {
		c()
		return err
	}
	current = tp
	return nil
}

func applyMobileRuntimePaths(cfg *config.Config) {
	if cfg == nil || strings.TrimSpace(cfg.StateFile) == "" {
		return
	}
	runtimeDir := filepath.Dir(cfg.StateFile)
	if cfg.SessionEgress.SingBox == nil {
		cfg.SessionEgress.SingBox = &config.SessionSingBoxRuntimeConfig{}
	}
	if strings.TrimSpace(cfg.SessionEgress.SingBox.ConfigDir) == "" {
		cfg.SessionEgress.SingBox.ConfigDir = filepath.Join(runtimeDir, "sing-box")
	}
}

func applyLocalSession(cfg *config.Config, session LocalSession) error {
	if cfg == nil {
		return fmt.Errorf("local session requires runtime config")
	}
	if cfg.Role != config.RoleClient {
		return fmt.Errorf("local session is supported only for client role")
	}
	if platform := strings.ToLower(strings.TrimSpace(session.Platform)); platform != "wbstream" {
		return fmt.Errorf("local session platform %q is unsupported", session.Platform)
	}
	accessToken := strings.TrimSpace(session.AccessToken)
	cookieHeader := strings.TrimSpace(session.CookieHeader)
	if accessToken == "" || cookieHeader == "" {
		return fmt.Errorf("local WBStream session requires access token and cookie header")
	}

	matched := false
	for index := range cfg.CarrierConfigs {
		carrier := &cfg.CarrierConfigs[index]
		if !isWBStreamCarrier(*carrier) {
			continue
		}
		if carrier.WBStream == nil {
			carrier.WBStream = &config.WBStreamConfig{}
		}
		carrier.WBStream.AccessToken = accessToken
		carrier.WBStream.CookieHeader = cookieHeader
		matched = true
	}
	if !matched {
		return fmt.Errorf("local WBStream session requires a configured WBStream carrier")
	}
	cfg.ClientRoomCreation = true
	return nil
}

func isWBStreamCarrier(carrier config.CarrierConfig) bool {
	if carrier.WBStream != nil {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(carrier.CarrierType), "wbstream") {
		return true
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(carrier.ID)), "wbstream")
}

func StopTransport() {
	mu.Lock()
	defer mu.Unlock()
	if current != nil {
		current.Stop()
		current = nil
	}
	if cancel != nil {
		cancel()
		cancel = nil
	}
}

func ConnectTransport(nodeID string) error {
	mu.Lock()
	tp := current
	mu.Unlock()
	if tp == nil {
		return errNotStarted
	}
	_, err := tp.Connect(context.Background(), nodeID)
	return err
}

func DisconnectTransport() {
	mu.Lock()
	tp := current
	mu.Unlock()
	if tp != nil {
		tp.Disconnect()
	}
}

// SelectEgressEndpoint pins the active session to one endpoint for a bounded
// diagnostic. Normal application sessions do not call this method and retain
// adaptive failover across every negotiated endpoint.
func SelectEgressEndpoint(endpointID string) (string, error) {
	mu.Lock()
	tp := current
	mu.Unlock()
	if tp == nil {
		return "", errNotStarted
	}
	status, err := tp.SelectEgressEndpoint(endpointID)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(status)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func GetSocksAddr() string {
	mu.Lock()
	tp := current
	mu.Unlock()
	if tp == nil {
		return ""
	}
	return tp.GetSocksAddr()
}

func ListNodes() string {
	mu.Lock()
	tp := current
	mu.Unlock()
	if tp == nil {
		return "[]"
	}
	nodes := tp.ListNodes()
	data, err := json.Marshal(nodes)
	if err != nil {
		return "[]"
	}
	return string(data)
}

func GetStatus() string {
	mu.Lock()
	tp := current
	mu.Unlock()
	if tp == nil {
		return `{"state":"stopped"}`
	}
	status := tp.Status()
	data, err := json.Marshal(status)
	if err != nil {
		return `{"state":"error"}`
	}
	return string(data)
}

func IsStarted() bool {
	mu.Lock()
	defer mu.Unlock()
	return current != nil && current.Started()
}

func GetHealth() string {
	mu.Lock()
	tp := current
	mu.Unlock()
	if tp == nil {
		return "{}"
	}
	snap := tp.CarrierHealthSnapshot()
	data, err := json.Marshal(snap)
	if err != nil {
		return "{}"
	}
	return string(data)
}

type LogCallback interface {
	OnLog(msg string)
}

func SetLogCallback(cb LogCallback) {
	_ = cb
}

type notStartedError struct{}

var errNotStarted = &notStartedError{}

func (e *notStartedError) Error() string { return "transport not started" }
