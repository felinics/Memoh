package runtime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/grpc/connectivity"
)

type runtimeTransportState struct {
	mu      sync.Mutex
	state   connectivity.State
	changed chan struct{}
}

func newRuntimeTransportState(state connectivity.State) *runtimeTransportState {
	return &runtimeTransportState{state: state, changed: make(chan struct{})}
}

func (s *runtimeTransportState) GetState() connectivity.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *runtimeTransportState) WaitForStateChange(ctx context.Context, source connectivity.State) bool {
	s.mu.Lock()
	if s.state != source {
		s.mu.Unlock()
		return true
	}
	changed := s.changed
	s.mu.Unlock()
	select {
	case <-changed:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *runtimeTransportState) transition(state connectivity.State) {
	s.mu.Lock()
	s.state = state
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
}

func TestMonitorRuntimeTransportReportsFirstReadyDeparture(t *testing.T) {
	transport := newRuntimeTransportState(connectivity.Ready)
	lost := make(chan connectivity.State, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go monitorRuntimeTransport(ctx, transport, lost, nil)

	transport.transition(connectivity.TransientFailure)
	select {
	case state := <-lost:
		if state != connectivity.TransientFailure {
			t.Fatalf("lost state = %s, want %s", state, connectivity.TransientFailure)
		}
	case <-time.After(time.Second):
		t.Fatal("transport departure was not reported")
	}
}

func TestMonitorRuntimeTransportStopsOnContextCancellation(t *testing.T) {
	transport := newRuntimeTransportState(connectivity.Ready)
	lost := make(chan connectivity.State, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		monitorRuntimeTransport(ctx, transport, lost, nil)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("transport monitor did not stop after cancellation")
	}
	select {
	case state := <-lost:
		t.Fatalf("canceled monitor reported state %s", state)
	default:
	}
}

func TestRuntimeWebSocketNetConnRejectsOversizedMessage(t *testing.T) {
	readErr := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			readErr <- err
			return
		}
		defer conn.CloseNow() //nolint:errcheck // test cleanup
		stream := runtimeWebSocketNetConn(r.Context(), conn)
		buffer := make([]byte, 32*1024)
		for {
			if _, err := stream.Read(buffer); err != nil {
				readErr <- err
				return
			}
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, response, err := websocket.Dial(ctx, "ws"+server.URL[len("http"):], nil)
	if response != nil && response.Body != nil {
		defer func() { _ = response.Body.Close() }()
	}
	if err != nil {
		t.Fatalf("dial WebSocket: %v", err)
	}
	defer conn.CloseNow() //nolint:errcheck // test cleanup
	if err := conn.Write(ctx, websocket.MessageBinary, make([]byte, runtimeMessageLimit+1)); err != nil {
		t.Fatalf("write oversized message: %v", err)
	}
	select {
	case err := <-readErr:
		if !errors.Is(err, websocket.ErrMessageTooBig) {
			t.Fatalf("oversized read error = %v, want ErrMessageTooBig", err)
		}
	case <-ctx.Done():
		t.Fatalf("oversized message was not rejected: %v", ctx.Err())
	}
}
