package thread

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/chat/event"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/hooks"
	"github.com/felinics/memoh/internal/runtimefence"
	"github.com/felinics/memoh/internal/runtimekind"
)

type runtimeFencedThreadWriter interface {
	UpdateSessionTitleWithRuntimeFence(ctx context.Context, arg sqlc.UpdateSessionTitleWithRuntimeFenceParams) (sqlc.BotSession, error)
	UpdateSessionMetadataWithRuntimeFence(ctx context.Context, arg sqlc.UpdateSessionMetadataWithRuntimeFenceParams) (sqlc.BotSession, error)
}

// Visibility describes whether a thread belongs in user-facing history or is
// reserved for an internal agent workflow.
type Visibility string

const (
	VisibilityUser     Visibility = "user"
	VisibilityInternal Visibility = "internal"
)

// Thread represents a chat thread within a bot.
type Thread struct {
	ID                    string         `json:"id"`
	BotID                 string         `json:"bot_id"`
	BotAgentID            string         `json:"bot_agent_id,omitempty"`
	RouteID               string         `json:"route_id,omitempty"`
	ChannelType           string         `json:"channel_type,omitempty"`
	Type                  string         `json:"type"`
	SessionMode           string         `json:"session_mode"`
	RuntimeType           string         `json:"runtime_type"`
	RuntimeMetadata       map[string]any `json:"runtime_metadata,omitempty"`
	Title                 string         `json:"title"`
	Metadata              map[string]any `json:"metadata,omitempty"`
	ParentThreadID        string         `json:"parent_session_id,omitempty"`
	CreatedByUserID       string         `json:"created_by_user_id,omitempty"`
	WorkdirID             string         `json:"workdir_id,omitempty"`
	CreatedAt             time.Time      `json:"created_at"`
	UpdatedAt             time.Time      `json:"updated_at"`
	RouteMetadata         map[string]any `json:"route_metadata,omitempty"`
	RouteConversationType string         `json:"route_conversation_type,omitempty"`
	Visibility            Visibility     `json:"-"`
} // @name session.Session

// The runtime vocabulary is owned by runtimekind (a leaf package shared with
// consumers that cannot import this one); these constants are pinned aliases
// so the session domain keeps its established names.
const (
	TypeChat              = "chat"
	TypeSchedule          = "schedule"
	TypeSubagent          = "subagent"
	TypeDiscuss           = "discuss"
	TypeACPAgent          = "acp_agent"
	RuntimeModel          = string(runtimekind.Model)
	RuntimeACPAgent       = string(runtimekind.ACPAgent)
	RuntimeCodex          = string(runtimekind.Codex)
	RuntimeClaudeCode     = string(runtimekind.ClaudeCode)
	DefaultACPProjectMode = "project"
	DefaultACPProjectPath = "/data"
)

// IsDirectRuntimeType reports whether a runtime type is one of the direct
// external agent runtimes, outside the ACP compatibility pool.
func IsDirectRuntimeType(runtimeType string) bool {
	return runtimekind.IsDirect(runtimeType)
}

// IsDirectRuntime reports whether a session runs on a direct external agent
// runtime.
func IsDirectRuntime(thread Thread) bool {
	return IsDirectRuntimeType(normalizeRuntimeType(thread.RuntimeType, thread.Type))
}

// UsesDecisionWaiter reports whether tool-approval and user-input decisions
// for this session are answered by waking an in-process runtime waiter (the
// ACP pool or a direct external runtime) instead of resuming the native agent
// loop with a tool result.
func UsesDecisionWaiter(thread Thread) bool {
	return runtimekind.UsesDecisionWaiter(normalizeRuntimeType(thread.RuntimeType, thread.Type))
}

// userFacingSessionTypes lists the session types intended to appear in
// user-facing session lists. Schedule and subagent sessions are
// system-internal — they back agent-driven loops and never surface in the UI.
var userFacingSessionTypes = []string{TypeChat, TypeDiscuss, TypeACPAgent}

// UserFacingSessionTypes returns a fresh copy of the user-facing session type
// list so callers can read or mutate it without disturbing the package-level
// source of truth.
func UserFacingSessionTypes() []string {
	out := make([]string, len(userFacingSessionTypes))
	copy(out, userFacingSessionTypes)
	return out
}

// AllSessionTypes returns every known session type. The paged list queries
// always demand an explicit type filter; callers that filter by visibility
// instead pass this list to make the type predicate a no-op.
func AllSessionTypes() []string {
	return []string{TypeChat, TypeSchedule, TypeSubagent, TypeDiscuss, TypeACPAgent}
}

var (
	ErrACPAgentIDRequired     = errors.New("acp_agent_id is required for acp_agent sessions")
	ErrACPProjectPathMissing  = errors.New("project_path is required for acp_agent sessions")
	ErrACPUnknownAgent        = errors.New("unknown ACP agent")
	ErrACPAgentNotEnabled     = errors.New("ACP agent is not enabled for this bot")
	ErrACPAgentNotConfigured  = errors.New("ACP agent is not configured for this bot")
	ErrACPRuntimeOwnerMissing = errors.New("runtime_owner_account_id is required for acp_agent sessions")
	ErrACPProjectModeInvalid  = errors.New("unknown ACP project mode")
	// ErrExternalRuntimeOwnerMissing mirrors the ACP owner requirement for
	// direct external runtimes: turns execute with workspace authority, so a
	// session without an owner has nobody to authorize them against.
	ErrExternalRuntimeOwnerMissing = errors.New("runtime_owner_account_id is required for external runtime sessions")
	ErrForkSourceNotFound          = errors.New("fork source session not found")
	ErrForkSourceNotReply          = errors.New("fork source must be a visible assistant reply")
	ErrForkSourceNotChat           = errors.New("fork source must be a chat session")
	ErrSessionHasMessages          = errors.New("session has visible messages")
)

func IsKnownType(typ string) bool {
	switch strings.TrimSpace(typ) {
	case TypeChat, TypeSchedule, TypeSubagent, TypeDiscuss, TypeACPAgent:
		return true
	default:
		return false
	}
}

// IsUserFacingType reports whether a session type is one that user-facing
// session list endpoints should return by default.
func IsUserFacingType(typ string) bool {
	return slices.Contains(userFacingSessionTypes, strings.TrimSpace(typ))
}

// NormalizeVisibility resolves a possibly-absent stored visibility value
// against the session mode's default. Exported for restore paths that replay
// archived session rows which may predate the visibility column.
func NormalizeVisibility(stored string, mode string) Visibility {
	return storedVisibility(stored, mode)
}

// storedVisibility trusts the persisted visibility column and only falls
// back to the mode-derived default for rows that predate the column (empty
// string can only appear if a migration path skipped the backfill).
func storedVisibility(stored string, mode string) Visibility {
	switch Visibility(stored) {
	case VisibilityUser, VisibilityInternal:
		return Visibility(stored)
	default:
		return visibilityForMode(mode)
	}
}

func visibilityForMode(mode string) Visibility {
	switch strings.TrimSpace(mode) {
	case TypeChat, TypeDiscuss:
		return VisibilityUser
	default:
		return VisibilityInternal
	}
}

// CreateInput holds input for creating a new thread.
type CreateInput struct {
	BotID           string
	BotAgentID      string
	RouteID         string
	ChannelType     string
	Type            string
	SessionMode     string
	RuntimeType     string
	Title           string
	Metadata        map[string]any
	RuntimeMetadata map[string]any
	ParentThreadID  string
	CreatedByUserID string
	// WorkdirID immutably binds the thread to a bot workdir. The caller
	// (handler layer) validates the workdir; thread persistence only stores
	// the reference so this package never imports the workdir domain.
	WorkdirID string
	// WorkdirPath is the resolved workdir directory, set if and only if
	// WorkdirID is set. For ACP threads it overrides the metadata
	// project_path so the runtime works in that directory; it also
	// leaves a creation-time path snapshot in the metadata for history.
	WorkdirPath string
	// Visibility overrides the default visibility derived from the session
	// mode. Schedule-created sessions use this to surface in user-facing
	// session lists while keeping session_mode='schedule' for prompt and
	// tool gating. Empty means "derive from mode".
	Visibility Visibility
}

