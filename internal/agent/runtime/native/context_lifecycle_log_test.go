package native

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
)

// lifecycleRecordingHandler is a self-contained slog.Handler that records
// every emitted record, mirroring the recordingHandler pattern in
// cmd/agent/module_test.go.
type lifecycleRecordingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (*lifecycleRecordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *lifecycleRecordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}

func (h *lifecycleRecordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *lifecycleRecordingHandler) WithGroup(string) slog.Handler { return h }

func (h *lifecycleRecordingHandler) countMessage(msg string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	count := 0
	for _, r := range h.records {
		if r.Message == msg {
			count++
		}
	}
	return count
}

func TestAgentGenerateLogsContextLifecycleOnceOnHardError(t *testing.T) {
	t.Parallel()

	modelProvider := &atomicMockProvider{
		handler: func(_ int, _ sdk.GenerateParams) (*sdk.GenerateResult, error) {
			return nil, errors.New("boom: hard failure")
		},
	}
	handler := &lifecycleRecordingHandler{}
	a := New(Deps{Logger: slog.New(handler)})

	_, err := a.Generate(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:         []sdk.Message{sdk.UserMessage("hard error")},
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
	})
	if err == nil {
		t.Fatal("expected hard error from Generate")
	}
	if got := handler.countMessage("context lifecycle"); got != 1 {
		t.Fatalf("context lifecycle log count = %d, want 1", got)
	}
}

func TestAgentGenerateLogsContextLifecycleOnceOnHappyPath(t *testing.T) {
	t.Parallel()

	modelProvider := &atomicMockProvider{
		handler: func(_ int, _ sdk.GenerateParams) (*sdk.GenerateResult, error) {
			return &sdk.GenerateResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
		},
	}
	handler := &lifecycleRecordingHandler{}
	a := New(Deps{Logger: slog.New(handler)})

	_, err := a.Generate(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:         []sdk.Message{sdk.UserMessage("hello")},
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if got := handler.countMessage("context lifecycle"); got != 1 {
		t.Fatalf("context lifecycle log count = %d, want 1 (no double-log regression)", got)
	}
}

func TestAgentStreamLogsContextLifecycleOnceOnNonRetryableStreamStartError(t *testing.T) {
	t.Parallel()

	modelProvider := &atomicMockProvider{
		stream: func(_ context.Context, _ sdk.GenerateParams) (*sdk.StreamResult, error) {
			return nil, errors.New("boom: non-retryable")
		},
	}
	handler := &lifecycleRecordingHandler{}
	a := New(Deps{Logger: slog.New(handler)})

	var sawError bool
	for event := range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:         []sdk.Message{sdk.UserMessage("hello")},
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
	}) {
		if event.Type == EventError {
			sawError = true
		}
	}
	if !sawError {
		t.Fatal("expected EventError from non-retryable stream start failure")
	}
	if got := handler.countMessage("context lifecycle"); got != 1 {
		t.Fatalf("context lifecycle log count = %d, want 1", got)
	}
}

func TestAgentStreamLogsContextLifecycleOnceOnBuildConfigError(t *testing.T) {
	t.Parallel()

	handler := &lifecycleRecordingHandler{}
	a := New(Deps{Logger: slog.New(handler)})

	var sawError bool
	for event := range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model"},
		Messages:         []sdk.Message{sdk.UserMessage("hello")},
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
	}) {
		if event.Type == EventError {
			sawError = true
		}
	}
	if !sawError {
		t.Fatal("expected EventError from a model with no provider")
	}
	if got := handler.countMessage("context lifecycle"); got != 1 {
		t.Fatalf("context lifecycle log count = %d, want 1", got)
	}
}

func TestAgentStreamLogsContextLifecycleOnceOnHappyPath(t *testing.T) {
	t.Parallel()

	modelProvider := &atomicMockProvider{
		handler: func(_ int, _ sdk.GenerateParams) (*sdk.GenerateResult, error) {
			return &sdk.GenerateResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
		},
	}
	handler := &lifecycleRecordingHandler{}
	a := New(Deps{Logger: slog.New(handler)})

	for event := range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:         []sdk.Message{sdk.UserMessage("hello")},
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
	}) {
		_ = event
	}
	if got := handler.countMessage("context lifecycle"); got != 1 {
		t.Fatalf("context lifecycle log count = %d, want 1 (no double-log regression)", got)
	}
}
