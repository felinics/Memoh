package sessionruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
)

// A history reset drops the run from the live projection as soon as the
// ledger holds it terminal, which can land between the owner's durable
// terminal write and its live release. The release then finds nothing to
// release, and the finish is still the run's outcome rather than a lost
// ownership.
func TestFinishRunAfterResetDroppedTheProjection(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)
	ctx := context.Background()
	admission, err := f.manager.Admit(ctx, f.input("inv-reset", `{"text":"hi"}`))
	if err != nil || !admission.Started {
		t.Fatalf("admit = (%+v, %v)", admission, err)
	}
	if _, err := f.manager.HandleAgentEvent(ctx, admission.Handle, native.StreamEvent{Type: native.EventTextDelta, Delta: "partial"}); err != nil {
		t.Fatalf("stream text: %v", err)
	}
	key := Key{BotID: testBotID, SessionID: testSessionID}
	if err := f.manager.invalidateHistoryResetSnapshots(ctx, []Key{key}, true); err != nil {
		t.Fatalf("drop projection: %v", err)
	}

	terminal, err := f.manager.FinishRun(ctx, admission.Handle, RunStatusAborted)
	if err != nil {
		t.Fatalf("FinishRun() after the projection was dropped = %v, want the run's outcome", err)
	}
	if terminal.RunID != admission.RunID {
		t.Fatalf("terminal run = %q, want %q", terminal.RunID, admission.RunID)
	}
	run, err := f.runs.Get(ctx, admission.RunID)
	if err != nil || run.State != ledger.StateAborted {
		t.Fatalf("ledger run = (%q, %v), want aborted", run.State, err)
	}
	if f.manager.localControl(admission.RunID) != nil {
		t.Fatal("finished run kept its local control")
	}
	snapshot, err := f.manager.Snapshot(ctx, testBotID, testSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CurrentRunView != nil && snapshot.CurrentRunView.Messages != nil {
		t.Fatalf("dropped run streamed back into the projection: %+v", snapshot.CurrentRunView)
	}
}

// The pass after the deletion restarts every projection so subscribers read
// the ledger again, but it never takes an active run off its owner.
func TestHistoryResetRestartKeepsActiveRun(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)
	ctx := context.Background()
	admission, err := f.manager.Admit(ctx, f.input("inv-active", `{"text":"hi"}`))
	if err != nil || !admission.Started {
		t.Fatalf("admit = (%+v, %v)", admission, err)
	}
	key := Key{BotID: testBotID, SessionID: testSessionID}
	before, _, err := f.manager.backend.Load(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.manager.invalidateHistoryResetSnapshots(ctx, []Key{key}, false); err != nil {
		t.Fatalf("restart projections: %v", err)
	}
	after, _, err := f.manager.backend.Load(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if after.CurrentRunView == nil || after.CurrentRunView.RunID != admission.RunID || after.Epoch != before.Epoch {
		t.Fatalf("active projection after restart = %+v (epoch %q), want it untouched (epoch %q)", after.CurrentRunView, after.Epoch, before.Epoch)
	}
	f.finish(t, admission)
}

// A bot-wide reset that cannot name the sessions it covers cannot clear their
// projections, so it fails before any deletion and gives its lease back.
func TestBeginBotHistoryResetFailsClosedWithoutSessionList(t *testing.T) {
	t.Parallel()
	runs := newFakeResetLedger()
	runs.botSessionsErr = errors.New("list failed")
	manager, backend := newResetTestManager(t, runs)

	if _, _, err := manager.BeginBotHistoryReset(context.Background(), "bot-1"); err == nil {
		t.Fatal("bot history reset began without its session list")
	}
	if released := runs.releasedLeases(); len(released) != 1 || released[0].Scope != ledger.ResetScopeBot {
		t.Fatalf("durable release = %#v, want the bot lease returned", released)
	}
	if _, blocked, err := backend.EffectiveHistoryReset(context.Background(), ResetScope{BotID: "bot-1"}); err != nil || blocked {
		t.Fatalf("live mirror after the failed reset = (%v, %v), want released", blocked, err)
	}
}
