package sessionruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
)

func TestGracefulShutdownMarksBeforeCancelAndClosesAdmission(t *testing.T) {
	for _, state := range []string{"running", "completed", "aborted", "waiting_decision", "without_resume"} {
		t.Run(state, func(t *testing.T) {
			runs := newFakeLedger()
			manager := NewManager(NewMemoryBackend(), Options{Ledger: runs, Fence: &fakeFence{}})
			defer func() { _ = manager.Close() }()
			payload := []byte(`{"resume":{"version":1}}`)
			if state == "without_resume" {
				payload = []byte(`{"query":"old run"}`)
			}
			var id string
			canceled := false
			admission, err := manager.Admit(t.Context(), AdmitInput{BotID: testBotID, SessionID: testSessionID, InvocationID: "shutdown", Payload: payload, Execution: Execution{Admission: func(context.Context, RunHandle) (RunAdmissionView, error) { return RunAdmissionView{}, nil }, Cancel: func() {
				run, getErr := runs.Get(context.Background(), id)
				if getErr != nil {
					t.Error(getErr)
					return
				}
				if run.State.Active() {
					t.Errorf("canceled before durable terminal: %s", run.State)
				}
				canceled = true
			}}})
			if err != nil {
				t.Fatal(err)
			}
			id = admission.RunID
			switch state {
			case "completed":
				_, _, err = runs.PrepareFinish(t.Context(), ledger.PrepareFinishParams{RunID: id, FencingToken: admission.Handle.FencingToken, State: ledger.StateCompleted})
			case "aborted":
				_, _, err = runs.RequestAbort(t.Context(), id)
			case "waiting_decision":
				_, _, err = runs.SetWaitingDecision(t.Context(), id, admission.Handle.FencingToken)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.InterruptForShutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
			run, err := runs.Get(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			if (run.ErrorCode == RunErrorInterrupted) != (state == "running") {
				t.Fatalf("state=%s marker=%q", run.State, run.ErrorCode)
			}
			if state == "completed" && run.State != ledger.StateCompleted {
				t.Fatalf("completion overwritten: %s", run.State)
			}
			if !canceled {
				t.Fatal("producer was not canceled")
			}
			_, err = manager.Admit(t.Context(), AdmitInput{BotID: testBotID, SessionID: "other", InvocationID: "late"})
			if !errors.Is(err, ErrManagerClosed) {
				t.Fatalf("admission remained open: %v", err)
			}
		})
	}
}
