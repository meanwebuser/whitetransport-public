package router

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

const (
	defaultSendQueueSize = 64
	maxSendRetries       = 2
	sendBackoffBase      = 200 * time.Millisecond
)

type sendRequest struct {
	carrier  carriers.Carrier
	endpoint carriers.Endpoint
	envelope fabric.Envelope
	result   chan error
}

// SendQueue provides ordered, rate-limited writes to carriers with retry.
// Each carrier gets its own worker goroutine to ensure sequential writes.
type SendQueue struct {
	health *CarrierHealth

	mu      sync.Mutex
	workers map[string]chan sendRequest
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// NewSendQueue creates a send queue with per-carrier workers.
func NewSendQueue(health *CarrierHealth) *SendQueue {
	return &SendQueue{
		health:  health,
		workers: make(map[string]chan sendRequest),
	}
}

// Start begins the send queue. Must be called before Send.
func (q *SendQueue) Start(ctx context.Context) {
	q.mu.Lock()
	defer q.mu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	q.ctx = ctx
	q.cancel = cancel
	_ = ctx
}

// Stop shuts down all workers.
func (q *SendQueue) Stop() {
	q.mu.Lock()
	cancel := q.cancel
	// Close all worker channels so goroutines exit.
	for _, ch := range q.workers {
		close(ch)
	}
	q.workers = make(map[string]chan sendRequest)
	q.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	q.wg.Wait()
}

// Send enqueues an envelope for writing to a carrier. The write happens
// asynchronously; the returned channel resolves when the write completes.
func (q *SendQueue) Send(carrierID string, carrier carriers.Carrier, endpoint carriers.Endpoint, envelope fabric.Envelope) <-chan error {
	q.mu.Lock()
	ch, ok := q.workers[carrierID]
	if !ok {
		ch = make(chan sendRequest, defaultSendQueueSize)
		q.workers[carrierID] = ch
		q.wg.Add(1)
		go q.worker(carrierID, ch)
	}
	q.mu.Unlock()

	result := make(chan error, 1)
	req := sendRequest{
		carrier:  carrier,
		endpoint: endpoint,
		envelope: envelope,
		result:   result,
	}
	select {
	case ch <- req:
	default:
		// Queue full — fail fast.
		result <- fmt.Errorf("send queue full for carrier %s", carrierID)
	}
	return result
}

// SendSync enqueues and waits for the write to complete.
func (q *SendQueue) SendSync(ctx context.Context, carrierID string, carrier carriers.Carrier, endpoint carriers.Endpoint, envelope fabric.Envelope) error {
	ch := q.Send(carrierID, carrier, endpoint, envelope)
	select {
	case err := <-ch:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (q *SendQueue) worker(carrierID string, ch chan sendRequest) {
	defer q.wg.Done()
	for {
		select {
		case req, ok := <-ch:
			if !ok {
				return
			}
			err := q.writeWithRetry(carrierID, req)
			req.result <- err
		case <-q.ctx.Done():
			return
		}
	}
}

func (q *SendQueue) writeWithRetry(carrierID string, req sendRequest) error {
	var lastErr error
	for attempt := 0; attempt <= maxSendRetries; attempt++ {
		if attempt > 0 {
			backoff := sendBackoffBase * time.Duration(1<<uint(attempt-1))
			select {
			case <-time.After(backoff):
			case <-q.ctx.Done():
				return q.ctx.Err()
			}
		}
		err := req.carrier.Write(q.ctx, req.endpoint, req.envelope)
		if err == nil {
			if q.health != nil {
				q.health.RecordWriteSuccess(carrierID)
			}
			return nil
		}
		lastErr = err
	}
	if q.health != nil {
		q.health.RecordWriteFailure(carrierID)
	}
	return fmt.Errorf("send to %s after %d retries: %w", carrierID, maxSendRetries, lastErr)
}
