package carriers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

const (
	defaultYandexDiskBaseURL         = "https://cloud-api.yandex.net/v1/disk"
	defaultYandexDiskBasePath        = "/ytp"
	defaultYandexDiskMaxFileSize     = 1 << 20
	defaultYandexDiskMinSendInterval = 500 * time.Millisecond
)

// YandexDiskConfig contains runtime inputs for the Yandex Disk file carrier.
type YandexDiskConfig struct {
	OAuthToken       string
	CookieHeader     string // session cookies as fallback when OAuth is unavailable
	BaseURL          string
	BasePath         string
	MaxFileSizeBytes int
	CleanupAfterRead bool
	MinSendInterval  time.Duration
	HTTPClient       HTTPDoer
}

// YandexDiskCarrier moves envelope chunks through Yandex Disk files.
type YandexDiskCarrier struct {
	token            string
	cookieHeader     string // session cookies (used when token is empty)
	baseURL          string
	basePath         string
	maxFileSizeBytes int
	cleanupAfterRead bool
	minSendInterval  time.Duration
	lastSendAt       time.Time
	httpClient       HTTPDoer
	desc             Descriptor
}

type yandexUploadLink struct {
	Href string `json:"href"`
}

type yandexDownloadLink struct {
	Href string `json:"href"`
}

type yandexResourceList struct {
	Embedded struct {
		Items []yandexResource `json:"items"`
	} `json:"_embedded"`
}

type yandexResource struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Modified string `json:"modified"`
	Type     string `json:"type"`
}

type yandexDiskCursor struct {
	LastModified string   `json:"last_modified"`
	SeenNames    []string `json:"seen_names,omitempty"`
}

// NewYandexDiskCarrier creates a durable file/object carrier backed by Yandex Disk.
func NewYandexDiskCarrier(cfg YandexDiskConfig) (*YandexDiskCarrier, error) {
	if strings.TrimSpace(cfg.OAuthToken) == "" && strings.TrimSpace(cfg.CookieHeader) == "" {
		return nil, fmt.Errorf("yandex disk requires oauth token or session cookies")
	}
	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	if baseURL == "" {
		baseURL = defaultYandexDiskBaseURL
	}
	basePath := cleanYandexPath(cfg.BasePath)
	if basePath == "" {
		basePath = defaultYandexDiskBasePath
	}
	maxFileSize := cfg.MaxFileSizeBytes
	if maxFileSize <= 0 {
		maxFileSize = defaultYandexDiskMaxFileSize
	}
	minSendInterval := cfg.MinSendInterval
	if minSendInterval <= 0 {
		minSendInterval = defaultYandexDiskMinSendInterval
	}
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	desc, err := FindStandardDescriptor(CarrierYandexDisk)
	if err != nil {
		return nil, err
	}
	desc.Limits.MaxPayloadBytes = maxFileSize
	desc.Limits.ChunkPayloadBytes = maxFileSize
	return &YandexDiskCarrier{
		token:            cfg.OAuthToken,
		cookieHeader:     cfg.CookieHeader,
		baseURL:          baseURL,
		basePath:         basePath,
		maxFileSizeBytes: maxFileSize,
		cleanupAfterRead: cfg.CleanupAfterRead,
		minSendInterval:  minSendInterval,
		httpClient:       client,
		desc:             desc,
	}, nil
}

func (c *YandexDiskCarrier) Descriptor() Descriptor { return c.desc }
func (c *YandexDiskCarrier) IsNative()              {}

