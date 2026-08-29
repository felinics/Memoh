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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/chat/event"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/media"
	"github.com/felinics/memoh/internal/runtimefence"
)

// DBService persists and reads bot history messages.
type DBService struct {
	queries   dbstore.Queries
	logger    *slog.Logger
	publisher event.Publisher
}

type historyTurnWriter interface {
	AppendMessageToHistoryTurnByRequest(ctx context.Context, arg sqlc.AppendMessageToHistoryTurnByRequestParams) (pgtype.UUID, error)
	CreateHistoryTurn(ctx context.Context, arg sqlc.CreateHistoryTurnParams) (dbstore.HistoryTurn, error)
	BindHistoryTurnAssistantByRequest(ctx context.Context, arg sqlc.BindHistoryTurnAssistantByRequestParams) (dbstore.HistoryTurn, error)
	BindLatestHistoryTurnAssistant(ctx context.Context, arg sqlc.BindLatestHistoryTurnAssistantParams) (dbstore.HistoryTurn, error)
	GetLatestVisibleHistoryTurnBySession(ctx context.Context, sessionID pgtype.UUID) (dbstore.HistoryTurn, error)
	LinkMessageToHistoryTurn(ctx context.Context, arg sqlc.LinkMessageToHistoryTurnParams) (pgtype.UUID, error)
	AppendMessageToLatestHistoryTurn(ctx context.Context, arg sqlc.AppendMessageToLatestHistoryTurnParams) (pgtype.UUID, error)
}

type historyTurnAppendLocker interface {
	LockHistoryTurnAppendByRequest(ctx context.Context, arg sqlc.LockHistoryTurnAppendByRequestParams) error
}

type directHistoryTurnWriter interface {
	CreateMessageInHistoryTurnByRequestAndBind(ctx context.Context, arg sqlc.CreateMessageInHistoryTurnByRequestAndBindParams) (sqlc.CreateMessageInHistoryTurnByRequestAndBindRow, error)
	CreateMessageWithHistoryTurn(ctx context.Context, arg sqlc.CreateMessageWithHistoryTurnParams) (sqlc.CreateMessageWithHistoryTurnRow, error)
}

type atomicDirectHistoryTurnWriter interface {
	directHistoryTurnWriter
	SupportsAtomicDirectHistoryTurnWrites() bool
}

type toolTailRoundWriter interface {
	CreateToolTailRound(ctx context.Context, arg sqlc.CreateToolTailRoundParams) ([]sqlc.CreateToolTailRoundRow, error)
	SupportsAtomicDirectHistoryTurnWrites() bool
}

type messageCleanupQueries interface {
	DeleteMessagesByIDs(ctx context.Context, ids []pgtype.UUID) error
}

type transactionalQueries interface {
	InTx(ctx context.Context, fn func(dbstore.Queries) error) error
}

type runtimeFencedSessionMetadataWriter interface {
	UpdateSessionMetadataWithRuntimeFence(ctx context.Context, arg sqlc.UpdateSessionMetadataWithRuntimeFenceParams) (sqlc.BotSession, error)
}

// NewService creates a message service.
func NewService(log *slog.Logger, queries dbstore.Queries, publishers ...event.Publisher) *DBService {
	if log == nil {
		log = slog.Default()
	}
	var publisher event.Publisher
	if len(publishers) > 0 {
		publisher = publishers[0]
	}
	return &DBService{
		queries:   queries,
		logger:    log.With(slog.String("service", "chat/message")),
		publisher: publisher,
	}
}

// Persist writes a single message to bot_history_messages.
func (s *DBService) Persist(ctx context.Context, input PersistInput) (Message, error) {
	const maxTurnSequenceRetries = 3
	var lastErr error
	for attempt := 0; attempt < maxTurnSequenceRetries; attempt++ {
		result, err := s.persistOnce(ctx, input)
		if err == nil {
			if !input.SkipHistoryTurn {
				s.publishMessageCreated(result)
			}
			return result, nil
		}
		if !isTurnSequenceUniqueViolation(err) {
			return Message{}, err
		}
		lastErr = err
	}
	return Message{}, lastErr
}

func (s *DBService) persistOnce(ctx context.Context, input PersistInput) (Message, error) {
	if _, fenced := runtimefence.FromContext(ctx); fenced {
		var result Message
		if err := runtimefence.InTransaction(ctx, s.queries, input.BotID, input.SessionID, func(queries dbstore.Queries) error {
			txService := *s
			txService.queries = queries
			var err error
			result, err = txService.persist(ctx, input)
			return err
		}); err != nil {
			return Message{}, err
		}
		return result, nil
	}
	if result, handled, err := s.persistDirectWithoutTx(ctx, input); handled || err != nil {
		if err != nil {
			return Message{}, err
		}
		return result, nil
	}

	if txer, ok := s.queries.(transactionalQueries); ok && shouldPersistMessageInTx(input) {
		var result Message
		if err := txer.InTx(ctx, func(queries dbstore.Queries) error {
			txService := *s
			txService.queries = queries
			var err error
			result, err = txService.persist(ctx, input)
			return err
		}); err != nil {
			return Message{}, err
		}
		return result, nil
	}

	result, err := s.persist(ctx, input)
	if err != nil {
		return Message{}, err
	}
	return result, nil
}

