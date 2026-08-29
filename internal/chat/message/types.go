package message

import (
	"context"
	"encoding/json"
	"time"
)

const (
	AgentStepInterruptedMetadataKey     = "agent_step_interrupted"
	HistoryErrorCodeMetadataKey         = "error_code"
	AgentStepInterruptedReasoningPrefix = "[Previous assistant response was interrupted during reasoning. Continue from this checkpoint:]\n"
)

// LatestInterruptedCheckpoint returns the last assistant entry only when it is
// an interrupted checkpoint. Earlier checkpoints are ordinary history.
func LatestInterruptedCheckpoint(n int, classify func(int) (assistant, interrupted bool)) int {
	if n <= 0 || classify == nil {
		return -1
	}
	for i := n - 1; i >= 0; i-- {
		assistant, interrupted := classify(i)
		if assistant {
			if interrupted {
				return i
			}
			return -1
		}
	}
	return -1
}

// MessageAsset carries media asset metadata attached to a message.
// ContentHash is the content-addressed identifier for the media file.
type MessageAsset struct {
	ContentHash string         `json:"content_hash"`
	Role        string         `json:"role"`
	Ordinal     int            `json:"ordinal"`
	Mime        string         `json:"mime"`
	SizeBytes   int64          `json:"size_bytes"`
	StorageKey  string         `json:"storage_key"`
	Name        string         `json:"name,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// Message represents a single persisted bot message.
type Message struct {
	ID                      string          `json:"id"`
	BotID                   string          `json:"bot_id"`
	SessionID               string          `json:"session_id,omitempty"`
	SenderChannelIdentityID string          `json:"sender_channel_identity_id,omitempty"`
	SenderUserID            string          `json:"sender_user_id,omitempty"`
	SenderDisplayName       string          `json:"sender_display_name,omitempty"`
	SenderAvatarURL         string          `json:"sender_avatar_url,omitempty"`
	Platform                string          `json:"platform,omitempty"`
	ExternalMessageID       string          `json:"external_message_id,omitempty"`
	SourceReplyToMessageID  string          `json:"source_reply_to_message_id,omitempty"`
	Role                    string          `json:"role"`
	Content                 json.RawMessage `json:"content"`
	Metadata                map[string]any  `json:"metadata,omitempty"`
	RawMetadata             json.RawMessage `json:"-"`
	TurnID                  string          `json:"turn_id,omitempty"`
	// TurnPosition is the immutable turn-level sequence reserved at admission
	// (SR-TURN-001). It is loaded on the UI read path so the frontend can
	// order and reconcile turns without guessing from text or timestamps.
	TurnPosition   *int64          `json:"turn_position,omitempty"`
	Usage          json.RawMessage `json:"usage,omitempty"`
	SessionMode    string          `json:"session_mode,omitempty"`
	RuntimeType    string          `json:"runtime_type,omitempty"`
	Assets         []MessageAsset  `json:"assets,omitempty"`
	CompactID      string          `json:"compact_id,omitempty"`
	EventID        string          `json:"event_id,omitempty"`
	DisplayContent string          `json:"display_content,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
}

