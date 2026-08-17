package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	sdk "github.com/memohai/twilight-ai/sdk"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
	"github.com/memohai/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/memohai/memoh/internal/agent/runtime/session"
	"github.com/memohai/memoh/internal/agent/turn"
	"github.com/memohai/memoh/internal/apperror"
)

type canceledDiscussTerminalReporter struct {
	started chan struct{}
}

func (f *canceledDiscussTerminalReporter) Stream(ctx context.Context, _ native.RunConfig) <-chan native.StreamEvent {
	ch := make(chan native.StreamEvent, 1)
	close(f.started)
	go func() {
		defer close(ch)
		<-ctx.Done()
		ch <- native.StreamEvent{Type: native.EventAgentAbort}
	}()
	return ch
}

type canceledDiscussMessagesReporter struct {
	started chan struct{}
}

func (f *canceledDiscussMessagesReporter) Stream(ctx context.Context, _ native.RunConfig) <-chan native.StreamEvent {
	ch := make(chan native.StreamEvent, 1)
	close(f.started)
	go func() {
		defer close(ch)
		<-ctx.Done()
		ch <- native.StreamEvent{
			Type:     native.EventAgentAbort,
			Messages: json.RawMessage(`[{"role":"assistant","content":"partial answer"}]`),
		}
	}()
	return ch
}

type decisionDiscussAgentStreamer struct{}

func (*decisionDiscussAgentStreamer) Stream(context.Context, native.RunConfig) <-chan native.StreamEvent {
	ch := make(chan native.StreamEvent, 2)
	ch <- native.StreamEvent{
		Type:       native.EventToolApprovalRequest,
		ApprovalID: "approval-1",
		Status:     "pending",
	}
	ch <- native.StreamEvent{
		Type:       native.EventAgentEnd,
		ApprovalID: "approval-1",
		Status:     "pending",
		Messages:   json.RawMessage(`[{"role":"assistant","content":"done"}]`),
	}
	close(ch)
	return ch
}

type fullBufferTerminalDiscussStreamer struct{}

func (*fullBufferTerminalDiscussStreamer) Stream(context.Context, native.RunConfig) <-chan native.StreamEvent {
	ch := make(chan native.StreamEvent, 16)
	for range 15 {
		ch <- native.StreamEvent{Type: native.EventTextDelta, Delta: "x"}
	}
	ch <- native.StreamEvent{
		Type:     native.EventAgentEnd,
		Messages: json.RawMessage(`[{"role":"assistant","content":"done"}]`),
	}
	close(ch)
	return ch
}

type idleDiscussAgentStreamer struct{}

func (*idleDiscussAgentStreamer) Stream(ctx context.Context, _ native.RunConfig) <-chan native.StreamEvent {
	ch := make(chan native.StreamEvent, 1)
	go func() {
		defer close(ch)
		<-ctx.Done()
		ch <- native.StreamEvent{Type: native.EventAgentAbort}
	}()
	return ch
}

func TestDiscussIdleTimeoutPersistsInterruptedTurnMarker(t *testing.T) {
	resolver := &fakeDiscussService{resolveResult: ResolveRunConfigResult{ModelID: "model-1"}}
	service := newDiscussTestService(&fakeRunner{}, &idleDiscussAgentStreamer{}, resolver)
	service.streamIdleTimeout = 10 * time.Millisecond

	h, err := service.StartTurn(context.Background(), discussCommand())
	if err != nil {
		t.Fatal(err)
	}
	drainDiscuss(t, h)

	if resolver.storeCalls != 1 {
		t.Fatalf("store calls = %d, want one interrupted turn", resolver.storeCalls)
	}
	if len(resolver.storedMessages) != 1 || resolver.storedMessages[0].Content[0].(sdk.TextPart).Text != interruptedTurnMarker {
		t.Fatalf("stored messages = %#v, want interrupted marker", resolver.storedMessages)
	}
}

func TestDiscussExplicitCancellationWithoutOutputSkipsInterruptedMarker(t *testing.T) {
	agent := &canceledDiscussTerminalReporter{started: make(chan struct{})}
	resolver := &fakeDiscussService{resolveResult: ResolveRunConfigResult{ModelID: "model-1"}}
	service := newDiscussTestService(&fakeRunner{}, agent, resolver)

	h, err := service.StartTurn(context.Background(), discussCommand())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-agent.started:
	case <-time.After(time.Second):
		t.Fatal("discuss stream did not start")
	}
	h.Cancel()
	drainDiscuss(t, h)

	if resolver.storeCalls != 0 {
		t.Fatalf("store calls = %d, want no synthetic message for explicit cancellation", resolver.storeCalls)
	}
}

