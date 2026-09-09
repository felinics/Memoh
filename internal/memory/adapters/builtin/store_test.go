package builtin

import (
	"context"
	"sort"
	"strings"

	storefs "github.com/felinics/memoh/internal/memory/storefs"
)

// fakeStore is an in-memory implementation of the memoryStore interface used by
// the file runtime tests in this package.
type fakeStore struct {
	items map[string]storefs.MemoryItem
}

func newFakeStore(items ...storefs.MemoryItem) *fakeStore {
	store := &fakeStore{items: map[string]storefs.MemoryItem{}}
	for _, item := range items {
		store.items[item.ID] = item
	}
	return store
}

func (s *fakeStore) PersistMemories(_ context.Context, _ string, items []storefs.MemoryItem, _ map[string]any) error {
	for _, item := range items {
		s.items[item.ID] = item
	}
	return nil
}

func (s *fakeStore) ReadAllMemoryFiles(_ context.Context, _ string) ([]storefs.MemoryItem, error) {
	out := make([]storefs.MemoryItem, 0, len(s.items))
	for _, item := range s.items {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *fakeStore) ReadAllMemoryFilesForIngest(_ context.Context, _ string) ([]storefs.MemoryItem, error) {
	// In tests the ingest path is identical to the read path (the fake serves
	// the same in-memory items either way); duplicate the read body so the
	// contextcheck linter does not flag a synthetic context.
	out := make([]storefs.MemoryItem, 0, len(s.items))
	for _, item := range s.items {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *fakeStore) RemoveMemories(_ context.Context, _ string, ids []string) error {
	for _, id := range ids {
		delete(s.items, strings.TrimSpace(id))
	}
	return nil
}

func (s *fakeStore) RemoveAllMemories(_ context.Context, _ string) error {
	s.items = map[string]storefs.MemoryItem{}
	return nil
}

func (s *fakeStore) RebuildFiles(_ context.Context, _ string, items []storefs.MemoryItem, _ map[string]any) error {
	s.items = map[string]storefs.MemoryItem{}
	for _, item := range items {
		s.items[item.ID] = item
	}
	return nil
}

func (*fakeStore) SyncOverview(context.Context, string) error { return nil }

func (s *fakeStore) CountMemoryFiles(_ context.Context, _ string) (int, error) {
	if len(s.items) == 0 {
		return 0, nil
	}
	return 1, nil
}

// Compile-time assertion that fakeStore satisfies the memoryStore interface.
var _ memoryStore = (*fakeStore)(nil)
