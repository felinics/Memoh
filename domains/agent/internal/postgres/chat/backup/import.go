package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	chatbackup "github.com/memohai/memoh/domains/agent/chat/backup"
	"github.com/memohai/memoh/domains/agent/chat/thread"
	agentsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
)

func (s *Store) Import(ctx context.Context, request chatbackup.ImportRequest) (chatbackup.ImportResult, error) {
	if _, err := uuid.Parse(request.BotID); err != nil {
		return chatbackup.ImportResult{}, fmt.Errorf("invalid chat import bot id: %w", err)
	}
	if request.ActorUserID != "" {
		if _, err := uuid.Parse(request.ActorUserID); err != nil {
			return chatbackup.ImportResult{}, fmt.Errorf("invalid chat import actor user id: %w", err)
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return chatbackup.ImportResult{}, fmt.Errorf("begin chat history import: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.botLock.LockBotExclusive(ctx, tx, request.BotID); err != nil {
		return chatbackup.ImportResult{}, fmt.Errorf("lock chat import bot: %w", err)
	}
	if request.Replace {
		queries := agentsqlc.New(tx)
		if err := queries.ClearHistoryByBot(ctx, mustPGUUID(request.BotID)); err != nil {
			return chatbackup.ImportResult{}, fmt.Errorf("clear chat history: %w", err)
		}
		if err := queries.SoftDeleteSessionsByBot(ctx, mustPGUUID(request.BotID)); err != nil {
			return chatbackup.ImportResult{}, fmt.Errorf("clear chat sessions: %w", err)
		}
	}

	sessionIDs := deterministicIDs(request.BotID, "session", sessionSourceIDs(request.Sessions))
	messageIDs := deterministicIDs(request.BotID, "message", messageSourceIDs(request.Messages))
	if err := importSessions(ctx, tx, request, sessionIDs, messageIDs); err != nil {
		return chatbackup.ImportResult{}, err
	}

	messages := reconstructLegacyTurns(request.Messages)
	eventReferences, err := importMessages(ctx, tx, request, messages, sessionIDs, messageIDs)
	if err != nil {
		return chatbackup.ImportResult{}, err
	}
	if err := importAssets(ctx, tx, request.BotID, request.Assets, messageIDs); err != nil {
		return chatbackup.ImportResult{}, err
	}
	if err := updateNextTurnPositions(ctx, tx, messages, sessionIDs); err != nil {
		return chatbackup.ImportResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return chatbackup.ImportResult{}, fmt.Errorf("commit chat history import: %w", err)
	}

	receipt := chatbackup.ImportReceipt{
		BotID:      request.BotID,
		SessionIDs: mapValues(sessionIDs),
		MessageIDs: mapValues(messageIDs),
		Replace:    request.Replace,
	}
	return chatbackup.ImportResult{
		SessionIDs:      sessionIDs,
		MessageIDs:      messageIDs,
		EventReferences: eventReferences,
		Receipt:         receipt,
	}, nil
}

func (s *Store) BindEventReferences(ctx context.Context, request chatbackup.BindEventReferencesRequest) error {
	if _, err := uuid.Parse(request.BotID); err != nil {
		return fmt.Errorf("invalid event binding bot id: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin chat event binding: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.botLock.LockBotExclusive(ctx, tx, request.BotID); err != nil {
		return fmt.Errorf("lock chat import bot: %w", err)
	}
	for _, binding := range request.Bindings {
		if _, err := uuid.Parse(binding.MessageID); err != nil {
			return fmt.Errorf("invalid event binding message id: %w", err)
		}
		if _, err := uuid.Parse(binding.EventID); err != nil {
			return fmt.Errorf("invalid event binding event id: %w", err)
		}
		tag, err := tx.Exec(ctx, `
			UPDATE agent.bot_history_messages
			SET event_id = $3
			WHERE team_id = iam.memoh_current_team_id()
			  AND bot_id = $1
			  AND id = $2
		`, request.BotID, binding.MessageID, binding.EventID)
		if err != nil {
			return fmt.Errorf("bind imported message event: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return errors.New("bind imported message event: message not found")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit chat event binding: %w", err)
	}
	return nil
}

func (s *Store) Compensate(ctx context.Context, receipt chatbackup.ImportReceipt) error {
	if _, err := uuid.Parse(receipt.BotID); err != nil {
		return fmt.Errorf("invalid chat compensation bot id: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin chat import compensation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.botLock.LockBotExclusive(ctx, tx, receipt.BotID); err != nil {
		return fmt.Errorf("lock chat import bot: %w", err)
	}
	if len(receipt.MessageIDs) > 0 {
		if _, err := tx.Exec(ctx, `
			DELETE FROM agent.bot_history_messages
			WHERE team_id = iam.memoh_current_team_id()
			  AND bot_id = $1
			  AND id = ANY($2::uuid[])
		`, receipt.BotID, receipt.MessageIDs); err != nil {
			return fmt.Errorf("compensate imported messages: %w", err)
		}
	}
	if len(receipt.SessionIDs) > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE agent.bot_sessions
			SET deleted_at = now(), updated_at = now()
			WHERE team_id = iam.memoh_current_team_id()
			  AND bot_id = $1
			  AND id = ANY($2::uuid[])
			  AND deleted_at IS NULL
		`, receipt.BotID, receipt.SessionIDs); err != nil {
			return fmt.Errorf("compensate imported sessions: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit chat import compensation: %w", err)
	}
	return nil
}

func importSessions(
	ctx context.Context,
	tx pgx.Tx,
	request chatbackup.ImportRequest,
	sessionIDs map[string]string,
	messageIDs map[string]string,
) error {
	pending := append([]chatbackup.Session(nil), request.Sessions...)
	inserted := make(map[string]struct{}, len(pending))
	for len(pending) > 0 {
		progressed := false
		next := make([]chatbackup.Session, 0, len(pending))
		for _, item := range pending {
			if item.ParentSessionID != nil {
				if _, ok := inserted[*item.ParentSessionID]; !ok {
					next = append(next, item)
					continue
				}
			}
			if err := importSession(ctx, tx, request, item, sessionIDs, messageIDs, true); err != nil {
				return err
			}
			inserted[item.ID] = struct{}{}
			progressed = true
		}
		if progressed {
			pending = next
			continue
		}
		for _, item := range next {
			if err := importSession(ctx, tx, request, item, sessionIDs, messageIDs, false); err != nil {
				return err
			}
		}
		break
	}
	return nil
}

func importSession(
	ctx context.Context,
	tx pgx.Tx,
	request chatbackup.ImportRequest,
	item chatbackup.Session,
	sessionIDs map[string]string,
	messageIDs map[string]string,
	keepParent bool,
) error {
	newID := sessionIDs[item.ID]
	if newID == "" {
		return errors.New("chat import session has no source id")
	}
	legacyType, sessionMode, runtimeType, err := restoredDescriptor(item.Type, item.SessionMode, item.RuntimeType)
	if err != nil {
		return fmt.Errorf("restore session descriptor: %w", err)
	}
	metadata := defaultJSONObject(item.Metadata)
	runtimeMetadata := defaultJSONObject(item.RuntimeMetadata)
	if runtimeType == thread.RuntimeACPAgent {
		metadata = rebindRuntimeOwner(metadata, request.ActorUserID)
		runtimeMetadata = rebindRuntimeOwner(runtimeMetadata, request.ActorUserID)
	}
	metadata = rebindForkMetadata(metadata, sessionIDs, messageIDs)

	var parentID *string
	if keepParent {
		parentID = mappedPointer(item.ParentSessionID, sessionIDs)
	}
	routeID := mappedPointer(item.RouteID, request.RouteIDs)
	createdAt := optionalTime(item.CreatedAt)
	updatedAt := optionalTime(item.UpdatedAt)
	var actorID *string
	if request.ActorUserID != "" {
		actorID = &request.ActorUserID
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO agent.bot_sessions (
			id, bot_id, route_id, channel_type, type, session_mode, runtime_type,
			runtime_metadata, title, metadata, parent_session_id, created_by_user_id,
			created_at, updated_at, deleted_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8::jsonb, $9, $10::jsonb, $11, $12,
			COALESCE($13::timestamptz, now()), COALESCE($14::timestamptz, now()), NULL
		)
		ON CONFLICT (id) DO UPDATE SET
			route_id = EXCLUDED.route_id,
			channel_type = EXCLUDED.channel_type,
			type = EXCLUDED.type,
			session_mode = EXCLUDED.session_mode,
			runtime_type = EXCLUDED.runtime_type,
			runtime_metadata = EXCLUDED.runtime_metadata,
			title = EXCLUDED.title,
			metadata = EXCLUDED.metadata,
			parent_session_id = EXCLUDED.parent_session_id,
			created_by_user_id = EXCLUDED.created_by_user_id,
			created_at = EXCLUDED.created_at,
			updated_at = EXCLUDED.updated_at,
			deleted_at = NULL
		WHERE bot_sessions.team_id = iam.memoh_current_team_id()
		  AND bot_sessions.bot_id = EXCLUDED.bot_id
	`, newID, request.BotID, routeID, item.ChannelType, legacyType, sessionMode, runtimeType,
		runtimeMetadata, item.Title, metadata, parentID, actorID, createdAt, updatedAt)
	if err != nil {
		return fmt.Errorf("restore chat session: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("restore chat session: deterministic id belongs to another bot")
	}
	return nil
}

func importMessages(
	ctx context.Context,
	tx pgx.Tx,
	request chatbackup.ImportRequest,
	messages []chatbackup.Message,
	sessionIDs map[string]string,
	messageIDs map[string]string,
) ([]chatbackup.EventReference, error) {
	turnIDs := make(map[string]string)
	for _, item := range messages {
		if item.TurnID != nil && *item.TurnID != "" {
			turnIDs[*item.TurnID] = deterministicID(request.BotID, "turn", *item.TurnID)
		}
	}

	references := make([]chatbackup.EventReference, 0)
	for _, item := range messages {
		newID := messageIDs[item.ID]
		if newID == "" {
			return nil, errors.New("chat import message has no source id")
		}
		sessionID := mappedPointer(item.SessionID, sessionIDs)
		senderChannelID := mappedPointer(item.SenderChannelIdentityID, request.ChannelIdentityIDs)
		senderUserID := mappedPointer(item.SenderUserID, request.UserIDs)
		turnID := mappedPointer(item.TurnID, turnIDs)
		supersededBy := mappedPointer(item.TurnSupersededByTurnID, turnIDs)
		content := item.Content
		if len(bytes.TrimSpace(content)) == 0 {
			return nil, fmt.Errorf("restore chat message %s: content is empty", item.ID)
		}
		metadata := defaultJSONObject(item.Metadata)
		createdAt := optionalTime(item.CreatedAt)
		tag, err := tx.Exec(ctx, `
			INSERT INTO agent.bot_history_messages (
				id, bot_id, session_id, sender_channel_identity_id,
				sender_account_user_id, source_message_id, source_reply_to_message_id,
				role, content, metadata, usage, session_mode, runtime_type, event_id,
				display_text, created_at, turn_id, turn_position, turn_message_seq,
				turn_visible, turn_superseded_by_turn_id, turn_superseded_at,
				turn_superseded_reason
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7,
				$8, $9::jsonb, $10::jsonb, $11::jsonb, $12, $13, NULL,
				$14, COALESCE($15::timestamptz, now()), $16, $17, $18,
				$19, $20, $21, $22
			)
			ON CONFLICT (id) DO UPDATE SET
				session_id = EXCLUDED.session_id,
				sender_channel_identity_id = EXCLUDED.sender_channel_identity_id,
				sender_account_user_id = EXCLUDED.sender_account_user_id,
				source_message_id = EXCLUDED.source_message_id,
				source_reply_to_message_id = EXCLUDED.source_reply_to_message_id,
				role = EXCLUDED.role,
				content = EXCLUDED.content,
				metadata = EXCLUDED.metadata,
				usage = EXCLUDED.usage,
				session_mode = EXCLUDED.session_mode,
				runtime_type = EXCLUDED.runtime_type,
				event_id = NULL,
				display_text = EXCLUDED.display_text,
				created_at = EXCLUDED.created_at,
				turn_id = EXCLUDED.turn_id,
				turn_position = EXCLUDED.turn_position,
				turn_message_seq = EXCLUDED.turn_message_seq,
				turn_visible = EXCLUDED.turn_visible,
				turn_superseded_by_turn_id = EXCLUDED.turn_superseded_by_turn_id,
				turn_superseded_at = EXCLUDED.turn_superseded_at,
				turn_superseded_reason = EXCLUDED.turn_superseded_reason
			WHERE bot_history_messages.team_id = iam.memoh_current_team_id()
			  AND bot_history_messages.bot_id = EXCLUDED.bot_id
		`, newID, request.BotID, sessionID, senderChannelID, senderUserID,
			item.ExternalMessageID, item.SourceReplyToMessageID, item.Role,
			content, metadata, nullableJSON(item.Usage), item.SessionMode,
			item.RuntimeType, item.DisplayText, createdAt, turnID,
			item.TurnPosition, item.TurnMessageSeq, item.TurnVisible,
			supersededBy, item.TurnSupersededAt, item.TurnSupersededReason)
		if err != nil {
			return nil, fmt.Errorf("restore chat message: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return nil, errors.New("restore chat message: deterministic id belongs to another bot")
		}
		if item.EventID != nil && *item.EventID != "" {
			references = append(references, chatbackup.EventReference{
				MessageID:  newID,
				OldEventID: *item.EventID,
			})
		}
	}
	return references, nil
}

func importAssets(
	ctx context.Context,
	tx pgx.Tx,
	botID string,
	assets []chatbackup.Asset,
	messageIDs map[string]string,
) error {
	for _, item := range assets {
		messageID := messageIDs[item.MessageID]
		if messageID == "" {
			continue
		}
		assetID := deterministicID(botID, "asset", item.RelID)
		if item.RelID == "" {
			assetID = deterministicID(botID, "asset", fmt.Sprintf("%s:%s:%d", item.MessageID, item.Role, item.Ordinal))
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO agent.bot_history_message_assets (
				id, message_id, role, ordinal, content_hash, name, metadata
			) VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb)
			ON CONFLICT (id) DO UPDATE SET
				message_id = EXCLUDED.message_id,
				role = EXCLUDED.role,
				ordinal = EXCLUDED.ordinal,
				content_hash = EXCLUDED.content_hash,
				name = EXCLUDED.name,
				metadata = EXCLUDED.metadata
			WHERE bot_history_message_assets.team_id = iam.memoh_current_team_id()
		`, assetID, messageID, item.Role, item.Ordinal, item.ContentHash,
			item.Name, defaultJSONObject(item.Metadata))
		if err != nil {
			return fmt.Errorf("restore chat message asset: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return errors.New("restore chat message asset: deterministic id conflict")
		}
	}
	return nil
}

func updateNextTurnPositions(
	ctx context.Context,
	tx pgx.Tx,
	messages []chatbackup.Message,
	sessionIDs map[string]string,
) error {
	nextPositions := make(map[string]int64)
	for _, item := range messages {
		if item.SessionID == nil || item.TurnPosition == nil {
			continue
		}
		next := *item.TurnPosition + 1
		if next > nextPositions[*item.SessionID] {
			nextPositions[*item.SessionID] = next
		}
	}
	for oldSessionID, next := range nextPositions {
		if next <= 1 {
			continue
		}
		if _, err := tx.Exec(ctx, `
			UPDATE agent.bot_sessions
			SET next_turn_position = GREATEST(next_turn_position, $1)
			WHERE team_id = iam.memoh_current_team_id() AND id = $2
		`, next, sessionIDs[oldSessionID]); err != nil {
			return fmt.Errorf("restore session next turn position: %w", err)
		}
	}
	return nil
}

type legacyTurnState struct {
	id       string
	position int64
	seq      int64
}

func reconstructLegacyTurns(source []chatbackup.Message) []chatbackup.Message {
	messages := append([]chatbackup.Message(nil), source...)
	nextPosition := make(map[string]int64)
	for _, item := range messages {
		if item.SessionID != nil && item.TurnPosition != nil && *item.TurnPosition >= nextPosition[*item.SessionID] {
			nextPosition[*item.SessionID] = *item.TurnPosition + 1
		}
	}
	states := make(map[string]*legacyTurnState)
	for i := range messages {
		item := &messages[i]
		if item.SessionID == nil || *item.SessionID == "" {
			continue
		}
		sessionID := *item.SessionID
		if item.TurnID != nil && item.TurnPosition != nil && item.TurnMessageSeq != nil {
			states[sessionID] = nil
			continue
		}
		state := states[sessionID]
		role := strings.ToLower(strings.TrimSpace(item.Role))
		if role == "user" || state == nil {
			position := nextPosition[sessionID]
			if position <= 0 {
				position = 1
			}
			seq := int64(1)
			if role != "user" {
				seq = 2
			}
			state = &legacyTurnState{
				id:       fmt.Sprintf("legacy:%s:%d", sessionID, position),
				position: position,
				seq:      seq,
			}
			states[sessionID] = state
			nextPosition[sessionID] = position + 1
		} else {
			state.seq++
		}
		item.TurnID = pointer(state.id)
		item.TurnPosition = pointer(state.position)
		item.TurnMessageSeq = pointer(state.seq)
		item.TurnVisible = true
	}
	return messages
}

func restoredDescriptor(legacyType, sessionMode, runtimeType string) (string, string, string, error) {
	sessionMode = strings.TrimSpace(sessionMode)
	runtimeType = strings.TrimSpace(runtimeType)
	if !thread.IsKnownSessionMode(sessionMode) || !thread.IsKnownRuntimeType(runtimeType) {
		derivedMode, derivedRuntime := thread.DescriptorFromLegacyType(legacyType)
		if !thread.IsKnownSessionMode(sessionMode) {
			sessionMode = derivedMode
		}
		if !thread.IsKnownRuntimeType(runtimeType) {
			runtimeType = derivedRuntime
		}
	}
	return thread.ResolveDescriptor(legacyType, sessionMode, runtimeType)
}

func rebindForkMetadata(raw []byte, sessionIDs, messageIDs map[string]string) []byte {
	var metadata map[string]any
	if err := json.Unmarshal(defaultJSONObject(raw), &metadata); err != nil || metadata == nil {
		return defaultJSONObject(raw)
	}
	fork, ok := metadata["forked_from"].(map[string]any)
	if !ok {
		return raw
	}
	remapJSONID(fork, "session_id", sessionIDs)
	remapJSONID(fork, "message_id", messageIDs)
	remapJSONID(fork, "fork_message_id", messageIDs)
	out, err := json.Marshal(metadata)
	if err != nil {
		return raw
	}
	return out
}

func rebindRuntimeOwner(raw []byte, actorUserID string) []byte {
	var metadata map[string]any
	if err := json.Unmarshal(defaultJSONObject(raw), &metadata); err != nil || metadata == nil {
		metadata = map[string]any{}
	}
	delete(metadata, "runtime_owner_account_id")
	if actorUserID != "" {
		metadata["runtime_owner_account_id"] = actorUserID
	}
	out, err := json.Marshal(metadata)
	if err != nil {
		return []byte(`{}`)
	}
	return out
}

func remapJSONID(value map[string]any, key string, ids map[string]string) {
	oldID, ok := value[key].(string)
	if !ok {
		return
	}
	if newID := ids[strings.TrimSpace(oldID)]; newID != "" {
		value[key] = newID
	}
}

func deterministicIDs(botID, kind string, source []string) map[string]string {
	result := make(map[string]string, len(source))
	for _, oldID := range source {
		if oldID != "" {
			result[oldID] = deterministicID(botID, kind, oldID)
		}
	}
	return result
}

func deterministicID(botID, kind, oldID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(kind+"\x00"+botID+"\x00"+oldID)).String()
}

func sessionSourceIDs(items []chatbackup.Session) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, item.ID)
	}
	return result
}

func messageSourceIDs(items []chatbackup.Message) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, item.ID)
	}
	return result
}

func mappedPointer(oldID *string, ids map[string]string) *string {
	if oldID == nil {
		return nil
	}
	newID := ids[*oldID]
	if newID == "" {
		return nil
	}
	return &newID
}

func mapValues(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func defaultJSONObject(raw []byte) []byte {
	if len(bytes.TrimSpace(raw)) == 0 {
		return []byte(`{}`)
	}
	return raw
}

func nullableJSON(raw []byte) any {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return raw
}

func optionalTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func pointer[T any](value T) *T {
	return &value
}

func mustPGUUID(value string) pgtype.UUID {
	return pgtype.UUID{Bytes: uuid.MustParse(value), Valid: true}
}