func TestDiscussCancellationPersistsAndPublishesTerminalOnDetachedContext(t *testing.T) {
	agent := &canceledDiscussMessagesReporter{started: make(chan struct{})}
	resolver := &fakeDiscussService{
		resolveResult: ResolveRunConfigResult{ModelID: "model-1"},
	}
	a := newDiscussTestService(&fakeRunner{}, agent, resolver)
	admitter := a.sessionRuntime.(*scriptedAdmitter)
	stored := make(chan struct{}, 1)
	published := make(chan struct{}, 1)
	a.turnHooks.storeRound = func(
		ctx context.Context,
		_, _, _, _, _ string,
		_ []sdk.Message,
		_ string,
		_ *contextfrag.LifecycleHolder,
	) error {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		stored <- struct{}{}
		return nil
	}
	a.publishTurnEvent = func(
		ctx context.Context,
		handle sessionruntime.RunHandle,
		event native.StreamEvent,
	) error {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if event.Type == native.EventAgentAbort {
			published <- struct{}{}
		}
		return admitter.PublishAgentEvent(ctx, handle, event)
	}
	h, err := a.StartTurn(context.Background(), discussCommand())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-agent.started:
	case <-time.After(time.Second):
		t.Fatal("discuss turn did not reach native streaming")
	}
	h.Cancel()
	for range h.Events() {
	}
	for streamErr := range h.Errs() {
		t.Fatalf("canceled discuss run exposed stream error: %v", streamErr)
	}
	select {
	case <-stored:
	default:
		t.Fatal("canceled discuss terminal messages were not stored")
	}
	select {
	case <-published:
	default:
		t.Fatal("canceled discuss terminal event was not published")
	}

	if got := admitter.awaitFinish(t); got.status != "" {
		t.Fatalf("status = %q, want runtime-derived aborted status", got.status)
	}
}

func TestDiscussPublishesDecisionTerminalAfterStoreSucceeds(t *testing.T) {
	agent := &decisionDiscussAgentStreamer{}
	resolver := &fakeDiscussService{
		resolveResult: ResolveRunConfigResult{ModelID: "model-1"},
	}
	a := newDiscussTestService(&fakeRunner{}, agent, resolver)
	admitter := a.sessionRuntime.(*scriptedAdmitter)
	storeStarted := make(chan struct{})
	releaseStore := make(chan struct{})
	storeFinished := make(chan struct{})
	resolver.storeFn = func() error {
		close(storeStarted)
		<-releaseStore
		admitter.mu.Lock()
		defer admitter.mu.Unlock()
		for _, event := range admitter.published {
			if event.Type == native.EventAgentEnd || event.Type == native.EventAgentAbort {
				return errors.New("terminal event published before StoreRound")
			}
		}
		close(storeFinished)
		return nil
	}

	h, err := a.StartTurn(context.Background(), discussCommand())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-storeStarted:
	case <-time.After(time.Second):
		t.Fatal("StoreRound was not called")
	}
	var events []turn.Event
	for len(events) < 2 {
		select {
		case event := <-h.Events():
			events = append(events, event)
		case <-time.After(time.Second):
			t.Fatal("non-terminal discuss events were not emitted before StoreRound")
		}
	}
	for _, event := range events {
		if event.Kind == string(native.EventAgentEnd) || event.Kind == string(native.EventAgentAbort) {
			t.Fatalf("terminal event emitted before StoreRound completed: %#v", event)
		}
	}
	select {
	case event := <-h.Events():
		t.Fatalf("event emitted while StoreRound was blocked: %#v", event)
	default:
	}
	close(releaseStore)
	events = append(events, drainDiscuss(t, h)...)

	admitter.mu.Lock()
	published := append([]native.StreamEvent(nil), admitter.published...)
	admitter.mu.Unlock()
	if len(published) != 2 ||
		published[0].Type != native.EventToolApprovalRequest ||
		published[1].Type != native.EventAgentEnd {
		t.Fatalf("published events = %#v, want approval request then agent end", published)
	}
	if got := admitter.awaitFinish(t); got.status != "" {
		t.Fatalf("finish status = %q, want unnamed runtime-derived status", got.status)
	}
	foundTerminal := false
	for _, event := range events {
		if event.Kind != string(native.EventAgentEnd) && event.Kind != string(native.EventAgentAbort) {
			continue
		}
		foundTerminal = true
		select {
		case <-storeFinished:
		default:
			t.Fatal("terminal event emitted before StoreRound returned")
		}
	}
	if !foundTerminal {
		t.Fatal("terminal event was not emitted after StoreRound succeeded")
	}
}

