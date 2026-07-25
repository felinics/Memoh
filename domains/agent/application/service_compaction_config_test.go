package application

import (
	"context"
	"log/slog"
	"testing"

	"github.com/memohai/memoh/domains/api/setting"
	modeldomain "github.com/memohai/memoh/domains/model"
	modelcatalog "github.com/memohai/memoh/domains/model/catalog"
)

type compactionConfigQueries struct {
	model    modelcatalog.Record
	provider modelcatalog.ResolvedProvider
}

func (q *compactionConfigQueries) ResolveModelProvider(context.Context, string) (modelcatalog.ResolvedProvider, error) {
	return q.provider, nil
}

type compactionModelStore struct {
	modelcatalog.Store
	model modelcatalog.Record
}

func (s compactionModelStore) GetByID(context.Context, string) (modelcatalog.Record, error) {
	return s.model, nil
}

func TestBuildCompactionConfigKeepsRatioSelection(t *testing.T) {
	t.Parallel()

	const modelUUID = "00000000-0000-0000-0000-000000000401"
	const providerUUID = "00000000-0000-0000-0000-000000000402"

	queries := &compactionConfigQueries{
		model: modelcatalog.Record{
			ID:         modelUUID,
			ModelID:    "compact-model",
			ProviderID: providerUUID,
			Type:       modeldomain.ModelTypeChat,
			Enable:     true,
			Config:     []byte(`{"context_window":200000}`),
		},
		provider: modelcatalog.ResolvedProvider{
			ID:         providerUUID,
			Name:       "test-provider",
			ClientType: modeldomain.ClientTypeOpenAICompletions,
			Enable:     true,
			APIKey:     "test-key",
		},
	}
	r := &Service{
		logger:                slog.New(slog.DiscardHandler),
		modelsService:         modelcatalog.NewService(slog.New(slog.DiscardHandler), compactionModelStore{model: queries.model}),
		modelProviderResolver: queries,
	}

	cfg, err := r.buildCompactionConfig(context.Background(), ChatRequest{
		BotID:    "00000000-0000-0000-0000-000000000403",
		ThreadID: "00000000-0000-0000-0000-000000000404",
	}, setting.Settings{
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
}

func TestEffectiveCompactionThreshold(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		threshold int
		budget    int
		want      int
	}{
		{name: "clamps to budget share when user threshold exceeds it", threshold: 100000, budget: 10000, want: 7000},
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
			name:          "excludes summaries and prompt overhead",
			resolved:      resolvedContext{compactableTokens: 4000, compactableTokensKnown: true},
			providerInput: 9000,
			want:          4000,
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
