package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/domains/agent/chat/compaction"
	dbsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
)

type Queries interface {
	ListUncompactedMessagesBySession(context.Context, pgtype.UUID) ([]dbsqlc.ListUncompactedMessagesBySessionRow, error)
	CreateCompactionLog(context.Context, dbsqlc.CreateCompactionLogParams) (dbsqlc.AgentBotHistoryMessageCompact, error)
	MarkMessagesCompacted(context.Context, dbsqlc.MarkMessagesCompactedParams) (int64, error)
	ListMessageAssetsBatch(context.Context, []pgtype.UUID) ([]dbsqlc.ListMessageAssetsBatchRow, error)
	CompleteCompactionLog(context.Context, dbsqlc.CompleteCompactionLogParams) (dbsqlc.AgentBotHistoryMessageCompact, error)
	CountCompactionLogsByBot(context.Context, pgtype.UUID) (int64, error)
	ListCompactionLogsByBot(context.Context, dbsqlc.ListCompactionLogsByBotParams) ([]dbsqlc.AgentBotHistoryMessageCompact, error)
	DeleteCompactionLogsByBot(context.Context, pgtype.UUID) error
	GetCompactionLogByID(context.Context, pgtype.UUID) (dbsqlc.AgentBotHistoryMessageCompact, error)
	ListCompactionArtifactLineageBySession(context.Context, pgtype.UUID) ([]dbsqlc.AgentBotHistoryMessageCompact, error)
	ListCompactionArtifactParentIDsBySuccessor(context.Context, dbsqlc.ListCompactionArtifactParentIDsBySuccessorParams) ([]pgtype.UUID, error)
}

type Store struct {
	queries Queries
}

type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func NewStore(queries Queries) *Store {
	return &Store{queries: queries}
}

func NewStoreFromDB(db DBTX) *Store {
	return NewStore(dbsqlc.New(db))
}

var (
	_ compaction.CompactionStore = (*Store)(nil)
	_ compaction.ArtifactStore   = (*Store)(nil)
)

