package weixin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/server"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func upstreamResponse(status int, body string) roundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: r}, nil
	}
}

// serveQR serves one QR request through an HTTP shell with the access log
// and the error handler, logging the handler and the shell to one buffer.
func serveQR(t *testing.T, lifecycle *channel.Lifecycle, upstream roundTripFunc, path, body string) (*httptest.ResponseRecorder, []map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h := NewQRHandler(log, lifecycle)
	h.client.httpClient = &http.Client{Transport: upstream}
	e := echo.New()
	e.HTTPErrorHandler = server.NewHTTPErrorHandler(log)
	e.Use(server.AccessLog(log))
	h.Register(e)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode log record %q: %v", line, err)
		}
		records = append(records, record)
	}
	return rec, records
}

// requireFailureRecord checks that the response does not carry the cause and
// that the request record is the only error record and carries it.
func requireFailureRecord(t *testing.T, rec *httptest.ResponseRecorder, records []map[string]any, status int, fault, cause string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, status, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), cause) {
		t.Fatalf("body leaks the cause: %s", rec.Body.String())
	}
	var request map[string]any
	for _, record := range records {
		if record["msg"] == "request" {
			request = record
			continue
		}
		if record["level"] == "ERROR" || strings.Contains(record["error"].(string), cause) {
			t.Fatalf("record %#v repeats the failure the request record holds", record)
		}
	}
	text, _ := request["error"].(string)
	if request == nil || request["fault"] != fault || !strings.Contains(text, cause) {
		t.Fatalf("request record = %#v, want fault %s with %q", request, fault, cause)
	}
}

func TestQRStartRecordsUpstreamFailureOnce(t *testing.T) {
	t.Parallel()

	upstream := func(*http.Request) (*http.Response, error) { return nil, errors.New("ilink secret failure") }
	rec, records := serveQR(t, nil, upstream, "/bots/bot-1/channel/weixin/qr/start", "")

	requireFailureRecord(t, rec, records, http.StatusInternalServerError, "dependency", "ilink secret failure")
}

func TestQRPollRecordsUpstreamFailureOnce(t *testing.T) {
	t.Parallel()

	rec, records := serveQR(t, nil, upstreamResponse(http.StatusForbidden, "ilink secret refusal"),
		"/bots/bot-1/channel/weixin/qr/poll", `{"qr_code":"code-1"}`)

	requireFailureRecord(t, rec, records, http.StatusInternalServerError, "dependency", "ilink secret refusal")
}

func TestQRPollRecordsCredentialSaveFailureOnce(t *testing.T) {
	t.Parallel()

	rec, records := serveQR(t, channel.NewLifecycle(nil, nil),
		upstreamResponse(http.StatusOK, `{"status":"confirmed","bot_token":"token-1"}`),
		"/bots/bot-1/channel/weixin/qr/poll", `{"qr_code":"code-1"}`)

	requireFailureRecord(t, rec, records, http.StatusInternalServerError, "server", "channel lifecycle store not configured")
}

// disabledConfigStore holds a disabled config for the bot and fails to
// enable it.
type disabledConfigStore struct{ channel.LifecycleStore }

func (disabledConfigStore) ResolveEffectiveConfig(_ context.Context, botID string, channelType channel.ChannelType) (channel.ChannelConfig, error) {
	return channel.ChannelConfig{BotID: botID, ChannelType: channelType, Disabled: true}, nil
}

func (disabledConfigStore) UpdateConfigDisabled(context.Context, string, channel.ChannelType, bool) (channel.ChannelConfig, error) {
	return channel.ChannelConfig{}, errors.New("config table secret failure")
}

type noopController struct{}

func (noopController) EnsureConnection(context.Context, channel.ChannelConfig) error { return nil }
func (noopController) RemoveConnection(context.Context, string, channel.ChannelType) {}

func TestQRPollRecordsBoundChannelEnableFailureOnce(t *testing.T) {
	t.Parallel()

	rec, records := serveQR(t, channel.NewLifecycle(disabledConfigStore{}, noopController{}),
		upstreamResponse(http.StatusOK, `{"status":"binded_redirect"}`),
		"/bots/bot-1/channel/weixin/qr/poll", `{"qr_code":"code-1"}`)

	requireFailureRecord(t, rec, records, http.StatusInternalServerError, "server", "config table secret failure")
}

func TestQRPollRecordsBindFailureCause(t *testing.T) {
	t.Parallel()

	rec, records := serveQR(t, nil, upstreamResponse(http.StatusOK, `{}`),
		"/bots/bot-1/channel/weixin/qr/poll", `{"qr_code":`)

	requireFailureRecord(t, rec, records, http.StatusBadRequest, "client", "unexpected EOF")
}
