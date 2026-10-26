package feishu

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// App registration creates a Feishu or Lark bot app by QR scan. It is the
// OAuth 2.0 device authorization grant (RFC 8628) behind Feishu's "one-click
// app creation": begin returns a device code and a verification URL that the
// user opens or scans in Feishu, and polling the device code returns the new
// app's App ID and App Secret once the user confirms.
//
// The protocol is only published through Feishu's SDKs. This client follows
// github.com/larksuite/oapi-sdk-go/v3 scene/registration (v3.12.1) request for
// request, with two deliberate differences: begin and poll are separate calls
// so a browser can drive the flow, and the lifetime is read from expires_in
// (what the endpoint actually returns) as well as the SDK's expire_in.

const (
	feishuAccountsBaseURL = "https://accounts.feishu.cn"
	larkAccountsBaseURL   = "https://accounts.larksuite.com"
	registrationPath      = "/oauth/v1/app/registration"

	registrationArchetype  = "PersonalAgent"
	registrationAuthMethod = "client_secret"
	registrationUserInfo   = "open_id"
	registrationSource     = "memoh"

	registrationRequestTimeout  = 10 * time.Second
	registrationDefaultInterval = 5 * time.Second
	registrationSlowDownStep    = 5 * time.Second
	// The official documentation promises a 10 minute link even when the
	// endpoint reports a longer device code lifetime, so the shorter wins.
	registrationMaxLifetime = 10 * time.Minute
	registrationMaxBodySize = 64 << 10
)

// registrationScopes are the tenant scopes the adapter calls. The platform
// template already grants the messaging basics; addons are additive, so
// repeating them is harmless and the list documents what Memoh relies on.
var registrationScopes = []string{
	"im:message",
	"im:message:send_as_bot",
	"im:message.p2p_msg:readonly",
	"im:message.group_at_msg:readonly",
	"im:resource",
	"im:chat:readonly",
	"contact:user.base:readonly",
}

var registrationEvents = []string{
	"im.message.receive_v1",
}

// ErrRegistrationFailed reports that the Feishu accounts endpoint refused or
// failed a registration request in a way polling again cannot fix.
var ErrRegistrationFailed = errors.New("feishu app registration failed")

// RegistrationPollStatus is the state of a registration after one poll.
type RegistrationPollStatus string

const (
	RegistrationPending RegistrationPollStatus = "pending"
	RegistrationSuccess RegistrationPollStatus = "success"
	RegistrationDenied  RegistrationPollStatus = "denied"
	RegistrationExpired RegistrationPollStatus = "expired"
)

// RegistrationStart is the result of beginning a registration.
type RegistrationStart struct {
	DeviceCode string
	// VerificationURL is what the user opens or scans in Feishu.
	VerificationURL string
	Interval        time.Duration
	ExpiresIn       time.Duration
}

// RegistrationPollResult is the result of one poll. Region is the region to
// poll next and to store with the credentials: a Lark tenant moves the flow
// from the Feishu accounts host to the Lark one.
type RegistrationPollResult struct {
	Status    RegistrationPollStatus
	Region    string
	AppID     string
	AppSecret string
	OwnerID   string
	// SlowDown asks the caller to widen its polling interval.
	SlowDown bool
}

// RegistrationAppPreset pre-fills the app creation page. The user can still
// edit both values before confirming.
type RegistrationAppPreset struct {
	Name        string
	Description string
}

// RegistrationClient talks to the Feishu and Lark accounts endpoints.
type RegistrationClient struct {
	httpClient *http.Client
	baseURLs   map[string]string
}

// NewRegistrationClient creates a client for the public accounts endpoints.
func NewRegistrationClient() *RegistrationClient {
	return newRegistrationClient(&http.Client{Timeout: registrationRequestTimeout}, map[string]string{
		regionFeishu: feishuAccountsBaseURL,
		regionLark:   larkAccountsBaseURL,
	})
}

func newRegistrationClient(httpClient *http.Client, baseURLs map[string]string) *RegistrationClient {
	return &RegistrationClient{httpClient: httpClient, baseURLs: baseURLs}
}

