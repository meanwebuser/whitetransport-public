// Package secrets loads the canonical token-store.json file for builds, tests,
// and config generation. It is the single Go entry point for reading secrets
// from the monorepo's secrets/production/*.json sources (via the generated
// token-store.json).
package secrets

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/meanwebuser/whitetransport/core/internal/config"
)

// DefaultTokenStorePath returns the path to secrets/token-store.json relative
// to the monorepo root.
func DefaultTokenStorePath(repoRoot string) string {
	return filepath.Join(repoRoot, "secrets", "token-store.json")
}

// TestOverridePath returns the path to secrets/token-store.test.json.
func TestOverridePath(repoRoot string) string {
	return filepath.Join(repoRoot, "secrets", "token-store.test.json")
}

// tokenStoreJSON is the on-disk format of secrets/token-store.json.
type tokenStoreJSON struct {
	Tokens   []json.RawMessage `json:"tokens"`
	Bindings []json.RawMessage `json:"bindings"`
}

// LoadTokenStore reads secrets/token-store.json and returns a config.TokenStoreConfig.
// If a test override file exists (secrets/token-store.test.json), its entries are
// merged on top (matching by token ID for tokens, by token_id+platform+role for bindings).
func LoadTokenStore(repoRoot string) (*config.TokenStoreConfig, error) {
	path := DefaultTokenStorePath(repoRoot)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read token store: %w", err)
	}

	var store config.TokenStoreConfig
	// Parse the full file, ignoring _comment and _source_hashes metadata
	var raw struct {
		Tokens   []config.TokenEntry   `json:"tokens"`
		Bindings []config.BindingEntry `json:"bindings"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse token store: %w", err)
	}
	store.Tokens = raw.Tokens
	store.Bindings = raw.Bindings

	// Merge test overrides if present
	overridePath := TestOverridePath(repoRoot)
	if overrideData, err := os.ReadFile(overridePath); err == nil {
		var override struct {
			Tokens   []config.TokenEntry   `json:"tokens"`
			Bindings []config.BindingEntry `json:"bindings"`
		}
		if err := json.Unmarshal(overrideData, &override); err == nil {
			mergeOverrides(&store, &config.TokenStoreConfig{Tokens: override.Tokens, Bindings: override.Bindings})
		}
	}

	return &store, nil
}

// mergeOverrides applies override tokens/bindings on top of the base store.
func mergeOverrides(base, override *config.TokenStoreConfig) {
	// Override tokens by ID
	tokenMap := make(map[string]int, len(base.Tokens))
	for i, t := range base.Tokens {
		tokenMap[t.ID] = i
	}
	for _, ot := range override.Tokens {
		if idx, ok := tokenMap[ot.ID]; ok {
			base.Tokens[idx] = ot
		} else {
			base.Tokens = append(base.Tokens, ot)
		}
	}

	// Override bindings by (token_id, platform, connection_type, role)
	type bkey struct{ TokenID, Platform, ConnType, Role string }
	bindMap := make(map[bkey]int, len(base.Bindings))
	for i, b := range base.Bindings {
		bindMap[bkey{b.TokenID, b.Platform, b.ConnectionType, b.Role}] = i
	}
	for _, ob := range override.Bindings {
		k := bkey{ob.TokenID, ob.Platform, ob.ConnectionType, ob.Role}
		if idx, ok := bindMap[k]; ok {
			base.Bindings[idx] = ob
		} else {
			base.Bindings = append(base.Bindings, ob)
		}
	}
}

// RepoRoot finds the monorepo root by walking up from the caller's file.
// Useful in test helpers:  root := secrets.RepoRoot()
func RepoRoot() string {
	_, file, _, ok := runtime.Caller(1)
	if !ok {
		return ""
	}
	dir := filepath.Dir(file)
	// Walk up until we find go.mod in core/go, then go up 2 more levels
	for {
		if _, err := os.Stat(filepath.Join(dir, "core", "go", "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "" // reached filesystem root
		}
		dir = parent
	}
}
