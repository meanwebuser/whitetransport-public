package carriers

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

const (
	vkHistoryFixtureToken   = "fixture-account"
	vkHistoryFixtureVersion = "5.199"
	vkHistoryFixtureBaseURL = "https://vk.test/method"
	vkHistoryFixturePeer    = "123"
)

func TestVKHistoryCacheCoalescesAndRefreshes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := NewVKHistoryCache()
		var calls atomic.Int32
		release := make(chan struct{})
		fetch := func(context.Context) (map[string]any, error) {
			id := calls.Add(1)
			<-release
			return map[string]any{"id": id}, nil
		}

		readers := []<-chan vkHistoryTestResult{startVKHistoryRead(t.Context(), cache, fetch)}
		synctest.Wait()
		// A slow request stays coalesced beyond the lifetime of a cached result.
		time.Sleep(3 * time.Second)
		for range 7 {
			readers = append(readers, startVKHistoryRead(t.Context(), cache, fetch))
		}
		synctest.Wait()
		close(release)
		for _, reader := range readers {
			result := <-reader
			if result.err != nil || result.response["id"] != int32(1) {
				t.Fatalf("coalesced read = %v, %v; want result from fetch 1", result.response, result.err)
			}
		}
		if got := calls.Load(); got != 1 {
			t.Fatalf("concurrent reads fetched %d times; want 1", got)
		}

		time.Sleep(1900 * time.Millisecond)
		response, err := readVKHistoryFixture(t.Context(), cache, fetch)
		if err != nil || response["id"] != int32(1) {
			t.Fatalf("fresh cache read = %v, %v; want result from fetch 1", response, err)
		}
		time.Sleep(100 * time.Millisecond)
		response, err = readVKHistoryFixture(t.Context(), cache, fetch)
		if err != nil || response["id"] != int32(2) {
			t.Fatalf("expired cache read = %v, %v; want result from fetch 2", response, err)
		}
	})
}

func TestVKHistoryCacheSeparatesIdentities(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := NewVKHistoryCache()
		calls := 0
		fetch := func(context.Context) (map[string]any, error) {
			calls++
			return map[string]any{"id": calls}, nil
		}
		for i, identity := range []struct{ name, token, version, baseURL, peer string }{
			{"original", vkHistoryFixtureToken, vkHistoryFixtureVersion, vkHistoryFixtureBaseURL, vkHistoryFixturePeer},
			{"token", "fixture-other-account", vkHistoryFixtureVersion, vkHistoryFixtureBaseURL, vkHistoryFixturePeer},
			{"version", vkHistoryFixtureToken, "5.200", vkHistoryFixtureBaseURL, vkHistoryFixturePeer},
			{"API base", vkHistoryFixtureToken, vkHistoryFixtureVersion, "https://other.vk.test/method", vkHistoryFixturePeer},
			{"peer", vkHistoryFixtureToken, vkHistoryFixtureVersion, vkHistoryFixtureBaseURL, "456"},
		} {
			for range 2 {
				response, err := cache.read(t.Context(), identity.token, identity.version, identity.baseURL, identity.peer, fetch)
				if err != nil || response["id"] != i+1 {
					t.Fatalf("read for distinct %s = %v, %v; want result from fetch %d", identity.name, response, err, i+1)
				}
			}
		}
	})
}

func TestVKHistoryCacheExpiresErrorsEarly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := NewVKHistoryCache()
		throttle := errors.New("fixture history throttled (VK code 9)")
		calls := 0
		fetch := func(context.Context) (map[string]any, error) {
			calls++
			if calls == 1 {
				return nil, throttle
			}
			return map[string]any{"id": calls}, nil
		}

		for _, delay := range []time.Duration{0, 100 * time.Millisecond} {
			time.Sleep(delay)
			if _, err := readVKHistoryFixture(t.Context(), cache, fetch); err != throttle {
				t.Fatalf("read within error TTL returned %v; want original throttle error", err)
			}
		}
		time.Sleep(400 * time.Millisecond)
		response, err := readVKHistoryFixture(t.Context(), cache, fetch)
		if err != nil || response["id"] != 2 {
			t.Fatalf("read after error TTL = %v, %v; want result from fetch 2", response, err)
		}
	})
}

func TestVKHistoryCacheCallerCancellationIsIndependent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := NewVKHistoryCache()
		var calls atomic.Int32
		fetch := func(ctx context.Context) (map[string]any, error) {
			id := calls.Add(1)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(3 * time.Second):
				return map[string]any{"id": id}, nil
			}
		}
		leaderCtx, cancelLeader := context.WithTimeout(t.Context(), time.Second)
		defer cancelLeader()
		leader := startVKHistoryRead(leaderCtx, cache, fetch)
		synctest.Wait()
		waiterCtx, cancelWaiter := context.WithCancel(t.Context())
		defer cancelWaiter()
		waiter := startVKHistoryRead(waiterCtx, cache, fetch)
		survivor := startVKHistoryRead(t.Context(), cache, fetch)
		synctest.Wait()

		started := time.Now()
		time.Sleep(time.Second)
		if result := <-leader; result.err != context.DeadlineExceeded {
			t.Fatalf("leader returned %v; want deadline exceeded", result.err)
		}
		cancelWaiter()
		if result := <-waiter; result.err != context.Canceled {
			t.Fatalf("waiter returned %v; want canceled", result.err)
		}
		if elapsed := time.Since(started); elapsed != time.Second {
			t.Fatalf("canceled readers took %s; want 1s", elapsed)
		}
		result := <-survivor
		if result.err != nil || result.response["id"] != int32(1) || calls.Load() != 1 {
			t.Fatalf("remaining reader got %v, %v (%d fetches); want result from the original fetch", result.response, result.err, calls.Load())
		}
	})
}

