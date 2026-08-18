// Package ledger is the durable half of the session runtime: the record of
// which runs were admitted, who owns them, and how they ended.
//
// It deliberately holds no liveness. The owner lease lives only in the live
// backend, so PostgreSQL receives exactly four kinds of write — admission,
// ownership/fencing change, decision, terminal transition. Nothing here is a
// keepalive or a progress sample, which is why write volume is proportional to
// the number of runs rather than to their duration or token rate. A reader
// therefore cannot ask this store "is the owner still alive"; only the live
// backend can answer that, and the reaper is built around that fact.
package ledger

import (
	"context"
	"errors"
	"time"
)

// State is a durable run state. The set matches section 3 of
// docs/design/session-runtime-requirements.md and the session_runs CHECK
// constraint; do not add a value to one without the other.
type State string

const (
	// StateAccepted means the input and admission result are persisted, so the
	// caller can stop retrying, but no owner has claimed the run yet.
	StateAccepted State = "accepted"
	// StateRunning means a valid owner is executing.
	StateRunning State = "running"
	// StateWaitingDecision means execution is parked on a decision_id.
	StateWaitingDecision State = "waiting_decision"

	StateCompleted State = "completed"
	StateAborted   State = "aborted"
	StateFailed    State = "failed"
	// StateLost means the owner disappeared and the accepted input was not
	// lost, but execution did not finish. It is not a success terminal.
	StateLost State = "lost"
)

// Active reports whether a run occupies its session's single active slot.
func (s State) Active() bool {
	switch s {
	case StateAccepted, StateRunning, StateWaitingDecision:
		return true
	default:
		return false
	}
}

// Terminal reports whether a run can no longer transition.
func (s State) Terminal() bool {
	switch s {
	case StateCompleted, StateAborted, StateFailed, StateLost:
		return true
	default:
		return false
	}
}

var (
	// ErrRunNotFound is returned when no row matches the requested identity.
	ErrRunNotFound = errors.New("ledger: session run not found")
	// ErrSessionNotFound is returned by Admit when the target session does not
	// exist or was deleted, which is distinguishable from a duplicate
	// invocation because a duplicate still resolves to a row.
	ErrSessionNotFound = errors.New("ledger: session not found")
	// ErrSessionBusy means the session already has an active run and this is a
	// different invocation. SR-OWN-001 allows answering busy instead of
	// queueing, and this design takes that option: the caller owns the retry,
	// which keeps a queue state, a queue index, queue promotion and queued
	// aborts out of the runtime entirely.
	//
	// It is retryable by construction — the same invocation_id may be submitted
	// again once the session frees up and will then be admitted normally,
	// because nothing was persisted for the rejected attempt.
	ErrSessionBusy = errors.New("ledger: session already has an active run")
	// ErrHistoryResetInProgress means admission reached the durable reset fence.
	// Nothing was persisted and the same invocation may be retried after the
	// lease expires or its owner releases it.
	ErrHistoryResetInProgress = errors.New("ledger: session history reset is in progress")
	// ErrResetScopeNotFound means the bot or session named by a reset lease no
	// longer exists (deleted or soft-deleted). Acquire loops must fail fast on
	// it instead of retrying: the scope can never become acquirable again.
	ErrResetScopeNotFound = errors.New("ledger: reset scope no longer exists")
)

const (
	ResetScopeSession = "session"
	ResetScopeBot     = "bot"
)

// ResetLease is the PostgreSQL half of one crash-recoverable history reset.
// Token-scoped renewal/release prevents an expired owner from disturbing a
// successor that acquired the same scope.
type ResetLease struct {
	Scope     string
	BotID     string
	SessionID string
	Token     string
	ExpiresAt time.Time
}

func (l ResetLease) Valid() bool {
	if l.BotID == "" || l.Token == "" || l.ExpiresAt.IsZero() {
		return false
	}
	return l.Scope == ResetScopeBot || l.Scope == ResetScopeSession && l.SessionID != ""
}

