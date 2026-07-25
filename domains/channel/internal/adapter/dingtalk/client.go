package dingtalk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	defaultAPIBase = "https://api.dingtalk.com"
	// tokenTTL is slightly under 2h to avoid races near the expiry boundary.
	tokenTTL = 110 * time.Minute
)

// apiClient handles DingTalk OpenAPI authentication and message delivery.
type apiClient struct {
	appKey    string
	appSecret string
	base      string

	mu       sync.RWMutex
	token    string
	tokenExp time.Time

	http *http.Client
}

func newAPIClient(appKey, appSecret string) *apiClient {
	return &apiClient{
		appKey:    appKey,
		appSecret: appSecret,
		base:      defaultAPIBase,
		http: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

type tokenResponse struct {
	AccessToken string `json:"accessToken"` //nolint:gosec // G117: DingTalk API response field, not a credential stored by us
	ExpireIn    int    `json:"expireIn"`
}

// getToken returns a valid access token, refreshing it when necessary.
func (c *apiClient) getToken(ctx context.Context) (string, error) {
	c.mu.RLock()
	if c.token != "" && time.Now().Before(c.tokenExp) {
		token := c.token
		c.mu.RUnlock()
		return token, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	// Double-check after acquiring write lock.
	if c.token != "" && time.Now().Before(c.tokenExp) {
		return c.token, nil
	}

	body, _ := json.Marshal(map[string]string{
		"appKey":    c.appKey,
		"appSecret": c.appSecret,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1.0/oauth2/accessToken", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("dingtalk token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req) //nolint:gosec // G704: URL is the DingTalk OpenAPI endpoint, operator-configured
	if err != nil {
		return "", fmt.Errorf("dingtalk token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("dingtalk token read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("dingtalk token: status %d: %s", resp.StatusCode, string(data))
	}
	var tr tokenResponse
	if err := json.Unmarshal(data, &tr); err != nil {
		return "", fmt.Errorf("dingtalk token parse: %w", err)
	}
	if strings.TrimSpace(tr.AccessToken) == "" {
		return "", errors.New("dingtalk token: empty in response")
	}
	c.token = tr.AccessToken
	c.tokenExp = time.Now().Add(tokenTTL)
	return c.token, nil
}

// doPost executes an authenticated POST to a DingTalk API path.
func (c *apiClient) doPost(ctx context.Context, path string, body any) ([]byte, error) {
	token, err := c.getToken(ctx)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-acs-dingtalk-access-token", token)

	resp, err := c.http.Do(req) //nolint:gosec // G704: URL is the DingTalk OpenAPI endpoint, operator-configured
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dingtalk api %s: status %d: %s", path, resp.StatusCode, string(data))
	}
	return data, nil
}

type sendUserMsgReq struct {
	RobotCode string   `json:"robotCode"`
	UserIds   []string `json:"userIds"`
	MsgKey    string   `json:"msgKey"`
	MsgParam  string   `json:"msgParam"`
}

// sendToUser sends a message to one or more DingTalk users via OpenAPI.
// userIds can contain at most 20 entries per request.
func (c *apiClient) sendToUser(ctx context.Context, robotCode string, userIds []string, msgKey, msgParam string) error {
	if len(userIds) == 0 {
		return errors.New("dingtalk: userIds is required")
	}
	_, err := c.doPost(ctx, "/v1.0/robot/oToMessages/batchSend", sendUserMsgReq{
		RobotCode: robotCode,
		UserIds:   userIds,
		MsgKey:    msgKey,
		MsgParam:  msgParam,
	})
	return err
}

type sendGroupMsgReq struct {
	RobotCode          string `json:"robotCode"`
	OpenConversationId string `json:"openConversationId"`
	MsgKey             string `json:"msgKey"`
	MsgParam           string `json:"msgParam"`
}

// sendToGroup sends a message to a DingTalk group via OpenAPI.
func (c *apiClient) sendToGroup(ctx context.Context, robotCode, openConversationId, msgKey, msgParam string) error {
	if strings.TrimSpace(openConversationId) == "" {
		return errors.New("dingtalk: openConversationId is required")
	}
	_, err := c.doPost(ctx, "/v1.0/robot/groupMessages/send", sendGroupMsgReq{
		RobotCode:          robotCode,
		OpenConversationId: openConversationId,
		MsgKey:             msgKey,
		MsgParam:           msgParam,
	})
	return err
}

// sendViaWebhook posts a reply through a session webhook URL.
// The webhook URL already contains auth; no access_token is required.
func (c *apiClient) sendViaWebhook(ctx context.Context, webhookURL string, body map[string]any) error {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("dingtalk webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req) //nolint:gosec // G704: webhook URL is received from DingTalk platform callback, not user-supplied
	if err != nil {
		return fmt.Errorf("dingtalk webhook: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("dingtalk webhook: status %d: %s", resp.StatusCode, string(data))
	}
	return nil
}

// getBotInfo queries basic info for the robot itself (used by DiscoverSelf).
type botInfoResponse struct {
	Result struct {
		Name      string `json:"name"`
		RobotCode string `json:"robotCode"`
	} `json:"result"`
	RequestID string `json:"requestId"`
}

type uploadMediaResponse struct {
	MediaID string `json:"media_id"`
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

// uploadMedia uploads a media file to DingTalk and returns the resulting mediaId.
// mediaType must be one of: "image", "voice", "video", "file".
// filename is used as the multipart filename; Content-Type is determined by the multipart writer.
func (c *apiClient) uploadMedia(ctx context.Context, mediaType, filename string, data io.Reader) (string, error) {
	token, err := c.getToken(ctx)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	// The "type" field tells DingTalk which media category this is.
	if err := mw.WriteField("type", mediaType); err != nil {
		return "", fmt.Errorf("dingtalk upload: write type field: %w", err)
	}

	// The file part must be named "media".
	part, err := mw.CreateFormFile("media", filename)
	if err != nil {
		return "", fmt.Errorf("dingtalk upload: create form file: %w", err)
	}
	if _, err := io.Copy(part, data); err != nil {
		return "", fmt.Errorf("dingtalk upload: copy media: %w", err)
	}
	if err := mw.Close(); err != nil {
		return "", fmt.Errorf("dingtalk upload: close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/media/upload?access_token="+token, &buf)
	if err != nil {
		return "", fmt.Errorf("dingtalk upload: build request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := c.http.Do(req) //nolint:gosec // URL is the DingTalk OpenAPI media endpoint
	if err != nil {
		return "", fmt.Errorf("dingtalk upload: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("dingtalk upload: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("dingtalk upload: status %d: %s", resp.StatusCode, string(raw))
	}
	var result uploadMediaResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("dingtalk upload: parse response: %w", err)
	}
	if result.ErrCode != 0 {
		return "", fmt.Errorf("dingtalk upload: errcode %d: %s", result.ErrCode, result.ErrMsg)
	}
	if strings.TrimSpace(result.MediaID) == "" {
		return "", fmt.Errorf("dingtalk upload: empty media_id in response: %s", string(raw))
	}
	return result.MediaID, nil
}

type downloadFileResponse struct {
	DownloadURL string `json:"downloadUrl"`
}

// downloadMessageFile resolves a downloadCode received in an inbound message to a
// temporary download URL, then streams the file content to the caller.
// The caller is responsible for closing the returned ReadCloser.
func (c *apiClient) downloadMessageFile(ctx context.Context, robotCode, downloadCode string) (io.ReadCloser, string, error) {
	data, err := c.doPost(ctx, "/v1.0/robot/messageFiles/download", map[string]string{
		"downloadCode": downloadCode,
		"robotCode":    robotCode,
	})
	if err != nil {
		return nil, "", fmt.Errorf("dingtalk download file: %w", err)
	}
	var result downloadFileResponse
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, "", fmt.Errorf("dingtalk download file: parse response: %w", err)
	}
	downloadURL := strings.TrimSpace(result.DownloadURL)
	if downloadURL == "" {
		return nil, "", fmt.Errorf("dingtalk download file: empty downloadUrl in response: %s", string(data))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("dingtalk download file: build request: %w", err)
	}
	resp, err := c.http.Do(req) //nolint:gosec // G107: URL is returned by DingTalk API, not user-supplied
	if err != nil {
		return nil, "", fmt.Errorf("dingtalk download file: fetch: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("dingtalk download file: status %d", resp.StatusCode)
	}
	mimeType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	return resp.Body, mimeType, nil
}

// getBotInfo retrieves the bot's own profile via the OpenAPI.
func (c *apiClient) getBotInfo(ctx context.Context, robotCode string) (botInfoResponse, error) {
	type req struct {
		RobotCode string `json:"robotCode"`
	}
	data, err := c.doPost(ctx, "/v1.0/robot/robotInfo", req{RobotCode: robotCode})
	if err != nil {
		return botInfoResponse{}, err
	}
	var info botInfoResponse
	if err := json.Unmarshal(data, &info); err != nil {
		return botInfoResponse{}, fmt.Errorf("dingtalk getBotInfo parse: %w", err)
	}
	return info, nil
}