func (c *YandexDiskCarrier) Write(ctx context.Context, endpoint Endpoint, envelope fabric.Envelope) error {
	data, err := encodeDocumentEnvelope(envelope)
	if err != nil {
		return err
	}
	if len(data) > c.maxFileSizeBytes {
		return fmt.Errorf("yandex disk envelope size %d exceeds carrier limit %d", len(data), c.maxFileSizeBytes)
	}
	if err := c.waitSendBudget(ctx); err != nil {
		return err
	}
	folder := c.folderPath(endpoint)
	if err := c.ensureFolder(ctx, c.basePath); err != nil {
		return err
	}
	if err := c.ensureFolder(ctx, folder); err != nil {
		return err
	}
	filePath := path.Join(folder, yandexDiskFileName(envelope.ID))
	var upload yandexUploadLink
	if err := c.apiJSON(ctx, http.MethodGet, "/resources/upload", url.Values{
		"path":      {filePath},
		"overwrite": {"true"},
	}, nil, &upload); err != nil {
		return err
	}
	if strings.TrimSpace(upload.Href) == "" {
		return fmt.Errorf("yandex disk upload link missing href")
	}
	if err := c.putUpload(ctx, upload.Href, data); err != nil {
		return err
	}
	c.lastSendAt = time.Now()
	return nil
}

func (c *YandexDiskCarrier) Read(ctx context.Context, endpoint Endpoint, cursor Cursor) (ReadResult, error) {
	currentCursor, err := parseYandexDiskCursor(cursor)
	if err != nil {
		return ReadResult{}, err
	}
	folder := c.folderPath(endpoint)
	var listing yandexResourceList
	err = c.apiJSON(ctx, http.MethodGet, "/resources", url.Values{
		"path":  {folder},
		"limit": {"100"},
		"sort":  {"modified"},
	}, nil, &listing)
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			return ReadResult{Cursor: cursor}, nil
		}
		return ReadResult{}, err
	}
	files := newYandexDiskFiles(listing.Embedded.Items, currentCursor)
	envelopes := make([]fabric.Envelope, 0, len(files))
	processed := make([]string, 0, len(files))
	for _, file := range files {
		raw, err := c.downloadFile(ctx, file.Path)
		if err != nil {
			return ReadResult{}, err
		}
		env, err := decodeDocumentEnvelope(raw)
		if err != nil {
			return ReadResult{}, fmt.Errorf("decode yandex disk file %s: %w", file.Name, err)
		}
		envelopes = append(envelopes, env)
		processed = append(processed, file.Path)
	}
	if c.cleanupAfterRead {
		for _, filePath := range processed {
			if err := c.DeleteMessage(ctx, endpoint, filePath); err != nil {
				return ReadResult{}, err
			}
		}
	}
	nextCursor := buildYandexDiskCursor(currentCursor, files)
	encodedCursor, err := json.Marshal(nextCursor)
	if err != nil {
		return ReadResult{}, err
	}
	return ReadResult{Envelopes: envelopes, Cursor: Cursor(encodedCursor)}, nil
}

func (c *YandexDiskCarrier) Probe(ctx context.Context, endpoint Endpoint) (Metrics, error) {
	start := time.Now()
	_, err := c.Read(ctx, endpoint, "")
	if err != nil {
		return Metrics{Healthy: false, FailureReason: err.Error()}, err
	}
	return Metrics{Healthy: true, Latency: time.Since(start), LastOK: time.Now(), BandwidthBPS: c.desc.Metrics.BandwidthBPS}, nil
}

func (c *YandexDiskCarrier) SafeEgressRecoveryProbe(ctx context.Context, endpoint Endpoint) (Metrics, error) {
	return c.Probe(ctx, endpoint)
}

func (c *YandexDiskCarrier) DeleteMessage(ctx context.Context, _ Endpoint, messageID string) error {
	if strings.TrimSpace(messageID) == "" {
		return fmt.Errorf("yandex disk message id is required")
	}
	return c.apiJSON(ctx, http.MethodDelete, "/resources", url.Values{
		"path":        {messageID},
		"permanently": {"true"},
	}, nil, nil)
}

func (c *YandexDiskCarrier) waitSendBudget(ctx context.Context) error {
	wait := c.lastSendAt.Add(c.minSendInterval).Sub(time.Now())
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *YandexDiskCarrier) ensureFolder(ctx context.Context, folder string) error {
	err := c.apiJSON(ctx, http.MethodPut, "/resources", url.Values{"path": {folder}}, nil, nil)
	if err != nil && !strings.Contains(err.Error(), "409") {
		return err
	}
	return nil
}