func isTurnSequenceUniqueViolation(err error) bool {
	if !dbpkg.IsUniqueViolation(err) {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName == "idx_bot_history_messages_turn_seq_unique" {
		return true
	}
	text := err.Error()
	return strings.Contains(text, "idx_bot_history_messages_turn_seq_unique") ||
		strings.Contains(text, "bot_history_messages.turn_id, bot_history_messages.turn_message_seq")
}

// PersistToolTailRound writes the common user -> assistant(tool-call) -> tool
// -> assistant(final) round with one PostgreSQL statement. Unsupported stores
// or non-matching inputs return handled=false so callers can use Persist.
func (s *DBService) PersistToolTailRound(ctx context.Context, inputs []PersistInput) ([]Message, bool, error) {
	if s == nil || s.queries == nil || !isToolTailRoundShape(inputs) {
		return nil, false, nil
	}
	writer, ok := s.queries.(toolTailRoundWriter)
	if !ok || !writer.SupportsAtomicDirectHistoryTurnWrites() {
		return nil, false, nil
	}

	prepared := make([]preparedPersistMessage, len(inputs))
	for i, input := range inputs {
		input.TurnRequestMessageID = ""
		item, err := s.preparePersistMessage(ctx, input)
		if err != nil {
			return nil, true, err
		}
		if !item.sessionID.Valid {
			return nil, false, nil
		}
		if i > 0 && (item.botID != prepared[0].botID || item.sessionID != prepared[0].sessionID) {
			return nil, false, nil
		}
		prepared[i] = item
	}

	messageIDs := [4]pgtype.UUID{newPGUUID(), newPGUUID(), newPGUUID(), newPGUUID()}
	// The round's turn is the request message's turn, so it comes from the same
	// admission the user row was prepared with. Minting one here would file the
	// whole round under a turn the client cannot address (SR-TURN-001).
	roundTurn := prepared[0].turn()
	turnID := roundTurn.id
	if !turnID.Valid {
		turnID = newPGUUID()
	}
	rows, err := writer.CreateToolTailRound(ctx, sqlc.CreateToolTailRoundParams{
		UserMessageID:                            messageIDs[0],
		UserSenderChannelIdentityID:              prepared[0].createArg.SenderChannelIdentityID,
		UserSenderUserID:                         prepared[0].createArg.SenderUserID,
		UserExternalMessageID:                    prepared[0].createArg.ExternalMessageID,
		UserSourceReplyToMessageID:               prepared[0].createArg.SourceReplyToMessageID,
		UserContent:                              prepared[0].createArg.Content,
		UserMetadata:                             prepared[0].createArg.Metadata,
		UserUsage:                                prepared[0].createArg.Usage,
		UserSessionMode:                          prepared[0].createArg.SessionMode,
		UserRuntimeType:                          prepared[0].createArg.RuntimeType,
		UserModelID:                              prepared[0].createArg.ModelID,
		UserEventID:                              prepared[0].createArg.EventID,
		UserDisplayText:                          prepared[0].createArg.DisplayText,
		ToolCallAssistantMessageID:               messageIDs[1],
		ToolCallAssistantSenderChannelIdentityID: prepared[1].createArg.SenderChannelIdentityID,
		ToolCallAssistantSenderUserID:            prepared[1].createArg.SenderUserID,
		ToolCallAssistantExternalMessageID:       prepared[1].createArg.ExternalMessageID,
		ToolCallAssistantSourceReplyToMessageID:  prepared[1].createArg.SourceReplyToMessageID,
		ToolCallAssistantContent:                 prepared[1].createArg.Content,
		ToolCallAssistantMetadata:                prepared[1].createArg.Metadata,
		ToolCallAssistantUsage:                   prepared[1].createArg.Usage,
		ToolCallAssistantSessionMode:             prepared[1].createArg.SessionMode,
		ToolCallAssistantRuntimeType:             prepared[1].createArg.RuntimeType,
		ToolCallAssistantModelID:                 prepared[1].createArg.ModelID,
		ToolCallAssistantEventID:                 prepared[1].createArg.EventID,
		ToolCallAssistantDisplayText:             prepared[1].createArg.DisplayText,
		ToolMessageID:                            messageIDs[2],
		ToolSenderChannelIdentityID:              prepared[2].createArg.SenderChannelIdentityID,
		ToolSenderUserID:                         prepared[2].createArg.SenderUserID,
		ToolExternalMessageID:                    prepared[2].createArg.ExternalMessageID,
		ToolSourceReplyToMessageID:               prepared[2].createArg.SourceReplyToMessageID,
		ToolContent:                              prepared[2].createArg.Content,
		ToolMetadata:                             prepared[2].createArg.Metadata,
		ToolUsage:                                prepared[2].createArg.Usage,
		ToolSessionMode:                          prepared[2].createArg.SessionMode,
		ToolRuntimeType:                          prepared[2].createArg.RuntimeType,
		ToolModelID:                              prepared[2].createArg.ModelID,
		ToolEventID:                              prepared[2].createArg.EventID,
		ToolDisplayText:                          prepared[2].createArg.DisplayText,
		FinalAssistantMessageID:                  messageIDs[3],
		FinalAssistantSenderChannelIdentityID:    prepared[3].createArg.SenderChannelIdentityID,
		FinalAssistantSenderUserID:               prepared[3].createArg.SenderUserID,
		FinalAssistantExternalMessageID:          prepared[3].createArg.ExternalMessageID,
		FinalAssistantSourceReplyToMessageID:     prepared[3].createArg.SourceReplyToMessageID,
		FinalAssistantContent:                    prepared[3].createArg.Content,
		FinalAssistantMetadata:                   prepared[3].createArg.Metadata,
		FinalAssistantUsage:                      prepared[3].createArg.Usage,
		FinalAssistantSessionMode:                prepared[3].createArg.SessionMode,
		FinalAssistantRuntimeType:                prepared[3].createArg.RuntimeType,
		FinalAssistantModelID:                    prepared[3].createArg.ModelID,
		FinalAssistantEventID:                    prepared[3].createArg.EventID,
		FinalAssistantDisplayText:                prepared[3].createArg.DisplayText,
		BotID:                                    prepared[0].botID,
		SessionID:                                prepared[0].sessionID,
		TurnID:                                   turnID,
		TurnPosition:                             roundTurn.position,
		RunID:                                    prepared[0].createArg.RunID,
	})
	if err != nil {
		return nil, true, err
	}
	if len(rows) != len(prepared) {
		return nil, true, fmt.Errorf("create tool tail round returned %d messages, want %d", len(rows), len(prepared))
	}

	messages := make([]Message, len(rows))
	for i, row := range rows {
		messages[i] = toMessageFromToolTailRound(row, prepared[i].createArg, prepared[i].metadata)
		s.publishMessageCreated(messages[i])
	}
	return messages, true, nil
}

// PersistRound writes all messages and history links under one PostgreSQL
// transaction. Distributed callers additionally validate their runtime fence
// in that transaction; local replacements use the same atomic write without a
// distributed ownership token.
func (s *DBService) PersistRound(ctx context.Context, inputs []PersistInput, options RoundPersistenceOptions) ([]Message, bool, error) {
	if s == nil || s.queries == nil || len(inputs) == 0 {
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
	for attempt := 0; attempt < maxTurnSequenceRetries; attempt++ {
		persisted := make([]Message, 0, len(inputs))
		persist := func(queries dbstore.Queries) error {
			txService := *s
			txService.queries = queries
			txService.publisher = nil
			if options.CleanupACPDecisionProjections {
				pgBotID, parseErr := dbpkg.ParseUUID(botID)
				if parseErr != nil {
					return parseErr
				}
				pgSessionID, parseErr := dbpkg.ParseUUID(sessionID)
				if parseErr != nil {
					return parseErr
				}
				pgRunID, parseErr := dbpkg.ParseUUID(strings.TrimSpace(inputs[0].RunID))
				if parseErr != nil {
					return fmt.Errorf("ACP projection cleanup requires run_id: %w", parseErr)
				}
				if _, deleteErr := queries.DeleteACPDecisionProjectionsByRun(ctx, sqlc.DeleteACPDecisionProjectionsByRunParams{
					BotID: pgBotID, SessionID: pgSessionID, RunID: pgRunID,
				}); deleteErr != nil {
					return deleteErr
				}
			}
			turnRequestMessageID := strings.TrimSpace(inputs[0].TurnRequestMessageID)
			for _, original := range inputs {
				input := original
				if !input.SkipHistoryTurn {
					input.TurnRequestMessageID = turnRequestMessageID
				}
				message, err := txService.persist(ctx, input)
				if err != nil {
					return err
				}
				if strings.EqualFold(strings.TrimSpace(input.Role), "user") && !input.SkipHistoryTurn {
					turnRequestMessageID = message.ID
				}
				persisted = append(persisted, message)
			}
			if options.Replacement != nil {
				if err := txService.replacePersistedRound(ctx, sessionID, persisted, *options.Replacement); err != nil {
					return err
				}
			}
			if options.ACPPublication != nil {
				pgBotID, parseErr := dbpkg.ParseUUID(botID)
				if parseErr != nil {
					return parseErr
				}
				pgSessionID, parseErr := dbpkg.ParseUUID(sessionID)
				if parseErr != nil {
					return parseErr
				}
				pgRunID, parseErr := dbpkg.ParseUUID(strings.TrimSpace(options.ACPPublication.RunID))
				if parseErr != nil {
					return fmt.Errorf("ACP publication requires run_id: %w", parseErr)
				}
				moved, upsertErr := queries.UpsertACPSessionPublication(ctx, sqlc.UpsertACPSessionPublicationParams{
					SessionID:       pgSessionID,
					BotID:           pgBotID,
					RunID:           pgRunID,
					CheckpointReset: options.ACPPublication.CheckpointReset,
				})
				if upsertErr != nil {
					return fmt.Errorf("publish ACP session head: %w", upsertErr)
				}
				if moved == 0 {
					// The guarded insert matched no run: the session's fencing
					// generation moved underneath this round. Publishing nothing
					// would silently promote a stale native context on restart.
					return runtimefence.ErrStale
				}
			}
			return nil
		}
		var err error
		if fenced {
			err = runtimefence.InTransaction(ctx, s.queries, botID, sessionID, persist)
		} else {
			txer, ok := s.queries.(transactionalQueries)
			if !ok {
				return nil, true, runtimefence.ErrTransactionsUnsupported
			}
			err = txer.InTx(ctx, persist)
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
		if !isTurnSequenceUniqueViolation(err) {
			return nil, true, err
		}
	}
	return nil, true, lastErr
}

func isToolTailRoundShape(inputs []PersistInput) bool {
	if len(inputs) != 4 {
		return false
	}
	if strings.TrimSpace(inputs[0].BotID) == "" || strings.TrimSpace(inputs[0].SessionID) == "" {
		return false
	}
	expectedRoles := [4]string{"user", "assistant", "tool", "assistant"}
	for i, input := range inputs {
		if input.SkipHistoryTurn || len(input.Assets) > 0 {
			return false
		}
		if !strings.EqualFold(strings.TrimSpace(input.Role), expectedRoles[i]) {
			return false
		}
		if strings.TrimSpace(input.BotID) != strings.TrimSpace(inputs[0].BotID) ||
			strings.TrimSpace(input.SessionID) != strings.TrimSpace(inputs[0].SessionID) {
			return false
		}
	}
	return true
}

func shouldPersistMessageInTx(input PersistInput) bool {
	return !input.SkipHistoryTurn || len(input.Assets) > 0
}

type preparedPersistMessage struct {
	createArg            sqlc.CreateMessageParams
	metadata             map[string]any
	botID                pgtype.UUID
	sessionID            pgtype.UUID
	turnRequestMessageID pgtype.UUID
	// turnID and turnPosition are set only when admission already decided this
	// message's turn. Both invalid means the history layer allocates one.
	turnID       pgtype.UUID
	turnPosition pgtype.Int8
}

func (p preparedPersistMessage) turn() turnIdentity {
	return turnIdentity{id: p.turnID, position: p.turnPosition}
}

func (s *DBService) preparePersistMessage(ctx context.Context, input PersistInput) (preparedPersistMessage, error) {
	pgBotID, err := dbpkg.ParseUUID(input.BotID)
	if err != nil {
		return preparedPersistMessage{}, fmt.Errorf("invalid bot id: %w", err)
	}

	pgSessionID, err := parseOptionalUUID(input.SessionID)
	if err != nil {
		return preparedPersistMessage{}, fmt.Errorf("invalid session id: %w", err)
	}
	pgSenderChannelIdentityID, err := parseOptionalUUID(input.SenderChannelIdentityID)
	if err != nil {
		return preparedPersistMessage{}, fmt.Errorf("invalid sender channel identity id: %w", err)
	}
	pgSenderUserID, err := parseOptionalUUID(input.SenderUserID)
	if err != nil {
		return preparedPersistMessage{}, fmt.Errorf("invalid sender user id: %w", err)
	}
	pgModelID, err := parseOptionalUUID(input.ModelID)
	if err != nil {
		return preparedPersistMessage{}, fmt.Errorf("invalid model id: %w", err)
	}
	pgEventID, err := parseOptionalUUID(input.EventID)
	if err != nil {
		return preparedPersistMessage{}, fmt.Errorf("invalid event id: %w", err)
	}
	pgRunID, err := parseOptionalUUID(input.RunID)
	if err != nil {
		return preparedPersistMessage{}, fmt.Errorf("invalid run id: %w", err)
	}
	pgTurnID, err := parseOptionalUUID(input.TurnID)
	if err != nil {
		return preparedPersistMessage{}, fmt.Errorf("invalid turn id: %w", err)
	}
	// A caller-decided turn is only usable whole: admission allocates the id and
	// the position together, and honouring one without the other would either
	// misorder the turn or take a slot under a name the client never saw.
	if pgTurnID.Valid != (input.TurnPosition != nil) {
		return preparedPersistMessage{}, errors.New("turn id and turn position must be supplied together")
	}

	metadata := nonNilMap(input.Metadata)
	metaBytes, err := json.Marshal(metadata)
	if err != nil {
		return preparedPersistMessage{}, fmt.Errorf("marshal message metadata: %w", err)
	}

	content := input.Content
	if len(content) == 0 {
		content = []byte("{}")
	}

	sessionMode, runtimeType := resolveRuntimeSnapshotWithQueries(ctx, s.queries, pgSessionID, input.SessionMode, input.RuntimeType)
	prepared := preparedPersistMessage{
		createArg: sqlc.CreateMessageParams{
			BotID:                   pgBotID,
			SessionID:               pgSessionID,
			SenderChannelIdentityID: pgSenderChannelIdentityID,
			SenderUserID:            pgSenderUserID,
			ExternalMessageID:       toPgText(input.ExternalMessageID),
			SourceReplyToMessageID:  toPgText(input.SourceReplyToMessageID),
			Role:                    input.Role,
			Content:                 content,
			Metadata:                metaBytes,
			Usage:                   input.Usage,
			SessionMode:             sessionMode,
			RuntimeType:             runtimeType,
			ModelID:                 pgModelID,
			EventID:                 pgEventID,
			DisplayText:             toPgText(input.DisplayText),
			RunID:                   pgRunID,
		},
		metadata:     metadata,
		botID:        pgBotID,
		sessionID:    pgSessionID,
		turnID:       pgTurnID,
		turnPosition: toPgInt8(input.TurnPosition),
	}

	if !input.SkipHistoryTurn {
		pgTurnRequestMessageID, err := parseOptionalUUID(input.TurnRequestMessageID)
		if err != nil {
			return preparedPersistMessage{}, fmt.Errorf("invalid turn request message id: %w", err)
		}
		prepared.turnRequestMessageID = pgTurnRequestMessageID
	}

	return prepared, nil
}

func (s *DBService) persistDirectWithoutTx(ctx context.Context, input PersistInput) (Message, bool, error) {
	if input.SkipHistoryTurn || len(input.Assets) > 0 {
		return Message{}, false, nil
	}
	direct, ok := s.queries.(atomicDirectHistoryTurnWriter)
	if !ok || strings.TrimSpace(input.SessionID) == "" {
		return Message{}, false, nil
	}
	if !direct.SupportsAtomicDirectHistoryTurnWrites() {
		return Message{}, false, nil
	}

	prepared, err := s.preparePersistMessage(ctx, input)
	if err != nil {
		return Message{}, true, err
	}
	if !prepared.sessionID.Valid {
		return Message{}, false, nil
	}
	result, pgMsgID, handled, err := persistDirectHistoryMessage(ctx, direct, prepared.createArg, prepared.metadata, input.Role, prepared.turnRequestMessageID, prepared.turn())
	if err != nil {
		return Message{}, true, err
	}
	if !handled {
		return Message{}, false, nil
	}
	result, err = s.finishPersistedMessage(ctx, result, pgMsgID, nil)
	if err != nil {
		return Message{}, true, err
	}
	return result, true, nil
}

func (s *DBService) persist(ctx context.Context, input PersistInput) (Message, error) {
	prepared, err := s.preparePersistMessage(ctx, input)
	if err != nil {
		return Message{}, err
	}
	createArg := prepared.createArg

	var pgTurnRequestMessageID pgtype.UUID
	if !input.SkipHistoryTurn {
		pgTurnRequestMessageID = prepared.turnRequestMessageID
		if direct, ok := s.queries.(directHistoryTurnWriter); ok && prepared.sessionID.Valid {
			result, pgMsgID, handled, err := persistDirectHistoryMessage(ctx, direct, createArg, prepared.metadata, input.Role, pgTurnRequestMessageID, prepared.turn())
			if err != nil {
				return Message{}, err
			}
			if handled {
				return s.finishPersistedMessage(ctx, result, pgMsgID, input.Assets)
			}
		}
	}

	row, err := s.queries.CreateMessage(ctx, createArg)
	if err != nil {
		return Message{}, err
	}

	result := toMessageFromCreate(row)
	if !input.SkipHistoryTurn {
		if err := s.persistHistoryTurn(ctx, prepared.botID, prepared.sessionID, row.ID, input.Role, pgTurnRequestMessageID); err != nil {
			s.cleanupPersistedMessage(ctx, row.ID)
			return Message{}, err
		}
	}

	return s.finishPersistedMessage(ctx, result, row.ID, input.Assets)
}

// turnIdentity is a turn decided before persistence, by admission. Both fields
// are set or neither is; the pair is validated at preparePersistMessage.
type turnIdentity struct {
	id       pgtype.UUID
	position pgtype.Int8
}

func persistDirectHistoryMessage(
	ctx context.Context,
	writer directHistoryTurnWriter,
	createArg sqlc.CreateMessageParams,
	metadata map[string]any,
	role string,
	requestMessageID pgtype.UUID,
	turn turnIdentity,
) (Message, pgtype.UUID, bool, error) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user":
		messageID := newPGUUID()
		// A run admitted through session_runs already drew this turn's id and
		// position from bot_sessions.next_turn_position, and the client has been
		// told that id. Minting a second one here would file the message under a
		// turn nobody can address and spend a second position (SR-TURN-001).
		turnID := turn.id
		if !turnID.Valid {
			turnID = newPGUUID()
		}
		row, err := writer.CreateMessageWithHistoryTurn(ctx, sqlc.CreateMessageWithHistoryTurnParams{
			TurnPosition:            turn.position,
			MessageID:               messageID,
			BotID:                   createArg.BotID,
			SessionID:               createArg.SessionID,
			SenderChannelIdentityID: createArg.SenderChannelIdentityID,
			SenderUserID:            createArg.SenderUserID,
			ExternalMessageID:       createArg.ExternalMessageID,
			SourceReplyToMessageID:  createArg.SourceReplyToMessageID,
			Role:                    createArg.Role,
			Content:                 createArg.Content,
			Metadata:                createArg.Metadata,
			Usage:                   createArg.Usage,
			SessionMode:             createArg.SessionMode,
			RuntimeType:             createArg.RuntimeType,
			ModelID:                 createArg.ModelID,
			EventID:                 createArg.EventID,
			DisplayText:             createArg.DisplayText,
			RunID:                   createArg.RunID,
			TurnID:                  turnID,
			TurnMessageSeq:          pgtype.Int8{Int64: 1, Valid: true},
		})
		if err != nil {
			return Message{}, pgtype.UUID{}, true, err
		}
		return toMessageFromCreateWithHistoryTurn(row, createArg, metadata), messageID, true, nil
	case "assistant", "tool":
		if !requestMessageID.Valid {
			return Message{}, pgtype.UUID{}, false, nil
		}
		row, err := writer.CreateMessageInHistoryTurnByRequestAndBind(ctx, sqlc.CreateMessageInHistoryTurnByRequestAndBindParams{
			Role:                    createArg.Role,
			SessionID:               createArg.SessionID,
			RequestMessageID:        requestMessageID,
			BotID:                   createArg.BotID,
			SenderChannelIdentityID: createArg.SenderChannelIdentityID,
			SenderUserID:            createArg.SenderUserID,
			ExternalMessageID:       createArg.ExternalMessageID,
			SourceReplyToMessageID:  createArg.SourceReplyToMessageID,
			Content:                 createArg.Content,
			Metadata:                createArg.Metadata,
			Usage:                   createArg.Usage,
			SessionMode:             createArg.SessionMode,
			RuntimeType:             createArg.RuntimeType,
			ModelID:                 createArg.ModelID,
			EventID:                 createArg.EventID,
			DisplayText:             createArg.DisplayText,
			RunID:                   createArg.RunID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return Message{}, pgtype.UUID{}, false, nil
		}
		if err != nil {
			return Message{}, pgtype.UUID{}, true, err
		}
		return toMessageFromCreateInHistoryTurnByRequestAndBind(row, createArg, metadata), row.ID, true, nil
	default:
		return Message{}, pgtype.UUID{}, false, nil
	}
}

func (s *DBService) finishPersistedMessage(ctx context.Context, result Message, pgMsgID pgtype.UUID, assets []AssetRef) (Message, error) {
	for _, ref := range assets {
		role := ref.Role
		if strings.TrimSpace(role) == "" {
			role = "attachment"
		}
		contentHash := strings.TrimSpace(ref.ContentHash)
		if contentHash == "" {
			s.logger.Warn("skip asset ref without content_hash")
			continue
		}
		if ref.Ordinal < math.MinInt32 || ref.Ordinal > math.MaxInt32 {
			return Message{}, fmt.Errorf("asset ordinal out of range: %d", ref.Ordinal)
		}
		if _, assetErr := s.queries.CreateMessageAsset(ctx, sqlc.CreateMessageAssetParams{
			MessageID:   pgMsgID,
			Role:        role,
			Ordinal:     int32(ref.Ordinal),
			ContentHash: contentHash,
			Mime:        strings.TrimSpace(ref.Mime),
			SizeBytes:   ref.SizeBytes,
			StorageKey:  strings.TrimSpace(ref.StorageKey),
			Name:        ref.Name,
			Metadata:    marshalMetadata(ref.Metadata),
		}); assetErr != nil {
			return Message{}, fmt.Errorf("create message asset link for %s: %w", result.ID, assetErr)
		}
	}

	if len(assets) > 0 {
		messageAssets := make([]MessageAsset, 0, len(assets))
		for _, ref := range assets {
			ch := strings.TrimSpace(ref.ContentHash)
			if ch == "" {
				continue
			}
			messageAssets = append(messageAssets, MessageAsset{
				ContentHash: ch,
				Role:        coalesce(ref.Role, "attachment"),
				Ordinal:     ref.Ordinal,
				Mime:        strings.TrimSpace(ref.Mime),
				SizeBytes:   ref.SizeBytes,
				StorageKey:  strings.TrimSpace(ref.StorageKey),
				Name:        ref.Name,
				Metadata:    ref.Metadata,
			})
		}
		result.Assets = messageAssets
	}

	return result, nil
}

func newPGUUID() pgtype.UUID {
	id := uuid.New()
	return pgtype.UUID{Bytes: id, Valid: true}
}

func (s *DBService) cleanupPersistedMessage(ctx context.Context, messageID pgtype.UUID) {
	if s == nil || s.queries == nil || !messageID.Valid {
		return
	}
	cleanup, ok := s.queries.(messageCleanupQueries)
	if !ok {
		return
	}
	if err := cleanup.DeleteMessagesByIDs(ctx, []pgtype.UUID{messageID}); err != nil {
		s.logger.Error("cleanup message after history turn failure failed", slog.String("message_id", messageID.String()), slog.Any("error", err))
	}
}

func (s *DBService) persistHistoryTurn(ctx context.Context, botID pgtype.UUID, sessionID pgtype.UUID, messageID pgtype.UUID, role string, requestMessageID pgtype.UUID) error {
	if s == nil || s.queries == nil || !sessionID.Valid {
		return nil
	}
	writer, ok := s.queries.(historyTurnWriter)
	if !ok {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user":
		turn, err := writer.CreateHistoryTurn(ctx, sqlc.CreateHistoryTurnParams{
			BotID:            botID,
			SessionID:        sessionID,
			RequestMessageID: messageID,
		})
		if err != nil {
			return fmt.Errorf("create history turn: %w", err)
		}
		if _, err := writer.LinkMessageToHistoryTurn(ctx, sqlc.LinkMessageToHistoryTurnParams{
			MessageID:      messageID,
			TurnID:         turn.ID,
			TurnMessageSeq: pgtype.Int8{Int64: 1, Valid: true},
		}); err != nil {
			return fmt.Errorf("link user message to history turn: %w", err)
		}
	case "assistant":
		if requestMessageID.Valid {
			if err := lockHistoryTurnAppendByRequest(ctx, writer, sessionID, requestMessageID); err != nil {
				return fmt.Errorf("lock requested history turn append: %w", err)
			}
			if turn, err := writer.BindHistoryTurnAssistantByRequest(ctx, sqlc.BindHistoryTurnAssistantByRequestParams{
				SessionID:          sessionID,
				RequestMessageID:   requestMessageID,
				AssistantMessageID: messageID,
			}); err == nil {
				if _, err := writer.LinkMessageToHistoryTurn(ctx, sqlc.LinkMessageToHistoryTurnParams{
					MessageID:      messageID,
					TurnID:         turn.ID,
					TurnMessageSeq: pgtype.Int8{Int64: 2, Valid: true},
				}); err != nil {
					return fmt.Errorf("link assistant message to requested history turn: %w", err)
				}
				return nil
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("bind history turn assistant by request: %w", err)
			}
			if err := appendMessageToHistoryTurnByRequest(ctx, writer, sessionID, requestMessageID, messageID); err == nil {
				return nil
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("append assistant message to requested history turn: %w", err)
			}
		}
		if _, err := writer.AppendMessageToLatestHistoryTurn(ctx, sqlc.AppendMessageToLatestHistoryTurnParams{
			SessionID: sessionID,
			MessageID: messageID,
		}); err == nil {
			return nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("append assistant message to latest history turn: %w", err)
		}
		turn, err := writer.CreateHistoryTurn(ctx, sqlc.CreateHistoryTurnParams{
			BotID:              botID,
			SessionID:          sessionID,
			AssistantMessageID: messageID,
		})
		if err != nil {
			return fmt.Errorf("create orphan assistant history turn: %w", err)
		}
		if _, err := writer.LinkMessageToHistoryTurn(ctx, sqlc.LinkMessageToHistoryTurnParams{
			MessageID:      messageID,
			TurnID:         turn.ID,
			TurnMessageSeq: pgtype.Int8{Int64: 2, Valid: true},
		}); err != nil {
			return fmt.Errorf("link orphan assistant message to history turn: %w", err)
		}
	case "tool":
		if requestMessageID.Valid {
			if err := lockHistoryTurnAppendByRequest(ctx, writer, sessionID, requestMessageID); err != nil {
				return fmt.Errorf("lock requested history turn append: %w", err)
			}
			if err := appendMessageToHistoryTurnByRequest(ctx, writer, sessionID, requestMessageID, messageID); err == nil {
				return nil
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("append tool message to requested history turn: %w", err)
			}
		}
		if _, err := writer.AppendMessageToLatestHistoryTurn(ctx, sqlc.AppendMessageToLatestHistoryTurnParams{
			SessionID: sessionID,
			MessageID: messageID,
		}); err != nil {
			return fmt.Errorf("append tool message to latest history turn: %w", err)
		}
	}
	return nil
}

func lockHistoryTurnAppendByRequest(ctx context.Context, writer historyTurnWriter, sessionID pgtype.UUID, requestMessageID pgtype.UUID) error {
	if !sessionID.Valid || !requestMessageID.Valid {
		return nil
	}
	locker, ok := writer.(historyTurnAppendLocker)
	if !ok {
		return nil
	}
	return locker.LockHistoryTurnAppendByRequest(ctx, sqlc.LockHistoryTurnAppendByRequestParams{
		SessionID:        sessionID,
		RequestMessageID: requestMessageID,
	})
}

func appendMessageToHistoryTurnByRequest(ctx context.Context, writer historyTurnWriter, sessionID pgtype.UUID, requestMessageID pgtype.UUID, messageID pgtype.UUID) error {
	_, err := writer.AppendMessageToHistoryTurnByRequest(ctx, sqlc.AppendMessageToHistoryTurnByRequestParams{
		SessionID:        sessionID,
		RequestMessageID: requestMessageID,
		MessageID:        messageID,
	})
	return err
}

func resolveRuntimeSnapshotWithQueries(ctx context.Context, queries dbstore.Queries, sessionID pgtype.UUID, sessionMode, runtimeType string) (string, string) {
	sessionMode = normalizeSessionMode(sessionMode)
	runtimeType = normalizeRuntimeType(runtimeType)
	if sessionMode != "" && runtimeType != "" && sessionMode != "subagent" {
		return sessionMode, runtimeType
	}
	if sessionID.Valid && queries != nil {
		if row, err := queries.GetSessionByID(ctx, sessionID); err == nil {
			rowMode, rowRuntime := sessionSnapshotFromRow(row)
			if rowMode == "subagent" && row.ParentSessionID.Valid {
				if parent, parentErr := queries.GetSessionByID(ctx, row.ParentSessionID); parentErr == nil {
					parentMode, parentRuntime := sessionSnapshotFromRow(parent)
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

func sessionSnapshotFromRow(row sqlc.BotSession) (string, string) {
	sessionMode := normalizeSessionMode(row.SessionMode)
	if sessionMode == "" {
		sessionMode = legacySessionMode(row.Type)
	}
	runtimeType := normalizeRuntimeType(row.RuntimeType)
	if runtimeType == "" {
		runtimeType = legacyRuntimeType(row.Type)
	}
	return sessionMode, runtimeType
}

func normalizeSessionMode(mode string) string {
	switch strings.TrimSpace(mode) {
	case "chat", "discuss", "schedule", "subagent":
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
	case "discuss", "schedule", "subagent":
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

// List returns all messages for a bot.
func (s *DBService) List(ctx context.Context, botID string) ([]Message, error) {
	pgBotID, err := dbpkg.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListMessages(ctx, pgBotID)
	if err != nil {
		return nil, err
	}
	msgs := toMessagesFromList(rows)
	s.enrichAssets(ctx, msgs)
	return msgs, nil
}

// ListSince returns bot messages since a given time.
func (s *DBService) ListSince(ctx context.Context, botID string, since time.Time) ([]Message, error) {
	pgBotID, err := dbpkg.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListMessagesSince(ctx, sqlc.ListMessagesSinceParams{
		BotID:     pgBotID,
		CreatedAt: pgtype.Timestamptz{Time: since, Valid: true},
	})
	if err != nil {
		return nil, err
	}
	msgs := toMessagesFromSince(rows)
	s.enrichAssets(ctx, msgs)
	return msgs, nil
}

// ListActiveSince returns bot messages since a given time, excluding passive_sync messages.
func (s *DBService) ListActiveSince(ctx context.Context, botID string, since time.Time) ([]Message, error) {
	pgBotID, err := dbpkg.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListActiveMessagesSince(ctx, sqlc.ListActiveMessagesSinceParams{
		BotID:     pgBotID,
		CreatedAt: pgtype.Timestamptz{Time: since, Valid: true},
	})
	if err != nil {
		return nil, err
	}
	msgs := toMessagesFromActiveSince(rows)
	s.enrichAssets(ctx, msgs)
	return msgs, nil
}

// ListLatest returns the latest N bot messages (newest first in DB; caller may reverse for ASC).
func (s *DBService) ListLatest(ctx context.Context, botID string, limit int32) ([]Message, error) {
	pgBotID, err := dbpkg.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListMessagesLatest(ctx, sqlc.ListMessagesLatestParams{
		BotID:    pgBotID,
		MaxCount: limit,
	})
	if err != nil {
		return nil, err
	}
	msgs := toMessagesFromLatest(rows)
	s.enrichAssets(ctx, msgs)
	return msgs, nil
}

// ListBefore returns up to limit messages older than before (created_at < before), ordered oldest-first.
func (s *DBService) ListBefore(ctx context.Context, botID string, before time.Time, limit int32) ([]Message, error) {
	pgBotID, err := dbpkg.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListMessagesBefore(ctx, sqlc.ListMessagesBeforeParams{
		BotID:     pgBotID,
		CreatedAt: pgtype.Timestamptz{Time: before, Valid: true},
		MaxCount:  limit,
	})
	if err != nil {
		return nil, err
	}
	msgs := toMessagesFromBefore(rows)
	s.enrichAssets(ctx, msgs)
	return msgs, nil
}

// --- Session-scoped queries ---

// ListBySession returns all messages for a session.
func (s *DBService) ListBySession(ctx context.Context, sessionID string) ([]Message, error) {
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListMessagesBySession(ctx, pgSessionID)
	if err != nil {
		return nil, err
	}
	msgs := toMessagesFromSessionList(rows)
	s.enrichAssets(ctx, msgs)
	return msgs, nil
}

// ListSinceBySession returns session messages since a given time.
func (s *DBService) ListSinceBySession(ctx context.Context, sessionID string, since time.Time) ([]Message, error) {
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListMessagesSinceBySession(ctx, sqlc.ListMessagesSinceBySessionParams{
		SessionID: pgSessionID,
		CreatedAt: pgtype.Timestamptz{Time: since, Valid: true},
	})
	if err != nil {
		return nil, err
	}
	msgs := toMessagesFromSinceBySession(rows)
	s.enrichAssets(ctx, msgs)
	return msgs, nil
}

// ListActiveSinceWithinBytes returns bot messages since a given time,
// admitted newest-first within the content byte budget (CM-ADM-001).
func (s *DBService) ListActiveSinceWithinBytes(ctx context.Context, botID string, since time.Time, maxBytes int64) ([]Message, error) {
	pgBotID, err := dbpkg.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListActiveMessagesSinceWithinBytes(ctx, sqlc.ListActiveMessagesSinceWithinBytesParams{
		BotID:     pgBotID,
		CreatedAt: pgtype.Timestamptz{Time: since, Valid: true},
		MaxBytes:  maxBytes,
	})
	if err != nil {
		return nil, err
	}
	msgs := toMessagesFromActiveSinceWithinBytes(rows)
	s.enrichAssets(ctx, msgs)
	return msgs, nil
}

// ListActiveSinceBySessionWithinBytes returns session messages since a given
// time, admitted newest-first within the content byte budget (CM-ADM-001).
func (s *DBService) ListActiveSinceBySessionWithinBytes(ctx context.Context, sessionID string, since time.Time, maxBytes int64) ([]Message, error) {
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListActiveMessagesSinceBySessionWithinBytes(ctx, sqlc.ListActiveMessagesSinceBySessionWithinBytesParams{
		SessionID: pgSessionID,
		CreatedAt: pgtype.Timestamptz{Time: since, Valid: true},
		MaxBytes:  maxBytes,
	})
	if err != nil {
		return nil, err
	}
	msgs := toMessagesFromActiveSinceBySessionWithinBytes(rows)
	s.enrichAssets(ctx, msgs)
	return msgs, nil
}

// MeasureActiveBySession aggregates a session's active message count and
// content bytes on the database side (CM-ADM-001).
func (s *DBService) MeasureActiveBySession(ctx context.Context, sessionID string, since time.Time) (ActiveMessagesMeasure, error) {
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return ActiveMessagesMeasure{}, err
	}
	row, err := s.queries.MeasureActiveMessagesBySession(ctx, sqlc.MeasureActiveMessagesBySessionParams{
		SessionID: pgSessionID,
		CreatedAt: pgtype.Timestamptz{Time: since, Valid: true},
	})
	if err != nil {
		return ActiveMessagesMeasure{}, err
	}
	return ActiveMessagesMeasure{MessageCount: row.MessageCount, ContentBytes: row.ContentBytes}, nil
}

// ListActiveSinceBySession returns session messages since a given time, excluding passive_sync messages.
func (s *DBService) ListActiveSinceBySession(ctx context.Context, sessionID string, since time.Time) ([]Message, error) {
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListActiveMessagesSinceBySession(ctx, sqlc.ListActiveMessagesSinceBySessionParams{
		SessionID: pgSessionID,
		CreatedAt: pgtype.Timestamptz{Time: since, Valid: true},
	})
	if err != nil {
		return nil, err
	}
	msgs := toMessagesFromActiveSinceBySession(rows)
	s.enrichAssets(ctx, msgs)
	return msgs, nil
}

// ListLatestBySession returns the latest N session messages.
func (s *DBService) ListLatestBySession(ctx context.Context, sessionID string, limit int32) ([]Message, error) {
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListMessagesLatestBySession(ctx, sqlc.ListMessagesLatestBySessionParams{
		SessionID: pgSessionID,
		MaxCount:  limit,
	})
	if err != nil {
		return nil, err
	}
	msgs := toMessagesFromLatestBySession(rows)
	s.enrichAssets(ctx, msgs)
	return msgs, nil
}

// ListLatestUIBySession returns the latest N session messages using the lighter
// field set needed by format=ui rendering.
func (s *DBService) ListLatestUIBySession(ctx context.Context, sessionID string, limit int32) ([]Message, error) {
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListMessagesLatestUIBySession(ctx, sqlc.ListMessagesLatestUIBySessionParams{
		SessionID: pgSessionID,
		MaxCount:  limit,
	})
	if err != nil {
		return nil, err
	}
	msgs := toMessagesFromLatestUIBySession(rows)
	s.enrichAssets(ctx, msgs)
	return msgs, nil
}

// ListBeforeBySession returns up to limit session messages older than before.
func (s *DBService) ListBeforeBySession(ctx context.Context, sessionID string, before time.Time, limit int32) ([]Message, error) {
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListMessagesBeforeBySession(ctx, sqlc.ListMessagesBeforeBySessionParams{
		SessionID: pgSessionID,
		CreatedAt: pgtype.Timestamptz{Time: before, Valid: true},
		MaxCount:  limit,
	})
	if err != nil {
		return nil, err
	}
	msgs := toMessagesFromBeforeBySession(rows)
	s.enrichAssets(ctx, msgs)
	return msgs, nil
}

// ListBeforeMessageBySession returns up to limit session messages before the
// cursor message in visible turn order.
func (s *DBService) ListBeforeMessageBySession(ctx context.Context, sessionID string, beforeMessageID string, limit int32) ([]Message, error) {
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return nil, err
	}
	pgMessageID, err := dbpkg.ParseUUID(beforeMessageID)
	if err != nil {
		return nil, err
	}
	cursor, err := s.queries.GetVisibleMessageCursorByIDBySession(ctx, sqlc.GetVisibleMessageCursorByIDBySessionParams{
		SessionID: pgSessionID,
		MessageID: pgMessageID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return []Message{}, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListMessagesBeforeCursorBySession(ctx, sqlc.ListMessagesBeforeCursorBySessionParams{
		SessionID:            pgSessionID,
		CursorTurnPosition:   cursor.TurnPosition.Int64,
		CursorTurnMessageSeq: cursor.TurnMessageSeq.Int64,
		CursorCreatedAt:      cursor.CreatedAt,
		CursorMessageID:      cursor.ID,
		MaxCount:             limit,
	})
	if err != nil {
		return nil, err
	}
	msgs := toMessagesFromBeforeCursorBySession(rows)
	s.enrichAssets(ctx, msgs)
	return msgs, nil
}

func (s *DBService) LocateByExternalIDBySession(ctx context.Context, sessionID string, externalMessageID string, beforeLimit int32, afterLimit int32) (LocateResult, error) {
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return LocateResult{}, err
	}
	externalMessageID = strings.TrimSpace(externalMessageID)
	if externalMessageID == "" {
		return LocateResult{}, errors.New("external message id is required")
	}
	if beforeLimit < 0 {
		beforeLimit = 0
	}
	if afterLimit < 0 {
		afterLimit = 0
	}

	rows, err := s.queries.LocateMessagesWindowByExternalIDBySession(ctx, sqlc.LocateMessagesWindowByExternalIDBySessionParams{
		SessionID:         pgSessionID,
		ExternalMessageID: toPgText(externalMessageID),
		BeforeLimit:       beforeLimit,
		AfterLimit:        afterLimit,
	})
	if err != nil {
		return LocateResult{}, err
	}
	if len(rows) == 0 {
		return LocateResult{}, pgx.ErrNoRows
	}
	if !rows[0].TargetTurnMessageSeq.Valid {
		return LocateResult{}, errors.New("message cursor missing turn sequence")
	}
	messages := toMessagesFromLocateWindowByExternalIDBySession(rows)

	s.enrichAssets(ctx, messages)
	return LocateResult{Messages: messages, TargetID: uuidString(rows[0].TargetID)}, nil
}

func (s *DBService) GetByIDBySession(ctx context.Context, sessionID string, messageID string) (Message, error) {
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return Message{}, err
	}
	pgMessageID, err := dbpkg.ParseUUID(messageID)
	if err != nil {
		return Message{}, err
	}
	row, err := s.queries.GetMessageByIDBySession(ctx, sqlc.GetMessageByIDBySessionParams{
		SessionID: pgSessionID,
		MessageID: pgMessageID,
	})
	if err != nil {
		return Message{}, err
	}
	msg := toMessageFromIDBySessionRow(row)
	msgs := []Message{msg}
	s.enrichAssets(ctx, msgs)
	return msgs[0], nil
}

func (s *DBService) ListVisibleFromBySession(ctx context.Context, sessionID string, messageID string) ([]Message, error) {
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return nil, err
	}
	pgMessageID, err := dbpkg.ParseUUID(messageID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListVisibleMessagesFromBySession(ctx, sqlc.ListVisibleMessagesFromBySessionParams{
		SessionID: pgSessionID,
		MessageID: pgMessageID,
	})
	if err != nil {
		return nil, err
	}
	msgs := toMessagesFromVisibleFromBySession(rows)
	s.enrichAssets(ctx, msgs)
	return msgs, nil
}

func (s *DBService) GetVisibleTurnByMessage(ctx context.Context, sessionID string, messageID string) (HistoryTurn, error) {
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return HistoryTurn{}, err
	}
	pgMessageID, err := dbpkg.ParseUUID(messageID)
	if err != nil {
		return HistoryTurn{}, err
	}
	row, err := s.queries.GetVisibleHistoryTurnByMessage(ctx, sqlc.GetVisibleHistoryTurnByMessageParams{
		SessionID: pgSessionID,
		MessageID: pgMessageID,
	})
	if err != nil {
		return HistoryTurn{}, err
	}
	return toHistoryTurn(row), nil
}

func (s *DBService) GetLatestVisibleTurnBySession(ctx context.Context, sessionID string) (HistoryTurn, error) {
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return HistoryTurn{}, err
	}
	row, err := s.queries.GetLatestVisibleHistoryTurnBySession(ctx, pgSessionID)
	if err != nil {
		return HistoryTurn{}, err
	}
	return toHistoryTurn(row), nil
}

func (s *DBService) replacePersistedRound(ctx context.Context, sessionID string, persisted []Message, replacement TurnReplacement) error {
	requestMessageID := strings.TrimSpace(replacement.RequestMessageID)
	if requestMessageID == "" {
		requestMessageID = firstPersistedRoleID(persisted, "user")
	}
	assistantMessageID := firstPersistedRoleID(persisted, "assistant")
	if assistantMessageID == "" {
		return errors.New("replacement assistant message was not persisted")
	}
	if _, err := replaceHistoryTurn(
		ctx,
		s.queries,
		sessionID,
		replacement.OldTurnID,
		replacement.ReplacementTurnID,
		replacement.ReplacementTurnPosition,
		requestMessageID,
		assistantMessageID,
		replacement.Reason,
	); err != nil {
		return fmt.Errorf("replace persisted history turn: %w", err)
	}
	if replacement.SessionMetadata == nil {
		return nil
	}
	metadata, err := json.Marshal(replacement.SessionMetadata)
	if err != nil {
		return fmt.Errorf("marshal replacement session metadata: %w", err)
	}
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return err
	}
	fence, fenced := runtimefence.FromContext(ctx)
	if !fenced {
		if _, err := s.queries.UpdateSessionMetadata(ctx, sqlc.UpdateSessionMetadataParams{
			Metadata: metadata,
			ID:       pgSessionID,
		}); err != nil {
			return fmt.Errorf("update replacement session metadata: %w", err)
		}
		return nil
	}
	writer, ok := s.queries.(runtimeFencedSessionMetadataWriter)
	if !ok {
		return errors.New("message store does not support fenced session metadata updates")
	}
	pgBotID, err := dbpkg.ParseUUID(fence.BotID)
	if err != nil {
		return err
	}
	if _, err := writer.UpdateSessionMetadataWithRuntimeFence(ctx, sqlc.UpdateSessionMetadataWithRuntimeFenceParams{
		Metadata:            metadata,
		ID:                  pgSessionID,
		BotID:               pgBotID,
		RuntimeFencingToken: fence.Token,
	}); errors.Is(err, pgx.ErrNoRows) {
		return runtimefence.ErrStale
	} else if err != nil {
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

func replaceHistoryTurn(
	ctx context.Context,
	queries dbstore.Queries,
	sessionID string,
	oldTurnID string,
	replacementTurnID string,
	replacementTurnPosition *int64,
	requestMessageID string,
	assistantMessageID string,
	reason string,
) (sqlc.ReplaceHistoryTurnRow, error) {
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return sqlc.ReplaceHistoryTurnRow{}, err
	}
	pgOldTurnID, err := dbpkg.ParseUUID(oldTurnID)
	if err != nil {
		return sqlc.ReplaceHistoryTurnRow{}, fmt.Errorf("invalid old turn id: %w", err)
	}
	pgReplacementTurnID, err := dbpkg.ParseUUID(replacementTurnID)
	if err != nil {
		return sqlc.ReplaceHistoryTurnRow{}, fmt.Errorf("invalid replacement turn id: %w", err)
	}
	if replacementTurnPosition == nil || *replacementTurnPosition <= 0 {
		return sqlc.ReplaceHistoryTurnRow{}, errors.New("replacement turn position must be positive")
	}
	pgRequestMessageID, err := parseOptionalUUID(requestMessageID)
	if err != nil {
		return sqlc.ReplaceHistoryTurnRow{}, fmt.Errorf("invalid request message id: %w", err)
	}
	pgAssistantMessageID, err := parseOptionalUUID(assistantMessageID)
	if err != nil {
		return sqlc.ReplaceHistoryTurnRow{}, fmt.Errorf("invalid assistant message id: %w", err)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "replace"
	}
	return queries.ReplaceHistoryTurn(ctx, sqlc.ReplaceHistoryTurnParams{
		OldTurnID:           pgOldTurnID,
		SessionID:           pgSessionID,
		ReplacementTurnID:   pgReplacementTurnID,
		ReplacementPosition: *replacementTurnPosition,
		RequestMessageID:    pgRequestMessageID,
		AssistantMessageID:  pgAssistantMessageID,
		SupersededAt:        pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		SupersededReason:    pgtype.Text{String: reason, Valid: true},
	})
}

func (s *DBService) ReplaceTurn(ctx context.Context, sessionID string, oldTurnID string, replacementTurnID string, replacementTurnPosition *int64, requestMessageID string, assistantMessageID string, reason string) (HistoryTurn, error) {
	var row sqlc.ReplaceHistoryTurnRow
	var err error
	if _, fenced := runtimefence.FromContext(ctx); fenced {
		err = runtimefence.InTransaction(ctx, s.queries, "", sessionID, func(queries dbstore.Queries) error {
			row, err = replaceHistoryTurn(ctx, queries, sessionID, oldTurnID, replacementTurnID, replacementTurnPosition, requestMessageID, assistantMessageID, reason)
			return err
		})
	} else {
		row, err = replaceHistoryTurn(ctx, s.queries, sessionID, oldTurnID, replacementTurnID, replacementTurnPosition, requestMessageID, assistantMessageID, reason)
	}
	if err != nil {
		return HistoryTurn{}, err
	}
	return toHistoryTurnFromReplaceRow(row), nil
}

// LinkAssets links asset refs to an existing persisted message.
func (s *DBService) LinkAssets(ctx context.Context, messageID string, assets []AssetRef) error {
	pgMsgID, err := dbpkg.ParseUUID(messageID)
	if err != nil {
		return fmt.Errorf("invalid message id: %w", err)
	}
	link := func(queries dbstore.Queries) error {
		if fence, fenced := runtimefence.FromContext(ctx); fenced {
			pgSessionID, parseErr := dbpkg.ParseUUID(fence.SessionID)
			if parseErr != nil {
				return parseErr
			}
			row, loadErr := queries.GetMessageByIDBySession(ctx, sqlc.GetMessageByIDBySessionParams{SessionID: pgSessionID, MessageID: pgMsgID})
			if loadErr != nil {
				return loadErr
			}
			if row.BotID.String() != fence.BotID {
				return runtimefence.ErrStale
			}
		}
		for _, ref := range assets {
			contentHash := strings.TrimSpace(ref.ContentHash)
			if contentHash == "" {
				continue
			}
			role := ref.Role
			if strings.TrimSpace(role) == "" {
				role = "attachment"
			}
			if ref.Ordinal < math.MinInt32 || ref.Ordinal > math.MaxInt32 {
				return fmt.Errorf("asset ordinal out of range: %d", ref.Ordinal)
			}
			if _, assetErr := queries.CreateMessageAsset(ctx, sqlc.CreateMessageAssetParams{
				MessageID:   pgMsgID,
				Role:        role,
				Ordinal:     int32(ref.Ordinal),
				ContentHash: contentHash,
				Mime:        strings.TrimSpace(ref.Mime),
				SizeBytes:   ref.SizeBytes,
				StorageKey:  strings.TrimSpace(ref.StorageKey),
				Name:        ref.Name,
				Metadata:    marshalMetadata(ref.Metadata),
			}); assetErr != nil {
				return fmt.Errorf("link asset to message %s: %w", messageID, assetErr)
			}
		}
		return nil
	}
	if _, fenced := runtimefence.FromContext(ctx); fenced {
		return runtimefence.InTransaction(ctx, s.queries, "", "", link)
	}
	return link(s.queries)
}

// DeleteByBot deletes all messages for a bot.
func (s *DBService) DeleteByBot(ctx context.Context, botID string) error {
	pgBotID, err := dbpkg.ParseUUID(botID)
	if err != nil {
		return err
	}
	// ClearHistoryByBot also removes the ACP publication heads, version
	// headers, and line sets in the same statement: cleared history has no
	// canonical ACP head, and a headless snapshot would be unreachable orphan
	// storage a fresh runtime must never resurrect.
	return runtimefence.InResetTransaction(ctx, s.queries, botID, "", func(queries dbstore.Queries) error {
		return queries.ClearHistoryByBot(ctx, pgBotID)
	})
}

// DeleteByIDs deletes specific messages by id.
func (s *DBService) DeleteByIDs(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	pgIDs := make([]pgtype.UUID, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		pgID, err := dbpkg.ParseUUID(id)
		if err != nil {
			return err
		}
		pgIDs = append(pgIDs, pgID)
	}
	if len(pgIDs) == 0 {
		return nil
	}
	return s.queries.DeleteMessagesByIDs(ctx, pgIDs)
}

// DeleteBySession deletes all messages for a session.
func (s *DBService) DeleteBySession(ctx context.Context, sessionID string) error {
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return err
	}
	// ClearHistoryBySession also removes the ACP publication head, version
	// headers, and line set in the same statement: cleared history has no
	// canonical ACP head, and a headless snapshot would be unreachable orphan
	// storage a fresh runtime must never resurrect.
	return runtimefence.InResetTransaction(ctx, s.queries, "", sessionID, func(queries dbstore.Queries) error {
		return queries.ClearHistoryBySession(ctx, pgSessionID)
	})
}

// --- Conversion helpers ---

func toMessageFromCreate(row sqlc.CreateMessageRow) Message {
	return toMessageFields(
		row.ID,
		row.BotID,
		row.SessionID,
		row.SenderChannelIdentityID,
		row.SenderUserID,
		pgtype.Text{},
		pgtype.Text{},
		extractPlatformFromMetadata(row.Metadata),
		row.ExternalMessageID,
		row.SourceReplyToMessageID,
		row.Role,
		row.Content,
		row.Metadata,
		row.Usage,
		row.SessionMode,
		row.RuntimeType,
		row.EventID,
		row.DisplayText,
		row.CreatedAt,
	)
}

func toMessageFromCreateWithHistoryTurn(row sqlc.CreateMessageWithHistoryTurnRow, createArg sqlc.CreateMessageParams, metadata map[string]any) Message {
	return toMessageFieldsWithMetadata(
		row.ID,
		createArg.BotID,
		createArg.SessionID,
		createArg.SenderChannelIdentityID,
		createArg.SenderUserID,
		pgtype.Text{},
		pgtype.Text{},
		extractPlatformFromMetadataMap(metadata),
		createArg.ExternalMessageID,
		createArg.SourceReplyToMessageID,
		createArg.Role,
		createArg.Content,
		createArg.Metadata,
		createArg.Usage,
		createArg.SessionMode,
		createArg.RuntimeType,
		createArg.EventID,
		createArg.DisplayText,
		row.CreatedAt,
		metadata,
	)
}

func toMessageFromCreateInHistoryTurnByRequestAndBind(row sqlc.CreateMessageInHistoryTurnByRequestAndBindRow, createArg sqlc.CreateMessageParams, metadata map[string]any) Message {
	return toMessageFieldsWithMetadata(
		row.ID,
		createArg.BotID,
		createArg.SessionID,
		createArg.SenderChannelIdentityID,
		createArg.SenderUserID,
		pgtype.Text{},
		pgtype.Text{},
		extractPlatformFromMetadataMap(metadata),
		createArg.ExternalMessageID,
		createArg.SourceReplyToMessageID,
		createArg.Role,
		createArg.Content,
		createArg.Metadata,
		createArg.Usage,
		createArg.SessionMode,
		createArg.RuntimeType,
		createArg.EventID,
		createArg.DisplayText,
		row.CreatedAt,
		metadata,
	)
}

func toMessageFromToolTailRound(row sqlc.CreateToolTailRoundRow, createArg sqlc.CreateMessageParams, metadata map[string]any) Message {
	return toMessageFieldsWithMetadata(
		row.ID,
		createArg.BotID,
		createArg.SessionID,
		createArg.SenderChannelIdentityID,
		createArg.SenderUserID,
		pgtype.Text{},
		pgtype.Text{},
		extractPlatformFromMetadataMap(metadata),
		createArg.ExternalMessageID,
		createArg.SourceReplyToMessageID,
		createArg.Role,
		createArg.Content,
		createArg.Metadata,
		createArg.Usage,
		createArg.SessionMode,
		createArg.RuntimeType,
		createArg.EventID,
		createArg.DisplayText,
		row.CreatedAt,
		metadata,
	)
}

func extractPlatformFromMetadata(metadata []byte) pgtype.Text {
	m := parseJSONMap(metadata)
	return extractPlatformFromMetadataMap(m)
}

func extractPlatformFromMetadataMap(m map[string]any) pgtype.Text {
	if v, ok := m["platform"].(string); ok && strings.TrimSpace(v) != "" {
		return pgtype.Text{String: strings.TrimSpace(v), Valid: true}
	}
	return pgtype.Text{}
}

func toMessageFromListRow(row sqlc.ListMessagesRow) Message {
	return toMessageFields(
		row.ID,
		row.BotID,
		row.SessionID,
		row.SenderChannelIdentityID,
		row.SenderUserID,
		row.SenderDisplayName,
		row.SenderAvatarUrl,
		row.Platform,
		row.ExternalMessageID,
		row.SourceReplyToMessageID,
		row.Role,
		row.Content,
		row.Metadata,
		row.Usage,
		row.SessionMode,
		row.RuntimeType,
		row.EventID,
		row.DisplayText,
		row.CreatedAt,
	)
}

func toMessageFromSessionListRow(row sqlc.ListMessagesBySessionRow) Message {
	return toMessageFields(
		row.ID,
		row.BotID,
		row.SessionID,
		row.SenderChannelIdentityID,
		row.SenderUserID,
		row.SenderDisplayName,
		row.SenderAvatarUrl,
		row.Platform,
		row.ExternalMessageID,
		row.SourceReplyToMessageID,
		row.Role,
		row.Content,
		row.Metadata,
		row.Usage,
		row.SessionMode,
		row.RuntimeType,
		row.EventID,
		row.DisplayText,
		row.CreatedAt,
	)
}

func toMessageFromSinceRow(row sqlc.ListMessagesSinceRow) Message {
	return toMessageFields(
		row.ID,
		row.BotID,
		row.SessionID,
		row.SenderChannelIdentityID,
		row.SenderUserID,
		row.SenderDisplayName,
		row.SenderAvatarUrl,
		row.Platform,
		row.ExternalMessageID,
		row.SourceReplyToMessageID,
		row.Role,
		row.Content,
		row.Metadata,
		row.Usage,
		row.SessionMode,
		row.RuntimeType,
		row.EventID,
		row.DisplayText,
		row.CreatedAt,
	)
}

func toMessageFromSinceBySessionRow(row sqlc.ListMessagesSinceBySessionRow) Message {
	return toMessageFields(
		row.ID,
		row.BotID,
		row.SessionID,
		row.SenderChannelIdentityID,
		row.SenderUserID,
		row.SenderDisplayName,
		row.SenderAvatarUrl,
		row.Platform,
		row.ExternalMessageID,
		row.SourceReplyToMessageID,
		row.Role,
		row.Content,
		row.Metadata,
		row.Usage,
		row.SessionMode,
		row.RuntimeType,
		row.EventID,
		row.DisplayText,
		row.CreatedAt,
	)
}

func toMessageFromActiveSinceRow(row sqlc.ListActiveMessagesSinceRow) Message {
	m := toMessageFields(
		row.ID,
		row.BotID,
		row.SessionID,
		row.SenderChannelIdentityID,
		row.SenderUserID,
		row.SenderDisplayName,
		row.SenderAvatarUrl,
		row.Platform,
		row.ExternalMessageID,
		row.SourceReplyToMessageID,
		row.Role,
		row.Content,
		row.Metadata,
		row.Usage,
		row.SessionMode,
		row.RuntimeType,
		row.EventID,
		row.DisplayText,
		row.CreatedAt,
	)
	if row.CompactID.Valid {
		m.CompactID = row.CompactID.String()
	}
	return m
}

func toMessageFromActiveSinceBySessionRow(row sqlc.ListActiveMessagesSinceBySessionRow) Message {
	m := toMessageFields(
		row.ID,
		row.BotID,
		row.SessionID,
		row.SenderChannelIdentityID,
		row.SenderUserID,
		row.SenderDisplayName,
		row.SenderAvatarUrl,
		row.Platform,
		row.ExternalMessageID,
		row.SourceReplyToMessageID,
		row.Role,
		row.Content,
		row.Metadata,
		row.Usage,
		row.SessionMode,
		row.RuntimeType,
		row.EventID,
		row.DisplayText,
		row.CreatedAt,
	)
	if row.CompactID.Valid {
		m.CompactID = row.CompactID.String()
	}
	return m
}

func toMessageFromLatestRow(row sqlc.ListMessagesLatestRow) Message {
	return toMessageFields(
		row.ID,
		row.BotID,
		row.SessionID,
		row.SenderChannelIdentityID,
		row.SenderUserID,
		row.SenderDisplayName,
		row.SenderAvatarUrl,
		row.Platform,
		row.ExternalMessageID,
		row.SourceReplyToMessageID,
		row.Role,
		row.Content,
		row.Metadata,
		row.Usage,
		row.SessionMode,
		row.RuntimeType,
		row.EventID,
		row.DisplayText,
		row.CreatedAt,
	)
}

func toMessageFromLatestBySessionRow(row sqlc.ListMessagesLatestBySessionRow) Message {
	message := toMessageFields(
		row.ID,
		row.BotID,
		row.SessionID,
		row.SenderChannelIdentityID,
		row.SenderUserID,
		row.SenderDisplayName,
		row.SenderAvatarUrl,
		row.Platform,
		row.ExternalMessageID,
		row.SourceReplyToMessageID,
		row.Role,
		row.Content,
		row.Metadata,
		row.Usage,
		row.SessionMode,
		row.RuntimeType,
		row.EventID,
		row.DisplayText,
		row.CreatedAt,
	)
	message.TurnID = uuidString(row.TurnID)
	return message
}

func toMessageFromLatestUIBySessionRow(row sqlc.ListMessagesLatestUIBySessionRow) Message {
	message := toMessageFieldsWithMetadataMode(
		row.ID,
		row.BotID,
		row.SessionID,
		row.SenderChannelIdentityID,
		row.SenderUserID,
		row.SenderDisplayName,
		row.SenderAvatarUrl,
		row.Platform,
		row.ExternalMessageID,
		row.SourceReplyToMessageID,
		row.Role,
		row.Content,
		row.Metadata,
		nil,
		"",
		"",
		pgtype.UUID{},
		row.DisplayText,
		row.CreatedAt,
		false,
	)
	message.TurnID = uuidString(row.TurnID)
	message.TurnPosition = int8Ptr(row.TurnPosition)
	return message
}

func toMessageFromBeforeRow(row sqlc.ListMessagesBeforeRow) Message {
	return toMessageFields(
		row.ID,
		row.BotID,
		row.SessionID,
		row.SenderChannelIdentityID,
		row.SenderUserID,
		row.SenderDisplayName,
		row.SenderAvatarUrl,
		row.Platform,
		row.ExternalMessageID,
		row.SourceReplyToMessageID,
		row.Role,
		row.Content,
		row.Metadata,
		row.Usage,
		row.SessionMode,
		row.RuntimeType,
		row.EventID,
		row.DisplayText,
		row.CreatedAt,
	)
}

func toMessageFromBeforeBySessionRow(row sqlc.ListMessagesBeforeBySessionRow) Message {
	message := toMessageFields(
		row.ID,
		row.BotID,
		row.SessionID,
		row.SenderChannelIdentityID,
		row.SenderUserID,
		row.SenderDisplayName,
		row.SenderAvatarUrl,
		row.Platform,
		row.ExternalMessageID,
		row.SourceReplyToMessageID,
		row.Role,
		row.Content,
		row.Metadata,
		row.Usage,
		row.SessionMode,
		row.RuntimeType,
		row.EventID,
		row.DisplayText,
		row.CreatedAt,
	)
	message.TurnID = uuidString(row.TurnID)
	message.TurnPosition = int8Ptr(row.TurnPosition)
	return message
}

func toMessageFromBeforeCursorBySessionRow(row sqlc.ListMessagesBeforeCursorBySessionRow) Message {
	message := toMessageFields(
		row.ID,
		row.BotID,
		row.SessionID,
		row.SenderChannelIdentityID,
		row.SenderUserID,
		row.SenderDisplayName,
		row.SenderAvatarUrl,
		row.Platform,
		row.ExternalMessageID,
		row.SourceReplyToMessageID,
		row.Role,
		row.Content,
		row.Metadata,
		row.Usage,
		row.SessionMode,
		row.RuntimeType,
		row.EventID,
		row.DisplayText,
		row.CreatedAt,
	)
	message.TurnID = uuidString(row.TurnID)
	message.TurnPosition = int8Ptr(row.TurnPosition)
	return message
}

func toMessageFromIDBySessionRow(row sqlc.GetMessageByIDBySessionRow) Message {
	return toMessageFields(
		row.ID,
		row.BotID,
		row.SessionID,
		row.SenderChannelIdentityID,
		row.SenderUserID,
		row.SenderDisplayName,
		row.SenderAvatarUrl,
		row.Platform,
		row.ExternalMessageID,
		row.SourceReplyToMessageID,
		row.Role,
		row.Content,
		row.Metadata,
		row.Usage,
		row.SessionMode,
		row.RuntimeType,
		row.EventID,
		row.DisplayText,
		row.CreatedAt,
	)
}

func toMessageFromLocateWindowByExternalIDBySessionRow(row sqlc.LocateMessagesWindowByExternalIDBySessionRow) Message {
	message := toMessageFields(
		row.ID,
		row.BotID,
		row.SessionID,
		row.SenderChannelIdentityID,
		row.SenderUserID,
		row.SenderDisplayName,
		row.SenderAvatarUrl,
		row.Platform,
		row.ExternalMessageID,
		row.SourceReplyToMessageID,
		row.Role,
		row.Content,
		row.Metadata,
		row.Usage,
		row.SessionMode,
		row.RuntimeType,
		row.EventID,
		row.DisplayText,
		row.CreatedAt,
	)
	message.TurnID = uuidString(row.TurnID)
	message.TurnPosition = int8Ptr(row.TurnPosition)
	return message
}

func toMessageFields(
	id pgtype.UUID,
	botID pgtype.UUID,
	sessionID pgtype.UUID,
	senderChannelIdentityID pgtype.UUID,
	senderUserID pgtype.UUID,
	senderDisplayName pgtype.Text,
	senderAvatarURL pgtype.Text,
	platform pgtype.Text,
	externalMessageID pgtype.Text,
	sourceReplyToMessageID pgtype.Text,
	role string,
	content []byte,
	metadata []byte,
	usage []byte,
	sessionMode string,
	runtimeType string,
	eventID pgtype.UUID,
	displayText pgtype.Text,
	createdAt pgtype.Timestamptz,
) Message {
	return toMessageFieldsWithMetadataMode(
		id,
		botID,
		sessionID,
		senderChannelIdentityID,
		senderUserID,
		senderDisplayName,
		senderAvatarURL,
		platform,
		externalMessageID,
		sourceReplyToMessageID,
		role,
		content,
		metadata,
		usage,
		sessionMode,
		runtimeType,
		eventID,
		displayText,
		createdAt,
		true,
	)
}

func toMessageFieldsWithMetadata(
	id pgtype.UUID,
	botID pgtype.UUID,
	sessionID pgtype.UUID,
	senderChannelIdentityID pgtype.UUID,
	senderUserID pgtype.UUID,
	senderDisplayName pgtype.Text,
	senderAvatarURL pgtype.Text,
	platform pgtype.Text,
	externalMessageID pgtype.Text,
	sourceReplyToMessageID pgtype.Text,
	role string,
	content []byte,
	rawMetadata []byte,
	usage []byte,
	sessionMode string,
	runtimeType string,
	eventID pgtype.UUID,
	displayText pgtype.Text,
	createdAt pgtype.Timestamptz,
	metadata map[string]any,
) Message {
	m := toMessageFieldsWithMetadataMode(
		id,
		botID,
		sessionID,
		senderChannelIdentityID,
		senderUserID,
		senderDisplayName,
		senderAvatarURL,
		platform,
		externalMessageID,
		sourceReplyToMessageID,
		role,
		content,
		rawMetadata,
		usage,
		sessionMode,
		runtimeType,
		eventID,
		displayText,
		createdAt,
		false,
	)
	m.Metadata = metadata
	return m
}

func toMessageFieldsWithMetadataMode(
	id pgtype.UUID,
	botID pgtype.UUID,
	sessionID pgtype.UUID,
	senderChannelIdentityID pgtype.UUID,
	senderUserID pgtype.UUID,
	senderDisplayName pgtype.Text,
	senderAvatarURL pgtype.Text,
	platform pgtype.Text,
	externalMessageID pgtype.Text,
	sourceReplyToMessageID pgtype.Text,
	role string,
	content []byte,
	metadata []byte,
	usage []byte,
	sessionMode string,
	runtimeType string,
	eventID pgtype.UUID,
	displayText pgtype.Text,
	createdAt pgtype.Timestamptz,
	parseMetadata bool,
) Message {
	m := Message{
		ID:                      id.String(),
		BotID:                   botID.String(),
		SessionID:               sessionID.String(),
		SenderChannelIdentityID: senderChannelIdentityID.String(),
		SenderUserID:            senderUserID.String(),
		SenderDisplayName:       dbpkg.TextToString(senderDisplayName),
		SenderAvatarURL:         dbpkg.TextToString(senderAvatarURL),
		Platform:                dbpkg.TextToString(platform),
		ExternalMessageID:       dbpkg.TextToString(externalMessageID),
		SourceReplyToMessageID:  dbpkg.TextToString(sourceReplyToMessageID),
		Role:                    role,
		Content:                 json.RawMessage(content),
		RawMetadata:             json.RawMessage(metadata),
		Usage:                   json.RawMessage(usage),
		SessionMode:             sessionMode,
		RuntimeType:             runtimeType,
		DisplayContent:          dbpkg.TextToString(displayText),
		CreatedAt:               createdAt.Time,
	}
	if eventID.Valid {
		m.EventID = eventID.String()
	}
	if parseMetadata {
		m.Metadata = parseJSONMap(metadata)
	}
	return m
}

func toMessagesFromList(rows []sqlc.ListMessagesRow) []Message {
	messages := make([]Message, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, toMessageFromListRow(row))
	}
	return messages
}

func toMessagesFromSessionList(rows []sqlc.ListMessagesBySessionRow) []Message {
	messages := make([]Message, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, toMessageFromSessionListRow(row))
	}
	return messages
}

func toMessagesFromSince(rows []sqlc.ListMessagesSinceRow) []Message {
	messages := make([]Message, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, toMessageFromSinceRow(row))
	}
	return messages
}

func toMessagesFromSinceBySession(rows []sqlc.ListMessagesSinceBySessionRow) []Message {
	messages := make([]Message, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, toMessageFromSinceBySessionRow(row))
	}
	return messages
}

