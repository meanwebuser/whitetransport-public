package runtime

import (
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/session"
)

const defaultRouteInterruptAfter = 10 * time.Second

var ErrStaleEgressGeneration = errors.New("stale egress route generation")

type routeDialLease struct {
	routeKey   string
	generation uint64
}

type routeGenerationState struct {
	eligible   bool
	generation uint64
	streams    map[*trackedRouteConn]uint64
	cause      error
}

// routeStreamRegistry makes route quarantine atomic with concurrent dial
// registration and bounds the lifetime of already-open streams.
type routeStreamRegistry struct {
	mu             sync.Mutex
	routes         map[string]*routeGenerationState
	interruptAfter time.Duration
	now            func() time.Time
	closed         bool
}

func newRouteStreamRegistry(interruptAfter time.Duration, now func() time.Time) *routeStreamRegistry {
	if interruptAfter <= 0 {
		interruptAfter = defaultRouteInterruptAfter
	}
	if now == nil {
		now = time.Now
	}
	return &routeStreamRegistry{routes: make(map[string]*routeGenerationState), interruptAfter: interruptAfter, now: now}
}

func (r *routeStreamRegistry) beginDial(routeKey string) (routeDialLease, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return routeDialLease{}, false
	}
	state := r.stateLocked(routeKey)
	if !state.eligible {
		return routeDialLease{}, false
	}
	return routeDialLease{routeKey: routeKey, generation: state.generation}, true
}

func (r *routeStreamRegistry) canDial(routeKey string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	return r.stateLocked(routeKey).eligible
}

func (r *routeStreamRegistry) restore(routeKey string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	state := r.stateLocked(routeKey)
	state.eligible = true
	state.cause = nil
}

// quarantineCause returns the typed reason that made a route unavailable.
// It is used by the foreground dial path to distinguish a carrier-quarantined
// automatic session from a target/application failure.
func (r *routeStreamRegistry) quarantineCause(routeKey string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	state := r.stateLocked(routeKey)
	if state.eligible {
		return nil
	}
	return state.cause
}

func (r *routeStreamRegistry) register(lease routeDialLease, conn net.Conn, onFailure func(error)) (net.Conn, bool) {
	tracked := &trackedRouteConn{Conn: conn, owner: r, routeKey: lease.routeKey, generation: lease.generation, onFailure: onFailure}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		_ = conn.Close()
		return nil, false
	}
	state := r.stateLocked(lease.routeKey)
	if !state.eligible || state.generation != lease.generation {
		r.mu.Unlock()
		_ = conn.Close()
		return nil, false
	}
	state.streams[tracked] = lease.generation
	r.mu.Unlock()
	return tracked, true
}

func (r *routeStreamRegistry) quarantine(routeKey string, cause error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	state := r.stateLocked(routeKey)
	if !state.eligible {
		r.mu.Unlock()
		return
	}
	state.eligible = false
	state.cause = cause
	state.generation++
	streams := make([]*trackedRouteConn, 0, len(state.streams))
	for stream := range state.streams {
		streams = append(streams, stream)
	}
	r.mu.Unlock()

	deadline := r.now().Add(r.interruptAfter)
	for _, stream := range streams {
		_ = stream.Conn.SetDeadline(deadline)
	}
	time.AfterFunc(r.interruptAfter, func() {
		for _, stream := range streams {
			_ = stream.Conn.SetDeadline(r.now())
			_ = stream.Close()
		}
	})
	for _, stream := range streams {
		stream.notifyFailure(cause)
	}
}

// shutdownSession is a terminal generation barrier for the registry owned by
// one active ControlPlane session. It rejects every in-flight/future dial,
// removes route state, and interrupts streams outside the registry lock.
func (r *routeStreamRegistry) shutdownSession(sessionID string) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	prefix := sessionID + "\x00"
	streams := make([]*trackedRouteConn, 0)
	for routeKey, state := range r.routes {
		state.eligible = false
		state.generation++
		if sessionID == "" || strings.HasPrefix(routeKey, prefix) {
			for stream := range state.streams {
				streams = append(streams, stream)
			}
		}
	}
	r.routes = make(map[string]*routeGenerationState)
	r.mu.Unlock()

	for _, stream := range streams {
		_ = stream.Conn.SetDeadline(r.now())
		_ = stream.Close()
	}
}

func (r *routeStreamRegistry) remove(stream *trackedRouteConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if state := r.routes[stream.routeKey]; state != nil {
		delete(state.streams, stream)
	}
}

func (r *routeStreamRegistry) activeCount(routeKey string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if state := r.routes[routeKey]; state != nil {
		return len(state.streams)
	}
	return 0
}

func (r *routeStreamRegistry) routeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.routes)
}

func (r *routeStreamRegistry) stateLocked(routeKey string) *routeGenerationState {
	state := r.routes[routeKey]
	if state == nil {
		state = &routeGenerationState{eligible: true, streams: make(map[*trackedRouteConn]uint64)}
		r.routes[routeKey] = state
	}
	return state
}

type trackedRouteConn struct {
	net.Conn
	owner      *routeStreamRegistry
	routeKey   string
	generation uint64
	onFailure  func(error)
	failure    sync.Once
	close      sync.Once
}

func (c *trackedRouteConn) Read(payload []byte) (int, error) {
	n, err := c.Conn.Read(payload)
	routeErr := routeFailureCause(c.Conn, err)
	if shouldQuarantineRouteError(routeErr) {
		c.owner.quarantine(c.routeKey, routeErr)
	}
	return n, err
}

func (c *trackedRouteConn) Write(payload []byte) (int, error) {
	n, err := c.Conn.Write(payload)
	routeErr := routeFailureCause(c.Conn, err)
	if shouldQuarantineRouteError(routeErr) {
		c.owner.quarantine(c.routeKey, routeErr)
	}
	return n, err
}

func routeFailureCause(conn net.Conn, fallback error) error {
	if reporter, ok := conn.(interface{ FailureCause() error }); ok {
		if cause := reporter.FailureCause(); cause != nil {
			if session.IsCarrierFailure(cause) || (fallback != nil && !isGracefulStreamTermination(fallback)) {
				return cause
			}
		}
	}
	return fallback
}

func shouldQuarantineRouteError(err error) bool {
	return err != nil && !isGracefulStreamTermination(err)
}

func isGracefulStreamTermination(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed)
}

func (c *trackedRouteConn) Close() error {
	var err error
	c.close.Do(func() {
		c.owner.remove(c)
		err = c.Conn.Close()
	})
	return err
}

func (c *trackedRouteConn) notifyFailure(err error) {
	c.failure.Do(func() {
		if c.onFailure != nil {
			c.onFailure(err)
		}
	})
}
