package runtime

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseTelemostExportFallsBackToGenericCookieBundleInDemoMode(t *testing.T) {
	for _, cookies := range [][]BrowserCookie{
		{{Name: "yandexuid", Value: "synthetic-tracker", Domain: ".yandex.ru"}},
		{{Name: "Session_id", Value: "", Domain: ".yandex.ru"}, {Name: "i", Value: "synthetic-tracker", Domain: ".yandex.ru"}},
	} {
		raw, err := json.Marshal(BrowserExport{Version: 1, Source: ExportSource{Host: "telemost.yandex.ru", URL: "https://telemost.yandex.ru/"}, Cookies: cookies})
		if err != nil {
			t.Fatal(err)
		}
		credentials, err := ParseBrowserExportBytes(raw)
		if err != nil {
			t.Fatal(err)
		}
		if len(credentials) != 1 || credentials[0].Platform != "browser" || credentials[0].Cookie == "" {
			t.Fatalf("generic demo bundle = %+v", credentials)
		}
	}
}

func TestParseTelemostExportSanitizesMeetingCapability(t *testing.T) {
	for _, tt := range []struct{ name, source, want string }{
		{"meeting", "https://telemost.yandex.ru/j/synthetic-room?tracking=omit#omit", "https://telemost.yandex.ru/j/synthetic-room"},
		{"homepage", "https://telemost.yandex.ru/?tracking=omit", ""},
		{"other page", "https://telemost.yandex.ru/chats", ""},
		{"unrelated host", "https://telemost.yandex.ru.example.com/j/synthetic-room", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(BrowserExport{Version: 1, Source: ExportSource{Host: "telemost.yandex.ru", URL: tt.source}, Cookies: []BrowserCookie{{Name: "Session_id", Value: "synthetic-session", Domain: ".yandex.ru"}}})
			if err != nil {
				t.Fatal(err)
			}
			creds, err := ParseBrowserExportBytes(raw)
			if err != nil {
				t.Fatal(err)
			}
			if len(creds) != 1 || creds[0].Extra != tt.want {
				t.Fatalf("meeting capability not sanitized for %s", tt.name)
			}
		})
	}
}

func TestParseTelemostExportAcceptsPageVisibleYandexSessionCookies(t *testing.T) {
	raw, err := json.Marshal(BrowserExport{
		Version: 1,
		Source:  ExportSource{Host: "telemost.yandex.ru", URL: "https://telemost.yandex.ru/"},
		Cookies: []BrowserCookie{
			{Name: "L", Value: "page-session", Domain: "telemost.yandex.ru"},
			{Name: "yandex_login", Value: "operator", Domain: "telemost.yandex.ru"},
			{Name: "yandexuid", Value: "device", Domain: ".yandex.ru"},
			{Name: "yuidss", Value: "device-session", Domain: ".yandex.ru"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	creds, err := ParseBrowserExportBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 1 {
		t.Fatalf("credentials = %d, want 1", len(creds))
	}
	if creds[0].Platform != "telemost" || creds[0].Cookie == "" {
		t.Fatalf("page-visible session was not retained: %+v", creds[0])
	}
	if !strings.Contains(creds[0].Cookie, "L=page-session") {
		t.Fatalf("session cookie missing from credential: %q", creds[0].Cookie)
	}
}
