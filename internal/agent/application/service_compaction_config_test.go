package application

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/internal/agent/context/compaction"
	"github.com/memohai/memoh/internal/db"
	"github.com/memohai/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/memohai/memoh/internal/db/store"
	"github.com/memohai/memoh/internal/models"
	"github.com/memohai/memoh/internal/settings"
)

type compactionConfigQueries struct {
	dbstore.Queries
	model    sqlc.Model
	provider sqlc.Provider
}

func (q *compactionConfigQueries) GetModelByID(context.Context, pgtype.UUID) (sqlc.Model, error) {
	return q.model, nil
}

func (q *compactionConfigQueries) GetProviderByID(context.Context, pgtype.UUID) (sqlc.Provider, error) {
	return q.provider, nil
}

func compactionConfigUUID(t *testing.T, id string) pgtype.UUID {
	t.Helper()
	parsed, err := db.ParseUUID(id)
	if err != nil {
		t.Fatalf("ParseUUID(%q): %v", id, err)
	}
	return parsed
}

func TestBuildCompactionConfigKeepsRatioSelection(t *testing.T) {
	t.Parallel()

	const modelUUID = "00000000-0000-0000-0000-000000000401"
	const providerUUID = "00000000-0000-0000-0000-000000000402"

	queries := &compactionConfigQueries{
		model: sqlc.Model{
			ID:         compactionConfigUUID(t, modelUUID),
			ModelID:    "compact-model",
			ProviderID: compactionConfigUUID(t, providerUUID),
			Type:       "chat",
			Enable:     true,
			Config:     []byte(`{"context_window":200000}`),
		},
		provider: sqlc.Provider{
			ID:         compactionConfigUUID(t, providerUUID),
			Name:       "test-provider",
			ClientType: "openai-completions",
			Enable:     true,
			Config:     []byte(`{"api_key":"test-key"}`),
		},
	}
	r := &Service{
		logger:        slog.New(slog.DiscardHandler),
		modelsService: models.NewService(slog.New(slog.DiscardHandler), queries),
		queries:       queries,
	}

	cfg, err := r.buildCompactionConfig(context.Background(), ChatRequest{
		BotID:    "00000000-0000-0000-0000-000000000403",
		ThreadID: "00000000-0000-0000-0000-000000000404",
	}, settings.Settings{
		CompactionModelID: modelUUID,
		CompactionRatio:   60,
	}, 150000)
	if err != nil {
		t.Fatalf("buildCompactionConfig: %v", err)
	}

	if cfg.TargetTokens != 0 {
		t.Fatalf("TargetTokens = %d, want 0 so ratio-based selection applies", cfg.TargetTokens)
	}
	if cfg.Ratio != 60 {
		t.Fatalf("Ratio = %d, want 60", cfg.Ratio)
	}
	if cfg.TotalInputTokens != 150000 {
		t.Fatalf("TotalInputTokens = %d, want 150000", cfg.TotalInputTokens)
	}
	if cfg.MaxCompactTokens != 180000 {
		t.Fatalf("MaxCompactTokens = %d, want 180000 (90%% of context window)", cfg.MaxCompactTokens)
	}
	if cfg.ModelContextTokens != 200000 {
		t.Fatalf("ModelContextTokens = %d, want 200000", cfg.ModelContextTokens)
	}
}

func TestEffectiveCompactionThreshold(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		threshold    int
		modelContext int
		ratio        int
		want         int
	}{
		{name: "default 128k budget keeps configured threshold", threshold: 100000, modelContext: 128000, ratio: 80, want: 100000},
		{name: "small compaction model triggers before overflow", threshold: 100000, modelContext: 32000, ratio: 80, want: 22976},
		{name: "keeps lower user threshold", threshold: 5000, modelContext: 200000, ratio: 80, want: 5000},
		{name: "keeps threshold when model window unknown", threshold: 100000, modelContext: 0, ratio: 80, want: 100000},
		{name: "zero threshold stays disabled", threshold: 0, modelContext: 200000, ratio: 80, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := effectiveCompactionThreshold(tc.threshold, tc.modelContext, tc.ratio); got != tc.want {
				t.Fatalf("effectiveCompactionThreshold(%d, %d, %d) = %d, want %d", tc.threshold, tc.modelContext, tc.ratio, got, tc.want)
			}
		})
	}
}

func TestAsyncCompactionInputTokensPrefersKnownCompactableHistory(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		resolved      resolvedContext
		providerInput int
		want          int
	}{
		{
			name:          "uses rolling summary plus raw history",
			resolved:      resolvedContext{compactionInputTokens: 7000, compactionInputTokensKnown: true, compactableTokens: 4000, compactableTokensKnown: true},
			providerInput: 9000,
			want:          7000,
		},
		{
			name:          "known summary-only history stays zero",
			resolved:      resolvedContext{compactableTokensKnown: true},
			providerInput: 9000,
			want:          0,
		},
		{
			name:          "pipeline without a raw projection falls back to provider usage",
			resolved:      resolvedContext{},
			providerInput: 9000,
			want:          9000,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := asyncCompactionInputTokens(tc.resolved, tc.providerInput); got != tc.want {
				t.Fatalf("asyncCompactionInputTokens() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestRollingSummaryTargetTokens(t *testing.T) {
	t.Parallel()

	if got := compaction.RollingSummaryTargetTokens(100000, 40, 1000000); got != 16384 {
		t.Fatalf("RollingSummaryTargetTokens(100000, 40, 1000000) = %d, want 16384", got)
	}
	if got := compaction.RollingSummaryTargetTokens(3, 1, 128000); got != 1 {
		t.Fatalf("small positive target = %d, want floor 1", got)
	}
}
