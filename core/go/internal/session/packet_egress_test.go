package session

import (
	"context"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
)

func TestPacketEgressContractCarriesFlowAndAddressMetadata(t *testing.T) {
	endpoint := carriers.Endpoint{ID: "packet-endpoint", Carrier: "file.mailbox"}
	want := PacketMetadata{
		FlowID:          "flow-1",
		SessionID:       "session-1",
		PeerID:          "node-1",
		SourceAddr:      "127.0.0.1:41000",
		DestinationAddr: "dns.example:53",
		ExpiresAt:       time.Now().Add(time.Minute).Round(time.Second),
	}
	fake := &recordingPacketEgress{}
	conn, err := fake.OpenPacketConn(context.Background(), endpoint, want)
	if err != nil {
		t.Fatalf("OpenPacketConn: %v", err)
	}
	defer conn.Close()
	if !reflect.DeepEqual(fake.endpoint, endpoint) || fake.metadata != want {
		t.Fatalf("packet metadata = endpoint=%+v metadata=%+v, want endpoint=%+v metadata=%+v", fake.endpoint, fake.metadata, endpoint, want)
	}
}

type recordingPacketEgress struct {
	endpoint carriers.Endpoint
	metadata PacketMetadata
}

func (r *recordingPacketEgress) SupportsPacketEndpoint(endpoint carriers.Endpoint) bool {
	return endpoint.Carrier == "file.mailbox"
}

func (r *recordingPacketEgress) OpenPacketConn(_ context.Context, endpoint carriers.Endpoint, metadata PacketMetadata) (net.PacketConn, error) {
	r.endpoint = endpoint
	r.metadata = metadata
	return &contractPacketConn{}, nil
}

type contractPacketConn struct{}

func (*contractPacketConn) ReadFrom([]byte) (int, net.Addr, error)          { return 0, nil, net.ErrClosed }
func (*contractPacketConn) WriteTo(payload []byte, _ net.Addr) (int, error) { return len(payload), nil }
func (*contractPacketConn) Close() error                                    { return nil }
func (*contractPacketConn) LocalAddr() net.Addr                             { return contractPacketAddr{} }
func (*contractPacketConn) SetDeadline(time.Time) error                     { return nil }
func (*contractPacketConn) SetReadDeadline(time.Time) error                 { return nil }
func (*contractPacketConn) SetWriteDeadline(time.Time) error                { return nil }

type contractPacketAddr struct{}

func (contractPacketAddr) Network() string { return "packet" }
func (contractPacketAddr) String() string  { return "packet://contract" }
