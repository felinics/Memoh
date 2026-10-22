package application

import (
	"context"
	"log/slog"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/chat/contextepoch"
	"github.com/felinics/memoh/internal/models"
)

// A model switch rewrites the rendered system region, so it must book an
// explicit model-change epoch advance before the turn renders (spec: 换模型
// 记 model-change 翻篇) — and the new epoch must build a fresh baseline
// (budget, hash, identity) instead of inheriting the old chapter's pins.
func TestWireEpochStabilityBooksModelChange(t *testing.T) {
	t.Parallel()

	const sessionID = "33333333-3333-4333-8333-333333333333"
	newService := func(t *testing.T, state contextepoch.State) (*Service, *fakeContextEpochQueries) {
		t.Helper()
		queries := &fakeContextEpochQueries{states: map[string]string{}}
		queries.setState(sessionID, state)
		return &Service{contextEpochs: contextepoch.NewStore(queries, nil), logger: slog.Default()}, queries
	}

	t.Run("changed model books an advance and resets the pins", func(t *testing.T) {
		t.Parallel()
		svc, queries := newService(t, contextepoch.State{
			Epoch: 1, LastReason: contextepoch.ReasonInit,
			ModelIdentity: "uuid-a/gpt-old/window=0/reserve=8192", BudgetTokens: 4000, PrefixHash: "hash-a",
		})
		cfg := native.RunConfig{CurrentModelUUID: "uuid-b", CurrentModelID: "gpt-new"}
		svc.wireEpochStability(context.Background(), sessionID, &cfg)

		state := queries.state(sessionID)
		if state.Epoch != 2 || state.LastReason != contextepoch.ReasonModelChange {
			t.Fatalf("state after model change = %+v; want epoch 2 with reason model-change", state)
		}
		if state.ModelIdentity != "uuid-b/gpt-new/window=0/reserve=8192" {
			t.Fatalf("new epoch identity = %q, want uuid-b/gpt-new", state.ModelIdentity)
		}
		if state.BudgetTokens != 0 || state.PrefixHash != "" {
			t.Fatalf("the new epoch must re-observe its baseline, got budget=%d hash=%q", state.BudgetTokens, state.PrefixHash)
		}
		if cfg.PinnedHistoryBudgetTokens != 0 {
			t.Fatalf("the old epoch's budget pin must not leak into the new chapter: %d", cfg.PinnedHistoryBudgetTokens)
		}
	})

	t.Run("unchanged model keeps the epoch and applies the budget pin", func(t *testing.T) {
		t.Parallel()
		svc, queries := newService(t, contextepoch.State{
			Epoch: 1, LastReason: contextepoch.ReasonInit,
			ModelIdentity: "uuid-a/gpt-old/window=0/reserve=8192", BudgetTokens: 4000,
		})
		cfg := native.RunConfig{CurrentModelUUID: "uuid-a", CurrentModelID: "gpt-old"}
		svc.wireEpochStability(context.Background(), sessionID, &cfg)

		state := queries.state(sessionID)
		if state.Epoch != 1 {
			t.Fatalf("an unchanged model must not advance the epoch: %+v", state)
		}
		if cfg.PinnedHistoryBudgetTokens != 4000 {
			t.Fatalf("pinned budget = %d, want 4000", cfg.PinnedHistoryBudgetTokens)
		}
	})

	t.Run("first observation pins the identity without advancing", func(t *testing.T) {
		t.Parallel()
		svc, queries := newService(t, contextepoch.State{Epoch: 1, LastReason: contextepoch.ReasonInit, BudgetTokens: 4000})
		cfg := native.RunConfig{CurrentModelUUID: "uuid-a", CurrentModelID: "gpt-old"}
		svc.wireEpochStability(context.Background(), sessionID, &cfg)

		state := queries.state(sessionID)
		if state.Epoch != 1 || state.ModelIdentity != "uuid-a/gpt-old/window=0/reserve=8192" {
			t.Fatalf("first observation = %+v; want epoch 1 with the pinned identity", state)
		}
		if cfg.PinnedHistoryBudgetTokens != 4000 {
			t.Fatalf("pinned budget = %d, want 4000", cfg.PinnedHistoryBudgetTokens)
		}
	})

	// A thinking toggle changes the resolved output reserve (8192 -> 32000),
	// which feeds the pinned-budget ceiling: it must book an advance or the
	// clamped budget would vary inside the epoch (rule 4).
	t.Run("thinking toggle books a change through the output reserve", func(t *testing.T) {
		t.Parallel()
		svc, queries := newService(t, contextepoch.State{
			Epoch: 1, LastReason: contextepoch.ReasonInit, ModelIdentity: "uuid-a/gpt-old/window=0/reserve=8192",
		})
		cfg := native.RunConfig{CurrentModelUUID: "uuid-a", CurrentModelID: "gpt-old", ReasoningConfig: &models.ReasoningConfig{Active: true}}
		svc.wireEpochStability(context.Background(), sessionID, &cfg)

		state := queries.state(sessionID)
		if state.Epoch != 2 || state.ModelIdentity != "uuid-a/gpt-old/window=0/reserve=32000" {
			t.Fatalf("a thinking toggle must book a model-change advance: %+v", state)
		}
	})

	// Editing the model record in place (same UUID, smaller context window)
	// changes the pinned-budget ceiling without touching the reserve: the
	// window is part of the identity, so the edit books an advance instead
	// of shrinking the budget inside the epoch.
	t.Run("in-place window edit books a change through the window", func(t *testing.T) {
		t.Parallel()
		svc, queries := newService(t, contextepoch.State{
			Epoch: 1, LastReason: contextepoch.ReasonInit, ModelIdentity: "uuid-a/gpt-old/window=128000/reserve=8192",
		})
		cfg := native.RunConfig{CurrentModelUUID: "uuid-a", CurrentModelID: "gpt-old", ContextBudgetMaxTokens: 64000}
		svc.wireEpochStability(context.Background(), sessionID, &cfg)

		state := queries.state(sessionID)
		if state.Epoch != 2 || state.ModelIdentity != "uuid-a/gpt-old/window=64000/reserve=8192" {
			t.Fatalf("an in-place window edit must book a model-change advance: %+v", state)
		}
	})

	// Two providers serving the same remote model name are different models
	// for cache purposes: the configured model UUID distinguishes them.
	t.Run("same model name on another provider books a change", func(t *testing.T) {
		t.Parallel()
		svc, queries := newService(t, contextepoch.State{
			Epoch: 1, LastReason: contextepoch.ReasonInit, ModelIdentity: "uuid-a/gpt-same/window=0/reserve=8192",
		})
		cfg := native.RunConfig{CurrentModelUUID: "uuid-b", CurrentModelID: "gpt-same"}
		svc.wireEpochStability(context.Background(), sessionID, &cfg)

		if state := queries.state(sessionID); state.Epoch != 2 || state.ModelIdentity != "uuid-b/gpt-same/window=0/reserve=8192" {
			t.Fatalf("provider switch with the same model name must book a change: %+v", state)
		}
	})
}
