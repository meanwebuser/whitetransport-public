package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const clientTokensFilename = "client-tokens.json"

var supportedPlatforms = []string{"wbstream", "telemost", "dion", "vk", "ok"}

// ClientCredential is a single locally-stored platform credential.
// Credentials never leave the device — they are used only to create
// egress rooms on the client side (role reversal).
type ClientCredential struct {
	ID        string    `json:"id"`
	Platform  string    `json:"platform"`
	Label     string    `json:"label,omitempty"`
	Token     string    `json:"token,omitempty"`
	Cookie    string    `json:"cookie,omitempty"`
	Extra     string    `json:"extra,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ClientCredentialFile is the on-disk format for client-tokens.json.
type ClientCredentialFile struct {
	Credentials []ClientCredential `json:"credentials"`
}

// ClientCredentialSummary is the safe view returned to the frontend.
// Token/Cookie/Extra values are masked.
type ClientCredentialSummary struct {
	ID        string    `json:"id"`
	Platform  string    `json:"platform"`
	Label     string    `json:"label,omitempty"`
	HasToken  bool      `json:"has_token"`
	HasCookie bool      `json:"has_cookie"`
	CreatedAt time.Time `json:"created_at"`
}

// ClientTokensPath returns the path to the local client-tokens.json.
func ClientTokensPath() (string, error) {
	dir, err := DefaultRuntimeConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, clientTokensFilename), nil
}

// LoadClientCredentials reads the local client-tokens.json file.
func LoadClientCredentials() ([]ClientCredential, error) {
	path, err := ClientTokensPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read client tokens: %w", err)
	}
	var file ClientCredentialFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse client tokens: %w", err)
	}
	return file.Credentials, nil
}

// SaveClientCredentials writes credentials to the local client-tokens.json.
func SaveClientCredentials(creds []ClientCredential) error {
	path, err := ClientTokensPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create client tokens dir: %w", err)
	}
	file := ClientCredentialFile{Credentials: creds}
	payload, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal client tokens: %w", err)
	}
	temporary, err := os.CreateTemp(dir, "."+clientTokensFilename+"-*")
	if err != nil {
		return fmt.Errorf("create temporary client tokens: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("set temporary client tokens permissions: %w", err)
	}
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary client tokens: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary client tokens: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary client tokens: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace client tokens: %w", err)
	}
	return nil
}

// AddClientCredential adds or updates a credential and persists it.
func AddClientCredential(cred ClientCredential) ([]ClientCredential, error) {
	if cred.Platform == "" {
		return nil, fmt.Errorf("platform is required")
	}
	if !isSupportedPlatform(cred.Platform) {
		return nil, fmt.Errorf("unsupported platform: %s", cred.Platform)
	}
	creds, err := LoadClientCredentials()
	if err != nil {
		return nil, err
	}
	if cred.ID == "" {
		cred.ID = fmt.Sprintf("%s-%d", cred.Platform, time.Now().UTC().UnixNano())
	}
	if cred.CreatedAt.IsZero() {
		cred.CreatedAt = time.Now().UTC()
	}
	updated := false
	for i, c := range creds {
		if c.ID == cred.ID {
			creds[i] = cred
			updated = true
			break
		}
	}
	if !updated {
		creds = append(creds, cred)
	}
	sort.Slice(creds, func(i, j int) bool {
		if creds[i].Platform != creds[j].Platform {
			return creds[i].Platform < creds[j].Platform
		}
		return creds[i].CreatedAt.Before(creds[j].CreatedAt)
	})
	if err := SaveClientCredentials(creds); err != nil {
		return nil, err
	}
	return creds, nil
}

// ReplaceClientCredentialsForPlatforms atomically replaces all local
// credentials for platforms present in replacements. Browser exports contain
// one current session per provider, so retaining earlier sessions would let the
// runtime choose stale credentials after a successful import.
func ReplaceClientCredentialsForPlatforms(replacements []ClientCredential) ([]ClientCredential, error) {
	if len(replacements) == 0 {
		return nil, fmt.Errorf("at least one replacement credential is required")
	}

	platforms := make(map[string]struct{}, len(replacements))
	ids := make(map[string]struct{}, len(replacements))
	for index := range replacements {
		credential := &replacements[index]
		if credential.Platform == "" {
			return nil, fmt.Errorf("replacement credential platform is required")
		}
		if !isSupportedPlatform(credential.Platform) {
			return nil, fmt.Errorf("unsupported platform: %s", credential.Platform)
		}
		if _, duplicate := platforms[credential.Platform]; duplicate {
			return nil, fmt.Errorf("multiple replacement credentials for platform: %s", credential.Platform)
		}
		platforms[credential.Platform] = struct{}{}
		if credential.ID == "" {
			credential.ID = fmt.Sprintf("%s-%d", credential.Platform, time.Now().UTC().UnixNano()+int64(index))
		}
		if _, duplicate := ids[credential.ID]; duplicate {
			return nil, fmt.Errorf("duplicate replacement credential id: %s", credential.ID)
		}
		ids[credential.ID] = struct{}{}
		if credential.CreatedAt.IsZero() {
			credential.CreatedAt = time.Now().UTC()
		}
	}

	current, err := LoadClientCredentials()
	if err != nil {
		return nil, err
	}
	updated := make([]ClientCredential, 0, len(current)+len(replacements))
	for _, credential := range current {
		if _, replaced := platforms[credential.Platform]; !replaced {
			updated = append(updated, credential)
		}
	}
	updated = append(updated, replacements...)
	sort.Slice(updated, func(i, j int) bool {
		if updated[i].Platform != updated[j].Platform {
			return updated[i].Platform < updated[j].Platform
		}
		return updated[i].CreatedAt.Before(updated[j].CreatedAt)
	})
	if err := SaveClientCredentials(updated); err != nil {
		return nil, err
	}
	return updated, nil
}

// RemoveClientCredential deletes a credential by ID.
func RemoveClientCredential(id string) ([]ClientCredential, error) {
	creds, err := LoadClientCredentials()
	if err != nil {
		return nil, err
	}
	filtered := creds[:0]
	for _, c := range creds {
		if c.ID != id {
			filtered = append(filtered, c)
		}
	}
	if err := SaveClientCredentials(filtered); err != nil {
		return nil, err
	}
	return filtered, nil
}

// SummarizeClientCredentials converts credentials to safe summaries.
func SummarizeClientCredentials(creds []ClientCredential) []ClientCredentialSummary {
	out := make([]ClientCredentialSummary, len(creds))
	for i, c := range creds {
		out[i] = ClientCredentialSummary{
			ID:        c.ID,
			Platform:  c.Platform,
			Label:     c.Label,
			HasToken:  c.Token != "",
			HasCookie: c.Cookie != "",
			CreatedAt: c.CreatedAt,
		}
	}
	return out
}

// HasClientRoomCredentials returns true if at least one video-tunnel
// platform credential (wbstream, telemost, dion) exists locally.
func HasClientRoomCredentials(creds []ClientCredential) bool {
	for _, c := range creds {
		if c.Platform == "wbstream" || c.Platform == "telemost" || c.Platform == "dion" {
			if c.Token != "" || c.Cookie != "" {
				return true
			}
		}
	}
	return false
}

func isSupportedPlatform(p string) bool {
	for _, s := range supportedPlatforms {
		if s == p {
			return true
		}
	}
	return false
}

// SupportedPlatforms returns the list of supported credential platforms.
func SupportedPlatforms() []string {
	out := make([]string, len(supportedPlatforms))
	copy(out, supportedPlatforms)
	return out
}
