package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/admin"
	"github.com/meanwebuser/whitetransport/core/internal/adminreporter"
	"github.com/meanwebuser/whitetransport/core/internal/api"
	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/chatdiscovery"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/keys"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/provider"
	"github.com/meanwebuser/whitetransport/core/internal/providers"
	"github.com/meanwebuser/whitetransport/core/internal/runtime"
	"github.com/meanwebuser/whitetransport/core/internal/tokens"
	"github.com/meanwebuser/whitetransport/core/internal/transport"
)

// Build-time metadata injected via -ldflags.
var (
	buildVersion = "dev"
	buildCommit  = "unknown"
	buildDate    = "unknown"
)

func main() {
	// Enable debug logging by default
	os.Setenv("WT_DEBUG", "1")

	// Subcommand dispatch
	if len(os.Args) > 1 && os.Args[1] == "migrate-tokens" {
		if err := migrateTokensCmd(); err != nil {
			fmt.Fprintf(os.Stderr, "migrate-tokens: %v\n", err)
			os.Exit(1)
		}
		return
	}

	configPath := flag.String("config", "", "path to whitetransportd JSON config")
	instanceName := flag.String("instance", "", "instance name for log prefix (multi-daemon isolation)")
	stateFileOverride := flag.String("state-file", "", "override state_file path from config (multi-daemon isolation)")
	printPlan := flag.Bool("plan", false, "print route plan JSON for one traffic class")
	serveRuntime := flag.Bool("serve", false, "serve planner API and local SOCKS5 proxy when configured")
	dispatchPayload := flag.Bool("dispatch", false, "dispatch one guarded runtime payload")
	confirmDispatch := flag.Bool("dispatch-confirm-write", false, "confirm live provider write for --dispatch")
	dispatchID := flag.String("dispatch-id", "", "optional explicit dispatch id")
	payloadType := flag.String("payload-type", "manual.smoke", "dispatch payload type")
	payloadString := flag.String("payload-string", "", "dispatch payload string")
	payloadBase64 := flag.String("payload-base64", "", "dispatch payload base64")
	payloadFile := flag.String("payload-file", "", "dispatch payload file")
	trafficClass := flag.String("traffic", string(fabric.TrafficControl), "traffic class for --plan or --dispatch")
	payloadBytes := flag.Int("payload-bytes", 0, "payload bytes for --plan")
	printVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *printVersion {
		fmt.Printf("whitetransportd %s commit=%s date=%s\n", buildVersion, buildCommit, buildDate)
		return
	}

	resolvedConfigPath, configDiag, err := resolveConfigPath(*configPath)
	if err != nil {
		writeConfigDiagnostics(os.Stderr, configDiag)
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}
	if os.Getenv("WT_DEBUG") != "0" {
		writeConfigDiagnostics(os.Stderr, configDiag)
	}

	cfg, err := config.Load(resolvedConfigPath)
	if err != nil {
		writeConfigDiagnostics(os.Stderr, configDiag)
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	// Multi-daemon isolation: apply --instance and --state-file overrides.
	if *instanceName != "" {
		log.SetPrefix("[" + *instanceName + "] ")
		log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	}
	if *stateFileOverride != "" {
		cfg.StateFile = *stateFileOverride
	}

	carrierDescriptors, err := cfg.CarrierDescriptors()
	if err != nil {
		fmt.Fprintf(os.Stderr, "load carrier descriptors: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf(
		"whitetransportd version=%s commit=%s date=%s role=%s node=%s socks=%s carriers=%d upstream=%s client_egress_only=%t\n",
		buildVersion,
		buildCommit,
		buildDate,
		cfg.Role,
		cfg.NodeID,
		cfg.SocksListen,
		len(carrierDescriptors),
		cfg.UpstreamProxy.URL,
		cfg.UpstreamProxy.ClientEgressOnly,
	)

	switch {
	case *dispatchPayload:
		if !*confirmDispatch {
			fmt.Fprintln(os.Stderr, "--dispatch requires --dispatch-confirm-write")
			os.Exit(2)
		}
		payload, err := loadDispatchPayload(*payloadString, *payloadBase64, *payloadFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load dispatch payload: %v\n", err)
			os.Exit(2)
		}
		id := *dispatchID
		if strings.TrimSpace(id) == "" {
			id = fmt.Sprintf("manual-%d", time.Now().Unix())
		}
		if err := dispatchRuntimePayload(cfg, id, *trafficClass, *payloadType, payload); err != nil {
			fmt.Fprintf(os.Stderr, "dispatch runtime payload: %v\n", err)
			os.Exit(1)
		}
	case *serveRuntime:
		tokenStore := transport.BuildTokenStore(cfg)
		if err := serveRuntimeEndpoints(cfg, carrierDescriptors, tokenStore); err != nil {
			fmt.Fprintf(os.Stderr, "serve runtime: %v\n", err)
			os.Exit(1)
		}
	case *printPlan:
		if err := printRoutePlan(*trafficClass, *payloadBytes, carrierDescriptors); err != nil {
			fmt.Fprintf(os.Stderr, "print route plan: %v\n", err)
			os.Exit(1)
		}
	}
}

type configCandidate struct {
	Source string
	Path   string
	Exists bool
}

type configDiagnostics struct {
	SelectedPath string
	Candidates   []configCandidate
}

func resolveConfigPath(explicitPath string) (string, configDiagnostics, error) {
	explicit := strings.TrimSpace(explicitPath)
	if explicit != "" {
		exists := fileExists(explicit)
		diag := configDiagnostics{
			SelectedPath: explicit,
			Candidates: []configCandidate{{
				Source: "--config",
				Path:   explicit,
				Exists: exists,
			}},
		}
		return explicit, diag, nil
	}

	candidates := defaultConfigCandidates()
	for _, candidate := range candidates {
		if candidate.Exists {
			return candidate.Path, configDiagnostics{
				SelectedPath: candidate.Path,
				Candidates:   candidates,
			}, nil
		}
	}

	return "", configDiagnostics{Candidates: candidates}, errors.New("no config path provided and no default config file found; pass --config /path/to/wt-config.json")
}

func defaultConfigCandidates() []configCandidate {
	paths := make([]configCandidate, 0, 16)
	add := func(source string, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		paths = append(paths, configCandidate{
			Source: source,
			Path:   value,
			Exists: fileExists(value),
		})
	}

	add("WT_CONFIG", os.Getenv("WT_CONFIG"))
	add("WT_CONFIG_PATH", os.Getenv("WT_CONFIG_PATH"))
	add("WHITETRANSPORTD_CONFIG", os.Getenv("WHITETRANSPORTD_CONFIG"))
	add("default", "/etc/whitetransport/config.json")
	add("default", "/etc/whitetransportd.json")
	add("default", "/opt/white-transport/config/whitetransportd.json")
	add("default", "/opt/white-transport/config-managed/config.json")

	if cwd, err := os.Getwd(); err == nil {
		add("cwd", filepath.Join(cwd, "whitetransportd.json"))
		add("cwd", filepath.Join(cwd, "config.json"))
	}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		add("binary-dir", filepath.Join(exeDir, "whitetransportd.json"))
		add("binary-dir", filepath.Join(exeDir, "..", "config", "whitetransportd.json"))
		add("binary-dir", filepath.Join(exeDir, "..", "config-managed", "config.json"))
	}

	return paths
}

func fileExists(path string) bool {
	stat, err := os.Stat(path)
	return err == nil && !stat.IsDir()
}

func writeConfigDiagnostics(output io.Writer, diag configDiagnostics) {
	if diag.SelectedPath != "" {
		fmt.Fprintf(output, "config selected: %s\n", diag.SelectedPath)
	} else {
		fmt.Fprintln(output, "config selected: (none)")
	}
	if len(diag.Candidates) == 0 {
		return
	}
	fmt.Fprintln(output, "config lookup candidates:")
	for _, candidate := range diag.Candidates {
		state := "missing"
		if candidate.Exists {
			state = "found"
		}
		fmt.Fprintf(output, "  - %s %s: %s\n", candidate.Source, state, candidate.Path)
	}
}

func printRoutePlan(trafficRaw string, payloadBytes int, carrierDescriptors []carriers.Descriptor) error {
	traffic := fabric.TrafficClass(trafficRaw)
	view, err := api.BuildPlanView(policy.DefaultAdaptivePolicy(), carrierDescriptors, traffic, payloadBytes)
	if err != nil {
		return err
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(view)
}

func dispatchRuntimePayload(cfg config.Config, id string, trafficRaw string, payloadType string, payload []byte) error {
	bindings, err := runtime.BuildCarrierBindings(cfg)
	if err != nil {
		return err
	}

	report, err := runtime.DispatchPayload(
		context.Background(),
		policy.DefaultAdaptivePolicy(),
		bindings,
		runtime.DispatchRequest{
			ID:          id,
			Traffic:     fabric.TrafficClass(trafficRaw),
			PayloadType: payloadType,
			Payload:     payload,
		},
	)
	if err != nil {
		return err
	}
	return writeDispatchSummary(os.Stdout, id, len(payload), report)
}

func writeDispatchSummary(output io.Writer, id string, payloadBytes int, report runtime.DispatchReport) error {
	summary := map[string]any{
		"id":             id,
		"payload_bytes":  payloadBytes,
		"strategy":       report.Plan.Strategy,
		"primary":        report.Plan.Primary.ID,
		"parallel":       descriptorIDs(report.Plan.Parallel),
		"repair":         descriptorIDs(report.Plan.Repair),
		"placements":     len(report.Placements),
		"pending_hedges": len(report.Result.PendingHedges),
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(summary)
}

func loadDispatchPayload(payloadString string, payloadBase64 string, payloadFile string) ([]byte, error) {
	sources := 0
	if payloadString != "" {
		sources++
	}
	if payloadBase64 != "" {
		sources++
	}
	if payloadFile != "" {
		sources++
	}
	if sources > 1 {
		return nil, errors.New("provide only one of --payload-string, --payload-base64, or --payload-file")
	}
	if payloadString != "" {
		return []byte(payloadString), nil
	}
	if payloadBase64 != "" {
		payload, err := base64.StdEncoding.DecodeString(payloadBase64)
		if err != nil {
			return nil, err
		}
		return payload, nil
	}
	if payloadFile != "" {
		return os.ReadFile(payloadFile)
	}
	return nil, errors.New("provide one of --payload-string, --payload-base64, or --payload-file")
}

func serveRuntimeEndpoints(cfg config.Config, carrierDescriptors []carriers.Descriptor, tokenStore *tokens.Store) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case sig := <-sigCh:
			log.Printf("caught signal %v, shutting down", sig)
			cancel()
		case <-ctx.Done():
		}
	}()

	tp, err := transport.Start(ctx, cfg, tokenStore)
	if err != nil {
		return err
	}
	defer tp.Stop()

	if err := adminreporter.Start(ctx, cfg.AdminReporter, cfg, tp, buildVersion, log.Printf); err != nil {
		return err
	}

	ps := providers.NewStore()
	ks := keys.NewStore()
	for _, cc := range cfg.CarrierConfigs {
		pType, pCat := providerTypeFromCarrierID(cc.ID)
		ps.Set(&providers.Model{
			ID:       cc.ID,
			Name:     cc.ID,
			Type:     pType,
			Category: pCat,
			Version:  "1.0.0",
		})
	}
	reg := runtime.NewProviderRegistry(ps, ks)

	configServer := admin.NewConfigServer(ps, ks)
	for _, cc := range cfg.CarrierConfigs {
		_ = configServer.SetProviderConfig(cc.ID, cc)
	}

	adminAPI := admin.NewAPI(ps, ks)
	for _, name := range reg.List() {
		if a, ok := reg.Get(name); ok {
			adminAPI.RegisterAdapter(name, a)
		}
	}

	var tokenServer *admin.TokenServer
	if tokenStore != nil {
		tokenServer = admin.NewTokenServer(tokenStore)
	}

	adaptivePolicy := policy.DefaultAdaptivePolicy()
	if tokenStore != nil {
		adaptivePolicy.TokenChecker = tokenStore
	}

	runtimeHandler := api.NewRuntimeHandlerWithBuildInfo(
		carrierDescriptors,
		adaptivePolicy,
		tp,
		log.Printf,
		api.BuildInfo{Version: buildVersion, Commit: buildCommit, Date: buildDate},
	)

	errCh := make(chan error, 1)
	mailbox := newCarrierMailbox()
	chatDiscoveryHandler := createChatDiscoveryHandler(cfg)

	go func() {
		errCh <- serveCombinedAPI(ctx, cfg.ListenAPI, runtimeHandler, adminAPI, configServer, tokenServer, mailbox, chatDiscoveryHandler)
	}()

	if cfg.SocksListen != "" {
		log.Printf("SOCKS5 proxy started by transport at %s", tp.GetSocksAddr())
	}

	err = <-errCh
	if err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, context.Canceled) && !isHTTPServerClosed(err) {
		return err
	}
	return nil
}

