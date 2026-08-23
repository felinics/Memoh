package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	chatview "github.com/memohai/memoh/internal/agent/view"
	"github.com/memohai/memoh/internal/runtimefence"
)

const (
	EventRuntimeSnapshot = "runtime_snapshot"
	EventRuntimeDelta    = "runtime_delta"
	EventRuntimeDropped  = "runtime_dropped"

	RunStatusRunning   = "running"
	RunStatusAdmitting = "admitting"
	// RunStatusWaitingDecision keeps the admitted run active while its native
	// execution is parked on a durable approval or ask_user decision.
	RunStatusWaitingDecision = "waiting_decision"
	RunStatusAborting        = "aborting"
	RunStatusCompleted       = "completed"
	RunStatusAborted         = "aborted"
	RunStatusErrored         = "errored"
	RunStatusLost            = "lost"

	SteerStatusPending  = "pending"
	SteerStatusQueued   = "queued"
	SteerStatusApplied  = "applied"
	SteerStatusRejected = "rejected"

	RunOperationRetry = "retry"
	RunOperationEdit  = "edit"

	CommandAbort                = "abort"
	CommandSteer                = "steer_current_run"
	CommandToolApprovalResponse = "tool_approval_response"
	CommandUserInputResponse    = "user_input_response"
	CommandHistoryReset         = "history_reset"
	CommandResult               = "command_result"

	ResetScopeSession = "session"
	ResetScopeBot     = "bot"
)

var (
	ErrCommandOwnerUnavailable = errors.New("runtime command owner is unavailable")
	ErrCommandTargetNotActive  = errors.New("runtime command target is not active")
	ErrCommandTargetMismatch   = errors.New("run does not belong to this session")
	ErrCommandExpired          = errors.New("runtime command expired before acknowledgement")
	ErrCommandBusy             = errors.New("runtime command executor is busy")
	ErrCommandPayloadConflict  = errors.New("runtime command payload conflicts with an earlier request")
	ErrDecisionNotFound        = errors.New("runtime decision was not found")
	ErrManagerClosed           = errors.New("session runtime manager is closed")
	ErrRunOwnershipLost        = errors.New("runtime run ownership was lost")
	ErrHistoryResetInProgress  = errors.New("session history reset is in progress")
	ErrHistoryResetUnavailable = errors.New("session history reset coordination is unavailable")
	ErrHistoryResetLeaseLost   = runtimefence.ErrResetLeaseLost
)

type Key struct {
	BotID     string `json:"bot_id"`
	SessionID string `json:"session_id"`
}

// String returns the canonical "botID:sessionID" composite used to key
// per-session state across backends and subscription registries.
func (k Key) String() string {
	return strings.TrimSpace(k.BotID) + ":" + strings.TrimSpace(k.SessionID)
}

type RunRef struct {
	BotID      string `json:"bot_id"`
	SessionID  string `json:"session_id"`
	RunID      string `json:"run_id"`
	OwnerID    string `json:"owner_id"`
	Generation string `json:"generation"`
	// FencingToken is the durable ownership token this reservation was made
	// with. It is stored with the ref so the lease index entry can be written
	// and removed from the backend's own copy, without a caller having to
	// reconstruct a token it may no longer hold. Zero means the run has no
	// ledger identity and therefore nothing for the reaper to transition.
	FencingToken int64 `json:"fencing_token,omitempty"`
}

// ResetScope identifies the canonical history protected by a reset lease.
// SessionID is empty for a bot-wide reset.
type ResetScope struct {
	BotID     string `json:"bot_id"`
	SessionID string `json:"session_id,omitempty"`
}

func (s ResetScope) normalized() ResetScope {
	s.BotID = strings.TrimSpace(s.BotID)
	s.SessionID = strings.TrimSpace(s.SessionID)
	return s
}

func (s ResetScope) kind() string {
	if strings.TrimSpace(s.SessionID) == "" {
		return ResetScopeBot
	}
	return ResetScopeSession
}

func (s ResetScope) valid() bool {
	s = s.normalized()
	return s.BotID != ""
}

