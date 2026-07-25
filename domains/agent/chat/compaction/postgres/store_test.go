package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/domains/agent/chat/compaction"
	dbsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
)

type fakeQueries struct {
	candidates  []dbsqlc.ListUncompactedMessagesBySessionRow
	createRow   dbsqlc.AgentBotHistoryMessageCompact
	createErr   error
	markArg     dbsqlc.MarkMessagesCompactedParams
	completeArg dbsqlc.CompleteCompactionLogParams
	completeErr error
	artifactRow dbsqlc.AgentBotHistoryMessageCompact
	artifactErr error
}

func (f *fakeQueries) ListUncompactedMessagesBySession(context.Context, pgtype.UUID) ([]dbsqlc.ListUncompactedMessagesBySessionRow, error) {
	return f.candidates, nil
}

func (f *fakeQueries) CreateCompactionLog(context.Context, dbsqlc.CreateCompactionLogParams) (dbsqlc.AgentBotHistoryMessageCompact, error) {
	return f.createRow, f.createErr
}

func (f *fakeQueries) MarkMessagesCompacted(_ context.Context, arg dbsqlc.MarkMessagesCompactedParams) (int64, error) {
	f.markArg = arg
	return int64(len(arg.MessageIds)), nil
}

func (*fakeQueries) ListMessageAssetsBatch(context.Context, []pgtype.UUID) ([]dbsqlc.ListMessageAssetsBatchRow, error) {
	return nil, nil
}

func (f *fakeQueries) CompleteCompactionLog(_ context.Context, arg dbsqlc.CompleteCompactionLogParams) (dbsqlc.AgentBotHistoryMessageCompact, error) {
	f.completeArg = arg
	return dbsqlc.AgentBotHistoryMessageCompact{}, f.completeErr
}

func (*fakeQueries) CountCompactionLogsByBot(context.Context, pgtype.UUID) (int64, error) {
	return 0, nil
}

func (*fakeQueries) ListCompactionLogsByBot(context.Context, dbsqlc.ListCompactionLogsByBotParams) ([]dbsqlc.AgentBotHistoryMessageCompact, error) {
	return nil, nil
}

func (*fakeQueries) DeleteCompactionLogsByBot(context.Context, pgtype.UUID) error {
	return nil
}

func (f *fakeQueries) GetCompactionLogByID(context.Context, pgtype.UUID) (dbsqlc.AgentBotHistoryMessageCompact, error) {
	return f.artifactRow, f.artifactErr
}

func (*fakeQueries) ListCompactionArtifactLineageBySession(context.Context, pgtype.UUID) ([]dbsqlc.AgentBotHistoryMessageCompact, error) {
	return nil, nil
}

func (*fakeQueries) ListCompactionArtifactParentIDsBySuccessor(context.Context, dbsqlc.ListCompactionArtifactParentIDsBySuccessorParams) ([]pgtype.UUID, error) {
	return nil, nil
}

func TestStoreListCandidatesConvertsSQLCRow(t *testing.T) {
	t.Parallel()

	id := pgUUID(uuid.New())
	createdAt := time.Unix(123, 0).UTC()
	queries := &fakeQueries{candidates: []dbsqlc.ListUncompactedMessagesBySessionRow{{
		ID:                id,
		Role:              "user",
		Content:           []byte(`"hello"`),
		CompactID:         pgtype.UUID{},
		CreatedAt:         pgtype.Timestamptz{Time: createdAt, Valid: true},
		ExternalMessageID: pgtype.Text{String: "  external-1  ", Valid: true},
		ConversationType:  pgtype.Text{String: " group ", Valid: true},
		ConversationName:  pgtype.Text{String: " Memoh ", Valid: true},
		ReplyTarget:       pgtype.Text{String: " chat-1 ", Valid: true},
	}}}

	rows, err := NewStore(queries).ListCandidates(t.Context(), uuid.NewString())
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].ID != uuid.UUID(id.Bytes).String() ||
		rows[0].CompactID != "" ||
		rows[0].ExternalMessageID != "external-1" ||
		rows[0].ConversationType != "group" ||
		rows[0].ConversationName != "Memoh" ||
		rows[0].ReplyTarget != "chat-1" ||
		!rows[0].CreatedAt.Equal(createdAt) {
		t.Fatalf("converted candidate = %#v", rows[0])
	}
}

