package main

import (
	"runtime"
	"strings"
	"testing"
)

func TestProbeMacAuthorizationIsCredentialFreeAndExplicitlyUnsupportedOffMac(t *testing.T) {
	result := probeMacAuthorization()
	if result.Operation != "health" {
		t.Fatalf("operation = %q, want health", result.Operation)
	}
	if runtime.GOOS != "darwin" {
		if result.Supported || result.Registered || result.Authorized {
			t.Fatalf("non-Darwin probe = %+v, want unsupported and inactive", result)
		}
		if !strings.Contains(result.Error, "macOS 13") {
			t.Fatalf("non-Darwin error = %q, want explicit macOS 13 requirement", result.Error)
		}
	}
}