func toMessagesFromActiveSince(rows []sqlc.ListActiveMessagesSinceRow) []Message {
	messages := make([]Message, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, toMessageFromActiveSinceRow(row))
	}
	return messages
}

func toMessagesFromActiveSinceBySession(rows []sqlc.ListActiveMessagesSinceBySessionRow) []Message {
	messages := make([]Message, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, toMessageFromActiveSinceBySessionRow(row))
	}
	return messages
}

func toMessagesFromActiveSinceWithinBytes(rows []sqlc.ListActiveMessagesSinceWithinBytesRow) []Message {
	messages := make([]Message, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, toMessageFromActiveSinceBySessionRow(sqlc.ListActiveMessagesSinceBySessionRow(row)))
	}
	return messages
}

func toMessagesFromActiveSinceBySessionWithinBytes(rows []sqlc.ListActiveMessagesSinceBySessionWithinBytesRow) []Message {
	messages := make([]Message, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, toMessageFromActiveSinceBySessionRow(sqlc.ListActiveMessagesSinceBySessionRow(row)))
	}
	return messages
}

func toMessagesFromLatest(rows []sqlc.ListMessagesLatestRow) []Message {
	messages := make([]Message, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, toMessageFromLatestRow(row))
	}
	return messages
}

func toMessagesFromLatestBySession(rows []sqlc.ListMessagesLatestBySessionRow) []Message {
	messages := make([]Message, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, toMessageFromLatestBySessionRow(row))
	}
	return messages
}

