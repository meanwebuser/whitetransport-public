# Authoritative System VPN Profile Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use test-driven development for each contract change. Keep this core-only block unstaged until the root agent reviews it.

**Goal:** Publish one revisioned, session-scoped `system_vpn_profile` in `runtimeapi.Status` only when the client has complete, validated inputs for a macOS system-VPN configuration.

**Architecture:** Keep public wire types in `core/go/pkg/runtimeapi`, internal status projection in `core/go/internal/api`, and profile derivation in `core/go/internal/runtime` beside the authoritative `ControlPlane` state. The builder receives an injected clock and resolver, normalizes configured route modes/CIDRs, derives only exact enabled carrier/control origins, and returns either a complete immutable profile or a redacted readiness reason; `statusLocked` publishes the profile atomically with the rest of one status snapshot.

**Tech Stack:** Go 1.24, `net/url`, `net/netip`, `net` resolver abstraction, JSON runtime API, table-driven unit tests, `go test -race`.

## Global Constraints

- Touch only `core/go` and this plan/worklog/lock; do not edit `apps/native-gui` or Android.
- Do not publish a profile for a disconnected/non-client session or when any required input is missing or invalid.
- Use user-facing route modes `none`, `bypass`, and `only`; do not carry package IDs into the daemon/macOS profile.
- `lan_access` is explicit. When enabled, derive documented private/link-local IPv4 and IPv6 CIDRs as user bypass routes; mandatory carrier/control host routes remain a separate always-excluded set.
- Never copy credentials, query strings, fragments, userinfo, or secret paths into endpoint origins or readiness messages.
- Every required hostname must resolve to exact `/32` or `/128` routes in the same immutable snapshot; resolver and clock are injectable for deterministic tests.
- `daemon_instance_id` is random per process; `profile_revision` is monotonic across listener, connect, disconnect, reconnect, failover, and dependency changes. `profile_hash` covers the canonical payload without the hash field.
- The profile carries a typed dependency set (`purpose`, `carrier`, `scheme`, `host`, `port`, `addresses`, `dns_expires_at`) for discovery, control, egress, and configured failover routes; unresolved dynamic providers fail closed.

## Task 1: Lock the wire model and validation contract

**Files:**
- Create: `core/go/pkg/runtimeapi/system_vpn_profile.go`
- Modify: `core/go/pkg/runtimeapi/models.go`
- Test: `core/go/pkg/runtimeapi/system_vpn_profile_test.go`

- [ ] **Step 1: Write RED tests** for JSON field names, required schema/session/generation/timestamps, route mode and CIDR normalization, secret-safe origins, explicit LAN routes, readiness provenance/reason, and disconnected absence represented by `nil`.
- [ ] **Step 2: Run** `cd core/go && go test ./pkg/runtimeapi -run 'SystemVPN|VPNProfile' -count=1`; record the missing type/field failure.
- [ ] **Step 3: Add** immutable public `SystemVPNProfile`, `SystemVPNReadiness`, and typed dependency/DNS snapshot types with JSON tags and validation helpers; keep `Status.SystemVPNProfile` and `Status.SystemVPNProfileReadiness` optional pointers so omission is atomic and explicit.
- [ ] **Step 4: Run** the focused package tests until the wire/validation tests pass.

## Task 2: Add routing configuration and deterministic profile builder

**Files:**
- Modify: `core/go/internal/config/config.go`
- Modify: `core/go/internal/config/defaults.go`
- Test: `core/go/internal/config/config_test.go`
- Create: `core/go/internal/runtime/system_vpn_profile.go`
- Test: `core/go/internal/runtime/system_vpn_profile_test.go`

- [ ] **Step 1: Write RED tests** for `full_tunnel`/`destination_split` model validation, route modes `none|bypass|only`, destination route requirements, LAN route expansion, loopback/nonzero SOCKS validation, endpoint userinfo/query/fragment/secret-path rejection, IPv4+IPv6 resolver output, mixed invalid endpoint omission, and bounded clock timestamps.
- [ ] **Step 2: Run** `cd core/go && go test ./internal/config ./internal/runtime -run 'SystemVPN|VPNProfile|Routing' -count=1`; confirm failures are due to absent model/builder behavior.
- [ ] **Step 3: Implement** typed config fields/defaults and `BuildSystemVPNProfile` with explicit `Clock` and `Resolver` interfaces. Use known provider defaults only for enabled bindings whose exact config lacks an explicit API origin; return a redacted readiness reason for unsupported/ambiguous providers rather than guessing.
- [ ] **Step 4: Run** focused config/runtime tests and a race repeat; refactor only after green.

## Task 3: Publish atomically through runtime status and API

**Files:**
- Modify: `core/go/internal/runtime/control.go`
- Modify: `core/go/internal/api/runtime_status_response.go`
- Modify: `core/go/pkg/runtimeapi/models.go`
- Modify: `core/go/internal/api/runtime_status_contract_test.go`
- Test: `core/go/internal/runtime/control_test.go`

- [ ] **Step 1: Write RED tests** proving connected-client status contains one complete profile, disconnected/node status omits it, profile generation/session changes across reconnects, stale profiles are not retained, and API JSON contains the profile as one status snapshot with no secrets.
- [ ] **Step 2: Run** `cd core/go && go test ./internal/api ./internal/runtime -run 'SystemVPN|VPNProfile|Status' -count=1`; capture the RED result.
- [ ] **Step 3: Integrate** the builder into `ControlPlane` lifecycle and `statusLocked`, incrementing `profile_revision` at session boundaries and clearing the published pointer on disconnect/error. Convert the internal status to the public runtimeapi model without reconstructing fields in the HTTP handler.
- [ ] **Step 4: Run** focused API/runtime tests, `go test -race` on affected packages, then `go test ./... -count=1` and `go vet` for core/go.

## Acceptance checks

- `cd core/go && go test ./pkg/runtimeapi ./internal/config ./internal/runtime ./internal/api -count=1`
- `cd core/go && go test -race ./pkg/runtimeapi ./internal/config ./internal/runtime ./internal/api -count=1`
- `cd core/go && go vet ./pkg/runtimeapi ./internal/config ./internal/runtime ./internal/api`
- `git diff --check` and secret scan before root stages anything.
- Mac-native profile consumption remains a separate downstream proof; this block does not claim Network Extension readiness.
