package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// BrowserExport is the format exported by browser extensions (cookies + localStorage).
type BrowserExport struct {
	Version        int               `json:"version"`
	ExportedAt     string            `json:"exportedAt"`
	Source         ExportSource      `json:"source"`
	SelectedTypes  []string          `json:"selectedTypes"`
	Cookies        []BrowserCookie   `json:"cookies,omitempty"`
	LocalStorage   []BrowserKeyValue `json:"localStorage,omitempty"`
	SessionStorage []BrowserKeyValue `json:"sessionStorage,omitempty"`
}

// ExportSource describes where the export came from.
type ExportSource struct {
	URL  string `json:"url"`
	Host string `json:"host"`
}

// BrowserCookie is a single cookie from browser export.
type BrowserCookie struct {
	Name           string  `json:"name"`
	Value          string  `json:"value"`
	Domain         string  `json:"domain"`
	Path           string  `json:"path"`
	Secure         bool    `json:"secure"`
	HTTPOnly       bool    `json:"httpOnly"`
	SameSite       string  `json:"sameSite,omitempty"`
	ExpirationDate float64 `json:"expirationDate,omitempty"`
	StoreID        string  `json:"storeId,omitempty"`
}

// BrowserKeyValue is a localStorage or sessionStorage entry.
type BrowserKeyValue struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// ParseBrowserExport reads a browser export file and extracts credentials.
func ParseBrowserExport(filePath string) ([]ClientCredential, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read export file: %w", err)
	}

	var export BrowserExport
	if err := json.Unmarshal(data, &export); err != nil {
		return nil, fmt.Errorf("parse export JSON: %w", err)
	}

	host := strings.ToLower(export.Source.Host)
	creds := make([]ClientCredential, 0, 3)

	// Parse by platform based on host
	switch {
	case strings.Contains(host, "wb.ru") || strings.Contains(host, "wildberries"):
		creds = parseWBStreamExport(&export)
	case strings.Contains(host, "dion.ru") || strings.Contains(host, "dion"):
		creds = parseDionExport(&export)
	case strings.Contains(host, "telemost.yandex") || strings.Contains(host, "telemost"):
		creds = parseTelemostExport(&export)
	case strings.Contains(host, "vk.com") || strings.Contains(host, "vk"):
		creds = parseVKExport(&export)
	case strings.Contains(host, "disk.yandex") || strings.Contains(host, "yandex.disk"):
		creds = parseYandexDiskExport(&export)
	default:
		// Try to detect from localStorage keys
		if hasWBStreamKeys(&export) {
			creds = parseWBStreamExport(&export)
		} else if hasDionKeys(&export) {
			creds = parseDionExport(&export)
		} else {
			return nil, fmt.Errorf("unsupported platform for host: %s", export.Source.Host)
		}
	}

	if len(creds) == 0 {
		return nil, fmt.Errorf("no credentials found in export from %s", export.Source.Host)
	}

	return creds, nil
}

func parseWBStreamExport(export *BrowserExport) []ClientCredential {
	// Extract x_wbaas_token from cookies
	var cookieHeader, accessToken string
	var cookies []BrowserCookie

	for _, c := range export.Cookies {
		if c.Name == "x_wbaas_token" || c.Name == "wbx-validation-key" || c.Name == "_wbauid" {
			cookies = append(cookies, c)
			if c.Name == "x_wbaas_token" {
				cookieHeader = fmt.Sprintf("%s=%s", c.Name, c.Value)
			}
		}
	}

	// Build full cookie header
	if len(cookies) > 1 {
		parts := make([]string, 0, len(cookies))
		for _, c := range cookies {
			parts = append(parts, fmt.Sprintf("%s=%s", c.Name, c.Value))
		}
		cookieHeader = strings.Join(parts, "; ")
	}

	// Extract accessToken from localStorage wb_auth_auth_slice
	for _, kv := range export.LocalStorage {
		if kv.Key == "wb_auth_auth_slice" {
			var authSlice struct {
				AccessToken string `json:"accessToken"`
				Phone       string `json:"phone"`
			}
			if err := json.Unmarshal([]byte(kv.Value), &authSlice); err == nil {
				accessToken = authSlice.AccessToken
			}
			break
		}
	}

	if accessToken == "" && cookieHeader == "" {
		return nil
	}

	now := time.Now().UTC()
	cred := ClientCredential{
		ID:        fmt.Sprintf("wbstream-%d", now.UnixNano()),
		Platform:  "wbstream",
		Label:     fmt.Sprintf("Imported from %s", export.Source.Host),
		Token:     accessToken,
		Cookie:    cookieHeader,
		CreatedAt: now,
	}

	return []ClientCredential{cred}
}

func parseDionExport(export *BrowserExport) []ClientCredential {
	var accessToken, refreshToken string
	var cookieToken string
	var accessCookie bool

	// Look for tokens in localStorage
	for _, kv := range export.LocalStorage {
		key := strings.ToLower(kv.Key)
		if strings.Contains(key, "access") || strings.Contains(key, "token") || strings.Contains(key, "dion") {
			// Try to parse as JSON
			var obj map[string]interface{}
			if err := json.Unmarshal([]byte(kv.Value), &obj); err == nil {
				if tok, ok := obj["access_token"].(string); ok {
					accessToken = tok
				}
				if tok, ok := obj["refresh_token"].(string); ok {
					refreshToken = tok
				}
			} else {
				// Direct token value
				if strings.HasPrefix(kv.Value, "eyJ") {
					accessToken = kv.Value
				}
			}
		}
	}

	// Also check cookies
	for _, c := range export.Cookies {
		name := strings.ToLower(c.Name)
		if strings.Contains(name, "refresh") {
			if refreshToken == "" {
				refreshToken = c.Value
			}
			continue
		}
		if strings.Contains(name, "access") {
			accessToken = c.Value
			accessCookie = true
			continue
		}
		if (strings.Contains(name, "token") || strings.Contains(name, "auth")) && cookieToken == "" {
			cookieToken = c.Value
		}
	}
	if cookieToken != "" && !accessCookie {
		accessToken = cookieToken
	}

	if accessToken == "" {
		return nil
	}

	now := time.Now().UTC()
	extra := ""
	if refreshToken != "" {
		extra = fmt.Sprintf("refresh_token=%s", refreshToken)
	}

	cred := ClientCredential{
		ID:        fmt.Sprintf("dion-%d", now.UnixNano()),
		Platform:  "dion",
		Label:     fmt.Sprintf("Imported from %s", export.Source.Host),
		Token:     accessToken,
		Extra:     extra,
		CreatedAt: now,
	}

	return []ClientCredential{cred}
}

