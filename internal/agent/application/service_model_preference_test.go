package application

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/settings"
)

// Mechanism tests for the session model preference (issue #879, spec v2
// §4.3): resolution chain order, the session level inside reasoning
// resolution, and the per-turn write-back gate.

// Chain: request > session memory > bot default (spec §3.2). The history
// fallback lives below the bot default and is already covered by
// TestSelectChatModelFallsBackToSessionLastModel.
func TestSelectChatModelSessionMemoryLevel(t *testing.T) {
	ctx := context.Background()
	provider := modelSelectionProviderRow(t, "00000000-0000-0000-0000-000000000610", "openai-completions", true)
	botModel := modelSelectionModelRow(t, "00000000-0000-0000-0000-000000000611", "gpt-bot", provider.ID, models.ModelTypeChat, true)
	prefModel := modelSelectionModelRow(t, "00000000-0000-0000-0000-000000000612", "gpt-pref", provider.ID, models.ModelTypeChat, true)
	fake := &modelSelectionFakeQueries{
		models: map[string]sqlc.Model{
			botModel.ModelID:  botModel,
			prefModel.ModelID: prefModel,
		},
		provider: provider,
	}
	resolver := newModelSelectionService(t, fake)
	botSettings := settings.Settings{ChatModelID: botModel.ID.String()}

	// Session memory beats the bot default.
	got, _, err := resolver.selectChatModel(ctx, ChatRequest{
		BotID:    "00000000-0000-0000-0000-000000000613",
		ThreadID: "00000000-0000-0000-0000-000000000614",
	}, botSettings, prefModel.ID.String())
	if err != nil {
		t.Fatalf("selectChatModel with session memory error = %v, want nil", err)
	}
	if got.ModelID != "gpt-pref" {
		t.Fatalf("model_id = %q, want session memory %q", got.ModelID, "gpt-pref")
	}

	// The request's own model beats session memory.
	got, _, err = resolver.selectChatModel(ctx, ChatRequest{
		BotID:    "00000000-0000-0000-0000-000000000613",
		ThreadID: "00000000-0000-0000-0000-000000000614",
		Model:    "gpt-bot",
	}, botSettings, prefModel.ID.String())
	if err != nil {
		t.Fatalf("selectChatModel with request model error = %v, want nil", err)
	}
	if got.ModelID != "gpt-bot" {
		t.Fatalf("model_id = %q, want request model %q", got.ModelID, "gpt-bot")
	}
}

// The session's remembered effort sits between the per-message request and
// the bot's stored value (spec §3.2): request > session memory > bot stored
// > model default. It must never be passed as the request level — that one
// travels to spawned subagents as "the user's explicit pick this turn".
func TestResolveReasoningConfigSessionLevel(t *testing.T) {
	toggleModel := models.GetResponse{
		Model: models.Model{
			Config: models.ModelConfig{
				ThinkingMode:     models.ThinkingModeToggle,
				ReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max"},
			},
		},
	}
	disablableModel := models.GetResponse{
		Model: models.Model{
			Config: models.ModelConfig{
				ThinkingMode:     models.ThinkingModeToggle,
				ReasoningEfforts: []string{models.ReasoningEffortDisable, "low", "medium", "high"},
			},
		},
	}
	const clientType = "openai-completions"

	tests := []struct {
		name         string
		model        models.GetResponse
		stored       string
		session      string
		requested    string
		wantEffort   string
		wantDisabled bool
	}{
		{name: "session beats stored", model: toggleModel, stored: "low", session: "high", requested: "", wantEffort: "high"},
		// "max" would be the wrong probe here: the generic OpenAI wire drops
		// it, so an illegal request tier would test wire policy, not ordering.
		{name: "request beats session", model: toggleModel, stored: "low", session: "high", requested: "xhigh", wantEffort: "xhigh"},
		{name: "empty session keeps stored", model: toggleModel, stored: "low", session: "", requested: "", wantEffort: "low"},
		{name: "illegal session tier falls to model default", model: toggleModel, stored: "", session: "bogus", requested: "", wantEffort: "medium"},
		{name: "session disable on disablable model", model: disablableModel, stored: "", session: "disable", requested: "", wantDisabled: true},
		// A remembered "disable" on a model that cannot turn off reconciles to
		// the model's default-on tier — the DB never replays an illegal pair.
		{name: "session disable reconciled on always-on model", model: toggleModel, stored: "", session: "disable", requested: "", wantEffort: "medium"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveReasoningConfig(tt.model, settings.Settings{ReasoningEffort: tt.stored}, tt.requested, tt.session, clientType)
			if got == nil {
				t.Fatal("resolveReasoningConfig returned nil, want a decision")
			}
			if got.Disabled != tt.wantDisabled {
				t.Fatalf("Disabled = %v, want %v", got.Disabled, tt.wantDisabled)
			}
			if !tt.wantDisabled && got.Effort != tt.wantEffort {
				t.Fatalf("Effort = %q, want %q", got.Effort, tt.wantEffort)
			}
		})
	}
}

