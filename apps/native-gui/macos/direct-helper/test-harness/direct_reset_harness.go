package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	testNetTarget       = "198.51.100.77:18080"
	testNetCIDR         = "198.51.100.77/32"
	tlsProbePayloadSize = 64 * 1024
)

// BinaryHash identifies the exact executable used by a root acceptance run.
type BinaryHash struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// CleanupState is deliberately explicit: a diagnostic result must never
// imply that a route or utun disappeared merely because stop was requested.
type CleanupState struct {
	HelperStopped bool `json:"helperStopped"`
	RouteRemoved  bool `json:"routeRemoved"`
	UtunRemoved   bool `json:"utunRemoved"`
	TempRemoved   bool `json:"tempRemoved"`
}

// HarnessResult is the machine-readable evidence emitted by this diagnostic.
// It is not a product-readiness or release result.
type HarnessResult struct {
	Test                      string       `json:"test"`
	ProofLevel                string       `json:"proofLevel"`
	Status                    string       `json:"status"`
	Exit                      int          `json:"exit"`
	Reason                    string       `json:"reason,omitempty"`
	TargetAddress             string       `json:"targetAddress"`
	TargetCIDR                string       `json:"targetCidr"`
	OrdinaryProxyFreePayload  bool         `json:"ordinaryProxyFreePayload"`
	ProductionCredentialsUsed bool         `json:"productionCredentialsUsed"`
	InternetRequired          bool         `json:"internetRequired"`
	BinaryHashes              []BinaryHash `json:"binaryHashes"`
	CreatedUtun               string       `json:"createdUtun,omitempty"`
	RouteDecision             string       `json:"routeDecision,omitempty"`
	RouteOutput               string       `json:"routeOutput,omitempty"`
	SocksGreetingObserved     bool         `json:"socksGreetingObserved"`
	SocksConnectObserved      bool         `json:"socksConnectObserved"`
	SocksRequestedATYP        string       `json:"socksRequestedAtyp,omitempty"`
	SocksRequestedTarget      string       `json:"socksRequestedTarget,omitempty"`
	SocksResponseCode         int          `json:"socksResponseCode"`
	PayloadNonce              string       `json:"payloadNonce,omitempty"`
	PayloadNonceResult        string       `json:"payloadNonceResult,omitempty"`
	PayloadNonceValid         bool         `json:"payloadNonceValid"`
	PayloadBytes              int          `json:"payloadBytes"`
	PayloadSHA256             string       `json:"payloadSha256,omitempty"`
	PayloadExpectedSHA256     string       `json:"payloadExpectedSha256,omitempty"`
	PayloadHashValid          bool         `json:"payloadHashValid"`
	PayloadProtocol           string       `json:"payloadProtocol"`
	TLSProbe                  bool         `json:"tlsProbe"`
	Cleanup                   CleanupState `json:"cleanup"`
	PhaseErrors               []string     `json:"phaseErrors,omitempty"`
	HelperStart               string       `json:"helperStart,omitempty"`
	HelperStop                string       `json:"helperStop,omitempty"`
	ConfigPath                string       `json:"configPath,omitempty"`
	LocalMapping              string       `json:"localMapping,omitempty"`
}

// Options controls a run. AcceptMacOS is intentionally opt-in because only
// root acceptance may create a utun or mutate a route table.
type Options struct {
	AcceptMacOS bool
	HelperPath  string
	Tun2Socks   string
	CurlPath    string
	Timeout     time.Duration
	TLSProbe    bool
}

type helperResult struct {
	OK      bool   `json:"ok"`
	Command string `json:"command"`
	Error   string `json:"error,omitempty"`
	State   *struct {
		PID       int    `json:"pid"`
		Interface string `json:"interface"`
		Routes    []struct {
			CIDR string `json:"cidr"`
			Via  string `json:"via"`
		} `json:"routes"`
	} `json:"state,omitempty"`
}

type socksTrace struct {
	mu               sync.Mutex
	greetingObserved bool
	connectObserved  bool
	requestedATYP    string
	requestedTarget  string
	responseCode     int
}

type payloadEvidence struct {
	Bytes          int
	SHA256         string
	ExpectedSHA256 string
	HashValid      bool
}

func (t *socksTrace) snapshot() (greeting, connect bool, atyp, target string, response int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.greetingObserved, t.connectObserved, t.requestedATYP, t.requestedTarget, t.responseCode
}

type instrumentedSOCKS struct {
	listener net.Listener
	mapping  string
	trace    socksTrace
	wg       sync.WaitGroup
	stopOnce sync.Once
}

