// Command browser-credential-bridge accepts one canonical browser credential
// export and stores only the parsed client-side credentials. It is a headless
// CLI bridge: stdin is the simplest path, and the optional loopback HTTP mode
// is the transport boundary a future MV3 extension can call.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/meanwebuser/whitetransport/apps/native-gui/internal/runtime"
)

const (
	capturePath        = "/browser-export"
	commandPath        = capturePath + "/command"
	maxBodySize        = 2 << 20
	defaultBridgePort  = 0
	defaultBridgeNonce = ""
)

type importResult struct {
	Platforms       []string `json:"platforms"`
	Count           int      `json:"count"`
	ConfigRefreshed bool     `json:"config_refreshed,omitempty"`
}

type importFunc func([]byte) (importResult, error)

func main() {
	port := flag.Int("port", defaultBridgePort, "loopback TCP port (0 chooses one)")
	nonce := flag.String("nonce", defaultBridgeNonce, "loopback bridge nonce (empty generates one)")
	stdin := flag.Bool("stdin", false, "read one canonical browser export from stdin and exit")
	keepAlive := flag.Bool("keep-alive", false, "keep the loopback HTTP bridge alive after successful imports")
	flag.Parse()
	if *stdin {
		if err := importFromStdin(); err != nil {
			fatal(err)
		}
		return
	}

	if *nonce == "" {
		generated, err := randomNonce()
		if err != nil {
			fatal(err)
		}
		*nonce = generated
	}

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		fatal(err)
	}
	defer listener.Close()

	done := make(chan error, 1)
	server := &http.Server{
		Handler:           newHandlerWithOptions(*nonce, importBrowserExport, done, *keepAlive, browserCommandFile()),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       5 * time.Second,
	}

	fmt.Printf("READY url=http://%s%s nonce=%s\n", listener.Addr(), capturePath, *nonce)
	go func() {
		err := server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			done <- err
		}
	}()

	if err := <-done; err != nil {
		fatal(err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
}

func importFromStdin() error {
	body, err := io.ReadAll(io.LimitReader(os.Stdin, maxBodySize+1))
	if err != nil {
		return fmt.Errorf("read browser export: %w", err)
	}
	if len(body) > maxBodySize {
		return errors.New("browser export is too large")
	}
	result, err := importBrowserExport(body)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func newHandler(nonce string, importExport importFunc, done chan<- error, keepAlive ...bool) http.Handler {
	persistent := len(keepAlive) > 0 && keepAlive[0]
	return newHandlerWithOptions(nonce, importExport, done, persistent, browserCommandFile())
}

func newHandlerWithOptions(nonce string, importExport importFunc, done chan<- error, persistent bool, commandFile string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != capturePath && r.URL.Path != commandPath {
			http.NotFound(w, r)
			return
		}
		if err := setBrowserCORSHeaders(w, r); err != nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.URL.Path == commandPath {
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", "GET, OPTIONS")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if r.Header.Get("X-WT-Bridge-Nonce") != nonce {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			command, err := readBrowserCommand(commandFile)
			if err != nil {
				http.Error(w, "command unavailable", http.StatusInternalServerError)
				return
			}
			if command == nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(command)
			return
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST, OPTIONS")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("X-WT-Bridge-Nonce") != nonce {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		defer r.Body.Close()
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodySize))
		if err != nil {
			http.Error(w, "invalid body", http.StatusRequestEntityTooLarge)
			return
		}
		result, err := importExport(body)
		if err != nil {
			http.Error(w, "import failed", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
		if !persistent {
			done <- nil
		}
	})
}

func browserCommandFile() string {
	dir := strings.TrimSpace(os.Getenv("WT_NATIVE_GUI_CONFIG_DIR"))
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "browser-refresh.command"
		}
		dir = filepath.Join(home, ".config", "WhiteTransport")
	}
	return filepath.Join(dir, "browser-refresh.command")
}

func readBrowserCommand(path string) (map[string]string, error) {
	// Claim the command with an atomic rename before reading it. This keeps
	// concurrent polling requests from delivering the same refresh twice.
	claimID, err := randomNonce()
	if err != nil {
		return nil, err
	}
	claimedPath := path + ".claimed-" + claimID
	if err := os.Rename(path, claimedPath); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer os.Remove(claimedPath)

	body, err := os.ReadFile(claimedPath)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "update" {
		return map[string]string{"command": "update"}, nil
	}
	var command map[string]string
	if err := json.Unmarshal(body, &command); err != nil || command["command"] != "update" {
		return nil, nil
	}
	if command["url"] != "" {
		parsed, err := url.Parse(command["url"])
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return nil, nil
		}
		command["url"] = parsed.String()
	}
	return command, nil
}

// setBrowserCORSHeaders permits syntactically valid Chrome extension origins
// while rejecting ordinary web pages. Requests without Origin remain available
// to the CLI/headless canary. The nonce is the only request gate in this
// development contract; production enrollment is a later security slice.
func setBrowserCORSHeaders(w http.ResponseWriter, r *http.Request) error {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return nil
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "chrome-extension" || parsed.Host == "" || parsed.Host != parsed.Hostname() || !isChromeExtensionID(parsed.Hostname()) || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("unsupported browser origin")
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-WT-Bridge-Nonce")
	w.Header().Set("Access-Control-Max-Age", "60")
	w.Header().Set("Vary", "Origin")
	return nil
}

func isChromeExtensionID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, char := range value {
		if char < 'a' || char > 'p' {
			return false
		}
	}
	return true
}

func importBrowserExport(body []byte) (importResult, error) {
	if !json.Valid(body) {
		return importResult{}, errors.New("browser export is not JSON")
	}
	credentials, err := runtime.ParseBrowserExportBytes(body)
	if err != nil {
		return importResult{}, err
	}
	if _, err := runtime.ReplaceClientCredentialsForPlatforms(credentials); err != nil {
		return importResult{}, err
	}
	resources := runtime.ResolveRuntimeResources(runtime.CurrentRuntimeMode())
	_, generated, err := runtime.EnsureManagedDaemonConfig(resources, runtime.NewDisabledLogSink())
	if err != nil {
		return importResult{}, fmt.Errorf("refresh managed daemon config: %w", err)
	}
	platforms := make(map[string]struct{}, len(credentials))
	for _, credential := range credentials {
		platforms[credential.Platform] = struct{}{}
	}
	result := importResult{Count: len(credentials)}
	for platform := range platforms {
		result.Platforms = append(result.Platforms, platform)
	}
	sort.Strings(result.Platforms)
	result.ConfigRefreshed = generated.Path != ""
	return result, nil
}

func randomNonce() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, strings.TrimSpace(err.Error()))
	os.Exit(1)
}
