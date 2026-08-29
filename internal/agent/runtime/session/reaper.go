package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
)

// Error codes written to session_runs.error_code by the reaper. They are the
// durable record of why a run stopped, so they must stay stable: an operator
// reading the ledger months later needs to tell "the owner vanished" apart from
// "the live backend was replaced".
const (
	runErrorOwnerLeaseExpired = "runtime_owner_lease_expired"
	runErrorBackendLost       = "runtime_live_backend_lost"
	runErrorAdmissionOrphaned = "runtime_admission_orphaned"
)

// Reaper is the single cluster-wide janitor for the session run ledger. Exactly
// one instance acts at a time, elected through a TTL lease in the live backend.
//
// One leader rather than many workers is a deliberate simplification. The three
// duties below are all idempotent fenced transitions, so the only thing extra
// reapers would add is duplicate PostgreSQL traffic and a second way for a
// candidate to be consumed and then dropped by a crash. Failover costs at most
// one lease period and repeats at most one transition, which changes nothing.
//
// The reaper is the only component that can decide a run is unrecoverable,
// because it is the only one that reads liveness and durable state together.
type Reaper struct {
	runs                   ledger.Store
	liveness               LivenessBackend
	tuning                 tuning
	ownerID                string
	logger                 *slog.Logger
	recoverWaitingDecision func(context.Context, LeaseCandidate) (bool, error)
	terminalObserver       func(context.Context, TerminalRun)
	terminalReconciler     func(context.Context) error

	// generation is the liveness incarnation last observed. Runs stamped with
	// anything else were claimed by a backend that no longer exists.
	generation string
	// generationObservedAt starts the fail-closed grace, measured from when this
	// incarnation was first seen rather than from process start. Sweeping
	// immediately after a replacement would condemn runs whose owners are merely
	// reconnecting.
	generationObservedAt time.Time
	// sweepCursor carries recovery progress across ticks, so a long outage is
	// worked through in bounded slices instead of restarted every tick. Only the
	// reaper goroutine touches it.
	sweepCursor ledger.Cursor

	startOnce sync.Once
	stop      context.CancelFunc
	done      chan struct{}
}

// SetWaitingDecisionRecoverer installs the owner-local half of parked-run
// recovery. It is set before Start, so the reaper never observes a partially
// configured callback.
func (r *Reaper) SetWaitingDecisionRecoverer(recoverer func(context.Context, LeaseCandidate) (bool, error)) {
	if r == nil {
		return
	}
	r.recoverWaitingDecision = recoverer
}

// SetTerminalObserver installs the sink for authoritative durable outcomes.
func (r *Reaper) SetTerminalObserver(observer func(context.Context, TerminalRun)) {
	if r == nil {
		return
	}
	r.terminalObserver = observer
}

// SetTerminalReconciler installs the bounded repair pass run by the elected
// reaper after its ordinary ledger duties.
func (r *Reaper) SetTerminalReconciler(reconciler func(context.Context) error) {
	if r == nil {
		return
	}
	r.terminalReconciler = reconciler
}

// NewReaper builds a reaper for one manager. It shares the manager's derived
// tuning so the leader lease and the tick that renews it cannot drift apart.
func NewReaper(runs ledger.Store, liveness LivenessBackend, tune tuning, ownerID string, logger *slog.Logger) *Reaper {
	if logger == nil {
		logger = slog.Default()
	}
	return &Reaper{
		runs:     runs,
		liveness: liveness,
		tuning:   tune,
		ownerID:  strings.TrimSpace(ownerID),
		logger:   logger.With(slog.String("component", "session_runtime_reaper")),
		done:     make(chan struct{}),
	}
}

// Start seeds the liveness generation and begins ticking. Reading it here also
// proves the backend is reachable before the reaper claims it can decide runs
// unrecoverable; the sweep re-reads it every pass from then on.
func (r *Reaper) Start(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if r.runs == nil {
		return ErrLedgerUnavailable
	}
	if r.liveness == nil {
		return errors.New("session runtime reaper requires a liveness backend")
	}
	generation, err := r.liveness.LivenessGeneration(ctx)
	if err != nil {
		return fmt.Errorf("read session runtime liveness generation: %w", err)
	}
	if strings.TrimSpace(generation) == "" {
		return errors.New("session runtime liveness generation is empty")
	}
	r.generation = generation
	r.generationObservedAt = time.Now()

	// Starting twice is a no-op rather than an error: Start is a lifecycle hook
	// and a second call means the same reaper is already doing the job asked of
	// it.
	r.startOnce.Do(func() {
		runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		r.stop = cancel
		go r.loop(runCtx)
	})
	return nil
}

// Close stops ticking and hands leadership back so a peer takes over in one
// tick instead of waiting out the lease.
func (r *Reaper) Close(ctx context.Context) error {
	if r == nil || r.stop == nil {
		return nil
	}
	r.stop()
	select {
	case <-r.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return r.liveness.ReleaseLeaderLease(context.WithoutCancel(ctx), r.ownerID)
}

func (r *Reaper) loop(ctx context.Context) {
	defer close(r.done)
	ticker := time.NewTicker(r.tuning.reaperTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.tick(ctx)
		}
	}
}

