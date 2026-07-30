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
		name      string
		threshold int
		budget    int
		want      int
	}{
		{name: "keeps configured threshold when chat send budget is lower", threshold: 100000, budget: 10000, want: 100000},
		{name: "keeps lower user threshold", threshold: 5000, budget: 200000, want: 5000},
		{name: "keeps threshold when budget unknown", threshold: 100000, budget: 0, want: 100000},
		{name: "zero threshold stays disabled", threshold: 0, budget: 200000, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := effectiveCompactionThreshold(tc.threshold, tc.budget); got != tc.want {
				t.Fatalf("effectiveCompactionThreshold(%d, %d) = %d, want %d", tc.threshold, tc.budget, got, tc.want)
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

	if got := compaction.RollingSummaryTargetTokens(100000, 40); got != 40000 {
		t.Fatalf("RollingSummaryTargetTokens(100000, 40) = %d, want 40000", got)
	}
	if got := compaction.RollingSummaryTargetTokens(3, 1); got != 1 {
		t.Fatalf("small positive target = %d, want floor 1", got)
	}
}

func TestSyncCompactionTargetTokens(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		budget int
		ratio  int
		want   int
	}{
		{name: "default ratio keeps 20% of budget", budget: 200000, ratio: 80, want: 40000},
		{name: "light ratio keeps most of budget", budget: 10000, ratio: 20, want: 8000},
		{name: "full ratio keeps nothing", budget: 200000, ratio: 100, want: 0},
		{name: "unknown budget disables target", budget: 0, ratio: 80, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := syncCompactionTargetTokens(tc.budget, tc.ratio); got != tc.want {
				t.Fatalf("syncCompactionTargetTokens(%d, %d) = %d, want %d", tc.budget, tc.ratio, got, tc.want)
			}
		})
	}
}
