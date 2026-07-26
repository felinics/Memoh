package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	runtimefence "github.com/memohai/memoh/domains/agent/chat/session/fence"
	"github.com/memohai/memoh/domains/agent/chat/thread"
	agentsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

func (s *ThreadStore) InTransaction(ctx context.Context, fn func(thread.Store) error) error {
	if s.pool != nil {
		return inThreadTransaction(ctx, s.pool, func(agentQueries *agentsqlc.Queries) error {
			return fn(&ThreadStore{agentQueries: agentQueries})
		})
	}
	return fn(s)
}

func (s *ThreadStore) CreateThread(ctx context.Context, record thread.CreateRecord) (thread.Thread, error) {
	botID, err := db.ParseUUID(record.BotID)
	if err != nil {
		return thread.Thread{}, err
	}
	routeID, err := optionalUUID(record.RouteID)
	if err != nil {
		return thread.Thread{}, err
	}
	parentID, err := optionalUUID(record.ParentThreadID)
	if err != nil {
		return thread.Thread{}, err
	}
	createdBy, err := optionalUUID(record.CreatedByUserID)
	if err != nil {
		return thread.Thread{}, err
	}
	metadata, err := json.Marshal(nonNilJSON(record.Metadata))
	if err != nil {
		return thread.Thread{}, fmt.Errorf("marshal metadata: %w", err)
	}
	runtimeMetadata, err := json.Marshal(nonNilJSON(record.RuntimeMetadata))
	if err != nil {
		return thread.Thread{}, fmt.Errorf("marshal runtime metadata: %w", err)
	}
	row, err := s.agentQueries.CreateSession(ctx, agentsqlc.CreateSessionParams{
		BotID:            botID,
		RouteID:          routeID,
		ChannelType:      optionalText(record.ChannelType),
		ConversationType: optionalText(record.ConversationType),
		ConversationName: optionalText(record.ConversationName),
		ReplyTarget:      optionalText(record.ReplyTarget),
		Type:             record.Type,
		SessionMode:      record.SessionMode,
		RuntimeType:      record.RuntimeType,
		RuntimeMetadata:  runtimeMetadata,
		Title:            record.Title,
		Metadata:         metadata,
		ParentSessionID:  parentID,
		CreatedByUserID:  createdBy,
	})
	if err != nil {
		return thread.Thread{}, err
	}
	return threadFromSession(row), nil
}

func (s *ThreadStore) GetThread(ctx context.Context, id string) (thread.Thread, error) {
	sessionID, err := db.ParseUUID(id)
	if err != nil {
		return thread.Thread{}, err
	}
	row, err := s.agentQueries.GetSessionByID(ctx, sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return thread.Thread{}, thread.ErrNotFound
	}
	if err != nil {
		return thread.Thread{}, err
	}
	return threadFromSession(row), nil
}

func (s *ThreadStore) ListThreadsByBot(ctx context.Context, record thread.ListRecord) ([]thread.Thread, error) {
	botID, err := db.ParseUUID(record.BotID)
	if err != nil {
		return nil, err
	}
	if record.Limit == 0 {
		rows, err := s.agentQueries.ListSessionsByBot(ctx, botID)
		if err != nil {
			return nil, err
		}
		result := make([]thread.Thread, 0, len(rows))
		for _, row := range rows {
			result = append(result, threadFromListRow(row))
		}
		return result, nil
	}
	parentID, err := optionalUUID(record.ParentThreadID)
	if err != nil {
		return nil, err
	}
	cursorID, err := optionalUUID(record.Cursor.ID)
	if err != nil {
		return nil, err
	}
	cursorAt := pgtype.Timestamptz{}
	if record.UseCursor {
		cursorAt = pgtype.Timestamptz{Time: record.Cursor.UpdatedAt, Valid: true}
	}
	rows, err := s.agentQueries.ListSessionsByBotPaged(ctx, agentsqlc.ListSessionsByBotPagedParams{
		BotID:            botID,
		Types:            record.Types,
		UseParentSession: record.UseParent,
		ParentSessionID:  parentID,
		UseCursor:        record.UseCursor,
		CursorUpdatedAt:  cursorAt,
		CursorID:         cursorID,
		LimitCount:       record.Limit,
	})
	if err != nil {
		return nil, err
	}
	result := make([]thread.Thread, 0, len(rows))
	for _, row := range rows {
		result = append(result, threadFromPagedFields(
			row.ID, row.BotID, row.RouteID, row.ChannelType,
			row.ConversationType, row.ConversationName, row.ReplyTarget, row.Type, row.SessionMode,
			row.RuntimeType, row.RuntimeMetadata, row.Title, row.Metadata,
			row.ParentSessionID, row.CreatedByUserID, row.CreatedAt, row.UpdatedAt))
	}
	return result, nil
}

