package sessionruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	"github.com/felinics/memoh/internal/agent/turn"
)

var (
	// ErrSessionBusy means the session already has an active run and this is a
	// different invocation. SR-OWN-001 offers two ways to honour single-session
	// execution — durable queueing or a stable retryable busy answer — and this
	// runtime answers busy. It is an alias rather than a second sentinel so that
	// one errors.Is works whether the caller holds the ledger error or this one.
	//
	// The consequence is deliberate: the session runtime is not a general task
	// queue. Channel delivery and schedules already own a retry
	// mechanism, and reusing those is strictly better than teaching the runtime
	// to hold work for them, which would cost a queued state, a queue index,
	// promotion on every terminal transition, queued aborts, and a queue
	// projection on the client.
	ErrSessionBusy = ledger.ErrSessionBusy

	// ErrInvocationConflict means one invocation_id was submitted twice with
	// different canonical input. Per SR-ADM-001 that is a stable, identifiable
	// conflict rather than a new run: silently admitting it would give the
	// caller two runs for one retry.
	//
	// Both sentinels reach clients as apperror catalog codes — session_busy is
	// retryable, invocation_conflict is not. Domain code keeps the sentinel;
	// the HTTP boundary owns that mapping.
	ErrInvocationConflict = errors.New("runtime invocation conflicts with an earlier submission")

	// ErrLedgerUnavailable means durable admission was requested on a manager
	// built without a ledger. Admission is durable-first by definition, so
	// there is no in-memory fallback to degrade to.
	ErrLedgerUnavailable = errors.New("session runtime ledger is not configured")

	// ErrFenceUnavailable means a ledger was configured without the persistence
	// fence that claims depend on. Claiming without it would hand a run
	// ownership that PostgreSQL does not enforce, so the superseded owner would
	// keep writing history — the failure SR-OWN-002 exists to rule out.
	ErrFenceUnavailable = errors.New("session runtime persistence fence is not configured")

	// ErrSessionRuntimeSplit means the deployment declared more than one
	// instance but the live backend cannot arbitrate ownership between them.
	ErrSessionRuntimeSplit = errors.New("session runtime cluster mode requires a distributed backend")
)

// AdmitInput is one submission from a public entry point: HTTP, a channel
// adapter or a schedule. Every caller supplies the same three
// identities, which is what lets one admission path serve all of them.
type AdmitInput struct {
	BotID     string
	SessionID string
	// InvocationID is the caller's retry identity. Callers that cannot mint one
	// must derive it deterministically from their source (for example a channel
	// message id), because a fresh id per attempt turns a retry into a second
	// run.
	InvocationID string
	// Payload is the canonically encoded submission. Its fingerprint decides
	// whether a repeated invocation_id is the same submit or a conflict, so the
	// encoding must be stable for equal inputs — same key order, no timestamps,
	// no request ids.
	Payload []byte
	// Execution is the owner-side plumbing used only if this call wins the
	// claim. A replay that finds the run already owned never touches it.
	Execution Execution
}

// Execution is what the winner of a claim needs in order to actually run the
// turn. It is part of the admit call rather than a second step because the two
// cannot be separated safely: live state reserved without an executor is a run
// holding a session's only slot with nothing driving it to a terminal state.
type Execution struct {
	// Admission persists the run's user turn and any replacement operation, and
	// returns the view subscribers should see when the run leaves admitting. It
	// runs after ownership is established, so its writes can be fenced with
	// RunHandle.FencingToken.
	Admission func(ctx context.Context, handle RunHandle) (RunAdmissionView, error)
	// AbortCh and Cancel are how a routed abort reaches this run. A run without
	// them is durably abortable but cannot be interrupted mid-turn.
	AbortCh chan<- struct{}
	Cancel  context.CancelFunc
	// InjectCh receives steering messages; nil means this caller does not
	// support steering. The caller owns channel closure; the runtime only stops
	// sending during teardown.
	InjectCh chan<- turn.InjectMessage
	// OwnershipCancel revokes the caller's execution context when the lease is
	// lost, so a superseded owner stops producing output rather than racing the
	// fence it can no longer pass.
	OwnershipCancel context.CancelCauseFunc
}

