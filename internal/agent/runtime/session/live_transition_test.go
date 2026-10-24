package sessionruntime

import (
	"context"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
)

// liveRunStates are the live statuses an owner can drive a run into through
// the manager's public surface. admitting is observed inside the admission
// builder, and lost is written only by the reaper's repair paths, so neither is
// a source state here.
var liveRunStates = []string{
	RunStatusRunning, RunStatusWaitingDecision, RunStatusAborting, RunStatusFinishing,
	RunStatusCompleted, RunStatusAborted, RunStatusErrored,
}

type liveTrigger struct {
	name  string
	edge  string
	apply func(ctx context.Context, f admitFixture, admission Admission)
	// legal maps each source status to the live status and durable state the
	// trigger produces. A source missing here must keep both.
	legal map[string]liveTransitionResult
}

type liveTransitionResult struct {
	status string
	state  ledger.State
}

// The live run state machine on the memory backend, driven only through the
// manager's public surface. Each trigger lists every source it may move; from
// any other source it must leave the live status and the durable state as they
// were. Terminal statuses accept nothing.
func TestLiveRunStateTransitionTable(t *testing.T) {
	t.Parallel()
	result := func(status string, state ledger.State) liveTransitionResult {
		return liveTransitionResult{status: status, state: state}
	}
	event := func(ev native.StreamEvent) func(context.Context, admitFixture, Admission) {
		return func(ctx context.Context, f admitFixture, admission Admission) {
			_, _ = f.manager.HandleAgentEvent(ctx, admission.Handle, ev)
		}
	}
	finish := func(status string) func(context.Context, admitFixture, Admission) {
		return func(ctx context.Context, f admitFixture, admission Admission) {
			_, _ = f.manager.FinishRun(ctx, admission.Handle, status)
		}
	}
	triggers := []liveTrigger{
		{
			name: "pending ask_user", edge: "L3",
			apply: event(pendingUserInputEvent("input-transition")),
			legal: map[string]liveTransitionResult{
				RunStatusRunning:         result(RunStatusWaitingDecision, ledger.StateWaitingDecision),
				RunStatusWaitingDecision: result(RunStatusWaitingDecision, ledger.StateWaitingDecision),
				// A request racing the abort parks the durable row, which the
				// recorded abort still finishes, but the live run stays
				// aborting so the finish reads the abort rather than a park.
				RunStatusAborting: result(RunStatusAborting, ledger.StateWaitingDecision),
			},
		},
		{
			name: "agent_start", edge: "L3r",
			apply: event(native.StreamEvent{Type: native.EventAgentStart}),
			legal: map[string]liveTransitionResult{
				RunStatusWaitingDecision: result(RunStatusRunning, ledger.StateRunning),
			},
		},
		{
			name: "abort", edge: "L4",
			apply: func(ctx context.Context, f admitFixture, admission Admission) {
				_, _ = f.manager.AbortControl(ctx, testBotID, testSessionID, admission.RunID, "control-transition")
			},
			legal: map[string]liveTransitionResult{
				RunStatusRunning: result(RunStatusAborting, ledger.StateRunning),
				// A parked run has no producer left to stop, so the abort
				// finishes it on the spot.
				RunStatusWaitingDecision: result(RunStatusAborted, ledger.StateAborted),
			},
		},
		{
			name: "agent_end", edge: "L5",
			apply: event(native.StreamEvent{Type: native.EventAgentEnd}),
			legal: map[string]liveTransitionResult{
				RunStatusRunning:  result(RunStatusFinishing, ledger.StateFinishing),
				RunStatusAborting: result(RunStatusFinishing, ledger.StateFinishing),
			},
		},
		{
			name: "unnamed finish", edge: "L6",
			apply: finish(""),
			legal: map[string]liveTransitionResult{
				RunStatusRunning:   result(RunStatusCompleted, ledger.StateCompleted),
				RunStatusAborting:  result(RunStatusAborted, ledger.StateAborted),
				RunStatusFinishing: result(RunStatusCompleted, ledger.StateCompleted),
			},
		},
		{
			name: "errored finish", edge: "L6",
			apply: finish(RunStatusErrored),
			legal: map[string]liveTransitionResult{
				RunStatusRunning:         result(RunStatusErrored, ledger.StateFailed),
				RunStatusWaitingDecision: result(RunStatusErrored, ledger.StateFailed),
				// A durable abort intent outranks the owner's own outcome.
				RunStatusAborting: result(RunStatusAborted, ledger.StateAborted),
				// The proposal already on record is the outcome.
				RunStatusFinishing: result(RunStatusCompleted, ledger.StateCompleted),
			},
		},
	}

	for _, trigger := range triggers {
		for _, from := range liveRunStates {
			want, legal := trigger.legal[from]
			t.Run(trigger.edge+" "+trigger.name+" from "+from, func(t *testing.T) {
				t.Parallel()
				ctx := context.Background()
				f := newAdmitFixture(t)
				admission := driveLiveRunTo(ctx, t, f, from)
				before := f.runs.State(admission.RunID)

				trigger.apply(ctx, f, admission)

				gotStatus := liveRunStatusOf(ctx, t, f)
				gotState := f.runs.State(admission.RunID)
				if !legal {
					if gotStatus != from || gotState != before {
						t.Fatalf("%s from %s moved the run to live %q / durable %q, want %q / %q unchanged",
							trigger.name, from, gotStatus, gotState, from, before)
					}
					return
				}
				if gotStatus != want.status || gotState != want.state {
					t.Fatalf("%s from %s = live %q / durable %q, want %q / %q",
						trigger.name, from, gotStatus, gotState, want.status, want.state)
				}
			})
		}
	}
}

