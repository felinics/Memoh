package thread

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type subagentStore struct {
	fakeThreadStore
	inTx          bool
	sessionCreate bool
	configCreate  bool
	contextCreate bool
	context       ForkContextRecord
	failConfig    bool
}

func (s *subagentStore) InTransaction(_ context.Context, fn func(Store) error) error {
	s.inTx = true
	err := fn(s)
	if err != nil {
		s.sessionCreate = false
		s.configCreate = false
		s.contextCreate = false
	}
	return err
}

func (s *subagentStore) CreateThread(ctx context.Context, record CreateRecord) (Thread, error) {
	s.sessionCreate = true
	return s.fakeThreadStore.CreateThread(ctx, record)
}

func (s *subagentStore) CreateSubagentConfig(_ context.Context, record SubagentConfigRecord) (SubagentConfig, error) {
	if s.failConfig {
		return SubagentConfig{}, errors.New("config insert failed")
	}
	s.configCreate = true
	now := time.Unix(1, 0).UTC()
	return SubagentConfig{
		ThreadID: record.ThreadID, ModelUUID: record.ModelUUID, ModelID: record.ModelID,
		ProviderName: record.ProviderName, Forked: record.Forked, CreatedAt: now, UpdatedAt: now,
	}, nil
}

func (s *subagentStore) CreateSubagentForkContext(_ context.Context, record ForkContextRecord) (int64, error) {
	s.contextCreate = true
	s.context = record
	return int64(len(record.Messages)), nil
}

func TestCreateSubagentPersistsSessionAndConfigInOneTransaction(t *testing.T) {
	store := &subagentStore{}
	svc := NewService(nil, store, nil, nil)
	session, config, err := svc.CreateSubagent(t.Context(), CreateSubagentInput{
		Thread: CreateInput{
			BotID:           "00000000-0000-0000-0000-000000000502",
			ParentThreadID:  "00000000-0000-0000-0000-000000000503",
			CreatedByUserID: "00000000-0000-0000-0000-000000000504",
			Title:           "worker", Metadata: map[string]any{"agent_id": "worker"},
		},
		ModelUUID: "00000000-0000-0000-0000-000000000505",
		ModelID:   "worker-model", ProviderName: "provider-a", Forked: true,
		ForkContext: []SubagentForkContextMessage{{
			Role: "user", Message: []byte(`{"role":"user","content":[]}`),
		}},
	})
	if err != nil {
		t.Fatalf("CreateSubagent() error = %v", err)
	}
	if !store.inTx || !store.sessionCreate || !store.configCreate || !store.contextCreate {
		t.Fatalf("transaction state = %+v", store)
	}
	if session.Type != TypeSubagent || config.ModelID != "worker-model" || !config.Forked {
		t.Fatalf("session=%+v config=%+v", session, config)
	}
}

func TestCreateSubagentPersistsEmptyForkContext(t *testing.T) {
	store := &subagentStore{}
	_, config, err := NewService(nil, store, nil, nil).CreateSubagent(t.Context(), CreateSubagentInput{
		Thread: CreateInput{
			BotID:          "00000000-0000-0000-0000-000000000501",
			ParentThreadID: "00000000-0000-0000-0000-000000000502",
		},
		ModelUUID: "00000000-0000-0000-0000-000000000505",
		ModelID:   "worker-model", ProviderName: "provider-a", Forked: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(store.context.Messages)
	if !store.contextCreate || string(data) != "[]" || !config.Forked {
		t.Fatalf("context=%s config=%+v", data, config)
	}
}

func TestCreateSubagentRollsBackSessionWhenConfigFails(t *testing.T) {
	store := &subagentStore{failConfig: true}
	_, _, err := NewService(nil, store, nil, nil).CreateSubagent(t.Context(), CreateSubagentInput{
		Thread: CreateInput{
			BotID:          "00000000-0000-0000-0000-000000000502",
			ParentThreadID: "00000000-0000-0000-0000-000000000503",
		},
		ModelUUID: "00000000-0000-0000-0000-000000000505",
		ModelID:   "worker-model", ProviderName: "provider-a",
	})
	if err == nil || !strings.Contains(err.Error(), "config insert failed") {
		t.Fatalf("error = %v", err)
	}
	if store.sessionCreate || store.configCreate {
		t.Fatalf("transaction did not roll back: %+v", store)
	}
}