func (in AdmitInput) normalized() AdmitInput {
	in.BotID = strings.TrimSpace(in.BotID)
	in.SessionID = strings.TrimSpace(in.SessionID)
	in.InvocationID = strings.TrimSpace(in.InvocationID)
	return in
}

func (in AdmitInput) validate() error {
	in = in.normalized()
	switch {
	case in.BotID == "":
		return errors.New("bot_id is required")
	case in.SessionID == "":
		return errors.New("session_id is required")
	case in.InvocationID == "":
		return errors.New("invocation_id is required")
	case len(in.Payload) == 0:
		return errors.New("payload is required")
	case in.Execution.Admission == nil:
		return errors.New("execution admission builder is required")
	default:
		return nil
	}
}

// Admission is the durable answer to one submission. It is safe to return to a
// caller as confirmation to stop retrying: everything in it is committed before
// the call returns.
type Admission struct {
	RunID string
	// TurnID and TurnPosition are decided at admission so the eventual terminal
	// write targets a turn that already exists on paper.
	TurnID       string
	TurnPosition int64
	State        ledger.State
	// Replay is true when this invocation was already admitted. The rest of the
	// struct then describes the original run, whatever state it has reached.
	Replay bool
	// Started is true when this call took ownership and began execution. It is
	// false for a replay of a run someone else owns and for a replay of a
	// finished run.
	Started bool
	// Cursor is the position at which this run became observable. A caller that
	// both starts runs and subscribes to the session uses it to place the run in
	// the stream it is already reading, rather than guessing whether a snapshot
	// it holds predates its own submission (SR-OBS-002).
	//
	// It is set only when Started. A replay reserved nothing, so this call
	// produced no position; where the session stands now is a different fact,
	// and the subscription's snapshot is the authoritative answer to it.
	Cursor Cursor
	// Handle names the run this process now owns, including the fencing token
	// its durable writes must carry. It is meaningful only when Started.
	Handle RunHandle
}

