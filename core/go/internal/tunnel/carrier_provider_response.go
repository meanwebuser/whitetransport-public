package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
)

type providerStreamResponse struct {
	id     string
	reader *packetClientReader
	frames chan fabric.Envelope
	abort  func(error)
}

func usesProviderStreamReader(binding policy.CarrierBinding) bool {
	_, provider := binding.Carrier.(*carriers.ProviderCarrier)
	return provider && binding.Carrier.Descriptor().Mode == carriers.DeliveryStream
}

func (t *CarrierTunnel) registerProviderStream(binding policy.CarrierBinding, id string, abort func(error)) *providerStreamResponse {
	t.mu.Lock()
	reader, start := t.ensurePacketReaderLocked(binding)
	stream := &providerStreamResponse{
		id: id, reader: reader, frames: make(chan fabric.Envelope, 32), abort: abort,
	}
	if reader.streams == nil {
		reader.streams = make(map[string]*providerStreamResponse)
	}
	reader.streams[id] = stream
	t.mu.Unlock()
	if start {
		go t.packetReadLoop(reader)
	}
	return stream
}

func (t *CarrierTunnel) unregisterProviderStream(stream *providerStreamResponse) {
	t.mu.Lock()
	delete(stream.reader.streams, stream.id)
	t.stopUnusedReaderLocked(stream.reader)
	t.mu.Unlock()
}

func (t *CarrierTunnel) routeProviderStream(key string, frame fabric.Envelope) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	reader := t.packetReaders[key]
	if reader == nil {
		return false
	}
	stream := reader.streams[frame.ID]
	if stream == nil {
		return false
	}
	select {
	case stream.frames <- frame:
	default:
		// Backpressure from one SOCKS consumer cannot stall every flow.
		// Abort that TCP stream so missing bytes never reach TLS silently.
		stream.abort(errors.New("carrier tunnel: provider stream receive queue is full"))
	}
	return true
}

func (t *CarrierTunnel) failProviderStreams(reader *packetClientReader, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, stream := range reader.streams {
		stream.abort(err)
	}
}

func (t *CarrierTunnel) readProviderStream(ctx context.Context, stream *providerStreamResponse, client net.Conn, failure *carrierFailureState, watch *egressResponseWatch) {
	defer client.Close()
	poll := time.NewTicker(egressReadInterval)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			failure.markGraceful()
			return
		case frame := <-stream.frames:
			if !t.deliverStreamResponse(stream.id, client, frame, watch) {
				failure.markGraceful()
				return
			}
		case now := <-poll.C:
			deadline := watch.responseDeadline(egressResponseLivenessTimeout)
			if !deadline.IsZero() && !now.Before(deadline) {
				failure.set(fmt.Errorf("carrier tunnel: no response within %s", egressResponseLivenessTimeout))
				return
			}
		}
	}
}
