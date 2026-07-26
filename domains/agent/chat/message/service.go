package message

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/memohai/memoh/domains/agent/chat/event"
	runtimefence "github.com/memohai/memoh/domains/agent/chat/session/fence"
)

// DBService persists and reads bot history messages through Message-owned ports.
type DBService struct {
	store     Persistence
	logger    *slog.Logger
	publisher event.Publisher
}

func NewService(log *slog.Logger, store Persistence, publishers ...event.Publisher) *DBService {
	if log == nil {
		log = slog.Default()
	}
	var publisher event.Publisher
	if len(publishers) > 0 {
		publisher = publishers[0]
	}
	return &DBService{
		store:     store,
		logger:    log.With(slog.String("service", "chat/message")),
		publisher: publisher,
	}
}

func (s *DBService) Persist(ctx context.Context, input PersistInput) (Message, error) {
	const maxTurnSequenceRetries = 3
	var lastErr error
	for range maxTurnSequenceRetries {
		result, err := s.persistOnce(ctx, input)
		if err == nil {
			if !input.SkipHistoryTurn {
				s.publishMessageCreated(result)
			}
			return result, nil
		}
		if !errors.Is(err, ErrTurnSequenceConflict) {
			return Message{}, err
		}
		lastErr = err
	}
	return Message{}, lastErr
}

func (s *DBService) persistOnce(ctx context.Context, input PersistInput) (Message, error) {
	if _, fenced := runtimefence.FromContext(ctx); fenced {
		var result Message
		err := s.store.InRuntimeFenceTransaction(ctx, input.BotID, input.SessionID, func(store Persistence) error {
			txService := *s
			txService.store = store
			var err error
			result, err = txService.persist(ctx, input)
			return err
		})
		return result, err
	}
	if result, handled, err := s.persistDirectWithoutTx(ctx, input); handled || err != nil {
		return result, err
	}
	if shouldPersistMessageInTx(input) {
		var result Message
		err := s.store.InTransaction(ctx, func(store Persistence) error {
			txService := *s
			txService.store = store
			var err error
			result, err = txService.persist(ctx, input)
			return err
		})
		return result, err
	}
	return s.persist(ctx, input)
}

func (s *DBService) PersistToolTailRound(ctx context.Context, inputs []PersistInput) ([]Message, bool, error) {
	if s == nil || s.store == nil || !isToolTailRoundShape(inputs) || !s.store.SupportsAtomicDirectWrites() {
		return nil, false, nil
	}
	records := make([]Record, len(inputs))
	for i, input := range inputs {
		input.TurnRequestMessageID = ""
		record, err := s.prepareRecord(ctx, input)
		if err != nil {
			return nil, true, err
		}
		if i > 0 && (record.BotID != records[0].BotID || record.SessionID != records[0].SessionID) {
			return nil, false, nil
		}
		record.ID = uuid.NewString()
		records[i] = record
	}
	rows, err := s.store.CreateToolTailRound(ctx, records, uuid.NewString())
	if err != nil {
		return nil, true, err
	}
	if len(rows) != len(records) {
		return nil, true, fmt.Errorf("create tool tail round returned %d messages, want %d", len(rows), len(records))
	}
	for _, message := range rows {
		s.publishMessageCreated(message)
	}
	return rows, true, nil
}

