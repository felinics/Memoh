package application

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/settings"
)

func TestUnifiedCompactionController(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		threshold     int
		targetPercent *int
		budget        int
		pressure      int
		wantTrigger   int
		wantTarget    int
		wantSync      bool
	}{
		{
			name:        "zero config uses 50 75 40",
			budget:      200000,
			pressure:    149999,
			wantTrigger: 100000,
			wantTarget:  80000,
		},
		{
			name:        "hard gate fires at 75 percent",
			budget:      200000,
			pressure:    150000,
			wantTrigger: 100000,
			wantTarget:  80000,
			wantSync:    true,
		},
		{
			name:        "absolute threshold only moves async trigger",
			threshold:   90000,
			budget:      200000,
			pressure:    149999,
			wantTrigger: 90000,
			wantTarget:  80000,
		},
		{
			name:        "absolute threshold clamps to hard gate",
			threshold:   500000,
			budget:      200000,
			pressure:    150000,
			wantTrigger: 150000,
			wantTarget:  80000,
			wantSync:    true,
		},
		{
			name:          "target override changes the configured target",
			threshold:     90000,
			targetPercent: targetPercentPointer(55),
			budget:        200000,
			pressure:      150000,
			wantTrigger:   90000,
			wantTarget:    110000,
			wantSync:      true,
		},
		{
			name:          "zero budget stands down",
			threshold:     90000,
			targetPercent: targetPercentPointer(55),
			pressure:      150000,
		},
		{
			name:          "small positive budget keeps controller active",
			threshold:     100,
			targetPercent: targetPercentPointer(1),
			budget:        1,
			pressure:      1,
			wantTrigger:   1,
			wantTarget:    1,
			wantSync:      true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := autoCompactionThreshold(tc.threshold, tc.budget); got != tc.wantTrigger {
				t.Fatalf("autoCompactionThreshold(%d, %d) = %d, want %d", tc.threshold, tc.budget, got, tc.wantTrigger)
			}
			if got := compactionTargetTokens(tc.targetPercent, tc.budget); got != tc.wantTarget {
				t.Fatalf("compactionTargetTokens(%v, %d) = %d, want %d", tc.targetPercent, tc.budget, got, tc.wantTarget)
			}
			if got := syncCompactionShouldRun(tc.pressure, tc.budget); got != tc.wantSync {
				t.Fatalf("syncCompactionShouldRun(%d, %d) = %t, want %t", tc.pressure, tc.budget, got, tc.wantSync)
			}
		})
	}
}

type controllerQueries struct {
	*compactionConfigQueries
	settings sqlc.GetSettingsByBotIDRow
}

func (q *controllerQueries) GetSettingsByBotID(context.Context, pgtype.UUID) (sqlc.GetSettingsByBotIDRow, error) {
	return q.settings, nil
}

type recordingCompactionRunner struct {
	configs []compaction.TriggerConfig
}

func (r *recordingCompactionRunner) RunCompactionSync(_ context.Context, cfg compaction.TriggerConfig) (compaction.Result, error) {
	r.configs = append(r.configs, cfg)
	return compaction.Result{Status: compaction.StatusOK}, nil
}

func newControllerPolicyService(t *testing.T, targetPercent *int) (*Service, *recordingCompactionRunner) {
	t.Helper()
	const (
		modelUUID    = "00000000-0000-0000-0000-000000000451"
		providerUUID = "00000000-0000-0000-0000-000000000452"
		botUUID      = "00000000-0000-0000-0000-000000000453"
	)
	target := pgtype.Int4{}
	if targetPercent != nil {
		target = pgtype.Int4{Int32: int32(*targetPercent), Valid: true} //nolint:gosec // test values stay within 1..99
	}
	queries := &controllerQueries{
		compactionConfigQueries: &compactionConfigQueries{
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
		},
		settings: sqlc.GetSettingsByBotIDRow{
			BotID:                   compactionConfigUUID(t, botUUID),
			Language:                "auto",
			ReasoningEffort:         "medium",
			CompactionEnabled:       true,
			CompactionTargetPercent: target,
			CompactionModelID:       compactionConfigUUID(t, modelUUID),
		},
	}
	runner := &recordingCompactionRunner{}
	service := &Service{
		logger:            slog.New(slog.DiscardHandler),
		modelsService:     models.NewService(slog.New(slog.DiscardHandler), queries),
		queries:           queries,
		settingsService:   settings.NewService(slog.New(slog.DiscardHandler), queries, nil, nil),
		compactionService: runner,
	}
	return service, runner
}

