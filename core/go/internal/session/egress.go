package session

import (
	"context"
	"errors"
	"net"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

const PayloadEgressStreamFailure = "session.egress.stream_failed"

var ErrEgressReturnCarrier = errors.New("node could not send stream data through the carrier")

// CarrierFailureError marks a failure of the transport/provider itself rather
// than cancellation, timeout, or refusal by one target address.
type CarrierFailureError struct{ Err error }

func (e *CarrierFailureError) Error() string { return e.Err.Error() }
func (e *CarrierFailureError) Unwrap() error { return e.Err }

func NewCarrierFailure(err error) error {
	if err == nil {
		return nil
	}
	return &CarrierFailureError{Err: err}
}

func IsCarrierFailure(err error) bool {
	var target *CarrierFailureError
	return errors.As(err, &target)
}

// CarrierTunnel dials remote egress over carrier-managed chunk/stream
// transports instead of direct TCP to the node.
type CarrierTunnel interface {
	SupportsEndpoint(endpoint carriers.Endpoint) bool
	DialContext(ctx context.Context, endpoint carriers.Endpoint, targetAddr string) (net.Conn, error)
}

// SessionCarrierTunnel preserves the authenticated session identity on every
// stream envelope so concurrent sessions can select independent ciphers.
type SessionCarrierTunnel interface {
	DialContextForSession(ctx context.Context, sessionID string, endpoint carriers.Endpoint, targetAddr string) (net.Conn, error)
}

// EgressStreamFailureControl relays a session-sealed failure over the existing
// client reply path when the return data carrier cannot deliver a close.
type EgressStreamFailureControl interface {
	SetEgressFailureReply(sessionID string, carrier carriers.Carrier, endpoint carriers.Endpoint)
	HandleEgressStreamFailure(envelope fabric.Envelope)
}