func (s *DBService) PersistRound(ctx context.Context, inputs []PersistInput, options RoundPersistenceOptions) ([]Message, bool, error) {
	if s == nil || s.store == nil || len(inputs) == 0 {
		return nil, false, nil
	}
	_, fenced := runtimefence.FromContext(ctx)
	if !fenced && options.Replacement == nil {
		return nil, false, nil
	}
	botID := strings.TrimSpace(inputs[0].BotID)
	sessionID := strings.TrimSpace(inputs[0].SessionID)
	if botID == "" || sessionID == "" {
		return nil, true, errors.New("atomic round requires bot and session ids")
	}
	for _, input := range inputs[1:] {
		if strings.TrimSpace(input.BotID) != botID || strings.TrimSpace(input.SessionID) != sessionID {
			return nil, true, errors.New("atomic round spans multiple sessions")
		}
	}

	const maxTurnSequenceRetries = 3
	var lastErr error
	for range maxTurnSequenceRetries {
		persisted := make([]Message, 0, len(inputs))
		write := func(store Persistence) error {
			txService := *s
			txService.store = store
			txService.publisher = nil
			requestID := strings.TrimSpace(inputs[0].TurnRequestMessageID)
			for _, original := range inputs {
				input := original
				if !input.SkipHistoryTurn {
					input.TurnRequestMessageID = requestID
				}
				message, err := txService.persist(ctx, input)
				if err != nil {
					return err
				}
				if strings.EqualFold(strings.TrimSpace(input.Role), "user") && !input.SkipHistoryTurn {
					requestID = message.ID
				}
				persisted = append(persisted, message)
			}
			if options.Replacement != nil {
				return txService.replacePersistedRound(ctx, sessionID, persisted, *options.Replacement)
			}
			return nil
		}
		var err error
		if fenced {
			err = s.store.InRuntimeFenceTransaction(ctx, botID, sessionID, write)
		} else {
			err = s.store.InTransaction(ctx, write)
		}
		if err == nil {
			for i, message := range persisted {
				if !inputs[i].SkipHistoryTurn {
					s.publishMessageCreated(message)
				}
			}
			return persisted, true, nil
		}
		lastErr = err
		if !errors.Is(err, ErrTurnSequenceConflict) {
			return nil, true, err
		}
	}
	return nil, true, lastErr
}

func isToolTailRoundShape(inputs []PersistInput) bool {
	if len(inputs) != 4 || strings.TrimSpace(inputs[0].BotID) == "" || strings.TrimSpace(inputs[0].SessionID) == "" {
		return false
	}
	roles := [4]string{"user", "assistant", "tool", "assistant"}
	for i, input := range inputs {
		if input.SkipHistoryTurn || len(input.Assets) > 0 ||
			!strings.EqualFold(strings.TrimSpace(input.Role), roles[i]) ||
			strings.TrimSpace(input.BotID) != strings.TrimSpace(inputs[0].BotID) ||
			strings.TrimSpace(input.SessionID) != strings.TrimSpace(inputs[0].SessionID) {
			return false
		}
	}
	return true
}

func shouldPersistMessageInTx(input PersistInput) bool {
	return !input.SkipHistoryTurn || len(input.Assets) > 0
}

func (s *DBService) prepareRecord(ctx context.Context, input PersistInput) (Record, error) {
	for _, field := range []struct {
		name     string
		value    string
		required bool
	}{
		{"bot id", input.BotID, true},
		{"session id", input.SessionID, false},
		{"sender channel identity id", input.SenderChannelIdentityID, false},
		{"sender user id", input.SenderUserID, false},
		{"model id", input.ModelID, false},
		{"event id", input.EventID, false},
		{"turn request message id", input.TurnRequestMessageID, false},
	} {
		if err := validateUUID(field.name, field.value, field.required); err != nil {
			return Record{}, err
		}
	}
	metadata := nonNilMap(input.Metadata)
	if _, err := json.Marshal(metadata); err != nil {
		return Record{}, fmt.Errorf("marshal message metadata: %w", err)
	}
	content := append([]byte(nil), input.Content...)
	if len(content) == 0 {
		content = []byte("{}")
	}
	sessionMode, runtimeType := s.resolveRuntimeSnapshot(ctx, input.SessionID, input.SessionMode, input.RuntimeType)
	return Record{
		BotID: input.BotID, SessionID: input.SessionID,
		SenderChannelIdentityID: input.SenderChannelIdentityID, SenderUserID: input.SenderUserID,
		SenderDisplayName: input.SenderDisplayName, SenderAvatarURL: input.SenderAvatarURL,
		ExternalMessageID: input.ExternalMessageID, SourceReplyToMessageID: input.SourceReplyToMessageID,
		Role: input.Role, Content: content, Metadata: metadata, Usage: append([]byte(nil), input.Usage...),
		SessionMode: sessionMode, RuntimeType: runtimeType, ModelID: input.ModelID,
		EventID: input.EventID, DisplayText: input.DisplayText,
	}, nil
}

