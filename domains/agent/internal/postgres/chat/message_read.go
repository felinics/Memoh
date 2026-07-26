package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/domains/agent/chat/message"
	dbsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

func (s *MessageStore) ListMessages(ctx context.Context, query message.ListQuery) ([]message.Message, error) {
	botID, err := optionalUUID(query.BotID)
	if err != nil {
		return nil, err
	}
	sessionID, err := optionalUUID(query.SessionID)
	if err != nil {
		return nil, err
	}
	since := pgtype.Timestamptz{Time: query.Since, Valid: !query.Since.IsZero()}
	before := pgtype.Timestamptz{Time: query.Before, Valid: !query.Before.IsZero()}
	switch query.Scope {
	case message.ListAll:
		rows, err := s.queries.ListMessages(ctx, botID)
		if err != nil {
			return nil, err
		}
		result := make([]message.Message, 0, len(rows))
		for _, row := range rows {
			result = append(result, messageFromFields(
				row.ID, row.BotID, row.SessionID, row.SenderChannelIdentityID, row.SenderUserID,
				row.SenderDisplayName, row.SenderAvatarUrl, row.Platform, row.ExternalMessageID,
				row.SourceReplyToMessageID, row.Role, row.Content, row.Metadata, row.Usage,
				row.SessionMode, row.RuntimeType, row.EventID, row.DisplayText, row.CreatedAt))
		}
		return result, nil
	case message.ListSince:
		rows, err := s.queries.ListMessagesSince(ctx, dbsqlc.ListMessagesSinceParams{BotID: botID, CreatedAt: since})
		if err != nil {
			return nil, err
		}
		result := make([]message.Message, 0, len(rows))
		for _, row := range rows {
			result = append(result, messageFromFields(
				row.ID, row.BotID, row.SessionID, row.SenderChannelIdentityID, row.SenderUserID,
				row.SenderDisplayName, row.SenderAvatarUrl, row.Platform, row.ExternalMessageID,
				row.SourceReplyToMessageID, row.Role, row.Content, row.Metadata, row.Usage,
				row.SessionMode, row.RuntimeType, row.EventID, row.DisplayText, row.CreatedAt))
		}
		return result, nil
	case message.ListActiveSince:
		rows, err := s.queries.ListActiveMessagesSince(ctx, dbsqlc.ListActiveMessagesSinceParams{BotID: botID, CreatedAt: since})
		if err != nil {
			return nil, err
		}
		result := make([]message.Message, 0, len(rows))
		for _, row := range rows {
			item := messageFromFields(
				row.ID, row.BotID, row.SessionID, row.SenderChannelIdentityID, row.SenderUserID,
				row.SenderDisplayName, row.SenderAvatarUrl, row.Platform, row.ExternalMessageID,
				row.SourceReplyToMessageID, row.Role, row.Content, row.Metadata, row.Usage,
				row.SessionMode, row.RuntimeType, row.EventID, row.DisplayText, row.CreatedAt)
			item.CompactID = db.UUIDString(row.CompactID)
			result = append(result, item)
		}
		return result, nil
	case message.ListLatest:
		rows, err := s.queries.ListMessagesLatest(ctx, dbsqlc.ListMessagesLatestParams{BotID: botID, MaxCount: query.Limit})
		if err != nil {
			return nil, err
		}
		result := make([]message.Message, 0, len(rows))
		for _, row := range rows {
			result = append(result, messageFromFields(
				row.ID, row.BotID, row.SessionID, row.SenderChannelIdentityID, row.SenderUserID,
				row.SenderDisplayName, row.SenderAvatarUrl, row.Platform, row.ExternalMessageID,
				row.SourceReplyToMessageID, row.Role, row.Content, row.Metadata, row.Usage,
				row.SessionMode, row.RuntimeType, row.EventID, row.DisplayText, row.CreatedAt))
		}
		return result, nil
	case message.ListBefore:
		rows, err := s.queries.ListMessagesBefore(ctx, dbsqlc.ListMessagesBeforeParams{
			BotID: botID, CreatedAt: before, MaxCount: query.Limit,
		})
		if err != nil {
			return nil, err
		}
		result := make([]message.Message, 0, len(rows))
		for i := len(rows) - 1; i >= 0; i-- {
			row := rows[i]
			result = append(result, messageFromFields(
				row.ID, row.BotID, row.SessionID, row.SenderChannelIdentityID, row.SenderUserID,
				row.SenderDisplayName, row.SenderAvatarUrl, row.Platform, row.ExternalMessageID,
				row.SourceReplyToMessageID, row.Role, row.Content, row.Metadata, row.Usage,
				row.SessionMode, row.RuntimeType, row.EventID, row.DisplayText, row.CreatedAt))
		}
		return result, nil
	case message.ListSession:
		rows, err := s.queries.ListMessagesBySession(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		result := make([]message.Message, 0, len(rows))
		for _, row := range rows {
			result = append(result, messageFromFields(
				row.ID, row.BotID, row.SessionID, row.SenderChannelIdentityID, row.SenderUserID,
				row.SenderDisplayName, row.SenderAvatarUrl, row.Platform, row.ExternalMessageID,
				row.SourceReplyToMessageID, row.Role, row.Content, row.Metadata, row.Usage,
				row.SessionMode, row.RuntimeType, row.EventID, row.DisplayText, row.CreatedAt))
		}
		return result, nil
	case message.ListSessionSince:
		rows, err := s.queries.ListMessagesSinceBySession(ctx, dbsqlc.ListMessagesSinceBySessionParams{
			SessionID: sessionID, CreatedAt: since,
		})
		if err != nil {
			return nil, err
		}
		result := make([]message.Message, 0, len(rows))
		for _, row := range rows {
			result = append(result, messageFromFields(
				row.ID, row.BotID, row.SessionID, row.SenderChannelIdentityID, row.SenderUserID,
				row.SenderDisplayName, row.SenderAvatarUrl, row.Platform, row.ExternalMessageID,
				row.SourceReplyToMessageID, row.Role, row.Content, row.Metadata, row.Usage,
				row.SessionMode, row.RuntimeType, row.EventID, row.DisplayText, row.CreatedAt))
		}
		return result, nil
	case message.ListSessionActiveSince:
		rows, err := s.queries.ListActiveMessagesSinceBySession(ctx, dbsqlc.ListActiveMessagesSinceBySessionParams{
			SessionID: sessionID, CreatedAt: since,
		})
		if err != nil {
			return nil, err
		}
		result := make([]message.Message, 0, len(rows))
		for _, row := range rows {
			item := messageFromFields(
				row.ID, row.BotID, row.SessionID, row.SenderChannelIdentityID, row.SenderUserID,
				row.SenderDisplayName, row.SenderAvatarUrl, row.Platform, row.ExternalMessageID,
				row.SourceReplyToMessageID, row.Role, row.Content, row.Metadata, row.Usage,
				row.SessionMode, row.RuntimeType, row.EventID, row.DisplayText, row.CreatedAt)
			item.CompactID = db.UUIDString(row.CompactID)
			result = append(result, item)
		}
		return result, nil
	case message.ListSessionLatest:
		rows, err := s.queries.ListMessagesLatestBySession(ctx, dbsqlc.ListMessagesLatestBySessionParams{
			SessionID: sessionID, MaxCount: query.Limit,
		})
		if err != nil {
			return nil, err
		}
		result := make([]message.Message, 0, len(rows))
		for _, row := range rows {
			result = append(result, messageFromFields(
				row.ID, row.BotID, row.SessionID, row.SenderChannelIdentityID, row.SenderUserID,
				row.SenderDisplayName, row.SenderAvatarUrl, row.Platform, row.ExternalMessageID,
				row.SourceReplyToMessageID, row.Role, row.Content, row.Metadata, row.Usage,
				row.SessionMode, row.RuntimeType, row.EventID, row.DisplayText, row.CreatedAt))
		}
		return result, nil
	case message.ListSessionLatestUI:
		rows, err := s.queries.ListMessagesLatestUIBySession(ctx, dbsqlc.ListMessagesLatestUIBySessionParams{
			SessionID: sessionID, MaxCount: query.Limit,
		})
		if err != nil {
			return nil, err
		}
		result := make([]message.Message, 0, len(rows))
		for _, row := range rows {
			result = append(result, messageFromFields(
				row.ID, row.BotID, row.SessionID, row.SenderChannelIdentityID, row.SenderUserID,
				row.SenderDisplayName, row.SenderAvatarUrl, row.Platform, row.ExternalMessageID,
				row.SourceReplyToMessageID, row.Role, row.Content, row.Metadata, nil, "", "",
				pgtype.UUID{}, row.DisplayText, row.CreatedAt))
		}
		return result, nil
	case message.ListSessionBefore:
		rows, err := s.queries.ListMessagesBeforeBySession(ctx, dbsqlc.ListMessagesBeforeBySessionParams{
			SessionID: sessionID, CreatedAt: before, MaxCount: query.Limit,
		})
		if err != nil {
			return nil, err
		}
		result := make([]message.Message, 0, len(rows))
		for i := len(rows) - 1; i >= 0; i-- {
			row := rows[i]
			result = append(result, messageFromFields(
				row.ID, row.BotID, row.SessionID, row.SenderChannelIdentityID, row.SenderUserID,
				row.SenderDisplayName, row.SenderAvatarUrl, row.Platform, row.ExternalMessageID,
				row.SourceReplyToMessageID, row.Role, row.Content, row.Metadata, row.Usage,
				row.SessionMode, row.RuntimeType, row.EventID, row.DisplayText, row.CreatedAt))
		}
		return result, nil
	default:
		return nil, fmt.Errorf("unsupported message list scope %q", query.Scope)
	}
}

func (s *MessageStore) GetVisibleMessageCursor(ctx context.Context, sessionID, messageID string) (message.MessageCursor, error) {
	session, id, err := twoUUIDs(sessionID, messageID)
	if err != nil {
		return message.MessageCursor{}, err
	}
	row, err := s.queries.GetVisibleMessageCursorByIDBySession(ctx, dbsqlc.GetVisibleMessageCursorByIDBySessionParams{
		SessionID: session, MessageID: id,
	})
	if err != nil {
		return message.MessageCursor{}, mapMessageError(err)
	}
	return message.MessageCursor{
		TurnPosition: row.TurnPosition.Int64, TurnMessageSeq: row.TurnMessageSeq.Int64,
		CreatedAt: row.CreatedAt.Time, MessageID: db.UUIDString(row.ID),
	}, nil
}

func (s *MessageStore) ListMessagesBeforeCursor(ctx context.Context, sessionID string, cursor message.MessageCursor, limit int32) ([]message.Message, error) {
	session, err := db.ParseUUID(sessionID)
	if err != nil {
		return nil, err
	}
	id, err := db.ParseUUID(cursor.MessageID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListMessagesBeforeCursorBySession(ctx, dbsqlc.ListMessagesBeforeCursorBySessionParams{
		SessionID: session, CursorTurnPosition: cursor.TurnPosition,
		CursorTurnMessageSeq: cursor.TurnMessageSeq,
		CursorCreatedAt:      pgtype.Timestamptz{Time: cursor.CreatedAt, Valid: true},
		CursorMessageID:      id, MaxCount: limit,
	})
	if err != nil {
		return nil, err
	}
	result := make([]message.Message, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		row := rows[i]
		result = append(result, messageFromFields(
			row.ID, row.BotID, row.SessionID, row.SenderChannelIdentityID, row.SenderUserID,
			row.SenderDisplayName, row.SenderAvatarUrl, row.Platform, row.ExternalMessageID,
			row.SourceReplyToMessageID, row.Role, row.Content, row.Metadata, row.Usage,
			row.SessionMode, row.RuntimeType, row.EventID, row.DisplayText, row.CreatedAt))
	}
	return result, nil
}

func (s *MessageStore) LocateMessages(ctx context.Context, sessionID, externalID string, beforeLimit, afterLimit int32) (message.LocateResult, error) {
	session, err := db.ParseUUID(sessionID)
	if err != nil {
		return message.LocateResult{}, err
	}
	rows, err := s.queries.LocateMessagesWindowByExternalIDBySession(ctx, dbsqlc.LocateMessagesWindowByExternalIDBySessionParams{
		SessionID: session, ExternalMessageID: optionalText(externalID),
		BeforeLimit: beforeLimit, AfterLimit: afterLimit,
	})
	if err != nil {
		return message.LocateResult{}, err
	}
	if len(rows) == 0 {
		return message.LocateResult{}, message.ErrNotFound
	}
	if !rows[0].TargetTurnMessageSeq.Valid {
		return message.LocateResult{}, errors.New("message cursor missing turn sequence")
	}
	result := message.LocateResult{Messages: make([]message.Message, 0, len(rows)), TargetID: db.UUIDString(rows[0].TargetID)}
	for _, row := range rows {
		result.Messages = append(result.Messages, messageFromFields(
			row.ID, row.BotID, row.SessionID, row.SenderChannelIdentityID, row.SenderUserID,
			row.SenderDisplayName, row.SenderAvatarUrl, row.Platform, row.ExternalMessageID,
			row.SourceReplyToMessageID, row.Role, row.Content, row.Metadata, row.Usage,
			row.SessionMode, row.RuntimeType, row.EventID, row.DisplayText, row.CreatedAt))
	}
	return result, nil
}

func (s *MessageStore) GetMessage(ctx context.Context, sessionID, messageID string) (message.Message, error) {
	session, id, err := twoUUIDs(sessionID, messageID)
	if err != nil {
		return message.Message{}, err
	}
	row, err := s.queries.GetMessageByIDBySession(ctx, dbsqlc.GetMessageByIDBySessionParams{
		SessionID: session, MessageID: id,
	})
	if err != nil {
		return message.Message{}, mapMessageError(err)
	}
	return messageFromFields(
		row.ID, row.BotID, row.SessionID, row.SenderChannelIdentityID, row.SenderUserID,
		row.SenderDisplayName, row.SenderAvatarUrl, row.Platform, row.ExternalMessageID,
		row.SourceReplyToMessageID, row.Role, row.Content, row.Metadata, row.Usage,
		row.SessionMode, row.RuntimeType, row.EventID, row.DisplayText, row.CreatedAt), nil
}

func (s *MessageStore) ListVisibleMessagesFrom(ctx context.Context, sessionID, messageID string) ([]message.Message, error) {
	session, id, err := twoUUIDs(sessionID, messageID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListVisibleMessagesFromBySession(ctx, dbsqlc.ListVisibleMessagesFromBySessionParams{
		SessionID: session, MessageID: id,
	})
	if err != nil {
		return nil, err
	}
	result := make([]message.Message, 0, len(rows))
	for _, row := range rows {
		item := messageFromFields(
			row.ID, row.BotID, row.SessionID, row.SenderChannelIdentityID, row.SenderUserID,
			row.SenderDisplayName, row.SenderAvatarUrl, row.Platform, row.ExternalMessageID,
			row.SourceReplyToMessageID, row.Role, row.Content, row.Metadata, row.Usage,
			row.SessionMode, row.RuntimeType, row.EventID, row.DisplayText, row.CreatedAt)
		item.CompactID = db.UUIDString(row.CompactID)
		result = append(result, item)
	}
	return result, nil
}

func (s *MessageStore) GetVisibleHistoryTurn(ctx context.Context, sessionID, messageID string) (message.HistoryTurn, error) {
	session, id, err := twoUUIDs(sessionID, messageID)
	if err != nil {
		return message.HistoryTurn{}, err
	}
	row, err := s.queries.GetVisibleHistoryTurnByMessage(ctx, dbsqlc.GetVisibleHistoryTurnByMessageParams{
		SessionID: session, MessageID: id,
	})
	if err != nil {
		return message.HistoryTurn{}, mapMessageError(err)
	}
	return historyTurnFromVisible(row), nil
}

func (s *MessageStore) GetLatestVisibleHistoryTurn(ctx context.Context, sessionID string) (message.HistoryTurn, error) {
	session, err := db.ParseUUID(sessionID)
	if err != nil {
		return message.HistoryTurn{}, err
	}
	row, err := s.queries.GetLatestVisibleHistoryTurnBySession(ctx, session)
	if err != nil {
		return message.HistoryTurn{}, mapMessageError(err)
	}
	return historyTurnFromLatest(row), nil
}

func (s *MessageStore) ListAssetLinks(ctx context.Context, ids []string) (map[string][]message.MessageAsset, error) {
	values := make([]pgtype.UUID, 0, len(ids))
	for _, id := range ids {
		value, err := db.ParseUUID(id)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	rows, err := s.queries.ListMessageAssetsBatch(ctx, values)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]message.MessageAsset)
	for _, row := range rows {
		if strings.TrimSpace(row.ContentHash) == "" {
			continue
		}
		result[db.UUIDString(row.MessageID)] = append(result[db.UUIDString(row.MessageID)], message.MessageAsset{
			ContentHash: strings.TrimSpace(row.ContentHash), Role: row.Role,
			Ordinal: int(row.Ordinal), Name: row.Name, Metadata: parseJSONMap(row.Metadata),
		})
	}
	return result, nil
}

func messageFromRecord(record message.Record, id pgtype.UUID, createdAt pgtype.Timestamptz) message.Message {
	metadata, _ := json.Marshal(nonNilJSON(record.Metadata))
	return messageFromFields(
		id, mustUUID(record.BotID), mustOptionalUUID(record.SessionID),
		mustOptionalUUID(record.SenderChannelIdentityID), mustOptionalUUID(record.SenderUserID),
		optionalText(record.SenderDisplayName), optionalText(record.SenderAvatarURL), platformFromMetadata(metadata),
		optionalText(record.ExternalMessageID), optionalText(record.SourceReplyToMessageID),
		record.Role, record.Content, metadata, record.Usage, record.SessionMode, record.RuntimeType,
		mustOptionalUUID(record.EventID), optionalText(record.DisplayText), createdAt)
}

func messageFromFields(
	id, botID, sessionID, senderChannelID, senderUserID pgtype.UUID,
	senderDisplayName, senderAvatarURL, platform, externalID, replyID pgtype.Text,
	role string,
	content, metadata, usage []byte,
	sessionMode, runtimeType string,
	eventID pgtype.UUID,
	displayText pgtype.Text,
	createdAt pgtype.Timestamptz,
) message.Message {
	result := message.Message{
		ID: db.UUIDString(id), BotID: db.UUIDString(botID), SessionID: db.UUIDString(sessionID),
		SenderChannelIdentityID: db.UUIDString(senderChannelID), SenderUserID: db.UUIDString(senderUserID),
		SenderDisplayName: db.TextToString(senderDisplayName), SenderAvatarURL: db.TextToString(senderAvatarURL),
		Platform: db.TextToString(platform), ExternalMessageID: db.TextToString(externalID),
		SourceReplyToMessageID: db.TextToString(replyID), Role: role,
		Content: append(json.RawMessage(nil), content...), RawMetadata: append(json.RawMessage(nil), metadata...),
		Usage: append(json.RawMessage(nil), usage...), SessionMode: sessionMode, RuntimeType: runtimeType,
		EventID: db.UUIDString(eventID), DisplayContent: db.TextToString(displayText), CreatedAt: createdAt.Time,
	}
	result.Metadata = parseJSONMap(metadata)
	return result
}

func historyTurnFromVisible(row dbsqlc.GetVisibleHistoryTurnByMessageRow) message.HistoryTurn {
	return historyTurnFromFields(
		row.ID, row.BotID, row.SessionID, row.Position, row.RequestMessageID, row.AssistantMessageID,
		row.SupersededByTurnID, row.SupersededAt, row.SupersededReason, row.CreatedAt, row.UpdatedAt,
	)
}

func historyTurnFromLatest(row dbsqlc.GetLatestVisibleHistoryTurnBySessionRow) message.HistoryTurn {
	return historyTurnFromFields(
		row.ID, row.BotID, row.SessionID, row.Position, row.RequestMessageID, row.AssistantMessageID,
		row.SupersededByTurnID, row.SupersededAt, row.SupersededReason, row.CreatedAt, row.UpdatedAt,
	)
}

func historyTurnFromCreate(row dbsqlc.CreateHistoryTurnRow) message.HistoryTurn {
	return historyTurnFromFields(
		row.ID, row.BotID, row.SessionID, row.Position, row.RequestMessageID, row.AssistantMessageID,
		row.SupersededByTurnID, row.SupersededAt, db.TextToString(row.SupersededReason), row.CreatedAt, row.UpdatedAt,
	)
}

func historyTurnFromBind(row dbsqlc.BindHistoryTurnAssistantByRequestRow) message.HistoryTurn {
	return historyTurnFromFields(
		row.ID, row.BotID, row.SessionID, row.Position, row.RequestMessageID, row.AssistantMessageID,
		row.SupersededByTurnID, row.SupersededAt, db.TextToString(row.SupersededReason), row.CreatedAt, row.UpdatedAt,
	)
}

func historyTurnFromFields(
	id, botID, sessionID pgtype.UUID,
	position int64,
	requestMessageID, assistantMessageID, supersededByTurnID pgtype.UUID,
	supersededAt pgtype.Timestamptz,
	supersededReason string,
	createdAt, updatedAt pgtype.Timestamptz,
) message.HistoryTurn {
	return message.HistoryTurn{
		ID: db.UUIDString(id), BotID: db.UUIDString(botID), SessionID: db.UUIDString(sessionID),
		Position: position, RequestMessageID: db.UUIDString(requestMessageID),
		AssistantMessageID: db.UUIDString(assistantMessageID),
		SupersededByTurnID: db.UUIDString(supersededByTurnID),
		SupersededAt:       timestamp(supersededAt), SupersededReason: supersededReason,
		CreatedAt: timestamp(createdAt), UpdatedAt: timestamp(updatedAt),
	}
}

func historyTurnFromReplace(row dbsqlc.ReplaceHistoryTurnRow) message.HistoryTurn {
	return message.HistoryTurn{
		ID: db.UUIDString(row.ID), BotID: db.UUIDString(row.BotID), SessionID: db.UUIDString(row.SessionID),
		Position: row.Position, RequestMessageID: db.UUIDString(row.RequestMessageID),
		AssistantMessageID: db.UUIDString(row.AssistantMessageID),
		SupersededByTurnID: db.UUIDString(row.SupersededByTurnID),
		SupersededAt:       timestamp(row.SupersededAt), SupersededReason: db.TextToString(row.SupersededReason),
		CreatedAt: timestamp(row.CreatedAt), UpdatedAt: timestamp(row.UpdatedAt),
	}
}

func timestamp(value pgtype.Timestamptz) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time
}

func platformFromMetadata(data []byte) pgtype.Text {
	value := parseJSONMap(data)
	platform, _ := value["platform"].(string)
	return optionalText(platform)
}

func twoUUIDs(a, b string) (pgtype.UUID, pgtype.UUID, error) {
	first, err := db.ParseUUID(a)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}
	second, err := db.ParseUUID(b)
	return first, second, err
}

func mustUUID(value string) pgtype.UUID {
	id, _ := db.ParseUUID(value)
	return id
}

func mustOptionalUUID(value string) pgtype.UUID {
	id, _ := optionalUUID(value)
	return id
}
