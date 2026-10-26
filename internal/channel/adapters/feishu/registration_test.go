package feishu

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
	"time"
)

type registrationEndpoint struct {
	t        *testing.T
	requests []url.Values
	respond  func(form url.Values) (int, string)
}

func (e *registrationEndpoint) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != registrationPath {
			e.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
			e.t.Errorf("content type = %q", got)
		}
		if err := r.ParseForm(); err != nil {
			e.t.Errorf("parse form: %v", err)
		}
		e.requests = append(e.requests, r.PostForm)
		status, body := e.respond(r.PostForm)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body) //nolint:gosec // G705: canned JSON from the test table, served to the test client.
	})
}

func newTestRegistrationClient(t *testing.T, feishuEndpoint, larkEndpoint *registrationEndpoint) *RegistrationClient {
	t.Helper()
	feishuServer := httptest.NewServer(feishuEndpoint.handler())
	t.Cleanup(feishuServer.Close)
	baseURLs := map[string]string{regionFeishu: feishuServer.URL}
	if larkEndpoint != nil {
		larkServer := httptest.NewServer(larkEndpoint.handler())
		t.Cleanup(larkServer.Close)
		baseURLs[regionLark] = larkServer.URL
	}
	return newRegistrationClient(&http.Client{Timeout: 5 * time.Second}, baseURLs)
}

func TestRegistrationBeginBuildsVerificationURL(t *testing.T) {
	endpoint := &registrationEndpoint{t: t, respond: func(url.Values) (int, string) {
		return http.StatusOK, `{"device_code":"dc-1","user_code":"ABCD-EFGH","verification_uri":"https://open.feishu.cn/page/launcher","verification_uri_complete":"https://open.feishu.cn/page/launcher?user_code=ABCD-EFGH","expires_in":3600,"interval":5}`
	}}
	client := newTestRegistrationClient(t, endpoint, nil)

	start, err := client.Begin(context.Background(), RegistrationAppPreset{Name: "Helper Bot", Description: " "})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	form := endpoint.requests[0]
	for key, want := range map[string]string{"action": "begin", "archetype": "PersonalAgent", "auth_method": "client_secret", "request_user_info": "open_id"} {
		if got := form.Get(key); got != want {
			t.Fatalf("form %s = %q, want %q", key, got, want)
		}
	}
	if start.DeviceCode != "dc-1" {
		t.Fatalf("device code = %q", start.DeviceCode)
	}
	if start.Interval != 5*time.Second {
		t.Fatalf("interval = %s", start.Interval)
	}
	if start.ExpiresIn != registrationMaxLifetime {
		t.Fatalf("expires in = %s, want the documented 10 minute cap", start.ExpiresIn)
	}

	parsed, err := url.Parse(start.VerificationURL)
	if err != nil {
		t.Fatalf("parse verification url: %v", err)
	}
	query := parsed.Query()
	for key, want := range map[string]string{"user_code": "ABCD-EFGH", "from": "sdk", "tp": "sdk", "source": "memoh", "name": "Helper Bot"} {
		if got := query.Get(key); got != want {
			t.Fatalf("query %s = %q, want %q", key, got, want)
		}
	}
	if query.Has("desc") {
		t.Fatal("blank description must not be sent")
	}
	addons := decodeAddons(t, query.Get("addons"))
	if !slices.Contains(addons.Scopes.Tenant, "contact:user.base:readonly") {
		t.Fatalf("addons scopes = %v", addons.Scopes.Tenant)
	}
	if !slices.Equal(addons.Events.Items.Tenant, []string{"im.message.receive_v1"}) {
		t.Fatalf("addons events = %v", addons.Events.Items.Tenant)
	}
}

type decodedAddons struct {
	Scopes struct {
		Tenant []string `json:"tenant"`
	} `json:"scopes"`
	Events struct {
		Items struct {
			Tenant []string `json:"tenant"`
		} `json:"items"`
	} `json:"events"`
}

func decodeAddons(t *testing.T, encoded string) decodedAddons {
	t.Helper()
	compressed, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("addons base64: %v", err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("addons gzip: %v", err)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("addons read: %v", err)
	}
	var addons decodedAddons
	if err := json.Unmarshal(raw, &addons); err != nil {
		t.Fatalf("addons json: %v", err)
	}
	return addons
}

