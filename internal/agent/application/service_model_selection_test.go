package application

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/reasoning"
	"github.com/felinics/memoh/internal/settings"
)

func TestMatchesModelReference_ModelID(t *testing.T) {
	t.Parallel()

	model := models.GetResponse{
		ID:      "a55f0d2d-1547-49a0-b085-ec4ab778f4b8",
		ModelID: "gpt-4o",
	}

	if !matchesModelReference(model, "gpt-4o") {
		t.Fatal("expected model slug to match")
	}
}

func TestMatchesModelReference_UUID(t *testing.T) {
	t.Parallel()

	model := models.GetResponse{
		ID:      "a55f0d2d-1547-49a0-b085-ec4ab778f4b8",
		ModelID: "gpt-4o",
	}

	if !matchesModelReference(model, "a55f0d2d-1547-49a0-b085-ec4ab778f4b8") {
		t.Fatal("expected model UUID to match")
	}
}

func TestMatchesModelReference_NoMatch(t *testing.T) {
	t.Parallel()

	model := models.GetResponse{
		ID:      "a55f0d2d-1547-49a0-b085-ec4ab778f4b8",
		ModelID: "gpt-4o",
	}

	if matchesModelReference(model, "gpt-4.1") {
		t.Fatal("expected non-matching model reference to fail")
	}
}

func TestMatchesModelReference_TrimmedInput(t *testing.T) {
	t.Parallel()

	model := models.GetResponse{
		ID:      "a55f0d2d-1547-49a0-b085-ec4ab778f4b8",
		ModelID: "gpt-4o",
	}

	if !matchesModelReference(model, "  gpt-4o  ") {
		t.Fatal("expected trimmed model slug to match")
	}
}

func TestBuildModelSelectionRequest_PreservesOverrides(t *testing.T) {
	t.Parallel()

	req := buildModelSelectionRequest(baseRunConfigParams{
		BotID:           "bot-1",
		SessionID:       "session-1",
		CurrentPlatform: "web",
		Model:           "model-override",
		Provider:        "openai-responses",
	}, "chat-1")

	if req.BotID != "bot-1" {
		t.Fatalf("unexpected bot id: %q", req.BotID)
	}
	if req.ChatID != "chat-1" {
		t.Fatalf("unexpected chat id: %q", req.ChatID)
	}
	if req.ThreadID != "session-1" {
		t.Fatalf("unexpected session id: %q", req.ThreadID)
	}
	if req.CurrentChannel != "web" {
		t.Fatalf("unexpected current channel: %q", req.CurrentChannel)
	}
	if req.Model != "model-override" {
		t.Fatalf("unexpected model override: %q", req.Model)
	}
	if req.Provider != "openai-responses" {
		t.Fatalf("unexpected provider override: %q", req.Provider)
	}
}

func TestSupportsImageInputForModel(t *testing.T) {
	t.Parallel()

	visionModel := models.GetResponse{
		Model: models.Model{
			Config: models.ModelConfig{
				Compatibilities: []string{models.CompatVision},
			},
		},
	}
	if !supportsImageInputForModel(visionModel) {
		t.Fatal("vision-compatible model should support image input")
	}

	plainModel := models.GetResponse{}
	if supportsImageInputForModel(plainModel) {
		t.Fatal("model without vision compatibility should not support image input")
	}
}