type registrationBeginResponse struct {
	DeviceCode              string `json:"device_code"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	Interval                int    `json:"interval"`
	ExpiresIn               int    `json:"expires_in"`
	ExpireIn                int    `json:"expire_in"`
	Error                   string `json:"error"`
	ErrorDescription        string `json:"error_description"`
}

type registrationPollResponse struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"` //nolint:gosec // G117: decodes the secret Feishu returns once; it is stored as a channel credential, never logged or echoed.
	UserInfo     *struct {
		OpenID      string `json:"open_id"`
		TenantBrand string `json:"tenant_brand"`
	} `json:"user_info"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// Begin starts a registration on the Feishu accounts host. A Lark user is
// detected while polling, so every registration begins on Feishu.
func (c *RegistrationClient) Begin(ctx context.Context, preset RegistrationAppPreset) (RegistrationStart, error) {
	var resp registrationBeginResponse
	if err := c.post(ctx, regionFeishu, url.Values{
		"action":            {"begin"},
		"archetype":         {registrationArchetype},
		"auth_method":       {registrationAuthMethod},
		"request_user_info": {registrationUserInfo},
	}, &resp); err != nil {
		return RegistrationStart{}, err
	}
	if resp.Error != "" {
		return RegistrationStart{}, fmt.Errorf("%w: begin: %s", ErrRegistrationFailed, resp.Error)
	}
	if strings.TrimSpace(resp.DeviceCode) == "" || strings.TrimSpace(resp.VerificationURIComplete) == "" {
		return RegistrationStart{}, fmt.Errorf("%w: begin: incomplete response", ErrRegistrationFailed)
	}
	verificationURL, err := buildRegistrationURL(resp.VerificationURIComplete, preset)
	if err != nil {
		return RegistrationStart{}, err
	}
	return RegistrationStart{
		DeviceCode:      resp.DeviceCode,
		VerificationURL: verificationURL,
		Interval:        registrationInterval(resp.Interval),
		ExpiresIn:       registrationLifetime(resp.ExpiresIn, resp.ExpireIn),
	}, nil
}

// Poll checks a registration once. A Lark tenant is re-polled on the Lark
// host within the same call, and the returned Region says where to continue.
func (c *RegistrationClient) Poll(ctx context.Context, region, deviceCode string) (RegistrationPollResult, error) {
	region, err := normalizeRegion(region)
	if err != nil {
		return RegistrationPollResult{}, err
	}
	resp, err := c.poll(ctx, region, deviceCode)
	if err != nil {
		return RegistrationPollResult{}, err
	}
	if region == regionFeishu && resp.UserInfo != nil && strings.EqualFold(resp.UserInfo.TenantBrand, regionLark) {
		region = regionLark
		if resp, err = c.poll(ctx, region, deviceCode); err != nil {
			return RegistrationPollResult{}, err
		}
	}
	return interpretPoll(region, resp)
}

func (c *RegistrationClient) poll(ctx context.Context, region, deviceCode string) (registrationPollResponse, error) {
	var resp registrationPollResponse
	err := c.post(ctx, region, url.Values{
		"action":      {"poll"},
		"device_code": {deviceCode},
	}, &resp)
	return resp, err
}

func interpretPoll(region string, resp registrationPollResponse) (RegistrationPollResult, error) {
	result := RegistrationPollResult{Region: region}
	if resp.ClientID != "" && resp.ClientSecret != "" {
		result.Status = RegistrationSuccess
		result.AppID = resp.ClientID
		result.AppSecret = resp.ClientSecret
		if resp.UserInfo != nil {
			result.OwnerID = resp.UserInfo.OpenID
		}
		return result, nil
	}
	switch resp.Error {
	case "", "authorization_pending":
		result.Status = RegistrationPending
	case "slow_down":
		result.Status = RegistrationPending
		result.SlowDown = true
	case "access_denied":
		result.Status = RegistrationDenied
	case "expired_token":
		result.Status = RegistrationExpired
	default:
		return RegistrationPollResult{}, fmt.Errorf("%w: poll: %s", ErrRegistrationFailed, resp.Error)
	}
	return result, nil
}

// post sends one form request. The endpoint answers pending and error states
// with a 4xx status and a JSON body, so the body is decoded whatever the
// status; only a body that is not JSON is a transport failure.
func (c *RegistrationClient) post(ctx context.Context, region string, form url.Values, out any) error {
	baseURL, ok := c.baseURLs[region]
	if !ok {
		return fmt.Errorf("feishu app registration: unknown region %q", region)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+registrationPath, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient.Do(req) //nolint:gosec // G704: the host comes from the fixed Feishu/Lark accounts table, never from input.
	if err != nil {
		return fmt.Errorf("feishu app registration: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, registrationMaxBodySize))
	if err != nil {
		return fmt.Errorf("feishu app registration: read response: %w", err)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("feishu app registration: decode response (status %d): %w", resp.StatusCode, err)
	}
	return nil
}

func buildRegistrationURL(raw string, preset RegistrationAppPreset) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%w: invalid verification url: %w", ErrRegistrationFailed, err)
	}
	if parsed.Scheme != "https" || parsed.Host == "" {
		return "", fmt.Errorf("%w: unexpected verification url", ErrRegistrationFailed)
	}
	addons, err := encodeRegistrationAddons()
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("from", "sdk")
	query.Set("tp", "sdk")
	query.Set("source", registrationSource)
	query.Set("addons", addons)
	if name := strings.TrimSpace(preset.Name); name != "" {
		query.Set("name", name)
	}
	if desc := strings.TrimSpace(preset.Description); desc != "" {
		query.Set("desc", desc)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// encodeRegistrationAddons encodes the scope and event addons the way the
// SDK does: JSON, gzip, then unpadded URL-safe base64.
func encodeRegistrationAddons() (string, error) {
	payload := map[string]any{
		"scopes": map[string][]string{"tenant": registrationScopes},
		"events": map[string]any{"items": map[string][]string{"tenant": registrationEvents}},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("feishu app registration: encode addons: %w", err)
	}
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write(raw); err != nil {
		return "", fmt.Errorf("feishu app registration: compress addons: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("feishu app registration: compress addons: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf.Bytes()), nil
}

func registrationInterval(seconds int) time.Duration {
	if seconds <= 0 {
		return registrationDefaultInterval
	}
	return time.Duration(seconds) * time.Second
}

func registrationLifetime(expiresIn, expireIn int) time.Duration {
	seconds := expiresIn
	if seconds <= 0 {
		seconds = expireIn
	}
	if seconds <= 0 {
		return registrationMaxLifetime
	}
	return min(time.Duration(seconds)*time.Second, registrationMaxLifetime)
}