func TestRegistrationLifetime(t *testing.T) {
	cases := []struct {
		name                string
		expiresIn, expireIn int
		want                time.Duration
	}{
		{name: "expires_in", expiresIn: 300, want: 5 * time.Minute},
		{name: "sdk field", expireIn: 120, want: 2 * time.Minute},
		{name: "capped", expiresIn: 3600, want: registrationMaxLifetime},
		{name: "missing", want: registrationMaxLifetime},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := registrationLifetime(tc.expiresIn, tc.expireIn); got != tc.want {
				t.Fatalf("lifetime = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestRegistrationBeginRejectsIncompleteResponse(t *testing.T) {
	endpoint := &registrationEndpoint{t: t, respond: func(url.Values) (int, string) {
		return http.StatusOK, `{"device_code":"dc-1"}`
	}}
	_, err := newTestRegistrationClient(t, endpoint, nil).Begin(context.Background(), RegistrationAppPreset{})
	if !errors.Is(err, ErrRegistrationFailed) {
		t.Fatalf("err = %v, want ErrRegistrationFailed", err)
	}
}

func TestRegistrationPollStatuses(t *testing.T) {
	cases := []struct {
		name         string
		status       int
		body         string
		want         RegistrationPollStatus
		wantSlowDown bool
	}{
		{name: "pending", status: http.StatusBadRequest, body: `{"error":"authorization_pending"}`, want: RegistrationPending},
		{name: "slow down", status: http.StatusBadRequest, body: `{"error":"slow_down"}`, want: RegistrationPending, wantSlowDown: true},
		{name: "denied", status: http.StatusBadRequest, body: `{"error":"access_denied"}`, want: RegistrationDenied},
		{name: "expired", status: http.StatusBadRequest, body: `{"error":"expired_token"}`, want: RegistrationExpired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			endpoint := &registrationEndpoint{t: t, respond: func(url.Values) (int, string) { return tc.status, tc.body }}
			result, err := newTestRegistrationClient(t, endpoint, nil).Poll(context.Background(), regionFeishu, "dc-1")
			if err != nil {
				t.Fatalf("poll: %v", err)
			}
			if result.Status != tc.want || result.SlowDown != tc.wantSlowDown {
				t.Fatalf("result = %+v", result)
			}
			if got := endpoint.requests[0].Get("device_code"); got != "dc-1" {
				t.Fatalf("device_code = %q", got)
			}
		})
	}
}

func TestRegistrationPollSuccess(t *testing.T) {
	endpoint := &registrationEndpoint{t: t, respond: func(url.Values) (int, string) {
		return http.StatusOK, `{"client_id":"cli_1","client_secret":"secret-1","user_info":{"open_id":"ou_owner","tenant_brand":"feishu"}}`
	}}
	result, err := newTestRegistrationClient(t, endpoint, nil).Poll(context.Background(), regionFeishu, "dc-1")
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if result.Status != RegistrationSuccess || result.AppID != "cli_1" || result.AppSecret != "secret-1" || result.OwnerID != "ou_owner" || result.Region != regionFeishu {
		t.Fatalf("result = %+v", result)
	}
}

func TestRegistrationPollSwitchesToLark(t *testing.T) {
	feishuEndpoint := &registrationEndpoint{t: t, respond: func(url.Values) (int, string) {
		return http.StatusBadRequest, `{"error":"authorization_pending","user_info":{"tenant_brand":"lark"}}`
	}}
	larkEndpoint := &registrationEndpoint{t: t, respond: func(url.Values) (int, string) {
		return http.StatusOK, `{"client_id":"cli_lark","client_secret":"secret-lark","user_info":{"tenant_brand":"lark"}}`
	}}
	client := newTestRegistrationClient(t, feishuEndpoint, larkEndpoint)

	result, err := client.Poll(context.Background(), regionFeishu, "dc-1")
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if result.Status != RegistrationSuccess || result.Region != regionLark || result.AppID != "cli_lark" {
		t.Fatalf("result = %+v", result)
	}
	if len(larkEndpoint.requests) != 1 {
		t.Fatalf("lark polls = %d, want 1", len(larkEndpoint.requests))
	}
}

func TestRegistrationPollUnknownErrorIsTerminal(t *testing.T) {
	endpoint := &registrationEndpoint{t: t, respond: func(url.Values) (int, string) {
		return http.StatusBadRequest, `{"error":"invalid_grant","error_description":"bad device code"}`
	}}
	_, err := newTestRegistrationClient(t, endpoint, nil).Poll(context.Background(), regionFeishu, "dc-1")
	if !errors.Is(err, ErrRegistrationFailed) {
		t.Fatalf("err = %v, want ErrRegistrationFailed", err)
	}
}

func TestRegistrationPollNonJSONIsTransient(t *testing.T) {
	endpoint := &registrationEndpoint{t: t, respond: func(url.Values) (int, string) {
		return http.StatusBadGateway, `<html>bad gateway</html>`
	}}
	_, err := newTestRegistrationClient(t, endpoint, nil).Poll(context.Background(), regionFeishu, "dc-1")
	if err == nil || errors.Is(err, ErrRegistrationFailed) {
		t.Fatalf("err = %v, want a transient error", err)
	}
}
