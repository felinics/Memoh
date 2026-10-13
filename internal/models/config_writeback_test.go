package models

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
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
