package session

import (
	"context"
	"net"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
)

// PacketMetadata identifies one UDP association inside an active WhiteTransport
// session. Address fields are serialized into packet envelopes so the exit node
// can preserve the original source and destination without exposing a direct
// client-side network dial path.
type PacketMetadata struct {
	FlowID          string
	SessionID       string
	PeerID          string
	SourceAddr      string
	DestinationAddr string
	ExpiresAt       time.Time
}

// PacketEgress opens carrier-backed datagram associations. Implementations must
// return a net.PacketConn whose reads and writes stay inside the selected
// encrypted session; callers must not fall back to net.ListenPacket/net.DialUDP
// on the client side.
type PacketEgress interface {
	SupportsPacketEndpoint(endpoint carriers.Endpoint) bool
	OpenPacketConn(ctx context.Context, endpoint carriers.Endpoint, metadata PacketMetadata) (net.PacketConn, error)
}

// PacketSessionLifecycle binds packet associations to an authenticated peer
// and closes every association before the owning session cipher is cleared.
type PacketSessionLifecycle interface {
	SetPacketSession(sessionID string, peerID string, expiresAt time.Time)
	ClosePacketSession(sessionID string)
}

// PacketSourceUpdater records the real local UDP source selected after a
// wildcard SOCKS UDP ASSOCIATE request. Implementations must serialize this
// value only inside the encrypted packet envelope.
type PacketSourceUpdater interface {
	SetPacketSource(sourceAddr string)
}
