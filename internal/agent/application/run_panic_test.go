package application

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	acpagent "github.com/felinics/memoh/internal/agent/runtime/acp"
	acpclient "github.com/felinics/memoh/internal/agent/runtime/acp/client"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// A panic in an external runtime ends that run with the internal code. The
// caller gets the failure on its error channel, the channels close, and the
// panic value is not part of what it is told.
func TestStreamChatEndsAnExternalRuntimePanicAsAnInternalFailure(t *testing.T) {
	t.Parallel()

	pool := &recordingACPPrompter{
		promptFn: func(context.Context, acpagent.PromptInput) (acpclient.PromptResult, error) {
			panic("runtime boom")
		},
	}
	service := newACPLifecycleService(t, pool, &recordingMessageService{}, nil)
	service.SetACPSessionPool(pool)

	chunks, errs := service.StreamChat(t.Context(), ChatRequest{
		BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID, Query: "go", UserMessagePersisted: true,
	})
	drainStreamChunks(t, chunks)
	var got error
	for err := range errs {
		got = err
	}
	if got == nil || apperror.CodeOf(got) != apperror.CodeInternal {
		t.Fatalf("StreamChat error = %v, want code %q", got, apperror.CodeInternal)
	}
	if apperror.ArgsOf(got) != nil && len(apperror.ArgsOf(got)) > 0 {
		t.Fatalf("error args = %v, want none", apperror.ArgsOf(got))
	}
}

