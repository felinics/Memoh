package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/domains/agent/chat/message"
	runtimefence "github.com/memohai/memoh/domains/agent/chat/session/fence"
	dbsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

func (s *MessageStore) InTransaction(ctx context.Context, fn func(message.Persistence) error) error {
	if s.pool == nil {
		return fn(s)
	}
	return inMessageTransaction(ctx, s.pool, s.bindLocker, func(locker BotSessionWriteLocker, db dbsqlc.DBTX) error {
		return fn(NewMessageStore(locker, db))
	})
}

func (s *MessageStore) InRuntimeFenceTransaction(ctx context.Context, botID, sessionID string, fn func(message.Persistence) error) error {
	fence, ok := runtimefence.FromContext(ctx)
	if !ok {
		return errors.New("runtime persistence fence is missing")
	}
	if strings.TrimSpace(botID) == "" {
		botID = fence.BotID
	}
	if strings.TrimSpace(sessionID) == "" {
		sessionID = fence.SessionID
	}
	if err := runtimefence.ValidateScope(ctx, botID, sessionID); err != nil {
		return err
	}
	if s.pool == nil {
		return s.withRuntimeFence(ctx, botID, sessionID, fence.Token, fn)
	}
	return inMessageTransaction(ctx, s.pool, s.bindLocker, func(locker BotSessionWriteLocker, db dbsqlc.DBTX) error {
		return NewMessageStore(locker, db).withRuntimeFence(ctx, botID, sessionID, fence.Token, fn)
	})
}

func (s *MessageStore) withRuntimeFence(
	ctx context.Context,
	botID string,
	sessionID string,
	token int64,
	fn func(message.Persistence) error,
) error {
	bot, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	session, err := db.ParseUUID(sessionID)
	if err != nil {
		return err
	}
	if s.locker == nil {
		return errBotSessionWriteLockerRequired
	}
	if _, err := s.locker.LockBotForSessionWrite(ctx, bot); err != nil {
		return mapMessageError(err)
	}
	if _, err := s.queries.LockSessionRuntimeFence(ctx, dbsqlc.LockSessionRuntimeFenceParams{
		BotID: bot, SessionID: session, RuntimeFencingToken: token,
	}); errors.Is(err, pgx.ErrNoRows) {
		return runtimefence.ErrStale
	} else if err != nil {
		return err
	}
	return fn(s)
}

func (s *MessageStore) CreateMessage(ctx context.Context, record message.Record) (message.Message, error) {
	arg, err := createMessageParams(record)
	if err != nil {
		return message.Message{}, err
	}
	row, err := s.queries.CreateMessage(ctx, arg)
	if err != nil {
		return message.Message{}, mapMessageError(err)
	}
	return messageFromFields(
		row.ID, row.BotID, row.SessionID, row.SenderChannelIdentityID, row.SenderUserID,
		row.SenderDisplayName, row.SenderAvatarUrl, platformFromMetadata(row.Metadata), row.ExternalMessageID,
		row.SourceReplyToMessageID, row.Role, row.Content, row.Metadata, row.Usage,
		row.SessionMode, row.RuntimeType, row.EventID, row.DisplayText, row.CreatedAt), nil
}

func (s *MessageStore) CreateMessageWithHistoryTurn(ctx context.Context, record message.Record, turnID string) (message.Message, error) {
	arg, err := createMessageParams(record)
	if err != nil {
		return message.Message{}, err
	}
	id, err := db.ParseUUID(record.ID)
	if err != nil {
		return message.Message{}, err
	}
	turn, err := db.ParseUUID(turnID)
	if err != nil {
		return message.Message{}, err
	}
	row, err := s.queries.CreateMessageWithHistoryTurn(ctx, dbsqlc.CreateMessageWithHistoryTurnParams{
		MessageID: id, BotID: arg.BotID, SessionID: arg.SessionID,
		SenderChannelIdentityID: arg.SenderChannelIdentityID, SenderUserID: arg.SenderUserID,
		SenderDisplayName: arg.SenderDisplayName, SenderAvatarUrl: arg.SenderAvatarUrl,
		ExternalMessageID: arg.ExternalMessageID, SourceReplyToMessageID: arg.SourceReplyToMessageID,
		Role: arg.Role, Content: arg.Content, Metadata: arg.Metadata, Usage: arg.Usage,
		SessionMode: arg.SessionMode, RuntimeType: arg.RuntimeType, ModelID: arg.ModelID,
		EventID: arg.EventID, DisplayText: arg.DisplayText, TurnID: turn,
		TurnMessageSeq: pgtype.Int8{Int64: 1, Valid: true},
	})
	if err != nil {
		return message.Message{}, mapMessageError(err)
	}
	return messageFromRecord(record, id, row.CreatedAt), nil
}

