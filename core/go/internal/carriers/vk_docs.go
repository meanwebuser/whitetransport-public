package carriers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

const (
	vkDocsMessagePrefix        = "wtbulk1 "
	vkDocsUploadAttemptTimeout = 6 * time.Second
	vkDocsUploadOverlapDelay   = 3 * time.Second
	vkDocsUploadAttempts       = 3
	vkDocsRecentWrites         = 100 // One VK history page.
)

type vkDocsEnvelopeFilterKey struct{}

// WithVKDocsEnvelopeFilter enables stream reads that can skip documents this
// carrier sent itself. A nonempty ID also limits marked documents to that stream.
func WithVKDocsEnvelopeFilter(ctx context.Context, envelopeID string) context.Context {
	return context.WithValue(ctx, vkDocsEnvelopeFilterKey{}, envelopeID)
}

func sortedMapKeys(values map[string]any) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// VKDocsConfig contains runtime inputs for VK document bulk carriers.
type VKDocsConfig struct {
	Token        string
	APIVersion   string
	BaseURL      string
	DescriptorID string
	HTTPClient   HTTPDoer
	HistoryCache *VKHistoryCache
}

// VKDocsCarrier moves envelope chunks through retained VK document attachments.
type VKDocsCarrier struct {
	token      string
	apiVersion string
	baseURL    string
	httpClient HTTPDoer
	desc       Descriptor
	history    *VKHistoryCache
	sentMu     sync.RWMutex
	sent       [vkDocsRecentWrites]vkDocsSentMessage
	nextSent   int
	// Upload URLs are reserved only while a POST is in flight, across all writes.
	activeGrants sync.Map
}

type vkDocsSentMessage struct {
	peerID    string
	messageID int
}

type decodedCarrierItem struct {
	id       int
	envelope fabric.Envelope
}

// NewVKDocsCarrier creates a VK document carrier for vk.docs.256 or
// vk.docs.1024. Payload codecs such as PNG/YTP can be layered above this
// adapter; this carrier preserves and retrieves raw envelope bytes.
func NewVKDocsCarrier(cfg VKDocsConfig) (*VKDocsCarrier, error) {
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, fmt.Errorf("vk docs token is required")
	}
	descriptorID := cfg.DescriptorID
	if descriptorID == "" {
		descriptorID = CarrierVKDocs256
	}
	if descriptorID != CarrierVKDocs256 && descriptorID != CarrierVKDocs1024 {
		return nil, fmt.Errorf("vk docs descriptor must be %s or %s", CarrierVKDocs256, CarrierVKDocs1024)
	}
	apiVersion := cfg.APIVersion
	if apiVersion == "" {
		apiVersion = "5.199"
	}
	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	if baseURL == "" {
		baseURL = DefaultVKAPIBaseURL
	}
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	desc, err := FindStandardDescriptor(descriptorID)
	if err != nil {
		return nil, err
	}
	return &VKDocsCarrier{
		token:      cfg.Token,
		apiVersion: apiVersion,
		baseURL:    baseURL,
		httpClient: client,
		desc:       desc,
		history:    cfg.HistoryCache,
	}, nil
}

func (c *VKDocsCarrier) Descriptor() Descriptor { return c.desc }
func (c *VKDocsCarrier) IsNative()              {}

