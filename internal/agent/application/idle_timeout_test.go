package application

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/apperror"
)

func TestIdleTimeoutPublishesStableResponseTimeoutCause(t *testing.T) {
	ctx, idle := withIdleTimeout(context.Background(), 10*time.Millisecond)
	defer idle.Stop()

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("idle timeout did not cancel context")
	}

	cause := context.Cause(ctx)
	if got := apperror.CodeOf(cause); got != apperror.CodeAgentResponseTimeout {
		t.Fatalf("timeout code = %q, want %q", got, apperror.CodeAgentResponseTimeout)
	}
	if private := apperror.CauseOf(cause); !errors.Is(private, context.DeadlineExceeded) {
		t.Fatalf("private cause = %v, want context deadline exceeded", private)
	}
}

func TestDefaultIdleTimeoutMatchesModelRequestWindow(t *testing.T) {
	if defaultIdleTimeout != 5*time.Minute {
		t.Fatalf("default idle timeout = %v, want 5m", defaultIdleTimeout)
	}
	_, idle := withIdleTimeout(context.Background())
	defer idle.Stop()
	if got := idle.currentTimeout(); got != 5*time.Minute {
		t.Fatalf("initial idle window = %v, want 5m", got)
	}
}

func TestIdleTimeoutCapsInitialWindow(t *testing.T) {
	ctx, idle := withIdleTimeout(context.Background(), time.Second, 20*time.Millisecond)
	defer idle.Stop()
	select {
	case <-ctx.Done():
		if !idle.DidFire() {
			t.Fatal("idle window ended without firing")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("initial idle window ignored the configured maximum")
	}
}

func TestServiceHTTPClientsHaveSeparateTimeoutOwnership(t *testing.T) {
	service := NewService(slog.New(slog.DiscardHandler), nil, nil, nil, nil, nil, nil, time.UTC, time.Minute)
	if service.streamHTTPClient.Timeout != 0 {
		t.Fatalf("stream client timeout = %v, want no whole-request deadline", service.streamHTTPClient.Timeout)
	}
	streamTransport, ok := service.streamHTTPClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("stream transport = %T, want *http.Transport", service.streamHTTPClient.Transport)
	}
	if streamTransport.ResponseHeaderTimeout != 0 {
		t.Fatalf("stream response header timeout = %v, want application watchdog ownership", streamTransport.ResponseHeaderTimeout)
	}

	if service.nonStreamingHTTPClient.Timeout != 10*time.Minute {
		t.Fatalf("non-streaming client timeout = %v, want 10m", service.nonStreamingHTTPClient.Timeout)
	}
	nonStreamingTransport, ok := service.nonStreamingHTTPClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("non-streaming transport = %T, want *http.Transport", service.nonStreamingHTTPClient.Transport)
	}
	if nonStreamingTransport == streamTransport {
		t.Fatal("streaming and non-streaming clients share a mutable transport")
	}
	if nonStreamingTransport.ResponseHeaderTimeout != 30*time.Second {
		t.Fatalf("non-streaming response header timeout = %v, want 30s", nonStreamingTransport.ResponseHeaderTimeout)
	}
}

func TestScaleIdleTimeoutForEffort(t *testing.T) {
	t.Parallel()

	base := 90 * time.Second
	cases := []struct {
		effort string
		want   time.Duration
	}{
		{"", base},
		{"low", base},
		{"medium", 3 * time.Minute},
		{"high", 6 * time.Minute},
		{"xhigh", 9 * time.Minute},
		{"max", 12 * time.Minute},
	}
	for _, tc := range cases {
		if got := scaleIdleTimeoutForEffort(base, tc.effort); got != tc.want {
			t.Fatalf("effort %q = %v, want %v", tc.effort, got, tc.want)
		}
	}
}

func TestIdleTimeoutPhases(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, idle := withIdleTimeout(t.Context(), time.Minute)
		defer idle.Stop()
		idle.Observe(native.StreamEvent{Type: native.EventToolCallStart, ToolCallID: "tool"})
		time.Sleep(2 * time.Minute)
		if ctx.Err() != nil {
			t.Fatal("model deadline interrupted a tool")
		}
		idle.Observe(native.StreamEvent{Type: native.EventToolCallEnd, ToolCallID: "tool"})
		time.Sleep(time.Minute)
		synctest.Wait()
		if !idle.DidFire() {
			t.Fatal("completed tool extended the next model window")
		}
	})
}

func TestIdleTimeoutWaitsForAllDecisions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, idle := withIdleTimeout(t.Context(), time.Minute)
		defer idle.Stop()
		for _, id := range []string{"a", "b"} {
			idle.Observe(native.StreamEvent{Type: native.EventToolCallStart, ToolCallID: id})
			idle.Observe(native.StreamEvent{Type: native.EventToolApprovalRequest, ToolCallID: id, ApprovalID: id, Status: "pending"})
		}
		time.Sleep(9 * time.Minute)
		idle.Observe(native.StreamEvent{Type: native.EventToolApprovalRequest, ToolCallID: "a", ApprovalID: "a", Status: "approved"})
		idle.Observe(native.StreamEvent{Type: native.EventToolCallEnd, ToolCallID: "a"})
		time.Sleep(2 * time.Minute)
		if ctx.Err() != nil {
			t.Fatal("remaining decision lost its wait")
		}
		idle.Observe(native.StreamEvent{Type: native.EventToolApprovalRequest, ToolCallID: "b", ApprovalID: "b", Status: "approved"})
		idle.Observe(native.StreamEvent{Type: native.EventToolCallEnd, ToolCallID: "b"})
		time.Sleep(time.Minute)
		synctest.Wait()
		if !idle.DidFire() {
			t.Fatal("model watchdog did not resume")
		}
	})
}

func TestIdleTimeoutParallelToolStillExpiresDuringDecision(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, idle := withIdleTimeout(t.Context())
		defer idle.Stop()
		idle.Observe(native.StreamEvent{Type: native.EventToolCallStart, ToolCallID: "waiting"})
		idle.Observe(native.StreamEvent{Type: native.EventUserInputRequest, ToolCallID: "waiting", UserInputID: "q", Status: "pending"})
		idle.Observe(native.StreamEvent{Type: native.EventToolCallStart, ToolCallID: "stalled"})
		time.Sleep(15 * time.Minute)
		synctest.Wait()
		if apperror.CodeOf(context.Cause(ctx)) != apperror.CodeAgentToolTimeout {
			t.Fatalf("cause = %v", context.Cause(ctx))
		}
	})
}

func TestIdleTimeoutStopCannotBeRearmed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, idle := withIdleTimeout(t.Context(), time.Second)
		idle.Stop()
		idle.Reset()
		time.Sleep(2 * time.Second)
		if ctx.Err() != nil {
			t.Fatal("stopped timer fired")
		}
	})
}
