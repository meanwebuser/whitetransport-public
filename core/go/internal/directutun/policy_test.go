package directutun

import (
	"context"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/pkg/runtimeapi"
)

type recordingExecutor struct {
	calls int
	plans []RoutePlan
	err   error
}

func (r *recordingExecutor) Execute(_ context.Context, plan RoutePlan) error {
	r.calls++
	r.plans = append(r.plans, plan)
	return r.err
}

func validProfile(now time.Time) runtimeapi.SystemVPNProfile {
	profile := runtimeapi.SystemVPNProfile{
		SchemaRevision:        runtimeapi.SystemVPNProfileSchemaRevision,
		DaemonInstanceID:      "daemon-1",
		ProfileRevision:       1,
		SessionID:             "session-1",
		SelectedNodeID:        "node-1",
		Ready:                 true,
		IssuedAt:              now.Add(-time.Minute),
		ExpiresAt:             now.Add(time.Minute),
		SocksListen:           "127.0.0.1:1080",
		RouteMode:             runtimeapi.SystemVPNRouteNone,
		CarrierControlOrigins: []string{"https://api.ok.ru", "https://api.vk.com", "wss://egress.example"},
		CarrierControlRoutes: map[string][]string{
			"api.ok.ru": {"203.0.113.10/32"}, "api.vk.com": {"203.0.113.11/32"}, "egress.example": {"203.0.113.12/32"},
		},
		DNSSnapshot: map[string][]string{
			"api.ok.ru": {"203.0.113.10"}, "api.vk.com": {"203.0.113.11"}, "egress.example": {"203.0.113.12"},
		},
		DNSServers: []string{"1.1.1.1"},
		Dependencies: []runtimeapi.SystemVPNDependency{
			{Purpose: runtimeapi.SystemVPNDependencyControl, Carrier: "ok", Scheme: "https", Host: "api.ok.ru", Port: 443, Addresses: []string{"203.0.113.10"}, DNSExpiresAt: now.Add(time.Minute)},
			{Purpose: runtimeapi.SystemVPNDependencyDiscovery, Carrier: "vk", Scheme: "https", Host: "api.vk.com", Port: 443, Addresses: []string{"203.0.113.11"}, DNSExpiresAt: now.Add(time.Minute)},
			{Purpose: runtimeapi.SystemVPNDependencyEgress, Carrier: "wbstream", Scheme: "wss", Host: "egress.example", Port: 443, Addresses: []string{"203.0.113.12"}, DNSExpiresAt: now.Add(time.Minute)},
		},
		MTU:       1500,
		Readiness: runtimeapi.SystemVPNReadiness{Ready: true, Provenance: "runtime/profile"},
	}
	if err := profile.SetHash(); err != nil {
		panic(err)
	}
	return profile
}

func validFacts(now time.Time) VerifiedFacts {
	return VerifiedFacts{
		Caller:          CallerFacts{UID: 501, AuditIdentity: "audit-1", BundleID: "com.meanwebuser.whitetransport", CDHash: "cdhash-1"},
		ConsoleUID:      501,
		InstalledCDHash: "cdhash-1",
		InstalledBundle: "com.meanwebuser.whitetransport",
		UtunName:        "utun0",
		Authorization: AuthorizationFacts{
			Right:         AuthorizationRight,
			AuditIdentity: "audit-1",
			IssuedAt:      now.Add(-time.Second),
			ExpiresAt:     now.Add(time.Minute),
		},
	}
}

func profileIdentity(profile runtimeapi.SystemVPNProfile) ProfileIdentity {
	return ProfileIdentity{DaemonInstanceID: profile.DaemonInstanceID, ProfileRevision: profile.ProfileRevision, SessionID: profile.SessionID, SelectedNodeID: profile.SelectedNodeID, ProfileHash: profile.ProfileHash}
}