func (s *ThreadStore) ListThreadsByUser(ctx context.Context, record thread.ListRecord) ([]thread.Thread, error) {
	botID, err := db.ParseUUID(record.BotID)
	if err != nil {
		return nil, err
	}
	userID, err := db.ParseUUID(record.CreatedByUserID)
	if err != nil {
		return nil, err
	}
	if record.Limit == 0 {
		rows, err := s.agentQueries.ListSessionsByBotAndCreatedByUser(ctx, agentsqlc.ListSessionsByBotAndCreatedByUserParams{
			BotID:           botID,
			CreatedByUserID: userID,
		})
		if err != nil {
			return nil, err
		}
		result := make([]thread.Thread, 0, len(rows))
		for _, row := range rows {
			result = append(result, threadFromUserListRow(row))
		}
		return result, nil
	}
	parentID, err := optionalUUID(record.ParentThreadID)
	if err != nil {
		return nil, err
	}
	cursorID, err := optionalUUID(record.Cursor.ID)
	if err != nil {
		return nil, err
	}
	cursorAt := pgtype.Timestamptz{}
	if record.UseCursor {
		cursorAt = pgtype.Timestamptz{Time: record.Cursor.UpdatedAt, Valid: true}
	}
	rows, err := s.agentQueries.ListSessionsByBotAndCreatedByUserPaged(ctx, agentsqlc.ListSessionsByBotAndCreatedByUserPagedParams{
		BotID:            botID,
		CreatedByUserID:  userID,
		Types:            record.Types,
		UseParentSession: record.UseParent,
		ParentSessionID:  parentID,
		UseCursor:        record.UseCursor,
		CursorUpdatedAt:  cursorAt,
		CursorID:         cursorID,
		LimitCount:       record.Limit,
	})
	if err != nil {
		return nil, err
	}
	result := make([]thread.Thread, 0, len(rows))
	for _, row := range rows {
		result = append(result, threadFromPagedFields(
			row.ID, row.BotID, row.RouteID, row.ChannelType,
			row.ConversationType, row.ConversationName, row.ReplyTarget, row.Type, row.SessionMode,
			row.RuntimeType, row.RuntimeMetadata, row.Title, row.Metadata,
			row.ParentSessionID, row.CreatedByUserID, row.CreatedAt, row.UpdatedAt))
	}
	return result, nil
}

func (s *ThreadStore) ListThreadsByRoute(ctx context.Context, id string) ([]thread.Thread, error) {
	routeID, err := db.ParseUUID(id)
	if err != nil {
		return nil, err
	}
	rows, err := s.agentQueries.ListSessionsByRoute(ctx, routeID)
	if err != nil {
		return nil, err
	}
	result := make([]thread.Thread, 0, len(rows))
	for _, row := range rows {
		result = append(result, threadFromSession(row))
	}
	return result, nil
}

func (s *ThreadStore) ListSubagentThreads(ctx context.Context, id string) ([]thread.Thread, error) {
	parentID, err := db.ParseUUID(id)
	if err != nil {
		return nil, err
	}
	rows, err := s.agentQueries.ListSubagentSessionsByParent(ctx, parentID)
	if err != nil {
		return nil, err
	}
	result := make([]thread.Thread, 0, len(rows))
	for _, row := range rows {
		result = append(result, threadFromSession(row))
	}
	return result, nil
}

