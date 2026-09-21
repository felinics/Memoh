package httpx_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"github.com/felinics/memoh/internal/httpx"
	"github.com/felinics/memoh/internal/logger"
)

// The path this covers is the reason the middleware exists: an identifier the
// client is given has to name something the logs can find. Before this, the id
// reached the response header and the access log line, and nothing a handler
// logged while serving the request carried it.
func TestRequestIDReachesWhatAHandlerLogs(t *testing.T) {
	var buf bytes.Buffer
	log := logger.New(&buf, "info", "json")

	e := echo.New()
	e.Use(middleware.RequestID())
	e.Use(httpx.RequestIDContext)
	e.GET("/probe", func(c echo.Context) error {
		// A handler logs the way the rest of the codebase should: with the
		// request's context, and without naming the request id itself.
		log.InfoContext(c.Request().Context(), "probe handled", slog.String("bot_id", "b_1"))
		return c.NoContent(http.StatusNoContent)
	})

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/probe", nil))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	header := rec.Header().Get(echo.HeaderXRequestID)
	if header == "" {
		t.Fatal("no request id on the response")
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &payload); err != nil {
		t.Fatalf("json.Unmarshal(%q) = %v", buf.String(), err)
	}
	if got := payload["msg"]; got != "probe handled" {
		t.Errorf("msg = %v, want the handler's message", got)
	}
	// The identifier in the log line must be the one the client was given,
	// otherwise a reported id searches for a request that was never served.
	if got, _ := payload["request_id"].(string); got != header {
		t.Errorf("request_id = %q, response header = %q", got, header)
	}
}

func TestRequestIDContextPrefersTheAssignedIDOverAClientHeader(t *testing.T) {
	// echo's RequestID middleware honours an inbound X-Request-Id. Whatever it
	// settles on is what the client sees, so that is what the logs must say —
	// this test fails if the middleware ever reads the two from different
	// places.
	const clientSupplied = "client-supplied-id"
	var buf bytes.Buffer
	log := logger.New(&buf, "info", "json")

	e := echo.New()
	e.Use(middleware.RequestID())
	e.Use(httpx.RequestIDContext)
	e.GET("/probe", func(c echo.Context) error {
		log.InfoContext(c.Request().Context(), "probe handled")
		return c.NoContent(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set(echo.HeaderXRequestID, clientSupplied)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	var payload map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &payload); err != nil {
		t.Fatalf("json.Unmarshal(%q) = %v", buf.String(), err)
	}
	if got, _ := payload["request_id"].(string); got != rec.Header().Get(echo.HeaderXRequestID) {
		t.Errorf("request_id = %q, response header = %q", got, rec.Header().Get(echo.HeaderXRequestID))
	}
}

func TestRequestIDContextIsAbsentWithoutTheAssigningMiddleware(t *testing.T) {
	// Installed alone it must not invent an id. A record with no request_id is
	// honest; one with a fabricated id is worse than none.
	var buf bytes.Buffer
	log := logger.New(&buf, "info", "json")

	e := echo.New()
	e.Use(httpx.RequestIDContext)
	e.GET("/probe", func(c echo.Context) error {
		log.InfoContext(c.Request().Context(), "probe handled")
		return c.NoContent(http.StatusNoContent)
	})

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/probe", nil))

	var payload map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &payload); err != nil {
		t.Fatalf("json.Unmarshal(%q) = %v", buf.String(), err)
	}
	if _, ok := payload["request_id"]; ok {
		t.Errorf("request_id present with no assigning middleware: %v", payload)
	}
}

// Moved here with the sanitizer it covers. The tokens it strips are why both
// HTTP shells must use the same function rather than each keeping a copy.
func TestSafeRequestLogURIStripsAnAuthorisingQuery(t *testing.T) {
	t.Parallel()

	// A public media URL is the concrete case: it authorises with a signature
	// in the query, so keeping the query would put the credential in the log.
	const path = "/channels/line/public/media/bot-1/" +
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/preview.jpg"
	u, err := url.Parse(path + "?exp=123&sig=secret")
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	if got := httpx.SafeRequestLogURI(u, u.RequestURI()); got != path {
		t.Fatalf("SafeRequestLogURI = %q, want %q", got, path)
	}
}

func TestSafeRequestLogURIDropsEveryQuery(t *testing.T) {
	t.Parallel()

	// The query goes for all requests, not only the paths known to carry a
	// token: a new such path would otherwise start logging its credential
	// the day it is added, and nothing would say so.
	u, err := url.Parse("/channels/telegram/webhook/cfg-1?update_id=7")
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	want := "/channels/telegram/webhook/cfg-1"
	if got := httpx.SafeRequestLogURI(u, u.RequestURI()); got != want {
		t.Fatalf("SafeRequestLogURI = %q, want %q", got, want)
	}
}

func TestSafeRequestLogURIFallsBackToParsingTheRawURI(t *testing.T) {
	t.Parallel()

	// echo hands the middleware a nil *url.URL on some malformed requests;
	// the raw target is then all there is to work from, and it must still
	// lose its query.
	if got := httpx.SafeRequestLogURI(nil, "/v1/items?token=secret"); got != "/v1/items" {
		t.Fatalf("SafeRequestLogURI = %q, want %q", got, "/v1/items")
	}
	if got := httpx.SafeRequestLogURI(nil, "://not a uri"); got != "" {
		t.Fatalf("SafeRequestLogURI = %q, want empty for an unparseable target", got)
	}
}
