// Command browseros-import accepts one credential export from the local
// BrowserOS service and stores only the parsed client-side credentials.
// It intentionally binds to loopback, requires a one-time nonce, and never
// logs an export or credential value.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/meanwebuser/whitetransport/apps/native-gui/internal/runtime"
)

const (
	capturePath = "/browseros-export"
	maxBodySize = 2 << 20
)

type importResult struct {
	Platforms       []string `json:"platforms"`
	Count           int      `json:"count"`
	ConfigRefreshed bool     `json:"config_refreshed,omitempty"`
}

type importFunc func([]byte) (importResult, error)

func main() {
	port := flag.Int("port", 0, "loopback TCP port (0 chooses one)")
	nonce := flag.String("nonce", "", "one-time capture nonce (generated when empty)")
	stdin := flag.Bool("stdin", false, "read one scheduler export from stdin and exit")
	stdinHost := flag.String("stdin-host", "", "Browser host for a scheduler export read from stdin")
	flag.Parse()
	if *stdin {
		if err := importFromStdin(*stdinHost); err != nil {
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
		Handler:           newHandler(*nonce, importBrowserExport, done),
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
	_ = server.Close()
}

func importFromStdin(host string) error {
	body, err := io.ReadAll(io.LimitReader(os.Stdin, maxBodySize+1))
	if err != nil {
		return fmt.Errorf("read scheduler export: %w", err)
	}
	if len(body) > maxBodySize {
		return errors.New("scheduler export is too large")
	}
	normalized, err := normalizeScheduledExport(body, host)
	if err != nil {
		return err
	}
	result, err := importBrowserExport(normalized)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func normalizeScheduledExport(body []byte, host string) ([]byte, error) {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" {
		return nil, errors.New("stdin-host is required for scheduler imports")
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, errors.New("scheduler export is not JSON")
	}
	if _, ok := payload["cookies"]; !ok {
		return nil, errors.New("scheduler export has no cookies")
	}
	var cookieValues map[string]string
	if err := json.Unmarshal(payload["cookies"], &cookieValues); err == nil {
		names := make([]string, 0, len(cookieValues))
		for name := range cookieValues {
			names = append(names, name)
		}
		sort.Strings(names)
		cookies := make([]map[string]string, 0, len(names))
		for _, name := range names {
			cookies = append(cookies, map[string]string{"name": name, "value": cookieValues[name]})
		}
		normalizedCookies, err := json.Marshal(cookies)
		if err != nil {
			return nil, fmt.Errorf("normalize scheduler cookies: %w", err)
		}
		payload["cookies"] = normalizedCookies
	}
	if rawStorage, ok := payload["localStorage"]; ok {
		var storageValues map[string]string
		if err := json.Unmarshal(rawStorage, &storageValues); err == nil {
			keys := make([]string, 0, len(storageValues))
			for key := range storageValues {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			storage := make([]map[string]string, 0, len(keys))
			for _, key := range keys {
				storage = append(storage, map[string]string{"key": key, "value": storageValues[key]})
			}
			normalizedStorage, err := json.Marshal(storage)
			if err != nil {
				return nil, fmt.Errorf("normalize scheduler localStorage: %w", err)
			}
			payload["localStorage"] = normalizedStorage
		}
	}
	payload["version"] = json.RawMessage("1")
	payload["source"] = json.RawMessage(fmt.Sprintf(`{"url":%q,"host":%q}`, "https://"+host+"/", host))
	if _, ok := payload["selectedTypes"]; !ok {
		payload["selectedTypes"] = json.RawMessage(`["cookies"]`)
	}
	return json.Marshal(payload)
}

func newHandler(nonce string, importExport importFunc, done chan<- error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != capturePath {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-WT-Capture-Nonce") != nonce {
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
		done <- nil
	})
}

func importBrowserExport(body []byte) (importResult, error) {
	if !json.Valid(body) {
		return importResult{}, errors.New("browser export is not JSON")
	}
	tmp, err := os.CreateTemp("", "wt-browseros-export-*.json")
	if err != nil {
		return importResult{}, fmt.Errorf("create temporary export: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return importResult{}, fmt.Errorf("write temporary export: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return importResult{}, fmt.Errorf("close temporary export: %w", err)
	}
	credentials, err := runtime.ParseBrowserExport(tmpPath)
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