func TestResolveReasoningConfig(t *testing.T) {
	t.Parallel()

	// Legacy data: reasoning compat without an explicit thinking_mode resolves to
	// toggle via the SupportsReasoning/ResolveThinkingMode bridge.
	toggleModel := models.GetResponse{
		Model: models.Model{
			Config: models.ModelConfig{
				Compatibilities: []string{models.CompatReasoning},
			},
		},
	}
	// Adaptive-capable model (Claude 4.6+ family): user can turn thinking off,
	// but when enabled it uses adaptive thinking.
	adaptiveModel := models.GetResponse{
		Model: models.Model{
			Config: models.ModelConfig{
				ThinkingMode:     models.ThinkingModeAdaptive,
				ReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max"},
			},
		},
	}
	codexModel := models.GetResponse{
		Model: models.Model{
			Config: models.ModelConfig{
				ThinkingMode:     models.ThinkingModeToggle,
				ReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max"},
			},
		},
	}
	// Advertises no medium, and lists its tiers strongest-first so a fallback that
	// simply took the head of the list would pick max.
	noMediumModel := models.GetResponse{
		Model: models.Model{
			Config: models.ModelConfig{
				ThinkingMode:     models.ThinkingModeToggle,
				ReasoningEfforts: []string{"max", "high", "low", "none"},
			},
		},
	}

	// A model that advertises it can be turned off, alongside its active tiers.
	disablableModel := models.GetResponse{
		Model: models.Model{
			Config: models.ModelConfig{
				ThinkingMode:     models.ThinkingModeToggle,
				ReasoningEfforts: []string{models.ReasoningEffortDisable, "minimal", "low", "medium", "high"},
			},
		},
	}
	// Legacy Anthropic (<=4.5): toggle mode advertising only the implicit
	// low/medium/high base. On the Anthropic wire this must stay non-adaptive so
	// the SDK sends thinking{type:"enabled", budget_tokens:N}.
	legacyAnthropicModel := models.GetResponse{
		Model: models.Model{
			Config: models.ModelConfig{
				ThinkingMode:     models.ThinkingModeToggle,
				ReasoningEfforts: []string{"low", "medium", "high"},
			},
		},
	}
	// Legacy Anthropic that also declares it can be turned off. Being turn-off
	// capable says nothing about which thinking wire the model speaks, so this one
	// must stay on the budget path.
	disablableLegacyAnthropicModel := models.GetResponse{
		Model: models.Model{
			Config: models.ModelConfig{
				ThinkingMode:     models.ThinkingModeToggle,
				ReasoningEfforts: []string{models.ReasoningEffortDisable, "low", "medium", "high"},
			},
		},
	}
	// Cloud-variant Claude 4.6+: the registry left it toggle (no
	// supports_adaptive_thinking) but it advertises 4.6+ effort tiers, so the
	// Anthropic wire promotes it to adaptive to stay off the legacy budget path.
	cloudEffortModel := models.GetResponse{
		Model: models.Model{
			Config: models.ModelConfig{
				ThinkingMode:     models.ThinkingModeToggle,
				ReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max"},
			},
		},
	}
	plainModel := models.GetResponse{}

	tests := []struct {
		name          string
		model         models.GetResponse
		botSettings   settings.Settings
		requestEffort string
		clientType    string
		want          *models.ReasoningConfig
	}{
		{
			name:          "unsupported disable override falls back to bot default",
			model:         toggleModel,
			botSettings:   settings.Settings{ReasoningEffort: models.ReasoningEffortHigh},
			requestEffort: models.ReasoningEffortDisable,
			want:          &models.ReasoningConfig{Active: true, Effort: models.ReasoningEffortHigh},
		},
		{
			name:          "legacy adaptive request enables toggle with default effort",
			model:         toggleModel,
			requestEffort: reasoning.EffortAdaptive,
			want:          &models.ReasoningConfig{Active: true, Effort: models.ReasoningEffortMedium},
		},
		{
			// A requested "none" is an off request, not an active tier. Treating it as
			// a tier produced Active with Effort "none" — enabled at no strength —
			// which wires to the same request as off.
			name:          "requested none falls back when the model cannot be turned off",
			model:         toggleModel,
			botSettings:   settings.Settings{ReasoningEffort: models.ReasoningEffortHigh},
			requestEffort: models.ReasoningEffortNone,
			want:          &models.ReasoningConfig{Active: true, Effort: models.ReasoningEffortHigh},
		},
		{
			name:          "requested none reads as off, and a disablable model carries the wire value",
			model:         disablableModel,
			botSettings:   settings.Settings{ReasoningEffort: models.ReasoningEffortHigh},
			requestEffort: models.ReasoningEffortNone,
			want:          &models.ReasoningConfig{Disabled: true, OffEffort: models.ReasoningEffortNone},
		},
		{
			name:          "explicit disable reads as off",
			model:         disablableModel,
			requestEffort: models.ReasoningEffortDisable,
			want:          &models.ReasoningConfig{Disabled: true, OffEffort: models.ReasoningEffortNone},
		},
		{
			// A bot parked on off, overridden for one message with a tier the model
			// does not advertise. The stored value must not be picked up as the active
			// tier: "disable" is advertisable now, so it would otherwise reach the
			// provider wire, where no vendor knows the word.
			name:          "a stored off never becomes the active tier",
			model:         disablableModel,
			botSettings:   settings.Settings{ReasoningEffort: models.ReasoningEffortDisable},
			requestEffort: models.ReasoningEffortXHigh,
			want: &models.ReasoningConfig{
				Active:    true,
				Effort:    models.ReasoningEffortMedium,
				OffEffort: models.ReasoningEffortNone,
			},
		},
		{
			name:          "explicit effort is trimmed",
			model:         toggleModel,
			requestEffort: " low ",
			want:          &models.ReasoningConfig{Active: true, Effort: models.ReasoningEffortLow},
		},
		{
			name:        "bot default is used when no request override",
			model:       toggleModel,
			botSettings: settings.Settings{ReasoningEffort: " high "},
			want:        &models.ReasoningConfig{Active: true, Effort: models.ReasoningEffortHigh},
		},
		{
			name:        "unset bot effort falls back to medium",
			model:       toggleModel,
			botSettings: settings.Settings{},
			want:        &models.ReasoningConfig{Active: true, Effort: models.ReasoningEffortMedium},
		},
		{
			name:        "stale bot disable falls back when the model cannot turn off",
			model:       toggleModel,
			botSettings: settings.Settings{ReasoningEffort: models.ReasoningEffortDisable},
			want:        &models.ReasoningConfig{Active: true, Effort: models.ReasoningEffortMedium},
		},
		{
			name:          "adaptive model without off support stays active",
			model:         adaptiveModel,
			requestEffort: models.ReasoningEffortDisable,
			want:          &models.ReasoningConfig{Active: true, Adaptive: true, Effort: models.ReasoningEffortMedium},
		},
		{
			name:          "adaptive model honors explicit effort",
			model:         adaptiveModel,
			requestEffort: models.ReasoningEffortXHigh,
			want:          &models.ReasoningConfig{Active: true, Adaptive: true, Effort: models.ReasoningEffortXHigh},
		},
		{
			name:          "generic openai compatibility drops max and falls back to medium",
			model:         adaptiveModel,
			requestEffort: models.ReasoningEffortMax,
			clientType:    string(models.ClientTypeOpenAICompletions),
			want:          &models.ReasoningConfig{Active: true, Adaptive: true, Effort: models.ReasoningEffortMedium},
		},
		{
			name:          "codex wire preserves max",
			model:         codexModel,
			requestEffort: models.ReasoningEffortMax,
			clientType:    string(models.ClientTypeOpenAICodex),
			want:          &models.ReasoningConfig{Active: true, Effort: models.ReasoningEffortMax},
		},
		{
			name:          "anthropic wire preserves max",
			model:         adaptiveModel,
			requestEffort: models.ReasoningEffortMax,
			clientType:    string(models.ClientTypeAnthropicMessages),
			want:          &models.ReasoningConfig{Active: true, Adaptive: true, Effort: models.ReasoningEffortMax},
		},
		{
			name:        "legacy anthropic stays non-adaptive for budget path",
			model:       legacyAnthropicModel,
			botSettings: settings.Settings{ReasoningEffort: models.ReasoningEffortHigh},
			clientType:  string(models.ClientTypeAnthropicMessages),
			want:        &models.ReasoningConfig{Active: true, Effort: models.ReasoningEffortHigh},
		},
		{
			name:        "declaring off does not promote a legacy anthropic model to adaptive",
			model:       disablableLegacyAnthropicModel,
			botSettings: settings.Settings{ReasoningEffort: models.ReasoningEffortHigh},
			clientType:  string(models.ClientTypeAnthropicMessages),
			want:        &models.ReasoningConfig{Active: true, Effort: models.ReasoningEffortHigh},
		},
		{
			name:        "anthropic cloud variant with effort tiers is promoted to adaptive",
			model:       cloudEffortModel,
			botSettings: settings.Settings{ReasoningEffort: models.ReasoningEffortHigh},
			clientType:  string(models.ClientTypeAnthropicMessages),
			want:        &models.ReasoningConfig{Active: true, Adaptive: true, Effort: models.ReasoningEffortHigh},
		},
		{
			name:        "non-anthropic effort tiers are not promoted to adaptive",
			model:       cloudEffortModel,
			botSettings: settings.Settings{ReasoningEffort: models.ReasoningEffortHigh},
			clientType:  string(models.ClientTypeOpenAICompletions),
			want:        &models.ReasoningConfig{Active: true, Effort: models.ReasoningEffortHigh},
		},
		{
			name:          "model without reasoning ignores request",
			model:         plainModel,
			requestEffort: models.ReasoningEffortHigh,
			want:          nil,
		},
		{
			// The tier the bot stored is gone from this model's ladder, so the
			// fallback has to land on a real neighbouring tier rather than the
			// weakest one the registry happened to list first.
			name:  "unsupported bot default lands on the tier nearest medium",
			model: noMediumModel,
			// low and high are equidistant from medium; ties go to the weaker tier.
			botSettings: settings.Settings{ReasoningEffort: models.ReasoningEffortMinimal},
			want:        &models.ReasoningConfig{Active: true, Effort: models.ReasoningEffortLow},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := resolveReasoningConfig(tt.model, tt.botSettings, tt.requestEffort, tt.clientType)
			if got == nil || tt.want == nil {
				if got != tt.want {
					t.Fatalf("expected %#v, got %#v", tt.want, got)
				}
				return
			}
			if got.Active != tt.want.Active || got.Disabled != tt.want.Disabled ||
				got.Adaptive != tt.want.Adaptive || got.Effort != tt.want.Effort {
				t.Fatalf("expected %#v, got %#v", tt.want, got)
			}
		})
	}
}

