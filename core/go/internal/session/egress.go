package session

import (
	"context"
	"net"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
)

// CarrierTunnel dials remote egress over carrier-managed chunk/stream
// transports instead of direct TCP to the node.
type CarrierTunnel interface {
	SupportsEndpoint(endpoint carriers.Endpoint) bool
	DialContext(ctx context.Context, endpoint carriers.Endpoint, targetAddr string) (net.Conn, error)
}
