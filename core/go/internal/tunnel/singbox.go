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
	Start(ctx context.Context, carrier carriers.ManagedStreamCarrier, endpoint carriers.Endpoint, localListen string) (SingBoxProcess, string, error)
}

var singBoxRunner SingBoxRunner = singBoxCommandRunner{}
var lookupSingBoxIP = net.DefaultResolver.LookupIP
var fallbackLookupSingBoxIPv4 = (&net.Resolver{
	PreferGo: true,
	Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, "1.1.1.1:53")
	},
}).LookupIP

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
	lifecycle                  sync.RWMutex // Close waits for all endpoint operations.
	mu                         sync.Mutex
	endpointLocks              map[string]*singBoxEndpointLock
	bindings                   map[string]policy.CarrierBinding
	staticBindings             map[string]policy.CarrierBinding
	canonicalSessionEndpointID string
	processes                  map[string]*singBoxProcessState
}

type singBoxEndpointLock struct {
	mu    sync.Mutex
	users int // holders and waiters, guarded by SingBoxTunnel.mu
}

type singBoxProcessState struct {
	endpoint    carriers.Endpoint
	process     SingBoxProcess
	localListen string
}

type singBoxProcessDone interface {
	Done() <-chan struct{}
}

func processExited(process SingBoxProcess) bool {
	done, ok := process.(singBoxProcessDone)
	if !ok {
		return false
	}
	select {
	case <-done.Done():
		return true
	default:
		return false
	}
}

func NewSingBoxTunnel(bindings map[string]policy.CarrierBinding) *SingBoxTunnel {
	singBoxBindings := make(map[string]policy.CarrierBinding)
	staticBindings := make(map[string]policy.CarrierBinding)
	for id, binding := range bindings {
		if _, managed := binding.Carrier.(carriers.ManagedStreamCarrier); managed {
			singBoxBindings[id] = binding
			staticBindings[id] = binding
		}
	}
	return &SingBoxTunnel{
		endpointLocks:  make(map[string]*singBoxEndpointLock),
		bindings:       singBoxBindings,
		staticBindings: staticBindings,
		processes:      make(map[string]*singBoxProcessState),
	}
}

func (t *SingBoxTunnel) SupportsEndpoint(endpoint carriers.Endpoint) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.bindings[endpoint.Carrier].Carrier.(carriers.ManagedStreamCarrier)
	return ok
}

// lockEndpoint serializes startup and cleanup for one endpoint across profile
// rotations. The registry mutex stays available while a child starts or exits.
func (t *SingBoxTunnel) lockEndpoint(endpointID string) func() {
	t.lifecycle.RLock()
	t.mu.Lock()
	lock := t.endpointLocks[endpointID]
	if lock == nil {
		lock = &singBoxEndpointLock{}
		t.endpointLocks[endpointID] = lock
	}
	lock.users++
	t.mu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		t.mu.Lock()
		lock.users--
		if lock.users == 0 {
			delete(t.endpointLocks, endpointID)
		}
		t.mu.Unlock()
		t.lifecycle.RUnlock()
	}
}

// SetSessionBinding adds a runtime-resolved endpoint-to-binding mapping so the
// SingBoxTunnel can dial endpoints from session answers where the binding key
// differs from the carrier name (e.g. auto-discovered "xray-de-httpupgrade").
func (t *SingBoxTunnel) SetSessionBinding(endpoint carriers.Endpoint, binding policy.CarrierBinding) {
	if _, managed := binding.Carrier.(carriers.ManagedStreamCarrier); !managed {
		return
	}
	unlock := t.lockEndpoint(endpoint.ID)
	defer unlock()

	t.mu.Lock()
	stale := t.processes[endpoint.ID]
	delete(t.processes, endpoint.ID)
	t.bindings[binding.Carrier.Descriptor().ID] = binding
	t.bindings[endpoint.Carrier] = binding
	t.canonicalSessionEndpointID = endpoint.ID
	t.mu.Unlock()
	if stale != nil {
		// Release the old listener and configuration before a replacement can start.
		_ = stale.process.Close()
	}
}