func (c *VKDocsCarrier) Write(ctx context.Context, endpoint Endpoint, envelope fabric.Envelope) error {
	peerID, err := vkPeerID(endpoint)
	if err != nil {
		return err
	}
	data, err := encodeDocumentEnvelope(envelope)
	if err != nil {
		return err
	}
	if c.desc.Limits.MaxPayloadBytes > 0 && len(data) > c.desc.Limits.MaxPayloadBytes {
		return fmt.Errorf("vk docs envelope size %d exceeds carrier limit %d", len(data), c.desc.Limits.MaxPayloadBytes)
	}
	file, err := c.uploadDocumentWithRetry(ctx, peerID, data, envelope.ID+".wtmsg", envelope.TrafficClass == fabric.TrafficEgress)
	if err != nil {
		return err
	}
	saveResult, err := c.call(ctx, "docs.save", url.Values{
		"file":  {file},
		"title": {envelope.ID + ".wtmsg"},
		"tags":  {"whitetransport"},
	})
	if err != nil {
		return err
	}
	attachment, err := vkDocAttachment(saveResult)
	if err != nil {
		return err
	}
	sendResult, err := c.call(ctx, "messages.send", url.Values{
		"peer_id":    {peerID},
		"message":    {vkDocsMessagePrefix + envelope.ID},
		"attachment": {attachment},
		"random_id":  {strconv.FormatInt(rand.New(rand.NewSource(time.Now().UnixNano())).Int63(), 10)},
	})
	if err != nil {
		return err
	}
	if _, ok := sendResult["response"]; !ok {
		return fmt.Errorf("vk messages.send missing response")
	}
	if id, err := vkResponseMessageID(sendResult); err == nil && id > 0 {
		c.sentMu.Lock()
		c.sent[c.nextSent] = vkDocsSentMessage{peerID: peerID, messageID: id}
		c.nextSent = (c.nextSent + 1) % len(c.sent)
		c.sentMu.Unlock()
	}
	return nil
}

func (c *VKDocsCarrier) sentMessage(peerID string, id int) bool {
	c.sentMu.RLock()
	defer c.sentMu.RUnlock()
	return slices.Contains(c.sent[:], vkDocsSentMessage{peerID: peerID, messageID: id})
}

func (c *VKDocsCarrier) uploadDocumentWithRetry(ctx context.Context, peerID string, data []byte, filename string, allowOverlap bool) (string, error) {
	ctx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer func() {
		cancel()
		workers.Wait()
	}()

	type uploadEvent struct {
		attempt    int
		postingAt  time.Time
		finishedAt time.Time
		elapsed    time.Duration
		file       string
		err        error
	}
	// Each worker can report one POST start and one result, even after cancellation.
	events := make(chan uploadEvent, 2*vkDocsUploadAttempts)
	type activeUpload struct {
		cancel  context.CancelFunc
		stopped bool
	}
	active := make(map[int]*activeUpload, 2)
	started := 0
	var overlapAt, retryAt time.Time
	var lastErr, permanentErr error
	start := func() {
		started++
		overlapAt = time.Time{}
		attempt := started
		attemptCtx, stop := context.WithCancel(ctx)
		active[attempt] = &activeUpload{cancel: stop}
		workers.Go(func() {
			began := time.Now()
			file, err := c.uploadDocumentAttempt(attemptCtx, peerID, data, filename, func() {
				events <- uploadEvent{attempt: attempt, postingAt: time.Now()}
			})
			finished := time.Now()
			events <- uploadEvent{attempt: attempt, finishedAt: finished, elapsed: finished.Sub(began), file: file, err: err}
		})
	}

	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if len(active) == 0 && (permanentErr != nil || started == vkDocsUploadAttempts) {
			if permanentErr != nil {
				return "", permanentErr
			}
			return "", lastErr
		}

		var ready <-chan time.Time
		if permanentErr == nil && started < vkDocsUploadAttempts {
			switch {
			case len(active) == 0:
				ready = time.After(time.Until(retryAt))
			case len(active) == 1 && allowOverlap && !overlapAt.IsZero():
				ready = time.After(max(time.Until(retryAt), time.Until(overlapAt)))
			}
		}
		var event uploadEvent
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case event = <-events:
		case <-ready:
			// Give a completed upload or a provider rejection priority over a new grant.
			select {
			case event = <-events:
			default:
				start()
				continue
			}
		}
		current := active[event.attempt]
		if !event.postingAt.IsZero() {
			if !current.stopped {
				overlapAt = event.postingAt.Add(vkDocsUploadOverlapDelay)
			}
			continue
		}
		current.cancel()
		delete(active, event.attempt)
		if event.attempt == started {
			overlapAt = time.Time{}
		}
		if current.stopped {
			continue
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if event.err == nil {
			return event.file, nil
		}
		lastErr = event.err
		uploadErr, ok := event.err.(*vkDocsUploadError)
		if !ok || !uploadErr.retryable {
			permanentErr = event.err
			continue
		}
		// A gateway timeout uses the same overlap and pacing rules as a local timeout.
		completedTimeout := uploadErr.timedOut || uploadErr.httpStatus == http.StatusGatewayTimeout
		allowOverlap = allowOverlap && completedTimeout
		if uploadErr.httpStatus == http.StatusTooManyRequests {
			for _, pending := range active {
				pending.stopped = true
				pending.cancel()
			}
		}
		delay := time.Duration(event.attempt) * time.Second
		if completedTimeout {
			delay -= event.elapsed
		}
		next := event.finishedAt.Add(max(delay, uploadErr.retryAfter))
		if next.After(retryAt) {
			retryAt = next
		}
		if started < vkDocsUploadAttempts && permanentErr == nil {
			log.Printf("[vk docs] retrying upload after attempt %d: %s", event.attempt, uploadErr)
		}
	}
}

