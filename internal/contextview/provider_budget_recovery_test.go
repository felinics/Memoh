package contextview

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	native "github.com/felinics/memoh/internal/agent/runtime/native"
)

func recoveryBudgetFixture() native.RunConfig {
	history := stableHistoryMessageFrag("history", sdk.AssistantMessage(strings.Repeat("h", 8000)))
	history.Budget.Overflow = contextfrag.OverflowKeep
	return native.RunConfig{
		ContextBudgetMaxTokens:  16000,
		ContextToolDefsResolved: true,
		ContextToolDefs:         []contextfrag.ToolDefAccounting{{Name: "large_roster", TokenEstimate: 10000}},
		ContextSourceFrags: []contextfrag.ContextFrag{
			systemTextFrag("system", strings.Repeat("s", 3200), contextfrag.KindSystemPrompt, 20),
			history,
			currentMessageFrag("current", "continue"),
		},
		ContextLifecycle: contextfrag.NewLifecycleHolder(),
	}
}

func TestProviderBudgetRecoveryUsesFinalHistoryBudget(t *testing.T) {
	cfg := recoveryBudgetFixture()
	calls := 0
	cfg.RecoverContextBudget = func(_ context.Context, failed native.RunConfig) (native.RunConfig, bool, error) {
		calls++
		plan := failed.ContextManifest.BudgetPlan
		if plan == nil || plan.HistoryBudget != 998 || plan.ToolDefsCost != 10000 || plan.ActualSystemCost != 1000 {
			t.Fatalf("recovery budget = %+v", plan)
		}
		failed.ContextSourceFrags = append([]contextfrag.ContextFrag(nil), failed.ContextSourceFrags...)
		failed.ContextSourceFrags[1] = stableHistoryMessageFrag("summary", sdk.AssistantMessage("earlier conversation summary"))
		return failed, true, nil
	}
	got, err := ProviderRunConfigApplier(nil)(context.Background(), cfg)
	if err != nil {
		t.Fatalf("context must be admitted after compaction: %v", err)
	}
	if calls != 1 || !reflect.DeepEqual(got.Messages, []sdk.Message{sdk.AssistantMessage("earlier conversation summary"), sdk.UserMessage("continue")}) {
		t.Fatalf("recovered calls=%d messages=%+v", calls, got.Messages)
	}
	for _, mutation := range got.ContextMutations.Records() {
		if mutation.Kind == contextfrag.MutationContextBudgetFailure {
			t.Fatal("successful recovery must not publish a failed-budget mutation")
		}
	}
}

func TestProviderBudgetRecoveryCanCompactBeforeHistoryIsDropped(t *testing.T) {
	cfg := recoveryBudgetFixture()
	cfg.ContextSourceFrags[1].Budget.Overflow = contextfrag.OverflowDrop
	calls := 0
	cfg.RecoverContextBudget = func(_ context.Context, cfg native.RunConfig) (native.RunConfig, bool, error) {
		calls++
		if cfg.ContextManifest.BudgetPlan.HistoryBudget != 998 {
			t.Fatalf("history allowance = %+v", cfg.ContextManifest.BudgetPlan)
		}
		cfg.ContextSourceFrags = append([]contextfrag.ContextFrag(nil), cfg.ContextSourceFrags...)
		cfg.ContextSourceFrags[1] = stableHistoryMessageFrag("summary", sdk.AssistantMessage("preserved by compaction"))
		return cfg, true, nil
	}
	got, err := ProviderRunConfigApplier(nil)(t.Context(), cfg)
	if err != nil || calls != 1 || !reflect.DeepEqual(got.Messages, []sdk.Message{sdk.AssistantMessage("preserved by compaction"), sdk.UserMessage("continue")}) {
		t.Fatalf("droppable history bypassed budget compaction: calls=%d error=%v messages=%+v", calls, err, got.Messages)
	}
}

