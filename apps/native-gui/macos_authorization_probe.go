package main

// macOSAuthorizationProbeResult is intentionally small and credential-free so
// Wails can render the privileged-boundary health result without exposing an
// authorization token, lease capability, or helper path.
type macOSAuthorizationProbeResult struct {
	Supported     bool   `json:"supported"`
	Registered    bool   `json:"registered"`
	Authorized    bool   `json:"authorized"`
	Operation     string `json:"operation"`
	HelperVersion string `json:"helperVersion"`
	Error         string `json:"error,omitempty"`
}

// ProbeMacAuthorization asks the native bridge to register the bundled
// LaunchDaemon and perform its typed no-op health request. It does not start a
// tunnel or mutate routes.
func (a *App) ProbeMacAuthorization() macOSAuthorizationProbeResult {
	return probeMacAuthorization()
}