func (c *VKDocsCarrier) uploadDocumentAttempt(ctx context.Context, peerID string, data []byte, filename string, onPost func()) (string, error) {
	// Each attempt starts with its own grant, including overlapping uploads.
	uploadInfo, err := c.call(ctx, "docs.getMessagesUploadServer", url.Values{
		"peer_id": {peerID},
		"type":    {"doc"},
	})
	if err != nil {
		return "", err
	}
	uploadURL, err := vkUploadURL(uploadInfo)
	if err != nil {
		return "", err
	}
	if _, active := c.activeGrants.LoadOrStore(uploadURL, struct{}{}); active {
		return "", &vkDocsUploadError{reason: "provider repeated an active upload grant", retryable: true}
	}
	defer c.activeGrants.Delete(uploadURL)
	result, uploadErr := c.uploadDocument(ctx, uploadURL, data, filename, onPost)
	if uploadErr != nil {
		return "", uploadErr
	}
	if file, ok := result["file"].(string); ok && strings.TrimSpace(file) != "" {
		return file, nil
	}
	_, rejected := result["error"]
	return "", &vkDocsUploadError{
		reason:    fmt.Sprintf("missing file token (response keys: %s)", sortedMapKeys(result)),
		retryable: rejected,
	}
}

func (c *VKDocsCarrier) Read(ctx context.Context, endpoint Endpoint, cursor Cursor) (ReadResult, error) {
	peerID, err := vkPeerID(endpoint)
	if err != nil {
		return ReadResult{}, err
	}
	rawItems, err := readVKHistory(ctx, c.history, c.token, c.apiVersion, c.baseURL, peerID, c.call)
	if err != nil {
		return ReadResult{}, err
	}
	filterID, streamRead := ctx.Value(vkDocsEnvelopeFilterKey{}).(string)
	after, _ := strconv.Atoi(string(cursor))
	type historyMessage struct {
		id           int
		marked       bool
		documentURLs []string
	}
	messages := make([]historyMessage, 0, len(rawItems))
	for _, raw := range rawItems {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id := intFromJSON(item["id"])
		if id <= after {
			continue
		}
		text, _ := item["text"].(string)
		marker, marked := strings.CutPrefix(text, vkDocsMessagePrefix)
		marker = strings.TrimSpace(marker)
		var documentURLs []string
		wantedStream := filterID == "" || !marked || marker == "" || marker == filterID
		if wantedStream && !(streamRead && c.sentMessage(peerID, id)) {
			documentURLs = vkDocURLs(item)
		}
		messages = append(messages, historyMessage{
			id:           id,
			marked:       marked,
			documentURLs: documentURLs,
		})
	}
	sort.SliceStable(messages, func(i, j int) bool { return messages[i].id < messages[j].id })
	envelopes := make([]fabric.Envelope, 0, len(messages))
poll:
	for _, message := range messages {
		beforeMessage := len(envelopes)
		for _, docURL := range message.documentURLs {
			envelope, err := c.downloadEnvelope(ctx, docURL)
			if err == nil {
				envelopes = append(envelopes, envelope)
				continue
			}
			if ctx.Err() != nil {
				return ReadResult{}, ctx.Err()
			}
			if message.marked && err.retryable {
				// Only acknowledge the fully read prefix. Return it normally so
				// the tunnel can poll again without closing the byte stream.
				log.Printf("[vk docs] %s (poll cursor held)", err)
				envelopes = envelopes[:beforeMessage]
				break poll
			}
		}
		after = message.id
	}
	return ReadResult{Envelopes: envelopes, Cursor: Cursor(strconv.Itoa(after))}, nil
}

