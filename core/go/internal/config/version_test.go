package config

import "testing"

func TestCompatibilityLine(t *testing.T) {
	testCases := []struct {
		version string
		want    string
		wantErr bool
	}{
		{version: "0.1.1", want: "0.1"},
		{version: "0.1.123", want: "0.1"},
		{version: "0.2.0", want: "0.2"},
		{version: "", wantErr: true},
		{version: "dev", wantErr: true},
		{version: "0.1", wantErr: true},
		{version: "0.1.patch", wantErr: true},
		{version: "v0.1.1", wantErr: true},
	}

	for _, testCase := range testCases {
		t.Run(testCase.version, func(t *testing.T) {
			got, err := CompatibilityLine(testCase.version)
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("CompatibilityLine(%q) = %q, want error", testCase.version, got)
				}
				return
			}
			if err != nil || got != testCase.want {
				t.Fatalf("CompatibilityLine(%q) = %q, %v; want %q", testCase.version, got, err, testCase.want)
			}
		})
	}
}

func TestCurrentProductVersionUsesSelectedCompatibilityLine(t *testing.T) {
	line, err := CompatibilityLine(Version)
	if err != nil {
		t.Fatalf("current Version %q: %v", Version, err)
	}
	if line != "0.1" {
		t.Fatalf("current Version %q has compatibility line %q, want 0.1", Version, line)
	}
}