// ResetLease is the live-backend half of the reset fence. The same token is
// used in PostgreSQL so renewal and release are successor-safe on both sides.
type ResetLease struct {
	Scope     ResetScope `json:"scope"`
	Token     string     `json:"token"`
	ExpiresAt time.Time  `json:"expires_at"`
}

func (l ResetLease) valid() bool {
	return l.Scope.valid() && strings.TrimSpace(l.Token) != "" && !l.ExpiresAt.IsZero()
}

// effectiveResetLease is the single precedence rule every reset-lease reader
// implements identically (the SQL readers in session_runtime_resets.sql mirror
// it): a bot-scope query is blocked by the bot lease or by ANY active session
// lease of that bot, because a bot-wide operation must not proceed while one
// of the bot's sessions is mid-reset; a session-scope query is blocked by the
// bot lease or by that exact session's lease. Callers pass only unexpired
// leases. Session leases are scanned in slice order, so callers that need
// determinism sort before calling.
func effectiveResetLease(scope ResetScope, bot *ResetLease, sessions []ResetLease) (ResetLease, bool) {
	if bot != nil {
		return *bot, true
	}
	scope = scope.normalized()
	if scope.SessionID == "" {
		if len(sessions) > 0 {
			return sessions[0], true
		}
		return ResetLease{}, false
	}
	for _, lease := range sessions {
		if strings.TrimSpace(lease.Scope.SessionID) == scope.SessionID {
			return lease, true
		}
	}
	return ResetLease{}, false
}

// identityMatches compares everything that names a reservation, ignoring the
// fencing token. Release and validation paths reconstruct a ref from live state
// that does not carry the token, so requiring it to match would reject the
// legitimate owner.
func (r RunRef) identityMatches(other RunRef) bool {
	return strings.TrimSpace(r.BotID) == strings.TrimSpace(other.BotID) &&
		strings.TrimSpace(r.SessionID) == strings.TrimSpace(other.SessionID) &&
		strings.TrimSpace(r.RunID) == strings.TrimSpace(other.RunID) &&
		strings.TrimSpace(r.OwnerID) == strings.TrimSpace(other.OwnerID) &&
		strings.TrimSpace(r.Generation) == strings.TrimSpace(other.Generation)
}

// RunHandle identifies one admitted run. A run id can be reused by a client that
// replays an old reservation, so owner-side mutations also carry the generation.
type RunHandle struct {
	BotID      string
	SessionID  string
	RunID      string
	TurnID     string
	Generation string
	// FencingToken is the ledger ownership token for this run. Callers need it
	// to fence their own durable writes, which is why it travels with the
	// handle rather than staying inside the runtime. It is zero for runs
	// started through the pre-ledger entry points.
	FencingToken int64
}

// TerminalRun is the authoritative durable outcome of one admitted run. It is
// emitted only after the fenced session_runs transition has applied, or when a
// replay observes that the same run is already terminal. State uses the durable
// ledger vocabulary: completed, aborted, failed, or lost.
type TerminalRun struct {
	RunID        string
	BotID        string
	SessionID    string
	FencingToken int64
	State        string
	ErrorCode    string
}

func (h RunHandle) normalized() RunHandle {
	h.BotID = strings.TrimSpace(h.BotID)
	h.SessionID = strings.TrimSpace(h.SessionID)
	h.RunID = strings.TrimSpace(h.RunID)
	h.TurnID = strings.TrimSpace(h.TurnID)
	h.Generation = strings.TrimSpace(h.Generation)
	return h
}

func (h RunHandle) valid() bool {
	h = h.normalized()
	return h.BotID != "" && h.SessionID != "" && h.RunID != "" && h.Generation != ""
}

func (h RunHandle) key() Key {
	h = h.normalized()
	return Key{BotID: h.BotID, SessionID: h.SessionID}
}

