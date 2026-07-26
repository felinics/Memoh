package http_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	httpx "github.com/memohai/memoh/domains/api/http"
)

func TestHardenServerLeavesNoUnboundedTimeout(t *testing.T) {
	t.Parallel()

	srv := &http.Server{} //nolint:gosec // G112: the zero value is the subject under test
	httpx.HardenServer(srv)

	for name, got := range map[string]time.Duration{
		"ReadHeaderTimeout": srv.ReadHeaderTimeout,
		"ReadTimeout":       srv.ReadTimeout,
		"WriteTimeout":      srv.WriteTimeout,
		"IdleTimeout":       srv.IdleTimeout,
	} {
		if got <= 0 {
			t.Errorf("%s = %v, want a bounded duration", name, got)
		}
	}
	if srv.MaxHeaderBytes <= 0 {
		t.Errorf("MaxHeaderBytes = %d, want a bound", srv.MaxHeaderBytes)
	}
}

// The production risk this guards is a ResponseWriter wrapper (compression,
// instrumentation) that breaks http.ResponseController's unwrap chain: the
// deadline would then survive and silently truncate every SSE stream.
func TestClearWriteDeadlineOutlivesServerWriteTimeout(t *testing.T) {
	t.Parallel()

	const writeTimeout = 100 * time.Millisecond

	e := echo.New()
	e.GET("/stream", func(c echo.Context) error {
		if err := httpx.PrepareSSE(c); err != nil {
			return err
		}
		c.Response().WriteHeader(http.StatusOK)
		time.Sleep(4 * writeTimeout)
		return httpx.WriteSSEData(c.Response(), c.Response(), "late")
	})

	ts := httptest.NewUnstartedServer(e)
	ts.Config.WriteTimeout = writeTimeout
	ts.Start()
	defer ts.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/stream", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body after write timeout elapsed: %v", err)
	}
	if want := "data: late\n\n"; string(body) != want {
		t.Fatalf("body = %q, want %q", body, want)
	}
}

func TestClearReadDeadlineOutlivesServerReadTimeout(t *testing.T) {
	t.Parallel()

	const readTimeout = 100 * time.Millisecond

	e := echo.New()
	e.POST("/upload", func(c echo.Context) error {
		if err := httpx.ClearReadDeadline(c); err != nil {
			return err
		}
		time.Sleep(4 * readTimeout)
		body, err := io.ReadAll(c.Request().Body)
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, string(body))
	})

	ts := httptest.NewUnstartedServer(e)
	ts.Config.ReadTimeout = readTimeout
	ts.Start()
	defer ts.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/upload",
		io.NopCloser(newSlowReader("payload", 2*readTimeout)))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set(echo.HeaderContentType, echo.MIMETextPlain)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "payload" {
		t.Fatalf("body = %q, want %q", body, "payload")
	}
}

// slowReader delays before yielding its payload so the request body is still
// being read once the server's ReadTimeout would have fired.
type slowReader struct {
	payload []byte
	delay   time.Duration
	sent    bool
}

func newSlowReader(payload string, delay time.Duration) *slowReader {
	return &slowReader{payload: []byte(payload), delay: delay}
}

func (r *slowReader) Read(p []byte) (int, error) {
	if r.sent {
		return 0, io.EOF
	}
	time.Sleep(r.delay)
	r.sent = true
	return copy(p, r.payload), nil
}
