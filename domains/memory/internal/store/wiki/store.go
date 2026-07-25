// Package wiki defines the persistence contract consumed by the memory
// graph runtime.
package wiki

import (
	"context"

	memorydomain "github.com/memohai/memoh/domains/memory"
)

// Reader is the read side of the memory wiki persistence contract.
type Reader interface {
	// GetNode returns one node by id, or ErrNodeNotFound.
	GetNode(ctx context.Context, botID, nodeID string) (memorydomain.NodeSpec, error)
	// ListNodes returns every node for a bot in captured_at ascending order.
	ListNodes(ctx context.Context, botID string) ([]memorydomain.NodeSpec, error)
	// ListNodesByLayer returns nodes for a bot filtered by layer.
	ListNodesByLayer(ctx context.Context, botID string, layer memorydomain.Layer) ([]memorydomain.NodeSpec, error)
	// CountNodes returns the node count for a bot.
	CountNodes(ctx context.Context, botID string) (int, error)
	// ListEdges returns every edge for a bot.
	ListEdges(ctx context.Context, botID string) ([]memorydomain.EdgeSpec, error)
	// CountEdges returns the edge count for a bot.
	CountEdges(ctx context.Context, botID string) (int, error)
}

// Writer is the write side of the memory wiki persistence contract.
type Writer interface {
	// UpsertNode inserts or updates a single node by id (idempotent on conflict).
	UpsertNode(ctx context.Context, node memorydomain.NodeSpec) (memorydomain.NodeSpec, error)
	// DeleteNode removes a node (and its incident edges via ON DELETE CASCADE
	// at the SQL level for bot-scoped rows, plus explicit edge cleanup here).
	DeleteNode(ctx context.Context, botID, nodeID string) error
	// DeleteAllNodes removes every node for a bot (edges removed by the caller
	// or DB cascade).
	DeleteAllNodes(ctx context.Context, botID string) error
	// UpsertEdges inserts or updates (on conflict) a batch of edges.
	UpsertEdges(ctx context.Context, edges []memorydomain.EdgeSpec) error
	// DeleteEdgesForNode removes all edges incident to a node (src or dst).
	DeleteEdgesForNode(ctx context.Context, botID, nodeID string) error
	// DeleteAllEdges removes every edge for a bot.
	DeleteAllEdges(ctx context.Context, botID string) error
	// RebuildDerivedEdges recomputes same_profile / same_topic / same_day / refs
	// edges for a bot from its current nodes, replacing any prior derived edges.
	// Returns the number of edges written.
	RebuildDerivedEdges(ctx context.Context, botID string) (int, error)
}

// Store is the complete persistence contract used by the graph runtime.
type Store interface {
	Reader
	Writer
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
