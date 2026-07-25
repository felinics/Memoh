// Derived from @tencent-weixin/openclaw-weixin (MIT License, Copyright (c) 2026 Tencent Inc.)
// See LICENSE in this directory for the full license text.

package weixin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	channelVersion         = "1.0.0"
	defaultLongPollTimeout = 35 * time.Second
	defaultAPITimeout      = 15 * time.Second
	defaultConfigTimeout   = 10 * time.Second

	sessionExpiredErrCode = -14
)

// Client talks to the Tencent iLink WeChat bot API.
type Client struct {
	httpClient *http.Client
	logger     *slog.Logger
}

// NewClient creates a WeChat API client.
func NewClient(log *slog.Logger) *Client {
	if log == nil {
		log = slog.Default()
	}
	return &Client{
		httpClient: &http.Client{Timeout: 0}, // per-request timeout via context
		logger:     log.With(slog.String("component", "weixin_client")),
	}
}

func buildBaseInfo() BaseInfo {
	return BaseInfo{ChannelVersion: channelVersion}
}

// randomWechatUIN generates the X-WECHAT-UIN header value: random uint32 -> decimal -> base64.
func randomWechatUIN() string {
	var buf [4]byte
	_, _ = rand.Read(buf[:])
	n := binary.BigEndian.Uint32(buf[:])
	return base64.StdEncoding.EncodeToString([]byte(strconv.FormatUint(uint64(n), 10)))
}

func (*Client) buildHeaders(token string, bodyLen int) http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("AuthorizationType", "ilink_bot_token")
	h.Set("Content-Length", strconv.Itoa(bodyLen))
	h.Set("X-WECHAT-UIN", randomWechatUIN())
	if strings.TrimSpace(token) != "" {
		h.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	}
	return h
}

func ensureTrailingSlash(u string) string {
	if strings.HasSuffix(u, "/") {
		return u
	}
	return u + "/"
}

func (c *Client) apiPost(ctx context.Context, baseURL, endpoint string, body []byte, token string, timeout time.Duration) ([]byte, error) {
	base := ensureTrailingSlash(baseURL)
	u, err := url.JoinPath(base, endpoint)
	if err != nil {
		return nil, fmt.Errorf("weixin api url: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("weixin api request: %w", err)
	}
	for k, vs := range c.buildHeaders(token, len(body)) {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}

	resp, err := c.httpClient.Do(req) //nolint:gosec // URL is constructed from trusted admin-configured baseURL
	if err != nil {
		return nil, fmt.Errorf("weixin api fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("weixin api read: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("weixin api %s %d: %s", endpoint, resp.StatusCode, string(raw))
	}
	return raw, nil
}

// GetUpdates performs a long-poll request to receive new messages.
func (c *Client) GetUpdates(ctx context.Context, cfg adapterConfig, getUpdatesBuf string) (*GetUpdatesResponse, error) {
	timeout := defaultLongPollTimeout
	if cfg.PollTimeoutSeconds > 0 {
		timeout = time.Duration(cfg.PollTimeoutSeconds) * time.Second
	}
	body, err := json.Marshal(GetUpdatesRequest{
		GetUpdatesBuf: getUpdatesBuf,
		BaseInfo:      buildBaseInfo(),
	})
	if err != nil {
		return nil, err
	}
	raw, err := c.apiPost(ctx, cfg.BaseURL, "ilink/bot/getupdates", body, cfg.Token, timeout+5*time.Second)
	if err != nil {
		if ctx.Err() != nil {
			return &GetUpdatesResponse{Ret: 0, Msgs: nil, GetUpdatesBuf: getUpdatesBuf}, nil
		}
		return nil, err
	}
	var resp GetUpdatesResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("weixin getupdates decode: %w", err)
	}
	return &resp, nil
}

// SendMessage sends a text or media message downstream.
func (c *Client) SendMessage(ctx context.Context, cfg adapterConfig, msg SendMessageRequest) error {
	msg.BaseInfo = buildBaseInfo()
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = c.apiPost(ctx, cfg.BaseURL, "ilink/bot/sendmessage", body, cfg.Token, defaultAPITimeout)
	return err
}

// GetConfig fetches bot config (typing_ticket etc.).
func (c *Client) GetConfig(ctx context.Context, cfg adapterConfig, userID, contextToken string) (*GetConfigResponse, error) {
	body, err := json.Marshal(GetConfigRequest{
		ILinkUserID:  userID,
		ContextToken: contextToken,
		BaseInfo:     buildBaseInfo(),
	})
	if err != nil {
		return nil, err
	}
	raw, err := c.apiPost(ctx, cfg.BaseURL, "ilink/bot/getconfig", body, cfg.Token, defaultConfigTimeout)
	if err != nil {
		return nil, err
	}
	var resp GetConfigResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("weixin getconfig decode: %w", err)
	}
	return &resp, nil
}

