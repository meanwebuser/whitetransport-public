package core_test

import (
	"os"
	"testing"

	"golang.org/x/mod/modfile"
)

const (
	relayModulePath   = "whitelist-bypass/relay"
	publicRelayPath   = "github.com/meanwebuser/whitelist-bypass/relay"
	publicRelayCommit = "v0.0.0-20260714105546-90690e4322e6"
)

// TestRelayDependencyIsPublicAndPinned protects clean public-clone builds from
// accidentally depending on an ignored sibling checkout or a floating branch.
func TestRelayDependencyIsPublicAndPinned(t *testing.T) {
	content, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}

	parsed, err := modfile.Parse("go.mod", content, nil)
	if err != nil {
		t.Fatalf("parse go.mod: %v", err)
	}

	for _, replacement := range parsed.Replace {
		if replacement.Old.Path != relayModulePath {
			continue
		}
		if replacement.New.Path != publicRelayPath || replacement.New.Version != publicRelayCommit {
			t.Fatalf("%s replacement = %s@%s, want %s@%s", relayModulePath, replacement.New.Path, replacement.New.Version, publicRelayPath, publicRelayCommit)
		}
		return
	}

	t.Fatalf("%s has no pinned public replacement", relayModulePath)
}