// tick performs one leader pass. Acquiring the lease is also renewing it, so a
// steady leader keeps its lease by doing its job; a leader that stops ticking
// loses it without any extra bookkeeping.
func (r *Reaper) tick(ctx context.Context) {
	leader, err := r.liveness.AcquireLeaderLease(ctx, r.ownerID, r.tuning.reaperLeaderLeaseTTL)
	if err != nil {
		r.logger.Warn("acquire session runtime reaper leadership failed", slog.Any("error", err))
		return
	}
	if !leader {
		return
	}
	if err := r.reapExpiredLeases(ctx); err != nil {
		r.logger.Warn("reap expired session runtime leases failed", slog.Any("error", err))
	}
	if err := r.recoverLostBackendGeneration(ctx); err != nil {
		r.logger.Warn("recover session runtime runs after backend loss failed", slog.Any("error", err))
	}
	if err := r.repairOrphanedAdmissions(ctx); err != nil {
		r.logger.Warn("repair orphaned session runtime admissions failed", slog.Any("error", err))
	}
	if r.terminalReconciler != nil {
		if err := r.terminalReconciler(context.WithoutCancel(ctx)); err != nil {
			r.logger.Warn("reconcile session runtime terminal observations failed", slog.Any("error", err))
		}
	}
}

// reapExpiredLeases handles the ordinary case: an owner stopped renewing. The
// live backend names the candidates on its own clock, so a reaper with a skewed
// clock cannot declare a healthy owner dead; this pass only decides what an
// expired lease means durably.
func (r *Reaper) reapExpiredLeases(ctx context.Context) error {
	candidates, err := r.liveness.ExpiredLeaseCandidates(ctx, int64(r.tuning.scanBatchSize))
	if err != nil {
		return fmt.Errorf("read expired session runtime lease candidates: %w", err)
	}
	var errs []error
	for _, candidate := range candidates {
		run, getErr := r.runs.Get(ctx, candidate.RunID)
		if getErr != nil && !errors.Is(getErr, ledger.ErrRunNotFound) {
			errs = append(errs, fmt.Errorf("load expired session runtime run: %w", getErr))
			continue
		}
		if getErr == nil && run.State == ledger.StateWaitingDecision && r.recoverWaitingDecision != nil {
			recovered, recoverErr := r.recoverWaitingDecision(ctx, candidate)
			if recoverErr != nil {
				errs = append(errs, fmt.Errorf("recover waiting-decision runtime run: %w", recoverErr))
				continue
			}
			if recovered {
				if _, err := r.liveness.ReleaseLeaseCandidate(ctx, candidate); err != nil {
					errs = append(errs, fmt.Errorf("release reclaimed session runtime lease candidate: %w", err))
				}
				continue
			}
		}
		if err := r.markLost(ctx, candidate.RunID, candidate.FencingToken, runErrorOwnerLeaseExpired); err != nil {
			errs = append(errs, err)
			// Keep the index entry so the next tick retries. Releasing an entry
			// whose durable transition failed would strand the run active with
			// no lease left to rediscover it.
			continue
		}
		// Released only after the durable transition, and only while the entry
		// still carries the token it was read with: a run reclaimed in between
		// keeps its new entry.
		if _, err := r.liveness.ReleaseLeaseCandidate(ctx, candidate); err != nil {
			errs = append(errs, fmt.Errorf("release session runtime lease candidate: %w", err))
		}
	}
	return errors.Join(errs...)
}

// recoverLostBackendGeneration is the fail-closed sweep. When the live backend
// is replaced, in-flight streamed text is gone; there is no checkpoint to resume
// from, by design. What survives is the admission and whatever history already
// reached bot_history_messages, so the honest durable outcome is `lost` rather
// than a pretence of recovery. A new submission starts a new run under the new
// generation; nothing is re-reserved.
//
// The sweep is bounded and batched because the number of stranded rows is
// proportional to traffic during the outage: one unbounded scan would turn
// recovery into a second incident.
func (r *Reaper) recoverLostBackendGeneration(ctx context.Context) error {
	if err := r.refreshGeneration(ctx); err != nil {
		// Comparing against an incarnation this pass could not confirm is how a
		// recovery sweep becomes the incident, so it declines to run.
		return err
	}
	// A brief blip must not condemn owners that are merely reconnecting, so the
	// sweep waits out the configured restart budget first.
	if time.Since(r.generationObservedAt) < r.tuning.backendLossGrace {
		return nil
	}
	var errs []error
	for range r.tuning.maxScanBatchesPerTick {
		runs, err := r.runs.StaleGenerationRuns(ctx, ledger.StaleGenerationQuery{
			CurrentGeneration: r.generation,
			After:             r.sweepCursor,
			Limit:             r.tuning.scanBatchSize,
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("list stale generation session runs: %w", err))
			break
		}
		for _, run := range runs {
			if err := r.markLost(ctx, run.RunID, run.FencingToken, runErrorBackendLost); err != nil {
				errs = append(errs, err)
			}
		}
		if len(runs) < int(r.tuning.scanBatchSize) {
			// Caught up. The next tick restarts from the beginning, because a
			// peer still holding a cached generation can add a row behind this
			// cursor.
			r.sweepCursor = ledger.Cursor{}
			break
		}
		last := runs[len(runs)-1]
		// The cursor advances past a failed transition too. live_generation is
		// stamped once and never updated, so the row keeps its keyset position
		// and a restarted sweep reaches it again.
		r.sweepCursor = ledger.Cursor{LiveGeneration: last.LiveGeneration, RunID: last.RunID}
	}
	return errors.Join(errs...)
}

