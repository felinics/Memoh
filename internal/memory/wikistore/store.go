// Package wikistore provides a graph store over the memory_nodes /
// memory_edges tables. It defines the Store interface in terms of plain Go
// POJOs (migrate.NodeSpec / EdgeSpec) so that the graph runtime never touches
// sqlc or driver-specific types. The PostgreSQL implementation wraps the
// sqlc-generated queries.
package wikistore

import (
	"context"
	"time"

	"github.com/felinics/memoh/internal/memory/migrate"
)

// Store is the backend-agnostic contract over the memory wiki tables. All
// methods are scoped to a single bot_id. NodeSpec/EdgeSpec come from the
// migrate package and carry no driver-specific types.
type Store interface {
	// UpsertNode inserts or updates a single node by id (idempotent on conflict).
	UpsertNode(ctx context.Context, node migrate.NodeSpec) (migrate.NodeSpec, error)
	// GetNode returns one node by id, or ErrNodeNotFound.
	GetNode(ctx context.Context, botID, nodeID string) (migrate.NodeSpec, error)
	// ListNodes returns every node for a bot in captured_at ascending order.
	ListNodes(ctx context.Context, botID string) ([]migrate.NodeSpec, error)
	// ListNodesByLayer returns nodes for a bot filtered by layer.
	ListNodesByLayer(ctx context.Context, botID string, layer migrate.MemoryLayer) ([]migrate.NodeSpec, error)
	// DeleteNode removes a node (and its incident edges via ON DELETE CASCADE
	// at the SQL level for bot-scoped rows, plus explicit edge cleanup here).
	DeleteNode(ctx context.Context, botID, nodeID string) error
	// DeleteAllNodes removes every node for a bot (edges removed by the caller
	// or DB cascade).
	DeleteAllNodes(ctx context.Context, botID string) error
	// CountNodes returns the node count for a bot.
	CountNodes(ctx context.Context, botID string) (int, error)

	// UpsertEdges inserts or updates (on conflict) a batch of edges.
	UpsertEdges(ctx context.Context, edges []migrate.EdgeSpec) error
	// ListEdges returns every edge for a bot.
	ListEdges(ctx context.Context, botID string) ([]migrate.EdgeSpec, error)
	// DeleteEdgesForNode removes all edges incident to a node (src or dst).
	DeleteEdgesForNode(ctx context.Context, botID, nodeID string) error
	// DeleteAllEdges removes every edge for a bot.
	DeleteAllEdges(ctx context.Context, botID string) error
	// CountEdges returns the edge count for a bot.
	CountEdges(ctx context.Context, botID string) (int, error)

	// RebuildDerivedEdges recomputes same_profile / same_topic / same_day / refs
	// edges for a bot from its current nodes, replacing any prior derived edges.
	// Returns the number of edges written.
	RebuildDerivedEdges(ctx context.Context, botID string) (int, error)
}

// ErrNodeNotFound is returned by GetNode when no node matches the id.
var ErrNodeNotFound = errNodeNotFound{}

type errNodeNotFound struct{}

func (errNodeNotFound) Error() string { return "wikistore: node not found" }

// Is lets errors.Is match this sentinel across the two backends (which may
// wrap it differently).
func (errNodeNotFound) Is(target error) bool {
	_, ok := target.(errNodeNotFound)
	return ok
}

// record is the canonical struct returned by Read operations; it mirrors
// migrate.NodeSpec but is internal so callers depend on migrate.* types only.
type record struct {
	ID               string
	BotID            string
	Body             string
	Hash             string
	Layer            string
	FactType         string
	Subject          string
	Confidence       float32
	Metadata         map[string]any
	SourceMessageIDs []string
	ProfileRef       string
	Topic            string
	CapturedAt       time.Time
	ExpiresAt        time.Time
}
