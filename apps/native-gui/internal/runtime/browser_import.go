package runtime

import (
	"encoding/json"
	"fmt"
	"net/url"
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
	return ParseBrowserExportBytes(data)
}

// ParseBrowserExportBytes parses an in-memory browser export and extracts credentials.
func ParseBrowserExportBytes(data []byte) ([]ClientCredential, error) {
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
			creds = parseGenericBrowserExport(&export)
		}
	}

	if len(creds) == 0 {
		creds = parseGenericBrowserExport(&export)
	}
	if len(creds) == 0 {
		return nil, fmt.Errorf("no cookies found in export from %s", export.Source.Host)
	}

	return creds, nil
}

func parseGenericBrowserExport(export *BrowserExport) []ClientCredential {
	parts := make([]string, 0, len(export.Cookies))
	for _, cookie := range export.Cookies {
		if strings.TrimSpace(cookie.Name) == "" || cookie.Value == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s", cookie.Name, cookie.Value))
	}
	if len(parts) == 0 {
		return nil
	}
	extra := ""
	if source, err := url.Parse(strings.TrimSpace(export.Source.URL)); err == nil && source.Scheme == "https" && source.Host != "" {
		extra = source.Scheme + "://" + source.Host + source.EscapedPath()
	}
	now := time.Now().UTC()
	return []ClientCredential{{
		ID:        fmt.Sprintf("browser-%d", now.UnixNano()),
		Platform:  "browser",
		Label:     fmt.Sprintf("Imported from %s", export.Source.Host),
		Cookie:    strings.Join(parts, "; "),
		Extra:     extra,
		CreatedAt: now,
	}}
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
	// Tracking identifiers alone do not establish an authenticated session.
	hasSession := false
	parts := make([]string, 0, len(export.Cookies))
	for _, c := range export.Cookies {
		if strings.TrimSpace(c.Value) == "" {
			continue
		}
		switch c.Name {
		case "Session_id", "sessionid2", "sessar", "L", "yandex_login", "ymex", "yp", "cycada", "_yasc":
			hasSession = true
		case "yandexuid", "yuidss", "i", "_mssngr_flags", "_mssngr_saved_state":
		default:
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s", c.Name, c.Value))
	}
	if !hasSession {
		return nil
	}

	now := time.Now().UTC()
	cred := ClientCredential{
		ID:        fmt.Sprintf("telemost-%d", now.UnixNano()),
		Platform:  "telemost",
		Label:     fmt.Sprintf("Imported from %s", export.Source.Host),
		Cookie:    strings.Join(parts, "; "),
		CreatedAt: now,
	}
	// Only an official meeting path is a room capability. A homepage or chat
	// URL still permits cookie import, but must not masquerade as a join link.
	meeting, err := url.Parse(strings.TrimSpace(export.Source.URL))
	if err == nil && meeting.Scheme == "https" && meeting.Host == "telemost.yandex.ru" &&
		meeting.User == nil && strings.HasPrefix(meeting.Path, "/j/") &&
		strings.Trim(strings.TrimPrefix(meeting.Path, "/j/"), "/") != "" {
		meeting.RawQuery = ""
		meeting.ForceQuery = false
		meeting.Fragment = ""
		meeting.RawFragment = ""
		cred.Extra = meeting.String()
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