// ClearSessionBinding removes a dynamic profile and stops its sidecar. This
// prevents a reconnect from retaining a previous session's server address or
// credential under the same endpoint ID.
func (t *SingBoxTunnel) ClearSessionBinding(endpoint carriers.Endpoint) {
	unlock := t.lockEndpoint(endpoint.ID)
	defer unlock()

	t.mu.Lock()
	state := t.processes[endpoint.ID]
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
	if state != nil {
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
	t.lifecycle.Lock()
	defer t.lifecycle.Unlock()

	t.mu.Lock()
	processes := t.processes
	t.processes = make(map[string]*singBoxProcessState)
	t.mu.Unlock()

	var closeErr error
	for key, state := range processes {
		if err := state.process.Close(); err != nil && closeErr == nil {
			closeErr = fmt.Errorf("sing-box tunnel: close %s: %w", key, err)
		}
	}
	return closeErr
}

func (t *SingBoxTunnel) ensureProcess(ctx context.Context, endpoint carriers.Endpoint) (*singBoxProcessState, error) {
	unlock := t.lockEndpoint(endpoint.ID)
	defer unlock()
	return t.ensureProcessLocked(ctx, endpoint)
}

// ensureProcessLocked requires the endpoint lock for both reuse and startup.
func (t *SingBoxTunnel) ensureProcessLocked(ctx context.Context, endpoint carriers.Endpoint) (*singBoxProcessState, error) {
	t.mu.Lock()
	stale := t.processes[endpoint.ID]
	if stale != nil && !processExited(stale.process) {
		t.mu.Unlock()
		return stale, nil
	}
	delete(t.processes, endpoint.ID)
	binding, ok := t.bindings[endpoint.Carrier]
	t.mu.Unlock()
	if stale != nil {
		_ = stale.process.Close()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("sing-box tunnel: no binding for %s", endpoint.Carrier)
	}
	carrier, ok := binding.Carrier.(carriers.ManagedStreamCarrier)
	if !ok {
		return nil, fmt.Errorf("sing-box tunnel: binding %s is not a managed stream carrier", endpoint.Carrier)
	}
	spec := carrier.SidecarSpec()
	localListen := strings.TrimSpace(spec.LocalListen)
	if localListen == "" {
		localListen = defaultSingBoxLocalListen
	}
	process, resolvedListen, err := singBoxRunner.Start(ctx, carrier, endpoint, localListen)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = process.Close()
		return nil, err
	}
	state := &singBoxProcessState{endpoint: endpoint, process: process, localListen: resolvedListen}
	t.mu.Lock()
	t.processes[endpoint.ID] = state
	carrier.SetStreamDialFunc(func(dialCtx context.Context, _ carriers.Endpoint, targetAddr string) (net.Conn, error) {
		return socks5Connect(dialCtx, resolvedListen, targetAddr)
	})
	t.mu.Unlock()
	return state, nil
}

// Reconcile replaces managed sidecars that exited while the transport was
// idle. Consumer-triggered dialing remains a second recovery path, but it is
// no longer required to notice a dead child.
func (t *SingBoxTunnel) Reconcile(ctx context.Context) error {
	t.mu.Lock()
	var dead []*singBoxProcessState
	for _, state := range t.processes {
		if processExited(state.process) {
			dead = append(dead, state)
		}
	}
	t.mu.Unlock()

	var firstErr error
	for _, state := range dead {
		if err := t.reconcileProcess(ctx, state); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("reconcile sing-box %s: %w", state.endpoint.ID, err)
		}
	}
	return firstErr
}

func (t *SingBoxTunnel) reconcileProcess(ctx context.Context, exited *singBoxProcessState) error {
	endpoint := exited.endpoint
	unlock := t.lockEndpoint(endpoint.ID)
	defer unlock()

	t.mu.Lock()
	// A dial, profile rotation or Close may have replaced this generation
	// since Reconcile took its snapshot.
	if t.processes[endpoint.ID] != exited {
		t.mu.Unlock()
		return nil
	}
	_, bound := t.bindings[endpoint.Carrier]
	if !bound {
		delete(t.processes, endpoint.ID)
	}
	t.mu.Unlock()
	if !bound {
		_ = exited.process.Close()
		return nil
	}
	_, err := t.ensureProcessLocked(ctx, endpoint)
	return err
}

type singBoxCommandRunner struct{}