func (s *ThreadStore) ForkThread(ctx context.Context, record thread.ForkRecord) (thread.Thread, error) {
	botID, err := db.ParseUUID(record.BotID)
	if err != nil {
		return thread.Thread{}, err
	}
	sessionID, err := db.ParseUUID(record.ThreadID)
	if err != nil {
		return thread.Thread{}, err
	}
	messageID, err := db.ParseUUID(record.MessageID)
	if err != nil {
		return thread.Thread{}, err
	}
	createdBy, err := optionalUUID(record.CreatedByUserID)
	if err != nil {
		return thread.Thread{}, err
	}
	metadata, err := json.Marshal(nonNilJSON(record.Metadata))
	if err != nil {
		return thread.Thread{}, err
	}
	row, err := s.agentQueries.ForkSessionFromAssistantMessage(ctx, agentsqlc.ForkSessionFromAssistantMessageParams{
		SessionID:       sessionID,
		BotID:           botID,
		MessageID:       messageID,
		Title:           record.Title,
		Metadata:        metadata,
		CreatedByUserID: createdBy,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return thread.Thread{}, thread.ErrNotFound
	}
	if err != nil {
		return thread.Thread{}, err
	}
	return threadFromSession(agentsqlc.AgentBotSession(row)), nil
}

func (s *ThreadStore) UpdateThreadDescriptor(ctx context.Context, record thread.DescriptorRecord) (thread.Thread, error) {
	id, err := db.ParseUUID(record.ThreadID)
	if err != nil {
		return thread.Thread{}, err
	}
	metadata, err := json.Marshal(nonNilJSON(record.Metadata))
	if err != nil {
		return thread.Thread{}, err
	}
	runtimeMetadata, err := json.Marshal(nonNilJSON(record.RuntimeMetadata))
	if err != nil {
		return thread.Thread{}, err
	}
	row, err := s.agentQueries.UpdateSessionTypeAndMetadata(ctx, agentsqlc.UpdateSessionTypeAndMetadataParams{
		ID:              id,
		Type:            record.Type,
		SessionMode:     record.SessionMode,
		RuntimeType:     record.RuntimeType,
		RuntimeMetadata: runtimeMetadata,
		Metadata:        metadata,
	})
	if err != nil {
		return thread.Thread{}, err
	}
	return threadFromSession(row), nil
}

func (s *ThreadStore) UpdateThreadTitle(ctx context.Context, id, title string, fence *thread.RuntimeFence) (thread.Thread, error) {
	sessionID, err := db.ParseUUID(id)
	if err != nil {
		return thread.Thread{}, err
	}
	var row agentsqlc.AgentBotSession
	if fence == nil {
		row, err = s.agentQueries.UpdateSessionTitle(ctx, agentsqlc.UpdateSessionTitleParams{ID: sessionID, Title: title})
	} else {
		botID, parseErr := db.ParseUUID(fence.BotID)
		if parseErr != nil {
			return thread.Thread{}, parseErr
		}
		row, err = s.agentQueries.UpdateSessionTitleWithRuntimeFence(ctx, agentsqlc.UpdateSessionTitleWithRuntimeFenceParams{
			Title: title, ID: sessionID, BotID: botID, RuntimeFencingToken: fence.Token,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return thread.Thread{}, runtimefence.ErrStale
		}
	}
	if err != nil {
		return thread.Thread{}, err
	}
	return threadFromSession(row), nil
}

func (s *ThreadStore) UpdateThreadMetadata(ctx context.Context, id string, value map[string]any, fence *thread.RuntimeFence) (thread.Thread, error) {
	sessionID, err := db.ParseUUID(id)
	if err != nil {
		return thread.Thread{}, err
	}
	metadata, err := json.Marshal(nonNilJSON(value))
	if err != nil {
		return thread.Thread{}, err
	}
	var row agentsqlc.AgentBotSession
	if fence == nil {
		row, err = s.agentQueries.UpdateSessionMetadata(ctx, agentsqlc.UpdateSessionMetadataParams{ID: sessionID, Metadata: metadata})
	} else {
		botID, parseErr := db.ParseUUID(fence.BotID)
		if parseErr != nil {
			return thread.Thread{}, parseErr
		}
		row, err = s.agentQueries.UpdateSessionMetadataWithRuntimeFence(ctx, agentsqlc.UpdateSessionMetadataWithRuntimeFenceParams{
			Metadata: metadata, ID: sessionID, BotID: botID, RuntimeFencingToken: fence.Token,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return thread.Thread{}, runtimefence.ErrStale
		}
	}
	if err != nil {
		return thread.Thread{}, err
	}
	return threadFromSession(row), nil
}

func (s *ThreadStore) SoftDeleteThread(ctx context.Context, id string) error {
	sessionID, err := db.ParseUUID(id)
	if err != nil {
		return err
	}
	return s.agentQueries.SoftDeleteSession(ctx, sessionID)
}

func (s *ThreadStore) TouchThread(ctx context.Context, id string) error {
	sessionID, err := db.ParseUUID(id)
	if err != nil {
		return err
	}
	return s.agentQueries.TouchSession(ctx, sessionID)
}

func (s *ThreadStore) CountThreadMessages(ctx context.Context, id string) (int64, error) {
	sessionID, err := db.ParseUUID(id)
	if err != nil {
		return 0, err
	}
	return s.agentQueries.CountMessagesBySession(ctx, sessionID)
}

func (s *ThreadStore) CreateSubagentConfig(ctx context.Context, record thread.SubagentConfigRecord) (thread.SubagentConfig, error) {
	sessionID, err := db.ParseUUID(record.ThreadID)
	if err != nil {
		return thread.SubagentConfig{}, err
	}
	modelID, err := db.ParseUUID(record.ModelUUID)
	if err != nil {
		return thread.SubagentConfig{}, err
	}
	row, err := s.agentQueries.CreateSubagentConfig(ctx, agentsqlc.CreateSubagentConfigParams{
		SessionID: sessionID, ModelUuid: modelID, ModelID: record.ModelID,
		ProviderName: record.ProviderName, Forked: record.Forked,
	})
	if err != nil {
		return thread.SubagentConfig{}, err
	}
	return subagentConfig(row), nil
}

func (s *ThreadStore) CreateSubagentForkContext(ctx context.Context, record thread.ForkContextRecord) (int64, error) {
	parentID, err := db.ParseUUID(record.ParentThreadID)
	if err != nil {
		return 0, err
	}
	sessionID, err := db.ParseUUID(record.ThreadID)
	if err != nil {
		return 0, err
	}
	messages, err := json.Marshal(record.Messages)
	if err != nil {
		return 0, err
	}
	row, err := s.agentQueries.CreateSubagentForkContext(ctx, agentsqlc.CreateSubagentForkContextParams{
		ParentSessionID: parentID, SessionID: sessionID, ContextMessages: messages,
	})
	if err != nil {
		return 0, err
	}
	return row.InsertedCount, nil
}

func (s *ThreadStore) GetSubagentConfig(ctx context.Context, id string) (thread.SubagentConfig, error) {
	sessionID, err := db.ParseUUID(id)
	if err != nil {
		return thread.SubagentConfig{}, err
	}
	row, err := s.agentQueries.GetSubagentConfig(ctx, sessionID)
	if err != nil {
		return thread.SubagentConfig{}, err
	}
	return subagentConfig(row), nil
}

func (s *ThreadStore) ListSubagentForkContext(ctx context.Context, id string) ([]thread.SubagentForkContextMessage, error) {
	sessionID, err := db.ParseUUID(id)
	if err != nil {
		return nil, err
	}
	rows, err := s.agentQueries.ListSubagentForkContext(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	result := make([]thread.SubagentForkContextMessage, 0, len(rows))
	for _, row := range rows {
		result = append(result, thread.SubagentForkContextMessage{
			Role: row.Role, Message: append(json.RawMessage(nil), row.Content...),
		})
	}
	return result, nil
}

func threadFromSession(row agentsqlc.AgentBotSession) thread.Thread {
	return threadFromPagedFields(
		row.ID, row.BotID, row.RouteID, row.ChannelType,
		row.ConversationType, row.ConversationName, row.ReplyTarget, row.Type, row.SessionMode,
		row.RuntimeType, row.RuntimeMetadata, row.Title, row.Metadata,
		row.ParentSessionID, row.CreatedByUserID, row.CreatedAt, row.UpdatedAt)
}

func threadFromListRow(row agentsqlc.ListSessionsByBotRow) thread.Thread {
	return threadFromPagedFields(
		row.ID, row.BotID, row.RouteID, row.ChannelType,
		row.ConversationType, row.ConversationName, row.ReplyTarget, row.Type, row.SessionMode,
		row.RuntimeType, row.RuntimeMetadata, row.Title, row.Metadata,
		row.ParentSessionID, row.CreatedByUserID, row.CreatedAt, row.UpdatedAt)
}

func threadFromUserListRow(row agentsqlc.ListSessionsByBotAndCreatedByUserRow) thread.Thread {
	return threadFromPagedFields(
		row.ID, row.BotID, row.RouteID, row.ChannelType,
		row.ConversationType, row.ConversationName, row.ReplyTarget, row.Type, row.SessionMode,
		row.RuntimeType, row.RuntimeMetadata, row.Title, row.Metadata,
		row.ParentSessionID, row.CreatedByUserID, row.CreatedAt, row.UpdatedAt)
}

func threadFromPagedFields(
	id, botID, routeID pgtype.UUID,
	channelType, conversationType, conversationName, replyTarget pgtype.Text,
	legacyType, sessionMode, runtimeType string,
	runtimeMetadata []byte,
	title string,
	metadata []byte,
	parentID, createdBy pgtype.UUID,
	createdAt, updatedAt pgtype.Timestamptz,
) thread.Thread {
	if !thread.IsKnownSessionMode(sessionMode) {
		sessionMode, _ = thread.DescriptorFromLegacyType(legacyType)
	}
	if !thread.IsKnownRuntimeType(runtimeType) {
		_, runtimeType = thread.DescriptorFromLegacyType(legacyType)
	}
	visibility := thread.VisibilityInternal
	if sessionMode == thread.TypeChat || sessionMode == thread.TypeDiscuss {
		visibility = thread.VisibilityUser
	}
	return thread.Thread{
		ID: db.UUIDString(id), BotID: db.UUIDString(botID), RouteID: db.UUIDString(routeID),
		ChannelType: db.TextToString(channelType), ConversationType: db.TextToString(conversationType),
		ConversationName: db.TextToString(conversationName), ReplyTarget: db.TextToString(replyTarget),
		Type: legacyType, SessionMode: sessionMode,
		RuntimeType: runtimeType, RuntimeMetadata: parseJSONMap(runtimeMetadata),
		Title: title, Metadata: parseJSONMap(metadata), ParentThreadID: db.UUIDString(parentID),
		CreatedByUserID: db.UUIDString(createdBy), CreatedAt: createdAt.Time, UpdatedAt: updatedAt.Time,
		Visibility: visibility,
	}
}

func subagentConfig(row agentsqlc.AgentSubagentConfig) thread.SubagentConfig {
	return thread.SubagentConfig{
		ThreadID: db.UUIDString(row.SessionID), ModelUUID: db.UUIDString(row.ModelUuid),
		ModelID: row.ModelID, ProviderName: row.ProviderName, Forked: row.Forked,
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
}

func nonNilJSON(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}

func parseJSONMap(data []byte) map[string]any {
	if len(data) == 0 {
		return nil
	}
	var value map[string]any
	_ = json.Unmarshal(data, &value)
	return value
}