func TestProviderBudgetRecoveryCannotBypassAdmission(t *testing.T) {
	for _, recovered := range []bool{false, true} {
		t.Run(map[bool]string{false: "no compaction", true: "still oversized"}[recovered], func(t *testing.T) {
			cfg := recoveryBudgetFixture()
			calls := 0
			cfg.RecoverContextBudget = func(_ context.Context, cfg native.RunConfig) (native.RunConfig, bool, error) {
				calls++
				return cfg, recovered, nil
			}
			got, err := ProviderRunConfigApplier(nil)(context.Background(), cfg)
			if !errors.Is(err, contextfrag.ErrProtectedContextOverflow) || calls != 1 {
				t.Fatalf("recovery calls=%d error=%v", calls, err)
			}
			found := false
			for _, mutation := range got.ContextMutations.Records() {
				found = found || mutation.Kind == contextfrag.MutationContextBudgetFailure
			}
			if !found {
				t.Fatal("unresolved overflow must retain failed-budget evidence")
			}
		})
	}
}

func TestProviderBudgetRecoveryIgnoresUnrecoverableRequests(t *testing.T) {
	for _, name := range []string{"fixed overhead", "canceled"} {
		t.Run(name, func(t *testing.T) {
			cfg := recoveryBudgetFixture()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch name {
			case "fixed overhead":
				cfg.ContextToolDefs[0].TokenEstimate = 16000
			case "canceled":
				cancel()
			}
			cfg.RecoverContextBudget = func(context.Context, native.RunConfig) (native.RunConfig, bool, error) {
				t.Fatal("request cannot be rescued by compacting history")
				return native.RunConfig{}, false, nil
			}
			if _, err := ProviderRunConfigApplier(nil)(ctx, cfg); err == nil {
				t.Fatal("unrecoverable request was admitted")
			}
		})
	}
}

func TestProviderBudgetRecoveryFailureKeepsBudgetAudit(t *testing.T) {
	cfg := recoveryBudgetFixture()
	cfg.RecoverContextBudget = func(context.Context, native.RunConfig) (native.RunConfig, bool, error) {
		return native.RunConfig{}, false, errors.New("private compaction failure")
	}
	got, err := ProviderRunConfigApplier(nil)(context.Background(), cfg)
	if !errors.Is(err, contextfrag.ErrProtectedContextOverflow) || got.ContextManifest.BudgetPlan == nil {
		t.Fatalf("failed recovery lost budget error or plan: %v / %+v", err, got.ContextManifest.BudgetPlan)
	}
	if len(got.ContextMutations.Records()) == 0 {
		t.Fatal("failed recovery lost budget audit")
	}
}

func TestProviderBudgetRecoveryRecomposeDoesNotRecordBudgetFailure(t *testing.T) {
	cfg := recoveryBudgetFixture()
	cfg.RecoverContextBudget = func(_ context.Context, cfg native.RunConfig) (native.RunConfig, bool, error) {
		return cfg, false, native.ErrContextRecompose
	}
	got, err := ProviderRunConfigApplier(nil)(context.Background(), cfg)
	if !errors.Is(err, native.ErrContextRecompose) {
		t.Fatalf("recompose control = %v", err)
	}
	if got.ContextMutations != nil {
		for _, mutation := range got.ContextMutations.Records() {
			if mutation.Kind == contextfrag.MutationContextBudgetFailure {
				t.Fatal("successful compaction recompose is not a budget failure")
			}
		}
	}
}

func TestProviderBudgetRecoveryHonorsCancellation(t *testing.T) {
	cfg := recoveryBudgetFixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg.RecoverContextBudget = func(_ context.Context, cfg native.RunConfig) (native.RunConfig, bool, error) {
		cancel()
		return cfg, true, nil
	}
	if _, err := ProviderRunConfigApplier(nil)(ctx, cfg); !errors.Is(err, context.Canceled) {
		t.Fatalf("recovery cancellation = %v", err)
	}
}