func TestAutomaticCompactionPathsApplyTargetAndBoundAsyncDrain(t *testing.T) {
	t.Parallel()

	service, runner := newControllerPolicyService(t, targetPercentPointer(55))
	req := ChatRequest{
		BotID:    "00000000-0000-0000-0000-000000000453",
		ThreadID: "00000000-0000-0000-0000-000000000454",
	}
	resolved := resolvedContext{
		contextTokenBudget:     200000,
		compactableTokens:      150000,
		compactableTokensKnown: true,
	}

	service.maybeCompact(context.Background(), req, resolved, 150000)
	if len(runner.configs) != 3 {
		t.Fatalf("async compaction passes = %d, want 3", len(runner.configs))
	}
	for pass, cfg := range runner.configs {
		if cfg.TargetTokens != 110000 {
			t.Fatalf("async pass %d TargetTokens = %d, want 110000", pass+1, cfg.TargetTokens)
		}
		if !cfg.AllowFrontierFusion {
			t.Fatalf("async pass %d AllowFrontierFusion = false, want true", pass+1)
		}
		if cfg.ContextWindowTokens != 200000 {
			t.Fatalf("async pass %d ContextWindowTokens = %d, want 200000", pass+1, cfg.ContextWindowTokens)
		}
	}

	runner.configs = nil
	result := service.runCompactionSync(context.Background(), req, 150000, 200000, "")
	if result.Status != compaction.StatusOK {
		t.Fatalf("sync compaction status = %q, want %q", result.Status, compaction.StatusOK)
	}
	if len(runner.configs) != 1 {
		t.Fatalf("sync compaction passes = %d, want 1", len(runner.configs))
	}
	if got := runner.configs[0].TargetTokens; got != 100000 {
		t.Fatalf("sync TargetTokens = %d, want 100000 soft-share cap", got)
	}
	if runner.configs[0].AllowFrontierFusion {
		t.Fatal("sync AllowFrontierFusion = true, want false")
	}
	if got := runner.configs[0].ContextWindowTokens; got != 200000 {
		t.Fatalf("sync ContextWindowTokens = %d, want 200000", got)
	}
}

func TestSyncBackstopTargetCapsAtSoftShare(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		targetPercent *int
		wantAsync     int
		wantSync      int
	}{
		{
			name:          "target above hard backstop",
			targetPercent: targetPercentPointer(90),
			wantAsync:     7200,
			wantSync:      4000,
		},
		{
			name:      "default target remains below cap",
			wantAsync: 3200,
			wantSync:  3200,
		},
		{
			name:          "target below soft share is honored",
			targetPercent: targetPercentPointer(49),
			wantAsync:     3920,
			wantSync:      3920,
		},
		{
			name:          "target at soft share boundary is honored",
			targetPercent: targetPercentPointer(50),
			wantAsync:     4000,
			wantSync:      4000,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			service, runner := newControllerPolicyService(t, tc.targetPercent)
			req := ChatRequest{
				BotID:    "00000000-0000-0000-0000-000000000453",
				ThreadID: "00000000-0000-0000-0000-000000000454",
			}
			resolved := resolvedContext{
				contextTokenBudget:     8000,
				compactableTokens:      4000,
				compactableTokensKnown: true,
			}

			service.maybeCompact(context.Background(), req, resolved, 4000)
			if len(runner.configs) != maxAsyncCompactionPasses {
				t.Fatalf("async compaction passes = %d, want %d", len(runner.configs), maxAsyncCompactionPasses)
			}
			for pass, cfg := range runner.configs {
				if cfg.TargetTokens != tc.wantAsync {
					t.Fatalf("async pass %d TargetTokens = %d, want %d", pass+1, cfg.TargetTokens, tc.wantAsync)
				}
			}

			runner.configs = nil
			result := service.runCompactionSync(context.Background(), req, 6000, 8000, "")
			if result.Status != compaction.StatusOK {
				t.Fatalf("sync compaction status = %q, want %q", result.Status, compaction.StatusOK)
			}
			if len(runner.configs) != 1 {
				t.Fatalf("sync compaction passes = %d, want 1", len(runner.configs))
			}
			if got := runner.configs[0].TargetTokens; got != tc.wantSync {
				t.Fatalf("sync TargetTokens = %d, want %d", got, tc.wantSync)
			}
		})
	}
}

func TestSetCompactionServicePreservesNil(t *testing.T) {
	t.Parallel()

	service := &Service{}
	service.SetCompactionService(nil)
	if service.compactionService != nil {
		t.Fatal("SetCompactionService(nil) stored a non-nil runner")
	}
}

func targetPercentPointer(value int) *int {
	return &value
}
