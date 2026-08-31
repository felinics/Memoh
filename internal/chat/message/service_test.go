package message

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	messageevent "github.com/felinics/memoh/internal/chat/event"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type runtimeSnapshotQueries struct {
	dbstore.Queries

	created      sqlc.CreateMessageParams
	createdAsset sqlc.CreateMessageAssetParams
	assetErr     error
}

func (q *runtimeSnapshotQueries) CreateMessageAsset(_ context.Context, arg sqlc.CreateMessageAssetParams) (sqlc.CreateMessageAssetRow, error) {
	q.createdAsset = arg
	return sqlc.CreateMessageAssetRow{}, q.assetErr
}

func (*runtimeSnapshotQueries) GetSessionByID(_ context.Context, id pgtype.UUID) (sqlc.BotSession, error) {
	return sqlc.BotSession{
		ID:          id,
		Type:        "acp_agent",
		SessionMode: "chat",
		RuntimeType: "acp_agent",
	}, nil
}

func (q *runtimeSnapshotQueries) CreateMessage(_ context.Context, arg sqlc.CreateMessageParams) (sqlc.CreateMessageRow, error) {
	q.created = arg
	return sqlc.CreateMessageRow{
		ID:          testMessageUUID("33333333-3333-3333-3333-333333333333"),
		BotID:       arg.BotID,
		SessionID:   arg.SessionID,
		Role:        arg.Role,
		Content:     arg.Content,
		Metadata:    arg.Metadata,
		Usage:       arg.Usage,
		SessionMode: arg.SessionMode,
		RuntimeType: arg.RuntimeType,
		DisplayText: arg.DisplayText,
		CreatedAt:   pgtype.Timestamptz{Valid: true},
	}, nil
}

func (*runtimeSnapshotQueries) CreateHistoryTurn(context.Context, sqlc.CreateHistoryTurnParams) (dbstore.HistoryTurn, error) {
	return dbstore.HistoryTurn{}, nil
}

func (*runtimeSnapshotQueries) AppendMessageToHistoryTurnByRequest(context.Context, sqlc.AppendMessageToHistoryTurnByRequestParams) (pgtype.UUID, error) {
	return pgtype.UUID{}, nil
}

func (*runtimeSnapshotQueries) BindHistoryTurnAssistantByRequest(context.Context, sqlc.BindHistoryTurnAssistantByRequestParams) (dbstore.HistoryTurn, error) {
	return dbstore.HistoryTurn{}, nil
}

func (*runtimeSnapshotQueries) BindLatestHistoryTurnAssistant(context.Context, sqlc.BindLatestHistoryTurnAssistantParams) (dbstore.HistoryTurn, error) {
	return dbstore.HistoryTurn{}, nil
}

func (*runtimeSnapshotQueries) GetLatestVisibleHistoryTurnBySession(context.Context, pgtype.UUID) (dbstore.HistoryTurn, error) {
	return dbstore.HistoryTurn{}, nil
}

func (*runtimeSnapshotQueries) LinkMessageToHistoryTurn(_ context.Context, arg sqlc.LinkMessageToHistoryTurnParams) (pgtype.UUID, error) {
	return arg.MessageID, nil
}

func (*runtimeSnapshotQueries) AppendMessageToLatestHistoryTurn(_ context.Context, arg sqlc.AppendMessageToLatestHistoryTurnParams) (pgtype.UUID, error) {
	return arg.MessageID, nil
}

func (*runtimeSnapshotQueries) LinkUnassignedMessagesAfterHistoryTurnAssistant(context.Context, pgtype.UUID) error {
	return nil
}

func TestPersistResolvesRuntimeSnapshotFromSession(t *testing.T) {
	queries := &runtimeSnapshotQueries{}
	svc := NewService(nil, queries)

	msg, err := svc.Persist(context.Background(), PersistInput{
		BotID:     "11111111-1111-1111-1111-111111111111",
		SessionID: "22222222-2222-2222-2222-222222222222",
		Role:      "user",
		Content:   []byte(`{"type":"text","text":"hello"}`),
	})
	if err != nil {
		t.Fatalf("Persist() error = %v", err)
	}

	if queries.created.SessionMode != "chat" || queries.created.RuntimeType != "acp_agent" {
		t.Fatalf("CreateMessage runtime snapshot = %q/%q, want chat/acp_agent", queries.created.SessionMode, queries.created.RuntimeType)
	}
	if msg.SessionMode != "chat" || msg.RuntimeType != "acp_agent" {
		t.Fatalf("message runtime snapshot = %q/%q, want chat/acp_agent", msg.SessionMode, msg.RuntimeType)
	}
}

