package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

// TestBeginSSEResponseSetsProxyFriendlyHeaders guards the header contract for
// every SSE endpoint: X-Accel-Buffering tells nginx (and compatible reverse
// proxies) to disable response buffering so events reach the browser as soon
// as the handler flushes them. Without it, proxy_buffering (on by default)
// can hold events back indefinitely.
func TestBeginSSEResponseSetsProxyFriendlyHeaders(t *testing.T) {
	t.Parallel()

	e := echo.New()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if _, _, err := beginSSEResponse(c); err != nil {
		t.Fatalf("beginSSEResponse: %v", err)
	}

	want := map[string]string{
		echo.HeaderContentType:  "text/event-stream",
		echo.HeaderCacheControl: "no-cache",
		echo.HeaderConnection:   "keep-alive",
		"X-Accel-Buffering":     "no",
	}
	for key, value := range want {
		if got := rec.Header().Get(key); got != value {
			t.Fatalf("header %s = %q, want %q", key, got, value)
		}
	}
}
