package application

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func recordTurnSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	return recorder
}

func turnSpan(t *testing.T, recorder *tracetest.SpanRecorder) sdktrace.ReadOnlySpan {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, span := range recorder.Ended() {
			if span.Name() == "agent.turn" {
				return span
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("no agent.turn span ended within 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func spanAttr(span sdktrace.ReadOnlySpan, key string) attribute.Value {
	for _, kv := range span.Attributes() {
		if string(kv.Key) == key {
			return kv.Value
		}
	}
	return attribute.Value{}
}

func drainChans(t *testing.T, chunks <-chan StreamChunk, errs <-chan error) error {
	t.Helper()
	var last error
	timeout := time.After(5 * time.Second)
	for chunks != nil || errs != nil {
		select {
		case _, ok := <-chunks:
			if !ok {
				chunks = nil
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			last = err
		case <-timeout:
			t.Fatal("stream did not finish within 5s")
		}
	}
	return last
}

// The span covers the turn, not the moment it is set up. A span closed
// before the work runs would report microseconds for a turn that took a
// minute — worse than no span, because it looks like an answer.
//
// The assertion is made while the turn is provably still running, by holding
// it inside a preflight hook. Checking "has it ended yet" straight after the
// call would only be a race with the goroutine's scheduling.
func TestTurnSpanCoversTheTurnNotItsSetup(t *testing.T) {
	recorder := recordTurnSpans(t)
	service := newTracingTestService()

	inTurn := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_, _ = service.streamChatWSResultWithHooks(context.Background(),
			ChatRequest{BotID: "b", ThreadID: "s"}, nil, nil,
			func(context.Context) error {
				close(inTurn)
				<-release
				return nil
			}, nil)
	}()

	select {
	case <-inTurn:
	case <-time.After(5 * time.Second):
		t.Fatal("the turn never reached the preflight hook")
	}
	for _, span := range recorder.Ended() {
		if span.Name() == "agent.turn" {
			t.Fatal("the turn span ended while the turn was still running")
		}
	}
	close(release)

	span := turnSpan(t, recorder)
	if got := spanAttr(span, "agent.bot_id").AsString(); got != "b" {
		t.Errorf("agent.bot_id = %q, want b", got)
	}
	if got := spanAttr(span, "agent.thread_id").AsString(); got != "s" {
		t.Errorf("agent.thread_id = %q, want s", got)
	}
	if got := span.SpanKind(); got != trace.SpanKindInternal {
		t.Errorf("span kind = %v, want internal", got)
	}
}

// A turn that failed is marked; a turn someone stopped is not. Recording
// cancellations as errors would make the error rate track how often users
// press stop.
func TestTurnSpanSeparatesFailureFromCancellation(t *testing.T) {
	t.Run("errored", func(t *testing.T) {
		recorder := recordTurnSpans(t)
		service := newTracingTestService()

		chunks, errs := service.StreamChat(context.Background(), ChatRequest{BotID: "b", ThreadID: "s"})
		if err := drainChans(t, chunks, errs); err == nil {
			t.Fatal("expected this harness to fail the turn")
		}

		span := turnSpan(t, recorder)
		if got := spanAttr(span, "agent.turn.outcome").AsString(); got != "errored" {
			t.Errorf("outcome = %q, want errored", got)
		}
		if span.Status().Code != codes.Error {
			t.Error("a failed turn did not mark the span as an error")
		}
	})

	t.Run("aborted", func(t *testing.T) {
		recorder := recordTurnSpans(t)
		service := newTracingTestService()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		chunks, errs := service.StreamChat(ctx, ChatRequest{BotID: "b", ThreadID: "s"})
		// Whatever the turn reported, a cancelled context makes it aborted.
		_ = drainChans(t, chunks, errs)

		span := turnSpan(t, recorder)
		if got := spanAttr(span, "agent.turn.outcome").AsString(); got != "aborted" {
			t.Errorf("outcome = %q, want aborted", got)
		}
		if span.Status().Code == codes.Error {
			t.Error("a stopped turn was recorded as a failure")
		}
	})
}

// The web UI's WebSocket never goes through the turn port, so a span placed
// only on StartTurn would be missing for the entry point most turns arrive
// through — and nothing would say so; the traces would simply have no turn in
// them. This is the second entry point, asserted separately for that reason.
func TestTurnSpanCoversTheWebSocketEntryPoint(t *testing.T) {
	recorder := recordTurnSpans(t)
	service := newTracingTestService()

	if _, err := service.streamChatWSResultWithHooks(context.Background(),
		ChatRequest{BotID: "b", ThreadID: "s"}, nil, nil, nil, nil); err == nil {
		t.Fatal("expected this harness to fail the turn")
	}

	span := turnSpan(t, recorder)
	if got := spanAttr(span, "agent.bot_id").AsString(); got != "b" {
		t.Errorf("agent.bot_id = %q, want b", got)
	}
	if span.Status().Code != codes.Error {
		t.Error("a turn that failed was not marked as an error")
	}
}

// newTracingTestService is newTurnTestService plus a logger. The harness
// leaves it nil because the paths it was written for never log; these tests
// drive StreamChat, which does.
func newTracingTestService() *Service {
	service := newTurnTestService(&fakeRunner{})
	service.logger = slog.New(slog.DiscardHandler)
	return service
}