func (c *YandexDiskCarrier) downloadFile(ctx context.Context, diskPath string) ([]byte, error) {
	var link yandexDownloadLink
	if err := c.apiJSON(ctx, http.MethodGet, "/resources/download", url.Values{"path": {diskPath}}, nil, &link); err != nil {
		return nil, err
	}
	if strings.TrimSpace(link.Href) == "" {
		return nil, fmt.Errorf("yandex disk download link missing href")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link.Href, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("yandex disk download failed: HTTP %d", res.StatusCode)
	}
	return io.ReadAll(res.Body)
}

func (c *YandexDiskCarrier) putUpload(ctx context.Context, uploadURL string, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/octet-stream")
	res, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		return fmt.Errorf("yandex disk upload failed: HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (c *YandexDiskCarrier) apiJSON(ctx context.Context, method string, apiPath string, values url.Values, body io.Reader, out any) error {
	endpoint := c.baseURL + apiPath
	if len(values) > 0 {
		endpoint += "?" + values.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("authorization", "OAuth "+c.token)
	} else if c.cookieHeader != "" {
		req.Header.Set("cookie", c.cookieHeader)
	}
	res, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNoContent {
		return nil
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		return fmt.Errorf("yandex disk API %s %s failed: HTTP %d: %s", method, apiPath, res.StatusCode, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

func (c *YandexDiskCarrier) folderPath(endpoint Endpoint) string {
	address := strings.TrimSpace(endpoint.Address)
	if address == "" {
		address = "default"
	}
	return path.Join(c.basePath, sanitizeYandexDiskMailbox(address))
}

func cleanYandexPath(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if !strings.HasPrefix(trimmed, "/") {
		trimmed = "/" + trimmed
	}
	return path.Clean(trimmed)
}

func sanitizeYandexDiskMailbox(value string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_", "..", "_")
	safe := replacer.Replace(value)
	if safe == "" || safe == "." {
		return "default"
	}
	return safe
}

func yandexDiskFileName(envelopeID string) string {
	base := sanitizeYandexDiskMailbox(envelopeID)
	if len(base) > 80 {
		sum := sha256.Sum256([]byte(envelopeID))
		base = base[:48] + "_" + hex.EncodeToString(sum[:])[:16]
	}
	return fmt.Sprintf("%d_%s.wtmsg", time.Now().UnixMilli(), base)
}

func parseYandexDiskCursor(cursor Cursor) (yandexDiskCursor, error) {
	if strings.TrimSpace(string(cursor)) == "" {
		return yandexDiskCursor{}, nil
	}
	var out yandexDiskCursor
	if err := json.Unmarshal([]byte(cursor), &out); err != nil {
		return yandexDiskCursor{}, fmt.Errorf("invalid yandex disk cursor: %w", err)
	}
	return out, nil
}

func newYandexDiskFiles(items []yandexResource, cursor yandexDiskCursor) []yandexResource {
	seen := make(map[string]struct{}, len(cursor.SeenNames))
	for _, name := range cursor.SeenNames {
		seen[name] = struct{}{}
	}
	files := make([]yandexResource, 0, len(items))
	for _, item := range items {
		if item.Type != "file" || !strings.HasSuffix(item.Name, ".wtmsg") {
			continue
		}
		if item.Modified < cursor.LastModified {
			continue
		}
		if item.Modified == cursor.LastModified {
			if _, ok := seen[item.Name]; ok {
				continue
			}
		}
		files = append(files, item)
	}
	sort.SliceStable(files, func(i int, j int) bool {
		if files[i].Modified == files[j].Modified {
			return files[i].Name < files[j].Name
		}
		return files[i].Modified < files[j].Modified
	})
	return files
}

func buildYandexDiskCursor(previous yandexDiskCursor, files []yandexResource) yandexDiskCursor {
	if len(files) == 0 {
		return previous
	}
	lastModified := files[len(files)-1].Modified
	names := make([]string, 0, len(files))
	for _, file := range files {
		if file.Modified == lastModified {
			names = append(names, file.Name)
		}
	}
	return yandexDiskCursor{LastModified: lastModified, SeenNames: names}
}
