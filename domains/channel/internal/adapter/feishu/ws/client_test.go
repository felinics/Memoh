package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

// fakeDispatcher records dispatched payloads and lets the test control the
// returned response/error per call.
type fakeDispatcher struct {
	mu       sync.Mutex
	payloads [][]byte
	respFn   func([]byte) (interface{}, error)
}

func (d *fakeDispatcher) Do(_ context.Context, payload []byte) (interface{}, error) {
	d.mu.Lock()
	d.payloads = append(d.payloads, payload)
	respFn := d.respFn
	d.mu.Unlock()
	if respFn == nil {
		return nil, nil
	}
	return respFn(payload)
}

func (d *fakeDispatcher) seen() [][]byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([][]byte, len(d.payloads))
	copy(out, d.payloads)
	return out
}

// mockGateway emulates the Feishu open-api gateway: HTTP endpoint
// discovery plus websocket upgrade, served by a single httptest.Server.
type mockGateway struct {
	t   testing.TB
	srv *httptest.Server

	mu          sync.Mutex
	conns       []*websocket.Conn
	upgradeHook func(*websocket.Conn)
	clientCfg   *larkws.ClientConfig
	endpointErr struct {
		code int // when non-zero, return as EndpointResp.Code
		msg  string
	}
	handshakeStatus    int
	handshakeAuthCode  int
	handshakeStatusMsg string
}

// mockGatewayOpt mutates an unstarted httptest.Server so callers can set
// Server.Config fields like ConnState. http.Server reads those from its
// own goroutine, so they must be set before Start.
type mockGatewayOpt func(*httptest.Server)

func newMockGateway(t testing.TB, opts ...mockGatewayOpt) *mockGateway {
	g := &mockGateway{t: t}
	mux := http.NewServeMux()
	mux.HandleFunc(larkws.GenEndpointUri, g.handleEndpoint)
	mux.HandleFunc("/ws", g.handleUpgrade)
	g.srv = httptest.NewUnstartedServer(mux)
	for _, opt := range opts {
		opt(g.srv)
	}
	g.srv.Start()
	t.Cleanup(g.close)
	return g
}

func (g *mockGateway) close() {
	g.mu.Lock()
	conns := g.conns
	g.conns = nil
	g.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
	g.srv.Close()
}

func (g *mockGateway) domain() string { return g.srv.URL }

func (g *mockGateway) handleEndpoint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, _ := io.ReadAll(r.Body)
	defer func() { _ = r.Body.Close() }()
	var req map[string]string
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req["AppID"] == "" || req["AppSecret"] == "" {
		http.Error(w, "missing creds", http.StatusBadRequest)
		return
	}
	g.mu.Lock()
	endpointErr := g.endpointErr
	clientCfg := g.clientCfg
	g.mu.Unlock()

	if endpointErr.code != 0 {
		_ = json.NewEncoder(w).Encode(larkws.EndpointResp{
			Code: endpointErr.code,
			Msg:  endpointErr.msg,
		})
		return
	}

	wsURL := strings.Replace(g.srv.URL, "http://", "ws://", 1) + "/ws?service_id=42&device_id=test-conn"
	resp := larkws.EndpointResp{
		Code: larkws.OK,
		Data: &larkws.Endpoint{
			Url:          wsURL,
			ClientConfig: clientCfg,
		},
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// waitForConn blocks until the gateway has accepted at least n upgraded
// websocket connections, then returns the n-th one. The poll keeps tests
// race-free without sprinkling sleeps inline.
func (g *mockGateway) waitForConn(t testing.TB, n int, timeout time.Duration) *websocket.Conn {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		g.mu.Lock()
		if len(g.conns) >= n {
			c := g.conns[n-1]
			g.mu.Unlock()
			return c
		}
		g.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("websocket connection #%d not seen within %v", n, timeout)
	return nil
}

func (g *mockGateway) handleUpgrade(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	hsStatus := g.handshakeStatus
	hsAuth := g.handshakeAuthCode
	hsMsg := g.handshakeStatusMsg
	hook := g.upgradeHook
	g.mu.Unlock()

	if hsStatus != 0 {
		w.Header().Set(larkws.HeaderHandshakeStatus, strconv.Itoa(hsStatus))
		w.Header().Set(larkws.HeaderHandshakeMsg, hsMsg)
		if hsAuth != 0 {
			w.Header().Set(larkws.HeaderHandshakeAuthErrCode, strconv.Itoa(hsAuth))
		}
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		g.t.Errorf("upgrade failed: %v", err)
		return
	}
	g.mu.Lock()
	g.conns = append(g.conns, conn)
	g.mu.Unlock()
	if hook != nil {
		hook(conn)
	}
}

// pushEvent fabricates an event-type data frame and writes it to the given
// server-side conn.
func (*mockGateway) pushEvent(conn *websocket.Conn, msgID, eventJSON string) error {
	headers := larkws.Headers{}
	headers.Add(larkws.HeaderType, string(larkws.MessageTypeEvent))
	headers.Add(larkws.HeaderMessageID, msgID)
	headers.Add(larkws.HeaderTraceID, "trace-"+msgID)
	headers.Add(larkws.HeaderSum, "1")
	headers.Add(larkws.HeaderSeq, "0")
	frame := &larkws.Frame{
		Method:  int32(larkws.FrameTypeData),
		Headers: headers,
		Payload: []byte(eventJSON),
	}
	bs, err := frame.Marshal()
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.BinaryMessage, bs)
}