// Admission passes through admitting before it is running, and that status is
// what a concurrent observer sees while the admission builder runs.
func TestLiveRunIsAdmittingWhileAdmissionBuilds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newAdmitFixture(t)
	in := f.input("inv-admitting", `{"text":"a"}`)
	var observed string
	in.Execution.Admission = func(ctx context.Context, _ RunHandle) (RunAdmissionView, error) {
		snapshot, err := f.manager.Snapshot(ctx, testBotID, testSessionID)
		if err == nil && snapshot.CurrentRunView != nil {
			observed = snapshot.CurrentRunView.Status
		}
		return RunAdmissionView{}, nil
	}
	if _, err := f.manager.Admit(ctx, in); err != nil {
		t.Fatalf("admit: %v", err)
	}
	if observed != RunStatusAdmitting {
		t.Fatalf("status during admission = %q, want admitting", observed)
	}
	if got := liveRunStatusOf(ctx, t, f); got != RunStatusRunning {
		t.Fatalf("status after admission = %q, want running", got)
	}
}

func driveLiveRunTo(ctx context.Context, t *testing.T, f admitFixture, status string) Admission {
	t.Helper()
	admission, err := f.manager.Admit(ctx, f.input("inv-transition", `{"text":"a"}`))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	handle := admission.Handle
	must := func(_ any, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("drive run to %s: %v", status, err)
		}
	}
	switch status {
	case RunStatusRunning:
	case RunStatusWaitingDecision:
		must(f.manager.HandleAgentEvent(ctx, handle, pendingUserInputEvent("input-source")))
	case RunStatusAborting:
		must(f.manager.AbortControl(ctx, testBotID, testSessionID, admission.RunID, "control-source"))
	case RunStatusFinishing:
		must(f.manager.HandleAgentEvent(ctx, handle, native.StreamEvent{Type: native.EventAgentEnd}))
	case RunStatusCompleted:
		must(f.manager.FinishRun(ctx, handle, RunStatusCompleted))
	case RunStatusErrored:
		must(f.manager.FinishRun(ctx, handle, RunStatusErrored))
	case RunStatusAborted:
		must(f.manager.AbortControl(ctx, testBotID, testSessionID, admission.RunID, "control-source"))
		must(f.manager.FinishRun(ctx, handle, ""))
	default:
		t.Fatalf("no route to live status %q", status)
	}
	if got := liveRunStatusOf(ctx, t, f); got != status {
		t.Fatalf("drove run to %q, want %q", got, status)
	}
	return admission
}

func pendingUserInputEvent(id string) native.StreamEvent {
	return native.StreamEvent{
		Type: native.EventUserInputRequest, ToolName: "ask_user", ToolCallID: "call-" + id,
		UserInputID: id, Status: "pending",
	}
}

func liveRunStatusOf(ctx context.Context, t *testing.T, f admitFixture) string {
	t.Helper()
	snapshot, err := f.manager.Snapshot(ctx, testBotID, testSessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.CurrentRunView == nil {
		return ""
	}
	return snapshot.CurrentRunView.Status
}
