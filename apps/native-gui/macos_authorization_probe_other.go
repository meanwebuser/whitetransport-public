//go:build !darwin

package main

func probeMacAuthorization() macOSAuthorizationProbeResult {
	return macOSAuthorizationProbeResult{
		Supported: false,
		Operation: "health",
		Error:     "macOS 13 or newer is required for SMAppService",
	}
}
