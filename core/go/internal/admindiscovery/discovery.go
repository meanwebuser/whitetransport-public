// Package admindiscovery implements simple HTTP-based node discovery by
// polling the admin panel's GET /api/discovery/nodes endpoint. Clients use
// this as an alternative (or complement) to scanning VK/OK bootstrap carriers.
package admindiscovery

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/session"
)

// NodeInfo is the JSON shape returned by /api/discovery/nodes.
type NodeInfo struct {
	NodeID       string `json:"node_id"`
	Name         string `json:"name"`
	Label        string `json:"label"`
	IP           string `json:"ip"`
	APIHost      string `json:"api_host"`
	APIPort      int    `json:"api_port"`
	SocksPort    *int   `json:"socks_port"`
	Status       string `json:"status"`
	Role         string `json:"role"`
	Country      string `json:"country"`
	CountryCode  string `json:"country_code"`
	City         string `json:"city"`
	MaxClients   int    `json:"max_clients"`
	Bandwidth    string `json:"bandwidth"`
	Ping         int    `json:"ping"`
	Uptime       float64 `json:"uptime"`
	HealthStatus string `json:"health_status"`
	Version      string `json:"version"`
	Available    bool   `json:"available"`
	LastSeenAt   string `json:"last_seen_at"`
}

type discoveryResponse struct {
	OK          bool       `json:"ok"`
	Nodes       []NodeInfo `json:"nodes"`
	Total       int        `json:"total"`
	GeneratedAt string     `json:"generatedAt"`
}

// NodeSink is called when admin discovery fetches updated node list.
// The ControlPlane implements this to inject discovered nodes.
type NodeSink func(nodes []NodeInfo)

// Poller periodically fetches the node list from the admin panel and
// calls the sink with fresh results.
type Poller struct {
	cfg      config.AdminDiscoveryConfig
	sink     NodeSink
	client   *http.Client
	interval time.Duration
	logf     func(format string, args ...any)
}

// Start begins the polling loop in a background goroutine.
// Returns nil immediately if discovery is disabled.
func Start(ctx context.Context, cfg config.AdminDiscoveryConfig, sink NodeSink, logf func(format string, args ...any)) error {
	if !cfg.Enabled {
		return nil
	}
	if strings.TrimSpace(cfg.AdminURL) == "" {
		return fmt.Errorf("admin_discovery enabled but admin_url is empty")
	}
	if logf == nil {
		logf = log.Printf
	}
	interval := time.Duration(cfg.PollIntervalSec) * time.Second
	if interval <= 0 {
		interval = 30 * time.Second
	}

	p := &Poller{
		cfg:      cfg,
		sink:     sink,
		client:   &http.Client{Timeout: 10 * time.Second},
		interval: interval,
		logf:     logf,
	}
	go p.loop(ctx)
	return nil
}

func (p *Poller) loop(ctx context.Context) {
	// Immediate first fetch
	if nodes, err := p.fetch(ctx); err != nil {
		p.logf("admindiscovery: initial fetch failed: %v", err)
	} else if len(nodes) > 0 {
		p.sink(nodes)
		p.logf("admindiscovery: initial fetch got %d nodes", len(nodes))
	}

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			nodes, err := p.fetch(ctx)
			if err != nil {
				p.logf("admindiscovery: fetch failed: %v", err)
				continue
			}
			if len(nodes) > 0 {
				p.sink(nodes)
			}
		}
	}
}

func (p *Poller) fetch(ctx context.Context) ([]NodeInfo, error) {
	u, err := url.Parse(strings.TrimRight(p.cfg.AdminURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("parse admin_url: %w", err)
	}
	u.Path = "/api/discovery/nodes"

	q := u.Query()
	statusFilter := strings.TrimSpace(p.cfg.StatusFilter)
	if statusFilter == "" {
		statusFilter = "online"
	}
	q.Set("status", statusFilter)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if token := p.cfg.TokenValue(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d from %s", resp.StatusCode, u.String())
	}

	var result discoveryResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if !result.OK {
		return nil, fmt.Errorf("admin returned ok=false")
	}
	return result.Nodes, nil
}

// ToAdvertisement converts a NodeInfo into a session.NodeAdvertisement so
// the ControlPlane can store it alongside carrier-discovered nodes.
func (n NodeInfo) ToAdvertisement() session.NodeAdvertisement {
	nodeID := n.NodeID
	if nodeID == "" {
		nodeID = n.Name
	}
	label := n.Label
	if label == "" {
		label = n.Name
	}
	caps := []string{"egress", "control"}
	return session.NodeAdvertisement{
		NodeID:       nodeID,
		Role:         session.RoleNode,
		Label:        label,
		Country:      n.Country,
		Region:       n.City,
		Capabilities: caps,
		// Carriers endpoints are not available via admin discovery;
		// session must be established through admin relay or direct API.
	}
}