// SendTyping sends or cancels the typing indicator.
func (c *Client) SendTyping(ctx context.Context, cfg adapterConfig, userID, typingTicket string, status int) error {
	body, err := json.Marshal(SendTypingRequest{
		ILinkUserID:  userID,
		TypingTicket: typingTicket,
		Status:       status,
		BaseInfo:     buildBaseInfo(),
	})
	if err != nil {
		return err
	}
	_, err = c.apiPost(ctx, cfg.BaseURL, "ilink/bot/sendtyping", body, cfg.Token, defaultConfigTimeout)
	return err
}

// GetUploadURL requests a CDN pre-signed upload URL.
func (c *Client) GetUploadURL(ctx context.Context, cfg adapterConfig, req GetUploadURLRequest) (*GetUploadURLResponse, error) {
	req.BaseInfo = buildBaseInfo()
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	raw, err := c.apiPost(ctx, cfg.BaseURL, "ilink/bot/getuploadurl", body, cfg.Token, defaultAPITimeout)
	if err != nil {
		return nil, err
	}
	var resp GetUploadURLResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("weixin getuploadurl decode: %w", err)
	}
	return &resp, nil
}

// FetchQRCode requests a new QR code for login.
func (c *Client) FetchQRCode(ctx context.Context, apiBaseURL string) (*QRCodeResponse, error) {
	base := ensureTrailingSlash(apiBaseURL)
	u := base + "ilink/bot/get_bot_qrcode?bot_type=" + url.QueryEscape(defaultBotType)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req) //nolint:gosec // URL from admin-configured baseURL
	if err != nil {
		return nil, fmt.Errorf("weixin qrcode fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("weixin qrcode %d: %s", resp.StatusCode, string(raw))
	}
	var qr QRCodeResponse
	if err := json.Unmarshal(raw, &qr); err != nil {
		return nil, fmt.Errorf("weixin qrcode decode: %w", err)
	}
	return &qr, nil
}

// PollQRStatus long-polls the QR code login status.
func (c *Client) PollQRStatus(ctx context.Context, apiBaseURL, qrcode string) (*QRStatusResponse, error) {
	base := ensureTrailingSlash(apiBaseURL)
	u := base + "ilink/bot/get_qrcode_status?qrcode=" + url.QueryEscape(qrcode)

	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("iLink-App-ClientVersion", "1")
	resp, err := c.httpClient.Do(req) //nolint:gosec // URL from admin-configured baseURL
	if err != nil {
		if ctx.Err() != nil {
			return &QRStatusResponse{Status: "wait"}, nil
		}
		return nil, fmt.Errorf("weixin qrstatus fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("weixin qrstatus %d: %s", resp.StatusCode, string(raw))
	}
	var status QRStatusResponse
	if err := json.Unmarshal(raw, &status); err != nil {
		return nil, fmt.Errorf("weixin qrstatus decode: %w", err)
	}
	if status.Status == "scaned" {
		status.Status = "scanned"
	}
	return &status, nil
}
