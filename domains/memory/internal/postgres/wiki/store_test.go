package wiki

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	memorydomain "github.com/memohai/memoh/domains/memory"
	dbsqlc "github.com/memohai/memoh/domains/memory/internal/postgres/sqlc"
	wikistore "github.com/memohai/memoh/domains/memory/internal/store/wiki"
)

const testBotID = "5bc71f07-6db7-4591-906a-a81a17072aef"

type recordingQueries struct {
	upsertNodeArg dbsqlc.UpsertMemoryNodeParams
	upsertNodeRow dbsqlc.MemoryMemoryNode
	upsertNodeErr error
	getNodeRow    dbsqlc.MemoryMemoryNode
	getNodeErr    error
	listNodeRows  []dbsqlc.MemoryMemoryNode
	listNodeErr   error
	listNodeCalls int
	listLayerArg  dbsqlc.ListMemoryNodesByBotLayerParams
	listLayerRows []dbsqlc.MemoryMemoryNode
	listEdgeRows  []dbsqlc.MemoryMemoryEdge
	insertEdges   []dbsqlc.InsertMemoryEdgeParams
	insertEdgeErr error
	deleteRels    []dbsqlc.DeleteMemoryEdgesByRelForBotParams
	deleteRelErr  error
	nodeCount     int64
	edgeCount     int64
}

func (q *recordingQueries) CountMemoryEdgesByBot(context.Context, pgtype.UUID) (int64, error) {
	return q.edgeCount, nil
}

func (q *recordingQueries) CountMemoryNodesByBot(context.Context, pgtype.UUID) (int64, error) {
	return q.nodeCount, nil
}

func (*recordingQueries) DeleteAllMemoryEdgesByBot(context.Context, pgtype.UUID) error {
	return nil
}

func (*recordingQueries) DeleteAllMemoryNodesByBot(context.Context, pgtype.UUID) error {
	return nil
}

func (q *recordingQueries) DeleteMemoryEdgesByRelForBot(_ context.Context, arg dbsqlc.DeleteMemoryEdgesByRelForBotParams) error {
	q.deleteRels = append(q.deleteRels, arg)
	return q.deleteRelErr
}

func (*recordingQueries) DeleteMemoryEdgesForNode(context.Context, dbsqlc.DeleteMemoryEdgesForNodeParams) error {
	return nil
}

func (*recordingQueries) DeleteMemoryNode(context.Context, dbsqlc.DeleteMemoryNodeParams) error {
	return nil
}

func (q *recordingQueries) GetMemoryNode(context.Context, dbsqlc.GetMemoryNodeParams) (dbsqlc.MemoryMemoryNode, error) {
	return q.getNodeRow, q.getNodeErr
}

func (q *recordingQueries) InsertMemoryEdge(_ context.Context, arg dbsqlc.InsertMemoryEdgeParams) error {
	q.insertEdges = append(q.insertEdges, arg)
	return q.insertEdgeErr
}

func (q *recordingQueries) ListMemoryEdgesByBot(context.Context, pgtype.UUID) ([]dbsqlc.MemoryMemoryEdge, error) {
	return q.listEdgeRows, nil
}

func (q *recordingQueries) ListMemoryNodesByBot(context.Context, pgtype.UUID) ([]dbsqlc.MemoryMemoryNode, error) {
	q.listNodeCalls++
	return q.listNodeRows, q.listNodeErr
}

func (q *recordingQueries) ListMemoryNodesByBotLayer(_ context.Context, arg dbsqlc.ListMemoryNodesByBotLayerParams) ([]dbsqlc.MemoryMemoryNode, error) {
	q.listLayerArg = arg
	return q.listLayerRows, nil
}

func (q *recordingQueries) UpsertMemoryNode(_ context.Context, arg dbsqlc.UpsertMemoryNodeParams) (dbsqlc.MemoryMemoryNode, error) {
	q.upsertNodeArg = arg
	return q.upsertNodeRow, q.upsertNodeErr
}