func startInstrumentedSOCKS(mapping string) (*instrumentedSOCKS, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen loopback SOCKS5: %w", err)
	}
	s := &instrumentedSOCKS{listener: listener, mapping: mapping}
	s.wg.Add(1)
	go s.acceptLoop()
	return s, nil
}

func (s *instrumentedSOCKS) Addr() string { return s.listener.Addr().String() }

func (s *instrumentedSOCKS) Close() {
	s.stopOnce.Do(func() {
		_ = s.listener.Close()
		s.wg.Wait()
	})
}

func (s *instrumentedSOCKS) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(conn)
		}()
	}
}

func (s *instrumentedSOCKS) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	var greeting [2]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil || greeting[0] != 0x05 {
		return
	}
	methods := make([]byte, int(greeting[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return
	}
	s.trace.mu.Lock()
	s.trace.greetingObserved = true
	s.trace.mu.Unlock()
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return
	}

	var head [4]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil || head[0] != 0x05 || head[1] != 0x01 {
		return
	}
	target, atyp, err := readSOCKSTarget(conn, head[3])
	if err != nil {
		return
	}
	s.trace.mu.Lock()
	s.trace.connectObserved = true
	s.trace.requestedATYP = atyp
	s.trace.requestedTarget = target
	s.trace.mu.Unlock()
	if target != testNetTarget || atyp != "ipv4" {
		_ = writeSOCKSReply(conn, 0x02)
		s.recordResponse(0x02)
		return
	}
	upstream, err := net.DialTimeout("tcp", s.mapping, 5*time.Second)
	if err != nil {
		_ = writeSOCKSReply(conn, 0x05)
		s.recordResponse(0x05)
		return
	}
	defer upstream.Close()
	if err := writeSOCKSReply(conn, 0x00); err != nil {
		s.recordResponse(0x01)
		return
	}
	s.recordResponse(0x00)
	proxyBidirectional(conn, upstream)
}

func (s *instrumentedSOCKS) recordResponse(code byte) {
	s.trace.mu.Lock()
	s.trace.responseCode = int(code)
	s.trace.mu.Unlock()
}

func readSOCKSTarget(reader io.Reader, atyp byte) (string, string, error) {
	switch atyp {
	case 0x01:
		var raw [6]byte
		if _, err := io.ReadFull(reader, raw[:]); err != nil {
			return "", "", err
		}
		port := binary.BigEndian.Uint16(raw[4:])
		return net.JoinHostPort(net.IP(raw[:4]).String(), fmt.Sprint(port)), "ipv4", nil
	case 0x03:
		var size [1]byte
		if _, err := io.ReadFull(reader, size[:]); err != nil {
			return "", "", err
		}
		host := make([]byte, int(size[0])+2)
		if _, err := io.ReadFull(reader, host); err != nil {
			return "", "", err
		}
		return net.JoinHostPort(string(host[:len(host)-2]), fmt.Sprint(binary.BigEndian.Uint16(host[len(host)-2:]))), "domain", nil
	case 0x04:
		var raw [18]byte
		if _, err := io.ReadFull(reader, raw[:]); err != nil {
			return "", "", err
		}
		return net.JoinHostPort(net.IP(raw[:16]).String(), fmt.Sprint(binary.BigEndian.Uint16(raw[16:]))), "ipv6", nil
	default:
		return "", "", fmt.Errorf("unsupported SOCKS5 ATYP 0x%02x", atyp)
	}
}

func writeSOCKSReply(writer io.Writer, code byte) error {
	reply := []byte{0x05, code, 0x00, 0x01}
	reply = append(reply, net.ParseIP("127.0.0.1").To4()...)
	reply = append(reply, 0x00, 0x00)
	return writeAll(writer, reply)
}

func proxyBidirectional(left, right net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(right, left); closeWrite(right); done <- struct{}{} }()
	go func() { _, _ = io.Copy(left, right); closeWrite(left); done <- struct{}{} }()
	<-done
	<-done
	_ = left.Close()
	_ = right.Close()
}

func closeWrite(conn net.Conn) {
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.CloseWrite()
	}
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

func startNonceHTTP(nonce string) (address string, closeServer func(), err error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, fmt.Errorf("listen nonce HTTP server: %w", err)
	}
	server := &http.Server{Handler: nonceHandler(nonce)}
	go func() { _ = server.Serve(listener) }()
	return listener.Addr().String(), func() { _ = server.Close() }, nil
}

// startNonceHTTPS provides the same deterministic payload over TLS so the
// root-only harness can distinguish a plaintext-only pass from a usable HTTPS
// dataplane without contacting a provider or the public internet.
func startNonceHTTPS(nonce string) (address string, closeServer func(), err error) {
	server := httptest.NewTLSServer(nonceHandler(string(tlsProbePayload(nonce))))
	return server.Listener.Addr().String(), server.Close, nil
}

