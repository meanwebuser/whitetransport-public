package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogSinkWritesAndReadsNewestFirst(t *testing.T) {
	t.Parallel()

	logSink, err := NewLogSink(filepath.Join(t.TempDir(), "WhiteTransport.log"))
	if err != nil {
		t.Fatalf("NewLogSink: %v", err)
	}
	if err := logSink.Write("info", "first", map[string]string{"step": "1"}); err != nil {
		t.Fatalf("Write first: %v", err)
	}
	if err := logSink.Write("warn", "second", nil); err != nil {
		t.Fatalf("Write second: %v", err)
	}
	if err := logSink.Write("error", "third", map[string]string{"step": "3"}); err != nil {
		t.Fatalf("Write third: %v", err)
	}

	lines, err := logSink.ReadLines(2)
	if err != nil {
		t.Fatalf("ReadLines: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("len(lines) = %d, want 2", len(lines))
	}
	if lines[0].Message != "third" || lines[0].Level != "error" || lines[0].Fields["step"] != "3" {
		t.Fatalf("newest line = %+v, want third error", lines[0])
	}
	if lines[1].Message != "second" || lines[1].Level != "warn" {
		t.Fatalf("second line = %+v, want second warn", lines[1])
	}
}

func TestDefaultLogPathHonorsOverride(t *testing.T) {
	override := filepath.Join(t.TempDir(), "override.log")
	t.Setenv("WT_NATIVE_GUI_LOG_FILE", override)
	path, err := DefaultLogPath()
	if err != nil {
		t.Fatalf("DefaultLogPath: %v", err)
	}
	if path != override {
		t.Fatalf("path = %q, want override %q", path, override)
	}
}

func TestNewLogSinkRequiresPath(t *testing.T) {
	t.Parallel()

	if _, err := NewLogSink(""); err == nil {
		t.Fatal("NewLogSink empty path succeeded, want error")
	}
}

func TestLogSinkRedactsSecretsFromMessagesAndFields(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "WhiteTransport.log")
	logSink, err := NewLogSink(path)
	if err != nil {
		t.Fatalf("NewLogSink: %v", err)
	}
	message := "connect vless://fixture-uuid@example.com:443 token=fixture-message-token"
	fields := map[string]string{
		"access_token": "fixture-access-token",
		"endpoint":     "socks5://fixture-user:fixture-pass@127.0.0.1:1080",
		"note":         "Authorization: Bearer fixture-bearer-token",
	}
	if err := logSink.Write("info", message, fields); err != nil {
		t.Fatalf("Write: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(raw)
	for _, secret := range []string{
		"fixture-uuid",
		"fixture-message-token",
		"fixture-access-token",
		"fixture-user",
		"fixture-pass",
		"fixture-bearer-token",
	} {
		if strings.Contains(content, secret) {
			t.Fatalf("persistent log contains secret fixture %q: %s", secret, content)
		}
	}
	if !strings.Contains(content, "[REDACTED]") {
		t.Fatalf("persistent log = %s, want redaction marker", content)
	}
}

func TestLogSinkRedactsExistingLinesWhenReading(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "WhiteTransport.log")
	logSink, err := NewLogSink(path)
	if err != nil {
		t.Fatalf("NewLogSink: %v", err)
	}
	legacy := "{\"level\":\"info\",\"message\":\"token=fixture-old-token\",\"fields\":{\"endpoint\":\"socks5://fixture-old-user:fixture-old-pass@127.0.0.1:1080\"}}\n"
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	lines, err := logSink.ReadLines(10)
	if err != nil {
		t.Fatalf("ReadLines: %v", err)
	}
	encoded := lines[0].Message + lines[0].Fields["endpoint"]
	for _, secret := range []string{"fixture-old-token", "fixture-old-user", "fixture-old-pass"} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("ReadLines returned secret fixture %q: %+v", secret, lines[0])
		}
	}
}
