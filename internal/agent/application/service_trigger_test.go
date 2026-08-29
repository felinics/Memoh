package application

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	chatview "github.com/felinics/memoh/internal/agent/view"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/models"
)

type silentTriggerProvider struct {
	mu       sync.Mutex
	attempts int
	started  chan struct{}
	once     sync.Once
}

func (*silentTriggerProvider) Name() string { return "silent-trigger" }

func (*silentTriggerProvider) ListModels(context.Context) ([]sdk.Model, error) { return nil, nil }

func (*silentTriggerProvider) Test(context.Context) *sdk.ProviderTestResult {
	return &sdk.ProviderTestResult{Status: sdk.ProviderStatusOK}
}

func (*silentTriggerProvider) TestModel(context.Context, string) (*sdk.ModelTestResult, error) {
	return &sdk.ModelTestResult{Supported: true}, nil
}

func (*silentTriggerProvider) DoGenerate(context.Context, sdk.GenerateParams) (*sdk.GenerateResult, error) {
	return nil, errors.New("unexpected non-streaming call")
}

func (p *silentTriggerProvider) DoStream(ctx context.Context, _ sdk.GenerateParams) (*sdk.StreamResult, error) {
	p.mu.Lock()
	p.attempts++
	p.mu.Unlock()
	p.once.Do(func() { close(p.started) })

	stream := make(chan sdk.StreamPart)
	go func() {
		<-ctx.Done()
		close(stream)
	}()
	return &sdk.StreamResult{Stream: stream}, nil
}

func (p *silentTriggerProvider) attemptCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.attempts
}

// recordingTurnEventPublisher stands in for the session-runtime projection
// sink: it records every published event and can be armed to refuse the Nth
// publish, which is how fence rejections surface to the consumer.
type recordingTurnEventPublisher struct {
	mu     sync.Mutex
	events []native.StreamEvent
	calls  int
	errAt  int // 1-based call index to fail; 0 means never fail
	err    error
}

func (p *recordingTurnEventPublisher) publish(_ context.Context, _ sessionruntime.RunHandle, event native.StreamEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.errAt > 0 && p.calls == p.errAt {
		return p.err
	}
	p.events = append(p.events, event)
	return nil
}

func (p *recordingTurnEventPublisher) published() []native.StreamEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]native.StreamEvent(nil), p.events...)
}

func newTriggerStreamService(messages *recordingMessageService, pub *recordingTurnEventPublisher) *Service {
	svc := &Service{
		logger:         slog.New(slog.DiscardHandler),
		messageService: messages,
	}
	if pub != nil {
		svc.publishTurnEvent = pub.publish
	}
	return svc
}

func scheduleTerminalEvent(t *testing.T, assistantText string) native.StreamEvent {
	t.Helper()
	messagesJSON, err := json.Marshal([]sdk.Message{sdk.AssistantMessage(assistantText)})
	if err != nil {
		t.Fatalf("marshal terminal messages: %v", err)
	}
	usageJSON, err := json.Marshal(sdk.Usage{InputTokens: 12, OutputTokens: 3})
	if err != nil {
		t.Fatalf("marshal usage: %v", err)
	}
	return native.StreamEvent{Type: native.EventAgentEnd, Messages: messagesJSON, Usage: usageJSON}
}

func triggerStreamRequest() ChatRequest {
	return ChatRequest{BotID: "bot-1", ChatID: "bot-1", ThreadID: "session-1", Query: "run scheduled task"}
}