func TestStoreClaimCandidatesConvertsRequiredAndOptionalUUIDs(t *testing.T) {
	t.Parallel()

	logID, messageID := uuid.New(), uuid.New()
	queries := &fakeQueries{}
	marked, err := NewStore(queries).ClaimCandidates(t.Context(), compaction.ClaimCandidatesInput{
		LogID:              logID.String(),
		MessageIDs:         []string{messageID.String()},
		ExpectedCompactIDs: []string{""},
	})
	if err != nil {
		t.Fatalf("ClaimCandidates: %v", err)
	}
	if marked != 1 ||
		queries.markArg.CompactID.Bytes != logID ||
		queries.markArg.MessageIds[0].Bytes != messageID ||
		queries.markArg.ExpectedCompactIds[0].Valid {
		t.Fatalf("mark params = %#v", queries.markArg)
	}
}

func TestStoreMapsSQLCNoRowsErrors(t *testing.T) {
	t.Parallel()

	queries := &fakeQueries{createErr: pgx.ErrNoRows, artifactErr: pgx.ErrNoRows}
	store := NewStore(queries)
	_, err := store.CreateLog(t.Context(), compaction.CreateLogInput{
		BotID:     uuid.NewString(),
		SessionID: uuid.NewString(),
	})
	if !errors.Is(err, compaction.ErrPersistenceConflict) {
		t.Fatalf("CreateLog error = %v, want persistence conflict", err)
	}
	_, err = store.GetArtifact(t.Context(), uuid.NewString())
	if !errors.Is(err, compaction.ErrArtifactNotFound) {
		t.Fatalf("GetArtifact error = %v, want artifact not found", err)
	}
}

func TestStoreCompleteLogConvertsArtifactInputAndMapsConflict(t *testing.T) {
	t.Parallel()

	id, modelID := uuid.New(), uuid.New()
	queries := &fakeQueries{completeErr: pgx.ErrNoRows}
	err := NewStore(queries).CompleteLog(t.Context(), compaction.CompleteLogInput{
		ID:           id.String(),
		Status:       "ok",
		Summary:      "summary",
		MessageCount: 2,
		ModelID:      modelID.String(),
		Coverage:     []byte(`[]`),
	})
	if !errors.Is(err, compaction.ErrPersistenceConflict) {
		t.Fatalf("CompleteLog error = %v, want persistence conflict", err)
	}
	if queries.completeArg.ID.Bytes != id ||
		queries.completeArg.ModelID.Bytes != modelID ||
		queries.completeArg.MessageCount != 2 {
		t.Fatalf("complete params = %#v", queries.completeArg)
	}
}

func TestStoreGetArtifactConvertsLineageFields(t *testing.T) {
	t.Parallel()

	id, parentID := uuid.New(), uuid.New()
	startedAt := time.Unix(456, 0).UTC()
	queries := &fakeQueries{artifactRow: dbsqlc.AgentBotHistoryMessageCompact{
		ID:              pgUUID(id),
		Status:          "ok",
		ArtifactVersion: 1,
		ParentIds:       []pgtype.UUID{pgUUID(parentID)},
		StartedAt:       pgtype.Timestamptz{Time: startedAt, Valid: true},
	}}
	got, err := NewStore(queries).GetArtifact(t.Context(), id.String())
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}
	if got.ID != id.String() ||
		len(got.ParentIDs) != 1 ||
		got.ParentIDs[0] != parentID.String() ||
		!got.StartedAt.Equal(startedAt) {
		t.Fatalf("artifact = %#v", got)
	}
}

func pgUUID(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}