func (s *MessageStore) CreateMessageInHistoryTurnByRequest(ctx context.Context, record message.Record, requestID string) (message.Message, error) {
	arg, err := createMessageParams(record)
	if err != nil {
		return message.Message{}, err
	}
	request, err := db.ParseUUID(requestID)
	if err != nil {
		return message.Message{}, err
	}
	row, err := s.queries.CreateMessageInHistoryTurnByRequestAndBind(ctx, dbsqlc.CreateMessageInHistoryTurnByRequestAndBindParams{
		Role: arg.Role, SessionID: arg.SessionID, RequestMessageID: request, BotID: arg.BotID,
		SenderChannelIdentityID: arg.SenderChannelIdentityID, SenderUserID: arg.SenderUserID,
		SenderDisplayName: arg.SenderDisplayName, SenderAvatarUrl: arg.SenderAvatarUrl,
		ExternalMessageID: arg.ExternalMessageID, SourceReplyToMessageID: arg.SourceReplyToMessageID,
		Content: arg.Content, Metadata: arg.Metadata, Usage: arg.Usage, SessionMode: arg.SessionMode,
		RuntimeType: arg.RuntimeType, ModelID: arg.ModelID, EventID: arg.EventID, DisplayText: arg.DisplayText,
	})
	if err != nil {
		return message.Message{}, mapMessageError(err)
	}
	return messageFromRecord(record, row.ID, row.CreatedAt), nil
}

