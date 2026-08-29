package handlers

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/workspace/bridge"
	"github.com/felinics/memoh/internal/workspace/bridgepb"
	"github.com/felinics/memoh/internal/workspace/bridgesvc"
)

const browserTestBufSize = 1 << 20

func TestBrowserSessionStoreExpiresAndTouches(t *testing.T) {
	t.Parallel()

	store := newBrowserSessionStore(time.Minute)
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	session, err := store.create("bot-1", 5173, now)
	if err != nil {
		t.Fatalf("create session failed: %v", err)
	}
	if session.ID == "" {
		t.Fatal("session id should not be empty")
	}
	if session.ExpiresAt != now.Add(time.Minute) {
		t.Fatalf("expires_at = %v, want %v", session.ExpiresAt, now.Add(time.Minute))
	}

	touched, ok := store.touch(session.ID, now.Add(30*time.Second))
	if !ok {
		t.Fatal("touch should find session before expiry")
	}
	if touched.ExpiresAt != now.Add(90*time.Second) {
		t.Fatalf("touch expires_at = %v, want %v", touched.ExpiresAt, now.Add(90*time.Second))
	}
	if _, ok := store.touchForBot(session.ID, "bot-2", now.Add(45*time.Second)); ok {
		t.Fatal("touchForBot should reject sessions owned by another bot")
	}
	if touched, ok := store.touchForBot(session.ID, "bot-1", now.Add(time.Minute)); !ok {
		t.Fatal("touchForBot should find session owned by the bot")
	} else if touched.ExpiresAt != now.Add(2*time.Minute) {
		t.Fatalf("touchForBot expires_at = %v, want %v", touched.ExpiresAt, now.Add(2*time.Minute))
	}
	if _, ok := store.touch(session.ID, now.Add(3*time.Minute)); ok {
		t.Fatal("expired session should be pruned")
	}
}

func TestBrowserSessionStoreDeleteForBot(t *testing.T) {
	t.Parallel()

	store := newBrowserSessionStore(time.Minute)
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	session, err := store.create("bot-1", 5173, now)
	if err != nil {
		t.Fatalf("create session failed: %v", err)
	}
	if store.deleteForBot(session.ID, "bot-2") {
		t.Fatal("deleteForBot should reject sessions owned by another bot")
	}
	if _, ok := store.touch(session.ID, now.Add(10*time.Second)); !ok {
		t.Fatal("session should still exist after rejected delete")
	}
	if !store.deleteForBot(session.ID, "bot-1") {
		t.Fatal("deleteForBot should delete sessions owned by the bot")
	}
	if _, ok := store.touch(session.ID, now.Add(20*time.Second)); ok {
		t.Fatal("session should be deleted")
	}
}

func TestValidateBrowserPort(t *testing.T) {
	t.Parallel()

	for _, port := range []int{1, 5173, 65535} {
		if err := validateBrowserPort(port); err != nil {
			t.Fatalf("validateBrowserPort(%d) unexpected error: %v", port, err)
		}
	}
	for _, port := range []int{0, -1, 65536} {
		if err := validateBrowserPort(port); err == nil {
			t.Fatalf("validateBrowserPort(%d) expected error", port)
		}
	}
}

func TestBrowserProxyURLAndHostParsing(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/bots/bot-1/container/browser/sessions", nil)
	req.Host = "127.0.0.1:8080"
	got := buildBrowserProxyURL(req, "0123456789abcdef0123456789abcdef", "app?q=1")
	want := "http://0123456789abcdef0123456789abcdef.browser.localhost:8080/app?q=1"
	if got != want {
		t.Fatalf("browser proxy url = %q, want %q", got, want)
	}

	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "memoh.example.com")
	got = buildBrowserProxyURL(req, "0123456789abcdef0123456789abcdef", "/")
	want = "https://0123456789abcdef0123456789abcdef.browser.memoh.example.com/"
	if got != want {
		t.Fatalf("forwarded browser proxy url = %q, want %q", got, want)
	}

	id, ok := browserSessionIDFromHost("0123456789abcdef0123456789abcdef.browser.memoh.example.com:443")
	if !ok || id != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("browser session id = %q, %v", id, ok)
	}
	if _, ok := browserSessionIDFromHost("not-a-session.browser.memoh.example.com"); ok {
		t.Fatal("invalid session id should not parse")
	}
}

