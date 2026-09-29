package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/schedule"
)

// stopDuringSetupDriver behaves like a driver the user stops while it is still
// setting up (starting its process, resuming its native session): it reports the
// cancellation as the runtime-unavailable failure that stage wraps errors in.
type stopDuringSetupDriver struct{ started chan struct{} }

func (stopDuringSetupDriver) RuntimeType() string { return "codex" }

func (d stopDuringSetupDriver) Prompt(ctx context.Context, _ external.PromptInput) (external.PromptResult, error) {
	close(d.started)
	<-ctx.Done()
	return external.PromptResult{}, external.Unavailable(ctx.Err())
}

func TestUserStopDuringDriverSetupKeepsTheUserMessage(t *testing.T) {
	messages := &recordingMessageService{}
	service := newACPLifecycleService(t, &recordingACPPrompter{}, messages, &recordingContextLifecycleStore{})
	driver := stopDuringSetupDriver{started: make(chan struct{})}
	abortCh := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- ignoreOutcome(service.streamRuntimeWS(context.Background(), driver, ChatRequest{
			BotID:    lifecycleTestBotID,
			ThreadID: lifecycleTestSessionID,
			RunID:    lifecycleTestRunID,
			Query:    "inspect",
		}, make(chan WSStreamEvent, 8), abortCh, true))
	}()
	select {
	case <-driver.started:
	case <-time.After(3 * time.Second):
		t.Fatal("prompt did not start")
	}
	close(abortCh)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a user stop surfaced as a runtime error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stopped prompt did not return")
	}
	if len(messages.deleted) != 0 {
		t.Fatalf("the user's message was deleted by their own stop: %v", messages.deleted)
	}
	if len(messages.persisted) == 0 || messages.persisted[0].Role != "user" {
		t.Fatalf("persisted = %+v, want the user message kept", messages.persisted)
	}
}

// ignoreOutcome keeps the error of a WebSocket stream call.
func ignoreOutcome(_ RunOutcome, err error) error {
	return err
}

// A driver that stops answering while still setting up reports the idle
// cancellation as runtime-unavailable. The turn failed on the idle timeout, not
// on configuration, so the round is kept with agent.response_timeout.
func TestIdleTimeoutDuringDriverSetupKeepsAFailedRound(t *testing.T) {
	messages := &recordingMessageService{}
	service := newACPLifecycleService(t, &recordingACPPrompter{}, messages, &recordingContextLifecycleStore{})
	service.streamIdleTimeout = 20 * time.Millisecond
	driver := stopDuringSetupDriver{started: make(chan struct{})}
	events := make(chan WSStreamEvent, 16)
	err := ignoreOutcome(service.streamRuntimeWS(context.Background(), driver, ChatRequest{
		BotID:    lifecycleTestBotID,
		ThreadID: lifecycleTestSessionID,
		RunID:    lifecycleTestRunID,
		Query:    "inspect",
	}, events, make(chan struct{}), true))
	if apperror.CodeOf(err) == apperror.CodeAgentResponseTimeout {
		t.Fatalf("the idle timeout was answered as a configuration failure: %v", err)
	}
	if len(messages.deleted) != 0 || len(messages.persisted) == 0 || messages.persisted[0].Role != "user" {
		t.Fatalf("persisted = %+v deleted = %v, want the user message kept", messages.persisted, messages.deleted)
	}
	var failure string
	for _, ev := range drainAgentEvents(t, events) {
		if ev.Type == native.EventError {
			failure = ev.Code
		}
	}
	if failure != string(apperror.CodeAgentResponseTimeout) {
		t.Fatalf("failure event code = %q, want %s", failure, apperror.CodeAgentResponseTimeout)
	}
}