func (s *DBService) persistDirectWithoutTx(ctx context.Context, input PersistInput) (Message, bool, error) {
	if input.SkipHistoryTurn || len(input.Assets) > 0 || strings.TrimSpace(input.SessionID) == "" ||
		!s.store.SupportsAtomicDirectWrites() {
		return Message{}, false, nil
	}
	record, err := s.prepareRecord(ctx, input)
	if err != nil {
		return Message{}, true, err
	}
	return s.persistDirectHistoryMessage(ctx, record, input.Role, input.TurnRequestMessageID)
}

func (s *DBService) persist(ctx context.Context, input PersistInput) (Message, error) {
	record, err := s.prepareRecord(ctx, input)
	if err != nil {
		return Message{}, err
	}
	if !input.SkipHistoryTurn {
		if result, handled, err := s.persistDirectHistoryMessage(ctx, record, input.Role, input.TurnRequestMessageID); handled {
			if err != nil {
				return Message{}, err
			}
			return s.finishPersistedMessage(ctx, result, input.Assets)
		}
	}
	result, err := s.store.CreateMessage(ctx, record)
	if err != nil {
		return Message{}, err
	}
	if !input.SkipHistoryTurn {
		if err := s.persistHistoryTurn(ctx, record.BotID, record.SessionID, result.ID, input.Role, input.TurnRequestMessageID); err != nil {
			s.cleanupPersistedMessage(ctx, result.ID)
			return Message{}, err
		}
	}
	return s.finishPersistedMessage(ctx, result, input.Assets)
}

func (s *DBService) persistDirectHistoryMessage(ctx context.Context, record Record, role, requestID string) (Message, bool, error) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user":
		record.ID = uuid.NewString()
		result, err := s.store.CreateMessageWithHistoryTurn(ctx, record, uuid.NewString())
		return result, true, err
	case "assistant", "tool":
		if strings.TrimSpace(requestID) == "" {
			return Message{}, false, nil
		}
		record.ID = uuid.NewString()
		result, err := s.store.CreateMessageInHistoryTurnByRequest(ctx, record, requestID)
		if errors.Is(err, ErrNotFound) {
			return Message{}, false, nil
		}
		return result, true, err
	default:
		return Message{}, false, nil
	}
}

func (s *DBService) finishPersistedMessage(ctx context.Context, result Message, assets []AssetRef) (Message, error) {
	for _, ref := range assets {
		contentHash := strings.TrimSpace(ref.ContentHash)
		if contentHash == "" {
			s.logger.WarnContext(ctx, "skip asset ref without content_hash")
			continue
		}
		if ref.Ordinal < math.MinInt32 || ref.Ordinal > math.MaxInt32 {
			return Message{}, fmt.Errorf("asset ordinal out of range: %d", ref.Ordinal)
		}
		err := s.store.CreateAssetLink(ctx, AssetLink{
			MessageID: result.ID, Role: coalesce(ref.Role, "attachment"),
			Ordinal: int32(ref.Ordinal), ContentHash: contentHash, Name: ref.Name, Metadata: ref.Metadata,
		})
		if err != nil {
			return Message{}, fmt.Errorf("create message asset link for %s: %w", result.ID, err)
		}
	}
	if len(assets) > 0 {
		result.Assets = make([]MessageAsset, 0, len(assets))
		for _, ref := range assets {
			if strings.TrimSpace(ref.ContentHash) == "" {
				continue
			}
			result.Assets = append(result.Assets, MessageAsset{
				ContentHash: strings.TrimSpace(ref.ContentHash), Role: coalesce(ref.Role, "attachment"),
				Ordinal: ref.Ordinal, Mime: ref.Mime, SizeBytes: ref.SizeBytes,
				StorageKey: ref.StorageKey, Name: ref.Name, Metadata: ref.Metadata,
			})
		}
	}
	return result, nil
}

func (s *DBService) cleanupPersistedMessage(ctx context.Context, messageID string) {
	if strings.TrimSpace(messageID) == "" {
		return
	}
	if err := s.store.DeleteMessages(ctx, []string{messageID}); err != nil {
		s.logger.ErrorContext(ctx, "cleanup message after history turn failure failed",
			slog.String("message_id", messageID), slog.Any("error", err))
	}
}

