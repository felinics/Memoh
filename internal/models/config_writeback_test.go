package models

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/reasoning"
)

type writeBackQueries struct {
	dbstore.Queries
	writes []sqlc.UpgradeModelConfigParams
}

func (q *writeBackQueries) UpgradeModelConfig(_ context.Context, arg sqlc.UpgradeModelConfigParams) (int64, error) {
	q.writes = append(q.writes, arg)
	return 1, nil
}

// Reading a row stored in an older schema version writes the upgraded config
// back, conditioned on the exact config that was read.
func TestReadWritesBackUpgradedConfig(t *testing.T) {
	t.Parallel()

	legacy := []byte(`{"compatibilities":["vision","tool-call","reasoning"],"thinking_mode":"toggle","reasoning_efforts":["disable","low","high"],"context_window":400000,"vendor_note":"kept"}`)
	queries := &writeBackQueries{}
	service := &Service{queries: queries, logger: slog.Default()}

	resp := service.convertToGetResponse(context.Background(), sqlc.Model{ModelID: "m", Type: string(ModelTypeChat), Config: legacy})
	if !resp.Config.ToolCall || resp.Config.Reasoning == nil {
		t.Fatalf("response config = %+v", resp.Config)
	}
	if len(queries.writes) != 1 {
		t.Fatalf("writes = %d, want 1", len(queries.writes))
	}
	write := queries.writes[0]
	if string(write.PreviousConfig) != string(legacy) {
		t.Fatalf("previous config = %s, want the config that was read", write.PreviousConfig)
	}
	var stored map[string]any
	if err := json.Unmarshal(write.Config, &stored); err != nil {
		t.Fatal(err)
	}
	if stored["schema_version"] != float64(ModelConfigSchemaVersion) || stored["tool_call"] != true || stored["vendor_note"] != "kept" {
		t.Fatalf("written config = %s", write.Config)
	}
	for _, key := range legacyOnlyKeys {
		if _, ok := stored[key]; ok {
			t.Fatalf("written config kept version 1 key %q: %s", key, write.Config)
		}
	}
	again, err := DecodeModelConfig(write.Config, ConfigContext{ModelID: "m", Type: ModelTypeChat})
	if err != nil || again.ThinkingMode() != resp.Config.ThinkingMode() {
		t.Fatalf("written config decodes to %+v, %v", again, err)
	}
}

func TestReadLeavesCurrentAndForeignConfigsAlone(t *testing.T) {
	t.Parallel()

	current, err := json.Marshal(ModelConfig{ToolCall: true})
	if err != nil {
		t.Fatal(err)
	}
	queries := &writeBackQueries{}
	service := &Service{queries: queries, logger: slog.Default()}
	for _, row := range []sqlc.Model{
		{ModelID: "current", Type: string(ModelTypeChat), Config: current},
		{ModelID: "voice", Type: string(ModelTypeSpeech), Config: []byte(`{"voice":"alloy"}`)},
		{ModelID: "empty", Type: string(ModelTypeChat)},
	} {
		service.convertToGetResponse(context.Background(), row)
	}
	if len(queries.writes) != 0 {
		t.Fatalf("writes = %+v, want none", queries.writes)
	}
}

type providerLookupQueries struct {
	writeBackQueries
	err error
}

func (q *providerLookupQueries) GetProviderByID(context.Context, pgtype.UUID) (sqlc.Provider, error) {
	if q.err != nil {
		return sqlc.Provider{}, q.err
	}
	return sqlc.Provider{ClientType: string(ClientTypeOpenAICompletions)}, nil
}

// A switch advertised by the enabled token alone upgrades differently per
// protocol. When the provider cannot be read the upgrade is lossy: it is served
// for this read but not written back, so the next read tries again instead of
// storing a can-disable toggle for good.
func TestReadSkipsWriteBackWhenTheProviderLookupFails(t *testing.T) {
	t.Parallel()

	row := sqlc.Model{
		ModelID:    "m",
		Type:       string(ModelTypeChat),
		Config:     []byte(`{"thinking_mode":"toggle","reasoning_efforts":["enabled"]}`),
		ProviderID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
	}
	failing := &providerLookupQueries{err: errors.New("provider unavailable")}
	service := &Service{queries: failing, logger: slog.Default()}
	resp := service.convertToGetResponse(context.Background(), row)
	if resp.Config.Reasoning == nil || !resp.Config.Reasoning.Toggle || resp.Config.Reasoning.Off != "" {
		t.Fatalf("served config = %+v, want the switch with its off answer unstated", resp.Config.Reasoning)
	}
	if len(failing.writes) != 0 {
		t.Fatal("a lossy upgrade was written back")
	}

	working := &providerLookupQueries{}
	service = &Service{queries: working, logger: slog.Default()}
	resp = service.convertToGetResponse(context.Background(), row)
	if resp.Config.Reasoning == nil || resp.Config.Reasoning.Off != reasoning.OffSupportRejected {
		t.Fatalf("served config = %+v, want off rejected on OpenAI", resp.Config.Reasoning)
	}
	if len(working.writes) != 1 {
		t.Fatalf("writes = %d, want the upgrade written back once the provider is known", len(working.writes))
	}
}

type createQueries struct {
	providerLookupQueries
	created []sqlc.CreateModelParams
}

func (q *createQueries) CreateModel(_ context.Context, arg sqlc.CreateModelParams) (sqlc.Model, error) {
	q.created = append(q.created, arg)
	return sqlc.Model{ModelID: arg.ModelID}, nil
}

// A request decode cannot see the provider, so a version 1 switch an older
// client sends is read again with the provider's client type before it is
// stored; otherwise the switch would be stored with its off answer unstated for
// good.
func TestCreateReadsAnOlderClientsSwitchWithTheProviderClientType(t *testing.T) {
	t.Parallel()

	var req AddRequest
	if err := json.Unmarshal([]byte(`{"model_id":"m","provider_id":"00000000-0000-0000-0000-000000000001","type":"chat",`+
		`"config":{"compatibilities":["reasoning"],"thinking_mode":"toggle","reasoning_efforts":["enabled"]}}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.Config.Reasoning == nil || req.Config.Reasoning.Off != "" {
		t.Fatalf("decoded without the provider: %+v", req.Config.Reasoning)
	}
	queries := &createQueries{}
	service := &Service{queries: queries, logger: slog.Default()}
	if _, err := service.Create(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(queries.created) != 1 {
		t.Fatalf("created = %d, want 1", len(queries.created))
	}
	stored, err := DecodeModelConfig(queries.created[0].Config, ConfigContext{ModelID: "m", Type: ModelTypeChat})
	if err != nil {
		t.Fatal(err)
	}
	if stored.Reasoning == nil || stored.Reasoning.Off != reasoning.OffSupportRejected {
		t.Fatalf("stored reasoning = %+v, want off rejected on OpenAI", stored.Reasoning)
	}
}
