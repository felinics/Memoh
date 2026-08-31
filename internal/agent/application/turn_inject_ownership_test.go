package application

import (
	"context"
	"sync"
	"testing"
	"time"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
)

type injectOwnershipAdmitter struct {
	input         sessionruntime.AdmitInput
	finishStarted chan struct{}
	finishRelease chan struct{}
}

func newInjectOwnershipAdmitter() *injectOwnershipAdmitter {
	return &injectOwnershipAdmitter{
		finishStarted: make(chan struct{}),
		finishRelease: make(chan struct{}),
	}
}

func (a *injectOwnershipAdmitter) Admit(_ context.Context, input sessionruntime.AdmitInput) (sessionruntime.Admission, error) {
	a.input = input
	return sessionruntime.Admission{
		RunID:   "run-inject-ownership",
		Started: true,
		Handle: sessionruntime.RunHandle{
			BotID:        input.BotID,
			SessionID:    input.SessionID,
			RunID:        "run-inject-ownership",
			FencingToken: 1,
		},
	}, nil
}

func (a *injectOwnershipAdmitter) FinishRun(context.Context, sessionruntime.RunHandle, string, string) error {
	close(a.finishStarted)
	<-a.finishRelease
	a.input.Execution.InjectCh <- turn.InjectMessage{Text: "finishing steer"}
	return nil
}

func TestRunEndStopsDirectInjectBeforeSessionFinishes(t *testing.T) {
	runner := &fakeRunner{chunks: []string{`{"type":"done"}`}}
	admitter := newInjectOwnershipAdmitter()
	service := &Service{
		sessionRuntime: admitter,
		turnHooks: &turnRuntimeHooks{
			streamChat: runner.StreamChat,
		},
	}
	var releaseOnce sync.Once
	releaseFinish := func() {
		releaseOnce.Do(func() { close(admitter.finishRelease) })
	}
	t.Cleanup(releaseFinish)

	handle, err := service.StartTurn(context.Background(), turn.StartTurnCommand{TeamID: "t", Mode: turn.ModeChat})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-admitter.finishStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("durable finisher did not start")
	}
	if err := handle.Inject(context.Background(), turn.InjectMessage{Text: "late"}); err == nil {
		t.Error("direct inject succeeded after execution stopped")
	}
	releaseFinish()
	drainHandle(handle)
	select {
	case got := <-runner.gotReq.InjectCh:
		if got.Text != "finishing steer" {
			t.Fatalf("inject text = %q", got.Text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session inject did not finish before channel close")
	}
}

func (*injectOwnershipAdmitter) MarkInlineDecisionRun(string, string, string) {}
