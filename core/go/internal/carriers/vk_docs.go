package carriers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

// VKDocsConfig contains runtime inputs for VK document bulk carriers.
type VKDocsConfig struct {
	Token        string
	APIVersion   string
	BaseURL      string
	DescriptorID string
	HTTPClient   HTTPDoer
}

// VKDocsCarrier moves envelope chunks through retained VK document attachments.
type VKDocsCarrier struct {
	token      string
	apiVersion string
	baseURL    string
	httpClient HTTPDoer
	desc       Descriptor
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
		baseURL = "https://api.vk.com/method"
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
	uploadInfo, err := c.call(ctx, "docs.getMessagesUploadServer", url.Values{
		"peer_id": {peerID},
		"type":    {"doc"},
	})
	if err != nil {
		return err
	}
	uploadURL, err := vkUploadURL(uploadInfo)
	if err != nil {
		return err
	}
	uploadResult, err := c.uploadDocument(ctx, uploadURL, data, envelope.ID+".wtmsg")
	if err != nil {
		return err
	}
	file, ok := uploadResult["file"].(string)
	if !ok || strings.TrimSpace(file) == "" {
		return fmt.Errorf("vk docs upload missing file token")
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
		"message":    {"wtbulk1 " + envelope.ID},
		"attachment": {attachment},
		"random_id":  {strconv.FormatInt(rand.New(rand.NewSource(time.Now().UnixNano())).Int63(), 10)},
	})
	if err != nil {
		return err
	}
	if _, ok := sendResult["response"]; !ok {
		return fmt.Errorf("vk messages.send missing response")
	}
	return nil
}

func (c *VKDocsCarrier) Read(ctx context.Context, endpoint Endpoint, cursor Cursor) (ReadResult, error) {
	peerID, err := vkPeerID(endpoint)
	if err != nil {
		return ReadResult{}, err
	}
	response, err := c.call(ctx, "messages.getHistory", url.Values{
		"peer_id": {peerID},
		"count":   {"100"},
	})
	if err != nil {
		return ReadResult{}, err
	}
	container, ok := response["response"].(map[string]any)
	if !ok {
		return ReadResult{}, fmt.Errorf("vk messages.getHistory missing response object")
	}
	rawItems, ok := container["items"].([]any)
	if !ok {
		return ReadResult{}, fmt.Errorf("vk messages.getHistory missing items")
	}
	after, _ := strconv.Atoi(string(cursor))
	maxID := after
	decoded := make([]decodedCarrierItem, 0, len(rawItems))
	for _, raw := range rawItems {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id := intFromJSON(item["id"])
		if id <= after {
			continue
		}
		if id > maxID {
			maxID = id
		}
		for _, docURL := range vkDocURLs(item) {
			envelope, err := c.downloadEnvelope(ctx, docURL)
			if err != nil {
				continue
			}
			decoded = append(decoded, decodedCarrierItem{id: id, envelope: envelope})
		}
	}
	sortDecodedItems(decoded)
	envelopes := make([]fabric.Envelope, 0, len(decoded))
	for _, item := range decoded {
		envelopes = append(envelopes, item.envelope)
	}
	return ReadResult{Envelopes: envelopes, Cursor: Cursor(strconv.Itoa(maxID))}, nil
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
	values.Set("access_token", c.token)
	values.Set("v", c.apiVersion)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/"+method, bytes.NewBufferString(values.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "WhiteTransport Go VK docs carrier")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("vk %s HTTP %d", method, resp.StatusCode)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, err
	}
	if rawError, ok := decoded["error"]; ok {
		return nil, fmt.Errorf("vk %s error: %v", method, rawError)
	}
	return decoded, nil
}

func (c *VKDocsCarrier) uploadDocument(ctx context.Context, uploadURL string, data []byte, filename string) (map[string]any, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(data); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("User-Agent", "WhiteTransport Go VK docs carrier")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("vk docs upload HTTP %d", resp.StatusCode)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func (c *VKDocsCarrier) downloadEnvelope(ctx context.Context, docURL string) (fabric.Envelope, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, docURL, nil)
	if err != nil {
		return fabric.Envelope{}, err
	}
	req.Header.Set("User-Agent", "WhiteTransport Go VK docs carrier")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fabric.Envelope{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fabric.Envelope{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fabric.Envelope{}, fmt.Errorf("vk doc download HTTP %d", resp.StatusCode)
	}
	return decodeDocumentEnvelope(raw)
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
