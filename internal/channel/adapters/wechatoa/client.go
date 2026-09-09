package wechatoa

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

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/common"
	"github.com/felinics/memoh/internal/redact"
)

type apiResponse struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

type mediaUploadResponse struct {
	MediaID string `json:"media_id"`
	Type    string `json:"type"`
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

type apiClient struct {
	appID     string
	appSecret string
	http      *http.Client

	mu         sync.Mutex
	tokenCache string
	expiresAt  time.Time
}

func (a *WeChatOAAdapter) clientForConfig(raw map[string]any) (*apiClient, error) {
	cfg, err := parseConfig(raw)
	if err != nil {
		return nil, err
	}
	redact.SetSecrets("wechatoa:"+cfg.AppID, cfg.AppSecret, cfg.Token, cfg.EncodingAESKey)
	key := strings.Join([]string{
		strings.TrimSpace(cfg.AppID),
		strings.TrimSpace(cfg.AppSecret),
		strings.TrimSpace(cfg.Token),
		strings.TrimSpace(cfg.EncodingAESKey),
		cfg.HTTPProxy.CacheKey(),
	}, "\x00")
	a.mu.RLock()
	client := a.clients[key]
	a.mu.RUnlock()
	if client != nil {
		return client, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if existing := a.clients[key]; existing != nil {
		return existing, nil
	}
	httpClient, err := common.NewHTTPClient(25*time.Second, cfg.HTTPProxy)
	if err != nil {
		return nil, err
	}
	client = &apiClient{
		appID:     cfg.AppID,
		appSecret: cfg.AppSecret,
		http:      httpClient,
	}
	a.clients[key] = client
	return client, nil
}

func (c *apiClient) getAccessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tokenCache != "" && time.Now().Before(c.expiresAt.Add(-5*time.Minute)) {
		return c.tokenCache, nil
	}
	body, _ := json.Marshal(map[string]any{
		"grant_type": "client_credential",
		"appid":      c.appID,
		"secret":     c.appSecret,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.weixin.qq.com/cgi-bin/stable_token", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req) //nolint:gosec // WeChat token endpoint is fixed by the adapter.
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	accessToken := strings.TrimSpace(fmt.Sprint(out["access_token"]))
	errMsg := strings.TrimSpace(fmt.Sprint(out["errmsg"]))
	errCode, _ := out["errcode"].(float64)
	expiresIn, _ := out["expires_in"].(float64)
	if accessToken == "" {
		return "", fmt.Errorf("wechatoa get token failed: %s (code: %d)", errMsg, int(errCode))
	}
	if expiresIn <= 0 {
		expiresIn = 7200
	}
	c.tokenCache = accessToken
	c.expiresAt = time.Now().Add(time.Duration(int64(expiresIn)) * time.Second)
	return c.tokenCache, nil
}

func (c *apiClient) sendPreparedMessage(ctx context.Context, openID string, msg channel.PreparedMessage) error {
	payload, err := c.buildSendPayload(ctx, msg)
	if err != nil {
		return err
	}
	payload["touser"] = openID
	token, err := c.getAccessToken(ctx)
	if err != nil {
		return err
	}
	buf, _ := json.Marshal(payload)
	url := "https://api.weixin.qq.com/cgi-bin/message/custom/send?access_token=" + token
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req) //nolint:gosec // WeChat message endpoint is fixed by the adapter.
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	var out apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	if out.ErrCode != 0 {
		return fmt.Errorf("wechatoa send failed: %s (code: %d)", strings.TrimSpace(out.ErrMsg), out.ErrCode)
	}
	return nil
}

func (c *apiClient) buildSendPayload(ctx context.Context, msg channel.PreparedMessage) (map[string]any, error) {
	text := strings.TrimSpace(msg.Message.PlainText())
	if len(msg.Attachments) > 0 || len(msg.Message.Attachments) > 0 {
		logical := channel.Attachment{}
		if len(msg.Message.Attachments) > 0 {
			logical = msg.Message.Attachments[0]
		}
		var prepared *channel.PreparedAttachment
		if len(msg.Attachments) > 0 {
			prepared = &msg.Attachments[0]
		}
		switch logical.Type {
		case channel.AttachmentImage:
			mediaID, err := c.ensureUploadMedia(ctx, "image", logical, prepared)
			if err != nil {
				return nil, err
			}
			return map[string]any{"msgtype": "image", "image": map[string]string{"media_id": mediaID}}, nil
		case channel.AttachmentVoice, channel.AttachmentAudio:
			mediaID, err := c.ensureUploadMedia(ctx, "voice", logical, prepared)
			if err != nil {
				return nil, err
			}
			return map[string]any{"msgtype": "voice", "voice": map[string]string{"media_id": mediaID}}, nil
		case channel.AttachmentVideo:
			mediaID, err := c.ensureUploadMedia(ctx, "video", logical, prepared)
			if err != nil {
				return nil, err
			}
			thumbID := strings.TrimSpace(readAttachmentMeta(logical.Metadata, "thumb_media_id"))
			if thumbID == "" {
				return nil, errors.New("wechatoa video requires thumb_media_id in attachment metadata")
			}
			return map[string]any{
				"msgtype": "video",
				"video": map[string]string{
					"media_id":       mediaID,
					"thumb_media_id": thumbID,
					"title":          strings.TrimSpace(logical.Name),
					"description":    strings.TrimSpace(logical.Caption),
				},
			}, nil
		default:
			return nil, fmt.Errorf("wechatoa does not support attachment type: %s", logical.Type)
		}
	}
	if text == "" {
		return nil, errors.New("message is required")
	}
	return map[string]any{
		"msgtype": "text",
		"text": map[string]string{
			"content": text,
		},
	}, nil
}

func readAttachmentMeta(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	raw, ok := metadata[key]
	if !ok {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(raw))
}

func (c *apiClient) ensureUploadMedia(ctx context.Context, typ string, logical channel.Attachment, prepared *channel.PreparedAttachment) (string, error) {
	if mediaID := strings.TrimSpace(logical.PlatformKey); mediaID != "" {
		return mediaID, nil
	}
	if prepared == nil {
		return "", errors.New("wechatoa attachment requires prepared upload payload")
	}
	if prepared.Kind == channel.PreparedAttachmentNativeRef && strings.TrimSpace(prepared.NativeRef) != "" {
		return strings.TrimSpace(prepared.NativeRef), nil
	}
	if prepared.Kind != channel.PreparedAttachmentUpload || prepared.Open == nil {
		return "", errors.New("wechatoa attachment requires upload payload")
	}
	reader, err := prepared.Open(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = reader.Close() }()
	filename := strings.TrimSpace(prepared.Name)
	if filename == "" {
		filename = strings.TrimSpace(logical.Name)
	}
	if filename == "" {
		filename = "media.bin"
	}
	return c.uploadMedia(ctx, typ, filename, reader)
}

func (c *apiClient) uploadMedia(ctx context.Context, typ string, filename string, content io.Reader) (string, error) {
	token, err := c.getAccessToken(ctx)
	if err != nil {
		return "", err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("media", filename)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, content); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	url := "https://api.weixin.qq.com/cgi-bin/media/upload?access_token=" + token + "&type=" + typ
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body.Bytes()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := c.http.Do(req) //nolint:gosec // WeChat media upload endpoint is fixed by the adapter.
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var out mediaUploadResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.MediaID) == "" {
		return "", fmt.Errorf("wechatoa upload media failed: %s (code: %d)", strings.TrimSpace(out.ErrMsg), out.ErrCode)
	}
	return strings.TrimSpace(out.MediaID), nil
}

func (c *apiClient) downloadMedia(ctx context.Context, mediaID string) (channel.AttachmentPayload, error) {
	token, err := c.getAccessToken(ctx)
	if err != nil {
		return channel.AttachmentPayload{}, err
	}
	url := "https://api.weixin.qq.com/cgi-bin/media/get?access_token=" + token + "&media_id=" + mediaID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return channel.AttachmentPayload{}, err
	}
	resp, err := c.http.Do(req) //nolint:gosec // WeChat media download endpoint is fixed by the adapter.
	if err != nil {
		return channel.AttachmentPayload{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		return channel.AttachmentPayload{}, fmt.Errorf("wechatoa download media failed: status %d", resp.StatusCode)
	}
	name := filenameFromHeaders(resp.Header.Get("Content-Disposition"))
	mime := strings.TrimSpace(resp.Header.Get("Content-Type"))
	return channel.AttachmentPayload{
		Reader: resp.Body,
		Name:   name,
		Mime:   mime,
	}, nil
}

func filenameFromHeaders(contentDisposition string) string {
	cd := strings.TrimSpace(contentDisposition)
	if cd == "" {
		return ""
	}
	parts := strings.Split(cd, "filename=")
	if len(parts) < 2 {
		return ""
	}
	return strings.Trim(parts[len(parts)-1], "\" ")
}
