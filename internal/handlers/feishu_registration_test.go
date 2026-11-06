package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/adapters/feishu"
	"github.com/felinics/memoh/internal/server"
)

const (
	feishuRegBotID  = "11111111-1111-1111-1111-111111111111"
	feishuRegUserID = "22222222-2222-2222-2222-222222222222"
)

type fakeFeishuSessions struct {
	starts     []string
	startErr   error
	pollResult feishu.RegistrationPollResult
	pollErr    error
}

func (f *fakeFeishuSessions) Start(_ context.Context, botID, ownerID string, preset feishu.RegistrationAppPreset) (feishu.RegistrationSession, error) {
	f.starts = append(f.starts, botID+"/"+ownerID+"/"+preset.Name)
	if f.startErr != nil {
		return feishu.RegistrationSession{}, f.startErr
	}
	return feishu.RegistrationSession{
		ID:              "session-1",
		VerificationURL: "https://open.feishu.cn/page/launcher?user_code=ABCD",
		ExpiresAt:       time.Now().Add(10 * time.Minute),
		Interval:        5 * time.Second,
	}, nil
}

func (f *fakeFeishuSessions) Poll(context.Context, string, string, string) (feishu.RegistrationPollResult, error) {
	return f.pollResult, f.pollErr
}

type fakeFeishuConfigs struct{ configured bool }

func (f *fakeFeishuConfigs) ResolveEffectiveConfig(context.Context, string, channel.ChannelType) (channel.ChannelConfig, error) {
	if f.configured {
		return channel.ChannelConfig{ID: "cfg-1"}, nil
	}
	return channel.ChannelConfig{}, channel.ErrChannelConfigNotFound
}

type fakeFeishuRuntime struct {
	channel.Runtime
	upserts    []channel.UpsertConfigRequest
	enabledErr error
}

func (f *fakeFeishuRuntime) UpsertBotChannelConfig(_ context.Context, _ string, _ channel.ChannelType, req channel.UpsertConfigRequest) (channel.ChannelConfig, error) {
	f.upserts = append(f.upserts, req)
	if req.Disabled != nil && !*req.Disabled && f.enabledErr != nil {
		return channel.ChannelConfig{}, f.enabledErr
	}
	return channel.ChannelConfig{ID: "cfg-1"}, nil
}

type feishuRegistrationFixture struct {
	handler  *FeishuRegistrationHandler
	sessions *fakeFeishuSessions
	configs  *fakeFeishuConfigs
	runtime  *fakeFeishuRuntime
	denied   bool
}

func newFeishuRegistrationFixture() *feishuRegistrationFixture {
	f := &feishuRegistrationFixture{
		sessions: &fakeFeishuSessions{},
		configs:  &fakeFeishuConfigs{},
		runtime:  &fakeFeishuRuntime{},
	}
	f.handler = &FeishuRegistrationHandler{
		sessions:       f.sessions,
		configs:        f.configs,
		channelRuntime: f.runtime,
		authorize: func(_ context.Context, channelIdentityID, botID string) (bots.Bot, error) {
			if f.denied || channelIdentityID != feishuRegUserID {
				return bots.Bot{}, echo.NewHTTPError(http.StatusForbidden, "bot access denied")
			}
			return bots.Bot{ID: botID, DisplayName: "Helper Bot"}, nil
		},
		activation: func(context.Context, map[string]any) (feishu.BotActivation, error) {
			return feishu.BotActivation{Known: true, Active: true}, nil
		},
		logger: slog.New(slog.DiscardHandler),
	}
	return f
}

func (f *feishuRegistrationFixture) do(t *testing.T, action, body string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	e.HTTPErrorHandler = server.NewHTTPErrorHandler(slog.New(slog.DiscardHandler))
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("user", &jwt.Token{Valid: true, Claims: jwt.MapClaims{"sub": feishuRegUserID, "user_id": feishuRegUserID}})
			return next(c)
		}
	})
	f.handler.Register(e)
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/bots/%s/channel/feishu/registration/%s", feishuRegBotID, action), strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func decodeFeishuRegJSON(t *testing.T, rec *httptest.ResponseRecorder, out any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
}

func problemCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var problem struct {
		Code string `json:"code"`
	}
	decodeFeishuRegJSON(t, rec, &problem)
	return problem.Code
}

func TestFeishuRegistrationStart(t *testing.T) {
	f := newFeishuRegistrationFixture()
	rec := f.do(t, "start", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resp FeishuRegistrationStartResponse
	decodeFeishuRegJSON(t, rec, &resp)
	if resp.SessionID != "session-1" || resp.IntervalSeconds != 5 || resp.ExpiresInSeconds < 590 || resp.ExpiresInSeconds > 600 || !strings.HasPrefix(resp.VerificationURL, "https://open.feishu.cn/") {
		t.Fatalf("response = %+v", resp)
	}
	if want := feishuRegBotID + "/" + feishuRegUserID + "/Helper Bot"; len(f.sessions.starts) != 1 || f.sessions.starts[0] != want {
		t.Fatalf("starts = %v, want %q", f.sessions.starts, want)
	}
}

func TestFeishuRegistrationStartRefusals(t *testing.T) {
	cases := []struct {
		name       string
		setup      func(*feishuRegistrationFixture)
		wantStatus int
		wantCode   apperror.Code
	}{
		{name: "no manage access", setup: func(f *feishuRegistrationFixture) { f.denied = true }, wantStatus: http.StatusForbidden},
		{name: "already configured", setup: func(f *feishuRegistrationFixture) { f.configs.configured = true }, wantStatus: http.StatusConflict, wantCode: apperror.CodeChannelAlreadyConfigured},
		{name: "rate limited", setup: func(f *feishuRegistrationFixture) { f.sessions.startErr = feishu.ErrRegistrationRateLimited }, wantStatus: http.StatusTooManyRequests, wantCode: apperror.CodeChannelRegistrationRateLimited},
		{name: "platform unavailable", setup: func(f *feishuRegistrationFixture) {
			f.sessions.startErr = errors.New("feishu app registration: dial tcp: connection refused")
		}, wantStatus: http.StatusBadGateway, wantCode: apperror.CodeChannelRegistrationUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFeishuRegistrationFixture()
			tc.setup(f)
			rec := f.do(t, "start", `{}`)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
			}
			if tc.wantCode != "" && problemCode(t, rec) != string(tc.wantCode) {
				t.Fatalf("code = %s", rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "connection refused") {
				t.Fatalf("private cause leaked: %s", rec.Body.String())
			}
			if tc.name != "rate limited" && tc.name != "platform unavailable" && len(f.sessions.starts) != 0 {
				t.Fatalf("refused start reached the session store: %v", f.sessions.starts)
			}
		})
	}
}