func TestPersistPropagatesMessageAssetCreationFailure(t *testing.T) {
	queries := &runtimeSnapshotQueries{assetErr: errors.New("asset link failed")}
	svc := NewService(nil, queries)

	_, err := svc.Persist(context.Background(), PersistInput{
		BotID:     "11111111-1111-1111-1111-111111111111",
		SessionID: "22222222-2222-2222-2222-222222222222",
		Role:      "user",
		Content:   []byte(`{"type":"text","text":"hello"}`),
		Assets:    []AssetRef{{ContentHash: "sha256:asset", Role: "attachment"}},
	})
	if err == nil || !strings.Contains(err.Error(), "asset link failed") {
		t.Fatalf("Persist() error = %v, want asset creation failure", err)
	}
}

func TestPersistStoresMessageAssetMetadata(t *testing.T) {
	queries := &runtimeSnapshotQueries{}
	svc := NewService(nil, queries)

	msg, err := svc.Persist(context.Background(), PersistInput{
		BotID:     "11111111-1111-1111-1111-111111111111",
		SessionID: "22222222-2222-2222-2222-222222222222",
		Role:      "user",
		Content:   []byte(`{"type":"text","text":"hello"}`),
		Assets: []AssetRef{{
			ContentHash: "sha256:asset",
			Role:        "attachment",
			Mime:        " image/png ",
			SizeBytes:   42,
			StorageKey:  " ab/asset.png ",
			Name:        "asset.png",
		}},
	})
	if err != nil {
		t.Fatalf("Persist() error = %v", err)
	}
	if got := queries.createdAsset.Mime; got != "image/png" {
		t.Fatalf("persisted mime = %q, want image/png", got)
	}
	if got := queries.createdAsset.SizeBytes; got != 42 {
		t.Fatalf("persisted size_bytes = %d, want 42", got)
	}
	if got := queries.createdAsset.StorageKey; got != "ab/asset.png" {
		t.Fatalf("persisted storage_key = %q, want ab/asset.png", got)
	}
	if len(msg.Assets) != 1 || msg.Assets[0].Mime != "image/png" || msg.Assets[0].StorageKey != "ab/asset.png" {
		t.Fatalf("returned assets = %#v", msg.Assets)
	}
}

func TestLinkAssetsStoresMessageAssetMetadata(t *testing.T) {
	queries := &runtimeSnapshotQueries{}
	svc := NewService(nil, queries)

	err := svc.LinkAssets(context.Background(), "33333333-3333-3333-3333-333333333333", []AssetRef{{
		ContentHash: "sha256:asset",
		Mime:        " image/png ",
		SizeBytes:   42,
		StorageKey:  " ab/asset.png ",
		Name:        "asset.png",
	}})
	if err != nil {
		t.Fatalf("LinkAssets() error = %v", err)
	}
	if got := queries.createdAsset; got.Mime != "image/png" || got.SizeBytes != 42 || got.StorageKey != "ab/asset.png" {
		t.Fatalf("linked asset metadata = %#v", got)
	}
}

type assetReadQueries struct {
	dbstore.Queries
	rows []sqlc.ListMessageAssetsBatchRow
}

func (q *assetReadQueries) ListMessageAssetsBatch(context.Context, []pgtype.UUID) ([]sqlc.ListMessageAssetsBatchRow, error) {
	return q.rows, nil
}