func parseTelemostExport(export *BrowserExport) []ClientCredential {
	// Telemost uses Yandex cookies
	var sessionCookie string
	for _, c := range export.Cookies {
		if c.Name == "Session_id" || c.Name == "sessionid2" || c.Name == "sessar" {
			if sessionCookie == "" {
				sessionCookie = fmt.Sprintf("%s=%s", c.Name, c.Value)
			}
		}
	}

	// Build full cookie header
	if len(export.Cookies) > 0 {
		parts := make([]string, 0, len(export.Cookies))
		for _, c := range export.Cookies {
			if c.Name == "Session_id" || c.Name == "sessionid2" || c.Name == "sessar" ||
				c.Name == "yandexuid" || c.Name == "yuidss" || c.Name == "i" {
				parts = append(parts, fmt.Sprintf("%s=%s", c.Name, c.Value))
			}
		}
		if len(parts) > 0 {
			sessionCookie = strings.Join(parts, "; ")
		}
	}

	if sessionCookie == "" {
		return nil
	}

	now := time.Now().UTC()
	cred := ClientCredential{
		ID:        fmt.Sprintf("telemost-%d", now.UnixNano()),
		Platform:  "telemost",
		Label:     fmt.Sprintf("Imported from %s", export.Source.Host),
		Cookie:    sessionCookie,
		CreatedAt: now,
	}
	// A live meeting URL is a capability scoped to the local client. Keep it
	// with the imported cookie so the role-reversal creator can join the exact
	// room BrowserOS is already using; do not place it in shared configuration.
	if joinLink := strings.TrimSpace(export.Source.URL); strings.Contains(strings.ToLower(joinLink), "telemost.yandex") && !strings.HasSuffix(strings.TrimRight(joinLink, "/"), "telemost.yandex.ru") {
		cred.Extra = joinLink
	}

	return []ClientCredential{cred}
}

func parseVKExport(export *BrowserExport) []ClientCredential {
	// VK uses remixsid cookie or localStorage token
	var token, cookie string

	for _, c := range export.Cookies {
		if c.Name == "remixsid" {
			cookie = fmt.Sprintf("remixsid=%s", c.Value)
		}
	}

	for _, kv := range export.LocalStorage {
		if strings.Contains(kv.Key, "token") || strings.Contains(kv.Key, "auth") {
			if strings.HasPrefix(kv.Value, "vk") {
				token = kv.Value
			}
		}
	}

	if token == "" && cookie == "" {
		return nil
	}

	now := time.Now().UTC()
	cred := ClientCredential{
		ID:        fmt.Sprintf("vk-%d", now.UnixNano()),
		Platform:  "vk",
		Label:     fmt.Sprintf("Imported from %s", export.Source.Host),
		Token:     token,
		Cookie:    cookie,
		CreatedAt: now,
	}

	return []ClientCredential{cred}
}

func parseYandexDiskExport(export *BrowserExport) []ClientCredential {
	// Yandex Disk uses OAuth token or cookies
	var oauthToken, cookieHeader string

	// Check localStorage for OAuth token
	for _, kv := range export.LocalStorage {
		if strings.Contains(kv.Key, "oauth") || strings.Contains(kv.Key, "token") {
			if strings.HasPrefix(kv.Value, "y0_") || strings.HasPrefix(kv.Value, "AQ") {
				oauthToken = kv.Value
				break
			}
		}
	}

	// Check cookies
	if oauthToken == "" {
		for _, c := range export.Cookies {
			if c.Name == "Session_id" || c.Name == "sessionid2" {
				cookieHeader = fmt.Sprintf("%s=%s", c.Name, c.Value)
				break
			}
		}
	}

	if oauthToken == "" && cookieHeader == "" {
		return nil
	}

	now := time.Now().UTC()
	cred := ClientCredential{
		ID:        fmt.Sprintf("yandex-%d", now.UnixNano()),
		Platform:  "yandex",
		Label:     fmt.Sprintf("Imported from %s", export.Source.Host),
		Token:     oauthToken,
		Cookie:    cookieHeader,
		CreatedAt: now,
	}

	return []ClientCredential{cred}
}

func hasWBStreamKeys(export *BrowserExport) bool {
	for _, kv := range export.LocalStorage {
		if kv.Key == "wb_auth_auth_slice" || kv.Key == "wb_stream_auth_slice" {
			return true
		}
	}
	for _, c := range export.Cookies {
		if c.Name == "x_wbaas_token" {
			return true
		}
	}
	return false
}

func hasDionKeys(export *BrowserExport) bool {
	for _, kv := range export.LocalStorage {
		if strings.Contains(kv.Key, "dion") || strings.Contains(kv.Key, "video") {
			return true
		}
	}
	return false
}
