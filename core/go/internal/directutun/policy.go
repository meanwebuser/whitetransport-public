package directutun

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/meanwebuser/whitetransport/core/pkg/runtimeapi"
)

// Policy validates helper requests and reserves the single direct-utun lease.
// It performs no host I/O; the executor is called only after every check has
// passed and receives a canonical typed RoutePlan.
type Policy struct {
	mu         sync.Mutex
	config     PolicyConfig
	active     bool
	generation uint64
	nonces     map[string]time.Time
	requestIDs map[string]time.Time
}

// NewPolicy constructs a fail-closed policy. A nil clock uses UTC wall time.
func NewPolicy(config PolicyConfig) *Policy {
	if config.Now == nil {
		config.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Policy{config: config, nonces: make(map[string]time.Time), requestIDs: make(map[string]time.Time)}
}

// Start validates and executes one direct-utun start plan. A second active
// lease is rejected before the executor is called.
func (p *Policy) Start(ctx context.Context, request StartRequest, facts VerifiedFacts, executor Executor) (Lease, error) {
	if p == nil {
		return Lease{}, errors.New("direct-utun policy is nil")
	}
	if executor == nil {
		return Lease{}, errors.New("direct-utun executor is nil")
	}
	now := p.config.Now().UTC()
	if err := validateRequest(request.Request, OperationStart, now); err != nil {
		return Lease{}, err
	}
	if err := p.validateFacts(facts, now); err != nil {
		return Lease{}, err
	}
	if err := request.Profile.Validate(now); err != nil {
		return Lease{}, fmt.Errorf("direct-utun profile rejected: %w", err)
	}
	if err := validateProfileIdentity(request.Profile, facts.AuthoritativeProfile); err != nil {
		return Lease{}, err
	}
	plan, err := buildRoutePlan(request, facts)
	if err != nil {
		return Lease{}, err
	}

	capability, err := newCapability()
	if err != nil {
		return Lease{}, err
	}
	p.mu.Lock()
	if p.active {
		p.mu.Unlock()
		return Lease{}, errors.New("direct-utun lease already active")
	}
	if _, seen := p.nonces[request.Request.Nonce]; seen {
		p.mu.Unlock()
		return Lease{}, errors.New("direct-utun request nonce replayed")
	}
	if _, seen := p.requestIDs[request.Request.RequestID]; seen {
		p.mu.Unlock()
		return Lease{}, errors.New("direct-utun request ID replayed")
	}
	p.nonces[request.Request.Nonce] = now
	p.requestIDs[request.Request.RequestID] = now
	p.generation++
	generation := p.generation
	p.active = true
	p.mu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	if err := executor.Execute(ctx, plan); err != nil {
		p.mu.Lock()
		p.active = false
		p.mu.Unlock()
		return Lease{}, fmt.Errorf("direct-utun executor rejected plan: %w", err)
	}
	return Lease{Capability: capability, Generation: generation, ProfileHash: request.Profile.ProfileHash, AuditIdentity: facts.Caller.AuditIdentity, UID: facts.Caller.UID}, nil
}

func validateProfileIdentity(profile runtimeapi.SystemVPNProfile, identity ProfileIdentity) error {
	if strings.TrimSpace(identity.DaemonInstanceID) == "" || identity.ProfileRevision == 0 || strings.TrimSpace(identity.SessionID) == "" || strings.TrimSpace(identity.SelectedNodeID) == "" || strings.TrimSpace(identity.ProfileHash) == "" {
		return errors.New("direct-utun authoritative profile identity is incomplete")
	}
	if profile.DaemonInstanceID != identity.DaemonInstanceID || profile.ProfileRevision != identity.ProfileRevision || profile.SessionID != identity.SessionID || profile.SelectedNodeID != identity.SelectedNodeID || !strings.EqualFold(profile.ProfileHash, identity.ProfileHash) {
		return errors.New("direct-utun profile does not match authoritative helper identity")
	}
	return nil
}

func (p *Policy) validateFacts(facts VerifiedFacts, now time.Time) error {
	caller := facts.Caller
	if caller.UID == 0 || strings.TrimSpace(caller.AuditIdentity) == "" || strings.TrimSpace(caller.BundleID) == "" || strings.TrimSpace(caller.CDHash) == "" {
		return errors.New("direct-utun caller facts are incomplete")
	}
	if facts.ConsoleUID == 0 || caller.UID != facts.ConsoleUID {
		return errors.New("direct-utun caller is not the active console user")
	}
	if p.config.ConsoleUID != 0 && caller.UID != p.config.ConsoleUID {
		return errors.New("direct-utun caller UID does not match installed policy")
	}
	if facts.InstalledCDHash == "" || caller.CDHash != facts.InstalledCDHash || (p.config.InstalledCDHash != "" && caller.CDHash != p.config.InstalledCDHash) {
		return errors.New("direct-utun caller CDHash is not the installed application")
	}
	if facts.InstalledBundle == "" || caller.BundleID != facts.InstalledBundle || (p.config.InstalledBundle != "" && caller.BundleID != p.config.InstalledBundle) {
		return errors.New("direct-utun caller bundle is not the installed application")
	}
	auth := facts.Authorization
	if auth.Right != AuthorizationRight || auth.AuditIdentity != caller.AuditIdentity {
		return errors.New("direct-utun authorization identity is invalid")
	}
	if auth.IssuedAt.IsZero() || auth.ExpiresAt.IsZero() || auth.IssuedAt.After(now) || !auth.ExpiresAt.After(now) || !auth.ExpiresAt.After(auth.IssuedAt) || auth.ExpiresAt.Sub(auth.IssuedAt) > MaxAuthorizationAge {
		return errors.New("direct-utun authorization is stale")
	}
	if err := validateUtunName(facts.UtunName); err != nil {
		return err
	}
	return nil
}

func validateRequest(request Request, operation Operation, now time.Time) error {
	if request.Version != ProtocolVersion || request.Operation != operation {
		return errors.New("direct-utun protocol operation is unsupported")
	}
	if strings.TrimSpace(request.RequestID) == "" || len(request.RequestID) > 128 {
		return errors.New("direct-utun request ID is invalid")
	}
	if request.Deadline.IsZero() || request.Deadline.Before(now) || request.Deadline.After(now.Add(MaxRequestLifetime)) {
		return errors.New("direct-utun request deadline is invalid")
	}
	decoded, err := hex.DecodeString(request.Nonce)
	if err != nil || len(decoded) != NonceBytes || len(request.Nonce) != NonceBytes*2 {
		return errors.New("direct-utun request nonce is invalid")
	}
	return nil
}

func buildRoutePlan(request StartRequest, facts VerifiedFacts) (RoutePlan, error) {
	profile := request.Profile
	destinations, err := canonicalCIDRs("destination_cidrs", profile.DestinationCIDRs, true)
	if err != nil {
		return RoutePlan{}, err
	}
	userBypass, err := canonicalCIDRs("user_bypass_cidrs", profile.UserBypassCIDRs, true)
	if err != nil {
		return RoutePlan{}, err
	}
	for _, value := range destinations {
		prefix := netip.MustParsePrefix(value)
		if prefix.Bits() == 0 {
			return RoutePlan{}, fmt.Errorf("destination_cidrs contains default route %q", value)
		}
	}
	// Direct mode always keeps the documented private/link-local ranges out of
	// the tunnel. Explicit profile values are additive, never a replacement.
	userBypass = mergeCIDRs(userBypass, runtimeapi.LANBypassCIDRs())
	excluded := append([]string(nil), userBypass...)
	for _, routes := range profile.CarrierControlRoutes {
		canonical, err := canonicalCIDRs("carrier_control_routes", routes, false)
		if err != nil {
			return RoutePlan{}, err
		}
		excluded = append(excluded, canonical...)
	}
	excluded = mergeCIDRs(nil, excluded)
	included := destinations
	if profile.RouteMode == runtimeapi.SystemVPNRouteNone {
		included = []string{"0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1"}
	}
	return RoutePlan{Operation: OperationStart, RequestID: request.Request.RequestID, ProfileHash: profile.ProfileHash, UtunName: facts.UtunName, SocksListen: profile.SocksListen, IncludedCIDRs: included, ExcludedCIDRs: excluded, MTU: profile.MTU}, nil
}

func canonicalCIDRs(name string, values []string, rejectDefault bool) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		trimmed := strings.TrimSpace(raw)
		prefix, err := netip.ParsePrefix(trimmed)
		if err != nil || prefix.String() != trimmed {
			return nil, fmt.Errorf("%s contains noncanonical CIDR %q", name, raw)
		}
		if rejectDefault && prefix.Bits() == 0 {
			return nil, fmt.Errorf("%s contains default route %q", name, raw)
		}
		if _, exists := seen[trimmed]; exists {
			return nil, fmt.Errorf("%s contains duplicate CIDR %q", name, raw)
		}
		seen[trimmed] = struct{}{}
		result = append(result, trimmed)
	}
	sort.Strings(result)
	return result, nil
}

func mergeCIDRs(base, additional []string) []string {
	seen := make(map[string]struct{}, len(base)+len(additional))
	result := make([]string, 0, len(base)+len(additional))
	for _, values := range [][]string{base, additional} {
		for _, value := range values {
			if _, exists := seen[value]; exists {
				continue
			}
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func validateUtunName(value string) error {
	if len(value) < len("utun1") || !strings.HasPrefix(value, "utun") {
		return errors.New("direct-utun interface identity is invalid")
	}
	for _, runeValue := range value[len("utun"):] {
		if !unicode.IsDigit(runeValue) {
			return errors.New("direct-utun interface identity is invalid")
		}
	}
	return nil
}

func newCapability() (string, error) {
	var value [NonceBytes]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate direct-utun lease capability: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}
