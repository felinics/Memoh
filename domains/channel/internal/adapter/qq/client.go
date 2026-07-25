package qq

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/memohai/memoh/internal/redact"
)

type qqClient struct {
	appID        string
	clientSecret string
	httpClient   *http.Client
	logger       interface {
		Debug(string, ...any)
	}
	apiBaseURL string
	tokenURL   string

	tokenMu   sync.Mutex
	token     string
	expiresAt time.Time

	msgSeqMu sync.Mutex
	msgSeq   map[string]int
}

func (c *qqClient) matches(cfg Config) bool {
	return c.appID == cfg.AppID && c.clientSecret == cfg.AppSecret
}

func (c *qqClient) clearToken() {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	c.token = ""
	c.expiresAt = time.Time{}
}

func (c *qqClient) accessToken(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	if c.token != "" && time.Now().Before(c.expiresAt.Add(-5*time.Minute)) {
		return c.token, nil
	}

	payload := map[string]string{
		"appId":        c.appID,
		"clientSecret": c.clientSecret,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(c.tokenURL)
	if err != nil || (u.Scheme != "https" && !isLocalhost(u.Host)) {
		return "", fmt.Errorf("invalid token url: %s", c.tokenURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req) //nolint:gosec // token URL is validated to https or localhost above
	if err != nil {
		return "", fmt.Errorf("qq token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("qq token read: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("qq token request failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var result map[string]json.RawMessage
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("qq token decode: %w", err)
	}
	tokenBytes, ok := result["access_token"]
	if !ok {
		return "", errors.New("qq token response missing access_token")
	}
	var token string
	if err := json.Unmarshal(tokenBytes, &token); err != nil {
		return "", fmt.Errorf("qq token decode: %w", err)
	}
	if strings.TrimSpace(token) == "" {
		return "", errors.New("qq token response missing access_token")
	}
	expiresIn := parseQQExpiresIn(result["expires_in"])
	if expiresIn <= 0 {
		expiresIn = 7200
	}
	c.token = token
	c.expiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second)
	redact.SetSecrets("qq-token:"+c.appID, c.clientSecret, c.token)
	return c.token, nil
}

func (c *qqClient) gatewayURL(ctx context.Context) (string, error) {
	var result struct {
		URL string `json:"url"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/gateway", nil, &result); err != nil {
		return "", err
	}
	if strings.TrimSpace(result.URL) == "" {
		return "", errors.New("qq gateway response missing url")
	}
	return result.URL, nil
}

func (c *qqClient) nextMsgSeq(replyTo string) int {
	if strings.TrimSpace(replyTo) == "" {
		return 1
	}
	c.msgSeqMu.Lock()
	defer c.msgSeqMu.Unlock()

	next := c.msgSeq[replyTo] + 1
	c.msgSeq[replyTo] = next
	if len(c.msgSeq) > 1024 {
		for key := range c.msgSeq {
			delete(c.msgSeq, key)
			if len(c.msgSeq) <= 512 {
				break
			}
		}
	}
	return next
}

func parseQQExpiresIn(raw json.RawMessage) int {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return 0
	}

	var numeric int
	if err := json.Unmarshal(raw, &numeric); err == nil {
		return numeric
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		value, err := strconv.Atoi(strings.TrimSpace(text))
		if err == nil {
			return value
		}
	}

	return 0
}

func (c *qqClient) doJSON(ctx context.Context, method, path string, payload any, out any) error {
	return c.doJSONWithRetry(ctx, method, c.apiBaseURL+path, payload, out, true)
}

func (c *qqClient) doJSONWithRetry(ctx context.Context, method, url string, payload any, out any, auth bool) error {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		lastErr = c.doJSONOnce(ctx, method, url, payload, out, auth)
		if lastErr == nil {
			return nil
		}
		if !auth || !strings.Contains(lastErr.Error(), "status=401") {
			return lastErr
		}
		c.clearToken()
	}
	return lastErr
}

func (c *qqClient) doJSONOnce(ctx context.Context, method, requestURL string, payload any, out any, auth bool) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}

	u, err := url.Parse(requestURL)
	if err != nil || (u.Scheme != "https" && !isLocalhost(u.Host)) {
		return fmt.Errorf("invalid api url: %s", requestURL)
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	if auth {
		token, err := c.accessToken(ctx)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "QQBot "+token)
	}

	resp, err := c.httpClient.Do(req) //nolint:gosec // requestURL is validated to https or localhost above
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf(
			"qq api request failed: method=%s url=%s status=%d body=%s",
			method,
			requestURL,
			resp.StatusCode,
			strings.TrimSpace(string(raw)),
		)
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("qq api decode failed: %w", err)
	}
	return nil
}

type qqMessageResponse struct {
	ID        string `json:"id"`
	Timestamp any    `json:"timestamp"`
}

type qqUploadResponse struct {
	FileUUID string `json:"file_uuid"`
	FileInfo string `json:"file_info"`
	TTL      int    `json:"ttl"`
}

func (c *qqClient) sendText(ctx context.Context, target qqTarget, text string, replyTo string, markdown bool) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	switch target.Kind {
	case qqTargetC2C:
		if replyTo == "" {
			return c.sendProactive(ctx, "/v2/users/"+target.ID+"/messages", text, markdown)
		}
		body := buildReplyTextBody(text, replyTo, c.nextMsgSeq(replyTo), markdown)
		return c.doJSON(ctx, http.MethodPost, "/v2/users/"+target.ID+"/messages", body, &qqMessageResponse{})
	case qqTargetGroup:
		if replyTo == "" {
			return c.sendProactive(ctx, "/v2/groups/"+target.ID+"/messages", text, markdown)
		}
		body := buildReplyTextBody(text, replyTo, c.nextMsgSeq(replyTo), markdown)
		return c.doJSON(ctx, http.MethodPost, "/v2/groups/"+target.ID+"/messages", body, &qqMessageResponse{})
	case qqTargetChannel:
		body := map[string]any{"content": text}
		if strings.TrimSpace(replyTo) != "" {
			replyID := strings.TrimSpace(replyTo)
			body["msg_id"] = replyID
			body["message_reference"] = map[string]any{"message_id": replyID}
		}
		return c.doJSON(ctx, http.MethodPost, "/channels/"+target.ID+"/messages", body, &qqMessageResponse{})
	default:
		return fmt.Errorf("unsupported qq target kind: %s", target.Kind)
	}
}

func (c *qqClient) sendProactive(ctx context.Context, path, text string, markdown bool) error {
	body := map[string]any{}
	if markdown {
		body["markdown"] = map[string]any{"content": text}
		body["msg_type"] = 2
	} else {
		body["content"] = text
		body["msg_type"] = 0
	}
	return c.doJSON(ctx, http.MethodPost, path, body, &qqMessageResponse{})
}

func buildReplyTextBody(text, replyTo string, seq int, markdown bool) map[string]any {
	body := map[string]any{
		"msg_id":  strings.TrimSpace(replyTo),
		"msg_seq": seq,
	}
	if markdown {
		body["markdown"] = map[string]any{"content": text}
		body["msg_type"] = 2
	} else {
		body["content"] = text
		body["msg_type"] = 0
	}
	return body
}

func (c *qqClient) sendInputHint(ctx context.Context, openID, replyTo string) error {
	if strings.TrimSpace(openID) == "" || strings.TrimSpace(replyTo) == "" {
		return nil
	}
	body := map[string]any{
		"msg_type": 6,
		"input_notify": map[string]any{
			"input_type":   1,
			"input_second": 60,
		},
		"msg_seq": c.nextMsgSeq(replyTo),
		"msg_id":  strings.TrimSpace(replyTo),
	}
	return c.doJSON(ctx, http.MethodPost, "/v2/users/"+openID+"/messages", body, nil)
}

func (c *qqClient) uploadMedia(ctx context.Context, target qqTarget, fileType int, rawBase64, fileName string) (string, error) {
	rawBase64 = strings.TrimSpace(rawBase64)
	if rawBase64 == "" {
		return "", errors.New("qq upload requires file_data")
	}
	body := map[string]any{
		"file_type":    fileType,
		"srv_send_msg": false,
	}
	body["file_data"] = rawBase64
	if fileType == qqMediaTypeFile && strings.TrimSpace(fileName) != "" {
		body["file_name"] = strings.TrimSpace(fileName)
	}

	var path string
	switch target.Kind {
	case qqTargetC2C:
		path = "/v2/users/" + target.ID + "/files"
	case qqTargetGroup:
		path = "/v2/groups/" + target.ID + "/files"
	default:
		return "", fmt.Errorf("qq upload not supported for target kind: %s", target.Kind)
	}

	var result qqUploadResponse
	if err := c.doJSON(ctx, http.MethodPost, path, body, &result); err != nil {
		return "", err
	}
	if strings.TrimSpace(result.FileInfo) == "" {
		return "", errors.New("qq upload response missing file_info")
	}
	return result.FileInfo, nil
}

func (c *qqClient) sendMedia(ctx context.Context, target qqTarget, fileInfo, replyTo, content string) error {
	body := map[string]any{
		"msg_type": 7,
		"media": map[string]any{
			"file_info": fileInfo,
		},
	}
	if strings.TrimSpace(content) != "" {
		body["content"] = strings.TrimSpace(content)
	}
	if strings.TrimSpace(replyTo) != "" {
		body["msg_id"] = strings.TrimSpace(replyTo)
		body["msg_seq"] = c.nextMsgSeq(replyTo)
	} else {
		body["msg_seq"] = 1
	}

	switch target.Kind {
	case qqTargetC2C:
		return c.doJSON(ctx, http.MethodPost, "/v2/users/"+target.ID+"/messages", body, &qqMessageResponse{})
	case qqTargetGroup:
		return c.doJSON(ctx, http.MethodPost, "/v2/groups/"+target.ID+"/messages", body, &qqMessageResponse{})
	default:
		return fmt.Errorf("qq media send not supported for target kind: %s", target.Kind)
	}
}

func isLocalhost(host string) bool {
	host = strings.ToLower(host)
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	if strings.HasPrefix(host, "127.0.0.1:") || strings.HasPrefix(host, "[::1]:") || strings.HasPrefix(host, "localhost:") {
		return true
	}
	return false
}
