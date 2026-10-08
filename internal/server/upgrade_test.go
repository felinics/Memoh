package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/auth"
	"github.com/felinics/memoh/internal/logger"
)

type upgradeLogTestHandler struct{}

func (upgradeLogTestHandler) Register(e *echo.Echo) {
	upgrader := websocket.Upgrader{}
	e.GET("/gorilla", func(c echo.Context) error {
		conn, err := upgrader.Upgrade(c.Response(), c.Request(), nil)
		if err != nil {
			return err
		}
		return conn.Close()
	})
	// runtime_connect.go hands coder/websocket the raw writer, not echo's.
	e.GET("/coder", func(c echo.Context) error {
		conn, err := coderws.Accept(c.Response().Writer, c.Request(), &coderws.AcceptOptions{
			Subprotocols: []string{"test"},
		})
		if err != nil {
			return err
		}
		return conn.CloseNow()
	})
}

// The access log is where an operator looks first when a WebSocket will not
// connect. A successful upgrade logged as 200 is indistinguishable from a
// handler that answered the request without upgrading.
func TestServerRequestLogReportsTheWebSocketHandshakeStatus(t *testing.T) {
	token, _, err := auth.GenerateToken("upgrade-log-user", "test-secret", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var logs syncLogBuffer
	srv := NewServer(logger.New(&logs, "info", "json"), ":0", "test-secret", upgradeLogTestHandler{})
	httpSrv := httptest.NewServer(srv.echo)
	t.Cleanup(httpSrv.Close)
	wsBase := "ws" + strings.TrimPrefix(httpSrv.URL, "http")

	gorillaConn, resp, err := websocket.DefaultDialer.Dial(wsBase+"/gorilla?token="+token, nil)
	if err != nil {
		t.Fatalf("gorilla dial: %v", err)
	}
	_ = resp.Body.Close()
	_ = gorillaConn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	coderConn, resp, err := coderws.Dial(ctx, wsBase+"/coder?token="+token, &coderws.DialOptions{Subprotocols: []string{"test"}})
	if err != nil {
		t.Fatalf("coder dial: %v", err)
	}
	if resp.Body != nil {
		_ = resp.Body.Close()
	}
	_ = coderConn.CloseNow()

	// A handshake the server refuses keeps the status it was refused with.
	// Gorilla writes it through echo; coder/websocket writes it past echo,
	// which used to leave the log at 200 and the error handler writing a
	// second response. The version header is what each rejects here.
	for _, path := range []string{"/gorilla", "/coder"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpSrv.URL+path+"?token="+token, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		req.Header.Set("Sec-WebSocket-Version", "12")
		resp, err := http.DefaultClient.Do(req) //nolint:gosec // G704: test-only URL from the local httptest server
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: refused handshake answered %d, want 400", path, resp.StatusCode)
		}
	}

	want := map[string][]int{"/gorilla": {101, 400}, "/coder": {101, 400}}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := logs.statusesByURI()
		if equalStatuses(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("request log statuses = %v, want %v\n%s", got, want, logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// equalStatuses ignores order: a handler that upgraded logs when it returns,
// which can be after the next request has been logged.
func equalStatuses(got, want map[string][]int) bool {
	if len(got) != len(want) {
		return false
	}
	for uri, statuses := range want {
		if !slices.Equal(slices.Sorted(slices.Values(got[uri])), statuses) {
			return false
		}
	}
	return true
}

// syncLogBuffer is written by the server's connection goroutines while the
// test reads it.
type syncLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncLogBuffer) statusesByURI() map[string][]int {
	out := map[string][]int{}
	for _, line := range strings.Split(strings.TrimSpace(b.String()), "\n") {
		var entry struct {
			Msg    string `json:"msg"`
			URI    string `json:"uri"`
			Status int    `json:"status"`
		}
		if json.Unmarshal([]byte(line), &entry) == nil && entry.Msg == "request" {
			out[entry.URI] = append(out[entry.URI], entry.Status)
		}
	}
	return out
}

// A writer that cannot be hijacked leaves the response to the error handler.
// Marking it switched anyway would make the handler think a response had
// been written and send the client nothing.
func TestFailedHijackLeavesTheResponseUnwritten(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	c := e.NewContext(req, httptest.NewRecorder())
	err := recordUpgradeStatus(func(c echo.Context) error {
		_, _, err := c.Response().Hijack()
		return err
	})(c)
	if err == nil {
		t.Fatal("a recorder was hijacked")
	}
	if c.Response().Committed || c.Response().Status == http.StatusSwitchingProtocols {
		t.Errorf("committed=%v status=%d after a failed hijack", c.Response().Committed, c.Response().Status)
	}
}