func TestEnrichAssetsUsesPersistedMetadataWithoutStorage(t *testing.T) {
	messageID := testMessageUUID("33333333-3333-3333-3333-333333333333")
	queries := &assetReadQueries{rows: []sqlc.ListMessageAssetsBatchRow{
		{
			MessageID:   messageID,
			ContentHash: "sha256:new",
			Role:        "attachment",
			Mime:        "image/webp",
			SizeBytes:   99,
			StorageKey:  "aa/new.webp",
			Name:        "new.webp",
			Metadata:    []byte(`{}`),
		},
		{
			MessageID:   messageID,
			ContentHash: "sha256:legacy",
			Role:        "attachment",
			Name:        "legacy.png",
			Metadata:    []byte(`{"storage_key":"bb/legacy.png"}`),
		},
	}}
	svc := NewService(nil, queries)
	messages := []Message{{ID: messageID.String()}}

	svc.enrichAssets(context.Background(), messages)

	if len(messages[0].Assets) != 2 {
		t.Fatalf("assets length = %d, want 2", len(messages[0].Assets))
	}
	if got := messages[0].Assets[0]; got.Mime != "image/webp" || got.SizeBytes != 99 || got.StorageKey != "aa/new.webp" {
		t.Fatalf("persisted asset = %#v", got)
	}
	if got := messages[0].Assets[1]; got.Mime != "image/png" || got.StorageKey != "bb/legacy.png" {
		t.Fatalf("legacy asset = %#v", got)
	}
}

type clearHistoryQueries struct {
	dbstore.Queries
	botID     pgtype.UUID
	sessionID pgtype.UUID
}

func (q *clearHistoryQueries) ClearHistoryByBot(_ context.Context, id pgtype.UUID) error {
	q.botID = id
	return nil
}

func (q *clearHistoryQueries) ClearHistoryBySession(_ context.Context, id pgtype.UUID) error {
	q.sessionID = id
	return nil
}

func (*clearHistoryQueries) DeleteAgentSessionPublicationsBySession(context.Context, pgtype.UUID) (int64, error) {
	return 0, nil
}

func (*clearHistoryQueries) DeleteAgentSessionStatesBySession(context.Context, pgtype.UUID) (int64, error) {
	return 0, nil
}

func (*clearHistoryQueries) DeleteAgentSessionStateLinesBySession(context.Context, pgtype.UUID) (int64, error) {
	return 0, nil
}

func TestDeleteByScopeClearsCanonicalHistory(t *testing.T) {
	queries := &clearHistoryQueries{}
	svc := NewService(nil, queries)
	const botID = "11111111-1111-1111-1111-111111111111"
	const sessionID = "22222222-2222-2222-2222-222222222222"

	if err := svc.DeleteByBot(context.Background(), botID); err != nil {
		t.Fatalf("DeleteByBot() error = %v", err)
	}
	if err := svc.DeleteBySession(context.Background(), sessionID); err != nil {
		t.Fatalf("DeleteBySession() error = %v", err)
	}
	if queries.botID.String() != botID || queries.sessionID.String() != sessionID {
		t.Fatalf("cleared scopes = bot:%s session:%s", queries.botID.String(), queries.sessionID.String())
	}
}

type failingHistoryTurnQueries struct {
	dbstore.Queries

	deleted []pgtype.UUID
}

func (*failingHistoryTurnQueries) GetSessionByID(_ context.Context, id pgtype.UUID) (sqlc.BotSession, error) {
	return sqlc.BotSession{
		ID:          id,
		Type:        "chat",
		SessionMode: "chat",
		RuntimeType: "model",
	}, nil
}

func (*failingHistoryTurnQueries) CreateMessage(_ context.Context, arg sqlc.CreateMessageParams) (sqlc.CreateMessageRow, error) {
	return sqlc.CreateMessageRow{
		ID:          testMessageUUID("44444444-4444-4444-4444-444444444444"),
		BotID:       arg.BotID,
		SessionID:   arg.SessionID,
		Role:        arg.Role,
		Content:     arg.Content,
		Metadata:    arg.Metadata,
		Usage:       arg.Usage,
		SessionMode: arg.SessionMode,
		RuntimeType: arg.RuntimeType,
		DisplayText: arg.DisplayText,
		CreatedAt:   pgtype.Timestamptz{Valid: true},
	}, nil
}

func (*failingHistoryTurnQueries) CreateHistoryTurn(context.Context, sqlc.CreateHistoryTurnParams) (dbstore.HistoryTurn, error) {
	return dbstore.HistoryTurn{}, errors.New("boom")
}

