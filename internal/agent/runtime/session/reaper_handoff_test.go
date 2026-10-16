package sessionruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
)

type handoffLiveness struct {
	*fakeLiveness
	ref RunRef
}

func TestReaperRetainsTerminalPointerUntilLiveRepairSucceeds(t *testing.T) {
	runs := newFakeLedger()
	runs.InsertClaimed("live-repair", testSessionID, 9, "live")
	_, _, _ = runs.SetWaitingDecision(t.Context(), "live-repair", 9)
	live := newFakeLiveness("live")
	live.setCandidates(LeaseCandidate{Key: Key{BotID: testBotID, SessionID: testSessionID}, RunID: "live-repair", FencingToken: 8, ExpiresAt: time.Now().Add(-time.Minute)})
	reaper := newTestReaperWithLiveness(t, runs, &handoffLiveness{fakeLiveness: live}, "live")
	failed := true
	reaper.SetTerminalLiveReconciler(func(context.Context, TerminalRun) error {
		if failed {
			return errors.New("redis unavailable")
		}
		return nil
	})
	var applied int
	reaper.SetTerminalObserver(func(_ context.Context, run TerminalRun) {
		if run.Applied {
			applied++
		}
	})
	reaper.tick(t.Context())
	if runs.State("live-repair") != ledger.StateLost || len(live.indexed()) != 1 || applied != 1 {
		t.Fatal("durable outcome or live repair retry pointer was lost")
	}
	failed = false
	reaper.tick(t.Context())
	if len(live.indexed()) != 0 || applied != 1 {
		t.Fatal("repair did not converge or durable outcome was applied twice")
	}
}

func (l *handoffLiveness) LoadRunRef(context.Context, Key, string) (RunRef, bool, error) {
	return l.ref, l.ref.RunID != "", nil
}

func TestReaperConsumesHandoffIndexOnlyAfterTerminalOrLiveSuccessor(t *testing.T) {
	for _, scenario := range []string{"expired", "live_successor", "renewed_owner", "no_liveness_reader"} {
		t.Run(scenario, func(t *testing.T) {
			runs := newFakeLedger()
			token := int64(9)
			if scenario == "renewed_owner" {
				token = 8
			}
			runs.InsertClaimed("handoff", testSessionID, token, "live")
			_, _, _ = runs.SetWaitingDecision(t.Context(), "handoff", token)
			live := newFakeLiveness("live")
			candidate := LeaseCandidate{Key: Key{BotID: testBotID, SessionID: testSessionID}, RunID: "handoff", FencingToken: 8, ExpiresAt: time.Now().Add(-time.Minute)}
			live.setCandidates(candidate)
			backend := &handoffLiveness{fakeLiveness: live}
			if scenario == "live_successor" || scenario == "renewed_owner" {
				backend.ref = RunRef{RunID: "handoff", FencingToken: token}
			}
			var liveness LivenessBackend = backend
			if scenario == "no_liveness_reader" {
				liveness = live
			}
			reaper := newTestReaperWithLiveness(t, runs, liveness, "live")
			reaper.SetWaitingDecisionRecoverer(func(context.Context, LeaseCandidate) (bool, error) { return false, nil })
			reaper.tick(t.Context())
			run, _ := runs.Get(t.Context(), "handoff")
			if scenario == "expired" {
				if run.State != ledger.StateLost || len(live.indexed()) != 0 {
					t.Fatalf("expired handoff stranded: run=%+v index=%+v", run, live.indexed())
				}
			} else if run.State != ledger.StateWaitingDecision {
				t.Fatalf("live or unverified successor was terminated: %+v", run)
			}
			wantIndex := scenario == "renewed_owner" || scenario == "no_liveness_reader"
			if (len(live.indexed()) != 0) != wantIndex {
				t.Fatalf("recovery pointer consumed incorrectly: %+v", live.indexed())
			}
		})
	}
}

type handoffCASLoser struct{ ledger.Store }

func (s handoffCASLoser) Finalize(ctx context.Context, p ledger.FinalizeParams) (ledger.Run, bool, error) {
	// A response/reclaim wins after the reaper's liveness read.
	run, err := s.Get(ctx, p.RunID)
	return run, false, err
}

func TestReaperRetainsIndexWhenTerminalCASDoesNotApply(t *testing.T) {
	runs := newFakeLedger()
	runs.InsertClaimed("race", testSessionID, 9, "live")
	_, _, _ = runs.SetWaitingDecision(t.Context(), "race", 9)
	live := newFakeLiveness("live")
	live.setCandidates(LeaseCandidate{Key: Key{BotID: testBotID, SessionID: testSessionID}, RunID: "race", FencingToken: 8, ExpiresAt: time.Now().Add(-time.Minute)})
	reaper := newTestReaperWithLiveness(t, runs, &handoffLiveness{fakeLiveness: live}, "live")
	reaper.runs = handoffCASLoser{Store: runs}
	reaper.tick(t.Context())
	if len(live.indexed()) != 1 {
		t.Fatal("active run lost its recovery pointer after unapplied CAS")
	}
}
