package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
)

// recordTerminals installs a terminal observer and returns what it saw.
func recordTerminals(manager *Manager) func() []TerminalRun {
	var (
		mu       sync.Mutex
		observed []TerminalRun
	)
	manager.SetTerminalObserver(func(_ context.Context, run TerminalRun) {
		mu.Lock()
		defer mu.Unlock()
		observed = append(observed, run)
	})
	return func() []TerminalRun {
		mu.Lock()
		defer mu.Unlock()
		return append([]TerminalRun(nil), observed...)
	}
}

// A run the manager ends itself, because its admission failed, records only
// the code. The cause reaches the terminal observer, which writes the run's
// result record, and goes nowhere else.
func TestFailedAdmissionHandsItsCauseToTheTerminalObserver(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)
	observed := recordTerminals(f.manager)
	cause := errors.New("persist user turn failed")
	in := f.input("inv-admission-cause", `{"text":"a"}`)
	in.Execution.Admission = func(context.Context, RunHandle) (RunAdmissionView, error) {
		return RunAdmissionView{}, cause
	}

	if _, err := f.manager.Admit(context.Background(), in); !errors.Is(err, cause) {
		t.Fatalf("admit error = %v, want the builder cause", err)
	}
	terminals := observed()
	if len(terminals) != 1 || !terminals[0].Applied || !errors.Is(terminals[0].Cause, cause) {
		t.Fatalf("terminal observations = %+v, want one applied observation carrying the cause", terminals)
	}
	run, err := f.runs.Get(context.Background(), terminals[0].RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.ErrorMessage != "" || run.ProposedErrorMessage != "" {
		t.Fatalf("session_runs messages = %q / %q, want none", run.ProposedErrorMessage, run.ErrorMessage)
	}
}

// An owner's own terminal write has no session-runtime cause: the owner
// reports its cause through the context it finishes with.
func TestOwnerFinishCarriesNoSessionRuntimeCause(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)
	observed := recordTerminals(f.manager)
	admission := admitRunning(t, f, "inv-owner-no-cause")

	if _, err := f.manager.FinishRunWithErrorCode(context.Background(), admission.Handle, RunStatusErrored, "agent.provider_overloaded"); err != nil {
		t.Fatal(err)
	}
	terminals := observed()
	if len(terminals) != 1 || terminals[0].Cause != nil || terminals[0].ErrorCode != "agent.provider_overloaded" {
		t.Fatalf("terminal observations = %+v, want the code and no cause", terminals)
	}
}

// Shutdown ends a running run as lost and records no message; the shutdown is
// named to the result record only.
func TestShutdownHandsItsCauseToTheTerminalObserver(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)
	observed := recordTerminals(f.manager)
	admission := admitRunning(t, f, "inv-shutdown-cause")

	if err := f.manager.CloseContext(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	terminals := observed()
	if len(terminals) != 1 || terminals[0].RunID != admission.RunID || terminals[0].Cause == nil ||
		terminals[0].Cause.Error() != "runtime owner shut down" {
		t.Fatalf("terminal observations = %+v, want the shutdown as cause", terminals)
	}
	run, err := f.runs.Get(context.Background(), admission.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != ledger.StateLost || run.ErrorMessage != "" {
		t.Fatalf("session_runs = state:%q message:%q, want lost and no message", run.State, run.ErrorMessage)
	}
}

// A stream error reaches the run view and its deltas as a code only. An event
// without a code is named runtime_run_failed, and its text is published
// nowhere a subscriber reads.
func TestRunViewPublishesStreamErrorsByCodeOnly(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)
	admission := admitRunning(t, f, "inv-view-code-only")
	const secret = "SECRET upstream said no"
	sub, err := f.manager.Subscribe(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close()

	handleEvent(t, f.manager, admission.Handle, native.StreamEvent{Type: native.EventError, Error: secret})

	view := currentRunView(t, f.manager)
	if view.ErrorCode != "runtime_run_failed" {
		t.Fatalf("run view = %+v, want runtime_run_failed", view)
	}
	snapshot, err := f.manager.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatal(err)
	}
	published, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range drainSubscription(sub) {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		published = append(published, data...)
	}
	if strings.Contains(string(published), "SECRET") || strings.Contains(string(published), `"error":`) {
		t.Fatalf("published run state carries error text: %s", published)
	}
}

func drainSubscription(sub Subscription) []Event {
	var events []Event
	for {
		select {
		case event, ok := <-sub.C:
			if !ok {
				return events
			}
			events = append(events, event)
		case <-time.After(50 * time.Millisecond):
			return events
		}
	}
}

// A run whose lease expired is shown lost under the code the reaper records
// for it, and a code the run already failed with is kept.
func TestExpiredLeaseNamesTheRunByCode(t *testing.T) {
	t.Parallel()
	manager := &Manager{}
	past := time.Now().Add(-time.Minute)
	for _, tc := range []struct {
		name, code, want string
	}{
		{name: "no code yet", want: runErrorOwnerLeaseExpired},
		{name: "already failed", code: "agent.provider_overloaded", want: "agent.provider_overloaded"},
	} {
		snapshot := Snapshot{CurrentRunView: &CurrentRunView{
			RunID: "run-1", Status: RunStatusRunning, OwnerLeaseExpiresAt: &past, ErrorCode: tc.code,
		}}
		if !manager.markLostIfExpired(&snapshot, time.Now()) {
			t.Fatalf("%s: expired lease not marked lost", tc.name)
		}
		if got := snapshot.CurrentRunView; got.Status != RunStatusLost || got.ErrorCode != tc.want {
			t.Fatalf("%s: run = %s/%s, want lost/%s", tc.name, got.Status, got.ErrorCode, tc.want)
		}
	}
}