func (*failingHistoryTurnQueries) AppendMessageToHistoryTurnByRequest(context.Context, sqlc.AppendMessageToHistoryTurnByRequestParams) (pgtype.UUID, error) {
	return pgtype.UUID{}, nil
}

func (*failingHistoryTurnQueries) BindHistoryTurnAssistantByRequest(context.Context, sqlc.BindHistoryTurnAssistantByRequestParams) (dbstore.HistoryTurn, error) {
	return dbstore.HistoryTurn{}, nil
}

func (*failingHistoryTurnQueries) BindLatestHistoryTurnAssistant(context.Context, sqlc.BindLatestHistoryTurnAssistantParams) (dbstore.HistoryTurn, error) {
	return dbstore.HistoryTurn{}, nil
}

func (*failingHistoryTurnQueries) GetLatestVisibleHistoryTurnBySession(context.Context, pgtype.UUID) (dbstore.HistoryTurn, error) {
	return dbstore.HistoryTurn{}, nil
}

func (*failingHistoryTurnQueries) LinkMessageToHistoryTurn(_ context.Context, arg sqlc.LinkMessageToHistoryTurnParams) (pgtype.UUID, error) {
	return arg.MessageID, nil
}

func (*failingHistoryTurnQueries) AppendMessageToLatestHistoryTurn(_ context.Context, arg sqlc.AppendMessageToLatestHistoryTurnParams) (pgtype.UUID, error) {
	return arg.MessageID, nil
}

func (*failingHistoryTurnQueries) LinkUnassignedMessagesAfterHistoryTurnAssistant(context.Context, pgtype.UUID) error {
	return nil
}

func (q *failingHistoryTurnQueries) DeleteMessagesByIDs(_ context.Context, ids []pgtype.UUID) error {
	q.deleted = append(q.deleted, ids...)
	return nil
}

type recordingPublisher struct {
	events []messageevent.Event
}

func (p *recordingPublisher) Publish(event messageevent.Event) {
	p.events = append(p.events, event)
}

func TestPersistCleansUpMessageWhenHistoryTurnFails(t *testing.T) {
	queries := &failingHistoryTurnQueries{}
	publisher := &recordingPublisher{}
	svc := NewService(nil, queries, publisher)

	_, err := svc.Persist(context.Background(), PersistInput{
		BotID:     "11111111-1111-1111-1111-111111111111",
		SessionID: "22222222-2222-2222-2222-222222222222",
		Role:      "user",
		Content:   []byte(`{"type":"text","text":"hello"}`),
	})
	if err == nil {
		t.Fatal("Persist() error = nil, want history turn failure")
	}
	if len(queries.deleted) != 1 {
		t.Fatalf("deleted messages = %d, want 1", len(queries.deleted))
	}
	if got := queries.deleted[0].String(); got != "44444444-4444-4444-4444-444444444444" {
		t.Fatalf("deleted message id = %s", got)
	}
	if len(publisher.events) != 0 {
		t.Fatalf("published events = %d, want 0", len(publisher.events))
	}
}

type retryTurnSequenceQueries struct {
	dbstore.Queries

	linkAttempts int
	deleted      []pgtype.UUID
}

func (*retryTurnSequenceQueries) GetSessionByID(_ context.Context, id pgtype.UUID) (sqlc.BotSession, error) {
	return sqlc.BotSession{
		ID:          id,
		Type:        "chat",
		SessionMode: "chat",
		RuntimeType: "model",
	}, nil
}

func (*retryTurnSequenceQueries) CreateMessage(_ context.Context, arg sqlc.CreateMessageParams) (sqlc.CreateMessageRow, error) {
	return sqlc.CreateMessageRow{
		ID:          testMessageUUID("55555555-5555-5555-5555-555555555555"),
		BotID:       arg.BotID,
		SessionID:   arg.SessionID,
		Role:        arg.Role,
		Content:     arg.Content,
		Metadata:    arg.Metadata,
		Usage:       arg.Usage,
		SessionMode: arg.SessionMode,
		RuntimeType: arg.RuntimeType,
		DisplayText: arg.DisplayText,
		CreatedAt:   pgtype.Timestamptz{Valid: true},
	}, nil
}