// modelSelectionFakeQueries is an in-memory dbstore.Queries fake for
// fetchChatModel tests. The embedded interface panics on any method the
// test did not expect to be called.
type modelSelectionFakeQueries struct {
	dbstore.Queries

	models         map[string]sqlc.Model
	provider       sqlc.Provider
	sessionModelID pgtype.UUID
}

func (f *modelSelectionFakeQueries) ListModelsByModelID(_ context.Context, modelID string) ([]sqlc.Model, error) {
	model, ok := f.models[modelID]
	if !ok {
		return nil, nil
	}
	return []sqlc.Model{model}, nil
}

func (f *modelSelectionFakeQueries) GetModelByID(_ context.Context, id pgtype.UUID) (sqlc.Model, error) {
	for _, model := range f.models {
		if id.Valid && model.ID == id {
			return model, nil
		}
	}
	return sqlc.Model{}, pgx.ErrNoRows
}

func (f *modelSelectionFakeQueries) GetProviderByID(_ context.Context, id pgtype.UUID) (sqlc.Provider, error) {
	if !id.Valid || id != f.provider.ID {
		return sqlc.Provider{}, pgx.ErrNoRows
	}
	return f.provider, nil
}

func (f *modelSelectionFakeQueries) GetLatestSessionModelID(_ context.Context, _ pgtype.UUID) (pgtype.UUID, error) {
	if !f.sessionModelID.Valid {
		return pgtype.UUID{}, pgx.ErrNoRows
	}
	return f.sessionModelID, nil
}

