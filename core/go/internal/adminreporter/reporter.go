package adminreporter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/router"
	"github.com/meanwebuser/whitetransport/core/internal/runtime"
)

type StatusProvider interface {
	Status() runtime.StatusView
	CarrierHealthSnapshot() map[string]router.CarrierSnapshot
	GetSocksAddr() string
}

type Reporter struct {
	cfg       config.AdminReporterConfig
	node      config.Config
	status    StatusProvider
	client    *http.Client
	logf      func(format string, args ...any)
	version   string
	startedAt time.Time

	logMu     sync.Mutex
	logBuffer []logEntry
}

type payload struct {
	NodeID       string   `json:"nodeId,omitempty"`
	Name         string   `json:"name,omitempty"`
	IP           string   `json:"ip,omitempty"`
	APIHost      string   `json:"apiHost,omitempty"`
	APIPort      int      `json:"apiPort,omitempty"`
	SocksPort    int      `json:"socksPort,omitempty"`
	Status       string   `json:"status,omitempty"`
	Role         string   `json:"role,omitempty"`
	Country      string   `json:"country,omitempty"`
	CountryCode  string   `json:"countryCode,omitempty"`
	City         string   `json:"city,omitempty"`
	MaxClients   int      `json:"maxClients,omitempty"`
	Bandwidth    string   `json:"bandwidth,omitempty"`
	Ping         int      `json:"ping,omitempty"`
	Uptime       float64  `json:"uptime,omitempty"`
	HealthStatus string   `json:"healthStatus,omitempty"`
	LastError    string   `json:"lastError,omitempty"`
	Version      string   `json:"version,omitempty"`
	Carriers     []string `json:"carriers,omitempty"`
}

func Start(ctx context.Context, cfg config.AdminReporterConfig, nodeCfg config.Config, status StatusProvider, version string, logf func(format string, args ...any)) error {
	if !cfg.Enabled {
		return nil
	}
	if strings.TrimSpace(cfg.AdminURL) == "" {
		return fmt.Errorf("admin_reporter enabled but admin_url is empty")
	}
	reporter := &Reporter{
		cfg:       cfg,
		node:      nodeCfg,
		status:    status,
		client:    &http.Client{Timeout: 8 * time.Second},
		logf:      logf,
		version:   strings.TrimSpace(version),
		startedAt: time.Now(),
	}
	if reporter.logf == nil {
		reporter.logf = log.Printf
	}

	go reporter.loop(ctx)
	return nil
}

func (r *Reporter) loop(ctx context.Context) {
	if err := r.send(ctx, r.cfg.RegisterPath); err != nil {
		r.logf("adminreporter: initial register failed: %v", err)
	}

	interval := time.Duration(r.cfg.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 60 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.send(ctx, r.cfg.HeartbeatPath); err != nil {
				r.logf("adminreporter: heartbeat failed: %v", err)
			}
			if r.cfg.UploadLogs {
				r.uploadLogs(ctx)
			}
		}
	}
}

func (r *Reporter) send(ctx context.Context, path string) error {
	endpoint, err := r.resolveEndpoint(path)
	if err != nil {
		return err
	}
	body, err := json.Marshal(r.buildPayload())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	token := r.cfg.TokenValue()
	if r.node.TokenStore != nil {
		token = r.cfg.TokenValueFromStore(*r.node.TokenStore)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d from %s", resp.StatusCode, endpoint)
	}
	return nil
}

func (r *Reporter) resolveEndpoint(path string) (string, error) {
	base, err := url.Parse(strings.TrimRight(r.cfg.AdminURL, "/"))
	if err != nil {
		return "", fmt.Errorf("parse admin_url: %w", err)
	}
	rel, err := url.Parse(path)
	if err != nil {
		return "", fmt.Errorf("parse reporter path: %w", err)
	}
	return base.ResolveReference(rel).String(), nil
}