func (s *MessageStore) CreateToolTailRound(ctx context.Context, records []message.Record, turnID string) ([]message.Message, error) {
	if len(records) != 4 {
		return nil, fmt.Errorf("tool tail round has %d records, want 4", len(records))
	}
	args := make([]dbsqlc.CreateMessageParams, len(records))
	ids := make([]pgtype.UUID, len(records))
	for i, record := range records {
		var err error
		args[i], err = createMessageParams(record)
		if err != nil {
			return nil, err
		}
		ids[i], err = db.ParseUUID(record.ID)
		if err != nil {
			return nil, err
		}
	}
	turn, err := db.ParseUUID(turnID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.CreateToolTailRound(ctx, toolTailParams(args, ids, turn))
	if err != nil {
		return nil, mapMessageError(err)
	}
	result := make([]message.Message, len(rows))
	for i, row := range rows {
		result[i] = messageFromRecord(records[i], row.ID, row.CreatedAt)
	}
	return result, nil
}

func (s *MessageStore) SupportsAtomicDirectWrites() bool {
	return s != nil && s.queries != nil
}

func (s *MessageStore) DeleteMessages(ctx context.Context, ids []string) error {
	values := make([]pgtype.UUID, 0, len(ids))
	for _, id := range ids {
		value, err := db.ParseUUID(id)
		if err != nil {
			return err
		}
		values = append(values, value)
	}
	return s.queries.DeleteMessagesByIDs(ctx, values)
}

func (s *MessageStore) DeleteMessagesByBot(ctx context.Context, id string) error {
	value, err := db.ParseUUID(id)
	if err != nil {
		return err
	}
	return s.queries.ClearHistoryByBot(ctx, value)
}

func (s *MessageStore) DeleteMessagesBySession(ctx context.Context, id string) error {
	value, err := db.ParseUUID(id)
	if err != nil {
		return err
	}
	return s.queries.ClearHistoryBySession(ctx, value)
}

func (s *MessageStore) GetSessionSnapshot(ctx context.Context, id string) (message.SessionSnapshot, error) {
	value, err := db.ParseUUID(id)
	if err != nil {
		return message.SessionSnapshot{}, err
	}
	row, err := s.queries.GetSessionByID(ctx, value)
	if err != nil {
		return message.SessionSnapshot{}, mapMessageError(err)
	}
	return message.SessionSnapshot{
		ID: db.UUIDString(row.ID), BotID: db.UUIDString(row.BotID), ParentThreadID: db.UUIDString(row.ParentSessionID),
		Type: row.Type, SessionMode: row.SessionMode, RuntimeType: row.RuntimeType,
	}, nil
}

func (s *MessageStore) UpdateSessionMetadata(ctx context.Context, id string, value map[string]any) error {
	sessionID, err := db.ParseUUID(id)
	if err != nil {
		return err
	}
	data, err := json.Marshal(nonNilJSON(value))
	if err != nil {
		return err
	}
	_, err = s.queries.UpdateSessionMetadata(ctx, dbsqlc.UpdateSessionMetadataParams{ID: sessionID, Metadata: data})
	return err
}

func (s *MessageStore) UpdateSessionMetadataWithFence(ctx context.Context, id, botID string, token int64, value map[string]any) error {
	session, err := db.ParseUUID(id)
	if err != nil {
		return err
	}
	bot, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	data, err := json.Marshal(nonNilJSON(value))
	if err != nil {
		return err
	}
	_, err = s.queries.UpdateSessionMetadataWithRuntimeFence(ctx, dbsqlc.UpdateSessionMetadataWithRuntimeFenceParams{
		ID: session, BotID: bot, RuntimeFencingToken: token, Metadata: data,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return runtimefence.ErrStale
	}
	return err
}

func (s *MessageStore) CreateHistoryTurn(ctx context.Context, record message.HistoryTurnCreate) (message.HistoryTurn, error) {
	botID, err := db.ParseUUID(record.BotID)
	if err != nil {
		return message.HistoryTurn{}, err
	}
	sessionID, err := db.ParseUUID(record.SessionID)
	if err != nil {
		return message.HistoryTurn{}, err
	}
	requestID, err := optionalUUID(record.RequestMessageID)
	if err != nil {
		return message.HistoryTurn{}, err
	}
	assistantID, err := optionalUUID(record.AssistantMessageID)
	if err != nil {
		return message.HistoryTurn{}, err
	}
	row, err := s.queries.CreateHistoryTurn(ctx, dbsqlc.CreateHistoryTurnParams{
		BotID: botID, SessionID: sessionID, RequestMessageID: requestID, AssistantMessageID: assistantID,
	})
	if err != nil {
		return message.HistoryTurn{}, mapMessageError(err)
	}
	return historyTurnFromCreate(row), nil
}

func (s *MessageStore) BindHistoryTurnAssistantByRequest(ctx context.Context, sessionID, requestID, assistantID string) (message.HistoryTurn, error) {
	session, request, assistant, err := threeUUIDs(sessionID, requestID, assistantID)
	if err != nil {
		return message.HistoryTurn{}, err
	}
	row, err := s.queries.BindHistoryTurnAssistantByRequest(ctx, dbsqlc.BindHistoryTurnAssistantByRequestParams{
		SessionID: session, RequestMessageID: request, AssistantMessageID: assistant,
	})
	if err != nil {
		return message.HistoryTurn{}, mapMessageError(err)
	}
	return historyTurnFromBind(row), nil
}

func (s *MessageStore) AppendMessageToHistoryTurnByRequest(ctx context.Context, sessionID, requestID, messageID string) error {
	session, request, id, err := threeUUIDs(sessionID, requestID, messageID)
	if err != nil {
		return err
	}
	_, err = s.queries.AppendMessageToHistoryTurnByRequest(ctx, dbsqlc.AppendMessageToHistoryTurnByRequestParams{
		SessionID: session, RequestMessageID: request, MessageID: id,
	})
	return mapMessageError(err)
}

func (s *MessageStore) AppendMessageToLatestHistoryTurn(ctx context.Context, sessionID, messageID string) error {
	session, err := db.ParseUUID(sessionID)
	if err != nil {
		return err
	}
	id, err := db.ParseUUID(messageID)
	if err != nil {
		return err
	}
	_, err = s.queries.AppendMessageToLatestHistoryTurn(ctx, dbsqlc.AppendMessageToLatestHistoryTurnParams{
		SessionID: session, MessageID: id,
	})
	return mapMessageError(err)
}

func (s *MessageStore) LinkMessageToHistoryTurn(ctx context.Context, messageID, turnID string, sequence int64) error {
	id, err := db.ParseUUID(messageID)
	if err != nil {
		return err
	}
	turn, err := db.ParseUUID(turnID)
	if err != nil {
		return err
	}
	_, err = s.queries.LinkMessageToHistoryTurn(ctx, dbsqlc.LinkMessageToHistoryTurnParams{
		MessageID: id, TurnID: turn, TurnMessageSeq: pgtype.Int8{Int64: sequence, Valid: true},
	})
	return mapMessageError(err)
}

func (s *MessageStore) LockHistoryTurnAppendByRequest(ctx context.Context, sessionID, requestID string) error {
	session, err := db.ParseUUID(sessionID)
	if err != nil {
		return err
	}
	request, err := db.ParseUUID(requestID)
	if err != nil {
		return err
	}
	return s.queries.LockHistoryTurnAppendByRequest(ctx, dbsqlc.LockHistoryTurnAppendByRequestParams{
		SessionID: session, RequestMessageID: request,
	})
}

func (s *MessageStore) ReplaceHistoryTurn(ctx context.Context, record message.HistoryTurnReplace) (message.HistoryTurn, error) {
	sessionID, err := db.ParseUUID(record.SessionID)
	if err != nil {
		return message.HistoryTurn{}, err
	}
	oldTurnID, err := db.ParseUUID(record.OldTurnID)
	if err != nil {
		return message.HistoryTurn{}, err
	}
	requestID, err := optionalUUID(record.RequestMessageID)
	if err != nil {
		return message.HistoryTurn{}, err
	}
	assistantID, err := optionalUUID(record.AssistantMessageID)
	if err != nil {
		return message.HistoryTurn{}, err
	}
	row, err := s.queries.ReplaceHistoryTurn(ctx, dbsqlc.ReplaceHistoryTurnParams{
		OldTurnID: oldTurnID, SessionID: sessionID, RequestMessageID: requestID,
		AssistantMessageID: assistantID,
		SupersededAt:       pgtype.Timestamptz{Time: record.SupersededAt, Valid: true},
		SupersededReason:   pgtype.Text{String: record.Reason, Valid: record.Reason != ""},
	})
	if err != nil {
		return message.HistoryTurn{}, mapMessageError(err)
	}
	return historyTurnFromReplace(row), nil
}

func (s *MessageStore) CreateAssetLink(ctx context.Context, link message.AssetLink) error {
	id, err := db.ParseUUID(link.MessageID)
	if err != nil {
		return err
	}
	metadata, err := json.Marshal(nonNilJSON(link.Metadata))
	if err != nil {
		return err
	}
	_, err = s.queries.CreateMessageAsset(ctx, dbsqlc.CreateMessageAssetParams{
		MessageID: id, Role: link.Role, Ordinal: link.Ordinal,
		ContentHash: link.ContentHash, Name: link.Name, Metadata: metadata,
	})
	return err
}

func createMessageParams(record message.Record) (dbsqlc.CreateMessageParams, error) {
	botID, err := db.ParseUUID(record.BotID)
	if err != nil {
		return dbsqlc.CreateMessageParams{}, err
	}
	sessionID, err := optionalUUID(record.SessionID)
	if err != nil {
		return dbsqlc.CreateMessageParams{}, err
	}
	senderChannelID, err := optionalUUID(record.SenderChannelIdentityID)
	if err != nil {
		return dbsqlc.CreateMessageParams{}, err
	}
	senderUserID, err := optionalUUID(record.SenderUserID)
	if err != nil {
		return dbsqlc.CreateMessageParams{}, err
	}
	modelID, err := optionalUUID(record.ModelID)
	if err != nil {
		return dbsqlc.CreateMessageParams{}, err
	}
	eventID, err := optionalUUID(record.EventID)
	if err != nil {
		return dbsqlc.CreateMessageParams{}, err
	}
	metadata, err := json.Marshal(nonNilJSON(record.Metadata))
	if err != nil {
		return dbsqlc.CreateMessageParams{}, err
	}
	return dbsqlc.CreateMessageParams{
		BotID: botID, SessionID: sessionID, SenderChannelIdentityID: senderChannelID,
		SenderUserID: senderUserID, SenderDisplayName: optionalText(record.SenderDisplayName),
		SenderAvatarUrl: optionalText(record.SenderAvatarURL), ExternalMessageID: optionalText(record.ExternalMessageID),
		SourceReplyToMessageID: optionalText(record.SourceReplyToMessageID), Role: record.Role,
		Content: record.Content, Metadata: metadata, Usage: record.Usage,
		SessionMode: record.SessionMode, RuntimeType: record.RuntimeType, ModelID: modelID,
		EventID: eventID, DisplayText: optionalText(record.DisplayText),
	}, nil
}

func mapMessageError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return message.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" &&
		(pgErr.ConstraintName == "idx_bot_history_messages_turn_seq_unique" ||
			strings.Contains(pgErr.Detail, "turn_message_seq")) {
		return fmt.Errorf("%w: %w", message.ErrTurnSequenceConflict, err)
	}
	text := err.Error()
	if strings.Contains(text, "idx_bot_history_messages_turn_seq_unique") ||
		strings.Contains(text, "bot_history_messages.turn_id, bot_history_messages.turn_message_seq") {
		return fmt.Errorf("%w: %w", message.ErrTurnSequenceConflict, err)
	}
	return err
}

func threeUUIDs(a, b, c string) (pgtype.UUID, pgtype.UUID, pgtype.UUID, error) {
	first, err := db.ParseUUID(a)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, err
	}
	second, err := db.ParseUUID(b)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, err
	}
	third, err := db.ParseUUID(c)
	return first, second, third, err
}

