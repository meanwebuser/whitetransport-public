package runtime

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseGenericBrowserExportRetainsCookiesForDemoBridge(t *testing.T) {
	raw, err := json.Marshal(BrowserExport{
		Version: 1,
		Source:  ExportSource{Host: "vk.com", URL: "https://vk.com/feed?private=drop"},
		Cookies: []BrowserCookie{
			{Name: "remixsid", Value: "synthetic-vk-cookie", Domain: ".vk.com"},
			{Name: "device_id", Value: "synthetic-device", Domain: ".vk.com"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := ParseBrowserExportBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(credentials) != 1 || credentials[0].Platform != "vk" {
		t.Fatalf("credentials = %+v", credentials)
	}
	if !strings.Contains(credentials[0].Cookie, "remixsid=synthetic-vk-cookie") {
		t.Fatalf("cookie bundle was not retained: %q", credentials[0].Cookie)
	}
}

func TestParseUnknownBrowserExportStoresTemporaryGenericBundle(t *testing.T) {
	raw, err := json.Marshal(BrowserExport{
		Version: 1,
		Source:  ExportSource{Host: "service.example", URL: "https://service.example/account?drop=1"},
		Cookies: []BrowserCookie{{Name: "session", Value: "synthetic-generic-cookie", Domain: ".service.example"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := ParseBrowserExportBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(credentials) != 1 || credentials[0].Platform != "browser" {
		t.Fatalf("credentials = %+v", credentials)
	}
	if credentials[0].Extra != "https://service.example/account" {
		t.Fatalf("extra = %q", credentials[0].Extra)
	}
}
