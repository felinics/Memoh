package channel

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/server"
)

// serveWebhook serves one request through an HTTP shell with the access log
// and the error handler, logging the handler and the shell to one buffer.
func serveWebhook(t *testing.T, h *WebhookHandler, path string) (*httptest.ResponseRecorder, []map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h.logger = log
	e := echo.New()
	e.HTTPErrorHandler = server.NewHTTPErrorHandler(log)
	e.Use(server.AccessLog(log))
	h.Register(e)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
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

func requireOneRequestRecord(t *testing.T, rec *httptest.ResponseRecorder, records []map[string]any, status int, fault, cause string) map[string]any {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, status, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), cause) {
		t.Fatalf("body leaks the cause: %s", rec.Body.String())
	}
	if len(records) != 1 || records[0]["msg"] != "request" {
		t.Fatalf("records = %#v, want the one request record", records)
	}
	record := records[0]
	text, _ := record["error"].(string)
	if record["fault"] != fault || !strings.Contains(text, cause) {
		t.Fatalf("request record = %#v, want fault %s with %q", record, fault, cause)
	}
	return record
}

func newFailureWebhookHandler(adapter *fakeWebhookAdapter, store *fakeWebhookStore) *WebhookHandler {
	registry := NewRegistry()
	registry.MustRegister(adapter)
	h := NewWebhookServerHandler(nil, (*Store)(nil), (*Manager)(nil))
	h.store = store
	h.manager = &fakeWebhookManager{registry: registry}
	h.registry = registry
	return h
}

func TestGenericWebhookHandlerRecordsAdapterFailureOnce(t *testing.T) {
	t.Parallel()

	adapter := &fakeWebhookAdapter{channelType: ChannelType("testhook"), err: errors.New("adapter secret failure")}
	h := newFailureWebhookHandler(adapter, &fakeWebhookStore{configs: []ChannelConfig{{ID: "cfg-1", BotID: "bot-1", ChannelType: adapter.channelType}}})

	rec, records := serveWebhook(t, h, "/channels/testhook/webhook/cfg-1")

	record := requireOneRequestRecord(t, rec, records, http.StatusInternalServerError, "server", "adapter secret failure")
	attrs, _ := record["error_attrs"].(map[string]any)
	if attrs["channel"] != "testhook" || attrs["config_id"] != "cfg-1" {
		t.Fatalf("error_attrs = %#v, want channel and config_id", record["error_attrs"])
	}
}

func TestGenericWebhookHandlerKeepsAdapterHTTPStatus(t *testing.T) {
	t.Parallel()

	refused := echo.NewHTTPError(http.StatusForbidden, "invalid signature").WithInternal(errors.New("signature mismatch detail"))
	adapter := &fakeWebhookAdapter{channelType: ChannelType("testhook"), err: refused}
	h := newFailureWebhookHandler(adapter, &fakeWebhookStore{configs: []ChannelConfig{{ID: "cfg-1", BotID: "bot-1", ChannelType: adapter.channelType}}})

	rec, records := serveWebhook(t, h, "/channels/testhook/webhook/cfg-1")

	requireOneRequestRecord(t, rec, records, http.StatusForbidden, "client", "signature mismatch detail")
}

func TestGenericWebhookHandlerRecordsStoreFailureOnce(t *testing.T) {
	t.Parallel()

	adapter := &fakeWebhookAdapter{channelType: ChannelType("testhook")}
	h := newFailureWebhookHandler(adapter, &fakeWebhookStore{err: errors.New("database secret failure")})

	rec, records := serveWebhook(t, h, "/channels/testhook/webhook/cfg-1")

	record := requireOneRequestRecord(t, rec, records, http.StatusInternalServerError, "server", "database secret failure")
	if record["error_source"] == nil {
		t.Fatalf("request record = %#v, want the frame the failure was wrapped at", record)
	}
}

func TestGenericWebhookHandlerRecordsUnknownPlatformCause(t *testing.T) {
	t.Parallel()

	adapter := &fakeWebhookAdapter{channelType: ChannelType("testhook")}
	h := newFailureWebhookHandler(adapter, &fakeWebhookStore{})

	rec, records := serveWebhook(t, h, "/channels/nosuchplatform/webhook/cfg-1")

	requireOneRequestRecord(t, rec, records, http.StatusBadRequest, "client", "nosuchplatform")

	c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/", nil), httptest.NewRecorder())
	c.SetParamNames("platform", "config_id")
	c.SetParamValues("nosuchplatform", "cfg-1")
	var httpErr *echo.HTTPError
	if err := h.Handle(c); !errors.As(err, &httpErr) || httpErr.Message != "unknown channel platform" || httpErr.Internal == nil {
		t.Fatalf("error = %v, want a constant message with the parse error as its cause", err)
	}
}
