package runtime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strings"
	"sync"
	"time"
)

const maxLogTailBytes int64 = 1 << 20

const redactedLogValue = "[REDACTED]"

var (
	sensitiveLogKeyPattern  = regexp.MustCompile(`(?i)(^|[_-])(authorization|cookie|credential|password|passphrase|secret|token|api[_-]?key)($|[_-])`)
	privateURITextPattern   = regexp.MustCompile(`(?i)\b(?:vless|trojan|ss|ssr)://[^\s"'<>]+`)
	URLUserInfoPattern      = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)[^/\s@]+@`)
	bearerTextPattern       = regexp.MustCompile(`(?i)(\b(?:authorization\s*:\s*)?bearer\s+)[^\s,;]+`)
	secretAssignmentPattern = regexp.MustCompile(`(?i)(\b(?:access[_-]?token|refresh[_-]?token|token|password|secret|cookie|api[_-]?key)\s*[=:]\s*)[^\s,;]+`)
	jwtTextPattern          = regexp.MustCompile(`\b[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)
	vkTokenTextPattern      = regexp.MustCompile(`\bvk1\.[A-Za-z0-9._-]+`)
)

// LogLine is one JSON-safe persistent desktop log entry.
type LogLine struct {
	Timestamp string            `json:"timestamp"`
	Level     string            `json:"level"`
	Message   string            `json:"message"`
	Fields    map[string]string `json:"fields,omitempty"`
}

// LogSink writes and tails the native GUI's persistent app log.
type LogSink struct {
	path     string
	disabled bool
	mu       sync.Mutex
	now      func() time.Time
}

// NewDefaultLogSink creates the persistent app log sink for the current OS.
func NewDefaultLogSink() (*LogSink, error) {
	path, err := DefaultLogPath()
	if err != nil {
		return nil, err
	}
	return NewLogSink(path)
}

// NewLogSink creates a file-backed log sink at path. If the file already
// exists and is non-empty, it is rotated to a timestamped backup before
// opening a fresh log file.
func NewLogSink(path string) (*LogSink, error) {
	cleanPath := strings.TrimSpace(path)
	if cleanPath == "" {
		return nil, fmt.Errorf("native gui log path is required")
	}
	if err := os.MkdirAll(filepath.Dir(cleanPath), 0o700); err != nil {
		return nil, fmt.Errorf("create native gui log directory: %w", err)
	}
	// Rotate old log file if it exists and is non-empty.
	if info, err := os.Stat(cleanPath); err == nil && info.Size() > 0 {
		ext := filepath.Ext(cleanPath)
		base := strings.TrimSuffix(cleanPath, ext)
		rotated := fmt.Sprintf("%s-%s%s", base, time.Now().UTC().Format("20060102-150405"), ext)
		_ = os.Rename(cleanPath, rotated)
	}
	file, err := os.OpenFile(cleanPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open native gui log file: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close native gui log file: %w", err)
	}
	return &LogSink{path: cleanPath, now: func() time.Time { return time.Now().UTC() }}, nil
}

// NewDisabledLogSink creates a no-op sink for focused tests.
func NewDisabledLogSink() *LogSink {
	return &LogSink{disabled: true, now: func() time.Time { return time.Now().UTC() }}
}

// DefaultLogPath returns the platform-native persistent app log path.
func DefaultLogPath() (string, error) {
	if override := strings.TrimSpace(os.Getenv("WT_NATIVE_GUI_LOG_FILE")); override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory for native gui log: %w", err)
	}
	switch goruntime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Logs", "WhiteTransport", "WhiteTransport.log"), nil
	case "windows":
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			return filepath.Join(localAppData, "WhiteTransport", "logs", "WhiteTransport.log"), nil
		}
		return filepath.Join(home, "AppData", "Local", "WhiteTransport", "logs", "WhiteTransport.log"), nil
	default:
		if stateHome := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); stateHome != "" {
			return filepath.Join(stateHome, "WhiteTransport", "WhiteTransport.log"), nil
		}
		return filepath.Join(home, ".local", "state", "WhiteTransport", "WhiteTransport.log"), nil
	}
}

