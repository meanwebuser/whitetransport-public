package carriers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const vkHistoryMethod = "messages.getHistory"

// DefaultVKAPIBaseURL also identifies the VK control dependency in VPN profiles.
const DefaultVKAPIBaseURL = "https://api.vk.ru/method"

// VKHistoryThrottleError reports a history poll that reached its retry limit
// or was interrupted while the provider was throttling it.
type VKHistoryThrottleError struct {
	Code int
}

func (e *VKHistoryThrottleError) Error() string {
	if e.Code == http.StatusTooManyRequests {
		return fmt.Sprintf("vk %s throttled (HTTP 429)", vkHistoryMethod)
	}
	return fmt.Sprintf("vk %s throttled (VK code %d)", vkHistoryMethod, e.Code)
}

func readVKHistory(ctx context.Context, history *VKHistoryCache, token, apiVersion, baseURL, peerID string, call func(context.Context, string, url.Values) (map[string]any, error)) ([]any, error) {
	fetch := func(ctx context.Context) (map[string]any, error) {
		return call(ctx, vkHistoryMethod, url.Values{"peer_id": {peerID}, "count": {"100"}})
	}
	response, err := history.read(ctx, token, apiVersion, baseURL, peerID, fetch)
	if err != nil {
		return nil, err
	}
	container, ok := response["response"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("vk %s missing response object", vkHistoryMethod)
	}
	items, ok := container["items"].([]any)
	if !ok {
		return nil, fmt.Errorf("vk %s missing items", vkHistoryMethod)
	}
	return items, nil
}

func callVK(ctx context.Context, client HTTPDoer, baseURL, token, apiVersion, userAgent, method string, values url.Values) (map[string]any, error) {
	values.Set("access_token", token)
	values.Set("v", apiVersion)
	isHistory := method == vkHistoryMethod
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/"+method, strings.NewReader(values.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("User-Agent", userAgent)
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil && lastErr != nil && isHistory {
				return nil, fmt.Errorf("%w: %w", lastErr, ctx.Err())
			}
			return nil, err
		}
		if resp.StatusCode == http.StatusTooManyRequests && isHistory {
			resp.Body.Close()
			lastErr = &VKHistoryThrottleError{Code: http.StatusTooManyRequests}
			if err := waitVKBackoff(ctx, vkRetryAfter(resp.Header.Get("Retry-After")), attempt); err != nil {
				return nil, fmt.Errorf("%w: %w", lastErr, err)
			}
			continue
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return nil, fmt.Errorf("vk %s HTTP %d", method, resp.StatusCode)
		}
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			return nil, err
		}
		rawError, hasError := decoded["error"]
		if !hasError {
			return decoded, nil
		}
		providerError, _ := rawError.(map[string]any)
		code := intFromJSON(providerError["error_code"])
		lastErr = fmt.Errorf("vk %s error: error_code=%d", method, code)
		if code != 6 && code != 9 {
			return nil, lastErr
		}
		if isHistory {
			lastErr = &VKHistoryThrottleError{Code: code}
		}
		delay := time.Duration(intFromJSON(providerError["retry_after"])) * time.Second
		if err := waitVKBackoff(ctx, min(delay, 10*time.Second), attempt); err != nil {
			if isHistory {
				return nil, fmt.Errorf("%w: %w", lastErr, err)
			}
			return nil, err
		}
	}
	return nil, lastErr
}

func vkRetryAfter(header string) time.Duration {
	header = strings.TrimSpace(header)
	if seconds, err := strconv.ParseInt(header, 10, 32); err == nil {
		return time.Duration(seconds) * time.Second
	}
	if retryAt, err := http.ParseTime(header); err == nil {
		return time.Until(retryAt)
	}
	return 0
}

func waitVKBackoff(ctx context.Context, delay time.Duration, attempt int) error {
	if delay <= 0 {
		delay = time.Duration(1<<attempt) * time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
