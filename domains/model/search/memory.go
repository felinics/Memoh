package search

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	searchport "github.com/memohai/memoh/domains/model/internal/port/search"
)

type memoryStore struct {
	mu   sync.Mutex
	byID map[string]searchport.ProviderRecord
}

func newMemoryStore() *memoryStore {
	return &memoryStore{byID: make(map[string]searchport.ProviderRecord)}
}

func (s *memoryStore) CreateProvider(_ context.Context, value searchport.ProviderWrite) (searchport.ProviderRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	row := searchport.ProviderRecord{
		ID:        uuid.NewString(),
		Name:      value.Name,
		Provider:  value.Provider,
		Config:    append(json.RawMessage(nil), value.Config...),
		Enable:    value.Enable,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.byID[row.ID] = row
	return cloneRecord(row), nil
}

func (s *memoryStore) FindProvider(_ context.Context, id string) (searchport.ProviderRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.byID[id]
	if !ok {
		return searchport.ProviderRecord{}, fmt.Errorf("search provider %q not found", id)
	}
	return cloneRecord(row), nil
}

func (s *memoryStore) ListProviders(_ context.Context, provider string) ([]searchport.ProviderRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]searchport.ProviderRecord, 0, len(s.byID))
	for _, row := range s.byID {
		if provider != "" && row.Provider != provider {
			continue
		}
		out = append(out, cloneRecord(row))
	}
	return out, nil
}

func (s *memoryStore) UpdateProvider(_ context.Context, value searchport.ProviderWrite) (searchport.ProviderRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.byID[value.ID]
	if !ok {
		return searchport.ProviderRecord{}, fmt.Errorf("search provider %q not found", value.ID)
	}
	row := searchport.ProviderRecord{
		ID:        value.ID,
		Name:      value.Name,
		Provider:  value.Provider,
		Config:    append(json.RawMessage(nil), value.Config...),
		Enable:    value.Enable,
		CreatedAt: current.CreatedAt,
		UpdatedAt: time.Now().UTC(),
	}
	s.byID[row.ID] = row
	return cloneRecord(row), nil
}

func (s *memoryStore) DeleteProvider(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[id]; !ok {
		return fmt.Errorf("search provider %q not found", id)
	}
	delete(s.byID, id)
	return nil
}

func cloneRecord(row searchport.ProviderRecord) searchport.ProviderRecord {
	row.Config = append(json.RawMessage(nil), row.Config...)
	return row
}