func TestTriggeredNativeStreamTimesOutSilentProviderWithoutRetry(t *testing.T) {
	provider := &silentTriggerProvider{started: make(chan struct{})}
	logger := slog.New(slog.DiscardHandler)
	svc := &Service{
		agent:             native.New(native.Deps{Logger: logger}),
		logger:            logger,
		messageService:    &recordingMessageService{},
		streamIdleTimeout: 100 * time.Millisecond,
	}
	cfg := triggerResolvedRunConfig(provider, "run scheduled task", time.Now(), sessionmode.Schedule)
	cfg = svc.prepareRunConfig(context.Background(), cfg)
	rc := resolvedContext{
		runConfig: cfg,
		model:     models.GetResponse{ID: "model-1"},
	}

	done := make(chan error, 1)
	go func() {
		_, err := svc.runTriggeredNativeStream(
			context.Background(),
			cfg,
			triggerStreamRequest(),
			rc,
			sessionruntime.RunHandle{RunID: "run-1", TurnID: "turn-1"},
			nil,
			nil,
		)
		done <- err
	}()

	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("silent provider was not started")
	}

	select {
	case err := <-done:
		if got := apperror.CodeOf(err); got != apperror.CodeAgentResponseTimeout {
			t.Fatalf("trigger error code = %q, want %q: %v", got, apperror.CodeAgentResponseTimeout, err)
		}
	case <-time.After(time.Second):
		t.Fatal("triggered stream did not stop after its idle timeout")
	}
	if got := provider.attemptCount(); got != 1 {
		t.Fatalf("provider attempts = %d, want 1; watchdog cancellation must not retry", got)
	}
}

