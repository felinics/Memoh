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

	"github.com/google/uuid"

	"github.com/memohai/memoh/domains/agent/chat/event"
	"github.com/memohai/memoh/domains/agent/chat/runtimefence"
	"github.com/memohai/memoh/domains/agent/extension/hooks"
)

// Visibility describes whether a thread belongs in user-facing history.
type Visibility string

const (
	VisibilityUser     Visibility = "user"
	VisibilityInternal Visibility = "internal"
)

// Thread represents a chat thread within a bot.
type Thread struct {
	ID                    string         `json:"id"`
	BotID                 string         `json:"bot_id"`
	RouteID               string         `json:"route_id,omitempty"`
	ChannelType           string         `json:"channel_type,omitempty"`
	ConversationType      string         `json:"conversation_type,omitempty"`
	ConversationName      string         `json:"conversation_name,omitempty"`
	ReplyTarget           string         `json:"reply_target,omitempty"`
	Type                  string         `json:"type"`
	SessionMode           string         `json:"session_mode"`
	RuntimeType           string         `json:"runtime_type"`
	RuntimeMetadata       map[string]any `json:"runtime_metadata,omitempty"`
	Title                 string         `json:"title"`
	Metadata              map[string]any `json:"metadata,omitempty"`
	ParentThreadID        string         `json:"parent_session_id,omitempty"`
	CreatedByUserID       string         `json:"created_by_user_id,omitempty"`
	CreatedAt             time.Time      `json:"created_at"`
	UpdatedAt             time.Time      `json:"updated_at"`
	RouteMetadata         map[string]any `json:"route_metadata,omitempty"`
	RouteConversationType string         `json:"route_conversation_type,omitempty"`
	Visibility            Visibility     `json:"-"`
} // @name session.Session

const (
	TypeChat              = "chat"
	TypeHeartbeat         = "heartbeat"
	TypeSchedule          = "schedule"
	TypeSubagent          = "subagent"
	TypeDiscuss           = "discuss"
	TypeACPAgent          = "acp_agent"
	RuntimeModel          = "model"
	RuntimeACPAgent       = "acp_agent"
	DefaultACPProjectMode = "project"
	DefaultACPProjectPath = "/data"
)

var userFacingSessionTypes = []string{TypeChat, TypeDiscuss, TypeACPAgent}

func UserFacingSessionTypes() []string {
	return slices.Clone(userFacingSessionTypes)
}

var (
	ErrNotFound               = errors.New("thread not found")
	ErrACPAgentIDRequired     = errors.New("acp_agent_id is required for acp_agent sessions")
	ErrACPProjectPathMissing  = errors.New("project_path is required for acp_agent sessions")
	ErrACPUnknownAgent        = errors.New("unknown ACP agent")
	ErrACPAgentNotEnabled     = errors.New("ACP agent is not enabled for this bot")
	ErrACPAgentNotConfigured  = errors.New("ACP agent is not configured for this bot")
	ErrACPRuntimeOwnerMissing = errors.New("runtime_owner_account_id is required for acp_agent sessions")
	ErrACPProjectModeInvalid  = errors.New("unknown ACP project mode")
	ErrForkSourceNotFound     = errors.New("fork source session not found")
	ErrForkSourceNotReply     = errors.New("fork source must be a visible assistant reply")
	ErrForkSourceNotChat      = errors.New("fork source must be a chat session")
)

func IsKnownType(typ string) bool {
	switch strings.TrimSpace(typ) {
	case TypeChat, TypeHeartbeat, TypeSchedule, TypeSubagent, TypeDiscuss, TypeACPAgent:
		return true
	default:
		return false
	}
}

func IsUserFacingType(typ string) bool {
	return slices.Contains(userFacingSessionTypes, strings.TrimSpace(typ))
}

type CreateInput struct {
	BotID            string
	RouteID          string
	ChannelType      string
	ConversationType string
	ConversationName string
	ReplyTarget      string
	Type             string
	SessionMode      string
	RuntimeType      string
	Title            string
	Metadata         map[string]any
	RuntimeMetadata  map[string]any
	ParentThreadID   string
	CreatedByUserID  string
}