func (s *DBService) persistHistoryTurn(ctx context.Context, botID, sessionID, messageID, role, requestID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user":
		turn, err := s.store.CreateHistoryTurn(ctx, HistoryTurnCreate{
			BotID: botID, SessionID: sessionID, RequestMessageID: messageID,
		})
		if err != nil {
			return fmt.Errorf("create history turn: %w", err)
		}
		if err := s.store.LinkMessageToHistoryTurn(ctx, messageID, turn.ID, 1); err != nil {
			return fmt.Errorf("link user message to history turn: %w", err)
		}
	case "assistant":
		if requestID != "" {
			if err := s.store.LockHistoryTurnAppendByRequest(ctx, sessionID, requestID); err != nil {
				return fmt.Errorf("lock requested history turn append: %w", err)
			}
			if turn, err := s.store.BindHistoryTurnAssistantByRequest(ctx, sessionID, requestID, messageID); err == nil {
				if err := s.store.LinkMessageToHistoryTurn(ctx, messageID, turn.ID, 2); err != nil {
					return fmt.Errorf("link assistant message to requested history turn: %w", err)
				}
				return nil
			} else if !errors.Is(err, ErrNotFound) {
				return fmt.Errorf("bind history turn assistant by request: %w", err)
			}
			if err := s.store.AppendMessageToHistoryTurnByRequest(ctx, sessionID, requestID, messageID); err == nil {
				return nil
			} else if !errors.Is(err, ErrNotFound) {
				return fmt.Errorf("append assistant message to requested history turn: %w", err)
			}
		}
		if err := s.store.AppendMessageToLatestHistoryTurn(ctx, sessionID, messageID); err == nil {
			return nil
		} else if !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("append assistant message to latest history turn: %w", err)
		}
		turn, err := s.store.CreateHistoryTurn(ctx, HistoryTurnCreate{
			BotID: botID, SessionID: sessionID, AssistantMessageID: messageID,
		})
		if err != nil {
			return fmt.Errorf("create orphan assistant history turn: %w", err)
		}
		if err := s.store.LinkMessageToHistoryTurn(ctx, messageID, turn.ID, 2); err != nil {
			return fmt.Errorf("link orphan assistant message to history turn: %w", err)
		}
	case "tool":
		if requestID != "" {
			if err := s.store.LockHistoryTurnAppendByRequest(ctx, sessionID, requestID); err != nil {
				return fmt.Errorf("lock requested history turn append: %w", err)
			}
			if err := s.store.AppendMessageToHistoryTurnByRequest(ctx, sessionID, requestID, messageID); err == nil {
				return nil
			} else if !errors.Is(err, ErrNotFound) {
				return fmt.Errorf("append tool message to requested history turn: %w", err)
			}
		}
		if err := s.store.AppendMessageToLatestHistoryTurn(ctx, sessionID, messageID); err != nil {
			return fmt.Errorf("append tool message to latest history turn: %w", err)
		}
	}
	return nil
}

func (s *DBService) resolveRuntimeSnapshot(ctx context.Context, sessionID, sessionMode, runtimeType string) (string, string) {
	sessionMode = normalizeSessionMode(sessionMode)
	runtimeType = normalizeRuntimeType(runtimeType)
	if sessionMode != "" && runtimeType != "" && sessionMode != "subagent" {
		return sessionMode, runtimeType
	}
	if strings.TrimSpace(sessionID) != "" {
		if row, err := s.store.GetSessionSnapshot(ctx, sessionID); err == nil {
			rowMode, rowRuntime := sessionSnapshot(row)
			if rowMode == "subagent" && row.ParentThreadID != "" {
				if parent, parentErr := s.store.GetSessionSnapshot(ctx, row.ParentThreadID); parentErr == nil {
					parentMode, parentRuntime := sessionSnapshot(parent)
					if sessionMode == "" || sessionMode == "subagent" {
						sessionMode = parentMode
					}
					if runtimeType == "" {
						runtimeType = parentRuntime
					}
				}
			}
			if sessionMode == "" {
				sessionMode = rowMode
			}
			if runtimeType == "" {
				runtimeType = rowRuntime
			}
		}
	}
	if sessionMode == "" {
		sessionMode = "chat"
	}
	if runtimeType == "" {
		runtimeType = "model"
	}
	return sessionMode, runtimeType
}

func sessionSnapshot(row SessionSnapshot) (string, string) {
	mode := normalizeSessionMode(row.SessionMode)
	if mode == "" {
		mode = legacySessionMode(row.Type)
	}
	runtimeType := normalizeRuntimeType(row.RuntimeType)
	if runtimeType == "" {
		runtimeType = legacyRuntimeType(row.Type)
	}
	return mode, runtimeType
}

