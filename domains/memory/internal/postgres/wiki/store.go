// Package wiki implements Memory wiki persistence over PostgreSQL.
package wiki

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	memorydomain "github.com/memohai/memoh/domains/memory"
	dbsqlc "github.com/memohai/memoh/domains/memory/internal/postgres/sqlc"
	wikistore "github.com/memohai/memoh/domains/memory/internal/store/wiki"

	"github.com/memohai/memoh/internal/db"
)

type queries interface {
	CountMemoryEdgesByBot(context.Context, pgtype.UUID) (int64, error)
	CountMemoryNodesByBot(context.Context, pgtype.UUID) (int64, error)
	DeleteAllMemoryEdgesByBot(context.Context, pgtype.UUID) error
	DeleteAllMemoryNodesByBot(context.Context, pgtype.UUID) error
	DeleteMemoryEdgesByRelForBot(context.Context, dbsqlc.DeleteMemoryEdgesByRelForBotParams) error
	DeleteMemoryEdgesForNode(context.Context, dbsqlc.DeleteMemoryEdgesForNodeParams) error
	DeleteMemoryNode(context.Context, dbsqlc.DeleteMemoryNodeParams) error
	GetMemoryNode(context.Context, dbsqlc.GetMemoryNodeParams) (dbsqlc.MemoryMemoryNode, error)
	InsertMemoryEdge(context.Context, dbsqlc.InsertMemoryEdgeParams) error
	ListMemoryEdgesByBot(context.Context, pgtype.UUID) ([]dbsqlc.MemoryMemoryEdge, error)
	ListMemoryNodesByBot(context.Context, pgtype.UUID) ([]dbsqlc.MemoryMemoryNode, error)
	ListMemoryNodesByBotLayer(context.Context, dbsqlc.ListMemoryNodesByBotLayerParams) ([]dbsqlc.MemoryMemoryNode, error)
	UpsertMemoryNode(context.Context, dbsqlc.UpsertMemoryNodeParams) (dbsqlc.MemoryMemoryNode, error)
}

type transactionBeginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// ErrTransactionBeginnerRequired reports that an atomic wiki operation was
// attempted with a query-only adapter.
var ErrTransactionBeginnerRequired = errors.New("wikistore(postgres): transaction beginner is required")

// Store adapts owner-generated statements to the Memory wiki contract.
type Store struct {
	queries            queries
	transactions       transactionBeginner
	transactionQueries func(pgx.Tx) queries
}

var _ wikistore.Store = (*Store)(nil)

func NewStore(q *dbsqlc.Queries, transactions transactionBeginner) *Store {
	if q == nil {
		return &Store{transactions: transactions}
	}
	return &Store{
		queries:      q,
		transactions: transactions,
		transactionQueries: func(tx pgx.Tx) queries {
			return q.WithTx(tx)
		},
	}
}

// NewStoreFromPool constructs a wiki store from a PostgreSQL pool.
func NewStoreFromPool(pool *pgxpool.Pool) *Store {
	if pool == nil {
		return nil
	}
	return NewStore(dbsqlc.New(pool), pool)
}

func newStore(q queries, transactions transactionBeginner, transactionQueries func(pgx.Tx) queries) *Store {
	return &Store{
		queries:            q,
		transactions:       transactions,
		transactionQueries: transactionQueries,
	}
}

func (s *Store) UpsertNode(ctx context.Context, node memorydomain.NodeSpec) (memorydomain.NodeSpec, error) {
	if s.queries == nil {
		return memorydomain.NodeSpec{}, errors.New("wikistore(postgres): queries not configured")
	}
	row, err := s.queries.UpsertMemoryNode(ctx, dbsqlc.UpsertMemoryNodeParams{
		ID:               node.ID,
		BotID:            pgUUID(node.BotID),
		Body:             node.Body,
		Hash:             node.Hash,
		Layer:            string(defaultLayer(node.Layer)),
		FactType:         node.FactType,
		Subject:          node.Subject,
		Confidence:       normalizedConfidence(node.Confidence),
		Metadata:         marshalJSON(node.Metadata),
		SourceMessageIds: marshalStringList(node.SourceMessageIDs),
		ProfileRef:       node.ProfileRef,
		Topic:            node.Topic,
		CapturedAt:       pgTimestamptz(node.CapturedAt),
		ExpiresAt:        pgTimestamptz(node.ExpiresAt),
	})
	if err != nil {
		return memorydomain.NodeSpec{}, fmt.Errorf("wikistore(postgres): upsert node: %w", err)
	}
	return nodeSpec(row), nil
}