// The scheduled path keeps the same round for the same reason.
func TestScheduledIdleTimeoutDuringDriverSetupKeepsAFailedRound(t *testing.T) {
	messages := &recordingMessageService{}
	service := newACPLifecycleService(t, &recordingACPPrompter{}, messages, &recordingContextLifecycleStore{})
	service.streamIdleTimeout = 20 * time.Millisecond
	driver := stopDuringSetupDriver{started: make(chan struct{})}
	_, err := service.triggerScheduleRuntime(context.Background(), lifecycleTestBotID, schedule.TriggerPayload{
		SessionID: lifecycleTestSessionID, Command: "inspect", OwnerUserID: "user-1",
	}, "", lifecycleTestRunID, driver)
	if apperror.CodeOf(err) != apperror.CodeAgentResponseTimeout {
		t.Fatalf("schedule error = %v, want %s", err, apperror.CodeAgentResponseTimeout)
	}
	if len(messages.deleted) != 0 || len(messages.persisted) == 0 {
		t.Fatalf("persisted = %+v deleted = %v, want the failed round kept", messages.persisted, messages.deleted)
	}
}

// A scheduled run whose runtime could not start leaves no round in history;
// a failure the runtime reports after it started keeps one.
func TestScheduledRuntimeConfigurationFailureLeavesNoRound(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		code      apperror.Code
		keepRound bool
	}{
		{"runtime unavailable", external.Unavailable(errors.New("bridge down")), apperror.CodeExternalRuntimeUnavailable, false},
		{"dependency missing", &external.DependencyMissingError{DependencyID: "codex"}, apperror.CodeAgentDependencyMissing, false},
		{"control failed", external.Fail(external.FailureControlFailed, errors.New("set mode")), apperror.CodeRuntimeControlFailed, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := &recordingMessageService{}
			service := newACPLifecycleService(t, &recordingACPPrompter{}, messages, &recordingContextLifecycleStore{})
			driver := noticeTestDriver{kind: "codex", prompt: func(context.Context, external.PromptInput) (external.PromptResult, error) {
				return external.PromptResult{}, tc.err
			}}
			_, err := service.triggerScheduleRuntime(context.Background(), lifecycleTestBotID, schedule.TriggerPayload{
				SessionID: lifecycleTestSessionID, Command: "inspect", OwnerUserID: "user-1",
			}, "", lifecycleTestRunID, driver)
			if got := apperror.CodeOf(err); got != tc.code {
				t.Fatalf("schedule error code = %q, want %q: %v", got, tc.code, err)
			}
			if kept := len(messages.deleted) == 0; kept != tc.keepRound {
				t.Fatalf("persisted = %+v deleted = %v, want round kept = %v", messages.persisted, messages.deleted, tc.keepRound)
			}
		})
	}
}

// A scheduled run that hits its execution budget failed on the budget, even
// when the runtime reported the stop as being unavailable; the round is kept.
func TestScheduledExecutionTimeoutIsNotAConfigurationFailure(t *testing.T) {
	messages := &recordingMessageService{}
	service := newACPLifecycleService(t, &recordingACPPrompter{}, messages, &recordingContextLifecycleStore{})
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	driver := noticeTestDriver{kind: "codex", prompt: func(context.Context, external.PromptInput) (external.PromptResult, error) {
		cancel(schedule.ErrExecutionTimeout)
		return external.PromptResult{TurnCompleted: true}, external.Unavailable(context.Canceled)
	}}
	_, err := service.triggerScheduleRuntime(ctx, lifecycleTestBotID, schedule.TriggerPayload{
		SessionID: lifecycleTestSessionID, Command: "inspect", OwnerUserID: "user-1",
	}, "", lifecycleTestRunID, driver)
	if got := apperror.CodeOf(err); got != apperror.CodeScheduleExecutionTimeout {
		t.Fatalf("schedule error code = %q, want %q: %v", got, apperror.CodeScheduleExecutionTimeout, err)
	}
	if len(messages.deleted) != 0 || len(messages.persisted) == 0 {
		t.Fatalf("persisted = %+v deleted = %v, want the failed round kept", messages.persisted, messages.deleted)
	}
}