// pushEventFragment writes one fragment of a multi-fragment event.
func (*mockGateway) pushEventFragment(conn *websocket.Conn, msgID string, sum, seq int, payload []byte) error {
	headers := larkws.Headers{}
	headers.Add(larkws.HeaderType, string(larkws.MessageTypeEvent))
	headers.Add(larkws.HeaderMessageID, msgID)
	headers.Add(larkws.HeaderSum, strconv.Itoa(sum))
	headers.Add(larkws.HeaderSeq, strconv.Itoa(seq))
	frame := &larkws.Frame{
		Method:  int32(larkws.FrameTypeData),
		Headers: headers,
		Payload: payload,
	}
	bs, err := frame.Marshal()
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.BinaryMessage, bs)
}

// readResponseFrame reads a single frame from the server-side conn and
// returns its decoded representation.
func (*mockGateway) readResponseFrame(conn *websocket.Conn) (*larkws.Frame, error) {
	mt, raw, err := conn.ReadMessage()
	if err != nil {
		return nil, err
	}
	if mt != websocket.BinaryMessage {
		return nil, fmt.Errorf("unexpected message type %d", mt)
	}
	frame := &larkws.Frame{}
	if err := frame.Unmarshal(raw); err != nil {
		return nil, err
	}
	return frame, nil
}

// writeServerPong sends a pong frame from the server side carrying an
// optional ClientConfig payload. The client treats the embedded
// PingInterval as a cadence update via handleControl.
func writeServerPong(conn *websocket.Conn, cfg *larkws.ClientConfig) error {
	headers := larkws.Headers{}
	headers.Add(larkws.HeaderType, string(larkws.MessageTypePong))
	var payload []byte
	if cfg != nil {
		bs, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		payload = bs
	}
	frame := &larkws.Frame{
		Method:  int32(larkws.FrameTypeControl),
		Headers: headers,
		Payload: payload,
	}
	bs, err := frame.Marshal()
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.BinaryMessage, bs)
}

func newTestClient(domain string, overrides ...func(*Config)) *Client {
	cfg := Config{
		AppID:            "app",
		AppSecret:        "secret",
		Domain:           domain,
		Logger:           slog.New(slog.DiscardHandler),
		PingInterval:     50 * time.Millisecond,
		HandshakeTimeout: 2 * time.Second,
		CloseGracePeriod: 200 * time.Millisecond,
	}
	for _, fn := range overrides {
		fn(&cfg)
	}
	return New(cfg)
}

// TestRunDispatchesEventAndAcks covers the happy path: connect, receive
// event, dispatch, ack.
func TestRunDispatchesEventAndAcks(t *testing.T) {
	t.Parallel()
	gw := newMockGateway(t)

	gw.upgradeHook = func(conn *websocket.Conn) {
		go func() {
			// give the client a moment to enter readLoop
			time.Sleep(10 * time.Millisecond)
			_ = gw.pushEvent(conn, "msg-1", `{"schema":"2.0","event":{}}`)
		}()
	}

	disp := &fakeDispatcher{}
	client := newTestClient(gw.domain())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(ctx, disp) }()

	conn := gw.waitForConn(t, 1, 2*time.Second)
	frame, err := gw.readResponseFrame(conn)
	if err != nil {
		t.Fatalf("read response frame: %v", err)
	}
	var ack larkws.Response
	if err := json.Unmarshal(frame.Payload, &ack); err != nil {
		t.Fatalf("decode ack payload: %v (raw=%q)", err, frame.Payload)
	}
	if ack.StatusCode != http.StatusOK {
		t.Errorf("ack status = %d, want 200", ack.StatusCode)
	}

	cancel()
	if err := <-runDone; err != nil {
		t.Errorf("Run returned error on cancel: %v", err)
	}

	if got := disp.seen(); len(got) != 1 {
		t.Fatalf("dispatcher saw %d payloads, want 1", len(got))
	}
}