func (s *Store) GetNode(ctx context.Context, botID, nodeID string) (memorydomain.NodeSpec, error) {
	if s.queries == nil {
		return memorydomain.NodeSpec{}, errors.New("wikistore(postgres): queries not configured")
	}
	row, err := s.queries.GetMemoryNode(ctx, dbsqlc.GetMemoryNodeParams{BotID: pgUUID(botID), ID: nodeID})
	if errors.Is(err, pgx.ErrNoRows) {
		return memorydomain.NodeSpec{}, wikistore.ErrNodeNotFound
	}
	if err != nil {
		return memorydomain.NodeSpec{}, fmt.Errorf("wikistore(postgres): get node: %w", err)
	}
	return nodeSpec(row), nil
}

func (s *Store) ListNodes(ctx context.Context, botID string) ([]memorydomain.NodeSpec, error) {
	if s.queries == nil {
		return nil, errors.New("wikistore(postgres): queries not configured")
	}
	rows, err := s.queries.ListMemoryNodesByBot(ctx, pgUUID(botID))
	if err != nil {
		return nil, fmt.Errorf("wikistore(postgres): list nodes: %w", err)
	}
	return nodeSpecs(rows), nil
}

func (s *Store) ListNodesByLayer(ctx context.Context, botID string, layer memorydomain.Layer) ([]memorydomain.NodeSpec, error) {
	if s.queries == nil {
		return nil, errors.New("wikistore(postgres): queries not configured")
	}
	rows, err := s.queries.ListMemoryNodesByBotLayer(ctx, dbsqlc.ListMemoryNodesByBotLayerParams{
		BotID: pgUUID(botID),
		Layer: string(defaultLayer(layer)),
	})
	if err != nil {
		return nil, fmt.Errorf("wikistore(postgres): list nodes by layer: %w", err)
	}
	return nodeSpecs(rows), nil
}

func (s *Store) DeleteNode(ctx context.Context, botID, nodeID string) error {
	if s.queries == nil {
		return errors.New("wikistore(postgres): queries not configured")
	}
	if err := s.queries.DeleteMemoryEdgesForNode(ctx, dbsqlc.DeleteMemoryEdgesForNodeParams{BotID: pgUUID(botID), SrcNode: nodeID}); err != nil {
		return fmt.Errorf("wikistore(postgres): delete node edges: %w", err)
	}
	if err := s.queries.DeleteMemoryNode(ctx, dbsqlc.DeleteMemoryNodeParams{BotID: pgUUID(botID), ID: nodeID}); err != nil {
		return fmt.Errorf("wikistore(postgres): delete node: %w", err)
	}
	return nil
}

func (s *Store) DeleteAllNodes(ctx context.Context, botID string) error {
	if s.queries == nil {
		return errors.New("wikistore(postgres): queries not configured")
	}
	if err := s.queries.DeleteAllMemoryEdgesByBot(ctx, pgUUID(botID)); err != nil {
		return fmt.Errorf("wikistore(postgres): delete all edges: %w", err)
	}
	if err := s.queries.DeleteAllMemoryNodesByBot(ctx, pgUUID(botID)); err != nil {
		return fmt.Errorf("wikistore(postgres): delete all nodes: %w", err)
	}
	return nil
}

func (s *Store) CountNodes(ctx context.Context, botID string) (int, error) {
	if s.queries == nil {
		return 0, errors.New("wikistore(postgres): queries not configured")
	}
	count, err := s.queries.CountMemoryNodesByBot(ctx, pgUUID(botID))
	if err != nil {
		return 0, fmt.Errorf("wikistore(postgres): count nodes: %w", err)
	}
	return int(count), nil
}