func tlsProbePayload(nonce string) []byte {
	payload := make([]byte, tlsProbePayloadSize)
	seed := []byte(nonce)
	for offset := 0; offset < len(payload); offset += len(seed) {
		copy(payload[offset:], seed)
	}
	return payload
}

func summarizePayload(actual, expected []byte) payloadEvidence {
	actualDigest := sha256.Sum256(actual)
	expectedDigest := sha256.Sum256(expected)
	return payloadEvidence{
		Bytes:          len(actual),
		SHA256:         hex.EncodeToString(actualDigest[:]),
		ExpectedSHA256: hex.EncodeToString(expectedDigest[:]),
		HashValid:      actualDigest == expectedDigest,
	}
}

func nonceHandler(nonce string) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/plain")
		writer.Header().Set("Content-Length", fmt.Sprint(len(nonce)))
		writer.Header().Set("Connection", "close")
		_, _ = io.WriteString(writer, nonce)
	})
}

func dialSOCKSAndReadHTTP(address, target string, request []byte, timeout time.Duration) ([]byte, error) {
	conn, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if err := writeAll(conn, []byte{0x05, 0x01, 0x00}); err != nil {
		return nil, err
	}
	var method [2]byte
	if _, err := io.ReadFull(conn, method[:]); err != nil {
		return nil, err
	}
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}
	port := 0
	if _, err := fmt.Sscan(portText, &port); err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid target port %q", portText)
	}
	ip := net.ParseIP(host).To4()
	if ip == nil {
		return nil, fmt.Errorf("self-test requires numeric IPv4 target, got %q", target)
	}
	connect := []byte{0x05, 0x01, 0x00, 0x01}
	connect = append(connect, ip...)
	var portBytes [2]byte
	binary.BigEndian.PutUint16(portBytes[:], uint16(port))
	connect = append(connect, portBytes[:]...)
	if err := writeAll(conn, connect); err != nil {
		return nil, err
	}
	var reply [10]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return nil, err
	}
	if reply[1] != 0x00 {
		return nil, fmt.Errorf("SOCKS CONNECT reply 0x%02x", reply[1])
	}
	if err := writeAll(conn, request); err != nil {
		return nil, err
	}
	closeWrite(conn)
	return io.ReadAll(conn)
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func hashBinary(path string) (BinaryHash, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return BinaryHash{}, err
	}
	digest, err := sha256File(absolute)
	if err != nil {
		return BinaryHash{}, fmt.Errorf("hash %s: %w", absolute, err)
	}
	return BinaryHash{Path: absolute, SHA256: digest}, nil
}

func commandOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func runHelper(ctx context.Context, helper, command, config string) (helperResult, string, error) {
	output, err := commandOutput(ctx, helper, command, "--config", config)
	text := strings.TrimSpace(string(output))
	var result helperResult
	if decodeErr := json.Unmarshal([]byte(text), &result); decodeErr != nil {
		if err != nil {
			return result, text, fmt.Errorf("direct-helper %s failed: %v (output %q)", command, err, text)
		}
		return result, text, fmt.Errorf("decode direct-helper %s response: %w", command, decodeErr)
	}
	if err != nil || !result.OK {
		if result.Error != "" {
			return result, text, errors.New(result.Error)
		}
		return result, text, fmt.Errorf("direct-helper %s exited with %v", command, err)
	}
	return result, text, nil
}

func listInterfaces(ctx context.Context) map[string]struct{} {
	output, err := commandOutput(ctx, "ifconfig", "-l")
	if err != nil {
		return map[string]struct{}{}
	}
	interfaces := make(map[string]struct{})
	for _, name := range strings.Fields(string(output)) {
		if strings.HasPrefix(name, "utun") {
			interfaces[name] = struct{}{}
		}
	}
	return interfaces
}

func findCreatedInterface(before map[string]struct{}, after map[string]struct{}) string {
	for name := range after {
		if _, exists := before[name]; !exists {
			return name
		}
	}
	return ""
}

func routeDecision(output, createdUtun string) string {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "interface:" {
			if createdUtun != "" && fields[1] == createdUtun {
				return "target routed via " + createdUtun
			}
			return "target routed via " + fields[1]
		}
	}
	return "route decision unavailable"
}