// TestRunHonorsContextCancel covers the original bug: ctx cancel must
// close the TCP conn and return nil, leaving no goroutine inside Run.
func TestRunHonorsContextCancel(t *testing.T) {
	t.Parallel()
	gw := newMockGateway(t)

	disp := &fakeDispatcher{}
	client := newTestClient(gw.domain())

	ctx, cancel := context.WithCancel(context.Background())

	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(ctx, disp) }()

	conn := gw.waitForConn(t, 1, 2*time.Second)
	cancel()

	select {
	case err := <-runDone:
		if err != nil {
			t.Errorf("Run after cancel returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of ctx cancel — connection leak suspected")
	}

	// Server-side ReadMessage must observe a close.
	_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Error("server still sees an open connection after client Run returned")
	}
}

// TestRunReturnsErrorOnServerClose verifies that a server-side disconnect is
// surfaced as an error so the caller knows to reconnect.
func TestRunReturnsErrorOnServerClose(t *testing.T) {
	t.Parallel()
	gw := newMockGateway(t)
	gw.upgradeHook = func(conn *websocket.Conn) {
		go func() {
			time.Sleep(20 * time.Millisecond)
			_ = conn.Close()
		}()
	}

	disp := &fakeDispatcher{}
	client := newTestClient(gw.domain())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := client.Run(ctx, disp)
	if err == nil {
		t.Fatal("Run returned nil on server-side close, want error")
	}
}

// TestRunHandshakeExceedConnLimitIsTransient verifies that
// AuthFailed+ExceedConnLimit surfaces as *larkws.ServerError so the
// caller's reconnect loop keeps trying. Stale Feishu conns expire on
// the server side and free up slots, so giving up here would leave
// the channel disconnected until manual intervention.
func TestRunHandshakeExceedConnLimitIsTransient(t *testing.T) {
	t.Parallel()
	gw := newMockGateway(t)
	gw.handshakeStatus = larkws.AuthFailed
	gw.handshakeAuthCode = larkws.ExceedConnLimit
	gw.handshakeStatusMsg = "exceed connection limit"

	disp := &fakeDispatcher{}
	client := newTestClient(gw.domain())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := client.Run(ctx, disp)
	if err == nil {
		t.Fatal("expected handshake error, got nil")
	}
	var se *larkws.ServerError
	if !errors.As(err, &se) {
		t.Fatalf("expected *larkws.ServerError so reconnect retries, got %T (%v)", err, err)
	}
	if se.Code != larkws.AuthFailed {
		t.Errorf("ServerError code = %d, want %d", se.Code, larkws.AuthFailed)
	}
}

// TestRunHandshakeAuthFailedIsTerminal verifies that a real auth
// failure (no ExceedConnLimit subcode) surfaces as *larkws.ClientError
// so the caller stops retrying instead of hammering the API.
func TestRunHandshakeAuthFailedIsTerminal(t *testing.T) {
	t.Parallel()
	gw := newMockGateway(t)
	gw.handshakeStatus = larkws.AuthFailed
	gw.handshakeAuthCode = 0
	gw.handshakeStatusMsg = "bad credentials"

	disp := &fakeDispatcher{}
	client := newTestClient(gw.domain())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := client.Run(ctx, disp)
	if err == nil {
		t.Fatal("expected handshake error, got nil")
	}
	var ce *larkws.ClientError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *larkws.ClientError so reconnect stops, got %T (%v)", err, err)
	}
	if ce.Code != larkws.AuthFailed {
		t.Errorf("ClientError code = %d, want %d", ce.Code, larkws.AuthFailed)
	}
}

// TestRunEndpointFailure verifies that a non-OK endpoint response surfaces
// as a typed error.
func TestRunEndpointFailure(t *testing.T) {
	t.Parallel()
	gw := newMockGateway(t)
	gw.endpointErr.code = 99999
	gw.endpointErr.msg = "bad app"

	disp := &fakeDispatcher{}
	client := newTestClient(gw.domain())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := client.Run(ctx, disp)
	if err == nil {
		t.Fatal("expected endpoint error, got nil")
	}
	var ce *larkws.ClientError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *larkws.ClientError, got %T (%v)", err, err)
	}
}