func serveCombinedAPI(ctx context.Context, listenAPI string, runtimeHandler http.Handler, adminAPI *admin.API, configServer *admin.ConfigServer, tokenServer *admin.TokenServer, mailbox *carrierMailbox, chatDiscovery http.Handler) error {
	if listenAPI == "" {
		listenAPI = "127.0.0.1:17680"
	}

	listener, err := net.Listen("tcp", listenAPI)
	if err != nil {
		return err
	}

	combined := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case path == "/v1/chats/discover":
			chatDiscovery.ServeHTTP(w, r)
		case strings.HasPrefix(path, "/carrier/"):
			if mailbox != nil {
				mailbox.handler().ServeHTTP(w, r)
			} else {
				writeJSONError(w, http.StatusNotFound, "carrier mailbox not configured")
			}
		case strings.HasPrefix(path, "/api/v1/config"):
			configServer.Handler().ServeHTTP(w, r)
		case strings.HasPrefix(path, "/api/v1/tokens") || strings.HasPrefix(path, "/api/v1/bindings"):
			if tokenServer != nil {
				tokenServer.Handler().ServeHTTP(w, r)
			} else {
				writeJSONError(w, http.StatusNotFound, "token store not configured")
			}
		case strings.HasPrefix(path, "/api/v1/"):
			adminAPI.Handler().ServeHTTP(w, r)
		default:
			runtimeHandler.ServeHTTP(w, r)
		}
	})

	server := &http.Server{
		Handler: combined,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	fmt.Printf("whitetransportd api listening on http://%s\n", listener.Addr().String())
	err = server.Serve(listener)
	if isHTTPServerClosed(err) {
		return nil
	}
	return err
}

