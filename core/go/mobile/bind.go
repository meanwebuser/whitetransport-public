package mobile

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/transport"
)

var (
	mu      sync.Mutex
	current *transport.Transport
	cancel  context.CancelFunc
)

func StartTransport(configJSON string) error {
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