func (c *VKDocsCarrier) Probe(ctx context.Context, endpoint Endpoint) (Metrics, error) {
	started := time.Now()
	_, err := c.Read(ctx, endpoint, "")
	if err != nil {
		return Metrics{Healthy: false, FailureReason: err.Error()}, err
	}
	return Metrics{Healthy: true, Latency: time.Since(started), LastOK: time.Now().UTC()}, nil
}

func (c *VKDocsCarrier) SafeEgressRecoveryProbe(ctx context.Context, endpoint Endpoint) (Metrics, error) {
	return c.Probe(ctx, endpoint)
}

func (c *VKDocsCarrier) DeleteMessage(ctx context.Context, endpoint Endpoint, messageID string) error {
	// VK docs carrier doesn't support message deletion
	return fmt.Errorf("delete message not implemented for vk docs carrier")
}

func (c *VKDocsCarrier) call(ctx context.Context, method string, values url.Values) (map[string]any, error) {
	return callVK(ctx, c.httpClient, c.baseURL, c.token, c.apiVersion, "WhiteTransport Go VK docs carrier", method, values)
}

type vkDocsUploadError struct {
	reason     string
	retryable  bool
	timedOut   bool
	httpStatus int
	retryAfter time.Duration
}

func (e *vkDocsUploadError) Error() string { return "vk docs upload " + e.reason }

func vkDocsUploadRequestFailure(ctx context.Context, reason string) *vkDocsUploadError {
	timedOut := ctx.Err() == context.DeadlineExceeded
	if timedOut {
		reason = "timed out"
	}
	return &vkDocsUploadError{reason: reason, retryable: true, timedOut: timedOut}
}

func (c *VKDocsCarrier) uploadDocument(ctx context.Context, uploadURL string, data []byte, filename string, onPost func()) (map[string]any, *vkDocsUploadError) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return nil, &vkDocsUploadError{reason: "multipart file creation failed"}
	}
	if _, err := part.Write(data); err != nil {
		return nil, &vkDocsUploadError{reason: "multipart file encoding failed"}
	}
	if err := writer.Close(); err != nil {
		return nil, &vkDocsUploadError{reason: "multipart encoding failed"}
	}
	ctx, cancel := context.WithTimeout(ctx, vkDocsUploadAttemptTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, &body)
	if err != nil {
		return nil, &vkDocsUploadError{reason: "invalid provider URL"}
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("User-Agent", "WhiteTransport Go VK docs carrier")
	if ctx.Err() != nil {
		return nil, vkDocsUploadRequestFailure(ctx, "request canceled")
	}
	onPost()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, vkDocsUploadRequestFailure(ctx, "request failed")
	}
	defer resp.Body.Close()
	if status := resp.StatusCode; status < 200 || status > 299 {
		return nil, &vkDocsUploadError{
			reason:     fmt.Sprintf("HTTP %d", status),
			retryable:  status == http.StatusMethodNotAllowed || vkDocsRetryableHTTPStatus(status),
			httpStatus: status,
			retryAfter: vkRetryAfter(resp.Header.Get("Retry-After")),
		}
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, vkDocsUploadRequestFailure(ctx, "response read failed")
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, &vkDocsUploadError{reason: "invalid response"}
	}
	return decoded, nil
}

func vkDocsRetryableHTTPStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500 && status < 600
}

type vkDocsAttachmentError struct {
	reason    string
	retryable bool
}

func (e *vkDocsAttachmentError) Error() string { return "attachment " + e.reason }

func (c *VKDocsCarrier) downloadEnvelope(ctx context.Context, docURL string) (fabric.Envelope, *vkDocsAttachmentError) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, docURL, nil)
	if err != nil {
		return fabric.Envelope{}, &vkDocsAttachmentError{reason: "URL invalid"}
	}
	req.Header.Set("User-Agent", "WhiteTransport Go VK docs carrier")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fabric.Envelope{}, &vkDocsAttachmentError{reason: "request failed", retryable: true}
	}
	defer resp.Body.Close()
	if status := resp.StatusCode; status < 200 || status > 299 {
		return fabric.Envelope{}, &vkDocsAttachmentError{
			reason:    fmt.Sprintf("download HTTP %d", status),
			retryable: vkDocsRetryableHTTPStatus(status),
		}
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fabric.Envelope{}, &vkDocsAttachmentError{reason: "body read failed", retryable: true}
	}
	envelope, err := decodeDocumentEnvelope(raw)
	if err != nil {
		return fabric.Envelope{}, &vkDocsAttachmentError{reason: "envelope decode failed"}
	}
	return envelope, nil
}

func vkUploadURL(response map[string]any) (string, error) {
	container, ok := response["response"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("vk docs.getMessagesUploadServer missing response object")
	}
	uploadURL, ok := container["upload_url"].(string)
	if !ok || strings.TrimSpace(uploadURL) == "" {
		return "", fmt.Errorf("vk docs.getMessagesUploadServer missing upload_url")
	}
	return uploadURL, nil
}

func vkDocAttachment(response map[string]any) (string, error) {
	container, ok := response["response"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("vk docs.save missing response object")
	}
	doc, ok := container["doc"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("vk docs.save missing doc")
	}
	ownerID := intFromJSON(doc["owner_id"])
	docID := intFromJSON(doc["id"])
	if ownerID == 0 || docID == 0 {
		return "", fmt.Errorf("vk docs.save missing owner_id or id")
	}
	return fmt.Sprintf("doc%d_%d", ownerID, docID), nil
}

func vkDocURLs(item map[string]any) []string {
	rawAttachments, ok := item["attachments"].([]any)
	if !ok {
		return nil
	}
	urls := make([]string, 0, len(rawAttachments))
	for _, raw := range rawAttachments {
		attachment, ok := raw.(map[string]any)
		if !ok || attachment["type"] != "doc" {
			continue
		}
		doc, ok := attachment["doc"].(map[string]any)
		if !ok {
			continue
		}
		docURL, ok := doc["url"].(string)
		if ok && strings.TrimSpace(docURL) != "" {
			urls = append(urls, strings.TrimSpace(docURL))
		}
	}
	return urls
}

func encodeDocumentEnvelope(envelope fabric.Envelope) ([]byte, error) {
	return json.Marshal(envelope)
}

func decodeDocumentEnvelope(data []byte) (fabric.Envelope, error) {
	var envelope fabric.Envelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fabric.Envelope{}, err
	}
	return envelope, nil
}

func sortDecodedItems(decoded []decodedCarrierItem) {
	for i := 1; i < len(decoded); i++ {
		current := decoded[i]
		j := i - 1
		for j >= 0 && decoded[j].id > current.id {
			decoded[j+1] = decoded[j]
			j--
		}
		decoded[j+1] = current
	}
}