func TestCleanBrowserProxyResponseHeaders(t *testing.T) {
	t.Parallel()

	header := http.Header{}
	header.Set("X-Frame-Options", "DENY")
	header.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; script-src 'self'")
	header.Set("Content-Security-Policy-Report-Only", "frame-ancestors 'none'")

	cleanBrowserProxyResponseHeaders(header)

	if got := header.Get("X-Frame-Options"); got != "" {
		t.Fatalf("X-Frame-Options = %q, want empty", got)
	}
	if got := header.Get("Content-Security-Policy"); got != "default-src 'self'; script-src 'self'" {
		t.Fatalf("CSP = %q", got)
	}
	if got := header.Get("Content-Security-Policy-Report-Only"); got != "" {
		t.Fatalf("report-only CSP = %q, want empty", got)
	}
}

func TestBrowserReverseProxyHTTPThroughBridgeTunnel(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hello" || r.URL.RawQuery != "name=memoh" {
			t.Fatalf("upstream URL = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		if !strings.HasPrefix(r.Host, "127.0.0.1:") {
			t.Fatalf("upstream host = %q, want 127.0.0.1:<port>", r.Host)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "payload" {
			t.Fatalf("upstream body = %q", string(body))
		}
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(upstream.Close)
	port := testServerPort(t, upstream.URL)

	client := newBrowserTestBridgeClient(t)
	proxy := newBrowserReverseProxy(client, port)

	req := httptest.NewRequest(http.MethodPost, "http://session.browser.localhost/hello?name=memoh", bytes.NewBufferString("payload"))
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req) //nolint:gosec // Test proxy target is a local httptest server reached through the bridge tunnel.

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "ok" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "" {
		t.Fatalf("X-Frame-Options = %q, want empty", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != "default-src 'self'" {
		t.Fatalf("CSP = %q", got)
	}
}

func TestBrowserReverseProxyWebSocketThroughBridgeTunnel(t *testing.T) {
	t.Parallel()

	upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upstream upgrade failed: %v", err)
		}
		defer func() { _ = conn.Close() }()
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("upstream read failed: %v", err)
		}
		if err := conn.WriteMessage(msgType, append([]byte("echo:"), data...)); err != nil {
			t.Fatalf("upstream write failed: %v", err)
		}
	}))
	t.Cleanup(upstream.Close)
	port := testServerPort(t, upstream.URL)

	client := newBrowserTestBridgeClient(t)
	proxy := newBrowserReverseProxy(client, port)
	proxyServer := httptest.NewServer(http.HandlerFunc(proxy.ServeHTTP))
	t.Cleanup(proxyServer.Close)

	wsURL := "ws" + strings.TrimPrefix(proxyServer.URL, "http") + "/socket"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		t.Fatalf("dial proxy websocket failed: %v", err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	defer func() { _ = conn.Close() }()
	if err := conn.WriteMessage(websocket.TextMessage, []byte("hello")); err != nil {
		t.Fatalf("write websocket failed: %v", err)
	}
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read websocket failed: %v", err)
	}
	if string(data) != "echo:hello" {
		t.Fatalf("websocket response = %q", string(data))
	}
}

func newBrowserTestBridgeClient(t *testing.T) *bridge.Client {
	t.Helper()

	lis := bufconn.Listen(browserTestBufSize)
	srv := grpc.NewServer()
	bridgepb.RegisterContainerServiceServer(srv, bridgesvc.New(bridgesvc.Options{}))

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(lis)
	}()
	t.Cleanup(func() {
		srv.Stop()
		<-done
	})

	conn, err := grpc.NewClient("passthrough://browser-test",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc client failed: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return bridge.NewClientFromConn(conn)
}

func testServerPort(t *testing.T, rawURL string) int {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse server URL failed: %v", err)
	}
	_, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatalf("split server host failed: %v", err)
	}
	value, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("parse server port failed: %v", err)
	}
	return value
}