func normalizeSessionMode(mode string) string {
	switch strings.TrimSpace(mode) {
	case "chat", "discuss", "heartbeat", "schedule", "subagent":
		return strings.TrimSpace(mode)
	default:
		return ""
	}
}

func normalizeRuntimeType(runtimeType string) string {
	switch strings.TrimSpace(runtimeType) {
	case "model", "acp_agent":
		return strings.TrimSpace(runtimeType)
	default:
		return ""
	}
}

func legacySessionMode(typ string) string {
	switch strings.TrimSpace(typ) {
	case "acp_agent":
		return "chat"
	case "discuss", "heartbeat", "schedule", "subagent":
		return strings.TrimSpace(typ)
	default:
		return "chat"
	}
}

func legacyRuntimeType(typ string) string {
	if strings.TrimSpace(typ) == "acp_agent" {
		return "acp_agent"
	}
	return "model"
}

func (s *DBService) List(ctx context.Context, botID string) ([]Message, error) {
	return s.list(ctx, ListQuery{Scope: ListAll, BotID: botID})
}

func (s *DBService) ListSince(ctx context.Context, botID string, since time.Time) ([]Message, error) {
	return s.list(ctx, ListQuery{Scope: ListSince, BotID: botID, Since: since})
}

func (s *DBService) ListActiveSince(ctx context.Context, botID string, since time.Time) ([]Message, error) {
	return s.list(ctx, ListQuery{Scope: ListActiveSince, BotID: botID, Since: since})
}

func (s *DBService) ListLatest(ctx context.Context, botID string, limit int32) ([]Message, error) {
	return s.list(ctx, ListQuery{Scope: ListLatest, BotID: botID, Limit: limit})
}

func (s *DBService) ListBefore(ctx context.Context, botID string, before time.Time, limit int32) ([]Message, error) {
	return s.list(ctx, ListQuery{Scope: ListBefore, BotID: botID, Before: before, Limit: limit})
}

func (s *DBService) ListBySession(ctx context.Context, sessionID string) ([]Message, error) {
	return s.list(ctx, ListQuery{Scope: ListSession, SessionID: sessionID})
}

func (s *DBService) ListSinceBySession(ctx context.Context, sessionID string, since time.Time) ([]Message, error) {
	return s.list(ctx, ListQuery{Scope: ListSessionSince, SessionID: sessionID, Since: since})
}

func (s *DBService) ListActiveSinceBySession(ctx context.Context, sessionID string, since time.Time) ([]Message, error) {
	return s.list(ctx, ListQuery{Scope: ListSessionActiveSince, SessionID: sessionID, Since: since})
}

func (s *DBService) ListLatestBySession(ctx context.Context, sessionID string, limit int32) ([]Message, error) {
	return s.list(ctx, ListQuery{Scope: ListSessionLatest, SessionID: sessionID, Limit: limit})
}

func (s *DBService) ListLatestUIBySession(ctx context.Context, sessionID string, limit int32) ([]Message, error) {
	return s.list(ctx, ListQuery{Scope: ListSessionLatestUI, SessionID: sessionID, Limit: limit})
}

func (s *DBService) ListBeforeBySession(ctx context.Context, sessionID string, before time.Time, limit int32) ([]Message, error) {
	return s.list(ctx, ListQuery{Scope: ListSessionBefore, SessionID: sessionID, Before: before, Limit: limit})
}

func (s *DBService) list(ctx context.Context, query ListQuery) ([]Message, error) {
	if query.BotID != "" {
		if err := validateUUID("bot id", query.BotID, true); err != nil {
			return nil, err
		}
	}
	if query.SessionID != "" {
		if err := validateUUID("session id", query.SessionID, true); err != nil {
			return nil, err
		}
	}
	messages, err := s.store.ListMessages(ctx, query)
	if err != nil {
		return nil, err
	}
	s.enrichAssets(ctx, messages)
	return messages, nil
}