func TestStoreMapsNodeFieldsWithoutDriverTypesAtContract(t *testing.T) {
	t.Parallel()

	botID := mustUUID(t, testBotID)
	capturedAt := time.Date(2026, time.July, 23, 11, 12, 13, 0, time.FixedZone("JST", 9*60*60))
	q := &recordingQueries{upsertNodeRow: dbsqlc.MemoryMemoryNode{
		ID:               "node-1",
		BotID:            botID,
		Body:             "remember this",
		Hash:             "hash-1",
		Layer:            string(memorydomain.LayerNote),
		FactType:         "preference",
		Subject:          "tea",
		Confidence:       0.5,
		Metadata:         []byte(`{"source":"chat"}`),
		SourceMessageIds: []byte(`["message-1"]`),
		ProfileRef:       "profile-1",
		Topic:            "drinks",
		CapturedAt:       pgtype.Timestamptz{Time: capturedAt, Valid: true},
	}}
	store := newStore(q, nil, nil)

	got, err := store.UpsertNode(t.Context(), memorydomain.NodeSpec{
		ID:               "node-1",
		BotID:            testBotID,
		Body:             "remember this",
		Hash:             "hash-1",
		Layer:            " ",
		FactType:         "preference",
		Subject:          "tea",
		Confidence:       2,
		Metadata:         map[string]any{"source": "chat"},
		SourceMessageIDs: []string{"message-1"},
		ProfileRef:       "profile-1",
		Topic:            "drinks",
		CapturedAt:       capturedAt,
	})
	if err != nil {
		t.Fatalf("UpsertNode() error = %v", err)
	}
	arg := q.upsertNodeArg
	if arg.BotID != botID || arg.Layer != string(memorydomain.LayerNote) || arg.Confidence != 0.5 {
		t.Fatalf("UpsertMemoryNode normalized params = %+v", arg)
	}
	if string(arg.Metadata) != `{"source":"chat"}` || string(arg.SourceMessageIds) != `["message-1"]` {
		t.Fatalf("UpsertMemoryNode JSON params = metadata %s, source IDs %s", arg.Metadata, arg.SourceMessageIds)
	}
	if !arg.CapturedAt.Valid || !arg.CapturedAt.Time.Equal(capturedAt) || arg.ExpiresAt.Valid {
		t.Fatalf("UpsertMemoryNode timestamps = captured %+v, expires %+v", arg.CapturedAt, arg.ExpiresAt)
	}
	if got.BotID != testBotID || got.Layer != memorydomain.LayerNote || got.Metadata["source"] != "chat" {
		t.Fatalf("UpsertNode() = %+v", got)
	}
	if len(got.SourceMessageIDs) != 1 || got.SourceMessageIDs[0] != "message-1" {
		t.Fatalf("UpsertNode() source message IDs = %v", got.SourceMessageIDs)
	}
	if !got.CapturedAt.Equal(capturedAt.UTC()) || !got.ExpiresAt.IsZero() {
		t.Fatalf("UpsertNode() timestamps = %v, %v", got.CapturedAt, got.ExpiresAt)
	}
}