func newModelSelectionService(t *testing.T, fake *modelSelectionFakeQueries) *Service {
	t.Helper()
	return &Service{
		modelsService: models.NewService(slog.New(slog.DiscardHandler), fake),
		queries:       fake,
	}
}

func modelSelectionProviderRow(t *testing.T, id string, clientType string, enable bool) sqlc.Provider {
	t.Helper()
	pgID, err := db.ParseUUID(id)
	if err != nil {
		t.Fatalf("parse provider uuid: %v", err)
	}
	return sqlc.Provider{
		ID:         pgID,
		Name:       "provider-" + id,
		ClientType: clientType,
		Enable:     enable,
		Config:     []byte(`{}`),
		Metadata:   []byte(`{}`),
	}
}

func modelSelectionModelRow(t *testing.T, id string, modelID string, providerID pgtype.UUID, modelType models.ModelType, enable bool) sqlc.Model {
	t.Helper()
	pgID, err := db.ParseUUID(id)
	if err != nil {
		t.Fatalf("parse model uuid: %v", err)
	}
	return sqlc.Model{
		ID:         pgID,
		ModelID:    modelID,
		Name:       pgtype.Text{String: modelID, Valid: true},
		ProviderID: providerID,
		Type:       string(modelType),
		Enable:     enable,
		Config:     []byte(`{}`),
	}
}