func (s *Store) ListCandidates(ctx context.Context, sessionID string) ([]compaction.CandidateRecord, error) {
	id, err := parseUUID(sessionID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListUncompactedMessagesBySession(ctx, id)
	if err != nil {
		return nil, err
	}
	records := make([]compaction.CandidateRecord, len(rows))
	for i, row := range rows {
		records[i] = candidateRecord(row)
	}
	return records, nil
}

func (s *Store) CreateLog(ctx context.Context, input compaction.CreateLogInput) (string, error) {
	botID, err := parseUUID(input.BotID)
	if err != nil {
		return "", err
	}
	sessionID, err := parseUUID(input.SessionID)
	if err != nil {
		return "", err
	}
	row, err := s.queries.CreateCompactionLog(ctx, dbsqlc.CreateCompactionLogParams{
		BotID:         botID,
		SessionID:     sessionID,
		ExpectedEpoch: input.ExpectedEpoch,
	})
	if err != nil {
		return "", mapConflict(err)
	}
	return uuidString(row.ID), nil
}

func (s *Store) ClaimCandidates(ctx context.Context, input compaction.ClaimCandidatesInput) (int64, error) {
	logID, err := parseUUID(input.LogID)
	if err != nil {
		return 0, err
	}
	messageIDs, err := parseUUIDs(input.MessageIDs)
	if err != nil {
		return 0, err
	}
	expectedIDs, err := parseOptionalUUIDs(input.ExpectedCompactIDs)
	if err != nil {
		return 0, err
	}
	return s.queries.MarkMessagesCompacted(ctx, dbsqlc.MarkMessagesCompactedParams{
		CompactID:          logID,
		MessageIds:         messageIDs,
		ExpectedCompactIds: expectedIDs,
	})
}

func (s *Store) ListAssets(ctx context.Context, messageIDs []string) ([]compaction.AssetRecord, error) {
	ids, err := parseUUIDs(messageIDs)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListMessageAssetsBatch(ctx, ids)
	if err != nil {
		return nil, err
	}
	records := make([]compaction.AssetRecord, len(rows))
	for i, row := range rows {
		records[i] = compaction.AssetRecord{
			MessageID:   uuidString(row.MessageID),
			Role:        strings.TrimSpace(row.Role),
			Ordinal:     int(row.Ordinal),
			ContentHash: strings.TrimSpace(row.ContentHash),
			Name:        strings.TrimSpace(row.Name),
			Metadata:    cloneBytes(row.Metadata),
		}
	}
	return records, nil
}

func (s *Store) CompleteLog(ctx context.Context, input compaction.CompleteLogInput) error {
	if input.MessageCount < 0 || input.MessageCount > math.MaxInt32 {
		return fmt.Errorf("compaction message count %d is out of range", input.MessageCount)
	}
	id, err := parseUUID(input.ID)
	if err != nil {
		return err
	}
	modelID, err := parseOptionalUUID(input.ModelID)
	if err != nil {
		return err
	}
	_, err = s.queries.CompleteCompactionLog(ctx, dbsqlc.CompleteCompactionLogParams{
		ID:            id,
		Status:        input.Status,
		Summary:       input.Summary,
		MessageCount:  int32(input.MessageCount),
		ErrorMessage:  input.ErrorMessage,
		Usage:         cloneBytes(input.Usage),
		ModelID:       modelID,
		Coverage:      cloneBytes(input.Coverage),
		AnchorStartMs: input.AnchorStartMs,
		AnchorEndMs:   input.AnchorEndMs,
	})
	return mapConflict(err)
}

func (s *Store) CountLogs(ctx context.Context, botID string) (int64, error) {
	id, err := parseUUID(botID)
	if err != nil {
		return 0, err
	}
	return s.queries.CountCompactionLogsByBot(ctx, id)
}

func (s *Store) ListLogs(ctx context.Context, input compaction.ListLogsInput) ([]compaction.LogRecord, error) {
	if input.Limit < 0 || input.Limit > math.MaxInt32 || input.Offset < 0 || input.Offset > math.MaxInt32 {
		return nil, errors.New("compaction log pagination is out of range")
	}
	botID, err := parseUUID(input.BotID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListCompactionLogsByBot(ctx, dbsqlc.ListCompactionLogsByBotParams{
		BotID:  botID,
		Limit:  int32(input.Limit),
		Offset: int32(input.Offset),
	})
	if err != nil {
		return nil, err
	}
	records := make([]compaction.LogRecord, len(rows))
	for i, row := range rows {
		records[i] = logRecord(row)
	}
	return records, nil
}

func (s *Store) DeleteLogs(ctx context.Context, botID string) error {
	id, err := parseUUID(botID)
	if err != nil {
		return err
	}
	return s.queries.DeleteCompactionLogsByBot(ctx, id)
}

func (s *Store) GetArtifact(ctx context.Context, id string) (compaction.ArtifactRecord, error) {
	artifactID, err := parseUUID(id)
	if err != nil {
		return compaction.ArtifactRecord{}, err
	}
	row, err := s.queries.GetCompactionLogByID(ctx, artifactID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return compaction.ArtifactRecord{}, compaction.ErrArtifactNotFound
		}
		return compaction.ArtifactRecord{}, err
	}
	return artifactRecord(row), nil
}

func (s *Store) ListArtifactsBySession(ctx context.Context, sessionID string) ([]compaction.ArtifactRecord, error) {
	id, err := parseUUID(sessionID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListCompactionArtifactLineageBySession(ctx, id)
	if err != nil {
		return nil, err
	}
	return artifactRecords(rows), nil
}

func (s *Store) ListParentIDs(ctx context.Context, input compaction.ArtifactParentsInput) ([]string, error) {
	successorID, err := parseUUID(input.SuccessorID)
	if err != nil {
		return nil, err
	}
	botID, err := parseUUID(input.BotID)
	if err != nil {
		return nil, err
	}
	sessionID, err := parseUUID(input.SessionID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListCompactionArtifactParentIDsBySuccessor(ctx, dbsqlc.ListCompactionArtifactParentIDsBySuccessorParams{
		SuccessorID: successorID,
		BotID:       botID,
		SessionID:   sessionID,
	})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if id := uuidString(row); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func candidateRecord(row dbsqlc.ListUncompactedMessagesBySessionRow) compaction.CandidateRecord {
	return compaction.CandidateRecord{
		ID:                      uuidString(row.ID),
		BotID:                   uuidString(row.BotID),
		SessionID:               uuidString(row.SessionID),
		SenderChannelIdentityID: uuidString(row.SenderChannelIdentityID),
		SenderUserID:            uuidString(row.SenderUserID),
		SenderDisplayName:       text(row.SenderDisplayName),
		SenderAvatarURL:         text(row.SenderAvatarUrl),
		Platform:                text(row.Platform),
		ExternalMessageID:       text(row.ExternalMessageID),
		SourceReplyToMessageID:  text(row.SourceReplyToMessageID),
		Role:                    row.Role,
		Content:                 cloneBytes(row.Content),
		Metadata:                cloneBytes(row.Metadata),
		Usage:                   cloneBytes(row.Usage),
		CompactID:               uuidString(row.CompactID),
		EventID:                 uuidString(row.EventID),
		DisplayText:             text(row.DisplayText),
		CreatedAt:               timestamp(row.CreatedAt),
		CompactionEpoch:         row.CompactionEpoch,
		ConversationType:        text(row.ConversationType),
		ConversationName:        text(row.ConversationName),
		ReplyTarget:             text(row.ReplyTarget),
	}
}

func artifactRecords(rows []dbsqlc.AgentBotHistoryMessageCompact) []compaction.ArtifactRecord {
	records := make([]compaction.ArtifactRecord, len(rows))
	for i, row := range rows {
		records[i] = artifactRecord(row)
	}
	return records
}

func artifactRecord(row dbsqlc.AgentBotHistoryMessageCompact) compaction.ArtifactRecord {
	parentIDs := make([]string, 0, len(row.ParentIds))
	for _, parentID := range row.ParentIds {
		if id := uuidString(parentID); id != "" {
			parentIDs = append(parentIDs, id)
		}
	}
	return compaction.ArtifactRecord{
		ID:              uuidString(row.ID),
		BotID:           uuidString(row.BotID),
		SessionID:       uuidString(row.SessionID),
		Status:          strings.TrimSpace(row.Status),
		Summary:         row.Summary,
		MessageCount:    int(row.MessageCount),
		ErrorMessage:    row.ErrorMessage,
		Usage:           cloneBytes(row.Usage),
		ModelID:         uuidString(row.ModelID),
		ArtifactVersion: int(row.ArtifactVersion),
		Coverage:        cloneBytes(row.Coverage),
		AnchorStartMs:   row.AnchorStartMs,
		AnchorEndMs:     row.AnchorEndMs,
		ArtifactLevel:   int(row.ArtifactLevel),
		ParentIDs:       parentIDs,
		SupersededBy:    uuidString(row.SupersededBy),
		SupersededAt:    timestamp(row.SupersededAt),
		StartedAt:       timestamp(row.StartedAt),
		CompletedAt:     timestamp(row.CompletedAt),
	}
}

func logRecord(row dbsqlc.AgentBotHistoryMessageCompact) compaction.LogRecord {
	return compaction.LogRecord{
		ID:           uuidString(row.ID),
		BotID:        uuidString(row.BotID),
		SessionID:    uuidString(row.SessionID),
		Status:       strings.TrimSpace(row.Status),
		Summary:      row.Summary,
		MessageCount: int(row.MessageCount),
		ErrorMessage: row.ErrorMessage,
		Usage:        cloneBytes(row.Usage),
		ModelID:      uuidString(row.ModelID),
		StartedAt:    timestamp(row.StartedAt),
		CompletedAt:  timestamp(row.CompletedAt),
	}
}

func parseUUID(value string) (pgtype.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("parse UUID %q: %w", value, err)
	}
	return pgtype.UUID{Bytes: id, Valid: true}, nil
}

func parseOptionalUUID(value string) (pgtype.UUID, error) {
	if strings.TrimSpace(value) == "" {
		return pgtype.UUID{}, nil
	}
	return parseUUID(value)
}

func parseUUIDs(values []string) ([]pgtype.UUID, error) {
	ids := make([]pgtype.UUID, len(values))
	for i, value := range values {
		id, err := parseUUID(value)
		if err != nil {
			return nil, fmt.Errorf("UUID at index %d: %w", i, err)
		}
		ids[i] = id
	}
	return ids, nil
}

func parseOptionalUUIDs(values []string) ([]pgtype.UUID, error) {
	ids := make([]pgtype.UUID, len(values))
	for i, value := range values {
		id, err := parseOptionalUUID(value)
		if err != nil {
			return nil, fmt.Errorf("UUID at index %d: %w", i, err)
		}
		ids[i] = id
	}
	return ids, nil
}

func mapConflict(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return compaction.ErrPersistenceConflict
	}
	return err
}

func uuidString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return uuid.UUID(value.Bytes).String()
}

func text(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return strings.TrimSpace(value.String)
}

func timestamp(value pgtype.Timestamptz) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time.UTC()
}

func cloneBytes(value []byte) []byte {
	return append([]byte(nil), value...)
}
