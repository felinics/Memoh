package application

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/memohai/memoh/internal/apperror"
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

func TestStreamingHTTPClientDelegatesSilenceToApplicationWatchdog(t *testing.T) {
	service := NewService(slog.New(slog.DiscardHandler), nil, nil, nil, nil, nil, nil, time.UTC, time.Minute)
	if service.streamHTTPClient.Timeout != 0 {
		t.Fatalf("stream client timeout = %v, want no whole-request deadline", service.streamHTTPClient.Timeout)
	}
	transport, ok := service.streamHTTPClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("stream transport = %T, want *http.Transport", service.streamHTTPClient.Transport)
	}
	if transport.ResponseHeaderTimeout != 0 {
		t.Fatalf("response header timeout = %v, want application watchdog ownership", transport.ResponseHeaderTimeout)
	}
	if service.compactionHTTPClient.Timeout != 10*time.Minute {
		t.Fatalf("compaction client timeout = %v, want bounded non-streaming request", service.compactionHTTPClient.Timeout)
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