// SubagentConfig is the persisted runtime selection for a managed subagent.
type SubagentConfig struct {
	ThreadID     string    `json:"session_id"`
	ModelUUID    string    `json:"model_uuid,omitempty"`
	ModelID      string    `json:"model_id"`
	ProviderName string    `json:"provider"`
	Forked       bool      `json:"fork"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// SubagentForkContextMessage is one hidden history message copied or
// materialized into a forked subagent session.
type SubagentForkContextMessage struct {
	SourceMessageID string          `json:"source_message_id,omitempty"`
	Role            string          `json:"role"`
	Message         json.RawMessage `json:"message"`
}

type CreateSubagentInput struct {
	Thread       CreateInput
	ModelUUID    string
	ModelID      string
	ProviderName string
	Forked       bool
	ForkContext  []SubagentForkContextMessage
}

type subagentTransactionalQueries interface {
	InTx(context.Context, func(dbstore.Queries) error) error
}

type sessionDescriptorTransactionalQueries interface {
	InTx(context.Context, func(dbstore.Queries) error) error
	SupportsTransactions() bool
}

type sessionDescriptorTransactionQueries interface {
	Queries
	LockBotForSessionWrite(context.Context, pgtype.UUID) (pgtype.UUID, error)
	LockSessionRuntimeFenceForActivation(context.Context, sqlc.LockSessionRuntimeFenceForActivationParams) (int64, error)
	NextSessionRuntimeFenceToken(context.Context) (int64, error)
	ActivateSessionRuntimeFence(context.Context, sqlc.ActivateSessionRuntimeFenceParams) (int64, error)
	DeleteAgentSessionStatesBySession(context.Context, pgtype.UUID) (int64, error)
	DeleteAgentSessionStateLinesBySession(context.Context, pgtype.UUID) (int64, error)
	DeleteAgentSessionPublicationsBySession(context.Context, pgtype.UUID) (int64, error)
}

// Queries is the storage surface owned by the Thread domain. Route lookup and
// route activation intentionally stay outside this contract.
type Queries interface {
	CountMessagesBySession(context.Context, pgtype.UUID) (int64, error)
	CreateSession(context.Context, sqlc.CreateSessionParams) (sqlc.BotSession, error)
	CreateSubagentConfig(context.Context, sqlc.CreateSubagentConfigParams) (sqlc.SubagentConfig, error)
	CreateSubagentForkContext(context.Context, sqlc.CreateSubagentForkContextParams) (sqlc.CreateSubagentForkContextRow, error)
	ForkSessionFromAssistantTurn(context.Context, sqlc.ForkSessionFromAssistantTurnParams) (sqlc.ForkSessionFromAssistantTurnRow, error)
	GetVisibleHistoryTurnByMessage(context.Context, sqlc.GetVisibleHistoryTurnByMessageParams) (dbstore.HistoryTurn, error)
	GetBotByID(context.Context, pgtype.UUID) (sqlc.GetBotByIDRow, error)
	GetSessionByID(context.Context, pgtype.UUID) (sqlc.BotSession, error)
	GetSubagentConfig(context.Context, pgtype.UUID) (sqlc.SubagentConfig, error)
	ListSessionsByBot(context.Context, pgtype.UUID) ([]sqlc.ListSessionsByBotRow, error)
	ListSessionsByBotAndCreatedByUser(context.Context, sqlc.ListSessionsByBotAndCreatedByUserParams) ([]sqlc.ListSessionsByBotAndCreatedByUserRow, error)
	ListSessionsByBotAndCreatedByUserPaged(context.Context, sqlc.ListSessionsByBotAndCreatedByUserPagedParams) ([]sqlc.ListSessionsByBotAndCreatedByUserPagedRow, error)
	ListSessionsByBotPaged(context.Context, sqlc.ListSessionsByBotPagedParams) ([]sqlc.ListSessionsByBotPagedRow, error)
	ListSessionsByRoute(context.Context, pgtype.UUID) ([]sqlc.BotSession, error)
	ListSubagentForkContext(context.Context, pgtype.UUID) ([]sqlc.ListSubagentForkContextRow, error)
	ListSubagentSessionsByParent(context.Context, pgtype.UUID) ([]sqlc.BotSession, error)
	SoftDeleteSession(context.Context, pgtype.UUID) error
	TouchSession(context.Context, pgtype.UUID) error
	UpdateSessionMetadata(context.Context, sqlc.UpdateSessionMetadataParams) (sqlc.BotSession, error)
	UpdateSessionRuntimeMetadata(context.Context, sqlc.UpdateSessionRuntimeMetadataParams) (sqlc.BotSession, error)
	UpdateSessionTitle(context.Context, sqlc.UpdateSessionTitleParams) (sqlc.BotSession, error)
	UpdateSessionTypeAndMetadata(context.Context, sqlc.UpdateSessionTypeAndMetadataParams) (sqlc.BotSession, error)
}

// ForkFromAssistantInput creates a new chat thread from the source thread's
// visible history through the assistant message's turn.
type ForkFromAssistantInput struct {
	BotID    string
	ThreadID string
	// RuntimeMetadataOverride replaces the clone's runtime metadata. External
	// runtime forks must pass it carrying the driver's freshly forked session
	// keys; without it the two Memoh sessions would share one runtime session.
	RuntimeMetadataOverride map[string]any
	// TurnID names the round the fork inherits through. A turn is the identity
	// a client holds while the round is still live, and the cut is turn-level
	// anyway, so the fork point is named by turn rather than by stored message.
	TurnID string
	// MessageID is the pre-turn spelling of TurnID.
	//
	// Deprecated: accepted so a client shipped against the message-id contract
	// keeps working after a server upgrade. It is resolved to the round that
	// contains it. Remove once the compatibility window closes.
	MessageID       string
	Title           string
	CreatedByUserID string
}

// Service manages bot chat threads.
type Service struct {
	queries           Queries
	hookService       *hooks.Service
	publisher         event.Publisher
	acpSetupValidator ACPSetupValidator
	logger            *slog.Logger
}

// ACPSetupValidation is the channel- and runtime-independent policy result
// needed before a thread can persist an ACP runtime descriptor.
type ACPSetupValidation struct {
	Known                 bool
	Enabled               bool
	MissingManagedFieldID string
}

// ACPSetupValidator is implemented by an Agent adapter. Thread owns this port
// so chat persistence never imports an Agent runtime implementation.
type ACPSetupValidator interface {
	ValidateACPSetup(agentID string, botMetadata map[string]any) ACPSetupValidation
}

// NewService creates a thread service. publisher may be nil — thread
// creation still succeeds when there is no event hub wired in (tests, or any
// caller that doesn't surface activity events).
func NewService(log *slog.Logger, queries Queries, publisher event.Publisher) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		queries:   queries,
		publisher: publisher,
		logger:    log.With(slog.String("service", "chat/thread")),
	}
}

func (s *Service) SetHookService(h *hooks.Service) {
	s.hookService = h
}

func (s *Service) SetACPSetupValidator(validator ACPSetupValidator) {
	if s == nil {
		return
	}
	s.acpSetupValidator = validator
}

// canonicalChannelType maps the product's own inbound surfaces (web composer,
// bundled CLI) to the single "local" channel type before persisting. The web
// REST session-create path has always stored "local", and the web UI treats
// any other non-empty channel_type as an external channel thread (read-only,
// channel badge), so adapter names like "web"/"cli" must not leak into
// bot_sessions. External channel adapters (telegram, discord, ...) pass
// through unchanged. Keep the surface list in sync with isLocalChannelType in
// internal/channel/inbound.
func canonicalChannelType(ct string) string {
	trimmed := strings.TrimSpace(ct)
	switch strings.ToLower(trimmed) {
	case "web", "cli":
		return "local"
	}
	return trimmed
}

// Create creates a new thread.
func (s *Service) Create(ctx context.Context, input CreateInput) (Thread, error) {
	pgBotID, err := dbpkg.ParseUUID(input.BotID)
	if err != nil {
		return Thread{}, fmt.Errorf("invalid bot id: %w", err)
	}
	pgRouteID, err := parseOptionalUUID(input.RouteID)
	if err != nil {
		return Thread{}, fmt.Errorf("invalid route id: %w", err)
	}
	pgBotAgentID, err := parseOptionalUUID(input.BotAgentID)
	if err != nil {
		return Thread{}, fmt.Errorf("invalid bot agent id: %w", err)
	}
	pgCreatedByUserID, err := parseOptionalUUID(input.CreatedByUserID)
	if err != nil {
		return Thread{}, fmt.Errorf("invalid created by user id: %w", err)
	}
	runtimeOwnerUserID := strings.TrimSpace(input.CreatedByUserID)

	channelType := pgtype.Text{}
	if ct := canonicalChannelType(input.ChannelType); ct != "" {
		channelType = pgtype.Text{String: ct, Valid: true}
	}

	meta := input.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	sessionType := strings.TrimSpace(input.Type)
	if sessionType == "" {
		sessionType = TypeChat
	}
	if !IsKnownType(sessionType) {
		return Thread{}, fmt.Errorf("unknown session type %q", sessionType)
	}
	desc, err := normalizeDescriptor(sessionType, input.SessionMode, input.RuntimeType, meta, input.RuntimeMetadata)
	if err != nil {
		return Thread{}, err
	}
	sessionType = desc.LegacyType
	meta = desc.Metadata
	runtimeMeta := desc.RuntimeMetadata
	if desc.RuntimeType == RuntimeACPAgent {
		meta = ApplyACPMetadataDefaults(meta)
		runtimeMeta = ApplyACPMetadataDefaults(runtimeMeta)
		meta = setACPRuntimeOwner(meta, runtimeOwnerUserID)
		runtimeMeta = setACPRuntimeOwner(runtimeMeta, runtimeOwnerUserID)
		meta = mergeACPMetadata(meta, runtimeMeta)
		runtimeMeta = mergeACPMetadata(runtimeMeta, meta)
		// A workdir binding owns the working directory: its resolved path
		// wins over any request-supplied project_path, and the metadata
		// keeps it as a creation-time snapshot.
		if strings.TrimSpace(input.WorkdirID) != "" && strings.TrimSpace(input.WorkdirPath) != "" {
			meta = overrideACPProjectPath(meta, input.WorkdirPath)
			runtimeMeta = overrideACPProjectPath(runtimeMeta, input.WorkdirPath)
		}
		if err := validateACPMetadata(meta); err != nil {
			return Thread{}, err
		}
		if err := s.validateACPCreatePolicy(ctx, pgBotID, meta, strings.TrimSpace(input.BotAgentID) == ""); err != nil {
			return Thread{}, err
		}
	} else if IsDirectRuntimeType(desc.RuntimeType) {
		meta = ApplyExternalMetadataDefaults(meta)
		runtimeMeta = ApplyExternalMetadataDefaults(runtimeMeta)
		meta = setACPRuntimeOwner(meta, runtimeOwnerUserID)
		runtimeMeta = setACPRuntimeOwner(runtimeMeta, runtimeOwnerUserID)
		if strings.TrimSpace(input.WorkdirID) != "" && strings.TrimSpace(input.WorkdirPath) != "" {
			meta = overrideACPProjectPath(meta, input.WorkdirPath)
			runtimeMeta = overrideACPProjectPath(runtimeMeta, input.WorkdirPath)
		}
		if err := validateExternalMetadata(meta); err != nil {
			return Thread{}, err
		}
	}
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		return Thread{}, fmt.Errorf("marshal metadata: %w", err)
	}
	runtimeMetaBytes, err := json.Marshal(nonNilMap(runtimeMeta))
	if err != nil {
		return Thread{}, fmt.Errorf("marshal runtime metadata: %w", err)
	}

	pgParentSessionID, err := parseOptionalUUID(input.ParentThreadID)
	if err != nil {
		return Thread{}, fmt.Errorf("invalid parent session id: %w", err)
	}
	pgWorkdirID, err := parseOptionalUUID(input.WorkdirID)
	if err != nil {
		return Thread{}, fmt.Errorf("invalid workdir id: %w", err)
	}

	visibility := input.Visibility
	switch visibility {
	case "":
		visibility = visibilityForMode(desc.SessionMode)
	case VisibilityUser, VisibilityInternal:
	default:
		return Thread{}, fmt.Errorf("unknown session visibility %q", visibility)
	}

	row, err := s.queries.CreateSession(ctx, sqlc.CreateSessionParams{
		BotID:           pgBotID,
		BotAgentID:      pgBotAgentID,
		RouteID:         pgRouteID,
		ChannelType:     channelType,
		Type:            sessionType,
		SessionMode:     desc.SessionMode,
		RuntimeType:     desc.RuntimeType,
		Visibility:      string(visibility),
		RuntimeMetadata: runtimeMetaBytes,
		Title:           input.Title,
		Metadata:        metaBytes,
		ParentSessionID: pgParentSessionID,
		CreatedByUserID: pgCreatedByUserID,
		WorkdirID:       pgWorkdirID,
	})
	if err != nil {
		return Thread{}, err
	}
	thread := toThread(row)
	s.publishThreadCreated(thread)
	s.runThreadStartHook(context.WithoutCancel(ctx), thread)
	return thread, nil
}

// CreateSubagent atomically creates the hidden child thread and its pinned
// model/fork configuration when the backing store supports transactions.
func (s *Service) CreateSubagent(ctx context.Context, input CreateSubagentInput) (Thread, SubagentConfig, error) {
	input.Thread.Type = TypeSubagent
	modelUUID, err := dbpkg.ParseUUID(input.ModelUUID)
	if err != nil {
		return Thread{}, SubagentConfig{}, fmt.Errorf("invalid subagent model uuid: %w", err)
	}
	if strings.TrimSpace(input.ModelID) == "" {
		return Thread{}, SubagentConfig{}, errors.New("subagent model_id is required")
	}
	if strings.TrimSpace(input.ProviderName) == "" {
		return Thread{}, SubagentConfig{}, errors.New("subagent provider is required")
	}
	if !input.Forked {
		input.ForkContext = nil
	} else if input.ForkContext == nil {
		input.ForkContext = []SubagentForkContextMessage{}
	}
	for i, message := range input.ForkContext {
		switch strings.TrimSpace(message.Role) {
		case "user", "assistant", "system", "tool":
		default:
			return Thread{}, SubagentConfig{}, fmt.Errorf("invalid fork context role at index %d", i)
		}
		if len(message.Message) == 0 || !json.Valid(message.Message) {
			return Thread{}, SubagentConfig{}, fmt.Errorf("invalid fork context message at index %d", i)
		}
		if strings.TrimSpace(message.SourceMessageID) != "" {
			if _, parseErr := dbpkg.ParseUUID(message.SourceMessageID); parseErr != nil {
				return Thread{}, SubagentConfig{}, fmt.Errorf("invalid fork context source message id at index %d: %w", i, parseErr)
			}
		}
	}
	forkContextJSON, err := json.Marshal(input.ForkContext)
	if err != nil {
		return Thread{}, SubagentConfig{}, fmt.Errorf("marshal fork context: %w", err)
	}

	// A subagent works inside its parent's workdir: same target, same
	// working directory. Callers never pass WorkdirID for subagents — it is
	// derived here so the inheritance cannot be forgotten at a call site.
	if strings.TrimSpace(input.Thread.WorkdirID) == "" {
		if parentID := strings.TrimSpace(input.Thread.ParentThreadID); parentID != "" {
			pgParentID, parseErr := dbpkg.ParseUUID(parentID)
			if parseErr != nil {
				return Thread{}, SubagentConfig{}, fmt.Errorf("invalid parent session id: %w", parseErr)
			}
			parentRow, parentErr := s.queries.GetSessionByID(ctx, pgParentID)
			if parentErr != nil && !errors.Is(parentErr, pgx.ErrNoRows) {
				return Thread{}, SubagentConfig{}, fmt.Errorf("load parent session: %w", parentErr)
			}
			if parentErr == nil && parentRow.WorkdirID.Valid {
				input.Thread.WorkdirID = parentRow.WorkdirID.String()
			}
		}
	}

	var created Thread
	var config SubagentConfig
	create := func(queries Queries) error {
		service := *s
		service.queries = queries
		service.publisher = nil
		service.hookService = nil
		createdThread, createErr := service.Create(ctx, input.Thread)
		if createErr != nil {
			return createErr
		}
		pgSessionID, createErr := dbpkg.ParseUUID(createdThread.ID)
		if createErr != nil {
			return createErr
		}
		row, createErr := queries.CreateSubagentConfig(ctx, sqlc.CreateSubagentConfigParams{
			SessionID:    pgSessionID,
			ModelUuid:    modelUUID,
			ModelID:      strings.TrimSpace(input.ModelID),
			ProviderName: strings.TrimSpace(input.ProviderName),
			Forked:       input.Forked,
		})
		if createErr != nil {
			return createErr
		}
		if input.Forked {
			parentSessionID, parseErr := dbpkg.ParseUUID(input.Thread.ParentThreadID)
			if parseErr != nil {
				return fmt.Errorf("invalid parent session id: %w", parseErr)
			}
			contextResult, contextErr := queries.CreateSubagentForkContext(ctx, sqlc.CreateSubagentForkContextParams{
				ParentSessionID: parentSessionID,
				SessionID:       pgSessionID,
				ContextMessages: forkContextJSON,
			})
			if contextErr != nil {
				return contextErr
			}
			if contextResult.InsertedCount != int64(len(input.ForkContext)) {
				return fmt.Errorf("persist fork context: inserted %d of %d messages", contextResult.InsertedCount, len(input.ForkContext))
			}
		}
		created = createdThread
		config = toSubagentConfig(row)
		return nil
	}

	if tx, ok := s.queries.(subagentTransactionalQueries); ok {
		err = tx.InTx(ctx, func(queries dbstore.Queries) error {
			return create(queries)
		})
	} else {
		err = create(s.queries)
	}
	if err != nil {
		return Thread{}, SubagentConfig{}, err
	}
	s.publishThreadCreated(created)
	s.runThreadStartHook(context.WithoutCancel(ctx), created)
	return created, config, nil
}

func (s *Service) GetSubagentConfig(ctx context.Context, sessionID string) (SubagentConfig, error) {
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return SubagentConfig{}, fmt.Errorf("invalid subagent session id: %w", err)
	}
	row, err := s.queries.GetSubagentConfig(ctx, pgSessionID)
	if err != nil {
		return SubagentConfig{}, err
	}
	return toSubagentConfig(row), nil
}

// ListSubagentForkContext loads only the hidden rows reserved for a forked
// subagent's inherited model context. Other invisible history rows are ignored.
func (s *Service) ListSubagentForkContext(ctx context.Context, sessionID string) ([]SubagentForkContextMessage, error) {
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return nil, fmt.Errorf("invalid subagent session id: %w", err)
	}
	rows, err := s.queries.ListSubagentForkContext(ctx, pgSessionID)
	if err != nil {
		return nil, err
	}
	messages := make([]SubagentForkContextMessage, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, SubagentForkContextMessage{
			Role:    row.Role,
			Message: append(json.RawMessage(nil), row.Content...),
		})
	}
	return messages, nil
}

// ForkFromAssistantTurn creates a new chat thread containing the source
// thread's visible linear history through the selected assistant turn.
func (s *Service) ForkFromAssistantTurn(ctx context.Context, input ForkFromAssistantInput) (Thread, error) {
	pgBotID, err := dbpkg.ParseUUID(input.BotID)
	if err != nil {
		return Thread{}, fmt.Errorf("invalid bot id: %w", err)
	}
	pgSessionID, err := dbpkg.ParseUUID(input.ThreadID)
	if err != nil {
		return Thread{}, fmt.Errorf("invalid session id: %w", err)
	}
	pgTurnID, err := s.forkTargetTurnID(ctx, pgSessionID, input)
	if err != nil {
		return Thread{}, err
	}
	pgCreatedByUserID, err := parseOptionalUUID(input.CreatedByUserID)
	if err != nil {
		return Thread{}, fmt.Errorf("invalid created by user id: %w", err)
	}

	sourceRow, err := s.queries.GetSessionByID(ctx, pgSessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Thread{}, ErrForkSourceNotFound
		}
		return Thread{}, err
	}
	source := toThread(sourceRow)
	if source.BotID != pgBotID.String() {
		return Thread{}, ErrForkSourceNotFound
	}
	if source.Type != TypeChat {
		return Thread{}, ErrForkSourceNotChat
	}
	// External runtime sessions must fork their runtime-side session too; a
	// caller that has not prepared one (no override) would leave two Memoh
	// sessions sharing a single runtime session.
	if IsDirectRuntime(source) && input.RuntimeMetadataOverride == nil {
		return Thread{}, ErrForkSourceNotChat
	}

	title := strings.TrimSpace(source.Title)
	if title == "" {
		title = "Untitled"
	}
	forkTitle := strings.TrimSpace(input.Title)
	if forkTitle == "" {
		forkTitle = title + " fork"
	}
	meta := nonNilMap(source.Metadata)
	// A fork is a new execution branch. It inherits conversation context, but
	// starts from the Bot's current Primary Computer instead of silently
	// carrying the source thread's last request-scoped target.
	delete(meta, "workspace_target_id")
	delete(meta, "workspace_target")
	meta["forked_from"] = map[string]any{
		"session_id": source.ID,
		"title":      title,
	}
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		return Thread{}, fmt.Errorf("marshal metadata: %w", err)
	}

	var runtimeMetadataOverrideBytes []byte
	if input.RuntimeMetadataOverride != nil {
		runtimeMetadataOverrideBytes, err = json.Marshal(input.RuntimeMetadataOverride)
		if err != nil {
			return Thread{}, fmt.Errorf("marshal fork runtime metadata: %w", err)
		}
	}

	row, err := s.queries.ForkSessionFromAssistantTurn(ctx, sqlc.ForkSessionFromAssistantTurnParams{
		SessionID:               pgSessionID,
		BotID:                   pgBotID,
		TurnID:                  pgTurnID,
		Title:                   forkTitle,
		Metadata:                metaBytes,
		RuntimeMetadataOverride: runtimeMetadataOverrideBytes,
		CreatedByUserID:         pgCreatedByUserID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Thread{}, ErrForkSourceNotReply
		}
		return Thread{}, err
	}
	thread := toThreadFromForkRow(row)
	s.publishThreadCreated(thread)
	s.runThreadStartHook(context.WithoutCancel(ctx), thread)
	return thread, nil
}

// forkTargetTurnID settles which round the fork inherits through. The turn id
// is the contract; the message id is the pre-turn spelling and is resolved to
// its round here, so the fork query only ever sees a turn.
//
// Deprecated behaviour: the message-id branch exists only for clients shipped
// before the turn-id contract. Remove it with the field.
func (s *Service) forkTargetTurnID(ctx context.Context, pgSessionID pgtype.UUID, input ForkFromAssistantInput) (pgtype.UUID, error) {
	if strings.TrimSpace(input.TurnID) != "" {
		pgTurnID, err := dbpkg.ParseUUID(input.TurnID)
		if err != nil {
			return pgtype.UUID{}, fmt.Errorf("invalid turn id: %w", err)
		}
		return pgTurnID, nil
	}
	pgMessageID, err := dbpkg.ParseUUID(input.MessageID)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid message id: %w", err)
	}
	turn, err := s.queries.GetVisibleHistoryTurnByMessage(ctx, sqlc.GetVisibleHistoryTurnByMessageParams{
		SessionID: pgSessionID,
		MessageID: pgMessageID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, ErrForkSourceNotReply
		}
		return pgtype.UUID{}, err
	}
	if !turn.ID.Valid {
		return pgtype.UUID{}, ErrForkSourceNotReply
	}
	return turn.ID, nil
}

// publishSessionCreated emits a session_created event for the new session.
// Best-effort: failures are logged but never fail the create.
func (s *Service) publishThreadCreated(thread Thread) {
	if s.publisher == nil {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"session_id": thread.ID,
		"bot_id":     thread.BotID,
		"type":       thread.Type,
		"title":      thread.Title,
		"created_at": thread.CreatedAt,
	})
	if err != nil {
		s.logger.Warn("marshal session_created event failed", slog.Any("error", err))
		return
	}
	s.publisher.Publish(event.Event{
		Type:  event.EventTypeSessionCreated,
		BotID: strings.TrimSpace(thread.BotID),
		Data:  payload,
	})
}

func (s *Service) runThreadStartHook(ctx context.Context, thread Thread) {
	if s == nil || s.hookService == nil {
		return
	}
	req := hooks.Request{
		Version:   1,
		Event:     hooks.EventSessionStart,
		BotID:     thread.BotID,
		SessionID: thread.ID,
		Workspace: hooks.WorkspaceInfo{
			CWD: hooks.DefaultWorkDir,
		},
		Turn: map[string]any{
			"session_type": thread.Type,
			"route_id":     thread.RouteID,
			"channel_type": thread.ChannelType,
		},
	}
	if _, err := s.hookService.Run(ctx, req, nil); err != nil {
		s.logger.Warn("session start hook failed",
			slog.String("bot_id", thread.BotID),
			slog.String("session_id", thread.ID),
			slog.Any("error", err),
		)
	}
}

// UpdateTypeAndMetadata updates a session's runtime type and metadata in one
// statement so callers don't expose a half-updated agent selection.
func (s *Service) UpdateTypeAndMetadata(ctx context.Context, sessionID, typ string, metadata map[string]any) (Thread, error) {
	return s.updateTypeAndMetadata(ctx, sessionID, typ, metadata, "")
}

// UpdateTypeAndMetadataWithOwner updates a session descriptor and binds any
// External Agent runtime ownership to a server-confirmed account id. The metadata owner
// field is never trusted from callers.
func (s *Service) UpdateTypeAndMetadataWithOwner(ctx context.Context, sessionID, typ string, metadata map[string]any, runtimeOwnerUserID string) (Thread, error) {
	return s.updateTypeAndMetadata(ctx, sessionID, typ, metadata, strings.TrimSpace(runtimeOwnerUserID))
}

func (s *Service) updateTypeAndMetadata(ctx context.Context, sessionID, typ string, metadata map[string]any, runtimeOwnerUserID string) (Thread, error) {
	return s.updateDescriptorAndMetadata(ctx, s.queries, sessionID, typ, "", "", metadata, nil, nil, strings.TrimSpace(runtimeOwnerUserID))
}

// UpdateDescriptorAndMetadataWithOwner updates the session mode/runtime
// descriptor directly. Callers that only patch metadata for a Phase 3 session
// must pass the existing descriptor so discuss+ACP sessions keep their runtime.
func (s *Service) UpdateDescriptorAndMetadataWithOwner(ctx context.Context, sessionID, typ, sessionMode, runtimeType string, metadata, runtimeMetadata map[string]any, botAgentID *string, runtimeOwnerUserID string) (Thread, error) {
	return s.updateDescriptorAndMetadata(ctx, s.queries, sessionID, typ, sessionMode, runtimeType, metadata, runtimeMetadata, botAgentID, strings.TrimSpace(runtimeOwnerUserID))
}

// UpdateEmptyDescriptorAndMetadataWithOwner updates a session runtime identity
// only while its visible history is still empty. PostgreSQL-backed stores lock
// the bot and session in the same transaction as the message check, descriptor
// update, runtime-fence bump, and ACP snapshot invalidation. The lock ordering
// matches runtime activation, so an already-running turn linearizes before the
// empty-history check and an old runtime token cannot write after the update.
func (s *Service) UpdateEmptyDescriptorAndMetadataWithOwner(ctx context.Context, sessionID, typ, sessionMode, runtimeType string, metadata, runtimeMetadata map[string]any, botAgentID *string, runtimeOwnerUserID string) (Thread, error) {
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return Thread{}, fmt.Errorf("invalid session id: %w", err)
	}
	existing, err := s.queries.GetSessionByID(ctx, pgSessionID)
	if err != nil {
		return Thread{}, err
	}
	pgBotID := existing.BotID

	update := func(queries Queries) (Thread, error) {
		count, countErr := queries.CountMessagesBySession(ctx, pgSessionID)
		if countErr != nil {
			return Thread{}, countErr
		}
		if count > 0 {
			return Thread{}, ErrSessionHasMessages
		}
		return s.updateDescriptorAndMetadata(ctx, queries, sessionID, typ, sessionMode, runtimeType, metadata, runtimeMetadata, botAgentID, strings.TrimSpace(runtimeOwnerUserID))
	}

	txer, ok := s.queries.(sessionDescriptorTransactionalQueries)
	if !ok || !txer.SupportsTransactions() {
		if _, fenced := runtimefence.ResetFromContext(ctx); fenced {
			return Thread{}, runtimefence.ErrTransactionsUnsupported
		}
		return update(s.queries)
	}

	var updated Thread
	err = txer.InTx(ctx, func(raw dbstore.Queries) error {
		queries, ok := raw.(sessionDescriptorTransactionQueries)
		if !ok {
			return errors.New("session descriptor transaction queries unavailable")
		}
		if _, lockErr := queries.LockBotForSessionWrite(ctx, pgBotID); lockErr != nil {
			return lockErr
		}
		if resetErr := runtimefence.ValidateResetLocked(ctx, raw, existing.BotID.String(), sessionID); resetErr != nil {
			return resetErr
		}
		if _, lockErr := queries.LockSessionRuntimeFenceForActivation(ctx, sqlc.LockSessionRuntimeFenceForActivationParams{
			SessionID: pgSessionID,
			BotID:     pgBotID,
		}); lockErr != nil {
			return lockErr
		}

		var updateErr error
		updated, updateErr = update(queries)
		if updateErr != nil {
			return updateErr
		}
		token, tokenErr := queries.NextSessionRuntimeFenceToken(ctx)
		if tokenErr != nil {
			return tokenErr
		}
		if _, activateErr := queries.ActivateSessionRuntimeFence(ctx, sqlc.ActivateSessionRuntimeFenceParams{
			RuntimeFencingToken: token,
			SessionID:           pgSessionID,
			BotID:               pgBotID,
		}); activateErr != nil {
			return activateErr
		}
		if _, deleteErr := queries.DeleteAgentSessionPublicationsBySession(ctx, pgSessionID); deleteErr != nil {
			return deleteErr
		}
		if _, deleteErr := queries.DeleteAgentSessionStatesBySession(ctx, pgSessionID); deleteErr != nil {
			return deleteErr
		}
		_, deleteErr := queries.DeleteAgentSessionStateLinesBySession(ctx, pgSessionID)
		return deleteErr
	})
	if err != nil {
		return Thread{}, err
	}
	return updated, nil
}

func (s *Service) updateDescriptorAndMetadata(ctx context.Context, queries Queries, sessionID, typ, sessionMode, runtimeType string, metadata, runtimeMetadata map[string]any, botAgentID *string, runtimeOwnerUserID string) (Thread, error) {
	pgID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return Thread{}, fmt.Errorf("invalid session id: %w", err)
	}
	sessionType := strings.TrimSpace(typ)
	if sessionType == "" {
		sessionType = TypeChat
	}
	if !IsKnownType(sessionType) {
		return Thread{}, fmt.Errorf("unknown session type %q", sessionType)
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	existing, err := queries.GetSessionByID(ctx, pgID)
	if err != nil {
		return Thread{}, err
	}
	pgBotAgentID := existing.BotAgentID
	if botAgentID != nil {
		value := strings.TrimSpace(*botAgentID)
		pgBotAgentID = pgtype.UUID{}
		if value != "" {
			pgBotAgentID, err = dbpkg.ParseUUID(value)
			if err != nil {
				return Thread{}, fmt.Errorf("invalid bot agent id: %w", err)
			}
		}
	}
	existingRuntimeMeta := parseJSONMap(existing.RuntimeMetadata)
	existingMeta := parseJSONMap(existing.Metadata)
	if existingRuntime := normalizeRuntimeType(existing.RuntimeType, existing.Type); existingRuntime == RuntimeACPAgent || IsDirectRuntimeType(existingRuntime) {
		existingRuntimeOwnerUserID := metadataString(existingRuntimeMeta, "runtime_owner_account_id")
		if existingRuntimeOwnerUserID == "" {
			existingRuntimeOwnerUserID = metadataString(existingMeta, "runtime_owner_account_id")
		}
		if existingRuntimeOwnerUserID != "" {
			runtimeOwnerUserID = existingRuntimeOwnerUserID
		}
	}
	if runtimeMetadata == nil {
		runtimeMetadata = existingRuntimeMeta
	}
	desc, err := normalizeDescriptor(sessionType, sessionMode, runtimeType, metadata, runtimeMetadata)
	if err != nil {
		return Thread{}, err
	}
	sessionType = desc.LegacyType
	metadata = desc.Metadata
	runtimeMeta := desc.RuntimeMetadata
	if desc.RuntimeType != RuntimeACPAgent && !IsDirectRuntimeType(desc.RuntimeType) {
		runtimeMeta = map[string]any{}
	}
	switch {
	case desc.RuntimeType == RuntimeACPAgent:
		metadata = ApplyACPMetadataDefaults(metadata)
		runtimeMeta = ApplyACPMetadataDefaults(runtimeMeta)
		metadata = setACPRuntimeOwner(metadata, runtimeOwnerUserID)
		runtimeMeta = setACPRuntimeOwner(runtimeMeta, runtimeOwnerUserID)
		metadata = mergeACPMetadata(metadata, runtimeMeta)
		runtimeMeta = mergeACPMetadata(runtimeMeta, metadata)
		if err := validateACPMetadata(metadata); err != nil {
			return Thread{}, err
		}
		if err := s.validateACPCreatePolicyWithQueries(ctx, queries, existing.BotID, metadata, !pgBotAgentID.Valid); err != nil {
			return Thread{}, err
		}
	case IsDirectRuntimeType(desc.RuntimeType):
		metadata = ApplyExternalMetadataDefaults(metadata)
		runtimeMeta = ApplyExternalMetadataDefaults(runtimeMeta)
		metadata = setACPRuntimeOwner(metadata, runtimeOwnerUserID)
		runtimeMeta = setACPRuntimeOwner(runtimeMeta, runtimeOwnerUserID)
		if err := validateExternalMetadata(metadata); err != nil {
			return Thread{}, err
		}
	}
	metaBytes, err := json.Marshal(metadata)
	if err != nil {
		return Thread{}, fmt.Errorf("marshal metadata: %w", err)
	}
	runtimeMetaBytes, err := json.Marshal(nonNilMap(runtimeMeta))
	if err != nil {
		return Thread{}, fmt.Errorf("marshal runtime metadata: %w", err)
	}
	row, err := queries.UpdateSessionTypeAndMetadata(ctx, sqlc.UpdateSessionTypeAndMetadataParams{
		ID:              pgID,
		Type:            sessionType,
		SessionMode:     desc.SessionMode,
		RuntimeType:     desc.RuntimeType,
		BotAgentID:      pgBotAgentID,
		RuntimeMetadata: runtimeMetaBytes,
		Metadata:        metaBytes,
	})
	if err != nil {
		return Thread{}, err
	}
	return toThread(row), nil
}

// Get returns a session by ID.
func (s *Service) Get(ctx context.Context, sessionID string) (Thread, error) {
	pgID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return Thread{}, fmt.Errorf("invalid session id: %w", err)
	}
	row, err := s.queries.GetSessionByID(ctx, pgID)
	if err != nil {
		return Thread{}, err
	}
	return toThread(row), nil
}

// ErrRuntimeMetadataStale reports that a runtime-metadata merge was skipped
// because the session no longer runs the expected runtime.
var ErrRuntimeMetadataStale = errors.New("session runtime changed; runtime metadata merge skipped")

// MergeRuntimeMetadata merges driver-owned keys into a session's runtime
// metadata; a nil value deletes the key. The write is guarded on runtimeType
// so a concurrent runtime switch turns the merge into ErrRuntimeMetadataStale
// instead of polluting another runtime's metadata, and on the caller's
// runtime fencing token so a superseded owner's late write after a cluster
// ownership handoff cannot clobber the new owner's runtime thread id.
func (s *Service) MergeRuntimeMetadata(ctx context.Context, sessionID, runtimeType string, delta map[string]any) (Thread, error) {
	pgID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return Thread{}, fmt.Errorf("invalid session id: %w", err)
	}
	row, err := s.queries.GetSessionByID(ctx, pgID)
	if err != nil {
		return Thread{}, err
	}
	if len(delta) == 0 {
		return toThread(row), nil
	}
	merged := parseJSONMap(row.RuntimeMetadata)
	if merged == nil {
		merged = map[string]any{}
	}
	for key, value := range delta {
		if value == nil {
			delete(merged, key)
			continue
		}
		merged[key] = value
	}
	mergedBytes, err := json.Marshal(merged)
	if err != nil {
		return Thread{}, fmt.Errorf("marshal runtime metadata: %w", err)
	}
	fencingToken := pgtype.Int8{}
	if fence, ok := runtimefence.FromContext(ctx); ok && fence.Token > 0 {
		fencingToken = pgtype.Int8{Int64: fence.Token, Valid: true}
	}
	updated, err := s.queries.UpdateSessionRuntimeMetadata(ctx, sqlc.UpdateSessionRuntimeMetadataParams{
		ID:              pgID,
		RuntimeType:     strings.TrimSpace(runtimeType),
		FencingToken:    fencingToken,
		RuntimeMetadata: mergedBytes,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Thread{}, ErrRuntimeMetadataStale
	}
	if err != nil {
		return Thread{}, err
	}
	return toThread(updated), nil
}

// ListByBot returns all active sessions for a bot.
func (s *Service) ListByBot(ctx context.Context, botID string) ([]Thread, error) {
	pgBotID, err := dbpkg.ParseUUID(botID)
	if err != nil {
		return nil, fmt.Errorf("invalid bot id: %w", err)
	}
	rows, err := s.queries.ListSessionsByBot(ctx, pgBotID)
	if err != nil {
		return nil, err
	}
	threads := make([]Thread, 0, len(rows))
	for _, row := range rows {
		threads = append(threads, toThreadFromListRow(row))
	}
	return threads, nil
}

// Cursor identifies the position in a thread listing for keyset
// pagination. The zero value means "start from the head".
type Cursor struct {
	UpdatedAt time.Time
	ID        string
}

type ListFilter struct {
	ParentThreadID string
	// WorkdirID filters to sessions bound to one workdir. Mutually
	// exclusive with WorkdirUnassigned.
	WorkdirID string
	// WorkdirUnassigned filters to sessions with no workdir binding — the
	// sidebar's ungrouped bucket.
	WorkdirUnassigned bool
	// Visibility filters to sessions with the given stored visibility.
	// Empty means no visibility predicate. The default session listing
	// passes VisibilityUser so schedule-created sessions surface by
	// visibility rather than by widening the legacy type filter.
	Visibility Visibility
}

// IsZero reports whether the cursor carries neither half — the start-of-list
// signal that pagedCursorParams maps to "no cursor predicate". A
// partially-populated cursor (only the timestamp or only the id) is not zero;
// pagedCursorParams rejects it as a programmer error so we never send
// malformed bindings down to SQL.
func (c Cursor) IsZero() bool {
	return c.ID == "" && c.UpdatedAt.IsZero()
}

// ListByBotPaged returns one page of sessions for a bot, filtered to the given
// types and starting after the given cursor. Callers that want a "has more"
// signal pass limit+1 and look for an extra row.
func (s *Service) ListByBotPaged(ctx context.Context, botID string, types []string, cursor Cursor, limit int64) ([]Thread, error) {
	return s.ListByBotPagedWithFilter(ctx, botID, types, cursor, limit, ListFilter{})
}

func (s *Service) ListByBotPagedWithFilter(ctx context.Context, botID string, types []string, cursor Cursor, limit int64, filter ListFilter) ([]Thread, error) {
	pgBotID, err := dbpkg.ParseUUID(botID)
	if err != nil {
		return nil, fmt.Errorf("invalid bot id: %w", err)
	}
	parentSessionID, useParentSession, err := pagedParentSessionParam(filter)
	if err != nil {
		return nil, err
	}
	workdirID, workdirUnassigned, useWorkdir, err := pagedWorkdirParams(filter)
	if err != nil {
		return nil, err
	}
	visibility, useVisibility, err := pagedVisibilityParam(filter)
	if err != nil {
		return nil, err
	}
	cursorUpdatedAt, cursorID, useCursor, err := pagedCursorParams(cursor)
	if err != nil {
		return nil, err
	}
	limitParam, err := pagedLimitToInt32(limit)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListSessionsByBotPaged(ctx, sqlc.ListSessionsByBotPagedParams{
		BotID:             pgBotID,
		Types:             types,
		UseParentSession:  useParentSession,
		ParentSessionID:   parentSessionID,
		UseWorkdir:        useWorkdir,
		WorkdirUnassigned: workdirUnassigned,
		WorkdirID:         workdirID,
		UseVisibility:     useVisibility,
		Visibility:        visibility,
		UseCursor:         useCursor,
		CursorUpdatedAt:   cursorUpdatedAt,
		CursorID:          cursorID,
		LimitCount:        limitParam,
	})
	if err != nil {
		return nil, err
	}
	threads := make([]Thread, 0, len(rows))
	for _, row := range rows {
		threads = append(threads, toThreadFromPagedRow(row))
	}
	return threads, nil
}

// ListByBotAndCreatedByUserPaged is the paged variant scoped to a single user.
func (s *Service) ListByBotAndCreatedByUserPaged(ctx context.Context, botID, userID string, types []string, cursor Cursor, limit int64) ([]Thread, error) {
	return s.ListByBotAndCreatedByUserPagedWithFilter(ctx, botID, userID, types, cursor, limit, ListFilter{})
}

func (s *Service) ListByBotAndCreatedByUserPagedWithFilter(ctx context.Context, botID, userID string, types []string, cursor Cursor, limit int64, filter ListFilter) ([]Thread, error) {
	pgBotID, err := dbpkg.ParseUUID(botID)
	if err != nil {
		return nil, fmt.Errorf("invalid bot id: %w", err)
	}
	pgUserID, err := dbpkg.ParseUUID(userID)
	if err != nil {
		return nil, fmt.Errorf("invalid user id: %w", err)
	}
	parentSessionID, useParentSession, err := pagedParentSessionParam(filter)
	if err != nil {
		return nil, err
	}
	workdirID, workdirUnassigned, useWorkdir, err := pagedWorkdirParams(filter)
	if err != nil {
		return nil, err
	}
	visibility, useVisibility, err := pagedVisibilityParam(filter)
	if err != nil {
		return nil, err
	}
	cursorUpdatedAt, cursorID, useCursor, err := pagedCursorParams(cursor)
	if err != nil {
		return nil, err
	}
	limitParam, err := pagedLimitToInt32(limit)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListSessionsByBotAndCreatedByUserPaged(ctx, sqlc.ListSessionsByBotAndCreatedByUserPagedParams{
		BotID:             pgBotID,
		CreatedByUserID:   pgUserID,
		Types:             types,
		UseParentSession:  useParentSession,
		ParentSessionID:   parentSessionID,
		UseWorkdir:        useWorkdir,
		WorkdirUnassigned: workdirUnassigned,
		WorkdirID:         workdirID,
		UseVisibility:     useVisibility,
		Visibility:        visibility,
		UseCursor:         useCursor,
		CursorUpdatedAt:   cursorUpdatedAt,
		CursorID:          cursorID,
		LimitCount:        limitParam,
	})
	if err != nil {
		return nil, err
	}
	threads := make([]Thread, 0, len(rows))
	for _, row := range rows {
		threads = append(threads, toThreadFromUserPagedRow(row))
	}
	return threads, nil
}

// pagedLimitToInt32 narrows the int64 page-size that flows through the
// service signatures into the int32 sqlc binds. The handler caps the user-
// supplied limit at sessionListMaxLimit and bumps it by one for the
// has-more probe; both fit in int32 by construction, so any out-of-range value
// here is a programmer error and surfaces as such.
func pagedLimitToInt32(limit int64) (int32, error) {
	if limit < 1 || limit > math.MaxInt32 {
		return 0, fmt.Errorf("session: paged limit %d is out of range", limit)
	}
	return int32(limit), nil
}

func pagedParentSessionParam(filter ListFilter) (pgtype.UUID, bool, error) {
	parentID := strings.TrimSpace(filter.ParentThreadID)
	if parentID == "" {
		return pgtype.UUID{}, false, nil
	}
	parsed, err := dbpkg.ParseUUID(parentID)
	if err != nil {
		return pgtype.UUID{}, false, fmt.Errorf("invalid parent session id: %w", err)
	}
	return parsed, true, nil
}

// pagedVisibilityParam maps the optional visibility filter onto the SQL
// binds, rejecting values outside the stored vocabulary so a typo can never
// silently return an empty listing.
func pagedVisibilityParam(filter ListFilter) (string, bool, error) {
	switch filter.Visibility {
	case "":
		return "", false, nil
	case VisibilityUser, VisibilityInternal:
		return string(filter.Visibility), true, nil
	default:
		return "", false, fmt.Errorf("session: unknown visibility filter %q", filter.Visibility)
	}
}

// pagedWorkdirParams maps the three-state workdir filter (off / one workdir /
// unassigned bucket) onto the SQL binds.
func pagedWorkdirParams(filter ListFilter) (workdirID pgtype.UUID, unassigned, useWorkdir bool, err error) {
	id := strings.TrimSpace(filter.WorkdirID)
	if filter.WorkdirUnassigned {
		if id != "" {
			return pgtype.UUID{}, false, false, errors.New("session: workdir filter cannot combine a workdir id with unassigned")
		}
		return pgtype.UUID{}, true, true, nil
	}
	if id == "" {
		return pgtype.UUID{}, false, false, nil
	}
	parsed, parseErr := dbpkg.ParseUUID(id)
	if parseErr != nil {
		return pgtype.UUID{}, false, false, fmt.Errorf("invalid workdir id: %w", parseErr)
	}
	return parsed, false, true, nil
}

func pagedCursorParams(cursor Cursor) (pgtype.Timestamptz, pgtype.UUID, bool, error) {
	if cursor.IsZero() {
		return pgtype.Timestamptz{}, pgtype.UUID{}, false, nil
	}
	if cursor.ID == "" || cursor.UpdatedAt.IsZero() {
		// The handler-side decoder rejects half-built cursors as 400, so by the
		// time we get here a partial cursor is an internal-construction bug.
		// Surface it loudly rather than silently restarting from the head and
		// returning a duplicate page.
		return pgtype.Timestamptz{}, pgtype.UUID{}, false, errors.New("session: cursor must carry both updated_at and id")
	}
	pgID, err := dbpkg.ParseUUID(cursor.ID)
	if err != nil {
		return pgtype.Timestamptz{}, pgtype.UUID{}, false, fmt.Errorf("invalid cursor id: %w", err)
	}
	return pgtype.Timestamptz{Time: cursor.UpdatedAt, Valid: true}, pgID, true, nil
}

// ListByBotAndCreatedByUser returns all active sessions for a bot created by a user.
func (s *Service) ListByBotAndCreatedByUser(ctx context.Context, botID, userID string) ([]Thread, error) {
	pgBotID, err := dbpkg.ParseUUID(botID)
	if err != nil {
		return nil, fmt.Errorf("invalid bot id: %w", err)
	}
	pgUserID, err := dbpkg.ParseUUID(userID)
	if err != nil {
		return nil, fmt.Errorf("invalid user id: %w", err)
	}
	rows, err := s.queries.ListSessionsByBotAndCreatedByUser(ctx, sqlc.ListSessionsByBotAndCreatedByUserParams{
		BotID:           pgBotID,
		CreatedByUserID: pgUserID,
	})
	if err != nil {
		return nil, err
	}
	threads := make([]Thread, 0, len(rows))
	for _, row := range rows {
		threads = append(threads, toThreadFromUserListRow(row))
	}
	return threads, nil
}

// ListByRoute returns all active sessions for a route.
func (s *Service) ListByRoute(ctx context.Context, routeID string) ([]Thread, error) {
	pgRouteID, err := dbpkg.ParseUUID(routeID)
	if err != nil {
		return nil, fmt.Errorf("invalid route id: %w", err)
	}
	rows, err := s.queries.ListSessionsByRoute(ctx, pgRouteID)
	if err != nil {
		return nil, err
	}
	threads := make([]Thread, 0, len(rows))
	for _, row := range rows {
		threads = append(threads, toThread(row))
	}
	return threads, nil
}

// ListSubagentsByParent returns active subagent sessions created under a
// parent session.
func (s *Service) ListSubagentsByParent(ctx context.Context, parentSessionID string) ([]Thread, error) {
	pgParentSessionID, err := dbpkg.ParseUUID(parentSessionID)
	if err != nil {
		return nil, fmt.Errorf("invalid parent session id: %w", err)
	}
	rows, err := s.queries.ListSubagentSessionsByParent(ctx, pgParentSessionID)
	if err != nil {
		return nil, err
	}
	threads := make([]Thread, 0, len(rows))
	for _, row := range rows {
		threads = append(threads, toThread(row))
	}
	return threads, nil
}

// UpdateTitle updates a session's title.
func (s *Service) UpdateTitle(ctx context.Context, sessionID, title string) (Thread, error) {
	pgID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return Thread{}, fmt.Errorf("invalid session id: %w", err)
	}
	var row sqlc.BotSession
	if fence, fenced := runtimefence.FromContext(ctx); fenced {
		if strings.TrimSpace(sessionID) != fence.SessionID {
			return Thread{}, runtimefence.ErrStale
		}
		writer, ok := s.queries.(runtimeFencedThreadWriter)
		if !ok {
			return Thread{}, errors.New("session store does not support runtime fencing")
		}
		pgBotID, parseErr := dbpkg.ParseUUID(fence.BotID)
		if parseErr != nil {
			return Thread{}, fmt.Errorf("invalid runtime fence bot id: %w", parseErr)
		}
		row, err = writer.UpdateSessionTitleWithRuntimeFence(ctx, sqlc.UpdateSessionTitleWithRuntimeFenceParams{
			Title:               title,
			ID:                  pgID,
			BotID:               pgBotID,
			RuntimeFencingToken: fence.Token,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return Thread{}, runtimefence.ErrStale
		}
	} else {
		row, err = s.queries.UpdateSessionTitle(ctx, sqlc.UpdateSessionTitleParams{
			ID:    pgID,
			Title: title,
		})
	}
	if err != nil {
		return Thread{}, err
	}
	return toThread(row), nil
}

// UpdateMetadata updates a session's metadata.
func (s *Service) UpdateMetadata(ctx context.Context, sessionID string, metadata map[string]any) (Thread, error) {
	pgID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return Thread{}, fmt.Errorf("invalid session id: %w", err)
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	metaBytes, err := json.Marshal(metadata)
	if err != nil {
		return Thread{}, fmt.Errorf("marshal metadata: %w", err)
	}
	var row sqlc.BotSession
	if fence, fenced := runtimefence.FromContext(ctx); fenced {
		if strings.TrimSpace(sessionID) != fence.SessionID {
			return Thread{}, runtimefence.ErrStale
		}
		writer, ok := s.queries.(runtimeFencedThreadWriter)
		if !ok {
			return Thread{}, errors.New("session store does not support runtime fencing")
		}
		pgBotID, parseErr := dbpkg.ParseUUID(fence.BotID)
		if parseErr != nil {
			return Thread{}, fmt.Errorf("invalid runtime fence bot id: %w", parseErr)
		}
		row, err = writer.UpdateSessionMetadataWithRuntimeFence(ctx, sqlc.UpdateSessionMetadataWithRuntimeFenceParams{
			Metadata:            metaBytes,
			ID:                  pgID,
			BotID:               pgBotID,
			RuntimeFencingToken: fence.Token,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return Thread{}, runtimefence.ErrStale
		}
	} else {
		row, err = s.queries.UpdateSessionMetadata(ctx, sqlc.UpdateSessionMetadataParams{
			ID:       pgID,
			Metadata: metaBytes,
		})
	}
	if err != nil {
		return Thread{}, err
	}
	return toThread(row), nil
}

// SoftDelete marks a session as deleted.
func (s *Service) SoftDelete(ctx context.Context, sessionID string) error {
	pgID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return fmt.Errorf("invalid session id: %w", err)
	}
	if _, fenced := runtimefence.ResetFromContext(ctx); !fenced {
		return s.queries.SoftDeleteSession(ctx, pgID)
	}
	queries, ok := s.queries.(dbstore.Queries)
	if !ok {
		return runtimefence.ErrTransactionsUnsupported
	}
	return runtimefence.InResetTransaction(ctx, queries, "", sessionID, func(txQueries dbstore.Queries) error {
		return txQueries.SoftDeleteSession(ctx, pgID)
	})
}

func (s *Service) MessageCount(ctx context.Context, sessionID string) (int64, error) {
	pgID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return 0, fmt.Errorf("invalid session id: %w", err)
	}
	return s.queries.CountMessagesBySession(ctx, pgID)
}

// Touch updates a session's updated_at timestamp.
func (s *Service) Touch(ctx context.Context, sessionID string) error {
	pgID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return fmt.Errorf("invalid session id: %w", err)
	}
	return s.queries.TouchSession(ctx, pgID)
}

func toThread(row sqlc.BotSession) Thread {
	parentID := ""
	if row.ParentSessionID.Valid {
		parentID = row.ParentSessionID.String()
	}
	createdByUserID := ""
	if row.CreatedByUserID.Valid {
		createdByUserID = row.CreatedByUserID.String()
	}
	workdirID := ""
	if row.WorkdirID.Valid {
		workdirID = row.WorkdirID.String()
	}
	sessionMode := normalizeSessionMode(row.SessionMode, row.Type)
	return Thread{
		ID:              row.ID.String(),
		BotID:           row.BotID.String(),
		BotAgentID:      row.BotAgentID.String(),
		RouteID:         row.RouteID.String(),
		ChannelType:     dbpkg.TextToString(row.ChannelType),
		Type:            row.Type,
		SessionMode:     sessionMode,
		RuntimeType:     normalizeRuntimeType(row.RuntimeType, row.Type),
		RuntimeMetadata: parseJSONMap(row.RuntimeMetadata),
		Title:           row.Title,
		Metadata:        parseJSONMap(row.Metadata),
		ParentThreadID:  parentID,
		CreatedByUserID: createdByUserID,
		WorkdirID:       workdirID,
		CreatedAt:       row.CreatedAt.Time,
		UpdatedAt:       row.UpdatedAt.Time,
		Visibility:      storedVisibility(row.Visibility, sessionMode),
	}
}

func toSubagentConfig(row sqlc.SubagentConfig) SubagentConfig {
	modelUUID := ""
	if row.ModelUuid.Valid {
		modelUUID = row.ModelUuid.String()
	}
	return SubagentConfig{
		ThreadID:     row.SessionID.String(),
		ModelUUID:    modelUUID,
		ModelID:      row.ModelID,
		ProviderName: row.ProviderName,
		Forked:       row.Forked,
		CreatedAt:    row.CreatedAt.Time,
		UpdatedAt:    row.UpdatedAt.Time,
	}
}

func toThreadFromForkRow(row sqlc.ForkSessionFromAssistantTurnRow) Thread {
	return toThread(sqlc.BotSession(row))
}

func validateACPMetadata(meta map[string]any) error {
	if strings.TrimSpace(metadataString(meta, "acp_agent_id")) == "" {
		return ErrACPAgentIDRequired
	}
	if strings.TrimSpace(metadataString(meta, "project_path")) == "" {
		return ErrACPProjectPathMissing
	}
	if strings.TrimSpace(metadataString(meta, "runtime_owner_account_id")) == "" {
		return ErrACPRuntimeOwnerMissing
	}
	switch strings.TrimSpace(metadataString(meta, "acp_project_mode")) {
	case "", DefaultACPProjectMode, "none":
	default:
		return fmt.Errorf("%w %q", ErrACPProjectModeInvalid, metadataString(meta, "acp_project_mode"))
	}
	return nil
}

func validateExternalMetadata(meta map[string]any) error {
	if strings.TrimSpace(metadataString(meta, "runtime_owner_account_id")) == "" {
		return ErrExternalRuntimeOwnerMissing
	}
	return nil
}

// ApplyExternalMetadataDefaults fills omitted working-directory metadata for
// direct external runtime sessions. Unlike ACP there is no project mode: the
// runtime always works inside the bot workspace.
func ApplyExternalMetadataDefaults(meta map[string]any) map[string]any {
	out := nonNilMap(meta)
	if strings.TrimSpace(metadataString(out, "project_path")) == "" {
		out["project_path"] = DefaultACPProjectPath
	}
	return out
}

// ApplyACPMetadataDefaults fills omitted ACP session project fields.
func ApplyACPMetadataDefaults(meta map[string]any) map[string]any {
	out := make(map[string]any, len(meta)+2)
	for key, value := range meta {
		out[key] = value
	}
	if strings.TrimSpace(metadataString(out, "project_path")) == "" {
		out["project_path"] = DefaultACPProjectPath
	}
	if strings.TrimSpace(metadataString(out, "acp_project_mode")) == "" {
		out["acp_project_mode"] = DefaultACPProjectMode
	}
	return out
}

// overrideACPProjectPath forces the ACP working directory onto a workdir's
// resolved path. Unlike ApplyACPMetadataDefaults this is not a fallback:
// a workdir binding owns the directory, so a request-supplied project_path
// never wins over it.
func overrideACPProjectPath(meta map[string]any, workdirPath string) map[string]any {
	out := make(map[string]any, len(meta)+2)
	for key, value := range meta {
		out[key] = value
	}
	out["project_path"] = strings.TrimSpace(workdirPath)
	out["acp_project_mode"] = DefaultACPProjectMode
	return out
}

type descriptor struct {
	LegacyType      string
	SessionMode     string
	RuntimeType     string
	Metadata        map[string]any
	RuntimeMetadata map[string]any
}

// ResolveDescriptor returns the normalized compatibility type plus split
// session-mode/runtime descriptor without applying metadata side effects.
func ResolveDescriptor(legacyType, sessionMode, runtimeType string) (string, string, string, error) {
	// Reject a contradictory request body up front: the legacy acp_agent type
	// unambiguously means an ACP runtime, so an explicit non-ACP runtime_type
	// alongside it must fail loudly rather than silently degrade to a plain
	// model chat session.
	if rt := strings.TrimSpace(runtimeType); strings.TrimSpace(legacyType) == TypeACPAgent && rt != "" && rt != RuntimeACPAgent {
		return "", "", "", fmt.Errorf("session type %q conflicts with runtime_type %q", TypeACPAgent, rt)
	}
	desc, err := normalizeDescriptor(legacyType, sessionMode, runtimeType, nil, nil)
	if err != nil {
		return "", "", "", err
	}
	return desc.LegacyType, desc.SessionMode, desc.RuntimeType, nil
}

func normalizeDescriptor(legacyType, sessionMode, runtimeType string, metadata, runtimeMetadata map[string]any) (descriptor, error) {
	legacyType = strings.TrimSpace(legacyType)
	sessionMode = strings.TrimSpace(sessionMode)
	runtimeType = strings.TrimSpace(runtimeType)
	if legacyType == "" {
		legacyType = TypeChat
	}
	if sessionMode == "" || runtimeType == "" {
		derivedMode, derivedRuntime := descriptorFromLegacyType(legacyType)
		if sessionMode == "" {
			sessionMode = derivedMode
		}
		if runtimeType == "" {
			runtimeType = derivedRuntime
		}
	}
	if !IsKnownSessionMode(sessionMode) {
		return descriptor{}, fmt.Errorf("unknown session mode %q", sessionMode)
	}
	if !IsKnownRuntimeType(runtimeType) {
		return descriptor{}, fmt.Errorf("unknown runtime type %q", runtimeType)
	}
	// The runtime capability table owns which modes each runtime can host
	// (e.g. agent runtimes never back subagent loops).
	if !runtimekind.SupportsSessionMode(runtimeType, sessionMode) {
		return descriptor{}, fmt.Errorf("runtime type %q is only supported for %s session modes", runtimeType, strings.Join(runtimekind.SupportedSessionModes(runtimeType), ", "))
	}
	out := descriptor{
		LegacyType:      legacyTypeForDescriptor(sessionMode, runtimeType),
		SessionMode:     sessionMode,
		RuntimeType:     runtimeType,
		Metadata:        nonNilMap(metadata),
		RuntimeMetadata: nonNilMap(runtimeMetadata),
	}
	if runtimeType == RuntimeACPAgent {
		out.RuntimeMetadata = mergeACPMetadata(out.RuntimeMetadata, out.Metadata)
		out.Metadata = mergeACPMetadata(out.Metadata, out.RuntimeMetadata)
	}
	return out, nil
}

func descriptorFromLegacyType(typ string) (string, string) {
	switch strings.TrimSpace(typ) {
	case TypeACPAgent:
		return TypeChat, RuntimeACPAgent
	case TypeDiscuss:
		return TypeDiscuss, RuntimeModel
	case TypeSchedule:
		return TypeSchedule, RuntimeModel
	case TypeSubagent:
		return TypeSubagent, RuntimeModel
	default:
		return TypeChat, RuntimeModel
	}
}

// DescriptorFromLegacyType maps the legacy single type column to the split
// session mode/runtime descriptor used by new code paths.
func DescriptorFromLegacyType(typ string) (string, string) {
	return descriptorFromLegacyType(typ)
}

func legacyTypeForDescriptor(sessionMode, runtimeType string) string {
	if runtimeType == RuntimeACPAgent && sessionMode == TypeChat {
		return TypeACPAgent
	}
	return sessionMode
}

// LegacyTypeForDescriptor returns the compatibility type value for a split
// session descriptor.
func LegacyTypeForDescriptor(sessionMode, runtimeType string) string {
	return legacyTypeForDescriptor(sessionMode, runtimeType)
}

func IsKnownSessionMode(mode string) bool {
	switch strings.TrimSpace(mode) {
	case TypeChat, TypeDiscuss, TypeSchedule, TypeSubagent:
		return true
	default:
		return false
	}
}

func IsKnownRuntimeType(runtimeType string) bool {
	return runtimekind.Valid(runtimeType)
}

// IsACPRuntime reports whether a session is backed by an ACP runtime. It keeps
// legacy chat ACP sessions (`type=acp_agent`) working while allowing newer
// descriptors such as `session_mode=discuss` + `runtime_type=acp_agent`.
func IsACPRuntime(thread Thread) bool {
	return normalizeRuntimeType(thread.RuntimeType, thread.Type) == RuntimeACPAgent
}

// SupportsSkillActivation reports whether a session shape can accept
// user-requested skill activation: chat mode (with the usual legacy-type
// fallback) on the built-in model runtime. This is the single definition for
// the rule — the web WS pre-check, the channel inbound gate, and the flow
// resolver's final guard all call it, so the three surfaces cannot drift.
func SupportsSkillActivation(sessionMode, legacyType, runtimeType string) bool {
	return normalizeSessionMode(sessionMode, legacyType) == TypeChat &&
		normalizeRuntimeType(runtimeType, legacyType) == RuntimeModel
}

func normalizeSessionMode(mode, legacyType string) string {
	if IsKnownSessionMode(mode) {
		return strings.TrimSpace(mode)
	}
	derived, _ := descriptorFromLegacyType(legacyType)
	return derived
}

func normalizeRuntimeType(runtimeType, legacyType string) string {
	if IsKnownRuntimeType(runtimeType) {
		return strings.TrimSpace(runtimeType)
	}
	_, derived := descriptorFromLegacyType(legacyType)
	return derived
}

func mergeACPMetadata(base, overlay map[string]any) map[string]any {
	out := nonNilMap(base)
	for _, key := range []string{"acp_agent_id", "project_path", "acp_project_mode", "runtime_owner_account_id"} {
		if value, ok := overlay[key]; ok {
			out[key] = value
		}
	}
	return out
}

func setACPRuntimeOwner(metadata map[string]any, ownerUserID string) map[string]any {
	out := nonNilMap(metadata)
	ownerUserID = strings.TrimSpace(ownerUserID)
	if ownerUserID == "" {
		delete(out, "runtime_owner_account_id")
		return out
	}
	out["runtime_owner_account_id"] = ownerUserID
	return out
}

func nonNilMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func (s *Service) validateACPCreatePolicy(ctx context.Context, botID pgtype.UUID, meta map[string]any, requireLegacyEnabledOption ...bool) error {
	requireLegacyEnabled := true
	if len(requireLegacyEnabledOption) > 0 {
		requireLegacyEnabled = requireLegacyEnabledOption[0]
	}
	return s.validateACPCreatePolicyWithQueries(ctx, s.queries, botID, meta, requireLegacyEnabled)
}

func (s *Service) validateACPCreatePolicyWithQueries(ctx context.Context, queries Queries, botID pgtype.UUID, meta map[string]any, requireLegacyEnabled bool) error {
	agentID := metadataString(meta, "acp_agent_id")
	if s.acpSetupValidator == nil {
		return fmt.Errorf("%w: ACP setup validator unavailable", ErrACPAgentNotConfigured)
	}
	if validation := s.acpSetupValidator.ValidateACPSetup(agentID, nil); !validation.Known {
		return fmt.Errorf("%w: %s", ErrACPUnknownAgent, agentID)
	}
	bot, err := queries.GetBotByID(ctx, botID)
	if err != nil {
		return err
	}
	botMeta := parseJSONMap(bot.Metadata)
	validation := s.acpSetupValidator.ValidateACPSetup(agentID, botMeta)
	if requireLegacyEnabled && !validation.Enabled {
		return fmt.Errorf("%w: %s", ErrACPAgentNotEnabled, agentID)
	}
	if validation.MissingManagedFieldID != "" {
		return fmt.Errorf("%w: %s missing %s", ErrACPAgentNotConfigured, agentID, validation.MissingManagedFieldID)
	}
	return nil
}

func metadataString(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	value, _ := meta[key].(string)
	return strings.TrimSpace(value)
}

func parseOptionalUUID(id string) (pgtype.UUID, error) {
	if strings.TrimSpace(id) == "" {
		return pgtype.UUID{}, nil
	}
	return dbpkg.ParseUUID(id)
}

func parseJSONMap(data []byte) map[string]any {
	if len(data) == 0 {
		return nil
	}
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	return m
}

func toThreadFromListRow(row sqlc.ListSessionsByBotRow) Thread {
	parentID := ""
	if row.ParentSessionID.Valid {
		parentID = row.ParentSessionID.String()
	}
	createdByUserID := ""
	if row.CreatedByUserID.Valid {
		createdByUserID = row.CreatedByUserID.String()
	}
	workdirID := ""
	if row.WorkdirID.Valid {
		workdirID = row.WorkdirID.String()
	}
	sessionMode := normalizeSessionMode(row.SessionMode, row.Type)
	return Thread{
		ID:              row.ID.String(),
		BotID:           row.BotID.String(),
		BotAgentID:      row.BotAgentID.String(),
		RouteID:         row.RouteID.String(),
		ChannelType:     dbpkg.TextToString(row.ChannelType),
		Type:            row.Type,
		SessionMode:     sessionMode,
		RuntimeType:     normalizeRuntimeType(row.RuntimeType, row.Type),
		RuntimeMetadata: parseJSONMap(row.RuntimeMetadata),
		Title:           row.Title,
		Metadata:        parseJSONMap(row.Metadata),
		ParentThreadID:  parentID,
		CreatedByUserID: createdByUserID,
		WorkdirID:       workdirID,
		CreatedAt:       row.CreatedAt.Time,
		UpdatedAt:       row.UpdatedAt.Time,
		Visibility:      storedVisibility(row.Visibility, sessionMode),
	}
}

func toThreadFromUserListRow(row sqlc.ListSessionsByBotAndCreatedByUserRow) Thread {
	parentID := ""
	if row.ParentSessionID.Valid {
		parentID = row.ParentSessionID.String()
	}
	createdByUserID := ""
	if row.CreatedByUserID.Valid {
		createdByUserID = row.CreatedByUserID.String()
	}
	workdirID := ""
	if row.WorkdirID.Valid {
		workdirID = row.WorkdirID.String()
	}
	sessionMode := normalizeSessionMode(row.SessionMode, row.Type)
	return Thread{
		ID:              row.ID.String(),
		BotID:           row.BotID.String(),
		BotAgentID:      row.BotAgentID.String(),
		RouteID:         row.RouteID.String(),
		ChannelType:     dbpkg.TextToString(row.ChannelType),
		Type:            row.Type,
		SessionMode:     sessionMode,
		RuntimeType:     normalizeRuntimeType(row.RuntimeType, row.Type),
		RuntimeMetadata: parseJSONMap(row.RuntimeMetadata),
		Title:           row.Title,
		Metadata:        parseJSONMap(row.Metadata),
		ParentThreadID:  parentID,
		CreatedByUserID: createdByUserID,
		WorkdirID:       workdirID,
		CreatedAt:       row.CreatedAt.Time,
		UpdatedAt:       row.UpdatedAt.Time,
		Visibility:      storedVisibility(row.Visibility, sessionMode),
	}
}

func toThreadFromPagedRow(row sqlc.ListSessionsByBotPagedRow) Thread {
	return threadFromPagedColumns(pagedColumns{
		ID: row.ID, BotID: row.BotID, BotAgentID: row.BotAgentID, RouteID: row.RouteID, ChannelType: row.ChannelType,
		Type: row.Type, SessionMode: row.SessionMode, RuntimeType: row.RuntimeType, Visibility: row.Visibility, RuntimeMetadata: row.RuntimeMetadata,
		Title: row.Title, Metadata: row.Metadata,
		ParentThreadID: row.ParentSessionID, CreatedByUserID: row.CreatedByUserID, WorkdirID: row.WorkdirID,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	})
}

func toThreadFromUserPagedRow(row sqlc.ListSessionsByBotAndCreatedByUserPagedRow) Thread {
	return threadFromPagedColumns(pagedColumns{
		ID: row.ID, BotID: row.BotID, BotAgentID: row.BotAgentID, RouteID: row.RouteID, ChannelType: row.ChannelType,
		Type: row.Type, SessionMode: row.SessionMode, RuntimeType: row.RuntimeType, Visibility: row.Visibility, RuntimeMetadata: row.RuntimeMetadata,
		Title: row.Title, Metadata: row.Metadata,
		ParentThreadID: row.ParentSessionID, CreatedByUserID: row.CreatedByUserID, WorkdirID: row.WorkdirID,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	})
}

// pagedColumns is the shared shape of the bot/route-joined session row
// returned by both paged list queries. The two sqlc-generated row structs
// happen to be structurally identical; centralizing the projection here
// keeps the conversion logic in one place.
type pagedColumns struct {
	ID              pgtype.UUID
	BotID           pgtype.UUID
	BotAgentID      pgtype.UUID
	RouteID         pgtype.UUID
	ChannelType     pgtype.Text
	Type            string
	SessionMode     string
	RuntimeType     string
	Visibility      string
	RuntimeMetadata []byte
	Title           string
	Metadata        []byte
	ParentThreadID  pgtype.UUID
	CreatedByUserID pgtype.UUID
	WorkdirID       pgtype.UUID
	CreatedAt       pgtype.Timestamptz
	UpdatedAt       pgtype.Timestamptz
}

func threadFromPagedColumns(c pagedColumns) Thread {
	parentID := ""
	if c.ParentThreadID.Valid {
		parentID = c.ParentThreadID.String()
	}
	createdByUserID := ""
	if c.CreatedByUserID.Valid {
		createdByUserID = c.CreatedByUserID.String()
	}
	workdirID := ""
	if c.WorkdirID.Valid {
		workdirID = c.WorkdirID.String()
	}
	sessionMode := normalizeSessionMode(c.SessionMode, c.Type)
	return Thread{
		ID:              c.ID.String(),
		BotID:           c.BotID.String(),
		BotAgentID:      c.BotAgentID.String(),
		RouteID:         c.RouteID.String(),
		ChannelType:     dbpkg.TextToString(c.ChannelType),
		Type:            c.Type,
		SessionMode:     sessionMode,
		RuntimeType:     normalizeRuntimeType(c.RuntimeType, c.Type),
		RuntimeMetadata: parseJSONMap(c.RuntimeMetadata),
		Title:           c.Title,
		Metadata:        parseJSONMap(c.Metadata),
		ParentThreadID:  parentID,
		CreatedByUserID: createdByUserID,
		WorkdirID:       workdirID,
		CreatedAt:       c.CreatedAt.Time,
		UpdatedAt:       c.UpdatedAt.Time,
		Visibility:      storedVisibility(c.Visibility, sessionMode),
	}
}