func TestDiscussWithholdsTerminalPublicationWhenStoreFails(t *testing.T) {
	storeErr := errors.New("store failed")
	resolver := &fakeDiscussService{
		resolveResult: ResolveRunConfigResult{ModelID: "model-1"},
		storeErr:      storeErr,
	}
	a := newDiscussTestService(&fakeRunner{}, &fakeAgentStreamer{}, resolver)
	admitter := a.sessionRuntime.(*scriptedAdmitter)

	h, err := a.StartTurn(context.Background(), discussCommand())
	if err != nil {
		t.Fatal(err)
	}
	var events []turn.Event
	for event := range h.Events() {
		events = append(events, event)
	}
	var publicErr error
	for streamErr := range h.Errs() {
		publicErr = streamErr
	}

	admitter.mu.Lock()
	published := append([]native.StreamEvent(nil), admitter.published...)
	admitter.mu.Unlock()
	for _, event := range published {
		if event.Type == native.EventAgentEnd || event.Type == native.EventAgentAbort {
			t.Fatalf("published terminal event before failed StoreRound: %#v", event)
		}
	}
	for _, event := range events {
		if event.Kind == string(native.EventAgentEnd) || event.Kind == string(native.EventAgentAbort) {
			t.Fatalf("emitted terminal event before failed StoreRound: %#v", event)
		}
	}
	got := admitter.awaitFinish(t)
	if got.status != sessionruntime.RunStatusErrored {
		t.Fatalf("finish status = %q, want %q", got.status, sessionruntime.RunStatusErrored)
	}
	if got.message != string(apperror.CodeSessionHistoryInconsistent) {
		t.Fatalf("finish message = %q, want %q", got.message, apperror.CodeSessionHistoryInconsistent)
	}
	if code := apperror.CodeOf(publicErr); code != apperror.CodeSessionHistoryInconsistent {
		t.Fatalf("public store error code = %q, want %q", code, apperror.CodeSessionHistoryInconsistent)
	}
	if cause := apperror.CauseOf(publicErr); !errors.Is(cause, storeErr) {
		t.Fatalf("private store cause = %v, want %v", cause, storeErr)
	}
}

func TestDiscussPublishedTerminalWinsOverLateConsumerCancellation(t *testing.T) {
	resolver := &fakeDiscussService{
		resolveResult: ResolveRunConfigResult{ModelID: "model-1"},
	}
	a := newDiscussTestService(&fakeRunner{}, &fullBufferTerminalDiscussStreamer{}, resolver)
	admitter := a.sessionRuntime.(*scriptedAdmitter)
	terminalPublished := make(chan struct{})
	a.publishTurnEvent = func(
		ctx context.Context,
		handle sessionruntime.RunHandle,
		event native.StreamEvent,
	) error {
		if err := admitter.PublishAgentEvent(ctx, handle, event); err != nil {
			return err
		}
		if event.Type == native.EventAgentEnd || event.Type == native.EventAgentAbort {
			close(terminalPublished)
		}
		return nil
	}

	h, err := a.StartTurn(context.Background(), discussCommand())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-terminalPublished:
	case <-time.After(time.Second):
		t.Fatal("terminal event was not published to the runtime")
	}
	h.Cancel()
	select {
	case streamErr, ok := <-h.Errs():
		if ok {
			t.Fatalf("late consumer cancellation exposed stream error: %v", streamErr)
		}
	case <-time.After(time.Second):
		t.Fatal("discuss pump did not observe late consumer cancellation")
	}
	var events []turn.Event
	for event := range h.Events() {
		events = append(events, event)
	}
	for _, event := range events {
		if event.Kind == string(native.EventAgentEnd) || event.Kind == string(native.EventAgentAbort) {
			t.Fatalf("blocked terminal event unexpectedly reached the consumer: %#v", event)
		}
	}
	if got := admitter.awaitFinish(t); got.status != "" {
		t.Fatalf("finish status = %q, want runtime-derived terminal outcome", got.status)
	}
}
