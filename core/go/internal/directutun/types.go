// Package directutun contains the platform-neutral policy boundary for the
// privileged direct-utun helper. It plans typed mutations only; Darwin code
// is responsible for obtaining the verified facts and executing the plan.
package directutun

import (
	"context"
	"time"

	"github.com/meanwebuser/whitetransport/core/pkg/runtimeapi"
)

const (
	ProtocolVersion     = "direct-utun.v1"
	AuthorizationRight  = "com.meanwebuser.whitetransport.direct-utun"
	MaxRequestLifetime  = 30 * time.Second
	MaxAuthorizationAge = 5 * time.Minute
	NonceBytes          = 32
)

// Operation is the fixed helper protocol operation set.
type Operation string

const (
	OperationHello     Operation = "hello"
	OperationStart     Operation = "start"
	OperationStop      Operation = "stop"
	OperationStatus    Operation = "status"
	OperationLogs      Operation = "logs"
	OperationReconcile Operation = "reconcile"
)

// Request contains only protocol metadata. Caller provenance is deliberately
// supplied separately as VerifiedFacts by the XPC boundary.
type Request struct {
	Version   string
	Operation Operation
	RequestID string
	Deadline  time.Time
	Nonce     string
}

// StartRequest is the sole input that can create a route lease.
type StartRequest struct {
	Request Request
	Profile runtimeapi.SystemVPNProfile
}

// CallerFacts are obtained from the operating-system audit token, never from
// fields supplied by the GUI request.
type CallerFacts struct {
	UID           uint32
	AuditIdentity string
	BundleID      string
	CDHash        string
}

// AuthorizationFacts are obtained by validating a fresh Authorization
// Services external form at the helper boundary.
type AuthorizationFacts struct {
	Right         string
	AuditIdentity string
	IssuedAt      time.Time
	ExpiresAt     time.Time
}

// VerifiedFacts are trusted boundary facts used for policy validation. The
// helper derives UtunName from its descriptor and does not accept it in a
// request body.
type VerifiedFacts struct {
	Caller               CallerFacts
	ConsoleUID           uint32
	InstalledCDHash      string
	InstalledBundle      string
	Authorization        AuthorizationFacts
	UtunName             string
	AuthoritativeProfile ProfileIdentity
}

// ProfileIdentity is the helper-derived identity of the immutable runtime
// profile. It is never accepted from the GUI request body.
type ProfileIdentity struct {
	DaemonInstanceID string
	ProfileRevision  uint64
	SessionID        string
	SelectedNodeID   string
	ProfileHash      string
}

// Lease is returned only after a start plan has been accepted by the executor.
// Capability is opaque and must not be logged or exposed in status responses.
type Lease struct {
	Capability    string
	Generation    uint64
	ProfileHash   string
	AuditIdentity string
	UID           uint32
}

// RoutePlan is a side-effect-free, canonical route mutation description.
// It contains no shell fragments or executable paths.
type RoutePlan struct {
	Operation     Operation
	RequestID     string
	ProfileHash   string
	UtunName      string
	SocksListen   string
	IncludedCIDRs []string
	ExcludedCIDRs []string
	MTU           int
}

// Executor is implemented by the platform helper. Implementations must only
// consume typed plans and must not infer commands from strings in the plan.
type Executor interface {
	Execute(context.Context, RoutePlan) error
}

// PolicyConfig controls expected installed identity and the injected clock.
type PolicyConfig struct {
	Now             func() time.Time
	ConsoleUID      uint32
	InstalledCDHash string
	InstalledBundle string
}
