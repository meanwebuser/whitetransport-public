package policy

import "testing"

func TestMakeBindingKey(t *testing.T) {
	tests := []struct {
		carrierID string
		role      string
		want      string
	}{
		{"vk.messages", "discovery", "vk.messages:discovery"},
		{"vk.messages", "node-client", "vk.messages:node-client"},
		{"vk.messages", "logs", "vk.messages:logs"},
		{"vk.messages", "admin", "vk.messages:admin"},
		{"vk.messages", "flex", "vk.messages:flex"},
		{"vk.messages", "", "vk.messages"},
		{"ok.messages", "discovery", "ok.messages:discovery"},
		{"ssh.tcp", "", "ssh.tcp"},
	}
	for _, tc := range tests {
		got := MakeBindingKey(tc.carrierID, tc.role)
		if got != tc.want {
			t.Errorf("MakeBindingKey(%q, %q) = %q; want %q", tc.carrierID, tc.role, got, tc.want)
		}
	}
}

func TestParseBindingKey(t *testing.T) {
	tests := []struct {
		key       string
		wantID    string
		wantRole  string
	}{
		{"vk.messages:discovery", "vk.messages", "discovery"},
		{"vk.messages:node-client", "vk.messages", "node-client"},
		{"vk.messages", "vk.messages", ""},
		{"ok.messages:logs", "ok.messages", "logs"},
		{"ssh.tcp", "ssh.tcp", ""},
		{"", "", ""},
	}
	for _, tc := range tests {
		gotID, gotRole := ParseBindingKey(tc.key)
		if gotID != tc.wantID || gotRole != tc.wantRole {
			t.Errorf("ParseBindingKey(%q) = (%q, %q); want (%q, %q)",
				tc.key, gotID, gotRole, tc.wantID, tc.wantRole)
		}
	}
}

func TestBindingKeyRoundTrip(t *testing.T) {
	cases := []struct {
		carrierID string
		role      string
	}{
		{"vk.messages", "discovery"},
		{"vk.messages", "node-client"},
		{"vk.messages", ""},
		{"ok.messages", "admin"},
		{"wbstream.vp8", ""},
	}
	for _, tc := range cases {
		key := MakeBindingKey(tc.carrierID, tc.role)
		gotID, gotRole := ParseBindingKey(key)
		if gotID != tc.carrierID || gotRole != tc.role {
			t.Errorf("round-trip failed: MakeBindingKey(%q, %q) = %q → ParseBindingKey = (%q, %q)",
				tc.carrierID, tc.role, key, gotID, gotRole)
		}
	}
}

func TestCarrierIDFromBindingKey(t *testing.T) {
	if got := CarrierIDFromBindingKey("vk.messages:discovery"); got != "vk.messages" {
		t.Errorf("CarrierIDFromBindingKey(\"vk.messages:discovery\") = %q; want \"vk.messages\"", got)
	}
	if got := CarrierIDFromBindingKey("ssh.tcp"); got != "ssh.tcp" {
		t.Errorf("CarrierIDFromBindingKey(\"ssh.tcp\") = %q; want \"ssh.tcp\"", got)
	}
}

func TestHasBindingKeyPrefix(t *testing.T) {
	tests := []struct {
		bindingKey string
		carrierID  string
		want       bool
	}{
		{"vk.messages:discovery", "vk.messages", true},
		{"vk.messages:node-client", "vk.messages", true},
		{"vk.messages", "vk.messages", true},
		{"ok.messages:logs", "vk.messages", false},
		{"vk.docs.256", "vk.messages", false},
		{"vk.messages", "vk.docs", false},
	}
	for _, tc := range tests {
		got := HasBindingKeyPrefix(tc.bindingKey, tc.carrierID)
		if got != tc.want {
			t.Errorf("HasBindingKeyPrefix(%q, %q) = %v; want %v",
				tc.bindingKey, tc.carrierID, got, tc.want)
		}
	}
}