func toMessagesFromLatestUIBySession(rows []sqlc.ListMessagesLatestUIBySessionRow) []Message {
	messages := make([]Message, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, toMessageFromLatestUIBySessionRow(row))
	}
	return messages
}

// toMessagesFromBefore returns messages in oldest-first order (ListMessagesBefore returns DESC; we reverse).
func toMessagesFromBefore(rows []sqlc.ListMessagesBeforeRow) []Message {
	messages := make([]Message, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		messages = append(messages, toMessageFromBeforeRow(rows[i]))
	}
	return messages
}

func toMessagesFromBeforeBySession(rows []sqlc.ListMessagesBeforeBySessionRow) []Message {
	messages := make([]Message, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		messages = append(messages, toMessageFromBeforeBySessionRow(rows[i]))
	}
	return messages
}

func toMessagesFromBeforeCursorBySession(rows []sqlc.ListMessagesBeforeCursorBySessionRow) []Message {
	messages := make([]Message, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		messages = append(messages, toMessageFromBeforeCursorBySessionRow(rows[i]))
	}
	return messages
}

func toMessagesFromLocateWindowByExternalIDBySession(rows []sqlc.LocateMessagesWindowByExternalIDBySessionRow) []Message {
	messages := make([]Message, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, toMessageFromLocateWindowByExternalIDBySessionRow(row))
	}
	return messages
}

