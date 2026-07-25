package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	chatview "github.com/memohai/memoh/domains/agent/view"
)

const (
	EventRuntimeSnapshot = "runtime_snapshot"
	EventRuntimeDelta    = "runtime_delta"
	EventRuntimeDropped  = "runtime_dropped"

	RunStatusRunning   = "running"
	RunStatusAdmitting = "admitting"
	RunStatusAborting  = "aborting"
	RunStatusCompleted = "completed"
	RunStatusAborted   = "aborted"
	RunStatusErrored   = "errored"
	RunStatusLost      = "lost"

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
	CommandResult               = "command_result"
)

var (
	ErrCommandOwnerUnavailable = errors.New("runtime command owner is unavailable")
	ErrCommandTargetNotActive  = errors.New("runtime command target is not active")
	ErrCommandTargetMismatch   = errors.New("stream does not belong to this session")
	ErrCommandExpired          = errors.New("runtime command expired before acknowledgement")
	ErrCommandBusy             = errors.New("runtime command executor is busy")
	ErrCommandPayloadConflict  = errors.New("runtime command payload conflicts with an earlier request")
	ErrManagerClosed           = errors.New("session runtime manager is closed")
	ErrRunOwnershipLost        = errors.New("runtime run ownership was lost")
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

type StreamRef struct {
	BotID      string `json:"bot_id"`
	SessionID  string `json:"session_id"`
	StreamID   string `json:"stream_id"`
	OwnerID    string `json:"owner_id"`
	Generation string `json:"generation"`
}

// RunHandle identifies one admitted run. Stream IDs may be reused after a run
// finishes, so owner-side mutations must also carry the run generation.
type RunHandle struct {
	BotID      string
	SessionID  string
	StreamID   string
	Generation string
}

func (h RunHandle) normalized() RunHandle {
	h.BotID = strings.TrimSpace(h.BotID)
	h.SessionID = strings.TrimSpace(h.SessionID)
	h.StreamID = strings.TrimSpace(h.StreamID)
	h.Generation = strings.TrimSpace(h.Generation)
	return h
}

func (h RunHandle) valid() bool {
	h = h.normalized()
	return h.BotID != "" && h.SessionID != "" && h.StreamID != "" && h.Generation != ""
}

func (h RunHandle) key() Key {
	h = h.normalized()
	return Key{BotID: h.BotID, SessionID: h.SessionID}
}

type Snapshot struct {
	BotID          string          `json:"bot_id"`
	SessionID      string          `json:"session_id"`
	Epoch          string          `json:"epoch"`
	Seq            int64           `json:"seq"`
	CurrentRunView *CurrentRunView `json:"current_run_view,omitempty"`
	Queue          []QueuedRunView `json:"queue"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

// EmptySnapshot returns the canonical empty runtime snapshot for a session.
func EmptySnapshot(botID, sessionID string) Snapshot {
	return Snapshot{
		BotID:     strings.TrimSpace(botID),
		SessionID: strings.TrimSpace(sessionID),
		Queue:     []QueuedRunView{},
	}
}

type QueuedRunView struct {
	StreamID string `json:"stream_id,omitempty"`
}

type CurrentRunView struct {
	StreamID            string               `json:"stream_id"`
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
	StreamID  string        `json:"stream_id,omitempty"`
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
	StreamID            string      `json:"stream_id"`
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
	Type         string          `json:"type"`
	ID           string          `json:"id,omitempty"`
	ReplyOwnerID string          `json:"reply_owner_id,omitempty"`
	BotID        string          `json:"bot_id"`
	SessionID    string          `json:"session_id"`
	StreamID     string          `json:"stream_id"`
	Generation   string          `json:"generation"`
	TargetID     string          `json:"target_id,omitempty"`
	SteerID      string          `json:"steer_id,omitempty"`
	Text         string          `json:"text,omitempty"`
	Payload      json.RawMessage `json:"payload,omitempty"`
	PayloadHash  string          `json:"payload_hash,omitempty"`
	ErrorCode    string          `json:"error_code,omitempty"`
	Error        string          `json:"error,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	ExpiresAt    time.Time       `json:"expires_at,omitempty"`
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
	UpdateActiveRun(ctx context.Context, key Key, streamID, generation string, update ActiveRunUpdate) (Snapshot, bool, error)
	StartRun(ctx context.Context, key Key, ref StreamRef, update SnapshotUpdate) (Snapshot, bool, error)
	ReleaseRun(ctx context.Context, key Key, ref StreamRef, update ActiveRunUpdate) (Snapshot, bool, error)
	RenewLease(ctx context.Context, key Key, streamID, ownerID, generation string, renewedAt, expiresAt time.Time) error
	ValidateRunOwnership(ctx context.Context, key Key, ref StreamRef) error
	LoadStreamRef(ctx context.Context, key Key, streamID string) (StreamRef, bool, error)
	DeleteStreamRef(ctx context.Context, ref StreamRef) (bool, error)
	PublishCommand(ctx context.Context, ownerID string, command Command) error
	SubscribeCommands(ctx context.Context, ownerID string) (CommandSubscription, error)
	StoreCommandResult(ctx context.Context, result Command, ttl time.Duration) error
	LoadCommandResult(ctx context.Context, commandID string) (Command, bool, error)
}

type startupHealthChecker interface {
	CheckHealth(ctx context.Context) error
}

type CommandSubscription struct {
	C     <-chan Command
	Close func()
}