func (*retryTurnSequenceQueries) CreateHistoryTurn(_ context.Context, arg sqlc.CreateHistoryTurnParams) (dbstore.HistoryTurn, error) {
	return dbstore.HistoryTurn{
		ID:        testMessageUUID("66666666-6666-6666-6666-666666666666"),
		BotID:     arg.BotID,
		SessionID: arg.SessionID,
		Position:  1,
	}, nil
}

func (*retryTurnSequenceQueries) AppendMessageToHistoryTurnByRequest(context.Context, sqlc.AppendMessageToHistoryTurnByRequestParams) (pgtype.UUID, error) {
	return pgtype.UUID{}, nil
}

func (*retryTurnSequenceQueries) BindHistoryTurnAssistantByRequest(_ context.Context, arg sqlc.BindHistoryTurnAssistantByRequestParams) (dbstore.HistoryTurn, error) {
	return dbstore.HistoryTurn{
		ID:                 testMessageUUID("66666666-6666-6666-6666-666666666666"),
		SessionID:          arg.SessionID,
		RequestMessageID:   arg.RequestMessageID,
		AssistantMessageID: arg.AssistantMessageID,
		Position:           1,
	}, nil
}

func (*retryTurnSequenceQueries) BindLatestHistoryTurnAssistant(context.Context, sqlc.BindLatestHistoryTurnAssistantParams) (dbstore.HistoryTurn, error) {
	return dbstore.HistoryTurn{}, nil
}

func (*retryTurnSequenceQueries) GetLatestVisibleHistoryTurnBySession(context.Context, pgtype.UUID) (dbstore.HistoryTurn, error) {
	return dbstore.HistoryTurn{}, nil
}

func (q *retryTurnSequenceQueries) LinkMessageToHistoryTurn(_ context.Context, arg sqlc.LinkMessageToHistoryTurnParams) (pgtype.UUID, error) {
	q.linkAttempts++
	if q.linkAttempts == 1 {
		return pgtype.UUID{}, &pgconn.PgError{Code: "23505", ConstraintName: "idx_bot_history_messages_turn_seq_unique"}
	}
	return arg.MessageID, nil
}

func (*retryTurnSequenceQueries) AppendMessageToLatestHistoryTurn(_ context.Context, arg sqlc.AppendMessageToLatestHistoryTurnParams) (pgtype.UUID, error) {
	return arg.MessageID, nil
}

func (*retryTurnSequenceQueries) LinkUnassignedMessagesAfterHistoryTurnAssistant(context.Context, pgtype.UUID) error {
	return nil
}

func (q *retryTurnSequenceQueries) DeleteMessagesByIDs(_ context.Context, ids []pgtype.UUID) error {
	q.deleted = append(q.deleted, ids...)
	return nil
}

func TestPersistRetriesTurnSequenceUniqueViolation(t *testing.T) {
	queries := &retryTurnSequenceQueries{}
	publisher := &recordingPublisher{}
	svc := NewService(nil, queries, publisher)

	msg, err := svc.Persist(context.Background(), PersistInput{
		BotID:                "11111111-1111-1111-1111-111111111111",
		SessionID:            "22222222-2222-2222-2222-222222222222",
		Role:                 "assistant",
		Content:              []byte(`{"type":"text","text":"hello"}`),
		TurnRequestMessageID: "77777777-7777-7777-7777-777777777777",
	})
	if err != nil {
		t.Fatalf("Persist() error = %v", err)
	}
	if msg.ID != "55555555-5555-5555-5555-555555555555" {
		t.Fatalf("message id = %s", msg.ID)
	}
	if queries.linkAttempts != 2 {
		t.Fatalf("link attempts = %d, want 2", queries.linkAttempts)
	}
	if len(queries.deleted) != 1 {
		t.Fatalf("deleted messages = %d, want 1", len(queries.deleted))
	}
	if len(publisher.events) != 1 {
		t.Fatalf("published events = %d, want 1", len(publisher.events))
	}
}

func testMessageUUID(value string) pgtype.UUID {
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		panic(err)
	}
	return id
}