func TestFeishuRegistrationPollSavesCredentialsWithoutReturningThem(t *testing.T) {
	f := newFeishuRegistrationFixture()
	f.sessions.pollResult = feishu.RegistrationPollResult{Status: feishu.RegistrationSuccess, Region: "lark", AppID: "cli_1", AppSecret: "very-secret"}

	rec := f.do(t, "poll", `{"session_id":"session-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resp FeishuRegistrationPollResponse
	decodeFeishuRegJSON(t, rec, &resp)
	if resp.Status != feishuRegistrationConnected {
		t.Fatalf("status = %q", resp.Status)
	}
	if strings.Contains(rec.Body.String(), "very-secret") || strings.Contains(rec.Body.String(), "cli_1") {
		t.Fatalf("credentials leaked: %s", rec.Body.String())
	}
	if len(f.runtime.upserts) != 1 {
		t.Fatalf("upserts = %d", len(f.runtime.upserts))
	}
	saved := f.runtime.upserts[0]
	if saved.Disabled == nil || *saved.Disabled {
		t.Fatal("channel must be saved enabled")
	}
	for key, want := range map[string]string{"appId": "cli_1", "appSecret": "very-secret", "region": "lark", "inboundMode": "websocket", "setupMode": feishu.SetupModeRegistration} {
		if got := saved.Credentials[key]; got != want {
			t.Fatalf("credential %s = %v, want %q", key, got, want)
		}
	}
}

func TestFeishuRegistrationPollNeedsActivation(t *testing.T) {
	f := newFeishuRegistrationFixture()
	f.sessions.pollResult = feishu.RegistrationPollResult{Status: feishu.RegistrationSuccess, Region: "feishu", AppID: "cli_1", AppSecret: "s"}
	f.handler.activation = func(context.Context, map[string]any) (feishu.BotActivation, error) {
		return feishu.BotActivation{Known: true, Active: false}, nil
	}
	var resp FeishuRegistrationPollResponse
	decodeFeishuRegJSON(t, f.do(t, "poll", `{"session_id":"session-1"}`), &resp)
	if resp.Status != feishuRegistrationNeedsActivation {
		t.Fatalf("status = %q", resp.Status)
	}
}

func TestFeishuRegistrationPollKeepsCredentialsWhenEnableFails(t *testing.T) {
	f := newFeishuRegistrationFixture()
	f.sessions.pollResult = feishu.RegistrationPollResult{Status: feishu.RegistrationSuccess, Region: "feishu", AppID: "cli_1", AppSecret: "s"}
	f.runtime.enabledErr = fmt.Errorf("feishu configuration verification failed: %w: bot not ready", channel.ErrChannelDiscoveryFailed)

	var resp FeishuRegistrationPollResponse
	decodeFeishuRegJSON(t, f.do(t, "poll", `{"session_id":"session-1"}`), &resp)
	if resp.Status != feishuRegistrationSavedDisabled {
		t.Fatalf("status = %q", resp.Status)
	}
	if len(f.runtime.upserts) != 2 || f.runtime.upserts[1].Disabled == nil || !*f.runtime.upserts[1].Disabled {
		t.Fatalf("upserts = %+v, want an enabled attempt then a disabled save", f.runtime.upserts)
	}
}

func TestFeishuRegistrationPollRefusesConfiguredBot(t *testing.T) {
	f := newFeishuRegistrationFixture()
	f.sessions.pollResult = feishu.RegistrationPollResult{Status: feishu.RegistrationSuccess, Region: "feishu", AppID: "cli_1", AppSecret: "s"}
	f.configs.configured = true

	rec := f.do(t, "poll", `{"session_id":"session-1"}`)
	if rec.Code != http.StatusConflict || problemCode(t, rec) != string(apperror.CodeChannelAlreadyConfigured) {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if len(f.runtime.upserts) != 0 {
		t.Fatal("configured bot must not be overwritten")
	}
}

func TestFeishuRegistrationPollStatuses(t *testing.T) {
	cases := []struct {
		name   string
		result feishu.RegistrationPollResult
		err    error
		want   string
	}{
		{name: "pending", result: feishu.RegistrationPollResult{Status: feishu.RegistrationPending}, want: feishuRegistrationPending},
		{name: "denied", result: feishu.RegistrationPollResult{Status: feishu.RegistrationDenied}, want: feishuRegistrationDenied},
		{name: "expired", result: feishu.RegistrationPollResult{Status: feishu.RegistrationExpired}, want: feishuRegistrationExpired},
		{name: "terminal failure", err: fmt.Errorf("%w: poll: invalid_grant", feishu.ErrRegistrationFailed), want: feishuRegistrationFailed},
		{name: "transient failure", err: errors.New("connection reset"), want: feishuRegistrationPending},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFeishuRegistrationFixture()
			f.sessions.pollResult, f.sessions.pollErr = tc.result, tc.err
			rec := f.do(t, "poll", `{"session_id":"session-1"}`)
			var resp FeishuRegistrationPollResponse
			decodeFeishuRegJSON(t, rec, &resp)
			if rec.Code != http.StatusOK || resp.Status != tc.want {
				t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
			}
			if len(f.runtime.upserts) != 0 {
				t.Fatal("unfinished registration must not save a channel")
			}
		})
	}
}

func TestFeishuRegistrationPollRequiresSession(t *testing.T) {
	f := newFeishuRegistrationFixture()
	rec := f.do(t, "poll", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}