type SubagentConfig struct {
	ThreadID     string    `json:"session_id"`
	ModelUUID    string    `json:"model_uuid,omitempty"`
	ModelID      string    `json:"model_id"`
	ProviderName string    `json:"provider"`
	Forked       bool      `json:"fork"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

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

type ForkFromAssistantInput struct {
	BotID           string
	ThreadID        string
	MessageID       string
	Title           string
	CreatedByUserID string
}

type ACPSetupValidation struct {
	Known                 bool
	Enabled               bool
	MissingManagedFieldID string
}

type ACPSetupValidator interface {
	ValidateACPSetup(agentID string, botMetadata map[string]any) ACPSetupValidation
}

type Service struct {
	store             Store
	policyReader      ACPPolicyReader
	hookService       *hooks.Service
	publisher         event.Publisher
	acpSetupValidator ACPSetupValidator
	logger            *slog.Logger
}

// NewService creates a thread service from Thread-owned persistence ports.
func NewService(log *slog.Logger, store Store, policyReader ACPPolicyReader, publisher event.Publisher) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		store:        store,
		policyReader: policyReader,
		publisher:    publisher,
		logger:       log.With(slog.String("service", "chat/thread")),
	}
}

func (s *Service) SetHookService(h *hooks.Service) {
	s.hookService = h
}

func (s *Service) SetACPSetupValidator(validator ACPSetupValidator) {
	if s != nil {
		s.acpSetupValidator = validator
	}
}

func canonicalChannelType(channelType string) string {
	trimmed := strings.TrimSpace(channelType)
	switch strings.ToLower(trimmed) {
	case "web", "cli":
		return "local"
	default:
		return trimmed
	}
}

func (s *Service) Create(ctx context.Context, input CreateInput) (Thread, error) {
	if err := validateRequiredUUID("bot id", input.BotID); err != nil {
		return Thread{}, err
	}
	for name, value := range map[string]string{
		"route id":           input.RouteID,
		"created by user id": input.CreatedByUserID,
		"parent session id":  input.ParentThreadID,
	} {
		if err := validateOptionalUUID(name, value); err != nil {
			return Thread{}, err
		}
	}
	sessionType := strings.TrimSpace(input.Type)
	if sessionType == "" {
		sessionType = TypeChat
	}
	if !IsKnownType(sessionType) {
		return Thread{}, fmt.Errorf("unknown session type %q", sessionType)
	}
	meta := nonNilMap(input.Metadata)
	desc, err := normalizeDescriptor(sessionType, input.SessionMode, input.RuntimeType, meta, input.RuntimeMetadata)
	if err != nil {
		return Thread{}, err
	}
	runtimeOwnerUserID := strings.TrimSpace(input.CreatedByUserID)
	meta = desc.Metadata
	runtimeMeta := desc.RuntimeMetadata
	if desc.RuntimeType == RuntimeACPAgent {
		meta = ApplyACPMetadataDefaults(meta)
		runtimeMeta = ApplyACPMetadataDefaults(runtimeMeta)
		meta = setACPRuntimeOwner(meta, runtimeOwnerUserID)
		runtimeMeta = setACPRuntimeOwner(runtimeMeta, runtimeOwnerUserID)
		meta = mergeACPMetadata(meta, runtimeMeta)
		runtimeMeta = mergeACPMetadata(runtimeMeta, meta)
		if err := validateACPMetadata(meta); err != nil {
			return Thread{}, err
		}
		if err := s.validateACPCreatePolicy(ctx, input.BotID, meta); err != nil {
			return Thread{}, err
		}
	}
	thread, err := s.store.CreateThread(ctx, CreateRecord{
		BotID:            strings.TrimSpace(input.BotID),
		RouteID:          strings.TrimSpace(input.RouteID),
		ChannelType:      canonicalChannelType(input.ChannelType),
		ConversationType: strings.TrimSpace(input.ConversationType),
		ConversationName: strings.TrimSpace(input.ConversationName),
		ReplyTarget:      strings.TrimSpace(input.ReplyTarget),
		Type:             desc.LegacyType,
		SessionMode:      desc.SessionMode,
		RuntimeType:      desc.RuntimeType,
		RuntimeMetadata:  runtimeMeta,
		Title:            input.Title,
		Metadata:         meta,
		ParentThreadID:   strings.TrimSpace(input.ParentThreadID),
		CreatedByUserID:  strings.TrimSpace(input.CreatedByUserID),
	})
	if err != nil {
		return Thread{}, err
	}
	s.afterCreate(ctx, thread)
	return thread, nil
}

func (s *Service) CreateSubagent(ctx context.Context, input CreateSubagentInput) (Thread, SubagentConfig, error) {
	input.Thread.Type = TypeSubagent
	if err := validateRequiredUUID("subagent model uuid", input.ModelUUID); err != nil {
		return Thread{}, SubagentConfig{}, err
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
		if err := validateOptionalUUID("fork context source message id", message.SourceMessageID); err != nil {
			return Thread{}, SubagentConfig{}, fmt.Errorf("%w at index %d", err, i)
		}
	}

	var created Thread
	var config SubagentConfig
	err := s.store.InTransaction(ctx, func(store Store) error {
		txService := *s
		txService.store = store
		txService.publisher = nil
		txService.hookService = nil
		var err error
		created, err = txService.Create(ctx, input.Thread)
		if err != nil {
			return err
		}
		config, err = store.CreateSubagentConfig(ctx, SubagentConfigRecord{
			ThreadID:     created.ID,
			ModelUUID:    strings.TrimSpace(input.ModelUUID),
			ModelID:      strings.TrimSpace(input.ModelID),
			ProviderName: strings.TrimSpace(input.ProviderName),
			Forked:       input.Forked,
		})
		if err != nil {
			return err
		}
		if !input.Forked {
			return nil
		}
		inserted, err := store.CreateSubagentForkContext(ctx, ForkContextRecord{
			ParentThreadID: input.Thread.ParentThreadID,
			ThreadID:       created.ID,
			Messages:       input.ForkContext,
		})
		if err != nil {
			return err
		}
		if inserted != int64(len(input.ForkContext)) {
			return fmt.Errorf("persist fork context: inserted %d of %d messages", inserted, len(input.ForkContext))
		}
		return nil
	})
	if err != nil {
		return Thread{}, SubagentConfig{}, err
	}
	s.afterCreate(ctx, created)
	return created, config, nil
}

func (s *Service) GetSubagentConfig(ctx context.Context, threadID string) (SubagentConfig, error) {
	if err := validateRequiredUUID("subagent session id", threadID); err != nil {
		return SubagentConfig{}, err
	}
	return s.store.GetSubagentConfig(ctx, threadID)
}

func (s *Service) ListSubagentForkContext(ctx context.Context, threadID string) ([]SubagentForkContextMessage, error) {
	if err := validateRequiredUUID("subagent session id", threadID); err != nil {
		return nil, err
	}
	return s.store.ListSubagentForkContext(ctx, threadID)
}

func (s *Service) ForkFromAssistantMessage(ctx context.Context, input ForkFromAssistantInput) (Thread, error) {
	for name, value := range map[string]string{
		"bot id":             input.BotID,
		"session id":         input.ThreadID,
		"message id":         input.MessageID,
		"created by user id": input.CreatedByUserID,
	} {
		if name == "created by user id" {
			if err := validateOptionalUUID(name, value); err != nil {
				return Thread{}, err
			}
			continue
		}
		if err := validateRequiredUUID(name, value); err != nil {
			return Thread{}, err
		}
	}
	source, err := s.store.GetThread(ctx, input.ThreadID)
	if errors.Is(err, ErrNotFound) {
		return Thread{}, ErrForkSourceNotFound
	}
	if err != nil {
		return Thread{}, err
	}
	if source.BotID != strings.TrimSpace(input.BotID) {
		return Thread{}, ErrForkSourceNotFound
	}
	if source.Type != TypeChat {
		return Thread{}, ErrForkSourceNotChat
	}
	sourceTitle := strings.TrimSpace(source.Title)
	if sourceTitle == "" {
		sourceTitle = "Untitled"
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = sourceTitle + " fork"
	}
	meta := nonNilMap(source.Metadata)
	delete(meta, "workspace_target_id")
	delete(meta, "workspace_target")
	meta["forked_from"] = map[string]any{
		"session_id": source.ID,
		"title":      sourceTitle,
		"message_id": strings.TrimSpace(input.MessageID),
	}
	thread, err := s.store.ForkThread(ctx, ForkRecord{
		BotID:           input.BotID,
		ThreadID:        input.ThreadID,
		MessageID:       input.MessageID,
		Title:           title,
		Metadata:        meta,
		CreatedByUserID: input.CreatedByUserID,
	})
	if errors.Is(err, ErrNotFound) {
		return Thread{}, ErrForkSourceNotReply
	}
	if err != nil {
		return Thread{}, err
	}
	s.afterCreate(ctx, thread)
	return thread, nil
}

func (s *Service) afterCreate(ctx context.Context, thread Thread) {
	s.publishThreadCreated(thread)
	s.runThreadStartHook(context.WithoutCancel(ctx), thread)
}

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
	s.publisher.Publish(event.Event{Type: event.EventTypeSessionCreated, BotID: strings.TrimSpace(thread.BotID), Data: payload})
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
		Workspace: hooks.WorkspaceInfo{CWD: hooks.DefaultWorkDir},
		Turn: map[string]any{
			"session_type": thread.Type,
			"route_id":     thread.RouteID,
			"channel_type": thread.ChannelType,
		},
	}
	if _, err := s.hookService.Run(ctx, req, nil); err != nil {
		s.logger.WarnContext(ctx, "session start hook failed",
			slog.String("bot_id", thread.BotID),
			slog.String("session_id", thread.ID),
			slog.Any("error", err))
	}
}

func (s *Service) UpdateTypeAndMetadata(ctx context.Context, threadID, typ string, metadata map[string]any) (Thread, error) {
	return s.updateDescriptorAndMetadata(ctx, threadID, typ, "", "", metadata, nil, "")
}

func (s *Service) UpdateTypeAndMetadataWithOwner(ctx context.Context, threadID, typ string, metadata map[string]any, ownerID string) (Thread, error) {
	return s.updateDescriptorAndMetadata(ctx, threadID, typ, "", "", metadata, nil, strings.TrimSpace(ownerID))
}

func (s *Service) UpdateDescriptorAndMetadataWithOwner(ctx context.Context, threadID, typ, sessionMode, runtimeType string, metadata, runtimeMetadata map[string]any, ownerID string) (Thread, error) {
	return s.updateDescriptorAndMetadata(ctx, threadID, typ, sessionMode, runtimeType, metadata, runtimeMetadata, strings.TrimSpace(ownerID))
}

func (s *Service) updateDescriptorAndMetadata(ctx context.Context, threadID, typ, sessionMode, runtimeType string, metadata, runtimeMetadata map[string]any, ownerID string) (Thread, error) {
	if err := validateRequiredUUID("session id", threadID); err != nil {
		return Thread{}, err
	}
	sessionType := strings.TrimSpace(typ)
	if sessionType == "" {
		sessionType = TypeChat
	}
	if !IsKnownType(sessionType) {
		return Thread{}, fmt.Errorf("unknown session type %q", sessionType)
	}
	existing, err := s.store.GetThread(ctx, threadID)
	if err != nil {
		return Thread{}, err
	}
	metadata = nonNilMap(metadata)
	if normalizeRuntimeType(existing.RuntimeType, existing.Type) == RuntimeACPAgent {
		existingOwner := metadataString(existing.RuntimeMetadata, "runtime_owner_account_id")
		if existingOwner == "" {
			existingOwner = metadataString(existing.Metadata, "runtime_owner_account_id")
		}
		if existingOwner != "" {
			ownerID = existingOwner
		}
	}
	if runtimeMetadata == nil {
		runtimeMetadata = existing.RuntimeMetadata
	}
	desc, err := normalizeDescriptor(sessionType, sessionMode, runtimeType, metadata, runtimeMetadata)
	if err != nil {
		return Thread{}, err
	}
	runtimeMeta := desc.RuntimeMetadata
	if desc.RuntimeType != RuntimeACPAgent {
		runtimeMeta = map[string]any{}
	} else {
		metadata = ApplyACPMetadataDefaults(desc.Metadata)
		runtimeMeta = ApplyACPMetadataDefaults(runtimeMeta)
		metadata = setACPRuntimeOwner(metadata, ownerID)
		runtimeMeta = setACPRuntimeOwner(runtimeMeta, ownerID)
		metadata = mergeACPMetadata(metadata, runtimeMeta)
		runtimeMeta = mergeACPMetadata(runtimeMeta, metadata)
		if err := validateACPMetadata(metadata); err != nil {
			return Thread{}, err
		}
		if err := s.validateACPCreatePolicy(ctx, existing.BotID, metadata); err != nil {
			return Thread{}, err
		}
	}
	return s.store.UpdateThreadDescriptor(ctx, DescriptorRecord{
		ThreadID:        threadID,
		Type:            desc.LegacyType,
		SessionMode:     desc.SessionMode,
		RuntimeType:     desc.RuntimeType,
		RuntimeMetadata: runtimeMeta,
		Metadata:        metadata,
	})
}

func (s *Service) Get(ctx context.Context, threadID string) (Thread, error) {
	if err := validateRequiredUUID("session id", threadID); err != nil {
		return Thread{}, err
	}
	return s.store.GetThread(ctx, threadID)
}

func (s *Service) ListByBot(ctx context.Context, botID string) ([]Thread, error) {
	if err := validateRequiredUUID("bot id", botID); err != nil {
		return nil, err
	}
	return s.store.ListThreadsByBot(ctx, ListRecord{BotID: botID})
}

type Cursor struct {
	UpdatedAt time.Time
	ID        string
}

type ListFilter struct {
	ParentThreadID string
}

func (c Cursor) IsZero() bool {
	return c.ID == "" && c.UpdatedAt.IsZero()
}

func (s *Service) ListByBotPaged(ctx context.Context, botID string, types []string, cursor Cursor, limit int64) ([]Thread, error) {
	return s.ListByBotPagedWithFilter(ctx, botID, types, cursor, limit, ListFilter{})
}

func (s *Service) ListByBotPagedWithFilter(ctx context.Context, botID string, types []string, cursor Cursor, limit int64, filter ListFilter) ([]Thread, error) {
	record, err := listRecord(botID, "", types, cursor, limit, filter)
	if err != nil {
		return nil, err
	}
	return s.store.ListThreadsByBot(ctx, record)
}

func (s *Service) ListByBotAndCreatedByUserPaged(ctx context.Context, botID, userID string, types []string, cursor Cursor, limit int64) ([]Thread, error) {
	return s.ListByBotAndCreatedByUserPagedWithFilter(ctx, botID, userID, types, cursor, limit, ListFilter{})
}

func (s *Service) ListByBotAndCreatedByUserPagedWithFilter(ctx context.Context, botID, userID string, types []string, cursor Cursor, limit int64, filter ListFilter) ([]Thread, error) {
	record, err := listRecord(botID, userID, types, cursor, limit, filter)
	if err != nil {
		return nil, err
	}
	return s.store.ListThreadsByUser(ctx, record)
}

func listRecord(botID, userID string, types []string, cursor Cursor, limit int64, filter ListFilter) (ListRecord, error) {
	if err := validateRequiredUUID("bot id", botID); err != nil {
		return ListRecord{}, err
	}
	if userID != "" {
		if err := validateRequiredUUID("user id", userID); err != nil {
			return ListRecord{}, err
		}
	}
	if limit < 1 || limit > math.MaxInt32 {
		return ListRecord{}, fmt.Errorf("session: paged limit %d is out of range", limit)
	}
	useCursor := !cursor.IsZero()
	if useCursor {
		if cursor.ID == "" || cursor.UpdatedAt.IsZero() {
			return ListRecord{}, errors.New("session: cursor must carry both updated_at and id")
		}
		if err := validateRequiredUUID("cursor id", cursor.ID); err != nil {
			return ListRecord{}, err
		}
	}
	parentID := strings.TrimSpace(filter.ParentThreadID)
	if err := validateOptionalUUID("parent session id", parentID); err != nil {
		return ListRecord{}, err
	}
	return ListRecord{
		BotID:           botID,
		CreatedByUserID: userID,
		Types:           types,
		ParentThreadID:  parentID,
		Cursor:          cursor,
		Limit:           int32(limit),
		UseCursor:       useCursor,
		UseParent:       parentID != "",
	}, nil
}

func (s *Service) ListByBotAndCreatedByUser(ctx context.Context, botID, userID string) ([]Thread, error) {
	if err := validateRequiredUUID("bot id", botID); err != nil {
		return nil, err
	}
	if err := validateRequiredUUID("user id", userID); err != nil {
		return nil, err
	}
	return s.store.ListThreadsByUser(ctx, ListRecord{BotID: botID, CreatedByUserID: userID})
}

func (s *Service) ListByRoute(ctx context.Context, routeID string) ([]Thread, error) {
	if err := validateRequiredUUID("route id", routeID); err != nil {
		return nil, err
	}
	return s.store.ListThreadsByRoute(ctx, routeID)
}

func (s *Service) ListSubagentsByParent(ctx context.Context, parentID string) ([]Thread, error) {
	if err := validateRequiredUUID("parent session id", parentID); err != nil {
		return nil, err
	}
	return s.store.ListSubagentThreads(ctx, parentID)
}

func (s *Service) UpdateTitle(ctx context.Context, threadID, title string) (Thread, error) {
	if err := validateRequiredUUID("session id", threadID); err != nil {
		return Thread{}, err
	}
	fence, err := threadFence(ctx, threadID)
	if err != nil {
		return Thread{}, err
	}
	return s.store.UpdateThreadTitle(ctx, threadID, title, fence)
}

func (s *Service) UpdateMetadata(ctx context.Context, threadID string, metadata map[string]any) (Thread, error) {
	if err := validateRequiredUUID("session id", threadID); err != nil {
		return Thread{}, err
	}
	fence, err := threadFence(ctx, threadID)
	if err != nil {
		return Thread{}, err
	}
	return s.store.UpdateThreadMetadata(ctx, threadID, nonNilMap(metadata), fence)
}

func threadFence(ctx context.Context, threadID string) (*RuntimeFence, error) {
	fence, ok := runtimefence.FromContext(ctx)
	if !ok {
		return nil, nil
	}
	if strings.TrimSpace(threadID) != strings.TrimSpace(fence.SessionID) {
		return nil, runtimefence.ErrStale
	}
	return &RuntimeFence{BotID: fence.BotID, ThreadID: fence.SessionID, Token: fence.Token}, nil
}

func (s *Service) SoftDelete(ctx context.Context, threadID string) error {
	if err := validateRequiredUUID("session id", threadID); err != nil {
		return err
	}
	return s.store.SoftDeleteThread(ctx, threadID)
}

func (s *Service) MessageCount(ctx context.Context, threadID string) (int64, error) {
	if err := validateRequiredUUID("session id", threadID); err != nil {
		return 0, err
	}
	return s.store.CountThreadMessages(ctx, threadID)
}

func (s *Service) Touch(ctx context.Context, threadID string) error {
	if err := validateRequiredUUID("session id", threadID); err != nil {
		return err
	}
	return s.store.TouchThread(ctx, threadID)
}

func validateACPMetadata(meta map[string]any) error {
	if metadataString(meta, "acp_agent_id") == "" {
		return ErrACPAgentIDRequired
	}
	if metadataString(meta, "project_path") == "" {
		return ErrACPProjectPathMissing
	}
	if metadataString(meta, "runtime_owner_account_id") == "" {
		return ErrACPRuntimeOwnerMissing
	}
	switch metadataString(meta, "acp_project_mode") {
	case "", DefaultACPProjectMode, "none":
		return nil
	default:
		return fmt.Errorf("%w %q", ErrACPProjectModeInvalid, metadataString(meta, "acp_project_mode"))
	}
}

func ApplyACPMetadataDefaults(meta map[string]any) map[string]any {
	out := nonNilMap(meta)
	if metadataString(out, "project_path") == "" {
		out["project_path"] = DefaultACPProjectPath
	}
	if metadataString(out, "acp_project_mode") == "" {
		out["acp_project_mode"] = DefaultACPProjectMode
	}
	return out
}

type descriptor struct {
	LegacyType      string
	SessionMode     string
	RuntimeType     string
	Metadata        map[string]any
	RuntimeMetadata map[string]any
}

func ResolveDescriptor(legacyType, sessionMode, runtimeType string) (string, string, string, error) {
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
		mode, runtime := descriptorFromLegacyType(legacyType)
		if sessionMode == "" {
			sessionMode = mode
		}
		if runtimeType == "" {
			runtimeType = runtime
		}
	}
	if !IsKnownSessionMode(sessionMode) {
		return descriptor{}, fmt.Errorf("unknown session mode %q", sessionMode)
	}
	if !IsKnownRuntimeType(runtimeType) {
		return descriptor{}, fmt.Errorf("unknown runtime type %q", runtimeType)
	}
	if runtimeType == RuntimeACPAgent && sessionMode != TypeChat && sessionMode != TypeDiscuss {
		return descriptor{}, fmt.Errorf("runtime type %q is only supported for %s or %s session modes", RuntimeACPAgent, TypeChat, TypeDiscuss)
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
	case TypeHeartbeat:
		return TypeHeartbeat, RuntimeModel
	case TypeSchedule:
		return TypeSchedule, RuntimeModel
	case TypeSubagent:
		return TypeSubagent, RuntimeModel
	default:
		return TypeChat, RuntimeModel
	}
}

func DescriptorFromLegacyType(typ string) (string, string) {
	return descriptorFromLegacyType(typ)
}

func legacyTypeForDescriptor(sessionMode, runtimeType string) string {
	if runtimeType == RuntimeACPAgent && sessionMode == TypeChat {
		return TypeACPAgent
	}
	return sessionMode
}

func LegacyTypeForDescriptor(sessionMode, runtimeType string) string {
	return legacyTypeForDescriptor(sessionMode, runtimeType)
}

func IsKnownSessionMode(mode string) bool {
	switch strings.TrimSpace(mode) {
	case TypeChat, TypeDiscuss, TypeHeartbeat, TypeSchedule, TypeSubagent:
		return true
	default:
		return false
	}
}

func IsKnownRuntimeType(runtimeType string) bool {
	switch strings.TrimSpace(runtimeType) {
	case RuntimeModel, RuntimeACPAgent:
		return true
	default:
		return false
	}
}

func IsACPRuntime(thread Thread) bool {
	return normalizeRuntimeType(thread.RuntimeType, thread.Type) == RuntimeACPAgent
}

func SupportsSkillActivation(sessionMode, legacyType, runtimeType string) bool {
	return normalizeSessionMode(sessionMode, legacyType) == TypeChat &&
		normalizeRuntimeType(runtimeType, legacyType) != RuntimeACPAgent
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

func setACPRuntimeOwner(metadata map[string]any, ownerID string) map[string]any {
	out := nonNilMap(metadata)
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		delete(out, "runtime_owner_account_id")
	} else {
		out["runtime_owner_account_id"] = ownerID
	}
	return out
}

func nonNilMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func (s *Service) validateACPCreatePolicy(ctx context.Context, botID string, meta map[string]any) error {
	agentID := metadataString(meta, "acp_agent_id")
	if s.acpSetupValidator == nil {
		return fmt.Errorf("%w: ACP setup validator unavailable", ErrACPAgentNotConfigured)
	}
	if validation := s.acpSetupValidator.ValidateACPSetup(agentID, nil); !validation.Known {
		return fmt.Errorf("%w: %s", ErrACPUnknownAgent, agentID)
	}
	if s.policyReader == nil {
		return fmt.Errorf("%w: ACP policy reader unavailable", ErrACPAgentNotConfigured)
	}
	botMetadata, err := s.policyReader.GetBotMetadata(ctx, botID)
	if err != nil {
		return err
	}
	validation := s.acpSetupValidator.ValidateACPSetup(agentID, botMetadata)
	if !validation.Enabled {
		return fmt.Errorf("%w: %s", ErrACPAgentNotEnabled, agentID)
	}
	if validation.MissingManagedFieldID != "" {
		return fmt.Errorf("%w: %s missing %s", ErrACPAgentNotConfigured, agentID, validation.MissingManagedFieldID)
	}
	return nil
}

func metadataString(meta map[string]any, key string) string {
	value, _ := meta[key].(string)
	return strings.TrimSpace(value)
}

func validateRequiredUUID(name, value string) error {
	if _, err := uuid.Parse(strings.TrimSpace(value)); err != nil {
		return fmt.Errorf("invalid %s: %w", name, err)
	}
	return nil
}

func validateOptionalUUID(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return validateRequiredUUID(name, value)
}