// Write-back gate (spec §3.3): only a request that CARRIES the pair writes;
// the write is skipped when the stored pair already matches; the stored value
// is the resolved pair in its normalized vocabulary.
func TestWriteBackSessionModelPreference(t *testing.T) {
	ctx := context.Background()
	const (
		sessionID = "00000000-0000-0000-0000-000000000615"
		modelUUID = "00000000-0000-0000-0000-000000000616"
	)
	chatModel := models.GetResponse{ID: modelUUID, Model: models.Model{ModelID: "gpt-pref"}}
	active := &models.ReasoningConfig{Active: true, Effort: "high"}

	t.Run("request without the pair never writes", func(t *testing.T) {
		fake := &modelSelectionFakeQueries{}
		svc := newModelSelectionService(t, fake)
		svc.writeBackSessionModelPreference(ctx, sessionID, false, chatModel, active, "", "")
		if len(fake.updatedPrefs) != 0 {
			t.Fatalf("writes = %d, want 0", len(fake.updatedPrefs))
		}
	})

	t.Run("stored pair already matches: skipped", func(t *testing.T) {
		fake := &modelSelectionFakeQueries{}
		svc := newModelSelectionService(t, fake)
		svc.writeBackSessionModelPreference(ctx, sessionID, true, chatModel, active, modelUUID, "high")
		if len(fake.updatedPrefs) != 0 {
			t.Fatalf("writes = %d, want 0", len(fake.updatedPrefs))
		}
	})

	t.Run("carried pair writes the resolved values", func(t *testing.T) {
		fake := &modelSelectionFakeQueries{}
		svc := newModelSelectionService(t, fake)
		svc.writeBackSessionModelPreference(ctx, sessionID, true, chatModel, active, "", "")
		if len(fake.updatedPrefs) != 1 {
			t.Fatalf("writes = %d, want 1", len(fake.updatedPrefs))
		}
		got := fake.updatedPrefs[0]
		if !got.PreferredChatModelID.Valid || got.PreferredChatModelID.String() != modelUUID {
			t.Fatalf("model = %v, want %s", got.PreferredChatModelID, modelUUID)
		}
		if !got.PreferredReasoningEffort.Valid || got.PreferredReasoningEffort.String != "high" {
			t.Fatalf("effort = %v, want high", got.PreferredReasoningEffort)
		}
	})

	t.Run("disabled thinking stores the disable vocabulary", func(t *testing.T) {
		fake := &modelSelectionFakeQueries{}
		svc := newModelSelectionService(t, fake)
		svc.writeBackSessionModelPreference(ctx, sessionID, true, chatModel, &models.ReasoningConfig{Disabled: true}, "", "")
		if len(fake.updatedPrefs) != 1 || fake.updatedPrefs[0].PreferredReasoningEffort.String != "disable" {
			t.Fatalf("writes = %+v, want one write with effort=disable", fake.updatedPrefs)
		}
	})

	t.Run("reasoning-less model stores NULL effort", func(t *testing.T) {
		fake := &modelSelectionFakeQueries{}
		svc := newModelSelectionService(t, fake)
		svc.writeBackSessionModelPreference(ctx, sessionID, true, chatModel, nil, "", "")
		if len(fake.updatedPrefs) != 1 || fake.updatedPrefs[0].PreferredReasoningEffort.Valid {
			t.Fatalf("writes = %+v, want one write with NULL effort", fake.updatedPrefs)
		}
	})
}

// A half pair (NULL model, surviving effort) arises when ON DELETE SET NULL
// clears only the model column. The effort must be dropped with the model —
// honoring it alone would pin it onto whatever model the chain falls back
// to, the cross-model effort memory the spec rules out (§3.7). A full pair
// still round-trips.
func TestSessionModelPreferenceHalfPair(t *testing.T) {
	ctx := context.Background()
	const sessionID = "00000000-0000-0000-0000-000000000617"

	var id pgtype.UUID
	if err := id.Scan(sessionID); err != nil {
		t.Fatalf("scan session id: %v", err)
	}

	newSvc := func(session sqlc.BotSession) *Service {
		return newModelSelectionService(t, &modelSelectionFakeQueries{session: session})
	}

	t.Run("NULL model drops the surviving effort", func(t *testing.T) {
		svc := newSvc(sqlc.BotSession{
			ID:                       id,
			PreferredReasoningEffort: pgtype.Text{String: "high", Valid: true},
		})
		modelID, effort := svc.sessionModelPreference(ctx, sessionID)
		if modelID != "" || effort != "" {
			t.Fatalf("half pair = (%q, %q), want no memory", modelID, effort)
		}
	})

	t.Run("full pair round-trips", func(t *testing.T) {
		var modelUUID pgtype.UUID
		if err := modelUUID.Scan("00000000-0000-0000-0000-000000000618"); err != nil {
			t.Fatalf("scan model id: %v", err)
		}
		svc := newSvc(sqlc.BotSession{
			ID:                       id,
			PreferredChatModelID:     modelUUID,
			PreferredReasoningEffort: pgtype.Text{String: "high", Valid: true},
		})
		modelID, effort := svc.sessionModelPreference(ctx, sessionID)
		if modelID != modelUUID.String() || effort != "high" {
			t.Fatalf("full pair = (%q, %q), want (%s, high)", modelID, effort, modelUUID.String())
		}
	})
}