func providerTypeFromCarrierID(id string) (provider.Type, provider.Category) {
	switch id {
	case carriers.CarrierVKMessages, carriers.CarrierOKMessages, carriers.CarrierFileMailbox:
		return provider.TypeMessaging, provider.CategorySocial
	case carriers.CarrierVKDocs256, carriers.CarrierVKDocs1024, carriers.CarrierVKPhotos:
		return provider.TypeFileTransfer, provider.CategoryCloud
	case carriers.CarrierOKDocs256, carriers.CarrierOKPhotos:
		return provider.TypeFileTransfer, provider.CategoryCloud
	case carriers.CarrierWBStreamVP8:
		return provider.TypeVideoCall, provider.CategoryVideo
	default:
		return provider.TypeMessaging, provider.CategoryOther
	}
}

func isHTTPServerClosed(err error) bool {
	return err != nil && errors.Is(err, http.ErrServerClosed)
}

func createChatDiscoveryHandler(cfg config.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vkTokens := make(map[string]string)
		okTokens := make(map[string]string)
		for _, cc := range cfg.CarrierConfigs {
			if cc.VKMessages != nil {
				token := cc.VKMessages.Token
				if token == "" && cc.VKMessages.TokenEnv != "" {
					token = os.Getenv(cc.VKMessages.TokenEnv)
				}
				if token != "" {
					vkTokens[cc.ID] = token
				}
			}
			if cc.OKMessages != nil {
				token := cc.OKMessages.Token
				if token == "" && cc.OKMessages.TokenEnv != "" {
					token = os.Getenv(cc.OKMessages.TokenEnv)
				}
				if token != "" {
					okTokens[cc.ID] = token
				}
			}
		}
		log.Printf("chat discovery: %d VK tokens, %d OK tokens", len(vkTokens), len(okTokens))
		for id, token := range vkTokens {
			preview := token
			if len(preview) > 20 {
				preview = preview[:20]
			}
			log.Printf("chat discovery: vk token %s len=%d prefix=%s", id, len(token), preview)
		}
		result, err := chatdiscovery.DiscoverAll(vkTokens, okTokens)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	})
}

func descriptorIDs(descriptors []carriers.Descriptor) []string {
	ids := make([]string, 0, len(descriptors))
	for _, descriptor := range descriptors {
		ids = append(ids, descriptor.ID)
	}
	return ids
}

// writeJSONError writes a JSON error response.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