func toMessagesFromVisibleFromBySession(rows []sqlc.ListVisibleMessagesFromBySessionRow) []Message {
	messages := make([]Message, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, toMessageFromVisibleFromBySessionRow(row))
	}
	return messages
}

func toMessageFromVisibleFromBySessionRow(row sqlc.ListVisibleMessagesFromBySessionRow) Message {
	m := toMessageFields(
		row.ID,
		row.BotID,
		row.SessionID,
		row.SenderChannelIdentityID,
		row.SenderUserID,
		row.SenderDisplayName,
		row.SenderAvatarUrl,
		row.Platform,
		row.ExternalMessageID,
		row.SourceReplyToMessageID,
		row.Role,
		row.Content,
		row.Metadata,
		row.Usage,
		row.SessionMode,
		row.RuntimeType,
		row.EventID,
		row.DisplayText,
		row.CreatedAt,
	)
	m.TurnID = uuidString(row.TurnID)
	if row.CompactID.Valid {
		m.CompactID = row.CompactID.String()
	}
	return m
}

func toHistoryTurn(row dbstore.HistoryTurn) HistoryTurn {
	return HistoryTurn{
		ID:                 uuidString(row.ID),
		BotID:              uuidString(row.BotID),
		SessionID:          uuidString(row.SessionID),
		Position:           row.Position,
		RequestMessageID:   uuidString(row.RequestMessageID),
		AssistantMessageID: uuidString(row.AssistantMessageID),
		SupersededByTurnID: uuidString(row.SupersededByTurnID),
		SupersededAt:       timeFromTimestamptz(row.SupersededAt),
		SupersededReason:   row.SupersededReason,
		CreatedAt:          timeFromTimestamptz(row.CreatedAt),
		UpdatedAt:          timeFromTimestamptz(row.UpdatedAt),
	}
}