type HistoryTurn struct {
	ID                 string    `json:"id"`
	BotID              string    `json:"bot_id"`
	SessionID          string    `json:"session_id"`
	Position           int64     `json:"position"`
	RequestMessageID   string    `json:"request_message_id,omitempty"`
	AssistantMessageID string    `json:"assistant_message_id,omitempty"`
	SupersededByTurnID string    `json:"superseded_by_turn_id,omitempty"`
	SupersededAt       time.Time `json:"superseded_at,omitempty"`
	SupersededReason   string    `json:"superseded_reason,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// AssetRef links a media asset to a persisted message.
// ContentHash is the content-addressed identifier for the media file.
type AssetRef struct {
	ContentHash string         `json:"content_hash"`
	Role        string         `json:"role"`
	Ordinal     int            `json:"ordinal"`
	Mime        string         `json:"mime,omitempty"`
	SizeBytes   int64          `json:"size_bytes,omitempty"`
	StorageKey  string         `json:"storage_key,omitempty"`
	Name        string         `json:"name,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// PersistInput is the input for persisting a message.
type PersistInput struct {
	BotID                   string
	SessionID               string
	SenderChannelIdentityID string
	SenderUserID            string
	ExternalMessageID       string
	SourceReplyToMessageID  string
	Role                    string
	Content                 json.RawMessage
	Metadata                map[string]any
	Usage                   json.RawMessage
	SessionMode             string
	RuntimeType             string
	Assets                  []AssetRef
	ModelID                 string
	EventID                 string
	DisplayText             string
	TurnRequestMessageID    string
	SkipHistoryTurn         bool
	// RunID links this row to the admitted run that produced it. It is
	// traceability only: history is read by turn, never by run.
	RunID string
	// TurnID and TurnPosition carry a turn this message must be filed under
	// because admission already allocated it (SR-TURN-001). Both empty means no
	// admission decided the turn and the history layer allocates one, which is
	// still the case for channel inbound and schedules.
	//
	// They travel together: a turn id without its position would file the row
	// under the right turn at the wrong place in the session's order, and a
	// position without its id would take a slot the client cannot name.
	TurnID       string
	TurnPosition *int64
}

type LocateResult struct {
	Messages []Message
	TargetID string
}

// ActiveMessagesMeasure is a database-side aggregate over a session's active
// messages (CM-ADM-001): counts and raw content bytes, computed without
// loading any payload into the process.
type ActiveMessagesMeasure struct {
	MessageCount int64
	ContentBytes int64
}

// Writer defines write behavior needed by the inbound router.
type Writer interface {
	Persist(ctx context.Context, input PersistInput) (Message, error)
}

// ToolTailRoundPersister optionally persists a complete
// user -> assistant(tool-call) -> tool -> assistant(final) round in one write.
type ToolTailRoundPersister interface {
	PersistToolTailRound(ctx context.Context, inputs []PersistInput) ([]Message, bool, error)
}

type TurnReplacement struct {
	OldTurnID               string
	ReplacementTurnID       string
	ReplacementTurnPosition *int64
	RequestMessageID        string
	Reason                  string
	SessionMetadata         map[string]any
}

// ACPPublication moves the session's canonical ACP publication head to the
// round's run inside the same transaction as the round's messages. A head with
// CheckpointReset=true is canonical but not resumable.
type ACPPublication struct {
	RunID           string
	CheckpointReset bool
}

type RoundPersistenceOptions struct {
	Replacement                   *TurnReplacement
	CleanupACPDecisionProjections bool
	ACPPublication                *ACPPublication
}

// AtomicRoundPersister writes a complete round in one transaction.
// Implementations must enforce any runtime fence carried by ctx, while still
// supporting unfenced local replacement transactions.
type AtomicRoundPersister interface {
	PersistRound(ctx context.Context, inputs []PersistInput, options RoundPersistenceOptions) ([]Message, bool, error)
}

// AgentStep is one native-agent step persisted atomically. Interrupted marks a
// text/reasoning snapshot of a step the model never finished, which takes a
// different writability predicate than a complete step.
type AgentStep struct {
	RunID       string
	Messages    []PersistInput
	Interrupted bool
}

type AgentStepPersister interface {
	PersistAgentStep(ctx context.Context, step AgentStep) ([]Message, error)
}

// Service defines message read/write behavior.
type Service interface {
	Writer
	List(ctx context.Context, botID string) ([]Message, error)
	ListSince(ctx context.Context, botID string, since time.Time) ([]Message, error)
	ListActiveSince(ctx context.Context, botID string, since time.Time) ([]Message, error)
	// ListActiveSinceWithinBytes is the byte-budgeted variant of
	// ListActiveSince (CM-ADM-001): rows are admitted newest-first until
	// their content byte total crosses maxBytes, so process memory is
	// bounded by the budget regardless of total history size.
	ListActiveSinceWithinBytes(ctx context.Context, botID string, since time.Time, maxBytes int64) ([]Message, error)
	ListLatest(ctx context.Context, botID string, limit int32) ([]Message, error)
	ListBefore(ctx context.Context, botID string, before time.Time, limit int32) ([]Message, error)
	ListBySession(ctx context.Context, sessionID string) ([]Message, error)
	ListSinceBySession(ctx context.Context, sessionID string, since time.Time) ([]Message, error)
	ListActiveSinceBySession(ctx context.Context, sessionID string, since time.Time) ([]Message, error)
	// ListActiveSinceBySessionWithinBytes is the byte-budgeted variant of
	// ListActiveSinceBySession (CM-ADM-001), same admission semantics as
	// ListActiveSinceWithinBytes scoped to one session.
	ListActiveSinceBySessionWithinBytes(ctx context.Context, sessionID string, since time.Time, maxBytes int64) ([]Message, error)
	// MeasureActiveBySession aggregates message count and content bytes on
	// the database side (CM-ADM-001): admission sizes a session's history
	// without shipping any payload into the process.
	MeasureActiveBySession(ctx context.Context, sessionID string, since time.Time) (ActiveMessagesMeasure, error)
	ListLatestBySession(ctx context.Context, sessionID string, limit int32) ([]Message, error)
	ListBeforeBySession(ctx context.Context, sessionID string, before time.Time, limit int32) ([]Message, error)
	ListBeforeMessageBySession(ctx context.Context, sessionID string, beforeMessageID string, limit int32) ([]Message, error)
	LocateByExternalIDBySession(ctx context.Context, sessionID string, externalMessageID string, beforeLimit int32, afterLimit int32) (LocateResult, error)
	GetByIDBySession(ctx context.Context, sessionID string, messageID string) (Message, error)
	ListVisibleFromBySession(ctx context.Context, sessionID string, messageID string) ([]Message, error)
	GetVisibleTurnByMessage(ctx context.Context, sessionID string, messageID string) (HistoryTurn, error)
	GetLatestVisibleTurnBySession(ctx context.Context, sessionID string) (HistoryTurn, error)
	ReplaceTurn(ctx context.Context, sessionID string, oldTurnID string, replacementTurnID string, replacementTurnPosition *int64, requestMessageID string, assistantMessageID string, reason string) (HistoryTurn, error)
	DeleteByIDs(ctx context.Context, ids []string) error
	DeleteByBot(ctx context.Context, botID string) error
	DeleteBySession(ctx context.Context, sessionID string) error
	LinkAssets(ctx context.Context, messageID string, assets []AssetRef) error
}
