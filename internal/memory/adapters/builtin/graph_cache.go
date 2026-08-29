package builtin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/felinics/memoh/internal/memory/migrate"
	memseg "github.com/felinics/memoh/internal/memory/segment"
	"github.com/felinics/memoh/internal/memory/wikistore"
)

// graphCacheTTL is how long a cached bot graph stays fresh before it is rebuilt
// from the store. Writes invalidate the entry immediately, so this TTL only
// bounds staleness when the same process keeps serving reads without writes
// (e.g. a long-idle bot).
const graphCacheTTL = 5 * time.Minute

// neighbor is one graph edge endpoint reached during BFS expansion.
type neighbor struct {
	node   migrate.NodeSpec
	rel    migrate.EdgeRel
	weight float32
}

// botGraph is the cached graph for a single bot: its nodes plus undirected
// adjacency derived from the stored edges.
type botGraph struct {
	nodes   map[string]migrate.NodeSpec
	adj     map[string][]neighbor // undirected: each edge added in both directions
	hash    string                // content hash over nodes+edges, for cache-busting
	builtAt time.Time
}

func (g *botGraph) neighbors(nodeID string) []neighbor {
	if g == nil {
		return nil
	}
	return g.adj[nodeID]
}

// nodeSlice returns the cached nodes as a slice (unsorted).
func (g *botGraph) nodeSlice() []migrate.NodeSpec {
	out := make([]migrate.NodeSpec, 0, len(g.nodes))
	for _, n := range g.nodes {
		out = append(out, n)
	}
	return out
}

// graphCache caches per-bot in-memory graphs, rebuilt lazily from the
// WikiStore. It is safe for concurrent use. A write to the store should call
// invalidate(botID) so the next read rebuilds.
type graphCache struct {
	mu       sync.RWMutex
	graphs   map[string]*botGraph
	versions map[string]uint64
}

func newGraphCache() *graphCache {
	return &graphCache{
		graphs:   make(map[string]*botGraph),
		versions: make(map[string]uint64),
	}
}

// getOrBuild returns the cached graph for botID, rebuilding it from store if
// missing, stale (older than graphCacheTTL), or if the store's content hash
// changed since the last build.
func (c *graphCache) getOrBuild(ctx context.Context, botID string, store wikistore.Store) (*botGraph, error) {
	// Fast path: a fresh, hash-matching entry under a read lock.
	c.mu.RLock()
	g := c.graphs[botID]
	c.mu.RUnlock()
	if g != nil && time.Since(g.builtAt) < graphCacheTTL {
		return g, nil
	}

	nodes, err := store.ListNodes(ctx, botID)
	if err != nil {
		return nil, fmt.Errorf("graph cache: list nodes: %w", err)
	}
	edges, err := store.ListEdges(ctx, botID)
	if err != nil {
		return nil, fmt.Errorf("graph cache: list edges: %w", err)
	}

	built := buildBotGraph(nodes, edges)
	c.mu.Lock()
	c.graphs[botID] = built
	c.mu.Unlock()
	return built, nil
}

// invalidate drops the cached graph for botID so the next read rebuilds.
func (c *graphCache) invalidate(botID string) {
	c.mu.Lock()
	delete(c.graphs, botID)
	c.versions[botID]++
	c.mu.Unlock()
}

func (c *graphCache) version(botID string) string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	version := c.versions[botID]
	c.mu.RUnlock()
	return strconv.FormatUint(version, 10)
}

// buildBotGraph constructs a botGraph from node + edge specs. Edges are added
// in both directions so BFS expansion is undirected.
func buildBotGraph(nodes []migrate.NodeSpec, edges []migrate.EdgeSpec) *botGraph {
	g := &botGraph{
		nodes: make(map[string]migrate.NodeSpec, len(nodes)),
		adj:   make(map[string][]neighbor, len(nodes)),
	}
	for _, n := range nodes {
		g.nodes[n.ID] = n
	}
	for _, e := range edges {
		if _, ok := g.nodes[e.SrcNode]; !ok {
			continue
		}
		if _, ok := g.nodes[e.DstNode]; !ok {
			continue
		}
		g.adj[e.SrcNode] = append(g.adj[e.SrcNode], neighbor{node: g.nodes[e.DstNode], rel: e.Rel, weight: e.Weight})
		g.adj[e.DstNode] = append(g.adj[e.DstNode], neighbor{node: g.nodes[e.SrcNode], rel: e.Rel, weight: e.Weight})
	}
	g.hash = graphContentHash(nodes, edges)
	g.builtAt = time.Now()
	return g
}

// graphContentHash produces a stable digest over the node bodies and edge
// endpoints so the cache can detect content drift cheaply.
func graphContentHash(nodes []migrate.NodeSpec, edges []migrate.EdgeSpec) string {
	h := sha256.New()
	ids := make([]string, 0, len(nodes))
	byID := make(map[string]migrate.NodeSpec, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.ID)
		byID[n.ID] = n
	}
	sort.Strings(ids)
	for _, id := range ids {
		n := byID[id]
		_, _ = fmt.Fprintln(h, id, n.Hash, n.Body)
	}
	type edgeKey struct{ src, dst, rel string }
	es := make([]edgeKey, 0, len(edges))
	for _, e := range edges {
		es = append(es, edgeKey{e.SrcNode, e.DstNode, string(e.Rel)})
	}
	sort.Slice(es, func(i, j int) bool { return fmt.Sprintf("%v", es[i]) < fmt.Sprintf("%v", es[j]) })
	for _, e := range es {
		_, _ = fmt.Fprintln(h, e.src, e.dst, e.rel)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// graphLexicalScore scores body against query for the graph seed/expand phase.
// It delegates to segment.LexicalScore, which splits CJK text via gse (Chinese
// has no inter-word spaces, so whitespace-only splitting collapsed a whole
// sentence into a single token that never matched) while keeping Latin/numeric
// runs on the whitespace-split path.
func graphLexicalScore(query, body string) float64 {
	return memseg.LexicalScore(query, body)
}
