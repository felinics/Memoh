package builtin

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/memohai/memoh/internal/memory/migrate"
	"github.com/memohai/memoh/internal/memory/wikistore"
)

func TestGraphCacheDoesNotInstallBuildInvalidatedDuringStoreRead(t *testing.T) {
	t.Parallel()

	store := &blockingGraphStore{
		nodes:        []migrate.NodeSpec{{ID: "old"}},
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	cache := newGraphCache()
	result := make(chan *botGraph, 1)
	errCh := make(chan error, 1)
	go func() {
		graph, err := cache.getOrBuild(context.Background(), "bot-1", store)
		if err != nil {
			errCh <- err
			return
		}
		result <- graph
	}()

	select {
	case <-store.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first graph build did not start")
	}
	store.setNodes([]migrate.NodeSpec{{ID: "new"}})
	cache.invalidate("bot-1")
	close(store.releaseFirst)

	var graph *botGraph
	select {
	case err := <-errCh:
		t.Fatalf("getOrBuild failed: %v", err)
	case graph = <-result:
	case <-time.After(time.Second):
		t.Fatal("graph build did not finish")
	}
	if _, ok := graph.nodes["new"]; !ok {
		t.Fatalf("graph nodes = %#v, want post-invalidation snapshot", graph.nodes)
	}
	if _, ok := graph.nodes["old"]; ok {
		t.Fatalf("stale node was installed after invalidation: %#v", graph.nodes)
	}
	if got := store.callCount(); got != 2 {
		t.Fatalf("ListNodes calls = %d, want rebuild after invalidation", got)
	}
}

type blockingGraphStore struct {
	wikistore.Store

	mu           sync.Mutex
	nodes        []migrate.NodeSpec
	calls        int
	firstStarted chan struct{}
	releaseFirst chan struct{}
}

func (s *blockingGraphStore) ListNodes(ctx context.Context, _ string) ([]migrate.NodeSpec, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	nodes := append([]migrate.NodeSpec(nil), s.nodes...)
	if call == 1 {
		close(s.firstStarted)
	}
	s.mu.Unlock()

	if call == 1 {
		select {
		case <-s.releaseFirst:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nodes, nil
}

func (*blockingGraphStore) ListEdges(context.Context, string) ([]migrate.EdgeSpec, error) {
	return nil, nil
}

func (s *blockingGraphStore) setNodes(nodes []migrate.NodeSpec) {
	s.mu.Lock()
	s.nodes = append([]migrate.NodeSpec(nil), nodes...)
	s.mu.Unlock()
}

func (s *blockingGraphStore) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}