// TestRunReassemblesFragmentedEvent ensures the fragment cache stitches
// multi-fragment payloads back together before dispatch.
func TestRunReassemblesFragmentedEvent(t *testing.T) {
	t.Parallel()
	gw := newMockGateway(t)
	full := `{"schema":"2.0","event":{"text":"hello world"}}`
	mid := len(full) / 2
	gw.upgradeHook = func(conn *websocket.Conn) {
		go func() {
			time.Sleep(10 * time.Millisecond)
			_ = gw.pushEventFragment(conn, "frag-1", 2, 0, []byte(full[:mid]))
			time.Sleep(10 * time.Millisecond)
			_ = gw.pushEventFragment(conn, "frag-1", 2, 1, []byte(full[mid:]))
		}()
	}

	disp := &fakeDispatcher{}
	client := newTestClient(gw.domain())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(ctx, disp) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(disp.seen()) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-runDone

	got := disp.seen()
	if len(got) != 1 {
		t.Fatalf("dispatcher saw %d events, want 1", len(got))
	}
	if string(got[0]) != full {
		t.Errorf("reassembled payload = %q, want %q", got[0], full)
	}
}

// TestRunSendsPing exercises the ping cadence: the server should observe a
// binary ping frame within roughly the configured interval.
func TestRunSendsPing(t *testing.T) {
	t.Parallel()
	gw := newMockGateway(t)

	pingSeen := make(chan struct{}, 1)
	gw.upgradeHook = func(conn *websocket.Conn) {
		go func() {
			for {
				mt, raw, err := conn.ReadMessage()
				if err != nil {
					return
				}
				if mt != websocket.BinaryMessage {
					continue
				}
				frame := &larkws.Frame{}
				if err := frame.Unmarshal(raw); err != nil {
					continue
				}
				headers := larkws.Headers(frame.Headers)
				if larkws.MessageType(headers.GetString(larkws.HeaderType)) == larkws.MessageTypePing {
					select {
					case pingSeen <- struct{}{}:
					default:
					}
					return
				}
			}
		}()
	}

	disp := &fakeDispatcher{}
	client := newTestClient(gw.domain(), func(c *Config) {
		c.PingInterval = 30 * time.Millisecond
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(ctx, disp) }()

	select {
	case <-pingSeen:
	case <-time.After(1 * time.Second):
		t.Fatal("did not observe ping frame within 1s")
	}
	cancel()
	<-runDone
}

// TestRunDispatcherErrorBecomesNon200 verifies the ack reflects dispatch
// failures so the platform can retry.
func TestRunDispatcherErrorBecomesNon200(t *testing.T) {
	t.Parallel()
	gw := newMockGateway(t)

	gw.upgradeHook = func(conn *websocket.Conn) {
		go func() {
			time.Sleep(10 * time.Millisecond)
			_ = gw.pushEvent(conn, "msg-err", `{"schema":"2.0","event":{}}`)
		}()
	}

	disp := &fakeDispatcher{
		respFn: func([]byte) (interface{}, error) {
			return nil, errors.New("dispatch boom")
		},
	}
	client := newTestClient(gw.domain())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(ctx, disp) }()

	conn := gw.waitForConn(t, 1, 2*time.Second)
	frame, err := gw.readResponseFrame(conn)
	if err != nil {
		t.Fatalf("read response frame: %v", err)
	}
	var ack larkws.Response
	if err := json.Unmarshal(frame.Payload, &ack); err != nil {
		t.Fatalf("decode ack payload: %v", err)
	}
	if ack.StatusCode != http.StatusInternalServerError {
		t.Errorf("ack status = %d, want %d", ack.StatusCode, http.StatusInternalServerError)
	}
	cancel()
	<-runDone
}

