package carriers

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"time"
)

const (
	vkHistorySuccessTTL = 2 * time.Second
	vkHistoryErrorTTL   = 500 * time.Millisecond
	vkHistoryMaxEntries = 128
)

// VKHistoryCache shares recent VK messages.getHistory responses across carriers.
// Create one with NewVKHistoryCache for the daemon's VK readers.
type VKHistoryCache struct {
	mu      sync.Mutex
	entries map[vkHistoryKey]vkHistoryResult
	flights map[vkHistoryKey]*vkHistoryFlight
}

type vkHistoryKey struct {
	tokenHash [sha256.Size]byte
	version   string
	baseURL   string
	peer      string
}

type vkHistoryResult struct {
	response  map[string]any
	err       error
	expiresAt time.Time
}

type vkHistoryFlight struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	result  vkHistoryResult
}

// NewVKHistoryCache creates a cache shared by VK history readers.
func NewVKHistoryCache() *VKHistoryCache {
	return &VKHistoryCache{
		entries: make(map[vkHistoryKey]vkHistoryResult),
		flights: make(map[vkHistoryKey]*vkHistoryFlight),
	}
}

// read shares the same history request for a key. Callers must treat the
// returned response as read-only. A nil cache calls fetch directly.
func (c *VKHistoryCache) read(ctx context.Context, token, apiVersion, baseURL, peer string, fetch func(context.Context) (map[string]any, error)) (map[string]any, error) {
	if c == nil {
		return fetch(ctx)
	}
	key := vkHistoryKey{
		tokenHash: sha256.Sum256([]byte(token)),
		version:   apiVersion,
		baseURL:   baseURL,
		peer:      peer,
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c.mu.Lock()
		if entry, ok := c.entries[key]; ok {
			if time.Now().Before(entry.expiresAt) {
				c.mu.Unlock()
				return entry.response, entry.err
			}
			delete(c.entries, key)
		}
		flight := c.flights[key]
		if flight == nil {
			// The request lives as long as any caller is waiting for it.
			fetchCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
			flight = &vkHistoryFlight{done: make(chan struct{}), cancel: cancel, waiters: 1}
			c.flights[key] = flight
			go c.load(fetchCtx, key, flight, fetch)
		} else if flight.waiters > 0 {
			flight.waiters++
		}
		c.mu.Unlock()

		select {
		case <-ctx.Done():
			c.mu.Lock()
			if c.flights[key] == flight && flight.waiters > 0 {
				flight.waiters--
				if flight.waiters == 0 {
					flight.cancel()
				}
			}
			c.mu.Unlock()
			return nil, ctx.Err()
		case <-flight.done:
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if flight.waiters > 0 {
				return flight.result.response, flight.result.err
			}
			// The abandoned fetch has returned. Try again with a fresh request.
		}
	}
}

func (c *VKHistoryCache) load(ctx context.Context, key vkHistoryKey, flight *vkHistoryFlight, fetch func(context.Context) (map[string]any, error)) {
	defer flight.cancel()
	response, err := fetch(ctx)
	ttl := vkHistorySuccessTTL
	if err != nil {
		ttl = vkHistoryErrorTTL
	}
	result := vkHistoryResult{response: response, err: err, expiresAt: time.Now().Add(ttl)}
	cacheable := !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)

	c.mu.Lock()
	defer c.mu.Unlock()
	flight.result = result
	if flight.waiters > 0 && cacheable {
		c.store(key, result)
	}
	delete(c.flights, key)
	// The result and waiter count are immutable after done is closed.
	close(flight.done)
}

// store runs under mu. Only completed requests count toward the cache limit.
func (c *VKHistoryCache) store(key vkHistoryKey, result vkHistoryResult) {
	if len(c.entries) >= vkHistoryMaxEntries {
		var earliestKey vkHistoryKey
		var earliestExpiry time.Time
		for candidate, entry := range c.entries {
			if earliestExpiry.IsZero() || entry.expiresAt.Before(earliestExpiry) {
				earliestKey, earliestExpiry = candidate, entry.expiresAt
			}
		}
		delete(c.entries, earliestKey)
	}
	c.entries[key] = result
}
