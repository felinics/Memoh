package application

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"testing"
	"time"

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

func TestIdleTimeoutToolCallRearmsCurrentWindow(t *testing.T) {
	ctx, idle := withIdleTimeout(context.Background(), 80*time.Millisecond)
	defer idle.Stop()

	time.Sleep(50 * time.Millisecond)
	idle.RecordToolCall()

	select {
	case <-ctx.Done():
		t.Fatal("tool call did not rearm the current idle window")
	case <-time.After(50 * time.Millisecond):
	}
}