// A panic while a run's output is being delivered ends that run: the terminal
// write that frees the thread's slot still happens, with the internal code, the
// handle's channels close, and the thread admits the next turn.
func TestStartTurnEndsADeliveryPanicAsAnInternalFailure(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{chunks: []string{`{"type":"text_delta","text":"a"}`}}
	service, admitter := newAdmittedTurnTestService(runner)
	service.publishTurnEvent = func(context.Context, sessionruntime.RunHandle, native.StreamEvent) error {
		panic("publish boom")
	}
	cmd := turn.StartTurnCommand{TeamID: "t", Mode: turn.ModeChat, BotID: "b", ThreadID: "s", Query: "hi"}

	h, err := service.StartTurn(t.Context(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	var got error
	timeout := time.After(5 * time.Second)
	for events, errs := h.Events(), h.Errs(); events != nil || errs != nil; {
		select {
		case _, ok := <-events:
			if !ok {
				events = nil
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			got = err
		case <-timeout:
			t.Fatal("the run's channels did not close after the panic")
		}
	}
	if got == nil || apperror.CodeOf(got) != apperror.CodeInternal {
		t.Fatalf("run error = %v, want code %q", got, apperror.CodeInternal)
	}
	finish := admitter.awaitFinish(t)
	if finish.status != sessionruntime.RunStatusErrored {
		t.Fatalf("terminal status = %q, want %q", finish.status, sessionruntime.RunStatusErrored)
	}

	service.publishTurnEvent = admitter.PublishAgentEvent
	next, err := service.StartTurn(t.Context(), cmd)
	if err != nil {
		t.Fatalf("the next turn was refused after the panic: %v", err)
	}
	next.Cancel()
}

// A panic in the loop that consumes a native run's events ends that run. The
// caller's channels close with the internal code, and the model's producer is
// not left waiting on a consumer that is gone.
func TestStreamChatEndsANativeConsumerPanicAsAnInternalFailure(t *testing.T) {
	fixture := newDirectLifecycleFixture(t, directLifecycleModelSuccess)
	fixture.service.publishTurnEvent = func(context.Context, sessionruntime.RunHandle, native.StreamEvent) error {
		panic("publish boom")
	}

	chunks, errs := fixture.service.StreamChat(t.Context(), ChatRequest{
		BotID: lifecycleTestBotID, ChatID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID,
		Query: directLifecyclePrompt, UserMessagePersisted: true, PublishRuntimeEvents: true,
	})
	var got error
	timeout := time.After(10 * time.Second)
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
			got = err
		case <-timeout:
			t.Fatal("the stream did not finish after the panic")
		}
	}
	if got == nil || apperror.CodeOf(got) != apperror.CodeInternal {
		t.Fatalf("StreamChat error = %v, want code %q", got, apperror.CodeInternal)
	}
	// The model's producer is released rather than left blocked on events no
	// one reads.
	deadline := time.Now().Add(5 * time.Second)
	for runStreamGoroutines() > 0 {
		if time.Now().After(deadline) {
			t.Fatal("the runtime's stream goroutine is still running after the consumer died")
		}
		time.Sleep(10 * time.Millisecond)
	}
	creates := fixture.lifecycles.creates()
	if len(creates) != 1 {
		t.Fatalf("lifecycle creates = %d, want 1", len(creates))
	}
	if code := creates[0].ErrorCode; !code.Valid || code.String != string(apperror.CodeInternal) {
		t.Fatalf("lifecycle error code = %#v, want %q: the panic must be the run's recorded cause", code, apperror.CodeInternal)
	}
}

func runStreamGoroutines() int {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	return strings.Count(string(buf), "native.(*Agent).runStream")
}

// A panic while the turn is being resolved, before any runtime has started,
// ends the turn the same way.
func TestStreamChatEndsASetupPanicAsAnInternalFailure(t *testing.T) {
	t.Parallel()

	pool := &recordingACPPrompter{}
	service := newACPLifecycleService(t, pool, &recordingMessageService{}, nil)
	service.sessionService = &fakeBackgroundSessionService{
		getFn: func(context.Context, string) (session.Thread, error) { panic("setup boom") },
	}

	chunks, errs := service.StreamChat(t.Context(), ChatRequest{
		BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID, Query: "go", UserMessagePersisted: true,
	})
	drainStreamChunks(t, chunks)
	var got error
	for err := range errs {
		got = err
	}
	if got == nil || apperror.CodeOf(got) != apperror.CodeInternal {
		t.Fatalf("StreamChat error = %v, want code %q", got, apperror.CodeInternal)
	}
	if pool.calls != 0 {
		t.Fatalf("runtime calls = %d, want none", pool.calls)
	}
}

// panicOnSettings panics where the native path loads the session's model
// preference, which is part of assembling the run before any model is called.
type panicOnSettings struct{ dbstore.Queries }

func (panicOnSettings) GetSessionByID(context.Context, pgtype.UUID) (sqlc.BotSession, error) {
	panic("resolve boom")
}

// A panic while the native path assembles the run (resolve, hooks, context
// assembly) ends that turn: the caller gets the internal code, the run is
// finished in the session runtime so the thread's slot is released, and the
// model is never called.
func TestStartTurnEndsANativeSetupPanicAsAnInternalFailure(t *testing.T) {
	fixture := newDirectLifecycleFixture(t, directLifecycleModelSuccess)
	fixture.service.queries = panicOnSettings{fixture.service.queries}

	handle, err := fixture.service.StartTurn(t.Context(), turn.StartTurnCommand{
		SchemaVersion: 1, TeamID: "direct-lifecycle-team", Mode: turn.ModeChat,
		BotID: lifecycleTestBotID, ChatID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID,
		Query: directLifecyclePrompt, UserMessagePersisted: true,
	})
	if err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}
	var got error
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range handle.Events() {
		}
		for err := range handle.Errs() {
			got = err
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the turn did not finish after the panic")
	}
	if got == nil || apperror.CodeOf(got) != apperror.CodeInternal {
		t.Fatalf("turn error = %v, want code %q", got, apperror.CodeInternal)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		fixture.runtime.mu.Lock()
		finishes := len(fixture.runtime.finishes)
		fixture.runtime.mu.Unlock()
		if finishes == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the run was never finished, so the thread would stay busy")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(fixture.requests.all()) != 0 {
		t.Fatal("the model was called although setup panicked")
	}
}

// A panic a runtime recovered in a goroutine of its own reaches the user as
// the catalog's catch-all, whatever the panic said.
func TestExternalAgentErrorTranslatesARuntimePanicToInternal(t *testing.T) {
	t.Parallel()

	cause := &external.PanicError{Where: "acp reader", Value: "private detail"}
	got := ExternalAgentError(fmt.Errorf("prompt: %w", cause))
	if apperror.CodeOf(got) != apperror.CodeInternal {
		t.Fatalf("ExternalAgentError() code = %q, want %q", apperror.CodeOf(got), apperror.CodeInternal)
	}
	if !errors.Is(apperror.CauseOf(got), cause) {
		t.Fatal("the panic must stay the private cause of the public error")
	}
}