func Run(options Options) (result HarnessResult) {
	result = HarnessResult{
		Test:                      "macos-direct-utun-reset",
		ProofLevel:                "diagnostic-only",
		Status:                    "not-run",
		Exit:                      2,
		TargetAddress:             testNetTarget,
		TargetCIDR:                testNetCIDR,
		ProductionCredentialsUsed: false,
		InternetRequired:          false,
		BinaryHashes:              []BinaryHash{},
		Cleanup:                   CleanupState{},
		PayloadProtocol:           "http",
		TLSProbe:                  options.TLSProbe,
	}
	if !options.AcceptMacOS {
		result.Reason = "root acceptance is opt-in; rerun with -accept-macos on the target Mac"
		return result
	}
	if runtime.GOOS != "darwin" {
		result.Reason = "-accept-macos requires Darwin; no helper, route, or utun action was attempted"
		return result
	}
	if options.HelperPath == "" || options.Tun2Socks == "" {
		result.Status = "configuration-error"
		result.Exit = 2
		result.Reason = "-helper and -tun2socks must be absolute executable paths"
		return result
	}
	if options.CurlPath == "" {
		options.CurlPath = "curl"
	}
	if options.Timeout <= 0 {
		options.Timeout = 30 * time.Second
	}
	for _, path := range []string{options.HelperPath, options.Tun2Socks} {
		hash, err := hashBinary(path)
		if err != nil {
			result.Status = "configuration-error"
			result.Exit = 2
			result.Reason = err.Error()
			return result
		}
		result.BinaryHashes = append(result.BinaryHashes, hash)
	}
	if executable, err := os.Executable(); err == nil {
		if hash, hashErr := hashBinary(executable); hashErr == nil {
			result.BinaryHashes = append(result.BinaryHashes, hash)
		}
	}
	runDir, err := os.MkdirTemp("", "wt-direct-reset.")
	if err != nil {
		result.Status = "setup-failed"
		result.Exit = 1
		result.Reason = err.Error()
		return result
	}
	defer func() { result.Cleanup.TempRemoved = os.RemoveAll(runDir) == nil }()

	nonce := fmt.Sprintf("wt-direct-reset-%d", time.Now().UnixNano())
	expectedPayload := []byte(nonce)
	var localHTTP string
	var closeHTTP func()
	if options.TLSProbe {
		expectedPayload = tlsProbePayload(nonce)
		localHTTP, closeHTTP, err = startNonceHTTPS(nonce)
		result.PayloadProtocol = "https"
	} else {
		localHTTP, closeHTTP, err = startNonceHTTP(nonce)
	}
	if err != nil {
		result.Status, result.Exit, result.Reason = "setup-failed", 1, err.Error()
		return result
	}
	defer closeHTTP()
	httpHost, httpPort, _ := net.SplitHostPort(localHTTP)
	result.LocalMapping = testNetTarget + " => " + net.JoinHostPort(httpHost, httpPort)
	socks, err := startInstrumentedSOCKS(localHTTP)
	if err != nil {
		result.Status, result.Exit, result.Reason = "setup-failed", 1, err.Error()
		return result
	}
	defer socks.Close()
	configPath := filepath.Join(runDir, "direct-helper.json")
	result.ConfigPath = configPath
	config := map[string]any{
		"socks_host": "127.0.0.1", "socks_port": mustPort(socks.Addr()), "mode": "only",
		"only_cidrs": []string{testNetCIDR}, "tun2socks_path": options.Tun2Socks,
		"mtu": 1500, "state_path": filepath.Join(runDir, "state.json"),
		"log_path":           filepath.Join(runDir, "direct-helper.log"),
		"test_result_path":   filepath.Join(runDir, "test-result.json"),
		"daemon_instance_id": "macos-direct-reset-harness", "profile_revision": 1,
		"profile_hash": "macos-direct-reset-harness", "session_id": "diagnostic",
		"profile_valid_until": time.Now().Add(10 * time.Minute).UTC(),
	}
	configData, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		result.Status, result.Exit, result.Reason = "setup-failed", 1, err.Error()
		return result
	}
	if err := os.WriteFile(configPath, configData, 0o600); err != nil {
		result.Status, result.Exit, result.Reason = "setup-failed", 1, err.Error()
		return result
	}
	ctx, cancel := context.WithTimeout(context.Background(), options.Timeout)
	defer cancel()
	beforeUtun := listInterfaces(ctx)
	started, startText, err := runHelper(ctx, options.HelperPath, "start", configPath)
	result.HelperStart = startText
	if err != nil {
		result.Status, result.Exit, result.Reason = "helper-start-failed", 1, err.Error()
		return result
	}
	stopRequired := true
	defer func() {
		if !stopRequired {
			return
		}
		stopped, stopText, stopErr := runHelper(context.Background(), options.HelperPath, "stop", configPath)
		result.HelperStop = stopText
		result.Cleanup.HelperStopped = stopErr == nil && stopped.OK
		result.Cleanup.RouteRemoved = result.Cleanup.HelperStopped && !fileExists(filepath.Join(runDir, "state.json"))
		afterStop := listInterfaces(context.Background())
		result.Cleanup.UtunRemoved = result.CreatedUtun == "" || !hasInterface(afterStop, result.CreatedUtun)
		if stopErr != nil {
			result.PhaseErrors = append(result.PhaseErrors, "stop: "+stopErr.Error())
		}
	}()
	if started.State != nil {
		result.CreatedUtun = started.State.Interface
	}
	afterStart := listInterfaces(ctx)
	if result.CreatedUtun == "" {
		result.CreatedUtun = findCreatedInterface(beforeUtun, afterStart)
	}
	routeOutput, routeErr := commandOutput(ctx, "route", "-n", "get", "198.51.100.77")
	result.RouteOutput = strings.TrimSpace(string(routeOutput))
	result.RouteDecision = routeDecision(result.RouteOutput, result.CreatedUtun)
	if routeErr != nil {
		result.PhaseErrors = append(result.PhaseErrors, "route: "+routeErr.Error())
	}
	curlArgs := []string{"--noproxy", "*", "--connect-timeout", "10", "--max-time", "15", "--silent", "--show-error", "--fail"}
	if options.TLSProbe {
		curlArgs = append(curlArgs, "--insecure")
	}
	scheme := result.PayloadProtocol
	output, curlErr := commandOutputWithEnv(ctx, append(curlArgs, scheme+"://"+testNetTarget+"/nonce"), options.CurlPath)
	payload := summarizePayload(output, expectedPayload)
	result.OrdinaryProxyFreePayload = true
	result.PayloadNonce = nonce
	result.PayloadBytes = payload.Bytes
	result.PayloadSHA256 = payload.SHA256
	result.PayloadExpectedSHA256 = payload.ExpectedSHA256
	result.PayloadHashValid = payload.HashValid
	if payload.HashValid {
		result.PayloadNonceResult = nonce
	}
	result.PayloadNonceValid = result.PayloadNonceResult == nonce && result.PayloadHashValid
	if curlErr != nil {
		result.PhaseErrors = append(result.PhaseErrors, "curl: "+curlErr.Error())
	}
	greeting, connect, atyp, target, response := socks.trace.snapshot()
	result.SocksGreetingObserved, result.SocksConnectObserved = greeting, connect
	result.SocksRequestedATYP, result.SocksRequestedTarget, result.SocksResponseCode = atyp, target, response
	stopRequired = true
	result.Status = "diagnostic-complete"
	result.ProofLevel = "macos-root-acceptance-diagnostic"
	result.Exit = 0
	if !result.PayloadNonceValid || !result.SocksConnectObserved || result.SocksResponseCode != 0 || routeErr != nil {
		result.Status, result.Exit = "diagnostic-failed", 1
	}
	return result
}

