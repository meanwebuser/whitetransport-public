package carriers

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

// OKDocsConfig contains runtime inputs for the OK fb.do document carrier.
type OKDocsConfig struct {
	AccessToken      string
	ApplicationKey   string
	SessionSecretKey string
	BaseURL          string
	DescriptorID     string
	HTTPClient       HTTPDoer
}

// OKDocsCarrier moves envelope chunks through OK document attachments.
type OKDocsCarrier struct {
	accessToken      string
	applicationKey   string
	sessionSecretKey string
	baseURL          string
	httpClient       HTTPDoer
	desc             Descriptor
}

// NewOKDocsCarrier creates the OK document carrier for ok.docs.256.
func NewOKDocsCarrier(cfg OKDocsConfig) (*OKDocsCarrier, error) {
	if strings.TrimSpace(cfg.AccessToken) == "" {
		return nil, fmt.Errorf("ok docs access token is required")
	}
	if strings.TrimSpace(cfg.ApplicationKey) == "" {
		return nil, fmt.Errorf("ok docs application key is required")
	}
	if strings.TrimSpace(cfg.SessionSecretKey) == "" {
		return nil, fmt.Errorf("ok docs session secret key is required")
	}
	descriptorID := cfg.DescriptorID
	if descriptorID == "" {
		descriptorID = CarrierOKDocs256
	}
	if descriptorID != CarrierOKDocs256 {
		return nil, fmt.Errorf("ok docs descriptor must be %s", CarrierOKDocs256)
	}
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		baseURL = "https://api.ok.ru/fb.do"
	}
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	desc, err := FindStandardDescriptor(descriptorID)
	if err != nil {
		return nil, err
	}
	return &OKDocsCarrier{
		accessToken:      cfg.AccessToken,
		applicationKey:   cfg.ApplicationKey,
		sessionSecretKey: cfg.SessionSecretKey,
		baseURL:          baseURL,
		httpClient:       client,
		desc:             desc,
	}, nil
}

func (c *OKDocsCarrier) Descriptor() Descriptor { return c.desc }
func (c *OKDocsCarrier) IsNative()              {}

func (c *OKDocsCarrier) Write(ctx context.Context, endpoint Endpoint, envelope fabric.Envelope) error {
	target, err := okDocTarget(endpoint)
	if err != nil {
		return err
	}
	data, err := encodeDocumentEnvelope(envelope)
	if err != nil {
		return err
	}
	if c.desc.Limits.MaxPayloadBytes > 0 && len(data) > c.desc.Limits.MaxPayloadBytes {
		return fmt.Errorf("ok docs envelope size %d exceeds carrier limit %d", len(data), c.desc.Limits.MaxPayloadBytes)
	}
	uploadParams := map[string]string{"count": "1"}
	if target.GroupID != "" {
		uploadParams["group_id"] = target.GroupID
	}
	uploadInfo, err := c.callAPI(ctx, "docs.getUploadUrl", uploadParams)
	if err != nil {
		return err
	}
	if err := checkOKFBResponse("docs.getUploadUrl", uploadInfo); err != nil {
		return err
	}
	uploadURL, err := okUploadURL(uploadInfo)
	if err != nil {
		return err
	}
	uploadResult, err := c.uploadDocument(ctx, uploadURL, data, envelope.ID+".wtmsg")
	if err != nil {
		return err
	}
	docID, token, err := okUploadedDoc(uploadResult)
	if err != nil {
		return err
	}
	commitResult, err := c.callAPI(ctx, "docs.commit", map[string]string{
		"doc_id": docID,
		"token":  token,
	})
	if err != nil {
		return err
	}
	if err := checkOKFBResponse("docs.commit", commitResult); err != nil {
		return err
	}
	committedID, err := okCommittedDocID(commitResult)
	if err != nil {
		return err
	}
	attachment, err := json.Marshal([]map[string]string{{"type": "doc", "id": committedID}})
	if err != nil {
		return err
	}
	sendResult, err := c.callAPI(ctx, "messages.send", map[string]string{
		"chat":         target.Chat,
		"recipient_id": target.RecipientID,
		"message":      "wtbulk1 " + envelope.ID,
		"attachment":   string(attachment),
	})
	if err != nil {
		return err
	}
	if err := checkOKFBResponse("messages.send", sendResult); err != nil {
		return err
	}
	return nil
}

