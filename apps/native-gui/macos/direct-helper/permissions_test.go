package main

import (
	"os"
	"strings"
	"testing"
)

// The GUI process runs as the console user while direct-helper is launched via
// sudo. The helper log is redacted and must remain readable by that user.
// Keep this Darwin-only open mode contract visible even when tests run on Linux
// (where runner_darwin.go is not compiled).
func TestDirectHelperLogArtifactIsOwnerReadable(t *testing.T) {
	data, err := os.ReadFile("runner_darwin.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644") < 2 {
		t.Fatal("direct-helper log must be opened owner-readable (0644) for the non-root GUI")
	}
}