func TestStorePreservesQueryOrderingAndNullMapping(t *testing.T) {
	t.Parallel()

	botID := mustUUID(t, testBotID)
	q := &recordingQueries{
		listNodeRows: []dbsqlc.MemoryMemoryNode{
			{ID: "first", BotID: botID, Metadata: []byte(`{}`), SourceMessageIds: []byte(`[]`)},
			{ID: "second", BotID: botID, Metadata: []byte(`not-json`), SourceMessageIds: []byte(`not-json`)},
		},
		listLayerRows: []dbsqlc.MemoryMemoryNode{{ID: "layer-node", BotID: botID}},
		listEdgeRows: []dbsqlc.MemoryMemoryEdge{
			{BotID: botID, SrcNode: "first", DstNode: "second", Rel: string(memorydomain.EdgeRefs), Metadata: []byte(`{}`)},
			{BotID: botID, SrcNode: "second", DstNode: "first", Rel: string(memorydomain.EdgeSameTopic), Metadata: []byte(`not-json`)},
		},
	}
	store := newStore(q, nil, nil)

	nodes, err := store.ListNodes(t.Context(), testBotID)
	if err != nil {
		t.Fatalf("ListNodes() error = %v", err)
	}
	if len(nodes) != 2 || nodes[0].ID != "first" || nodes[1].ID != "second" {
		t.Fatalf("ListNodes() order = %v", nodeIDs(nodes))
	}
	if nodes[0].Metadata != nil || nodes[0].SourceMessageIDs == nil || len(nodes[0].SourceMessageIDs) != 0 {
		t.Fatalf("ListNodes() empty JSON mapping = %+v", nodes[0])
	}
	if nodes[1].Metadata != nil || nodes[1].SourceMessageIDs != nil {
		t.Fatalf("ListNodes() invalid JSON mapping = %+v", nodes[1])
	}
	for _, node := range nodes {
		if !node.CapturedAt.IsZero() || !node.ExpiresAt.IsZero() {
			t.Fatalf("ListNodes() null timestamp mapping for %s = %+v", node.ID, node)
		}
	}
	if _, err := store.ListNodesByLayer(t.Context(), testBotID, " "); err != nil {
		t.Fatalf("ListNodesByLayer() error = %v", err)
	}
	if q.listLayerArg.Layer != string(memorydomain.LayerNote) {
		t.Fatalf("ListNodesByLayer() layer = %q", q.listLayerArg.Layer)
	}
	edges, err := store.ListEdges(t.Context(), testBotID)
	if err != nil {
		t.Fatalf("ListEdges() error = %v", err)
	}
	if len(edges) != 2 || edges[0].Rel != memorydomain.EdgeRefs || edges[1].Rel != memorydomain.EdgeSameTopic {
		t.Fatalf("ListEdges() order = %+v", edges)
	}
	if edges[0].Metadata != nil || edges[1].Metadata != nil {
		t.Fatalf("ListEdges() null metadata mapping = %+v", edges)
	}
}