func TestVKHistoryCacheWaitsForAbandonedFetch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := NewVKHistoryCache()
		ctx, cancel := context.WithCancel(t.Context())
		canceled := make(chan struct{})
		finish := make(chan struct{})
		leader := startVKHistoryRead(ctx, cache, func(ctx context.Context) (map[string]any, error) {
			select {
			case <-ctx.Done():
				close(canceled)
				<-finish
				return nil, ctx.Err()
			case <-finish:
				return nil, errors.New("fixture released before cancellation")
			}
		})
		synctest.Wait()
		cancel()
		synctest.Wait()
		select {
		case result := <-leader:
			if result.err != context.Canceled {
				t.Errorf("leader returned %v; want canceled", result.err)
			}
		default:
			t.Error("last reader did not return on cancellation")
		}
		select {
		case <-canceled:
		default:
			t.Error("last reader leaving did not cancel the fetch")
		}

		var replacementStarted atomic.Bool
		replacement := startVKHistoryRead(t.Context(), cache, func(context.Context) (map[string]any, error) {
			replacementStarted.Store(true)
			return map[string]any{"id": 2}, nil
		})
		synctest.Wait()
		if replacementStarted.Load() {
			t.Error("replacement overlapped the canceled fetch")
		}
		close(finish)
		result := <-replacement
		if result.err != nil || result.response["id"] != 2 {
			t.Fatalf("replacement read = %v, %v; want fresh result", result.response, result.err)
		}
	})
}

func TestVKHistoryCacheDoesNotCacheCancellationErrors(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cache := NewVKHistoryCache()
				_, err := readVKHistoryFixture(t.Context(), cache, func(context.Context) (map[string]any, error) {
					return nil, fmt.Errorf("fixture history: %w", cause)
				})
				if !errors.Is(err, cause) {
					t.Fatalf("first read returned %v; want %v", err, cause)
				}
				response, err := readVKHistoryFixture(t.Context(), cache, func(context.Context) (map[string]any, error) {
					return map[string]any{"id": 2}, nil
				})
				if err != nil || response["id"] != 2 {
					t.Fatalf("read after canceled fetch = %v, %v; want fresh result", response, err)
				}
			})
		})
	}
}

func TestVKHistoryCacheBoundsEntries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := NewVKHistoryCache()
		fetch := func(context.Context) (map[string]any, error) { return map[string]any{"id": 1}, nil }
		for peer := range vkHistoryMaxEntries + 1 {
			_, err := cache.read(t.Context(), vkHistoryFixtureToken, vkHistoryFixtureVersion, vkHistoryFixtureBaseURL, strconv.Itoa(peer), fetch)
			if err != nil {
				t.Fatal(err)
			}
		}
		if got := len(cache.entries); got != vkHistoryMaxEntries {
			t.Fatalf("retained %d entries; want %d", got, vkHistoryMaxEntries)
		}
	})
}

func TestVKHistoryCacheNilReceiverCallsFetch(t *testing.T) {
	var cache *VKHistoryCache
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for i := 1; i <= 2; i++ {
		response, err := readVKHistoryFixture(ctx, cache, func(fetchCtx context.Context) (map[string]any, error) {
			if fetchCtx != ctx {
				t.Fatal("nil cache changed the caller context")
			}
			return map[string]any{"id": i}, nil
		})
		if err != nil || response["id"] != i {
			t.Fatalf("nil cache read = %v, %v; want result from fetch %d", response, err, i)
		}
	}
}

type vkHistoryTestResult struct {
	response map[string]any
	err      error
}

func startVKHistoryRead(ctx context.Context, cache *VKHistoryCache, fetch func(context.Context) (map[string]any, error)) <-chan vkHistoryTestResult {
	done := make(chan vkHistoryTestResult, 1)
	go func() {
		response, err := readVKHistoryFixture(ctx, cache, fetch)
		done <- vkHistoryTestResult{response: response, err: err}
	}()
	return done
}

func readVKHistoryFixture(ctx context.Context, cache *VKHistoryCache, fetch func(context.Context) (map[string]any, error)) (map[string]any, error) {
	return cache.read(ctx, vkHistoryFixtureToken, vkHistoryFixtureVersion, vkHistoryFixtureBaseURL, vkHistoryFixturePeer, fetch)
}
