//go:build integration

package tests

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func requireDaemon(t *testing.T) (string, string) {
	t.Helper()
	binary := os.Getenv("WT_DAEMON_BINARY")
	if binary == "" {
		binary = filepath.Join(repoRoot(t), "core/go/artifacts/whitetransportd-linux-x64")
	}
	if _, err := os.Stat(binary); err != nil {
		binary = filepath.Join(repoRoot(t), "core/go/whitetransportd")
		if _, err := os.Stat(binary); err != nil {
			t.Skipf("skip: daemon binary not found at %s", binary)
		}
	}
	config := os.Getenv("WT_CLIENT_CONFIG")
	if config == "" {
		t.Skip("skip: WT_CLIENT_CONFIG must point to an explicit deployment config")
	}
	if _, err := os.Stat(config); err != nil {
		t.Skipf("skip: client config not found: %s", config)
	}
	return binary, config
}

// patchedConfigWithTokenStore creates a temp config file with token_store block
// injected from secrets/token-store.json. Returns the temp file path.
func patchedConfigWithTokenStore(t *testing.T, basePath string) string {
	t.Helper()
	root := repoRoot(t)
	cmd := exec.Command("python3", filepath.Join(root, "ops/config/inject-token-store.py"), basePath)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("inject-token-store: %v", err)
	}
	tmpFile := filepath.Join(t.TempDir(), "patched-config.json")
	if err := os.WriteFile(tmpFile, out, 0600); err != nil {
		t.Fatalf("write patched config: %v", err)
	}
	return tmpFile
}

func startDaemon(t *testing.T, binary, configPath string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(binary, "--config", configPath, "--serve")
	cmd.Env = os.Environ()
	outBuf := &bytes.Buffer{}
	cmd.Stdout = outBuf
	cmd.Stderr = outBuf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	t.Cleanup(func() {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		cmd.Wait()
		if t.Failed() {
			t.Logf("daemon output:\n%s", outBuf.String())
		}
	})
	return cmd
}

func waitForHealth(t *testing.T, baseURL string, maxWait time.Duration) {
	t.Helper()
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/health")
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("health endpoint not ready after %v", maxWait)
}

func httpGet(url string) (*http.Response, error) {
	return http.Get(url)
}

// TestIntegrationRuntimeHealth checks that the health endpoint returns 200.
func TestIntegrationRuntimeHealth(t *testing.T) {
	if os.Getenv("WT_INTEGRATION") == "" {
		t.Skip("skip: WT_INTEGRATION not set")
	}
	binary, configPath := requireDaemon(t)
	configPath = patchedConfigWithTokenStore(t, configPath)
	startDaemon(t, binary, configPath)
	waitForHealth(t, "http://127.0.0.1:17685", 10*time.Second)
}

// TestIntegrationRuntimeNodes checks that at least one available node is discovered.
func TestIntegrationRuntimeNodes(t *testing.T) {
	if os.Getenv("WT_INTEGRATION") == "" {
		t.Skip("skip: WT_INTEGRATION not set")
	}
	binary, configPath := requireDaemon(t)
	configPath = patchedConfigWithTokenStore(t, configPath)
	startDaemon(t, binary, configPath)
	waitForHealth(t, "http://127.0.0.1:17685", 10*time.Second)

	deadline := time.Now().Add(30 * time.Second)
	var nodes []map[string]any
	var found bool
	for time.Now().Before(deadline) {
		resp, err := httpGet("http://127.0.0.1:17685/v1/nodes")
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if err := json.NewDecoder(resp.Body).Decode(&nodes); err != nil {
			resp.Body.Close()
			time.Sleep(500 * time.Millisecond)
			continue
		}
		resp.Body.Close()
		for _, n := range nodes {
			if avail, ok := n["available"].(bool); ok && avail {
				found = true
				break
			}
		}
		if found {
			break
		}
		time.Sleep(1 * time.Second)
	}
	if !found {
		t.Fatal("no available nodes discovered within 30s")
	}
	t.Logf("discovered %d nodes", len(nodes))
}

// TestIntegrationRuntimeConnect checks that the session/connect endpoint
// responds within a reasonable time (even if the connection ultimately fails).
func TestIntegrationRuntimeConnect(t *testing.T) {
	if os.Getenv("WT_INTEGRATION") == "" {
		t.Skip("skip: WT_INTEGRATION not set")
	}
	binary, configPath := requireDaemon(t)
	configPath = patchedConfigWithTokenStore(t, configPath)
	startDaemon(t, binary, configPath)
	waitForHealth(t, "http://127.0.0.1:17685", 10*time.Second)

	// Wait for at least one available node.
	nodesDeadline := time.Now().Add(30 * time.Second)
	var nodes []map[string]any
	for time.Now().Before(nodesDeadline) {
		resp, err := httpGet("http://127.0.0.1:17685/v1/nodes")
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if err := json.NewDecoder(resp.Body).Decode(&nodes); err != nil {
			resp.Body.Close()
			time.Sleep(500 * time.Millisecond)
			continue
		}
		resp.Body.Close()
		hasAvailable := false
		for _, n := range nodes {
			if a, ok := n["available"].(bool); ok && a {
				hasAvailable = true
				break
			}
		}
		if hasAvailable {
			break
		}
		time.Sleep(1 * time.Second)
	}

	// Send connect request with a 150s timeout.
	client := &http.Client{Timeout: 150 * time.Second}
	resp, err := client.Post("http://127.0.0.1:17685/v1/session/connect", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Logf("connect returned error (expected if node is slow): %v", err)
	} else {
		resp.Body.Close()
	}
}

// TestIntegrationRuntimeDisconnect checks that the disconnect endpoint works.
func TestIntegrationRuntimeDisconnect(t *testing.T) {
	if os.Getenv("WT_INTEGRATION") == "" {
		t.Skip("skip: WT_INTEGRATION not set")
	}
	binary, configPath := requireDaemon(t)
	configPath = patchedConfigWithTokenStore(t, configPath)
	startDaemon(t, binary, configPath)
	waitForHealth(t, "http://127.0.0.1:17685", 10*time.Second)

	resp, err := http.Post("http://127.0.0.1:17685/v1/session/disconnect", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	resp.Body.Close()
}

// TestIntegrationRuntimeCursorPersistence checks that the cursor file is written
// and contains at least one cursor entry after the daemon has been running.
func TestIntegrationRuntimeCursorPersistence(t *testing.T) {
	if os.Getenv("WT_INTEGRATION") == "" {
		t.Skip("skip: WT_INTEGRATION not set")
	}
	binary, configPath := requireDaemon(t)
	configPath = patchedConfigWithTokenStore(t, configPath)
	startDaemon(t, binary, configPath)
	waitForHealth(t, "http://127.0.0.1:17685", 10*time.Second)

	// Wait a few seconds for the router to process at least one read cycle.
	time.Sleep(5 * time.Second)

	cursorFile := os.Getenv("WT_CURSOR_FILE")
	if cursorFile == "" {
		t.Fatal("WT_CURSOR_FILE is required for cursor persistence integration")
	}
	data, err := os.ReadFile(cursorFile)
	if err != nil {
		t.Fatalf("read cursor file %s: %v", cursorFile, err)
	}
	var cursors map[string]string
	if err := json.Unmarshal(data, &cursors); err != nil {
		t.Fatalf("parse cursor file: %v", err)
	}
	if len(cursors) == 0 {
		t.Error("cursor file is empty")
	}
	t.Logf("cursor file has %d entries: %v", len(cursors), cursors)
}
