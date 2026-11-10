package handlers

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

// fakeTerminalStream is a PTY that prints nothing, or, with flood set, prints
// without pause until it is closed.
type fakeTerminalStream struct {
	flood     bool
	closeOnce sync.Once
	closed    chan struct{}
}

func newFakeTerminalStream(flood bool) *fakeTerminalStream {
	return &fakeTerminalStream{flood: flood, closed: make(chan struct{})}
}

var floodOutput = bytes.Repeat([]byte("x"), 1<<20)

func (s *fakeTerminalStream) Recv() (*pb.ExecOutput, error) {
	if s.flood {
		select {
		case <-s.closed:
			return nil, io.EOF
		default:
			return &pb.ExecOutput{Stream: pb.ExecOutput_STDOUT, Data: floodOutput}, nil
		}
	}
	<-s.closed
	return nil, io.EOF
}

func (*fakeTerminalStream) SendStdin([]byte) error { return nil }

func (*fakeTerminalStream) Resize(uint32, uint32) error { return nil }

func (s *fakeTerminalStream) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

// openTestTerminal serves one terminal session and returns its client, and a
// channel closed when serveTerminal returns.
func openTestTerminal(t *testing.T, handler *ContainerdHandler, stream terminalStream) (*websocket.Conn, <-chan struct{}) {
	t.Helper()
	served := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close() }()
		handler.serveTerminal(r.Context(), conn, stream, "bot-1")
		close(served)
	}))
	t.Cleanup(srv.Close)
	client, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	t.Cleanup(func() { _ = client.Close() })
	return client, served
}

func terminalTestHandler(idle time.Duration) *ContainerdHandler {
	return &ContainerdHandler{
		logger:              slog.New(slog.DiscardHandler),
		wsHeartbeat:         testWSHeartbeat,
		terminalIdleTimeout: idle,
	}
}

// A peer that vanished without closing answers no pings. Without a read
// deadline the session, and the shell behind it, would wait for the idle
// timeout.
func TestTerminalWSDisconnectsASilentClient(t *testing.T) {
	t.Parallel()
	client, served := openTestTerminal(t, terminalTestHandler(time.Hour), newFakeTerminalStream(false))
	client.SetPingHandler(func(string) error { return nil })
	go func() {
		for {
			if _, _, err := client.ReadMessage(); err != nil {
				return
			}
		}
	}()

	start := time.Now()
	select {
	case <-served:
	case <-time.After(20 * testWSHeartbeat.readTimeout):
		t.Fatal("the server kept a silent client connected")
	}
	if elapsed := time.Since(start); elapsed < testWSHeartbeat.readTimeout/2 {
		t.Errorf("disconnected after %v, long before the %v read timeout", elapsed, testWSHeartbeat.readTimeout)
	}
}

// An idle terminal sends heartbeats, but answering them is not activity:
// the session must still close at its idle timeout.
func TestTerminalWSHeartbeatDoesNotExtendIdleSession(t *testing.T) {
	t.Parallel()
	const idle = 600 * time.Millisecond
	const deadline = 10 * idle
	handler := terminalTestHandler(idle)
	// Only the idle timer should end this test. Peer liveness has separate
	// coverage; keep its deadlines beyond the client's failure deadline.
	handler.wsHeartbeat.readTimeout = 2 * deadline
	handler.wsHeartbeat.writeTimeout = 2 * deadline
	client, _ := openTestTerminal(t, handler, newFakeTerminalStream(false))
	pinged := false
	defaultPing := client.PingHandler()
	client.SetPingHandler(func(data string) error {
		pinged = true
		return defaultPing(data)
	})

	if err := client.SetReadDeadline(time.Now().Add(deadline)); err != nil {
		t.Fatal(err)
	}
	_, _, err := client.ReadMessage()
	var closed *websocket.CloseError
	if !errors.As(err, &closed) || closed.Code != websocket.CloseGoingAway || closed.Text != "idle timeout" {
		t.Fatalf("read ended with %v, want the idle timeout's going-away close", err)
	}
	if !pinged {
		t.Fatal("the terminal closed without sending a heartbeat")
	}
}

// Output can outpace a reader, and a peer that stops reading fills the socket
// buffer. The write has to give up, or the handler waits on it forever.
func TestTerminalWSGivesUpOnAPeerThatStoppedReading(t *testing.T) {
	t.Parallel()
	_, served := openTestTerminal(t, terminalTestHandler(time.Hour), newFakeTerminalStream(true))

	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("the session outlived a write the peer never read")
	}
}