func (s *Store) UpsertEdges(ctx context.Context, edges []memorydomain.EdgeSpec) error {
	if s.queries == nil {
		return errors.New("wikistore(postgres): queries not configured")
	}
	for _, edge := range edges {
		if err := s.queries.InsertMemoryEdge(ctx, dbsqlc.InsertMemoryEdgeParams{
			BotID:    pgUUID(edge.BotID),
			SrcNode:  edge.SrcNode,
			DstNode:  edge.DstNode,
			Rel:      string(edge.Rel),
			Weight:   edge.Weight,
			Metadata: marshalJSON(edge.Metadata),
		}); err != nil {
			return fmt.Errorf("wikistore(postgres): upsert edge: %w", err)
		}
	}
	return nil
}

func (s *Store) ListEdges(ctx context.Context, botID string) ([]memorydomain.EdgeSpec, error) {
	if s.queries == nil {
		return nil, errors.New("wikistore(postgres): queries not configured")
	}
	rows, err := s.queries.ListMemoryEdgesByBot(ctx, pgUUID(botID))
	if err != nil {
		return nil, fmt.Errorf("wikistore(postgres): list edges: %w", err)
	}
	edges := make([]memorydomain.EdgeSpec, 0, len(rows))
	for _, row := range rows {
		edges = append(edges, edgeSpec(row))
	}
	return edges, nil
}

func (s *Store) DeleteEdgesForNode(ctx context.Context, botID, nodeID string) error {
	if s.queries == nil {
		return errors.New("wikistore(postgres): queries not configured")
	}
	if err := s.queries.DeleteMemoryEdgesForNode(ctx, dbsqlc.DeleteMemoryEdgesForNodeParams{BotID: pgUUID(botID), SrcNode: nodeID}); err != nil {
		return fmt.Errorf("wikistore(postgres): delete edges for node: %w", err)
	}
	return nil
}

func (s *Store) DeleteAllEdges(ctx context.Context, botID string) error {
	if s.queries == nil {
		return errors.New("wikistore(postgres): queries not configured")
	}
	if err := s.queries.DeleteAllMemoryEdgesByBot(ctx, pgUUID(botID)); err != nil {
		return fmt.Errorf("wikistore(postgres): delete all edges: %w", err)
	}
	return nil
}

func (s *Store) CountEdges(ctx context.Context, botID string) (int, error) {
	if s.queries == nil {
		return 0, errors.New("wikistore(postgres): queries not configured")
	}
	count, err := s.queries.CountMemoryEdgesByBot(ctx, pgUUID(botID))
	if err != nil {
		return 0, fmt.Errorf("wikistore(postgres): count edges: %w", err)
	}
	return int(count), nil
}

func (s *Store) RebuildDerivedEdges(ctx context.Context, botID string) (int, error) {
	if s.queries == nil {
		return 0, errors.New("wikistore(postgres): queries not configured")
	}
	if s.transactions == nil || s.transactionQueries == nil {
		return 0, ErrTransactionBeginnerRequired
	}
	tx, err := s.transactions.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("wikistore(postgres): begin derived edge rebuild: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	txQueries := s.transactionQueries(tx)
	if txQueries == nil {
		return 0, errors.New("wikistore(postgres): transaction queries not configured")
	}
	count, err := rebuildDerivedEdges(ctx, txQueries, botID)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("wikistore(postgres): commit derived edge rebuild: %w", err)
	}
	return count, nil
}

func rebuildDerivedEdges(ctx context.Context, q queries, botID string) (int, error) {
	for _, rel := range memorydomain.DerivedEdgeRels {
		if err := q.DeleteMemoryEdgesByRelForBot(ctx, dbsqlc.DeleteMemoryEdgesByRelForBotParams{
			BotID: pgUUID(botID),
			Rel:   string(rel),
		}); err != nil {
			return 0, fmt.Errorf("wikistore(postgres): clear derived edges: %w", err)
		}
	}
	rows, err := q.ListMemoryNodesByBot(ctx, pgUUID(botID))
	if err != nil {
		return 0, fmt.Errorf("wikistore(postgres): list nodes: %w", err)
	}
	derived := derivedEdges(memorydomain.PlanFromNodes(nodeSpecs(rows)))
	for _, edge := range derived {
		if err := q.InsertMemoryEdge(ctx, dbsqlc.InsertMemoryEdgeParams{
			BotID:    pgUUID(edge.BotID),
			SrcNode:  edge.SrcNode,
			DstNode:  edge.DstNode,
			Rel:      string(edge.Rel),
			Weight:   edge.Weight,
			Metadata: marshalJSON(edge.Metadata),
		}); err != nil {
			return 0, fmt.Errorf("wikistore(postgres): upsert edge: %w", err)
		}
	}
	return len(derived), nil
}