func toolTailParams(args []dbsqlc.CreateMessageParams, ids []pgtype.UUID, turnID pgtype.UUID) dbsqlc.CreateToolTailRoundParams {
	return dbsqlc.CreateToolTailRoundParams{
		UserMessageID: ids[0], UserSenderChannelIdentityID: args[0].SenderChannelIdentityID,
		UserSenderUserID: args[0].SenderUserID, UserSenderDisplayName: args[0].SenderDisplayName,
		UserSenderAvatarUrl: args[0].SenderAvatarUrl, UserExternalMessageID: args[0].ExternalMessageID,
		UserSourceReplyToMessageID: args[0].SourceReplyToMessageID, UserContent: args[0].Content,
		UserMetadata: args[0].Metadata, UserUsage: args[0].Usage, UserSessionMode: args[0].SessionMode,
		UserRuntimeType: args[0].RuntimeType, UserModelID: args[0].ModelID, UserEventID: args[0].EventID,
		UserDisplayText:                          args[0].DisplayText,
		ToolCallAssistantMessageID:               ids[1],
		ToolCallAssistantSenderChannelIdentityID: args[1].SenderChannelIdentityID,
		ToolCallAssistantSenderUserID:            args[1].SenderUserID,
		ToolCallAssistantSenderDisplayName:       args[1].SenderDisplayName,
		ToolCallAssistantSenderAvatarUrl:         args[1].SenderAvatarUrl,
		ToolCallAssistantExternalMessageID:       args[1].ExternalMessageID,
		ToolCallAssistantSourceReplyToMessageID:  args[1].SourceReplyToMessageID,
		ToolCallAssistantContent:                 args[1].Content, ToolCallAssistantMetadata: args[1].Metadata,
		ToolCallAssistantUsage: args[1].Usage, ToolCallAssistantSessionMode: args[1].SessionMode,
		ToolCallAssistantRuntimeType: args[1].RuntimeType, ToolCallAssistantModelID: args[1].ModelID,
		ToolCallAssistantEventID: args[1].EventID, ToolCallAssistantDisplayText: args[1].DisplayText,
		ToolMessageID: ids[2], ToolSenderChannelIdentityID: args[2].SenderChannelIdentityID,
		ToolSenderUserID: args[2].SenderUserID, ToolSenderDisplayName: args[2].SenderDisplayName,
		ToolSenderAvatarUrl: args[2].SenderAvatarUrl, ToolExternalMessageID: args[2].ExternalMessageID,
		ToolSourceReplyToMessageID: args[2].SourceReplyToMessageID, ToolContent: args[2].Content,
		ToolMetadata: args[2].Metadata, ToolUsage: args[2].Usage, ToolSessionMode: args[2].SessionMode,
		ToolRuntimeType: args[2].RuntimeType, ToolModelID: args[2].ModelID, ToolEventID: args[2].EventID,
		ToolDisplayText:                       args[2].DisplayText,
		FinalAssistantMessageID:               ids[3],
		FinalAssistantSenderChannelIdentityID: args[3].SenderChannelIdentityID,
		FinalAssistantSenderUserID:            args[3].SenderUserID,
		FinalAssistantSenderDisplayName:       args[3].SenderDisplayName,
		FinalAssistantSenderAvatarUrl:         args[3].SenderAvatarUrl,
		FinalAssistantExternalMessageID:       args[3].ExternalMessageID,
		FinalAssistantSourceReplyToMessageID:  args[3].SourceReplyToMessageID,
		FinalAssistantContent:                 args[3].Content, FinalAssistantMetadata: args[3].Metadata,
		FinalAssistantUsage: args[3].Usage, FinalAssistantSessionMode: args[3].SessionMode,
		FinalAssistantRuntimeType: args[3].RuntimeType, FinalAssistantModelID: args[3].ModelID,
		FinalAssistantEventID: args[3].EventID, FinalAssistantDisplayText: args[3].DisplayText,
		BotID: args[0].BotID, SessionID: args[0].SessionID, TurnID: turnID,
	}
}