func (c *OKDocsCarrier) Read(ctx context.Context, endpoint Endpoint, cursor Cursor) (ReadResult, error) {
	target, err := okDocTarget(endpoint)
	if err != nil {
		return ReadResult{}, err
	}
	response, err := c.callAPI(ctx, "messages.getHistory", map[string]string{
		"chat":  target.Chat,
		"count": "100",
	})
	if err != nil {
		return ReadResult{}, err
	}
	if err := checkOKFBResponse("messages.getHistory", response); err != nil {
		return ReadResult{}, err
	}
	messages, err := okFBMessages(response)
	if err != nil {
		return ReadResult{}, err
	}
	after, _ := strconv.Atoi(string(cursor))
	maxID := after
	decoded := make([]decodedCarrierItem, 0, len(messages))
	for _, message := range messages {
		id := okMessageID(message)
		if id <= after {
			continue
		}
		if id > maxID {
			maxID = id
		}
		for _, docURL := range okDocAttachmentURLs(message) {
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

func (c *OKDocsCarrier) Probe(ctx context.Context, endpoint Endpoint) (Metrics, error) {
	started := time.Now()
	_, err := c.Read(ctx, endpoint, "")
	if err != nil {
		return Metrics{Healthy: false, FailureReason: err.Error()}, err
	}
	return Metrics{Healthy: true, Latency: time.Since(started), LastOK: time.Now().UTC()}, nil
}

func (c *OKDocsCarrier) DeleteMessage(ctx context.Context, endpoint Endpoint, messageID string) error {
	// OK docs carrier doesn't support message deletion
	return fmt.Errorf("delete message not implemented for ok docs carrier")
}

func (c *OKDocsCarrier) callAPI(ctx context.Context, method string, params map[string]string) (map[string]any, error) {
	values := map[string]string{
		"access_token":    c.accessToken,
		"application_key": c.applicationKey,
		"format":          "json",
		"method":          method,
	}
	for key, value := range params {
		if strings.TrimSpace(value) != "" {
			values[key] = value
		}
	}
	values["sig"] = okSignature(values, c.sessionSecretKey)
	query := url.Values{}
	for key, value := range values {
		query.Set(key, value)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "WhiteTransport Go OK docs carrier")
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
		return nil, fmt.Errorf("ok fb %s HTTP %d", method, resp.StatusCode)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func (c *OKDocsCarrier) uploadDocument(ctx context.Context, uploadURL string, data []byte, filename string) (map[string]any, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file1", filename)
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
	req.Header.Set("User-Agent", "WhiteTransport Go OK docs carrier")
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
		return nil, fmt.Errorf("ok docs upload HTTP %d", resp.StatusCode)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func (c *OKDocsCarrier) downloadEnvelope(ctx context.Context, docURL string) (fabric.Envelope, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, docURL, nil)
	if err != nil {
		return fabric.Envelope{}, err
	}
	req.Header.Set("User-Agent", "WhiteTransport Go OK docs carrier")
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
		return fabric.Envelope{}, fmt.Errorf("ok doc download HTTP %d", resp.StatusCode)
	}
	return decodeDocumentEnvelope(raw)
}

type okDocumentTarget struct {
	Chat        string
	RecipientID string
	GroupID     string
}

func okDocTarget(endpoint Endpoint) (okDocumentTarget, error) {
	chat := strings.TrimSpace(endpoint.Address)
	if chat == "" && endpoint.Metadata != nil {
		chat = strings.TrimSpace(endpoint.Metadata["chat"])
	}
	if chat == "" && endpoint.Metadata != nil {
		chat = strings.TrimSpace(endpoint.Metadata["chat_id"])
	}
	if chat == "" {
		return okDocumentTarget{}, fmt.Errorf("ok docs endpoint requires chat address")
	}
	recipientID := ""
	if endpoint.Metadata != nil {
		recipientID = strings.TrimSpace(endpoint.Metadata["recipient_id"])
	}
	if recipientID == "" {
		recipientID = strings.TrimPrefix(chat, "chat:")
	}
	groupID := ""
	if strings.HasPrefix(chat, "chat:C") {
		groupID = strings.TrimPrefix(chat, "chat:C")
	}
	return okDocumentTarget{Chat: chat, RecipientID: recipientID, GroupID: groupID}, nil
}

func okUploadURL(response map[string]any) (string, error) {
	uploadURL, ok := response["upload_url"].(string)
	if ok && strings.TrimSpace(uploadURL) != "" {
		return strings.TrimSpace(uploadURL), nil
	}
	container, ok := response["response"].(map[string]any)
	if ok {
		uploadURL, ok := container["upload_url"].(string)
		if ok && strings.TrimSpace(uploadURL) != "" {
			return strings.TrimSpace(uploadURL), nil
		}
	}
	return "", fmt.Errorf("ok docs.getUploadUrl missing upload_url")
}

func okUploadedDoc(response map[string]any) (string, string, error) {
	docs, err := okDocsList(response)
	if err != nil {
		return "", "", err
	}
	if len(docs) == 0 {
		return "", "", fmt.Errorf("ok docs upload returned no docs")
	}
	id := strings.TrimSpace(stringFromJSON(docs[0]["id"]))
	token := strings.TrimSpace(stringFromJSON(docs[0]["token"]))
	if id == "" || token == "" {
		return "", "", fmt.Errorf("ok docs upload missing id or token")
	}
	return id, token, nil
}

func okDocsList(response map[string]any) ([]map[string]any, error) {
	if docs, ok := mapItems(response["docs"]); ok {
		return docs, nil
	}
	container, ok := response["response"].(map[string]any)
	if ok {
		if docs, ok := mapItems(container["docs"]); ok {
			return docs, nil
		}
	}
	return nil, fmt.Errorf("ok docs upload missing docs")
}

func okCommittedDocID(response map[string]any) (string, error) {
	if id := strings.TrimSpace(stringFromJSON(response["id"])); id != "" {
		return id, nil
	}
	container, ok := response["response"].(map[string]any)
	if ok {
		if id := strings.TrimSpace(stringFromJSON(container["id"])); id != "" {
			return id, nil
		}
	}
	return "", fmt.Errorf("ok docs.commit missing id")
}

func okFBMessages(response map[string]any) ([]map[string]any, error) {
	if messages, ok := mapItems(response["messages"]); ok {
		return messages, nil
	}
	container, ok := response["response"].(map[string]any)
	if ok {
		if messages, ok := mapItems(container["messages"]); ok {
			return messages, nil
		}
	}
	return nil, fmt.Errorf("ok messages.getHistory missing messages")
}

func okMessageID(message map[string]any) int {
	for _, key := range []string{"messageId", "message_id", "id"} {
		if id := intFromJSON(message[key]); id > 0 {
			return id
		}
	}
	return 0
}

func okDocAttachmentURLs(message map[string]any) []string {
	attachments := make([]map[string]any, 0)
	if attachment, ok := message["attachment"].(map[string]any); ok {
		attachments = append(attachments, attachment)
	}
	if attachmentList, ok := mapItems(message["attachments"]); ok {
		attachments = append(attachments, attachmentList...)
	}
	urls := make([]string, 0, len(attachments))
	for _, attachment := range attachments {
		if attachment["type"] != "doc" {
			continue
		}
		doc, ok := attachment["doc"].(map[string]any)
		if !ok {
			continue
		}
		docURL := strings.TrimSpace(stringFromJSON(doc["url"]))
		if docURL != "" {
			urls = append(urls, docURL)
		}
	}
	return urls
}

func checkOKFBResponse(action string, response map[string]any) error {
	if rawError, ok := response["error_code"]; ok {
		return fmt.Errorf("ok fb %s error_code: %v", action, rawError)
	}
	if rawError, ok := response["error"]; ok {
		return fmt.Errorf("ok fb %s error: %v", action, rawError)
	}
	return nil
}

func okSignature(values map[string]string, sessionSecretKey string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		if key != "sig" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, key := range keys {
		builder.WriteString(key)
		builder.WriteString("=")
		builder.WriteString(values[key])
	}
	sum := md5.Sum([]byte(builder.String() + sessionSecretKey))
	return hex.EncodeToString(sum[:])
}

func stringFromJSON(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return strconv.FormatInt(int64(typed), 10)
	case int:
		return strconv.Itoa(typed)
	default:
		return ""
	}
}