func TestSelectChatModelFallsBackToSessionLastModel(t *testing.T) {
	ctx := context.Background()
	provider := modelSelectionProviderRow(t, "00000000-0000-0000-0000-000000000601", "openai-completions", true)
	model := modelSelectionModelRow(t, "00000000-0000-0000-0000-000000000602", "gpt-session", provider.ID, models.ModelTypeChat, true)
	fake := &modelSelectionFakeQueries{
		models:         map[string]sqlc.Model{model.ModelID: model},
		provider:       provider,
		sessionModelID: model.ID,
	}
	resolver := newModelSelectionService(t, fake)

	// No request model and no bot default: a resumed turn
	// (ask_user / tool approval) must fall back to the model that produced
	// the session's latest round instead of erroring.
	req := ChatRequest{
		BotID:    "00000000-0000-0000-0000-000000000600",
		ThreadID: "00000000-0000-0000-0000-000000000603",
	}
	got, prov, err := resolver.selectChatModel(ctx, req, settings.Settings{})
	if err != nil {
		t.Fatalf("selectChatModel session fallback error = %v, want nil", err)
	}
	if got.ModelID != "gpt-session" {
		t.Fatalf("selectChatModel model_id = %q, want %q", got.ModelID, "gpt-session")
	}
	if prov.Name != provider.Name {
		t.Fatalf("selectChatModel provider = %q, want %q", prov.Name, provider.Name)
	}
}

func TestSelectChatModelWithoutAnyModelStillErrors(t *testing.T) {
	ctx := context.Background()
	fake := &modelSelectionFakeQueries{}
	resolver := newModelSelectionService(t, fake)

	req := ChatRequest{
		BotID:    "00000000-0000-0000-0000-000000000700",
		ThreadID: "00000000-0000-0000-0000-000000000701",
	}
	_, _, err := resolver.selectChatModel(ctx, req, settings.Settings{})
	if err == nil || !strings.Contains(err.Error(), "chat model not configured") {
		t.Fatalf("selectChatModel without any model error = %v, want chat model not configured", err)
	}
}

func TestFetchChatModelRejectsDisabledModel(t *testing.T) {
	ctx := context.Background()
	provider := modelSelectionProviderRow(t, "00000000-0000-0000-0000-000000000101", "openai-completions", true)
	model := modelSelectionModelRow(t, "00000000-0000-0000-0000-000000000102", "gpt-disabled", provider.ID, models.ModelTypeChat, false)
	fake := &modelSelectionFakeQueries{
		models:   map[string]sqlc.Model{model.ModelID: model},
		provider: provider,
	}
	resolver := newModelSelectionService(t, fake)

	_, _, err := resolver.fetchChatModel(ctx, "gpt-disabled")
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("fetchChatModel disabled model error = %v, want disabled error", err)
	}
}

func TestFetchChatModelRejectsDisabledProvider(t *testing.T) {
	ctx := context.Background()
	provider := modelSelectionProviderRow(t, "00000000-0000-0000-0000-000000000201", "openai-completions", false)
	model := modelSelectionModelRow(t, "00000000-0000-0000-0000-000000000202", "gpt-provider-disabled", provider.ID, models.ModelTypeChat, true)
	fake := &modelSelectionFakeQueries{
		models:   map[string]sqlc.Model{model.ModelID: model},
		provider: provider,
	}
	resolver := newModelSelectionService(t, fake)

	_, _, err := resolver.fetchChatModel(ctx, "gpt-provider-disabled")
	if err == nil || !strings.Contains(err.Error(), "provider") || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("fetchChatModel disabled provider error = %v, want provider disabled error", err)
	}
}

