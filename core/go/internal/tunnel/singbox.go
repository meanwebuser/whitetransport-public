package tunnel

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
)

const (
	defaultSingBoxStartTimeout = 10 * time.Second
	defaultSingBoxLocalListen  = "127.0.0.1:0"
	maxSingBoxDiagnosticBytes  = 8192
)

var (
	singBoxVLESSURIPattern = regexp.MustCompile(`(?i)vless://[^\s"']+`)
	singBoxUUIDPattern     = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}\b`)
)

// SingBoxProcess is a managed local sing-box instance.
type SingBoxProcess interface {
	Close() error
}

// SingBoxRunner starts the local sing-box process for one carrier binding.
type SingBoxRunner interface {
	Start(ctx context.Context, carrier *carriers.SingBoxVLESSCarrier, endpoint carriers.Endpoint, localListen string) (SingBoxProcess, string, error)
}

var singBoxRunner SingBoxRunner = singBoxCommandRunner{}
var lookupSingBoxIP = net.DefaultResolver.LookupIPAddr

// SetSingBoxRunnerForTest overrides sing-box process startup in tests.
func SetSingBoxRunnerForTest(runner SingBoxRunner) func() {
	previous := singBoxRunner
	singBoxRunner = runner
	return func() {
		singBoxRunner = previous
	}
}

// SingBoxTunnel dials target TCP addresses through a local sing-box mixed
// inbound backed by a configured VLESS outbound.
type SingBoxTunnel struct {
	mu                         sync.Mutex
	bindings                   map[string]policy.CarrierBinding
	staticBindings             map[string]policy.CarrierBinding
	canonicalSessionEndpointID string
	processes                  map[string]singBoxProcessState
}

type singBoxProcessState struct {
	process     SingBoxProcess
	localListen string
}

func NewSingBoxTunnel(bindings map[string]policy.CarrierBinding) *SingBoxTunnel {
	singBoxBindings := make(map[string]policy.CarrierBinding)
	staticBindings := make(map[string]policy.CarrierBinding)
	for id, binding := range bindings {
		if binding.Carrier.Descriptor().ID == carriers.CarrierSingBoxVLESS {
			singBoxBindings[id] = binding
			staticBindings[id] = binding
		}
	}
	return &SingBoxTunnel{
		bindings:       singBoxBindings,
		staticBindings: staticBindings,
		processes:      make(map[string]singBoxProcessState),
	}
}

func (t *SingBoxTunnel) SupportsEndpoint(endpoint carriers.Endpoint) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if binding, ok := t.bindings[endpoint.Carrier]; ok {
		return binding.Carrier.Descriptor().ID == carriers.CarrierSingBoxVLESS
	}
	// The canonical ID is an alias for a configured Xray route. A remote
	// endpoint identity, however, must match an explicit session binding; do
	// not accept arbitrary xray-* values and let CompositeTunnel fall through.
	if endpoint.Carrier != carriers.CarrierSingBoxVLESS {
		return false
	}
	for key := range t.bindings {
		if strings.Contains(key, "xray") || strings.Contains(key, "singbox") {
			return true
		}
	}
	return false
}

// SetSessionBinding adds a runtime-resolved endpoint-to-binding mapping so the
// SingBoxTunnel can dial endpoints from session answers where the binding key
// differs from the carrier name (e.g. auto-discovered "xray-de-httpupgrade").
func (t *SingBoxTunnel) SetSessionBinding(endpoint carriers.Endpoint, binding policy.CarrierBinding) {
	if binding.Carrier.Descriptor().ID != carriers.CarrierSingBoxVLESS {
		return
	}
	t.mu.Lock()
	stale, hadStale := t.processes[endpoint.ID]
	delete(t.processes, endpoint.ID)
	t.bindings[endpoint.Carrier] = binding
	t.bindings[carriers.CarrierSingBoxVLESS] = binding
	t.canonicalSessionEndpointID = endpoint.ID
	t.mu.Unlock()
	if hadStale {
		// Session answers may rotate a profile while retaining its stable route
		// ID. A running sidecar owns the previous immutable config, so it must
		// be replaced rather than silently reused for the new binding.
		_ = stale.process.Close()
	}
}

// ClearSessionBinding removes a dynamic profile and stops its sidecar. This
// prevents a reconnect from retaining a previous session's server address or
// credential under the same endpoint ID.
func (t *SingBoxTunnel) ClearSessionBinding(endpoint carriers.Endpoint) {
	t.mu.Lock()
	state, hadProcess := t.processes[endpoint.ID]
	delete(t.processes, endpoint.ID)
	if staticBinding, configured := t.staticBindings[endpoint.Carrier]; configured {
		t.bindings[endpoint.Carrier] = staticBinding
	} else {
		delete(t.bindings, endpoint.Carrier)
	}
	if t.canonicalSessionEndpointID == endpoint.ID {
		if staticBinding, configured := t.staticBindings[carriers.CarrierSingBoxVLESS]; configured {
			t.bindings[carriers.CarrierSingBoxVLESS] = staticBinding
		} else {
			delete(t.bindings, carriers.CarrierSingBoxVLESS)
		}
		t.canonicalSessionEndpointID = ""
	}
	t.mu.Unlock()
	if hadProcess {
		_ = state.process.Close()
	}
}

func (t *SingBoxTunnel) DialContext(ctx context.Context, endpoint carriers.Endpoint, targetAddr string) (net.Conn, error) {
	state, err := t.ensureProcess(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	conn, err := socks5Connect(ctx, state.localListen, targetAddr)
	if err != nil {
		return nil, withSingBoxDiagnostics(fmt.Errorf("sing-box tunnel: socks connect %s via %s: %w", targetAddr, state.localListen, err), state.process)
	}
	return conn, nil
}

func (t *SingBoxTunnel) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	var closeErr error
	for key, state := range t.processes {
		if err := state.process.Close(); err != nil && closeErr == nil {
			closeErr = fmt.Errorf("sing-box tunnel: close %s: %w", key, err)
		}
		delete(t.processes, key)
	}
	return closeErr
}

func (t *SingBoxTunnel) ensureProcess(ctx context.Context, endpoint carriers.Endpoint) (singBoxProcessState, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if state, ok := t.processes[endpoint.ID]; ok {
		return state, nil
	}
	binding, ok := t.bindings[endpoint.Carrier]
	if !ok {
		return singBoxProcessState{}, fmt.Errorf("sing-box tunnel: no binding for %s", endpoint.Carrier)
	}
	carrier, ok := binding.Carrier.(*carriers.SingBoxVLESSCarrier)
	if !ok {
		return singBoxProcessState{}, fmt.Errorf("sing-box tunnel: binding %s is not SingBoxVLESSCarrier", endpoint.Carrier)
	}
	localListen := strings.TrimSpace(carrier.Config().LocalListen)
	if localListen == "" {
		localListen = defaultSingBoxLocalListen
	}
	process, resolvedListen, err := singBoxRunner.Start(ctx, carrier, endpoint, localListen)
	if err != nil {
		return singBoxProcessState{}, err
	}
	state := singBoxProcessState{process: process, localListen: resolvedListen}
	t.processes[endpoint.ID] = state
	return state, nil
}

type singBoxCommandRunner struct{}

func (singBoxCommandRunner) Start(ctx context.Context, carrier *carriers.SingBoxVLESSCarrier, endpoint carriers.Endpoint, localListen string) (SingBoxProcess, string, error) {
	cfg := carrier.Config()
	resolvedServer, err := resolveSingBoxServer(ctx, cfg.Server)
	if err != nil {
		return nil, "", err
	}
	// sing-box may inherit an unusable loopback resolver on macOS. Resolve the
	// socket address in the parent while retaining the original TLS SNI and
	// HTTPUpgrade Host fields in the immutable carrier configuration.
	cfg.Server = resolvedServer
	resolvedListen, err := reserveListenAddress(localListen)
	if err != nil {
		return nil, "", err
	}
	data, err := buildSingBoxConfigJSON(cfg, resolvedListen)
	if err != nil {
		return nil, "", err
	}
	configDir := strings.TrimSpace(cfg.ConfigDir)
	if configDir == "" {
		configDir, err = os.MkdirTemp("", "whitetransport-singbox-*")
		if err != nil {
			return nil, "", fmt.Errorf("sing-box tunnel: create temp config dir: %w", err)
		}
	} else if err := os.MkdirAll(configDir, 0700); err != nil {
		return nil, "", fmt.Errorf("sing-box tunnel: create config dir: %w", err)
	}
	configPath := filepath.Join(configDir, endpoint.ID+".json")
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		return nil, "", fmt.Errorf("sing-box tunnel: write config: %w", err)
	}

	command := exec.Command(strings.TrimSpace(cfg.BinaryPath), "run", "-c", configPath)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, "", fmt.Errorf("sing-box tunnel: stdout pipe: %w", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return nil, "", fmt.Errorf("sing-box tunnel: stderr pipe: %w", err)
	}
	if err := command.Start(); err != nil {
		return nil, "", fmt.Errorf("sing-box tunnel: start %s: %w", cfg.BinaryPath, err)
	}
	process := &singBoxCommandProcess{command: command, configPath: configPath, configDir: configDir, diagnostics: &singBoxDiagnosticTail{}}
	go drainSingBoxLog(stdout, process.diagnostics)
	go drainSingBoxLog(stderr, process.diagnostics)

	timeout := defaultSingBoxStartTimeout
	if cfg.StartTimeoutSecs > 0 {
		timeout = time.Duration(cfg.StartTimeoutSecs) * time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := waitForTCP(waitCtx, resolvedListen); err != nil {
		_ = process.Close()
		return nil, "", withSingBoxDiagnostics(err, process)
	}
	return process, resolvedListen, nil
}

func resolveSingBoxServer(ctx context.Context, server string) (string, error) {
	host := strings.TrimSpace(server)
	if host == "" {
		return "", fmt.Errorf("sing-box tunnel: server is required")
	}
	if parsed := net.ParseIP(host); parsed != nil {
		return parsed.String(), nil
	}
	addresses, err := lookupSingBoxIP(ctx, host)
	if err != nil {
		return "", fmt.Errorf("sing-box tunnel: resolve server %s: %w", host, err)
	}
	for _, address := range addresses {
		if ipv4 := address.IP.To4(); ipv4 != nil {
			return ipv4.String(), nil
		}
	}
	for _, address := range addresses {
		if address.IP != nil {
			return address.IP.String(), nil
		}
	}
	return "", fmt.Errorf("sing-box tunnel: resolve server %s: no IP addresses", host)
}

type singBoxCommandProcess struct {
	command     *exec.Cmd
	configPath  string
	configDir   string
	diagnostics *singBoxDiagnosticTail
}

func (p *singBoxCommandProcess) Close() error {
	if p.command != nil && p.command.Process != nil {
		_ = p.command.Process.Kill()
		_ = p.command.Wait()
	}
	_ = os.Remove(p.configPath)
	if strings.Contains(filepath.Base(p.configDir), "whitetransport-singbox-") {
		_ = os.RemoveAll(p.configDir)
	}
	return nil
}

// DiagnosticTail returns bounded, credential-redacted sidecar output for a
// failed runtime operation. It never returns the generated config itself.
func (p *singBoxCommandProcess) DiagnosticTail() string {
	if p == nil || p.diagnostics == nil {
		return ""
	}
	return p.diagnostics.String()
}

type singBoxDiagnosticTailProvider interface {
	DiagnosticTail() string
}

func withSingBoxDiagnostics(err error, process SingBoxProcess) error {
	if err == nil {
		return nil
	}
	provider, ok := process.(singBoxDiagnosticTailProvider)
	if !ok {
		return err
	}
	tail := redactSingBoxDiagnostic(provider.DiagnosticTail())
	if tail == "" {
		return err
	}
	return fmt.Errorf("%w; sing-box diagnostics: %s", err, tail)
}

type singBoxDiagnosticTail struct {
	mu   sync.Mutex
	text string
}

func (t *singBoxDiagnosticTail) Append(line string) {
	if t == nil {
		return
	}
	safe := redactSingBoxDiagnostic(line)
	if safe == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.text != "" {
		t.text += "\n"
	}
	t.text += safe
	if len(t.text) > maxSingBoxDiagnosticBytes {
		t.text = t.text[len(t.text)-maxSingBoxDiagnosticBytes:]
	}
}

func (t *singBoxDiagnosticTail) String() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return redactSingBoxDiagnostic(t.text)
}

func redactSingBoxDiagnostic(value string) string {
	redacted := singBoxVLESSURIPattern.ReplaceAllString(value, "vless://[REDACTED]")
	return singBoxUUIDPattern.ReplaceAllString(redacted, "[REDACTED-UUID]")
}

func buildSingBoxConfigJSON(cfg carriers.SingBoxVLESSConfig, localListen string) ([]byte, error) {
	listenHost, listenPort, err := carriers.SplitHostPort(localListen)
	if err != nil {
		return nil, fmt.Errorf("sing-box tunnel: parse local listen %q: %w", localListen, err)
	}
	outbound := map[string]any{
		"type":        "vless",
		"tag":         "proxy",
		"server":      cfg.Server,
		"server_port": cfg.ServerPort,
		"uuid":        cfg.UUID,
		"network":     cfg.Network,
	}
	if cfg.Flow != "" {
		outbound["flow"] = cfg.Flow
	}
	if cfg.TLSEnabled {
		tlsConfig := map[string]any{
			"enabled":     true,
			"server_name": cfg.TLSServerName,
			"insecure":    cfg.TLSInsecure,
		}
		if cfg.UTLSFingerprint != "" {
			tlsConfig["utls"] = map[string]any{
				"enabled":     true,
				"fingerprint": cfg.UTLSFingerprint,
			}
		}
		if cfg.RealityEnabled {
			tlsConfig["reality"] = map[string]any{"enabled": true, "public_key": cfg.RealityPublicKey, "short_id": cfg.RealityShortID}
		}
		outbound["tls"] = tlsConfig
	}
	// Xray labels a Reality route's base network as "tcp". In sing-box, TCP
	// is represented by omitting transport entirely; emitting {type:"tcp"}
	// makes the sidecar reject the generated profile at startup.
	if cfg.TransportType != "" && !strings.EqualFold(cfg.TransportType, "tcp") {
		transport := map[string]any{"type": cfg.TransportType}
		if cfg.TransportHost != "" && !strings.EqualFold(cfg.TransportType, "grpc") {
			// sing-box transports carry the HTTP/WebSocket Host override in
			// headers; the Xray-style top-level host field is rejected by the
			// sing-box config parser.
			transport["headers"] = map[string]string{"Host": cfg.TransportHost}
		}
		if cfg.TransportPath != "" {
			if strings.EqualFold(cfg.TransportType, "grpc") {
				transport["service_name"] = cfg.TransportPath
			} else {
				transport["path"] = cfg.TransportPath
			}
		}
		outbound["transport"] = transport
	}
	doc := map[string]any{
		"log": map[string]any{"level": "warn", "timestamp": true},
		"inbounds": []map[string]any{{
			"type":        "mixed",
			"tag":         "wt-mixed-in",
			"listen":      listenHost,
			"listen_port": listenPort,
		}},
		"outbounds": []map[string]any{
			outbound,
		},
		"route": map[string]any{"final": "proxy"},
	}
	return json.MarshalIndent(doc, "", "  ")
}

// BuildVLESSRoutingConfig generates a sing-box VLESS config with routing rules.
// When mode is "ru_direct", RU traffic goes direct and rest goes through VLESS.
func BuildVLESSRoutingConfig(cfg carriers.SingBoxVLESSConfig, localListen, mode, geoIPURL, geoSiteURL string) ([]byte, error) {
	base, err := buildSingBoxConfigJSON(cfg, localListen)
	if err != nil {
		return nil, err
	}
	if mode == "" || mode == "all_proxy" {
		return base, nil
	}
	var doc map[string]any
	if err := json.Unmarshal(base, &doc); err != nil {
		return nil, err
	}
	outbounds, _ := doc["outbounds"].([]map[string]any)
	outbounds = append(outbounds, map[string]any{"type": "direct", "tag": "direct"})
	doc["outbounds"] = outbounds
	doc["route"] = buildRouteRules(mode, geoIPURL, geoSiteURL)
	return json.MarshalIndent(doc, "", "  ")
}

func buildRouteRules(mode, geoIPURL, geoSiteURL string) map[string]any {
	if mode == "" || mode == "all_proxy" {
		return map[string]any{"final": "proxy"}
	}
	if mode == "all_direct" {
		return map[string]any{"final": "direct"}
	}
	if geoIPURL == "" {
		geoIPURL = "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-ru.srs"
	}
	if geoSiteURL == "" {
		geoSiteURL = "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-ru.srs"
	}
	return map[string]any{
		"rule_set": []map[string]any{
			{"tag": "geoip-ru", "type": "remote", "format": "binary", "url": geoIPURL, "download_detour": "direct"},
			{"tag": "geosite-ru", "type": "remote", "format": "binary", "url": geoSiteURL, "download_detour": "direct"},
		},
		"rules": []map[string]any{
			{
				"rule_set": []string{"geosite-category-ads-all"},
				"outbound": "direct",
			},
			{
				"rule_set": []string{"geoip-ru", "geosite-ru"},
				"outbound": "direct",
			},
			{
				"protocol": "dns",
				"outbound": "dns-out",
			},
		},
		"final": "proxy",
	}
}

func reserveListenAddress(requested string) (string, error) {
	host, port, err := carriers.SplitHostPort(requested)
	if err != nil {
		return "", fmt.Errorf("sing-box tunnel: parse local listen: %w", err)
	}
	if port != 0 {
		return requested, nil
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return "", fmt.Errorf("sing-box tunnel: reserve local listen: %w", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return "", fmt.Errorf("sing-box tunnel: close reserved listener: %w", err)
	}
	return addr, nil
}

func waitForTCP(ctx context.Context, addr string) error {
	dialer := net.Dialer{Timeout: 200 * time.Millisecond}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("sing-box tunnel: local inbound %s did not start: %w", addr, ctx.Err())
		case <-ticker.C:
		}
	}
}

func socks5Connect(ctx context.Context, proxyAddr string, targetAddr string) (net.Conn, error) {
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(conn, greeting); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if greeting[0] != 0x05 || greeting[1] != 0x00 {
		_ = conn.Close()
		return nil, fmt.Errorf("unexpected socks5 greeting response %v", greeting)
	}
	request, err := socks5ConnectRequest(targetAddr)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if _, err := conn.Write(request); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := readSocks5ConnectResponse(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func socks5ConnectRequest(targetAddr string) ([]byte, error) {
	host, portString, err := net.SplitHostPort(targetAddr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		return nil, err
	}
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("invalid socks5 target port %d", port)
	}
	request := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(host); ip != nil {
		if ipv4 := ip.To4(); ipv4 != nil {
			request = append(request, 0x01)
			request = append(request, ipv4...)
		} else {
			request = append(request, 0x04)
			request = append(request, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return nil, fmt.Errorf("socks5 target host is too long")
		}
		request = append(request, 0x03, byte(len(host)))
		request = append(request, []byte(host)...)
	}
	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, uint16(port))
	request = append(request, portBytes...)
	return request, nil
}

func readSocks5ConnectResponse(reader io.Reader) error {
	buffered := bufio.NewReader(reader)
	header := make([]byte, 4)
	if _, err := io.ReadFull(buffered, header); err != nil {
		return err
	}
	if header[0] != 0x05 {
		return fmt.Errorf("unexpected socks5 version %d", header[0])
	}
	if header[1] != 0x00 {
		return fmt.Errorf("socks5 connect failed with status %d", header[1])
	}
	switch header[3] {
	case 0x01:
		_, err := io.ReadFull(buffered, make([]byte, 4+2))
		return err
	case 0x03:
		length, err := buffered.ReadByte()
		if err != nil {
			return err
		}
		_, err = io.ReadFull(buffered, make([]byte, int(length)+2))
		return err
	case 0x04:
		_, err := io.ReadFull(buffered, make([]byte, 16+2))
		return err
	default:
		return fmt.Errorf("unsupported socks5 bind address type %d", header[3])
	}
}

func drainSingBoxLog(reader io.Reader, tail *singBoxDiagnosticTail) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		tail.Append(scanner.Text())
	}
}