// Run is one row of the durable ledger. Zero values mean "not set" rather than
// "empty": OwnerID is empty until the run is claimed, and AbortRequestedAt is
// the zero time until an abort is requested.
type Run struct {
	RunID        string
	BotID        string
	SessionID    string
	InvocationID string
	// TurnID and TurnPosition are allocated at admission so that a terminal
	// write targets a pre-decided turn. Combined with the existing unique index
	// on (turn_id, turn_message_seq), that makes terminal replay idempotent
	// without any new machinery.
	TurnID       string
	TurnPosition int64

	State            State
	Input            []byte
	InputFingerprint string

	OwnerID      string
	FencingToken int64
	OwnerSince   time.Time
	// LiveGeneration is the live backend incarnation that claimed this run. It
	// is stamped once and never updated, which is what makes it a stable keyset
	// cursor for the fail-closed recovery sweep.
	LiveGeneration string

	AbortRequestedAt time.Time
	ErrorCode        string
	ErrorMessage     string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// AdmitParams is one durable admission. RunID and TurnID are minted by the
// caller so the identity it hands back to a client is the identity that was
// committed.
type AdmitParams struct {
	RunID            string
	BotID            string
	SessionID        string
	InvocationID     string
	TurnID           string
	Input            []byte
	InputFingerprint string
}

// ClaimParams moves ownership of an accepted run to one owner.
type ClaimParams struct {
	RunID string
	// OwnerID is empty for a single-instance memory backend, where there is
	// only ever one owner and no cross-process claim to arbitrate.
	OwnerID        string
	FencingToken   int64
	LiveGeneration string
}

// FinalizeParams is the fenced terminal write, used by the owner for
// completed/aborted/failed and by the reaper for lost.
type FinalizeParams struct {
	RunID        string
	FencingToken int64
	State        State
	ErrorCode    string
	ErrorMessage string
}

// Cursor is a keyset position in the stale-generation sweep. The zero Cursor
// starts a sweep from the beginning.
type Cursor struct {
	LiveGeneration string
	RunID          string
}

// StaleGenerationQuery pages through runs whose claiming backend incarnation is
// no longer current.
type StaleGenerationQuery struct {
	CurrentGeneration string
	After             Cursor
	Limit             int32
}

// OrphanQuery finds admissions that committed but were never claimed.
type OrphanQuery struct {
	MinAge time.Duration
	Limit  int32
}

// Store is the durable session run ledger.
//
// Every mutation after admission is a fenced idempotent statement: it applies
// only when the caller's fencing token still matches the row, and it reports
// applied=false rather than an error when it does not. False means "already
// applied, or superseded by a newer owner" — both of which are ordinary
// outcomes for a retrying owner or a reaper that failed over mid-transition.
type Store interface {
	// Admit persists one admission. created is false when this invocation was
	// already admitted, in which case the original row is returned unchanged
	// and no turn position is consumed. Callers compare InputFingerprint to
	// decide between "same submit, same answer" and a conflict.
	//
	// It returns ErrSessionBusy when the session already has a different active
	// run. Nothing is persisted in that case, so the same invocation may be
	// submitted again later. An implementation is expected to reject busy
	// cheaply — contention is ordinary traffic here, not an exceptional event.
	Admit(ctx context.Context, params AdmitParams) (run Run, created bool, err error)

	Get(ctx context.Context, runID string) (Run, error)
	GetByInvocation(ctx context.Context, sessionID, invocationID string) (Run, error)
	// ActiveRun returns the session's single active run, or ErrRunNotFound.
	ActiveRun(ctx context.Context, sessionID string) (Run, error)
	// LatestRun backs the snapshot fallback when live state is gone: it answers
	// what PostgreSQL can prove about the most recent run and nothing more.
	LatestRun(ctx context.Context, sessionID string) (Run, error)

	// NextFencingToken draws from the monotonic sequence shared with the
	// PostgreSQL persistence fence, so one token orders both.
	NextFencingToken(ctx context.Context) (int64, error)

	Claim(ctx context.Context, params ClaimParams) (run Run, applied bool, err error)
	SetWaitingDecision(ctx context.Context, runID string, fencingToken int64) (run Run, applied bool, err error)
	Resume(ctx context.Context, runID string, fencingToken int64) (run Run, applied bool, err error)
	Finalize(ctx context.Context, params FinalizeParams) (run Run, applied bool, err error)

	// RequestAbort records the intent, which is not fenced: an abort may arrive
	// at any instance and the owner applies it.
	RequestAbort(ctx context.Context, runID string) (run Run, applied bool, err error)

	StaleGenerationRuns(ctx context.Context, query StaleGenerationQuery) ([]Run, error)
	OrphanedRuns(ctx context.Context, query OrphanQuery) ([]Run, error)
}

// ResetStore is the durable admission/reset arbiter implemented by PostgreSQL.
// It stays separate from Store so focused runtime tests and non-admission
// embedders do not need to fake lifecycle mutation they never exercise.
type ResetStore interface {
	AcquireReset(ctx context.Context, lease ResetLease, ttl time.Duration) (ResetLease, bool, error)
	RenewReset(ctx context.Context, lease ResetLease, ttl time.Duration) (ResetLease, bool, error)
	ReleaseReset(ctx context.Context, lease ResetLease) (bool, error)
	EffectiveReset(ctx context.Context, botID, sessionID string) (ResetLease, bool, error)
	ActiveRunsByBot(ctx context.Context, botID string) ([]Run, error)
}

// OrphanResetStore atomically invalidates a disappeared owner's persistence
// token and terminalizes its active run while the same reset lease is still
// valid. PostgreSQL implements this as one parent-locked transaction.
type OrphanResetStore interface {
	FenceAndFinalizeOrphan(ctx context.Context, reset ResetLease, run Run) (Run, bool, error)
}