// TestEndpointURLConstruction is a sanity test for the gateway helper to
// keep upgrade routing aligned with the production handshake URL shape.
func TestEndpointURLConstruction(t *testing.T) {
	t.Parallel()
	gw := newMockGateway(t)
	body, _ := json.Marshal(map[string]string{"AppID": "a", "AppSecret": "b"})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, gw.domain()+larkws.GenEndpointUri, strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // G107: test request to httptest.Server
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	parsed := &larkws.EndpointResp{}
	if err := json.Unmarshal(raw, parsed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if parsed.Code != larkws.OK {
		t.Fatalf("got code=%d msg=%s", parsed.Code, parsed.Msg)
	}
	u, err := url.Parse(parsed.Data.Url)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	if u.Query().Get(larkws.ServiceID) == "" || u.Query().Get(larkws.DeviceID) == "" {
		t.Errorf("endpoint url missing service_id/device_id: %s", parsed.Data.Url)
	}
}

// TestBuildDialerInheritsDefaults verifies the dialer keeps
// http.ProxyFromEnvironment from websocket.DefaultDialer and does not mutate
// the package-level default.
func TestBuildDialerInheritsDefaults(t *testing.T) {
	t.Parallel()
	c := newTestClient("https://example.com")
	d := c.buildDialer()
	if d.Proxy == nil {
		t.Errorf("dialer.Proxy is nil; expected http.ProxyFromEnvironment")
	}
	if d.HandshakeTimeout != c.cfg.HandshakeTimeout {
		t.Errorf("dialer.HandshakeTimeout = %v, want %v", d.HandshakeTimeout, c.cfg.HandshakeTimeout)
	}
	if websocket.DefaultDialer.HandshakeTimeout == c.cfg.HandshakeTimeout {
		t.Errorf("buildDialer mutated websocket.DefaultDialer; must work on a copy")
	}
}

// TestRunHandshakeFailureDoesNotLeakConnections asserts that repeated
// rejected handshakes do not leave live TCP conns piling up on the server
// side, which is what would happen if a gorilla upgrade ever stopped
// closing the netConn for us.
func TestRunHandshakeFailureDoesNotLeakConnections(t *testing.T) {
	t.Parallel()
	var live int32
	gw := newMockGateway(t, func(srv *httptest.Server) {
		srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
			switch state {
			case http.StateNew:
				atomic.AddInt32(&live, 1)
			case http.StateClosed, http.StateHijacked:
				atomic.AddInt32(&live, -1)
			}
		}
	})
	gw.handshakeStatus = larkws.AuthFailed
	gw.handshakeAuthCode = larkws.ExceedConnLimit
	gw.handshakeStatusMsg = "exceed connection limit"

	disp := &fakeDispatcher{}
	const attempts = 30
	for i := 0; i < attempts; i++ {
		client := newTestClient(gw.domain())
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := client.Run(ctx, disp)
		cancel()
		if err == nil {
			t.Fatalf("attempt %d: expected handshake error, got nil", i)
		}
	}

	// Tolerance covers in-flight conns whose StateClosed callback has not
	// fired yet (typically 0-1); a real leak would be near `attempts`.
	const tolerance = int32(3)
	deadline := time.Now().Add(5 * time.Second)
	var last int32
	for time.Now().Before(deadline) {
		last = atomic.LoadInt32(&live)
		if last <= tolerance {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("after %d failed handshakes the test server still has %d live conns (tolerance=%d); resp.Body is leaking", attempts, last, tolerance)
}

// TestFragmentCacheReassembles is a focused unit test for the fragment
// cache, independent of the websocket plumbing.
func TestFragmentCacheReassembles(t *testing.T) {
	t.Parallel()
	c := newFragmentCache(5 * time.Second)
	defer c.stop()

	if got := c.add("k", 3, 0, []byte("foo")); got != nil {
		t.Errorf("expected nil while incomplete, got %q", got)
	}
	if got := c.add("k", 3, 2, []byte("baz")); got != nil {
		t.Errorf("expected nil while incomplete, got %q", got)
	}
	got := c.add("k", 3, 1, []byte("bar"))
	if string(got) != "foobarbaz" {
		t.Errorf("reassembled = %q, want foobarbaz", got)
	}
	// After completion the entry should be evicted, so the next add for
	// the same id behaves as a brand-new sequence.
	if got := c.add("k", 1, 0, []byte("solo")); string(got) != "solo" {
		t.Errorf("post-eviction add = %q, want solo", got)
	}
}

// TestRunDispatchesEventsConcurrently asserts a slow dispatcher does not
// stall the read loop: a second event must be ack'd while the first one is
// still inside dispatcher.Do.
func TestRunDispatchesEventsConcurrently(t *testing.T) {
	t.Parallel()
	gw := newMockGateway(t)
	// Disable client pings so the server only sees ACK frames. With
	// pings on, one arriving while slow dispatch is blocked would look
	// like the fast ack.
	client := newTestClient(gw.domain(), func(c *Config) {
		c.PingInterval = 1 * time.Hour
	})

	gw.upgradeHook = func(conn *websocket.Conn) {
		go func() {
			time.Sleep(10 * time.Millisecond)
			_ = gw.pushEvent(conn, "msg-slow", `{"schema":"2.0","event":{"slow":true}}`)
			_ = gw.pushEvent(conn, "msg-fast", `{"schema":"2.0","event":{"slow":false}}`)
		}()
	}

	released := make(chan struct{})
	slowEntered := make(chan struct{})
	var firstFlag atomic.Bool
	disp := &fakeDispatcher{
		respFn: func(payload []byte) (interface{}, error) {
			if strings.Contains(string(payload), `"slow":true`) {
				if firstFlag.CompareAndSwap(false, true) {
					close(slowEntered)
				}
				<-released
			}
			return nil, nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(ctx, disp) }()

	conn := gw.waitForConn(t, 1, 2*time.Second)
	<-slowEntered

	// While slow dispatch is still blocked, the fast event's ack must
	// already be readable.
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	frame, err := gw.readResponseFrame(conn)
	if err != nil {
		close(released)
		t.Fatalf("fast ack did not arrive while slow dispatch was blocked: %v", err)
	}
	if got := larkws.Headers(frame.Headers).GetString(larkws.HeaderMessageID); got != "msg-fast" {
		close(released)
		t.Fatalf("first ack message_id = %q, want msg-fast", got)
	}
	_ = conn.SetReadDeadline(time.Time{})

	close(released)
	frame, err = gw.readResponseFrame(conn)
	if err != nil {
		t.Fatalf("slow ack did not arrive after release: %v", err)
	}
	if got := larkws.Headers(frame.Headers).GetString(larkws.HeaderMessageID); got != "msg-slow" {
		t.Fatalf("second ack message_id = %q, want msg-slow", got)
	}

	cancel()
	if err := <-runDone; err != nil {
		t.Errorf("Run returned error on cancel: %v", err)
	}
}

// TestRunReadDeadlineFiresOnSilentServer verifies that a server which
// upgrades and then sends nothing trips ReadIdleTimeout instead of
// hanging on OS keepalive.
func TestRunReadDeadlineFiresOnSilentServer(t *testing.T) {
	t.Parallel()
	gw := newMockGateway(t)

	// No upgradeHook: the server accepts the upgrade and then goes
	// silent. PingInterval=1h keeps ping traffic out of the picture
	// so the read deadline is what's being tested.
	client := newTestClient(gw.domain(), func(c *Config) {
		c.PingInterval = 1 * time.Hour
		c.ReadIdleTimeout = 80 * time.Millisecond
	})

	disp := &fakeDispatcher{}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(ctx, disp) }()

	select {
	case err := <-runDone:
		if err == nil {
			t.Fatalf("Run returned nil; expected read deadline error")
		}
		if !strings.Contains(err.Error(), "ws read") {
			t.Fatalf("expected ws read error, got: %v", err)
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatalf("Run did not return within ReadIdleTimeout budget")
	}
}

// TestSpawnDispatchCapsConcurrency verifies that a blocking dispatcher
// never runs more than dispatchConcurrency goroutines at once, even
// when the server floods frames.
func TestSpawnDispatchCapsConcurrency(t *testing.T) {
	t.Parallel()
	gw := newMockGateway(t)

	var inflight, peak int64
	release := make(chan struct{})
	disp := &fakeDispatcher{
		respFn: func([]byte) (interface{}, error) {
			cur := atomic.AddInt64(&inflight, 1)
			for {
				old := atomic.LoadInt64(&peak)
				if cur <= old || atomic.CompareAndSwapInt64(&peak, old, cur) {
					break
				}
			}
			<-release
			atomic.AddInt64(&inflight, -1)
			return nil, nil
		},
	}

	const totalFrames = dispatchConcurrency * 4
	gw.upgradeHook = func(conn *websocket.Conn) {
		go func() {
			for i := 0; i < totalFrames; i++ {
				_ = gw.pushEvent(conn, fmt.Sprintf("m-%d", i), `{}`)
			}
		}()
	}

	client := newTestClient(gw.domain(), func(c *Config) {
		c.PingInterval = 1 * time.Hour
	})

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(ctx, disp) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&inflight) >= int64(dispatchConcurrency) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := atomic.LoadInt64(&inflight); got != int64(dispatchConcurrency) {
		close(release)
		cancel()
		<-runDone
		t.Fatalf("inflight=%d did not reach cap=%d", got, dispatchConcurrency)
	}

	// Give readLoop time to over-spawn if the cap were missing.
	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt64(&peak); got > int64(dispatchConcurrency) {
		close(release)
		cancel()
		<-runDone
		t.Fatalf("peak=%d exceeds cap=%d", got, dispatchConcurrency)
	}

	close(release)
	cancel()
	if err := <-runDone; err != nil {
		t.Errorf("Run returned error: %v", err)
	}
}

// TestRunWaitsForInFlightDispatch verifies that when ctx is cancelled
// mid-dispatch, Run waits for the goroutine to finish before returning.
// Otherwise a follow-up Connect would race the live handler.
func TestRunWaitsForInFlightDispatch(t *testing.T) {
	t.Parallel()
	gw := newMockGateway(t)

	started := make(chan struct{})
	finished := make(chan struct{})
	disp := &fakeDispatcher{
		respFn: func([]byte) (interface{}, error) {
			select {
			case <-started:
			default:
				close(started)
			}
			<-finished
			return nil, nil
		},
	}

	gw.upgradeHook = func(conn *websocket.Conn) {
		go func() {
			time.Sleep(10 * time.Millisecond)
			_ = gw.pushEvent(conn, "msg-1", `{"hello":"world"}`)
		}()
	}

	client := newTestClient(gw.domain(), func(c *Config) {
		c.PingInterval = 1 * time.Hour
	})

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(ctx, disp) }()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		cancel()
		close(finished)
		t.Fatalf("dispatcher was never invoked")
	}

	cancel()

	// Run should stay blocked while the dispatch goroutine is
	// still inside respFn.
	select {
	case err := <-runDone:
		close(finished)
		t.Fatalf("Run returned (err=%v) before dispatcher finished", err)
	case <-time.After(150 * time.Millisecond):
	}

	close(finished)

	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after dispatcher unblocked")
	}
}