func nodeSpecs(rows []dbsqlc.MemoryMemoryNode) []memorydomain.NodeSpec {
	nodes := make([]memorydomain.NodeSpec, 0, len(rows))
	for _, row := range rows {
		nodes = append(nodes, nodeSpec(row))
	}
	return nodes
}

func nodeSpec(row dbsqlc.MemoryMemoryNode) memorydomain.NodeSpec {
	return memorydomain.NodeSpec{
		ID:               row.ID,
		BotID:            db.UUIDString(row.BotID),
		Body:             row.Body,
		Hash:             row.Hash,
		Layer:            memorydomain.Layer(row.Layer),
		FactType:         row.FactType,
		Subject:          row.Subject,
		Confidence:       row.Confidence,
		Metadata:         unmarshalMetadata(row.Metadata),
		SourceMessageIDs: unmarshalStringList(row.SourceMessageIds),
		ProfileRef:       row.ProfileRef,
		Topic:            row.Topic,
		CapturedAt:       timeValue(row.CapturedAt),
		ExpiresAt:        timeValue(row.ExpiresAt),
	}
}

func edgeSpec(row dbsqlc.MemoryMemoryEdge) memorydomain.EdgeSpec {
	return memorydomain.EdgeSpec{
		BotID:    db.UUIDString(row.BotID),
		SrcNode:  row.SrcNode,
		DstNode:  row.DstNode,
		Rel:      memorydomain.EdgeRel(row.Rel),
		Weight:   row.Weight,
		Metadata: unmarshalMetadata(row.Metadata),
	}
}

func defaultLayer(layer memorydomain.Layer) memorydomain.Layer {
	if layer = memorydomain.Layer(strings.TrimSpace(string(layer))); layer == "" {
		return memorydomain.LayerNote
	}
	return layer
}

func normalizedConfidence(confidence float32) float32 {
	if confidence < 0 || confidence > 1 {
		return 0.5
	}
	return confidence
}

func marshalJSON(value map[string]any) []byte {
	if len(value) == 0 {
		return []byte("{}")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte("{}")
	}
	return encoded
}

func unmarshalMetadata(value []byte) map[string]any {
	if len(value) == 0 {
		return nil
	}
	var metadata map[string]any
	if err := json.Unmarshal(value, &metadata); err != nil || len(metadata) == 0 {
		return nil
	}
	return metadata
}

func marshalStringList(values []string) []byte {
	if len(values) == 0 {
		return []byte("[]")
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return []byte("[]")
	}
	return encoded
}

func unmarshalStringList(value []byte) []string {
	if len(value) == 0 {
		return nil
	}
	var values []string
	if err := json.Unmarshal(value, &values); err != nil {
		return nil
	}
	return values
}

func derivedEdges(edges []memorydomain.EdgeSpec) []memorydomain.EdgeSpec {
	if len(edges) == 0 {
		return nil
	}
	allowed := make(map[memorydomain.EdgeRel]struct{}, len(memorydomain.DerivedEdgeRels))
	for _, rel := range memorydomain.DerivedEdgeRels {
		allowed[rel] = struct{}{}
	}
	derived := make([]memorydomain.EdgeSpec, 0, len(edges))
	for _, edge := range edges {
		if _, ok := allowed[edge.Rel]; ok {
			derived = append(derived, edge)
		}
	}
	return derived
}

func pgUUID(value string) pgtype.UUID {
	var id pgtype.UUID
	_ = id.Scan(value)
	return id
}

func pgTimestamptz(value time.Time) pgtype.Timestamptz {
	if value.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}

func timeValue(value pgtype.Timestamptz) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time.UTC()
}