func (r *Reporter) buildPayload() payload {
	status := r.status.Status()
	health := r.status.CarrierHealthSnapshot()
	name := strings.TrimSpace(r.node.DisplayName)
	if name == "" {
		name = strings.TrimSpace(r.node.NodeID)
	}
	apiHost := strings.TrimSpace(r.cfg.APIHost)
	if apiHost == "" {
		apiHost = hostPart(r.node.ListenAPI)
	}
	apiPort := r.cfg.APIPort
	if apiPort == 0 {
		apiPort = portPart(r.node.ListenAPI, 17680)
	}
	socksPort := portPart(r.status.GetSocksAddr(), 0)
	ip := strings.TrimSpace(r.cfg.IP)
	if ip == "" {
		ip = apiHost
	}
	carrierIDs := make([]string, 0, len(health))
	for carrierID := range health {
		carrierIDs = append(carrierIDs, carrierID)
	}

	return payload{
		NodeID:       r.node.NodeID,
		Name:         name,
		IP:           ip,
		APIHost:      apiHost,
		APIPort:      apiPort,
		SocksPort:    socksPort,
		Status:       nodeStatusFromRuntime(status),
		Role:         string(r.node.Role),
		Country:      r.node.Country,
		CountryCode:  "",
		City:         r.node.Region,
		MaxClients:   10,
		Bandwidth:    "1Gbps",
		Ping:         0,
		Uptime:       time.Since(r.startedAt).Seconds(),
		HealthStatus: healthStatusFromRuntime(status),
		LastError:    status.LastError,
		Version:      r.nodeVersion(),
		Carriers:     carrierIDs,
	}
}

func (r *Reporter) nodeVersion() string {
	if strings.TrimSpace(r.cfg.NodeVersion) != "" {
		return strings.TrimSpace(r.cfg.NodeVersion)
	}
	if r.version != "" {
		return r.version
	}
	return "dev"
}

func nodeStatusFromRuntime(status runtime.StatusView) string {
	if status.State == "" {
		return "unknown"
	}
	if status.State == "connected" || status.State == "connecting" || status.State == "reconnecting" {
		return "online"
	}
	if status.State == "disconnected" {
		return "online"
	}
	return status.State
}

func healthStatusFromRuntime(status runtime.StatusView) string {
	if status.LastError != "" {
		return "degraded"
	}
	if status.State == "" {
		return "unknown"
	}
	return "healthy"
}

func hostPart(addr string) string {
	if addr == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err == nil {
		return host
	}
	if strings.Contains(addr, ":") {
		host, _, ok := strings.Cut(strings.TrimSpace(addr), ":")
		if ok {
			return host
		}
	}
	return strings.TrimSpace(addr)
}

func portPart(addr string, fallback int) int {
	if addr == "" {
		return fallback
	}
	_, port, err := strings.Cut(addr, ":")
	if !err {
		return fallback
	}
	parsed, parseErr := strconv.Atoi(strings.TrimSpace(port))
	if parseErr != nil {
		return fallback
	}
	return parsed
}

// logEntry is a single log record buffered for upload.
type logEntry struct {
	Level     string `json:"level"`
	Message   string `json:"message"`
	Component string `json:"component,omitempty"`
	Details   any    `json:"details,omitempty"`
	Timestamp string `json:"timestamp"`
}

// AddLog buffers a log entry for periodic upload to the admin panel.
func (r *Reporter) AddLog(level, message, component string, details any) {
	if !r.cfg.UploadLogs {
		return
	}
	r.logMu.Lock()
	defer r.logMu.Unlock()
	if len(r.logBuffer) >= 200 {
		// Cap buffer size to prevent unbounded growth
		r.logBuffer = r.logBuffer[100:]
	}
	r.logBuffer = append(r.logBuffer, logEntry{
		Level:     level,
		Message:   message,
		Component: component,
		Details:   details,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
}

func (r *Reporter) uploadLogs(ctx context.Context) {
	r.logMu.Lock()
	if len(r.logBuffer) == 0 {
		r.logMu.Unlock()
		return
	}
	logs := make([]logEntry, len(r.logBuffer))
	copy(logs, r.logBuffer)
	r.logBuffer = r.logBuffer[:0]
	r.logMu.Unlock()

	logsPath := r.cfg.LogsPath
	if logsPath == "" {
		logsPath = "/api/nodes/logs"
	}
	endpoint, err := r.resolveEndpoint(logsPath)
	if err != nil {
		r.logf("adminreporter: resolve logs path: %v", err)
		return
	}

	body, err := json.Marshal(map[string]any{
		"nodeId": r.node.NodeID,
		"logs":   logs,
	})
	if err != nil {
		r.logf("adminreporter: marshal logs: %v", err)
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		r.logf("adminreporter: create logs request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if token := r.cfg.TokenValue(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		r.logf("adminreporter: upload logs failed: %v", err)
		// Re-buffer logs for retry
		r.logMu.Lock()
		r.logBuffer = append(logs, r.logBuffer...)
		r.logMu.Unlock()
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		r.logf("adminreporter: upload logs status %d", resp.StatusCode)
	} else {
		r.logf("adminreporter: uploaded %d log entries", len(logs))
	}
}