func (s *DBService) ListBeforeMessageBySession(ctx context.Context, sessionID, beforeMessageID string, limit int32) ([]Message, error) {
	if err := validateUUID("session id", sessionID, true); err != nil {
		return nil, err
	}
	if err := validateUUID("message id", beforeMessageID, true); err != nil {
		return nil, err
	}
	cursor, err := s.store.GetVisibleMessageCursor(ctx, sessionID, beforeMessageID)
	if errors.Is(err, ErrNotFound) {
		return []Message{}, nil
	}
	if err != nil {
		return nil, err
	}
	messages, err := s.store.ListMessagesBeforeCursor(ctx, sessionID, cursor, limit)
	if err != nil {
		return nil, err
	}
	s.enrichAssets(ctx, messages)
	return messages, nil
}

func (s *DBService) LocateByExternalIDBySession(ctx context.Context, sessionID, externalID string, beforeLimit, afterLimit int32) (LocateResult, error) {
	if err := validateUUID("session id", sessionID, true); err != nil {
		return LocateResult{}, err
	}
	externalID = strings.TrimSpace(externalID)
	if externalID == "" {
		return LocateResult{}, errors.New("external message id is required")
	}
	result, err := s.store.LocateMessages(ctx, sessionID, externalID, max(0, beforeLimit), max(0, afterLimit))
	if err != nil {
		return LocateResult{}, err
	}
	s.enrichAssets(ctx, result.Messages)
	return result, nil
}

func (s *DBService) GetByIDBySession(ctx context.Context, sessionID, messageID string) (Message, error) {
	message, err := s.store.GetMessage(ctx, sessionID, messageID)
	if err != nil {
		return Message{}, err
	}
	messages := []Message{message}
	s.enrichAssets(ctx, messages)
	return messages[0], nil
}

func (s *DBService) ListVisibleFromBySession(ctx context.Context, sessionID, messageID string) ([]Message, error) {
	messages, err := s.store.ListVisibleMessagesFrom(ctx, sessionID, messageID)
	if err != nil {
		return nil, err
	}
	s.enrichAssets(ctx, messages)
	return messages, nil
}

func (s *DBService) GetVisibleTurnByMessage(ctx context.Context, sessionID, messageID string) (HistoryTurn, error) {
	return s.store.GetVisibleHistoryTurn(ctx, sessionID, messageID)
}

func (s *DBService) GetLatestVisibleTurnBySession(ctx context.Context, sessionID string) (HistoryTurn, error) {
	return s.store.GetLatestVisibleHistoryTurn(ctx, sessionID)
}

func (s *DBService) replacePersistedRound(ctx context.Context, sessionID string, persisted []Message, replacement TurnReplacement) error {
	requestID := strings.TrimSpace(replacement.RequestMessageID)
	if requestID == "" {
		requestID = firstPersistedRoleID(persisted, "user")
	}
	assistantID := firstPersistedRoleID(persisted, "assistant")
	if assistantID == "" {
		return errors.New("replacement assistant message was not persisted")
	}
	if _, err := s.replaceHistoryTurn(ctx, sessionID, replacement.OldTurnID, requestID, assistantID, replacement.Reason); err != nil {
		return fmt.Errorf("replace persisted history turn: %w", err)
	}
	if replacement.SessionMetadata == nil {
		return nil
	}
	if fence, fenced := runtimefence.FromContext(ctx); fenced {
		if err := s.store.UpdateSessionMetadataWithFence(ctx, sessionID, fence.BotID, fence.Token, replacement.SessionMetadata); err != nil {
			return fmt.Errorf("update replacement session metadata: %w", err)
		}
		return nil
	}
	if err := s.store.UpdateSessionMetadata(ctx, sessionID, replacement.SessionMetadata); err != nil {
		return fmt.Errorf("update replacement session metadata: %w", err)
	}
	return nil
}

func firstPersistedRoleID(messages []Message, role string) string {
	for _, message := range messages {
		if strings.EqualFold(strings.TrimSpace(message.Role), role) && strings.TrimSpace(message.ID) != "" {
			return strings.TrimSpace(message.ID)
		}
	}
	return ""
}