// Admit is the one durable entry point for starting a session run. It commits
// the admission before returning, so a caller that receives an Admission can
// stop retrying even if this server dies immediately afterwards (SR-ADM-001,
// SR-DUR-001).
//
// The order is deliberate and not reorderable: persist, then claim, then
// execute. Reserving live state first would create a run that exists only in a
// process, which is the failure the ledger exists to prevent.
//
// A caller that gets Started sees a run that is owned, fenced and reserved, and
// receives the handle its own durable writes must be fenced with. A caller that
// gets an Admission without Started is looking at a run someone else owns or one
// that already finished, and must not execute anything.
//
// A session runs one turn at a time, so a different invocation arriving while
// one is active gets ErrSessionBusy and is expected to retry (SR-OWN-001). The
// runtime holds nothing on the caller's behalf.
func (m *Manager) Admit(ctx context.Context, in AdmitInput) (Admission, error) {
	if m == nil || m.backend == nil {
		return Admission{}, ErrManagerClosed
	}
	if m.isClosed() {
		return Admission{}, ErrManagerClosed
	}
	if err := in.validate(); err != nil {
		return Admission{}, err
	}
	in = in.normalized()
	if m.runs == nil {
		return Admission{}, ErrLedgerUnavailable
	}
	if m.cluster && m.distributed == nil {
		return Admission{}, ErrSessionRuntimeSplit
	}
	if err := m.waitForHistoryReset(ctx, ResetScope{BotID: in.BotID, SessionID: in.SessionID}); err != nil {
		return Admission{}, err
	}

	fingerprint := fingerprintPayload(in.Payload)
	run, created, err := m.runs.Admit(ctx, ledger.AdmitParams{
		RunID:            uuid.NewString(),
		BotID:            in.BotID,
		SessionID:        in.SessionID,
		InvocationID:     in.InvocationID,
		TurnID:           uuid.NewString(),
		Input:            in.Payload,
		InputFingerprint: fingerprint,
	})
	switch {
	case errors.Is(err, ledger.ErrHistoryResetInProgress):
		return Admission{}, ErrHistoryResetInProgress
	case errors.Is(err, ErrSessionBusy):
		// Nothing was persisted, so this is the caller's cue to retry later
		// rather than an outcome to record. Returning the sentinel unwrapped
		// keeps that decision a single errors.Is at every entry point.
		return Admission{}, ErrSessionBusy
	case err != nil:
		return Admission{}, fmt.Errorf("admit runtime run: %w", err)
	}
	if !created && run.InputFingerprint != fingerprint {
		return Admission{}, ErrInvocationConflict
	}

	admission := Admission{
		RunID:        run.RunID,
		TurnID:       run.TurnID,
		TurnPosition: run.TurnPosition,
		State:        run.State,
		Replay:       !created,
	}

	if run.State != ledger.StateAccepted || run.OwnerID != "" {
		// A replay of a run that someone (possibly this process on an earlier
		// attempt) already owns, or one that already finished. Either way the
		// answer is the committed state, not a second execution.
		return admission, nil
	}

	// An accepted run with no owner is claimable, and that covers two cases with
	// one path: the admission this call just wrote, and an earlier admission
	// whose owner died before reserving anything. A retry of the same invocation
	// therefore adopts its own orphan instead of waiting for the reaper.
	return m.claimAndStart(ctx, in, admission)
}

func (m *Manager) waitForHistoryReset(ctx context.Context, scope ResetScope) error {
	backend, ok := m.backend.(HistoryResetBackend)
	if !ok {
		return nil
	}
	for {
		_, active, err := backend.EffectiveHistoryReset(ctx, scope)
		if err != nil {
			return fmt.Errorf("check live history reset fence: %w", err)
		}
		if resetStore, ok := m.runs.(ledger.ResetStore); ok {
			_, durableActive, durableErr := resetStore.EffectiveReset(ctx, scope.BotID, scope.SessionID)
			if durableErr != nil {
				return fmt.Errorf("check PostgreSQL history reset fence: %w", durableErr)
			}
			active = active || durableActive
		}
		if !active {
			return nil
		}
		if err := waitHistoryResetRetry(ctx, m.ownerLeaseTTL); err != nil {
			return err
		}
	}
}