func (singBoxCommandRunner) Start(ctx context.Context, carrier carriers.ManagedStreamCarrier, endpoint carriers.Endpoint, localListen string) (SingBoxProcess, string, error) {
	spec := carrier.SidecarSpec()
	resolvedServer, err := resolveSingBoxServer(ctx, spec.Server)
	if err != nil {
		return nil, "", err
	}
	// sing-box may inherit an unusable loopback resolver on macOS. Resolve the
	// socket address in the parent while retaining the original TLS SNI and
	// HTTPUpgrade Host fields in the immutable carrier configuration.
	listenHost, _, err := carriers.SplitHostPort(localListen)
	if err != nil {
		return nil, "", err
	}
	if listenHost == "localhost" || listenHost == "" {
		listenHost = "127.0.0.1"
	}
	if ip := net.ParseIP(listenHost); ip == nil || !ip.IsLoopback() {
		return nil, "", fmt.Errorf("sing-box tunnel: managed inbound must use a loopback IP")
	}
	// The child owns allocation. A configured port is only a legacy preference,
	// never an address at which a foreign listener can satisfy readiness.
	data, err := spec.ConfigJSON(net.JoinHostPort(listenHost, "0"), resolvedServer)
	if err != nil {
		return nil, "", err
	}
	configDir := strings.TrimSpace(spec.ConfigDir)
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

	command := exec.Command(strings.TrimSpace(spec.BinaryPath), "run", "-c", configPath)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, "", fmt.Errorf("sing-box tunnel: stdout pipe: %w", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return nil, "", fmt.Errorf("sing-box tunnel: stderr pipe: %w", err)
	}
	if err := command.Start(); err != nil {
		return nil, "", fmt.Errorf("sing-box tunnel: start %s: %w", spec.BinaryPath, err)
	}
	process := &singBoxCommandProcess{command: command, configPath: configPath, configDir: configDir, diagnostics: &singBoxDiagnosticTail{}, done: make(chan struct{})}
	ready := make(chan string, 1)
	var readers sync.WaitGroup
	readers.Add(2)
	for _, stream := range []io.Reader{stdout, stderr} {
		go func(reader io.Reader) {
			defer readers.Done()
			drainSingBoxLog(reader, process.diagnostics, ready)
		}(stream)
	}
	go func() {
		// Drain the final diagnostic before Wait closes the pipe handles.
		readers.Wait()
		_ = command.Wait()
		close(process.done)
	}()

	timeout := defaultSingBoxStartTimeout
	if spec.StartTimeoutSecs > 0 {
		timeout = time.Duration(spec.StartTimeoutSecs) * time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var resolvedListen string
	select {
	case resolvedListen = <-ready:
		host, _, splitErr := net.SplitHostPort(resolvedListen)
		if splitErr != nil || !net.ParseIP(host).Equal(net.ParseIP(listenHost)) {
			_ = process.Close()
			return nil, "", withSingBoxDiagnostics(fmt.Errorf("sing-box tunnel: child announced unexpected inbound"), process)
		}
	case <-process.done:
		_ = process.Close()
		return nil, "", withSingBoxDiagnostics(fmt.Errorf("sing-box tunnel: child exited before inbound startup"), process)
	case <-waitCtx.Done():
		_ = process.Close()
		return nil, "", withSingBoxDiagnostics(fmt.Errorf("sing-box tunnel: inbound startup: %w", waitCtx.Err()), process)
	}
	if err := waitForTCP(waitCtx, resolvedListen); err != nil {
		_ = process.Close()
		return nil, "", withSingBoxDiagnostics(err, process)
	}
	select {
	case <-process.done:
		_ = process.Close()
		return nil, "", withSingBoxDiagnostics(fmt.Errorf("sing-box tunnel: child exited during inbound startup"), process)
	default:
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
	addresses, ipv4Err := lookupSingBoxIP(ctx, "ip4", host)
	for _, address := range addresses {
		if ipv4 := address.To4(); ipv4 != nil {
			return ipv4.String(), nil
		}
	}
	addresses, fallbackErr := fallbackLookupSingBoxIPv4(ctx, "ip4", host)
	for _, address := range addresses {
		if ipv4 := address.To4(); ipv4 != nil {
			return ipv4.String(), nil
		}
	}
	addresses, ipv6Err := lookupSingBoxIP(ctx, "ip6", host)
	for _, address := range addresses {
		if address != nil {
			return address.String(), nil
		}
	}
	if ipv4Err != nil {
		return "", fmt.Errorf("sing-box tunnel: resolve server %s IPv4: system=%v fallback=%v", host, ipv4Err, fallbackErr)
	}
	if ipv6Err != nil {
		return "", fmt.Errorf("sing-box tunnel: resolve server %s IPv6: %w", host, ipv6Err)
	}
	return "", fmt.Errorf("sing-box tunnel: resolve server %s: no IP addresses", host)
}

type singBoxCommandProcess struct {
	command     *exec.Cmd
	configPath  string
	configDir   string
	diagnostics *singBoxDiagnosticTail
	done        chan struct{}
	closeOnce   sync.Once
}

func (p *singBoxCommandProcess) Done() <-chan struct{} { return p.done }

func (p *singBoxCommandProcess) Close() error {
	p.closeOnce.Do(func() {
		if p.command != nil && p.command.Process != nil {
			_ = p.command.Process.Kill()
			<-p.done
		}
		_ = os.Remove(p.configPath)
		if strings.Contains(filepath.Base(p.configDir), "whitetransport-singbox-") {
			_ = os.RemoveAll(p.configDir)
		}
	})
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
	return carriers.BuildSingBoxConfigJSON(cfg, localListen)
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

var singBoxANSI = regexp.MustCompile(`\x1b\[[0-9;]*m`)
var singBoxInboundStarted = regexp.MustCompile(`^(?:[+-][0-9]{4} [0-9-]+ [0-9:]+ )?INFO(?:\[[0-9]+\])? inbound/mixed\[wt-mixed-in\]: tcp server started at (\S+)$`)

func announcedSingBoxInbound(line string) string {
	match := singBoxInboundStarted.FindStringSubmatch(singBoxANSI.ReplaceAllString(strings.TrimSpace(line), ""))
	if len(match) != 2 {
		return ""
	}
	host, port, err := net.SplitHostPort(match[1])
	number, numberErr := strconv.Atoi(port)
	ip := net.ParseIP(host)
	if err != nil || numberErr != nil || number < 1 || number > 65535 || ip == nil || !ip.IsLoopback() {
		return ""
	}
	return match[1]
}

func drainSingBoxLog(reader io.Reader, tail *singBoxDiagnosticTail, ready ...chan string) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		tail.Append(line)
		if len(ready) > 0 {
			if address := announcedSingBoxInbound(line); address != "" {
				select {
				case ready[0] <- address:
				default:
				}
			}
		}
	}
}