// TestFetchEndpointHonorsTimeout verifies that a hung HTTP endpoint
// trips EndpointTimeout instead of stalling on the caller's longer
// ctx, so the reconnect loop keeps moving.
func TestFetchEndpointHonorsTimeout(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Hijack so the test owns the conn; otherwise srv.Close
		// blocks on idle keep-alive teardown after the client
		// times out.
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijacker", http.StatusInternalServerError)
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer func() { _ = conn.Close() }()
		// Block until the client closes its half once
		// EndpointTimeout fires. The 5s ceiling guards a hung test.
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 64)
		_, _ = conn.Read(buf)
	}))
	defer srv.Close()

	client := newTestClient(srv.URL, func(c *Config) {
		c.EndpointTimeout = 100 * time.Millisecond
	})

	disp := &fakeDispatcher{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(ctx, disp) }()

	select {
	case err := <-runDone:
		elapsed := time.Since(start)
		if err == nil {
			t.Fatalf("expected error from EndpointTimeout, got nil")
		}
		if elapsed > 1*time.Second {
			t.Fatalf("Run took %v, expected ~EndpointTimeout=100ms", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Run did not return within EndpointTimeout budget")
	}
}

// TestRunReArmsPingTickerOnServerCadenceChange verifies that pingLoop
// picks up cadence updates from the server's pong. Without ticker
// re-arming, the gateway would see ~10 pings per 2s at the original
// 200ms cadence; once re-armed to the server's 1s cadence it should
// see at most a handful.
func TestRunReArmsPingTickerOnServerCadenceChange(t *testing.T) {
	t.Parallel()
	gw := newMockGateway(t)

	var pingCount atomic.Int64
	pongSent := make(chan struct{})
	gw.upgradeHook = func(conn *websocket.Conn) {
		go func() {
			var pongOnce sync.Once
			for {
				mt, raw, err := conn.ReadMessage()
				if err != nil {
					return
				}
				if mt != websocket.BinaryMessage {
					continue
				}
				frame := &larkws.Frame{}
				if err := frame.Unmarshal(raw); err != nil {
					continue
				}
				if larkws.MessageType(larkws.Headers(frame.Headers).GetString(larkws.HeaderType)) != larkws.MessageTypePing {
					continue
				}
				pingCount.Add(1)
				pongOnce.Do(func() {
					_ = writeServerPong(conn, &larkws.ClientConfig{PingInterval: 1})
					close(pongSent)
				})
			}
		}()
	}

	disp := &fakeDispatcher{}
	client := newTestClient(gw.domain(), func(c *Config) {
		c.PingInterval = 200 * time.Millisecond
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(ctx, disp) }()

	select {
	case <-pongSent:
	case <-time.After(2 * time.Second):
		cancel()
		<-runDone
		t.Fatal("server never received the first ping within 2s")
	}

	// Wait long enough to expose a stuck ticker: 2s at the old 200ms
	// cadence would be ~10 pings; re-armed to 1s it should be ~3
	// (the original ping plus one or two at the new cadence).
	time.Sleep(2 * time.Second)
	cancel()
	<-runDone

	if got := pingCount.Load(); got > 5 {
		t.Errorf("ping count = %d, expected <= 5 after server cadence change to 1s", got)
	}
}

// TestSpawnDispatchRecoversPanic verifies that a panicking dispatcher
// does not crash the process: the bad frame is dropped (no ack) and a
// subsequent good frame still flows through.
func TestSpawnDispatchRecoversPanic(t *testing.T) {
	t.Parallel()
	gw := newMockGateway(t)

	var dispatchCount atomic.Int64
	disp := &fakeDispatcher{
		respFn: func([]byte) (interface{}, error) {
			if dispatchCount.Add(1) == 1 {
				panic("synthetic dispatcher boom")
			}
			return nil, nil
		},
	}

	goodAck := make(chan string, 1)
	gw.upgradeHook = func(conn *websocket.Conn) {
		go func() {
			time.Sleep(10 * time.Millisecond)
			_ = gw.pushEvent(conn, "msg-bad", `{}`)
			time.Sleep(50 * time.Millisecond)
			_ = gw.pushEvent(conn, "msg-good", `{}`)

			for i := 0; i < 3; i++ {
				_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
				frame, err := gw.readResponseFrame(conn)
				if err != nil {
					return
				}
				id := larkws.Headers(frame.Headers).GetString(larkws.HeaderMessageID)
				if id == "msg-good" {
					select {
					case goodAck <- id:
					default:
					}
					return
				}
			}
		}()
	}

	client := newTestClient(gw.domain(), func(c *Config) {
		c.PingInterval = 1 * time.Hour
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(ctx, disp) }()

	select {
	case <-goodAck:
	case <-time.After(3 * time.Second):
		cancel()
		<-runDone
		t.Fatal("good ack never arrived; recover may have failed to keep the session alive")
	}

	cancel()
	<-runDone

	if got := dispatchCount.Load(); got < 2 {
		t.Errorf("dispatchCount = %d, want >= 2 (both frames reached the dispatcher)", got)
	}
}

// TestReadDeadlineFollowsPingInterval verifies that the per-read
// deadline tracks the active pingInterval so a server-driven cadence
// change keeps things in step. A user-supplied ReadIdleTimeout still
// wins over the derived value.
func TestReadDeadlineFollowsPingInterval(t *testing.T) {
	t.Parallel()
	s := &session{}

	cases := []struct {
		ping time.Duration
		want time.Duration
	}{
		{2 * time.Second, 2*time.Second*2 + 5*time.Second},
		{500 * time.Millisecond, 500*time.Millisecond*2 + 5*time.Second},
		{10 * time.Second, 10*time.Second*2 + 5*time.Second},
	}
	for _, tc := range cases {
		s.pingIntervalNs.Store(int64(tc.ping))
		if got := s.readDeadline(); got != tc.want {
			t.Errorf("ping=%v: readDeadline=%v, want %v", tc.ping, got, tc.want)
		}
	}

	s.readIdleTimeout = 99 * time.Second
	s.pingIntervalNs.Store(int64(2 * time.Second))
	if got := s.readDeadline(); got != 99*time.Second {
		t.Errorf("override: readDeadline=%v, want 99s", got)
	}
}

// TestFragmentCacheTTLEviction confirms incomplete fragments are not
// retained forever.
func TestFragmentCacheTTLEviction(t *testing.T) {
	t.Parallel()
	c := newFragmentCache(20 * time.Millisecond)
	defer c.stop()
	if got := c.add("k", 2, 0, []byte("foo")); got != nil {
		t.Errorf("expected nil while incomplete, got %q", got)
	}
	// Wait for the janitor to evict.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		_, exists := c.items["k"]
		c.mu.Unlock()
		if !exists {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("expired fragment was not evicted")
}