// Snapshot is the authoritative live view of one session. It holds at most one
// run: admission answers busy rather than queueing, so there is no pending list
// to project and a subscriber never has to reason about work it cannot see yet.
type Snapshot struct {
	BotID          string          `json:"bot_id"`
	SessionID      string          `json:"session_id"`
	Epoch          string          `json:"epoch"`
	Seq            int64           `json:"seq"`
	CurrentRunView *CurrentRunView `json:"current_run_view,omitempty"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

// EmptySnapshot returns the canonical empty runtime snapshot for a session.
func EmptySnapshot(botID, sessionID string) Snapshot {
	return Snapshot{
		BotID:     strings.TrimSpace(botID),
		SessionID: strings.TrimSpace(sessionID),
	}
}

// Cursor is a position in one session's observable event stream. Epoch and Seq
// travel as a pair because Seq restarts whenever the epoch does: a live backend
// that loses its state hands the session a new epoch, so comparing sequence
// numbers across epochs would order two unrelated streams against each other.
//
// This is the position subscribers dedupe and recover on (SR-OBS-002). It is a
// different thing from ledger.Cursor, which is a keyset position in the reaper's
// sweep over durable rows.
type Cursor struct {
	Epoch string `json:"epoch,omitempty"`
	Seq   int64  `json:"seq"`
}

func (s Snapshot) cursor() Cursor {
	return Cursor{Epoch: strings.TrimSpace(s.Epoch), Seq: s.Seq}
}

type CurrentRunView struct {
	RunID string `json:"run_id" validate:"required" format:"uuid"`
	// TurnID is the durable turn this run writes into, allocated at admission.
	// It is part of the observable view because SR-OBS-003 requires every
	// subscriber to agree on the run's turn, and a subscriber that only learns
	// the run id cannot line the run up against persisted history.
	TurnID string `json:"turn_id" validate:"required" format:"uuid"`
	// InvocationID is the caller-supplied intent identity recorded at admission
	// (session_runs.invocation_id). It rides the live view so the client that
	// originated the send can match projection frames to its optimistic turn by
	// reading the frame, instead of inferring the pairing from arrival timing
	// while waiting for the acceptance that names the two. Subscribers that did
	// not originate the run see an id unknown to them and treat the turn as
	// foreign — which is the correct standalone rendering for cross-device runs.
	InvocationID        string               `json:"invocation_id,omitempty"`
	Generation          string               `json:"generation"`
	Status              string               `json:"status"`
	OwnerID             string               `json:"owner_id,omitempty"`
	OwnerLeaseExpiresAt *time.Time           `json:"owner_lease_expires_at,omitempty"`
	StartedAt           time.Time            `json:"started_at"`
	UpdatedAt           time.Time            `json:"updated_at"`
	Messages            []chatview.UIMessage `json:"messages"`
	RequestUserTurn     *chatview.UITurn     `json:"request_user_turn,omitempty"`
	Error               string               `json:"error,omitempty"`
	Steer               *SteerState          `json:"steer,omitempty"`
	Operation           *RunOperationView    `json:"operation,omitempty"`
}

// RunAdmissionView is the canonical state published when a reserved run
// becomes active. RequestUserTurn is intentionally runtime state rather than
// a durable history row; ordinary sends still persist user + assistant
// together when the run reaches a terminal result.
type RunAdmissionView struct {
	RequestUserTurn *chatview.UITurn
	Operation       *RunOperationView
}

type RunOperationView struct {
	Kind                 string           `json:"kind" validate:"required" enums:"retry,edit"`
	ReplaceFromMessageID string           `json:"replace_from_message_id" validate:"required"`
	ReplacementUserTurn  *chatview.UITurn `json:"replacement_user_turn,omitempty"`
}

type SteerState struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	Text      string    `json:"text,omitempty"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Event struct {
	Type      string        `json:"type"`
	BotID     string        `json:"bot_id"`
	SessionID string        `json:"session_id"`
	Epoch     string        `json:"epoch,omitempty"`
	RunID     string        `json:"run_id,omitempty"`
	Seq       int64         `json:"seq"`
	UpdatedAt *time.Time    `json:"updated_at,omitempty"`
	Snapshot  *Snapshot     `json:"snapshot,omitempty"`
	Delta     *RuntimeDelta `json:"delta,omitempty"`
	Message   string        `json:"message,omitempty"`
}

// RuntimeDelta carries only the state changed by one committed runtime
// transition. Full snapshots are reserved for hydration and gap recovery.
type RuntimeDelta struct {
	CurrentRunView  *CurrentRunView         `json:"current_run_view,omitempty"`
	Run             *CurrentRunPatch        `json:"run,omitempty"`
	MessageAppends  []RuntimeMessageAppend  `json:"message_appends,omitempty"`
	ProgressAppends []RuntimeProgressAppend `json:"progress_appends,omitempty"`
	MessageUpserts  []chatview.UIMessage    `json:"message_upserts,omitempty"`
	ResetMessages   bool                    `json:"reset_messages,omitempty"`
}

type CurrentRunPatch struct {
	RunID               string      `json:"run_id"`
	Status              *string     `json:"status,omitempty"`
	Error               *string     `json:"error,omitempty"`
	Steer               *SteerState `json:"steer,omitempty"`
	UpdatedAt           *time.Time  `json:"updated_at,omitempty"`
	OwnerLeaseExpiresAt *time.Time  `json:"owner_lease_expires_at,omitempty"`
}

type RuntimeMessageAppend struct {
	ID      int                    `json:"id"`
	Type    chatview.UIMessageType `json:"type"`
	Content string                 `json:"content"`
}

type RuntimeProgressAppend struct {
	ID       int `json:"id"`
	Progress any `json:"progress"`
	Input    any `json:"input,omitempty"`
}

type Command struct {
	Type         string `json:"type"`
	ID           string `json:"id,omitempty"`
	ReplyOwnerID string `json:"reply_owner_id,omitempty"`
	BotID        string `json:"bot_id"`
	SessionID    string `json:"session_id"`
	RunID        string `json:"run_id"`
	Generation   string `json:"generation"`
	FencingToken int64  `json:"fencing_token,omitempty"`
	TargetID     string `json:"target_id,omitempty"`
	// DecisionResolved means the command target was resolved from PostgreSQL
	// before routing. Owner-side execution must not consult the live UI
	// projection again: it is derived state and may lag the durable decision.
	DecisionResolved bool            `json:"decision_resolved,omitempty"`
	SteerID          string          `json:"steer_id,omitempty"`
	Text             string          `json:"text,omitempty"`
	Payload          json.RawMessage `json:"payload,omitempty"`
	PayloadHash      string          `json:"payload_hash,omitempty"`
	ErrorCode        string          `json:"error_code,omitempty"`
	Error            string          `json:"error,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	ExpiresAt        time.Time       `json:"expires_at,omitempty"`
}

// DecisionTarget is the durable identity of one approval or user-input
// request. It is resolved from PostgreSQL and is the correctness boundary for
// decision routing; CurrentRunView.Messages is only a subscriber projection.
type DecisionTarget struct {
	Type         string
	ID           string
	BotID        string
	SessionID    string
	RunID        string
	TurnID       string
	Status       string
	FencingToken int64
	ControlID    string
	PayloadHash  string
}

func (t DecisionTarget) normalized() DecisionTarget {
	t.Type = strings.TrimSpace(t.Type)
	t.ID = strings.TrimSpace(t.ID)
	t.BotID = strings.TrimSpace(t.BotID)
	t.SessionID = strings.TrimSpace(t.SessionID)
	t.RunID = strings.TrimSpace(t.RunID)
	t.TurnID = strings.TrimSpace(t.TurnID)
	t.Status = strings.TrimSpace(t.Status)
	t.ControlID = strings.TrimSpace(t.ControlID)
	t.PayloadHash = strings.TrimSpace(t.PayloadHash)
	return t
}

func (t DecisionTarget) runtimeOwned() bool {
	t = t.normalized()
	return t.ID != "" && t.BotID != "" && t.SessionID != "" &&
		t.RunID != "" && t.TurnID != "" && t.FencingToken > 0
}

// DecisionStore is implemented by the application layer over the PostgreSQL
// decision tables. RouteDecisionResponse uses ResolveRuntimeDecision for every
// transport; recovery uses PendingRuntimeDecision to preserve exactly the
// decision that parked a run while advancing its fencing token.
type DecisionStore interface {
	ResolveRuntimeDecision(ctx context.Context, commandType, decisionID string) (DecisionTarget, error)
	PendingRuntimeDecision(ctx context.Context, runID string) (DecisionTarget, bool, error)
}

// DecisionResponse is one transport-neutral answer. ControlID is minted by the
// caller and remains the command identity even after the addressed run leaves
// live state.
type DecisionResponse struct {
	ControlID  string
	Type       string
	DecisionID string
	BotID      string
	SessionID  string
	RunID      string
	Payload    json.RawMessage
}

// DecisionResponseResult separates "this is a runtime decision" from "the
// answer changed it". A resolved terminal decision is handled but not applied;
// an unfenced ACP/MCP request is not handled and follows its existing path.
type DecisionResponseResult struct {
	Handled bool
	Applied bool
}

type Subscription struct {
	C     <-chan Event
	Close func()
}

type (
	SnapshotUpdate  func(snapshot Snapshot, exists bool) (Snapshot, bool, error)
	ActiveRunUpdate func(snapshot Snapshot, now time.Time) (Snapshot, bool, error)
)

type Backend interface {
	Now(ctx context.Context) (time.Time, error)
	// Load returns a snapshot the caller owns and may freely mutate.
	Load(ctx context.Context, key Key) (Snapshot, bool, error)
	Update(ctx context.Context, key Key, update SnapshotUpdate) (Snapshot, bool, error)
	Publish(ctx context.Context, event Event) error
	Subscribe(ctx context.Context, key Key) (Subscription, error)
	Close() error
}

// DistributedBackend adds cross-process run ownership and command routing.
// MemoryBackend intentionally does not implement this interface.
type DistributedBackend interface {
	Backend
	UpdateActiveRun(ctx context.Context, key Key, runID, generation string, update ActiveRunUpdate) (Snapshot, bool, error)
	StartRun(ctx context.Context, key Key, ref RunRef, update SnapshotUpdate) (Snapshot, bool, error)
	ReleaseRun(ctx context.Context, key Key, ref RunRef, update ActiveRunUpdate) (Snapshot, bool, error)
	RenewLease(ctx context.Context, key Key, runID, ownerID, generation string, renewedAt, expiresAt time.Time) error
	ValidateRunOwnership(ctx context.Context, key Key, ref RunRef) error
	LoadRunRef(ctx context.Context, key Key, runID string) (RunRef, bool, error)
	DeleteRunRef(ctx context.Context, ref RunRef) (bool, error)
	PublishCommand(ctx context.Context, ownerID string, command Command) error
	SubscribeCommands(ctx context.Context, ownerID string) (CommandSubscription, error)
	StoreCommandResult(ctx context.Context, result Command, ttl time.Duration) error
	LoadCommandResult(ctx context.Context, commandID string) (Command, bool, error)
}

// HistoryResetBackend provides a tokenized, expiring live gate. Redis uses
// key TTLs; MemoryBackend uses the same contract under its process mutex.
type HistoryResetBackend interface {
	AcquireHistoryReset(ctx context.Context, scope ResetScope, token string, ttl time.Duration) (ResetLease, bool, error)
	RenewHistoryReset(ctx context.Context, lease ResetLease, ttl time.Duration) (ResetLease, bool, error)
	ReleaseHistoryReset(ctx context.Context, lease ResetLease) (bool, error)
	EffectiveHistoryReset(ctx context.Context, scope ResetScope) (ResetLease, bool, error)
}

type historyResetStartBackend interface {
	StartRunIfNoHistoryReset(ctx context.Context, key Key, update SnapshotUpdate) (Snapshot, bool, error)
}

// HistoryResetHandler performs owner-local runtime teardown. Returning is the
// acknowledgement boundary: the ACP pool must not return until Session.Close
// and the owned process Close operation have completed.
type HistoryResetHandler func(context.Context, ResetScope) error

type startupHealthChecker interface {
	CheckHealth(ctx context.Context) error
}

type CommandSubscription struct {
	C     <-chan Command
	Close func()
}