func toHistoryTurnFromReplaceRow(row sqlc.ReplaceHistoryTurnRow) HistoryTurn {
	return HistoryTurn{
		ID:                 uuidString(row.ID),
		BotID:              uuidString(row.BotID),
		SessionID:          uuidString(row.SessionID),
		Position:           row.Position,
		RequestMessageID:   uuidString(row.RequestMessageID),
		AssistantMessageID: uuidString(row.AssistantMessageID),
		SupersededByTurnID: uuidString(row.SupersededByTurnID),
		SupersededAt:       timeFromTimestamptz(row.SupersededAt),
		SupersededReason:   textString(row.SupersededReason),
		CreatedAt:          timeFromTimestamptz(row.CreatedAt),
		UpdatedAt:          timeFromTimestamptz(row.UpdatedAt),
	}
}

func parseOptionalUUID(id string) (pgtype.UUID, error) {
	if strings.TrimSpace(id) == "" {
		return pgtype.UUID{}, nil
	}
	return dbpkg.ParseUUID(id)
}

func uuidString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return id.String()
}

func timeFromTimestamptz(value pgtype.Timestamptz) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time
}

func textString(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func toPgText(value string) pgtype.Text {
	value = strings.TrimSpace(value)
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

// toPgInt8 keeps nil distinct from zero: position 0 is the first turn of a
// session, so a plain int64 could not say "no position was supplied".
func toPgInt8(value *int64) pgtype.Int8 {
	if value == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *value, Valid: true}
}

// int8Ptr is the read-side mirror of toPgInt8: NULL means "position unknown"
// (rows outside the UI turn read model), so nil is preserved instead of zero.
func int8Ptr(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	return &value.Int64
}

func nonNilMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func coalesce(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func parseJSONMap(data []byte) map[string]any {
	if len(data) == 0 {
		return nil
	}
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	return m
}

func (s *DBService) publishMessageCreated(message Message) {
	if s.publisher == nil {
		return
	}
	payload, err := json.Marshal(message)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("marshal message event failed", slog.Any("error", err))
		}
		return
	}
	s.publisher.Publish(event.Event{
		Type:  event.EventTypeMessageCreated,
		BotID: strings.TrimSpace(message.BotID),
		Data:  payload,
	})
}