func (s *DBService) replaceHistoryTurn(ctx context.Context, sessionID, oldTurnID, requestID, assistantID, reason string) (HistoryTurn, error) {
	for _, field := range []struct {
		name     string
		value    string
		required bool
	}{
		{"session id", sessionID, true},
		{"old turn id", oldTurnID, true},
		{"request message id", requestID, false},
		{"assistant message id", assistantID, false},
	} {
		if err := validateUUID(field.name, field.value, field.required); err != nil {
			return HistoryTurn{}, err
		}
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "replace"
	}
	return s.store.ReplaceHistoryTurn(ctx, HistoryTurnReplace{
		SessionID: sessionID, OldTurnID: oldTurnID, RequestMessageID: requestID,
		AssistantMessageID: assistantID, SupersededAt: time.Now().UTC(), Reason: reason,
	})
}

func (s *DBService) ReplaceTurn(ctx context.Context, sessionID, oldTurnID, requestID, assistantID, reason string) (HistoryTurn, error) {
	var result HistoryTurn
	write := func(store Persistence) error {
		txService := *s
		txService.store = store
		var err error
		result, err = txService.replaceHistoryTurn(ctx, sessionID, oldTurnID, requestID, assistantID, reason)
		return err
	}
	var err error
	if _, fenced := runtimefence.FromContext(ctx); fenced {
		err = s.store.InRuntimeFenceTransaction(ctx, "", sessionID, write)
	} else {
		err = write(s.store)
	}
	return result, err
}

func (s *DBService) LinkAssets(ctx context.Context, messageID string, assets []AssetRef) error {
	if err := validateUUID("message id", messageID, true); err != nil {
		return err
	}
	link := func(store Persistence) error {
		if fence, fenced := runtimefence.FromContext(ctx); fenced {
			message, err := store.GetMessage(ctx, fence.SessionID, messageID)
			if err != nil {
				return err
			}
			if message.BotID != fence.BotID {
				return runtimefence.ErrStale
			}
		}
		txService := *s
		txService.store = store
		_, err := txService.finishPersistedMessage(ctx, Message{ID: messageID}, assets)
		return err
	}
	if fence, fenced := runtimefence.FromContext(ctx); fenced {
		return s.store.InRuntimeFenceTransaction(ctx, fence.BotID, fence.SessionID, link)
	}
	return s.store.InTransaction(ctx, link)
}

func (s *DBService) DeleteByBot(ctx context.Context, botID string) error {
	return s.store.DeleteMessagesByBot(ctx, botID)
}

func (s *DBService) DeleteByIDs(ctx context.Context, ids []string) error {
	filtered := make([]string, 0, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			continue
		}
		if err := validateUUID("message id", id, true); err != nil {
			return err
		}
		filtered = append(filtered, id)
	}
	if len(filtered) == 0 {
		return nil
	}
	return s.store.DeleteMessages(ctx, filtered)
}

func (s *DBService) DeleteBySession(ctx context.Context, sessionID string) error {
	return s.store.DeleteMessagesBySession(ctx, sessionID)
}

func (s *DBService) publishMessageCreated(message Message) {
	if s.publisher == nil {
		return
	}
	payload, err := json.Marshal(message)
	if err != nil {
		s.logger.Warn("marshal message event failed", slog.Any("error", err))
		return
	}
	s.publisher.Publish(event.Event{
		Type: event.EventTypeMessageCreated, BotID: strings.TrimSpace(message.BotID), Data: payload,
	})
}

func (s *DBService) enrichAssets(ctx context.Context, messages []Message) {
	if len(messages) == 0 {
		return
	}
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		if uuid.Validate(message.ID) == nil {
			ids = append(ids, message.ID)
		}
	}
	assets, err := s.store.ListAssetLinks(ctx, ids)
	if err != nil {
		s.logger.WarnContext(ctx, "enrich assets failed, returning messages without assets", slog.Any("error", err))
		ensureAssetsSlice(messages)
		return
	}
	for i := range messages {
		if links, ok := assets[messages[i].ID]; ok {
			messages[i].Assets = links
		} else {
			messages[i].Assets = []MessageAsset{}
		}
	}
}

func ensureAssetsSlice(messages []Message) {
	for i := range messages {
		if messages[i].Assets == nil {
			messages[i].Assets = []MessageAsset{}
		}
	}
}

func validateUUID(name, value string, required bool) error {
	value = strings.TrimSpace(value)
	if value == "" && !required {
		return nil
	}
	if _, err := uuid.Parse(value); err != nil {
		return fmt.Errorf("invalid %s: %w", name, err)
	}
	return nil
}

func nonNilMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}

func coalesce(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
