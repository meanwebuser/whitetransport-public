package main

import (
	"encoding/json"
	"testing"
)

func TestEmbeddedFrontendIsSharedClientBundle(t *testing.T) {
	payload, err := assets.ReadFile("frontend/dist/whitetransport-client-web.json")
	if err != nil {
		t.Fatalf("read embedded shared bundle marker: %v", err)
	}
	var marker struct {
		Bundle string   `json:"bundle"`
		Schema int      `json:"schema"`
		Shell  []string `json:"shell"`
	}
	if err := json.Unmarshal(payload, &marker); err != nil {
		t.Fatalf("decode embedded shared bundle marker: %v", err)
	}
	if marker.Bundle != "@whitetransport/client-web" || marker.Schema != 1 {
		t.Fatalf("embedded marker = %+v, want canonical client-web bundle", marker)
	}
	wantShell := []string{"home", "endpoints", "settings"}
	if len(marker.Shell) != len(wantShell) {
		t.Fatalf("embedded shell = %v, want %v", marker.Shell, wantShell)
	}
	for index := range wantShell {
		if marker.Shell[index] != wantShell[index] {
			t.Fatalf("embedded shell = %v, want %v", marker.Shell, wantShell)
		}
	}
}