func validStart(now time.Time, profile runtimeapi.SystemVPNProfile) StartRequest {
	return StartRequest{Request: Request{Version: ProtocolVersion, Operation: OperationStart, RequestID: "request-1", Deadline: now.Add(time.Second), Nonce: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, Profile: profile}
}

func TestPolicyRejectsHostileStartBeforeExecutor(t *testing.T) {
	now := time.Date(2026, 7, 20, 17, 0, 0, 0, time.UTC)
	profile := runtimeapi.SystemVPNProfile{
		SchemaRevision:   runtimeapi.SystemVPNProfileSchemaRevision,
		DaemonInstanceID: "daemon-1",
		ProfileRevision:  1,
		SessionID:        "session-1",
		SelectedNodeID:   "node-1",
		Ready:            true,
		IssuedAt:         now.Add(-time.Minute),
		ExpiresAt:        now.Add(time.Minute),
		SocksListen:      "127.0.0.1:1080;touch /tmp/pwned",
		RouteMode:        runtimeapi.SystemVPNRouteNone,
		LANAccess:        false,
		MTU:              1500,
		Readiness:        runtimeapi.SystemVPNReadiness{Ready: true, Provenance: "runtime/profile"},
	}
	if err := profile.SetHash(); err != nil {
		t.Fatalf("set profile hash: %v", err)
	}

	executor := &recordingExecutor{}
	policy := NewPolicy(PolicyConfig{Now: func() time.Time { return now }})
	request := StartRequest{
		Request: Request{
			Version:   ProtocolVersion,
			Operation: OperationStart,
			RequestID: "request-1",
			Deadline:  now.Add(time.Second),
			Nonce:     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		Profile: profile,
	}
	facts := VerifiedFacts{
		Caller:          CallerFacts{UID: 501, AuditIdentity: "audit-1", BundleID: "com.meanwebuser.whitetransport", CDHash: "cdhash-1"},
		ConsoleUID:      501,
		InstalledCDHash: "cdhash-1",
		Authorization: AuthorizationFacts{
			Right:         "com.meanwebuser.whitetransport.direct-utun",
			AuditIdentity: "audit-1",
			IssuedAt:      now.Add(-time.Second),
			ExpiresAt:     now.Add(time.Minute),
		},
	}
	if _, err := policy.Start(context.Background(), request, facts, executor); err == nil {
		t.Fatal("hostile start unexpectedly accepted")
	}
	if executor.calls != 0 {
		t.Fatalf("hostile start reached executor %d times", executor.calls)
	}
}

func TestPolicyRejectsProfileNotMatchingAuthoritativeIdentity(t *testing.T) {
	now := time.Date(2026, 7, 20, 17, 0, 0, 0, time.UTC)
	original := validProfile(now)
	facts := validFacts(now)
	facts.AuthoritativeProfile = profileIdentity(original)
	request := validStart(now, original)
	request.Profile.DestinationCIDRs = []string{"203.0.113.0/24"}
	request.Profile.RouteMode = runtimeapi.SystemVPNRouteOnly
	if err := request.Profile.SetHash(); err != nil {
		t.Fatalf("rehash altered request profile: %v", err)
	}
	executor := &recordingExecutor{}
	if _, err := NewPolicy(PolicyConfig{Now: func() time.Time { return now }}).Start(context.Background(), request, facts, executor); err == nil {
		t.Fatal("altered and rehashed profile unexpectedly accepted")
	}
	if executor.calls != 0 {
		t.Fatalf("altered profile reached executor %d times", executor.calls)
	}
}

func TestPolicyRejectsHostileInputsBeforeExecutor(t *testing.T) {
	now := time.Date(2026, 7, 20, 17, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*StartRequest, *VerifiedFacts)
	}{
		{name: "stale profile", mutate: func(request *StartRequest, _ *VerifiedFacts) {
			request.Profile.IssuedAt = now.Add(-10 * time.Minute)
			request.Profile.ExpiresAt = now.Add(-5 * time.Minute)
		}},
		{name: "forged profile hash", mutate: func(request *StartRequest, _ *VerifiedFacts) { request.Profile.ProfileHash = "forged" }},
		{name: "bad MTU", mutate: func(request *StartRequest, _ *VerifiedFacts) { request.Profile.MTU = 500 }},
		{name: "non-loopback SOCKS", mutate: func(request *StartRequest, _ *VerifiedFacts) { request.Profile.SocksListen = "192.0.2.1:1080" }},
		{name: "noncanonical CIDR", mutate: func(request *StartRequest, _ *VerifiedFacts) {
			request.Profile.RouteMode = runtimeapi.SystemVPNRouteOnly
			request.Profile.DestinationCIDRs = []string{"203.0.113.1/24"}
		}},
		{name: "expired authorization", mutate: func(_ *StartRequest, facts *VerifiedFacts) { facts.Authorization.ExpiresAt = now.Add(-time.Second) }},
		{name: "mismatched audit identity", mutate: func(_ *StartRequest, facts *VerifiedFacts) { facts.Authorization.AuditIdentity = "other-audit" }},
		{name: "forged utun identity", mutate: func(_ *StartRequest, facts *VerifiedFacts) { facts.UtunName = "utun0;touch /tmp/pwned" }},
		{name: "unknown operation", mutate: func(request *StartRequest, _ *VerifiedFacts) { request.Request.Operation = Operation("exec") }},
		{name: "user supplied default route", mutate: func(request *StartRequest, facts *VerifiedFacts) {
			request.Profile.UserBypassCIDRs = []string{"0.0.0.0/0"}
			if err := request.Profile.SetHash(); err != nil {
				panic(err)
			}
			facts.AuthoritativeProfile = profileIdentity(request.Profile)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validStart(now, validProfile(now))
			facts := validFacts(now)
			facts.AuthoritativeProfile = profileIdentity(request.Profile)
			test.mutate(&request, &facts)
			executor := &recordingExecutor{}
			policy := NewPolicy(PolicyConfig{Now: func() time.Time { return now }})
			if _, err := policy.Start(context.Background(), request, facts, executor); err == nil {
				t.Fatal("hostile request unexpectedly accepted")
			}
			if executor.calls != 0 {
				t.Fatalf("hostile request reached executor %d times", executor.calls)
			}
		})
	}
}

func TestPolicyRejectsSecondLeaseAndNonceReplay(t *testing.T) {
	now := time.Date(2026, 7, 20, 17, 0, 0, 0, time.UTC)
	policy := NewPolicy(PolicyConfig{Now: func() time.Time { return now }})
	facts := validFacts(now)
	executor := &recordingExecutor{}
	first := validStart(now, validProfile(now))
	facts.AuthoritativeProfile = profileIdentity(first.Profile)
	if _, err := policy.Start(context.Background(), first, facts, executor); err != nil {
		t.Fatalf("first start: %v", err)
	}
	second := validStart(now, validProfile(now))
	second.Request.RequestID = "request-2"
	if _, err := policy.Start(context.Background(), second, facts, executor); err == nil {
		t.Fatal("second active lease unexpectedly accepted")
	}
	if executor.calls != 1 {
		t.Fatalf("second lease reached executor %d times", executor.calls)
	}
}

func TestPolicyUsesSplitDefaultRoutesForFullTunnel(t *testing.T) {
	now := time.Date(2026, 7, 20, 17, 0, 0, 0, time.UTC)
	profile := validProfile(now)
	facts := validFacts(now)
	facts.AuthoritativeProfile = profileIdentity(profile)
	executor := &recordingExecutor{}
	if _, err := NewPolicy(PolicyConfig{Now: func() time.Time { return now }}).Start(context.Background(), validStart(now, profile), facts, executor); err != nil {
		t.Fatalf("full-tunnel start: %v", err)
	}
	if len(executor.plans) != 1 {
		t.Fatalf("executor plans = %d, want 1", len(executor.plans))
	}
	want := []string{"0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1"}
	if got := executor.plans[0].IncludedCIDRs; len(got) != len(want) || !equalStrings(got, want) {
		t.Fatalf("full-tunnel included routes = %#v, want %#v", got, want)
	}
}

func TestPolicyRetainsReplayProtectionAfterExecutorFailure(t *testing.T) {
	now := time.Date(2026, 7, 20, 17, 0, 0, 0, time.UTC)
	profile := validProfile(now)
	facts := validFacts(now)
	facts.AuthoritativeProfile = profileIdentity(profile)
	executor := &recordingExecutor{err: context.Canceled}
	policy := NewPolicy(PolicyConfig{Now: func() time.Time { return now }})
	first := validStart(now, profile)
	if _, err := policy.Start(context.Background(), first, facts, executor); err == nil {
		t.Fatal("executor failure unexpectedly returned success")
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls after failed start = %d, want 1", executor.calls)
	}
	if _, err := policy.Start(context.Background(), first, facts, executor); err == nil {
		t.Fatal("replayed failed request unexpectedly accepted")
	}
	duplicateID := validStart(now, profile)
	duplicateID.Request.Nonce = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	if _, err := policy.Start(context.Background(), duplicateID, facts, executor); err == nil {
		t.Fatal("duplicate request ID with fresh nonce unexpectedly accepted")
	}
	if executor.calls != 1 {
		t.Fatalf("duplicate request ID reached executor %d times", executor.calls)
	}
	second := validStart(now, profile)
	second.Request.RequestID = "request-2"
	second.Request.Nonce = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	executor.err = nil
	if _, err := policy.Start(context.Background(), second, facts, executor); err != nil {
		t.Fatalf("fresh request after rollback: %v", err)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