// claimAndStart turns an accepted row into a running, owned, executing run.
//
// The four steps are one sequence with one direction: draw a token, take durable
// ownership with it, promote it to the persistence fence, then reserve live
// state. Each step is only safe after the previous one. Drawing before claiming
// keeps tokens monotonic across contenders; claiming before activating is what
// stops a loser from fencing out the legitimate owner, which is the failure mode
// that makes the reverse order unsafe rather than merely wasteful; activating
// before reserving means that by the time anything is observable, the writer
// that can persist this run is the same one that owns it.
func (m *Manager) claimAndStart(ctx context.Context, in AdmitInput, admission Admission) (Admission, error) {
	if m.fence == nil {
		return Admission{}, ErrFenceUnavailable
	}
	token, err := m.runs.NextFencingToken(ctx)
	if err != nil {
		return Admission{}, fmt.Errorf("draw runtime fencing token: %w", err)
	}
	generation, err := m.livenessGeneration(ctx)
	if err != nil {
		return Admission{}, err
	}
	claimed, applied, err := m.runs.Claim(ctx, ledger.ClaimParams{
		RunID:          admission.RunID,
		OwnerID:        m.ownerID,
		FencingToken:   token,
		LiveGeneration: generation,
	})
	if errors.Is(err, ledger.ErrHistoryResetInProgress) {
		return Admission{}, ErrHistoryResetInProgress
	}
	if err != nil {
		return Admission{}, fmt.Errorf("claim runtime run: %w", err)
	}
	if !applied {
		// A peer claimed first. The run is progressing under an owner that is
		// not this process, which is an answer rather than a failure: the
		// caller already has its durable admission and must not start a second
		// execution of it.
		current, err := m.runs.Get(ctx, admission.RunID)
		if err != nil {
			return Admission{}, fmt.Errorf("read claimed runtime run: %w", err)
		}
		admission.State = current.State
		return admission, nil
	}
	admission.State = claimed.State

	if err := m.fence.Activate(ctx, in.BotID, in.SessionID, token); err != nil {
		// Ownership is this process's, so nothing else is harmed by ending the
		// run here; leaving it running would strand the session's only slot
		// until the reaper's grace period elapsed.
		return Admission{}, m.abandonClaim(ctx, admission.RunID, token, "runtime_fence_activation_failed", fmt.Errorf("activate runtime persistence fence: %w", err))
	}

	handle, cursor, err := m.startRun(ctx, runStart{
		botID:           in.BotID,
		sessionID:       in.SessionID,
		runID:           admission.RunID,
		turnID:          admission.TurnID,
		invocationID:    in.InvocationID,
		fencingToken:    token,
		builder:         in.Execution.Admission,
		ownershipCancel: in.Execution.OwnershipCancel,
		abortCh:         in.Execution.AbortCh,
		cancel:          in.Execution.Cancel,
		injectCh:        in.Execution.InjectCh,
	})
	switch {
	case err != nil:
		return Admission{}, m.abandonClaim(ctx, admission.RunID, token, "runtime_reservation_failed", err)
	case !handle.valid():
		// The live backend declined without an error, which means the session
		// already holds an active reservation this process cannot displace.
		return Admission{}, m.abandonClaim(ctx, admission.RunID, token, "runtime_reservation_declined", ErrRunOwnershipLost)
	}

	admission.Started = true
	admission.Handle = handle
	admission.Cursor = cursor
	return admission, nil
}

// abandonClaim releases a run this process owns but cannot execute. The write is
// fenced by the same token the claim used, so it cannot disturb a successor, and
// it is idempotent, so a caller that crashes here leaves the run to the reaper
// with the same outcome. cause is returned unchanged: the reason the run could
// not start is more useful to the caller than the bookkeeping that followed.
func (m *Manager) abandonClaim(ctx context.Context, runID string, token int64, code string, cause error) error {
	_, _, err := m.runs.Finalize(context.WithoutCancel(ctx), ledger.FinalizeParams{
		RunID:        runID,
		FencingToken: token,
		State:        ledger.StateFailed,
		ErrorCode:    code,
		ErrorMessage: cause.Error(),
	})
	if err != nil {
		m.logger.Error("release unstartable runtime run failed; the reaper will finish it",
			slog.Any("error", err), slog.String("run_id", runID), slog.String("cause", cause.Error()))
	}
	return cause
}

// livenessGeneration reads the live backend incarnation stamped on a claim. A
// manager without a liveness backend cannot be recovered after a restart,
// because nothing would distinguish this incarnation from the dead one.
func (m *Manager) livenessGeneration(ctx context.Context) (string, error) {
	if m.liveness == nil {
		return "", errors.New("session runtime liveness backend is not configured")
	}
	generation, err := m.liveness.LivenessGeneration(ctx)
	if err != nil {
		return "", fmt.Errorf("read runtime liveness generation: %w", err)
	}
	if strings.TrimSpace(generation) == "" {
		return "", errors.New("session runtime liveness generation is empty")
	}
	return generation, nil
}

// fingerprintPayload hashes the canonical submission. A hash rather than the
// payload itself keeps the comparison cheap and bounded while still detecting
// any difference in the bytes the caller actually sent.
func fingerprintPayload(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