func TestTriggeredNativeStreamCallerCancellationWinsOverIdleTimeout(t *testing.T) {
	provider := &silentTriggerProvider{started: make(chan struct{})}
	logger := slog.New(slog.DiscardHandler)
	svc := &Service{
		agent:             native.New(native.Deps{Logger: logger}),
		logger:            logger,
		messageService:    &recordingMessageService{},
		streamIdleTimeout: time.Second,
	}
	cfg := triggerResolvedRunConfig(provider, "run scheduled task", time.Now(), sessionmode.Schedule)
	cfg = svc.prepareRunConfig(context.Background(), cfg)
	rc := resolvedContext{
		runConfig: cfg,
		model:     models.GetResponse{ID: "model-1"},
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	wantCause := errors.New("manual schedule trigger stopped")
	done := make(chan error, 1)
	go func() {
		_, err := svc.runTriggeredNativeStream(
			ctx,
			cfg,
			triggerStreamRequest(),
			rc,
			sessionruntime.RunHandle{RunID: "run-1", TurnID: "turn-1"},
			nil,
			nil,
		)
		done <- err
	}()

	select {
	case <-provider.started:
	case <-time.After(time.Second):
		cancel(wantCause)
		<-done
		t.Fatal("silent provider was not started")
	}
	cancel(wantCause)

	select {
	case err := <-done:
		if !errors.Is(err, wantCause) {
			t.Fatalf("trigger error = %v, want caller cancellation cause", err)
		}
		if got := apperror.CodeOf(err); got == apperror.CodeAgentResponseTimeout {
			t.Fatalf("caller cancellation was rewritten as timeout: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("triggered stream did not stop after caller cancellation")
	}
	if got := provider.attemptCount(); got != 1 {
		t.Fatalf("provider attempts = %d, want 1 after caller cancellation", got)
	}
}

func TestConsumeTriggeredStreamProjectsEventsAndBuildsResult(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	pub := &recordingTurnEventPublisher{}
	svc := newTriggerStreamService(messages, pub)

	events := make(chan native.StreamEvent, 2)
	events <- native.StreamEvent{Type: native.EventTextDelta, Delta: "working"}
	events <- scheduleTerminalEvent(t, "对账完成。")
	close(events)

	result, err := svc.consumeTriggeredStream(context.Background(), events, triggerStreamRequest(), resolvedContext{}, sessionruntime.RunHandle{RunID: "run-1", TurnID: "turn-1"}, nil, nil)
	if err != nil {
		t.Fatalf("consumeTriggeredStream() error = %v", err)
	}
	if result.Status != "ok" {
		t.Fatalf("result.Status = %q, want ok", result.Status)
	}
	if result.Text != "对账完成。" {
		t.Fatalf("result.Text = %q, want the terminal assistant text", result.Text)
	}
	if len(result.UsageBytes) == 0 {
		t.Fatal("result.UsageBytes is empty, want the terminal usage payload")
	}

	published := pub.published()
	if len(published) != 2 {
		t.Fatalf("published %d events, want both stream events projected", len(published))
	}
	if published[0].Type != native.EventTextDelta || published[1].Type != native.EventAgentEnd {
		t.Fatalf("published event types = %q, %q; want text delta then agent end", published[0].Type, published[1].Type)
	}

	// No step committer: the terminal snapshot fallback persists the round
	// (user query prepended + assistant output), exactly like the WS fallback.
	if len(messages.persisted) != 2 {
		t.Fatalf("persisted %d messages, want user + assistant via terminal snapshot", len(messages.persisted))
	}
	if messages.persisted[0].Role != "user" || messages.persisted[1].Role != "assistant" {
		t.Fatalf("persisted roles = %q, %q; want user then assistant", messages.persisted[0].Role, messages.persisted[1].Role)
	}
}

func TestConsumeTriggeredStreamStopsWhenProjectionRefused(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	pub := &recordingTurnEventPublisher{errAt: 2, err: errors.New("projection refused")}
	svc := newTriggerStreamService(messages, pub)

	events := make(chan native.StreamEvent, 3)
	events <- native.StreamEvent{Type: native.EventTextDelta, Delta: "a"}
	events <- native.StreamEvent{Type: native.EventTextDelta, Delta: "b"}
	events <- scheduleTerminalEvent(t, "done")
	close(events)

	_, err := svc.consumeTriggeredStream(context.Background(), events, triggerStreamRequest(), resolvedContext{}, sessionruntime.RunHandle{RunID: "run-1", TurnID: "turn-1"}, nil, nil)
	if err == nil {
		t.Fatal("consumeTriggeredStream() error = nil, want the projection failure")
	}
	if pub.calls != 2 {
		t.Fatalf("publish calls = %d, want exactly the refused call before stopping", pub.calls)
	}
	// The terminal event was never consumed, so nothing may be persisted.
	if len(messages.persisted) != 0 {
		t.Fatalf("persisted %d messages, want none after a refused projection", len(messages.persisted))
	}
}

func TestConsumeTriggeredStreamFailsWithoutTerminalEvent(t *testing.T) {
	t.Parallel()

	svc := newTriggerStreamService(&recordingMessageService{}, &recordingTurnEventPublisher{})

	events := make(chan native.StreamEvent, 1)
	events <- native.StreamEvent{Type: native.EventTextDelta, Delta: "working"}
	close(events)

	_, err := svc.consumeTriggeredStream(context.Background(), events, triggerStreamRequest(), resolvedContext{}, sessionruntime.RunHandle{RunID: "run-1", TurnID: "turn-1"}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "terminal event") {
		t.Fatalf("consumeTriggeredStream() error = %v, want the missing-terminal failure", err)
	}
}

func TestConsumeTriggeredStreamSurfacesStreamErrorWithoutTerminal(t *testing.T) {
	t.Parallel()

	svc := newTriggerStreamService(&recordingMessageService{}, &recordingTurnEventPublisher{})

	events := make(chan native.StreamEvent, 1)
	events <- native.StreamEvent{Type: native.EventError, Error: "provider boom"}
	close(events)

	_, err := svc.consumeTriggeredStream(context.Background(), events, triggerStreamRequest(), resolvedContext{}, sessionruntime.RunHandle{RunID: "run-1", TurnID: "turn-1"}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "provider boom") {
		t.Fatalf("consumeTriggeredStream() error = %v, want the provider error", err)
	}
}

func TestConsumeTriggeredStreamKeepsDiagnosticsInternalAndPublishesStableFailure(t *testing.T) {
	t.Parallel()

	pub := &recordingTurnEventPublisher{}
	svc := newTriggerStreamService(&recordingMessageService{}, pub)

	events := make(chan native.StreamEvent, 1)
	events <- native.StreamEvent{Type: native.EventError, Error: "SECRET provider boom"}
	close(events)

	_, err := svc.consumeTriggeredStream(context.Background(), events, triggerStreamRequest(), resolvedContext{}, sessionruntime.RunHandle{RunID: "run-1", TurnID: "turn-1"}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "SECRET provider boom") {
		t.Fatalf("internal trigger error = %v, want private provider diagnostic", err)
	}
	published := pub.published()
	if len(published) != 1 {
		t.Fatalf("published events = %#v, want one stable failure", published)
	}
	failure := published[0]
	if failure.Code != string(apperror.CodeAgentResponseInterrupted) {
		t.Fatalf("published code = %q, want %q", failure.Code, apperror.CodeAgentResponseInterrupted)
	}
	if strings.Contains(failure.Error, "SECRET") {
		t.Fatalf("private diagnostic leaked to runtime projection: %q", failure.Error)
	}
}

func TestConsumeTriggeredStreamSkipsPersistenceOnOwnershipLoss(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	svc := newTriggerStreamService(messages, &recordingTurnEventPublisher{})

	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(sessionruntime.ErrRunOwnershipLost)

	events := make(chan native.StreamEvent, 1)
	events <- scheduleTerminalEvent(t, "done")
	close(events)

	_, err := svc.consumeTriggeredStream(ctx, events, triggerStreamRequest(), resolvedContext{}, sessionruntime.RunHandle{RunID: "run-1", TurnID: "turn-1"}, nil, nil)
	if !errors.Is(err, sessionruntime.ErrRunOwnershipLost) {
		t.Fatalf("consumeTriggeredStream() error = %v, want ErrRunOwnershipLost", err)
	}
	if len(messages.persisted) != 0 {
		t.Fatalf("persisted %d messages, want none: a superseded owner must not write history", len(messages.persisted))
	}
}

func TestConsumeTriggeredStreamReportsAbortWithPartialTranscript(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	svc := newTriggerStreamService(messages, &recordingTurnEventPublisher{})

	// A routed abort (web stop, runtime revoke) cancels the run context with
	// a cause; the terminal event still carries the partial transcript.
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(context.Canceled)

	messagesJSON, err := json.Marshal([]sdk.Message{sdk.AssistantMessage("partial answer")})
	if err != nil {
		t.Fatalf("marshal abort messages: %v", err)
	}
	events := make(chan native.StreamEvent, 2)
	events <- native.StreamEvent{Type: native.EventTextDelta, Delta: "working"}
	events <- native.StreamEvent{Type: native.EventAgentAbort, Messages: messagesJSON}
	close(events)

	_, err = svc.consumeTriggeredStream(ctx, events, triggerStreamRequest(), resolvedContext{}, sessionruntime.RunHandle{RunID: "run-1", TurnID: "turn-1"}, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("consumeTriggeredStream() error = %v, want the abort cause, not a success", err)
	}
	// The abort changes the reported outcome, not the history discipline: the
	// partial transcript persists for audit exactly like a clean terminal.
	if len(messages.persisted) != 2 {
		t.Fatalf("persisted %d messages, want user + partial assistant", len(messages.persisted))
	}
}

func TestConsumeTriggeredStreamDeferredApprovalIsNotAnAbort(t *testing.T) {
	t.Parallel()

	svc := newTriggerStreamService(&recordingMessageService{}, &recordingTurnEventPublisher{})

	messagesJSON, err := json.Marshal([]sdk.Message{sdk.AssistantMessage("needs approval")})
	if err != nil {
		t.Fatalf("marshal deferred messages: %v", err)
	}
	events := make(chan native.StreamEvent, 1)
	// A deferred approval pauses the run for a decision; it is not a stopped
	// run, so the outcome stays ok (mirrors the WS loop's deferred guard).
	events <- native.StreamEvent{Type: native.EventAgentAbort, ApprovalID: "appr-1", Messages: messagesJSON}
	close(events)

	result, err := svc.consumeTriggeredStream(context.Background(), events, triggerStreamRequest(), resolvedContext{}, sessionruntime.RunHandle{RunID: "run-1", TurnID: "turn-1"}, nil, nil)
	if err != nil {
		t.Fatalf("consumeTriggeredStream() error = %v, want nil for a deferred approval", err)
	}
	if result.Status != "ok" {
		t.Fatalf("result.Status = %q, want ok", result.Status)
	}
}

func TestConsumeTriggeredStreamWithStepCommitterDoesNotDoublePersist(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	svc := newTriggerStreamService(messages, &recordingTurnEventPublisher{})

	// A step committer with nothing committed finalizes into a no-op, but its
	// presence must switch the terminal path away from the snapshot fallback —
	// otherwise every step would be written twice.
	committer := &agentStepCommitter{service: svc, req: triggerStreamRequest()}

	events := make(chan native.StreamEvent, 1)
	events <- scheduleTerminalEvent(t, "done")
	close(events)

	result, err := svc.consumeTriggeredStream(context.Background(), events, triggerStreamRequest(), resolvedContext{}, sessionruntime.RunHandle{RunID: "run-1", TurnID: "turn-1"}, committer, nil)
	if err != nil {
		t.Fatalf("consumeTriggeredStream() error = %v", err)
	}
	if result.Status != "ok" || result.Text != "done" {
		t.Fatalf("result = %#v, want completed output", result)
	}
	if len(messages.persisted) != 0 {
		t.Fatalf("persisted %d messages via the fallback, want none: step committer owns persistence", len(messages.persisted))
	}
}

// fakeTriggeredAdmitter captures the Admit input so tests can drive the
// admission hook exactly the way the runtime manager would at activation.
type fakeTriggeredAdmitter struct {
	admitInput sessionruntime.AdmitInput
}

func (f *fakeTriggeredAdmitter) Admit(_ context.Context, input sessionruntime.AdmitInput) (sessionruntime.Admission, error) {
	f.admitInput = input
	return sessionruntime.Admission{
		Started:      true,
		RunID:        "run-1",
		TurnID:       "turn-1",
		TurnPosition: 1,
		Handle: sessionruntime.RunHandle{
			BotID:        input.BotID,
			SessionID:    input.SessionID,
			RunID:        "run-1",
			TurnID:       "turn-1",
			FencingToken: 1,
		},
	}, nil
}

func (*fakeTriggeredAdmitter) FinishRun(context.Context, sessionruntime.RunHandle, string, string) error {
	return nil
}

func TestAdmitTriggeredRunInjectsAdmissionView(t *testing.T) {
	t.Parallel()

	admitter := &fakeTriggeredAdmitter{}
	svc := &Service{logger: slog.New(slog.DiscardHandler), sessionRuntime: admitter}

	viewFn := func(handle sessionruntime.RunHandle) *sessionruntime.RunAdmissionView {
		return &sessionruntime.RunAdmissionView{
			RequestUserTurn: &chatview.UITurn{
				TurnID:    handle.TurnID,
				Role:      "user",
				Text:      "run scheduled task",
				Timestamp: time.Now(),
			},
		}
	}

	ctx, admission, _, err := svc.admitTriggeredRun(context.Background(), "bot-1", "session-1", "inv-1", []byte(`{}`), viewFn)
	if err != nil {
		t.Fatalf("admitTriggeredRun() error = %v", err)
	}
	if admitter.admitInput.Execution.Admission == nil {
		t.Fatal("runtime admission hook is nil")
	}
	view, err := admitter.admitInput.Execution.Admission(ctx, admission.Handle)
	if err != nil {
		t.Fatalf("admission hook error = %v", err)
	}
	if view.RequestUserTurn == nil {
		t.Fatal("request user turn is nil, want the injected projection")
	}
	if view.RequestUserTurn.Text != "run scheduled task" {
		t.Fatalf("request user turn text = %q, want the schedule command", view.RequestUserTurn.Text)
	}
	if view.RequestUserTurn.TurnID != admission.TurnID {
		t.Fatalf("request user turn turn id = %q, want the admitted turn %q", view.RequestUserTurn.TurnID, admission.TurnID)
	}
}

func TestAdmitTriggeredRunWithoutViewKeepsEmptyProjection(t *testing.T) {
	t.Parallel()

	admitter := &fakeTriggeredAdmitter{}
	svc := &Service{logger: slog.New(slog.DiscardHandler), sessionRuntime: admitter}

	ctx, admission, _, err := svc.admitTriggeredRun(context.Background(), "bot-1", "session-1", "inv-1", []byte(`{}`), nil)
	if err != nil {
		t.Fatalf("admitTriggeredRun() error = %v", err)
	}
	view, err := admitter.admitInput.Execution.Admission(ctx, admission.Handle)
	if err != nil {
		t.Fatalf("admission hook error = %v", err)
	}
	if view.RequestUserTurn != nil {
		t.Fatalf("request user turn = %#v, want nil without a view factory", view.RequestUserTurn)
	}
}