// enrichAssets batch-loads asset links for a list of messages (single-table query).
func (s *DBService) enrichAssets(ctx context.Context, messages []Message) {
	if len(messages) == 0 {
		return
	}
	ids := make([]pgtype.UUID, 0, len(messages))
	for _, m := range messages {
		pgID, err := dbpkg.ParseUUID(m.ID)
		if err != nil {
			continue
		}
		ids = append(ids, pgID)
	}
	if len(ids) == 0 {
		return
	}
	rows, err := s.queries.ListMessageAssetsBatch(ctx, ids)
	if err != nil {
		s.logger.Warn("enrich assets failed, returning messages without assets", slog.Any("error", err))
		ensureAssetsSlice(messages)
		return
	}
	assetMap := map[string][]MessageAsset{}
	for _, row := range rows {
		msgID := row.MessageID.String()
		contentHash := strings.TrimSpace(row.ContentHash)
		if contentHash == "" {
			continue
		}
		metadata := unmarshalMetadata(row.Metadata)
		storageKey := strings.TrimSpace(row.StorageKey)
		if storageKey == "" {
			storageKey, _ = metadata["storage_key"].(string)
			storageKey = strings.TrimSpace(storageKey)
		}
		mimeType := strings.TrimSpace(row.Mime)
		if mimeType == "" {
			mimeType = media.MimeFromPath(coalesce(storageKey, row.Name))
		}
		assetMap[msgID] = append(assetMap[msgID], MessageAsset{
			ContentHash: contentHash,
			Role:        row.Role,
			Ordinal:     int(row.Ordinal),
			Mime:        mimeType,
			SizeBytes:   row.SizeBytes,
			StorageKey:  storageKey,
			Name:        row.Name,
			Metadata:    metadata,
		})
	}
	for i := range messages {
		if assets, ok := assetMap[messages[i].ID]; ok {
			messages[i].Assets = assets
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

func marshalMetadata(m map[string]any) []byte {
	if len(m) == 0 {
		return []byte("{}")
	}
	b, err := json.Marshal(m)
	if err != nil {
		return []byte("{}")
	}
	return b
}

func unmarshalMetadata(b []byte) map[string]any {
	if len(b) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil || len(m) == 0 {
		return nil
	}
	return m
}
