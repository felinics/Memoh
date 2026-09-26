package application

import (
	"context"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/apperror"
)

// stopDuringSetupDriver behaves like a driver the user stops while it is still
// setting up (starting its process, resuming its native session): it reports the
// cancellation under the configuration-class code that stage wraps errors in.
type stopDuringSetupDriver struct{ started chan struct{} }

func (stopDuringSetupDriver) RuntimeType() string { return "codex" }

func (d stopDuringSetupDriver) Prompt(ctx context.Context, _ external.PromptInput) (external.PromptResult, error) {
	close(d.started)
	<-ctx.Done()
	return external.PromptResult{}, apperror.Wrap(apperror.CodeExternalRuntimeUnavailable, ctx.Err(), nil)
}

func TestUserStopDuringDriverSetupKeepsTheUserMessage(t *testing.T) {
	messages := &recordingMessageService{}
	service := newACPLifecycleService(t, &recordingACPPrompter{}, messages, &recordingContextLifecycleStore{})
	driver := stopDuringSetupDriver{started: make(chan struct{})}
	abortCh := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- service.streamRuntimeWS(context.Background(), driver, ChatRequest{
			BotID:    lifecycleTestBotID,
			ThreadID: lifecycleTestSessionID,
			RunID:    lifecycleTestRunID,
			Query:    "inspect",
		}, make(chan WSStreamEvent, 8), abortCh)
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