func TestStoreMapsNoRowsToDomainError(t *testing.T) {
	t.Parallel()

	store := newStore(&recordingQueries{getNodeErr: fmt.Errorf("select memory node: %w", pgx.ErrNoRows)}, nil, nil)
	_, err := store.GetNode(t.Context(), testBotID, "missing")
	if !errors.Is(err, wikistore.ErrNodeNotFound) {
		t.Fatalf("GetNode() error = %v, want ErrNodeNotFound", err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetNode() leaked pgx.ErrNoRows: %v", err)
	}
}

func TestStoreRebuildsOnlyDerivedEdges(t *testing.T) {
	t.Parallel()

	botID := mustUUID(t, testBotID)
	capturedAt := time.Date(2026, time.July, 23, 0, 0, 0, 0, time.UTC)
	txQueries := &recordingQueries{listNodeRows: []dbsqlc.MemoryMemoryNode{
		{ID: "first", BotID: botID, ProfileRef: "profile-1", CapturedAt: pgtype.Timestamptz{Time: capturedAt, Valid: true}},
		{ID: "second", BotID: botID, ProfileRef: "profile-1", CapturedAt: pgtype.Timestamptz{Time: capturedAt, Valid: true}},
	}}
	tx := &transactionStub{}
	beginner := &transactionBeginnerStub{tx: tx}
	baseQueries := &recordingQueries{}
	store := newStore(baseQueries, beginner, func(got pgx.Tx) queries {
		if got != tx {
			t.Fatalf("transaction query binding got %p, want %p", got, tx)
		}
		return txQueries
	})

	count, err := store.RebuildDerivedEdges(t.Context(), testBotID)
	if err != nil {
		t.Fatalf("RebuildDerivedEdges() error = %v", err)
	}
	if beginner.beginCalls != 1 || tx.commitCalls != 1 || tx.rollbackCalls != 1 {
		t.Fatalf("transaction calls = begin:%d commit:%d rollback:%d, want 1/1/1", beginner.beginCalls, tx.commitCalls, tx.rollbackCalls)
	}
	if len(baseQueries.deleteRels) != 0 || baseQueries.listNodeCalls != 0 || len(baseQueries.insertEdges) != 0 {
		t.Fatalf("outer queries used during transaction: deletes=%d lists=%d inserts=%d", len(baseQueries.deleteRels), baseQueries.listNodeCalls, len(baseQueries.insertEdges))
	}
	if len(txQueries.deleteRels) != len(memorydomain.DerivedEdgeRels) {
		t.Fatalf("DeleteMemoryEdgesByRelForBot calls = %d, want %d", len(txQueries.deleteRels), len(memorydomain.DerivedEdgeRels))
	}
	for i, rel := range memorydomain.DerivedEdgeRels {
		if txQueries.deleteRels[i].Rel != string(rel) || txQueries.deleteRels[i].BotID != botID {
			t.Fatalf("DeleteMemoryEdgesByRelForBot call %d = %+v", i, txQueries.deleteRels[i])
		}
	}
	if count != 2 || len(txQueries.insertEdges) != 2 {
		t.Fatalf("RebuildDerivedEdges() count = %d, inserts = %+v", count, txQueries.insertEdges)
	}
	rels := map[string]bool{}
	for _, arg := range txQueries.insertEdges {
		rels[arg.Rel] = true
	}
	if !rels[string(memorydomain.EdgeSameProfile)] || !rels[string(memorydomain.EdgeSameDay)] {
		t.Fatalf("RebuildDerivedEdges() rels = %v", rels)
	}
}

func TestStoreRebuildDerivedEdgesFailsBeforeStatementsWithoutTransaction(t *testing.T) {
	t.Parallel()

	q := &recordingQueries{}
	store := newStore(q, nil, nil)
	_, err := store.RebuildDerivedEdges(t.Context(), testBotID)
	if !errors.Is(err, ErrTransactionBeginnerRequired) {
		t.Fatalf("RebuildDerivedEdges() error = %v, want %v", err, ErrTransactionBeginnerRequired)
	}
	if len(q.deleteRels) != 0 || q.listNodeCalls != 0 || len(q.insertEdges) != 0 {
		t.Fatalf("statements executed without transaction: deletes=%d lists=%d inserts=%d", len(q.deleteRels), q.listNodeCalls, len(q.insertEdges))
	}
}

func TestStoreRebuildDerivedEdgesRollsBackFailures(t *testing.T) {
	t.Parallel()

	failure := errors.New("forced transaction statement failure")
	tests := []struct {
		name    string
		queries *recordingQueries
	}{
		{name: "delete", queries: &recordingQueries{deleteRelErr: failure}},
		{name: "list", queries: &recordingQueries{listNodeErr: failure}},
		{
			name: "insert",
			queries: &recordingQueries{
				listNodeRows:  derivedTestNodes(t),
				insertEdgeErr: failure,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := &transactionStub{}
			beginner := &transactionBeginnerStub{tx: tx}
			store := newStore(&recordingQueries{}, beginner, func(pgx.Tx) queries { return tt.queries })

			_, err := store.RebuildDerivedEdges(t.Context(), testBotID)
			if !errors.Is(err, failure) {
				t.Fatalf("RebuildDerivedEdges() error = %v, want %v", err, failure)
			}
			if beginner.beginCalls != 1 || tx.commitCalls != 0 || tx.rollbackCalls != 1 {
				t.Fatalf("transaction calls = begin:%d commit:%d rollback:%d, want 1/0/1", beginner.beginCalls, tx.commitCalls, tx.rollbackCalls)
			}
		})
	}
}

func TestStoreRebuildDerivedEdgesDoesNotRunStatementsWhenBeginFails(t *testing.T) {
	t.Parallel()

	beginErr := errors.New("begin failed")
	q := &recordingQueries{}
	beginner := &transactionBeginnerStub{beginErr: beginErr}
	store := newStore(q, beginner, func(pgx.Tx) queries {
		t.Fatal("transaction query factory called after Begin failure")
		return nil
	})

	_, err := store.RebuildDerivedEdges(t.Context(), testBotID)
	if !errors.Is(err, beginErr) {
		t.Fatalf("RebuildDerivedEdges() error = %v, want %v", err, beginErr)
	}
	if beginner.beginCalls != 1 || len(q.deleteRels) != 0 || q.listNodeCalls != 0 || len(q.insertEdges) != 0 {
		t.Fatalf("begin/statements = %d/%d/%d/%d, want 1/0/0/0", beginner.beginCalls, len(q.deleteRels), q.listNodeCalls, len(q.insertEdges))
	}
}

func TestNewStoreBindsGeneratedQueriesToBegunTransaction(t *testing.T) {
	t.Parallel()

	outer := &transactionStub{execErr: errors.New("outer queries must not execute")}
	statementErr := errors.New("transaction delete failed")
	tx := &transactionStub{execErr: statementErr}
	beginner := &transactionBeginnerStub{tx: tx}
	store := NewStore(dbsqlc.New(outer), beginner)

	_, err := store.RebuildDerivedEdges(t.Context(), testBotID)
	if !errors.Is(err, statementErr) {
		t.Fatalf("RebuildDerivedEdges() error = %v, want %v", err, statementErr)
	}
	if outer.execCalls != 0 || tx.execCalls != 1 {
		t.Fatalf("Exec() calls = outer:%d tx:%d, want 0/1", outer.execCalls, tx.execCalls)
	}
	if tx.commitCalls != 0 || tx.rollbackCalls != 1 {
		t.Fatalf("transaction completion = commit:%d rollback:%d, want 0/1", tx.commitCalls, tx.rollbackCalls)
	}
}

func TestStoreRequiresQueries(t *testing.T) {
	t.Parallel()

	store := NewStore(nil, nil)
	if _, err := store.ListNodes(t.Context(), testBotID); err == nil {
		t.Fatal("ListNodes() error = nil, want missing queries error")
	}
	if err := store.UpsertEdges(t.Context(), nil); err == nil {
		t.Fatal("UpsertEdges() error = nil, want missing queries error")
	}
}

type transactionBeginnerStub struct {
	tx         pgx.Tx
	beginErr   error
	beginCalls int
}

func (b *transactionBeginnerStub) Begin(context.Context) (pgx.Tx, error) {
	b.beginCalls++
	return b.tx, b.beginErr
}

type transactionStub struct {
	pgx.Tx
	execErr       error
	execCalls     int
	commitErr     error
	commitCalls   int
	rollbackCalls int
}

func (tx *transactionStub) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	tx.execCalls++
	return pgconn.CommandTag{}, tx.execErr
}

func (tx *transactionStub) Commit(context.Context) error {
	tx.commitCalls++
	return tx.commitErr
}

func (tx *transactionStub) Rollback(context.Context) error {
	tx.rollbackCalls++
	return nil
}

func derivedTestNodes(t *testing.T) []dbsqlc.MemoryMemoryNode {
	t.Helper()
	botID := mustUUID(t, testBotID)
	capturedAt := time.Date(2026, time.July, 23, 0, 0, 0, 0, time.UTC)
	return []dbsqlc.MemoryMemoryNode{
		{ID: "first", BotID: botID, ProfileRef: "profile-1", CapturedAt: pgtype.Timestamptz{Time: capturedAt, Valid: true}},
		{ID: "second", BotID: botID, ProfileRef: "profile-1", CapturedAt: pgtype.Timestamptz{Time: capturedAt, Valid: true}},
	}
}

func mustUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		t.Fatalf("scan UUID %q: %v", value, err)
	}
	return id
}

func nodeIDs(nodes []memorydomain.NodeSpec) []string {
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.ID)
	}
	return ids
}
