package tunnel

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/provider"
)

func TestCarrierTunnelRoutesSharedProviderResponses(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	prov := &responseQueueProvider{
		receiving: make(chan struct{}),
		opened:    make(chan string, 2),
		responses: make(chan []byte),
	}
	ep := carriers.Endpoint{ID: "telemost-test", Carrier: prov.ID(), Address: "room://test"}
	carrier, err := carriers.NewProviderCarrier(prov, ep)
	if err != nil {
		t.Fatal(err)
	}
	client := NewCarrierTunnel("client", map[string]policy.CarrierBinding{
		ep.Carrier: {Carrier: carrier, Endpoint: ep},
	})
	defer client.CloseAll()
	first, err := client.DialContext(ctx, ep, "first.example:443")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	firstID := <-prov.opened
	select {
	case <-prov.receiving:
	case <-ctx.Done():
		t.Fatal("first stream did not start receiving")
	}

	second, err := client.DialContext(ctx, ep, "second.example:443")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	secondID := <-prov.opened
	// The first physical Receive is already waiting. Give it the second
	// stream's response, then verify that both streams get their own data.
	for _, response := range []struct{ id, payload string }{
		{secondID, "second response"},
		{firstID, "first response"},
	} {
		frame := fabric.NewEnvelope(response.id, fabric.TrafficEgress, tunnelData, []byte(response.payload))
		frame.Source, frame.Sequence = "exit", 1
		data, err := carriers.MarshalEnvelope(frame)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case prov.responses <- data:
		case <-ctx.Done():
			t.Fatal("provider response was not consumed")
		}
	}

	for _, stream := range []struct {
		reader io.Reader
		want   string
	}{
		{second, "second response"},
		{first, "first response"},
	} {
		got := make([]byte, len(stream.want))
		if _, err := io.ReadFull(stream.reader, got); err != nil || string(got) != stream.want {
			t.Fatalf("response = %q, %v, want %q", got, err, stream.want)
		}
	}
}

type responseQueueProvider struct {
	provider.Provider
	firstRead sync.Once
	receiving chan struct{}
	opened    chan string
	responses chan []byte
}

func (*responseQueueProvider) ID() string                  { return "telemost" }
func (*responseQueueProvider) Category() provider.Category { return provider.CategoryVideo }
func (*responseQueueProvider) GetLimits() provider.Limits {
	return provider.Limits{MaxPayloadBytes: 32768}
}
func (p *responseQueueProvider) Send(ctx context.Context, payload []byte) error {
	env, err := carriers.ParseEnvelope(payload)
	if err != nil {
		return err
	}
	if env.PayloadType == tunnelOpen {
		p.opened <- env.ID
	}
	return nil
}
func (p *responseQueueProvider) Receive(ctx context.Context) ([]byte, error) {
	p.firstRead.Do(func() { close(p.receiving) })
	select {
	case data := <-p.responses:
		return data, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