func TestFetchChatModelReturnsEnabledModelAndProvider(t *testing.T) {
	ctx := context.Background()
	provider := modelSelectionProviderRow(t, "00000000-0000-0000-0000-000000000301", "openai-completions", true)
	model := modelSelectionModelRow(t, "00000000-0000-0000-0000-000000000302", "gpt-enabled", provider.ID, models.ModelTypeChat, true)
	fake := &modelSelectionFakeQueries{
		models:   map[string]sqlc.Model{model.ModelID: model},
		provider: provider,
	}
	resolver := newModelSelectionService(t, fake)

	got, prov, err := resolver.fetchChatModel(ctx, "gpt-enabled")
	if err != nil {
		t.Fatalf("fetchChatModel enabled model error = %v, want nil", err)
	}
	if got.ModelID != "gpt-enabled" {
		t.Fatalf("fetchChatModel model_id = %q, want %q", got.ModelID, "gpt-enabled")
	}
	if got.ID != "00000000-0000-0000-0000-000000000302" {
		t.Fatalf("fetchChatModel id = %q, want %q", got.ID, "00000000-0000-0000-0000-000000000302")
	}
	if prov.Name != provider.Name {
		t.Fatalf("fetchChatModel provider = %q, want %q", prov.Name, provider.Name)
	}
	if !prov.Enable {
		t.Fatal("fetchChatModel returned disabled provider, want enabled")
	}
}

func TestFetchChatModelRejectsImageOnlyModel(t *testing.T) {
	ctx := context.Background()
	provider := modelSelectionProviderRow(t, "00000000-0000-0000-0000-000000000401", "openai-completions", true)
	model := modelSelectionModelRow(t, "00000000-0000-0000-0000-000000000402", "qwen-image", provider.ID, models.ModelTypeChat, true)
	model.Config = []byte(`{"compatibilities":["image-output"]}`)
	fake := &modelSelectionFakeQueries{
		models:   map[string]sqlc.Model{model.ModelID: model},
		provider: provider,
	}
	resolver := newModelSelectionService(t, fake)

	_, _, err := resolver.fetchChatModel(ctx, "qwen-image")
	if err == nil || !strings.Contains(err.Error(), "image generation model") || !strings.Contains(err.Error(), "bot image model") {
		t.Fatalf("fetchChatModel image-only model error = %v, want image model guidance", err)
	}
}

func TestFetchChatModelRejectsImportedImageModelWithoutCompatibility(t *testing.T) {
	ctx := context.Background()
	provider := modelSelectionProviderRow(t, "00000000-0000-0000-0000-000000000501", "openai-completions", true)
	model := modelSelectionModelRow(t, "00000000-0000-0000-0000-000000000502", "wan2.7-image-pro", provider.ID, models.ModelTypeChat, true)
	fake := &modelSelectionFakeQueries{
		models:   map[string]sqlc.Model{model.ModelID: model},
		provider: provider,
	}
	resolver := newModelSelectionService(t, fake)

	_, _, err := resolver.fetchChatModel(ctx, "wan2.7-image-pro")
	if err == nil || !strings.Contains(err.Error(), "image generation model") {
		t.Fatalf("fetchChatModel imported image model error = %v, want image model guidance", err)
	}
}

func TestValidateSelectedChatModelAllowsToolCallingImageOutputModel(t *testing.T) {
	t.Parallel()

	model := models.GetResponse{
		ModelID: "openrouter/auto",
		Model: models.Model{
			Type:   models.ModelTypeChat,
			Enable: true,
			Config: models.ModelConfig{
				Compatibilities: []string{models.CompatToolCall, models.CompatImageOutput},
			},
		},
	}
	if err := validateSelectedChatModel(model, sqlc.Provider{}); err != nil {
		t.Fatalf("validateSelectedChatModel() error = %v, want nil", err)
	}
}

func TestValidateSelectedChatModelAllowsGoogleImageOutputModel(t *testing.T) {
	t.Parallel()

	model := models.GetResponse{
		ModelID: "gemini-2.5-flash-image-preview",
		Model: models.Model{
			Type:   models.ModelTypeChat,
			Enable: true,
			Config: models.ModelConfig{
				Compatibilities: []string{models.CompatImageOutput},
			},
		},
	}
	provider := sqlc.Provider{ClientType: string(models.ClientTypeGoogleGenerativeAI)}
	if err := validateSelectedChatModel(model, provider); err != nil {
		t.Fatalf("validateSelectedChatModel() error = %v, want nil", err)
	}
}