// Path returns the log file path. Disabled sinks return an empty path.
func (s *LogSink) Path() string {
	if s == nil || s.disabled {
		return ""
	}
	return s.path
}

// Info writes an informational log entry.
func (s *LogSink) Info(message string, fields map[string]string) {
	_ = s.Write("info", message, fields)
}

// Error writes an error log entry.
func (s *LogSink) Error(message string, err error, fields map[string]string) {
	if fields == nil {
		fields = map[string]string{}
	}
	if err != nil {
		fields["error"] = err.Error()
	}
	_ = s.Write("error", message, fields)
}

// Write appends a structured log entry.
func (s *LogSink) Write(level string, message string, fields map[string]string) error {
	if s == nil || s.disabled {
		return nil
	}
	entry := LogLine{
		Timestamp: s.now().Format(time.RFC3339Nano),
		Level:     normalizeLogLevel(level),
		Message:   RedactText(strings.TrimSpace(message)),
		Fields:    cleanLogFields(fields),
	}
	if entry.Message == "" {
		return fmt.Errorf("native gui log message is required")
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode native gui log entry: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open native gui log file: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("write native gui log entry: %w", err)
	}
	return nil
}

// ReadLines returns the newest log entries first.
func (s *LogSink) ReadLines(limit int) ([]LogLine, error) {
	if s == nil || s.disabled {
		return []LogLine{}, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	content, err := readLogTail(s.path, maxLogTailBytes)
	if err != nil {
		if os.IsNotExist(err) {
			return []LogLine{}, nil
		}
		return nil, err
	}
	lines := parseLogLines(content)
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	reverseLogLines(lines)
	return lines, nil
}

func readLogTail(path string, maxBytes int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat native gui log file: %w", err)
	}
	offset := int64(0)
	if info.Size() > maxBytes {
		offset = info.Size() - maxBytes
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek native gui log file: %w", err)
	}
	content, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("read native gui log file: %w", err)
	}
	if offset > 0 {
		if newline := bytes.IndexByte(content, '\n'); newline >= 0 {
			content = content[newline+1:]
		}
	}
	return content, nil
}

func parseLogLines(content []byte) []LogLine {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var lines []LogLine
	for scanner.Scan() {
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		var line LogLine
		if err := json.Unmarshal([]byte(raw), &line); err != nil || line.Message == "" {
			line = LogLine{Level: "info", Message: RedactText(raw)}
		} else {
			line.Message = RedactText(line.Message)
			line.Fields = cleanLogFields(line.Fields)
		}
		lines = append(lines, line)
	}
	return lines
}

func reverseLogLines(lines []LogLine) {
	for left, right := 0, len(lines)-1; left < right; left, right = left+1, right-1 {
		lines[left], lines[right] = lines[right], lines[left]
	}
}

func normalizeLogLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug", "info", "warn", "error":
		return strings.ToLower(strings.TrimSpace(level))
	default:
		return "info"
	}
}

func cleanLogFields(fields map[string]string) map[string]string {
	if len(fields) == 0 {
		return nil
	}
	cleaned := make(map[string]string, len(fields))
	for key, value := range fields {
		cleanKey := strings.TrimSpace(key)
		if cleanKey == "" {
			continue
		}
		if sensitiveLogKeyPattern.MatchString(cleanKey) {
			cleaned[cleanKey] = redactedLogValue
			continue
		}
		cleaned[cleanKey] = RedactText(value)
	}
	if len(cleaned) == 0 {
		return nil
	}
	return cleaned
}

// RedactText removes credential-bearing URI, header, assignment, and token
// forms before a value reaches persistent logs or UI log readers.
func RedactText(value string) string {
	redacted := privateURITextPattern.ReplaceAllString(value, redactedLogValue)
	redacted = URLUserInfoPattern.ReplaceAllString(redacted, `${1}`+redactedLogValue+`@`)
	redacted = bearerTextPattern.ReplaceAllString(redacted, `${1}`+redactedLogValue)
	redacted = secretAssignmentPattern.ReplaceAllString(redacted, `${1}`+redactedLogValue)
	redacted = jwtTextPattern.ReplaceAllString(redacted, redactedLogValue)
	return vkTokenTextPattern.ReplaceAllString(redacted, redactedLogValue)
}