func commandOutputWithEnv(ctx context.Context, args []string, name string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "ALL_PROXY=", "HTTP_PROXY=", "HTTPS_PROXY=", "http_proxy=", "https_proxy=", "all_proxy=", "NO_PROXY=*", "no_proxy=*")
	return cmd.CombinedOutput()
}

func mustPort(address string) int {
	_, portText, err := net.SplitHostPort(address)
	if err != nil {
		return 0
	}
	var port int
	_, _ = fmt.Sscan(portText, &port)
	return port
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func hasInterface(interfaces map[string]struct{}, name string) bool {
	_, ok := interfaces[name]
	return ok
}

func main() {
	accept := flag.Bool("accept-macos", false, "allow root-only direct-helper start/stop and route/utun mutation")
	helper := flag.String("helper", os.Getenv("WT_DIRECT_HELPER_BIN"), "absolute direct-helper executable")
	tun2socks := flag.String("tun2socks", os.Getenv("WT_TUN2SOCKS_BIN"), "absolute tun2socks executable")
	curlPath := flag.String("curl", os.Getenv("WT_CURL_BIN"), "curl executable")
	tlsProbe := flag.Bool("tls-probe", false, "run the payload probe over HTTPS through the direct utun")
	flag.Parse()
	result := Run(Options{AcceptMacOS: *accept, HelperPath: *helper, Tun2Socks: *tun2socks, CurlPath: *curlPath, TLSProbe: *tlsProbe})
	data, _ := json.Marshal(result)
	fmt.Println(string(data))
	os.Exit(result.Exit)
}
