package runtime

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// AuthSessionStateReady is a non-sensitive completion state for a local
// provider sign-in. It deliberately carries no credential material.
const AuthSessionStateReady = "ready"

// AuthProviderPolicy constrains one provider-owned MiniAuthWebView. Platform
// adapters may load only LoginURL and AllowedHosts; all captured credentials
// remain in local platform storage until a successful completion.
type AuthProviderPolicy struct {
	Platform        string
	LoginURL        string
	AllowedHosts    []string
	CompletionHosts []string
}

// AuthSessionPlan is the safe request supplied to an OS WebView adapter.
type AuthSessionPlan struct {
	Platform     string
	LoginURL     string
	AllowedHosts []string
}

// AuthNavigation is the validated, non-sensitive navigation result returned
// to an OS adapter. Query values and page content are intentionally omitted.
type AuthNavigation struct {
	Host         string
	IsCompletion bool
}

// AuthSessionStatus is safe to render in the UI or logs.
type AuthSessionStatus struct {
	Platform string
	State    string
	Message  string
}

// AuthSessionBroker is the shared Go contract between platform-owned login
// views and the native runtime. It does not launch a browser, persist data, or
// transmit credentials: each OS adapter owns its local WebView and secure
// storage implementation.
type AuthSessionBroker struct {
	policies map[string]AuthProviderPolicy
}

// NewAuthSessionBroker validates policies up front so a platform adapter never
// receives an untrusted origin or insecure initial URL.
func NewAuthSessionBroker(policies []AuthProviderPolicy) (*AuthSessionBroker, error) {
	broker := &AuthSessionBroker{policies: make(map[string]AuthProviderPolicy, len(policies))}
	for _, policy := range policies {
		platform := strings.ToLower(strings.TrimSpace(policy.Platform))
		if !isSupportedPlatform(platform) {
			return nil, fmt.Errorf("auth session platform %q is unsupported", policy.Platform)
		}
		if _, exists := broker.policies[platform]; exists {
			return nil, fmt.Errorf("duplicate auth session policy for %q", platform)
		}

		login, err := secureURL(policy.LoginURL)
		if err != nil {
			return nil, fmt.Errorf("auth session login URL for %q: %w", platform, err)
		}
		allowed, err := normalizedHosts(policy.AllowedHosts)
		if err != nil {
			return nil, fmt.Errorf("auth session allowed hosts for %q: %w", platform, err)
		}
		if !containsHost(allowed, login.Hostname()) {
			return nil, fmt.Errorf("auth session login host %q is outside the allowlist for %q", login.Hostname(), platform)
		}
		completion, err := normalizedHosts(policy.CompletionHosts)
		if err != nil {
			return nil, fmt.Errorf("auth session completion hosts for %q: %w", platform, err)
		}
		for _, host := range completion {
			if !containsHost(allowed, host) {
				return nil, fmt.Errorf("auth session completion host %q is outside the allowlist for %q", host, platform)
			}
		}

		broker.policies[platform] = AuthProviderPolicy{
			Platform:        platform,
			LoginURL:        login.String(),
			AllowedHosts:    allowed,
			CompletionHosts: completion,
		}
	}
	return broker, nil
}

// Start creates the allowlisted plan an OS adapter needs to show a provider
// login page. It never returns prior credentials or browser state.
func (b *AuthSessionBroker) Start(platform string) (AuthSessionPlan, error) {
	policy, err := b.policy(platform)
	if err != nil {
		return AuthSessionPlan{}, err
	}
	return AuthSessionPlan{
		Platform:     policy.Platform,
		LoginURL:     policy.LoginURL,
		AllowedHosts: append([]string(nil), policy.AllowedHosts...),
	}, nil
}

// ValidateNavigation accepts only HTTPS navigations to the declared provider
// allowlist. It intentionally discards URL query values before returning.
func (b *AuthSessionBroker) ValidateNavigation(platform, rawURL string) (AuthNavigation, error) {
	policy, err := b.policy(platform)
	if err != nil {
		return AuthNavigation{}, err
	}
	navigation, err := secureURL(rawURL)
	if err != nil {
		return AuthNavigation{}, fmt.Errorf("auth session navigation: %w", err)
	}
	host := strings.ToLower(navigation.Hostname())
	if !containsHost(policy.AllowedHosts, host) {
		return AuthNavigation{}, fmt.Errorf("auth session navigation host %q is outside the allowlist for %q", host, policy.Platform)
	}
	return AuthNavigation{Host: host, IsCompletion: containsHost(policy.CompletionHosts, host)}, nil
}

// Complete normalizes a local credential handoff only after the declared
// provider callback. Persistence is deliberately delegated to the platform's
// secure local store, keeping this cross-platform broker stateless.
func (b *AuthSessionBroker) Complete(platform, callbackURL string, credential ClientCredential) (ClientCredential, AuthSessionStatus, error) {
	policy, err := b.policy(platform)
	if err != nil {
		return ClientCredential{}, AuthSessionStatus{}, err
	}
	navigation, err := b.ValidateNavigation(policy.Platform, callbackURL)
	if err != nil {
		return ClientCredential{}, AuthSessionStatus{}, err
	}
	if !navigation.IsCompletion {
		return ClientCredential{}, AuthSessionStatus{}, fmt.Errorf("auth session callback is not a declared completion host for %q", policy.Platform)
	}
	if normalized := strings.ToLower(strings.TrimSpace(credential.Platform)); normalized != policy.Platform {
		return ClientCredential{}, AuthSessionStatus{}, fmt.Errorf("auth session credential platform %q does not match %q", credential.Platform, policy.Platform)
	}
	if strings.TrimSpace(credential.Token) == "" && strings.TrimSpace(credential.Cookie) == "" && strings.TrimSpace(credential.Extra) == "" {
		return ClientCredential{}, AuthSessionStatus{}, fmt.Errorf("auth session credential for %q is empty", policy.Platform)
	}
	credential.Platform = policy.Platform
	credential.Label = strings.TrimSpace(credential.Label)
	return credential, AuthSessionStatus{
		Platform: policy.Platform,
		State:    AuthSessionStateReady,
		Message:  "Локальная сессия готова для создания комнаты.",
	}, nil
}

func (b *AuthSessionBroker) policy(platform string) (AuthProviderPolicy, error) {
	if b == nil {
		return AuthProviderPolicy{}, fmt.Errorf("auth session broker is nil")
	}
	normalized := strings.ToLower(strings.TrimSpace(platform))
	policy, ok := b.policies[normalized]
	if !ok {
		return AuthProviderPolicy{}, fmt.Errorf("auth session provider %q is not configured", platform)
	}
	return policy, nil
}

func secureURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return nil, fmt.Errorf("must be an absolute HTTPS URL")
	}
	return parsed, nil
}

func normalizedHosts(hosts []string) ([]string, error) {
	if len(hosts) == 0 {
		return nil, fmt.Errorf("must not be empty")
	}
	seen := make(map[string]struct{}, len(hosts))
	for _, raw := range hosts {
		host := strings.ToLower(strings.TrimSpace(raw))
		if host == "" || strings.Contains(host, "/") || strings.Contains(host, ":") {
			return nil, fmt.Errorf("invalid host %q", raw)
		}
		seen[host] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for host := range seen {
		out = append(out, host)
	}
	sort.Strings(out)
	return out, nil
}

func containsHost(hosts []string, needle string) bool {
	for _, host := range hosts {
		if host == needle {
			return true
		}
	}
	return false
}