// refreshGeneration re-reads the live incarnation before each sweep.
//
// Reading it once at start is wrong in exactly the case the sweep exists for.
// When a running backend is replaced, the runs stranded by that replacement
// carry the generation this process booted with, while the runs admitted
// afterwards carry the new one — so a frozen comparison excludes every casualty
// and matches every survivor. The sweep does not merely miss; it inverts.
//
// A change also restarts the grace, because the restart budget is measured from
// the replacement rather than from this process's boot: owners reconnecting to a
// new backend need that budget just as much as owners reconnecting after one.
// The cursor resets for the same reason — it keys on the generation column, so a
// position taken under the previous incarnation says nothing about this one.
func (r *Reaper) refreshGeneration(ctx context.Context) error {
	generation, err := r.liveness.LivenessGeneration(ctx)
	if err != nil {
		return fmt.Errorf("read session runtime liveness generation: %w", err)
	}
	generation = strings.TrimSpace(generation)
	if generation == "" {
		return errors.New("session runtime liveness generation is empty")
	}
	if generation == r.generation {
		return nil
	}
	r.logger.Info("session runtime liveness generation changed",
		slog.String("previous_generation", r.generation),
		slog.String("current_generation", generation),
	)
	r.generation = generation
	r.generationObservedAt = time.Now()
	r.sweepCursor = ledger.Cursor{}
	return nil
}

// repairOrphanedAdmissions covers the one gap the other two duties cannot see: a
// process that committed an admission and died before reserving the run in the
// live backend. No lease was ever written, so the run never becomes an expiry
// candidate, and no generation was ever stamped, so the sweep skips it. Without
// this pass such a run would hold its session's active slot forever.
func (r *Reaper) repairOrphanedAdmissions(ctx context.Context) error {
	runs, err := r.runs.OrphanedRuns(ctx, ledger.OrphanQuery{
		MinAge: r.tuning.orphanGrace,
		Limit:  r.tuning.scanBatchSize,
	})
	if err != nil {
		return fmt.Errorf("list orphaned session runs: %w", err)
	}
	var errs []error
	for _, run := range runs {
		// FencingToken is 0 on a never-claimed row, which the fenced statement
		// accepts because it matches what is stored.
		if err := r.markLost(ctx, run.RunID, run.FencingToken, runErrorAdmissionOrphaned); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// markLost applies the fenced terminal transition, and that is the whole
// operation: the transition frees the session's active slot, and because
// admission answers busy instead of queueing, there is nothing waiting to
// promote. The next submission simply succeeds.
//
// The transition is idempotent, so a reaper that dies mid-pass or fails over
// repeats at most one no-op write — which is what makes leader failover boring.
func (r *Reaper) markLost(ctx context.Context, runID string, fencingToken int64, errorCode string) error {
	run, applied, err := r.runs.Finalize(ctx, ledger.FinalizeParams{
		RunID:        runID,
		FencingToken: fencingToken,
		State:        ledger.StateLost,
		ErrorCode:    errorCode,
	})
	if err != nil {
		return fmt.Errorf("mark session run lost: %w", err)
	}
	if !applied {
		// A prior owner may have committed its terminal row and died before
		// publishing the application-owned terminal observation. Replay that
		// authoritative outcome, but never speak for a newer active owner.
		run, err = r.runs.Get(ctx, runID)
		if err != nil {
			if errors.Is(err, ledger.ErrRunNotFound) {
				return nil
			}
			return fmt.Errorf("load authoritative reaped run terminal: %w", err)
		}
		if !run.State.Terminal() {
			return nil
		}
	}
	if r.terminalObserver != nil {
		r.terminalObserver(context.WithoutCancel(ctx), terminalRunFromLedger(run))
	}
	if !applied {
		return nil
	}
	if run.State == ledger.StateAborted {
		r.logger.Info("session run finalized after abort intent",
			slog.String("run_id", run.RunID),
			slog.String("session_id", run.SessionID),
		)
		return nil
	}
	r.logger.Info("session run marked lost",
		slog.String("run_id", run.RunID),
		slog.String("session_id", run.SessionID),
		slog.String("error_code", errorCode),
	)
	return nil
}
