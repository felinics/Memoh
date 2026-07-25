package application

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/memohai/memoh/domains/api/setting"
	modeldomain "github.com/memohai/memoh/domains/model"
	modelcatalog "github.com/memohai/memoh/domains/model/catalog"
	modelexecution "github.com/memohai/memoh/domains/model/execution"
)

func TestOffEffortFor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		levels []string
		want   string
	}{
		{"none wins", []string{modeldomain.ReasoningEffortNone, "low", "medium"}, modeldomain.ReasoningEffortNone},
		{"minimal when no none", []string{modeldomain.ReasoningEffortMinimal, "low", "medium"}, modeldomain.ReasoningEffortMinimal},
		{"empty when only real tiers (omit, do not enable)", []string{"medium", "high", "xhigh"}, ""},
		{"legacy base yields empty (omit reasoning_effort)", []string{"low", "medium", "high"}, ""},
		{"empty levels yield empty", nil, ""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := offEffortFor(tt.levels); got != tt.want {
				t.Fatalf("offEffortFor(%v) = %q, want %q", tt.levels, got, tt.want)
			}
		})
	}
}

func TestMatchesModelReference_ModelID(t *testing.T) {
	t.Parallel()

	model := modelcatalog.GetResponse{
		ID:      "a55f0d2d-1547-49a0-b085-ec4ab778f4b8",
		ModelID: "gpt-4o",
	}

	if !matchesModelReference(model, "gpt-4o") {
		t.Fatal("expected model slug to match")
	}
}

func TestMatchesModelReference_UUID(t *testing.T) {
	t.Parallel()

	model := modelcatalog.GetResponse{
		ID:      "a55f0d2d-1547-49a0-b085-ec4ab778f4b8",
		ModelID: "gpt-4o",
	}

	if !matchesModelReference(model, "a55f0d2d-1547-49a0-b085-ec4ab778f4b8") {
		t.Fatal("expected model UUID to match")
	}
}

func TestMatchesModelReference_NoMatch(t *testing.T) {
	t.Parallel()

	model := modelcatalog.GetResponse{
		ID:      "a55f0d2d-1547-49a0-b085-ec4ab778f4b8",
		ModelID: "gpt-4o",
	}

	if matchesModelReference(model, "gpt-4.1") {
		t.Fatal("expected non-matching model reference to fail")
	}
}

func TestMatchesModelReference_TrimmedInput(t *testing.T) {
	t.Parallel()

	model := modelcatalog.GetResponse{
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

	visionModel := modelcatalog.GetResponse{
		Model: modeldomain.Model{
			Config: modeldomain.ModelConfig{
				Compatibilities: []string{modeldomain.CompatVision},
			},
		},
	}
	if !supportsImageInputForModel(visionModel) {
		t.Fatal("vision-compatible model should support image input")
	}

	plainModel := modelcatalog.GetResponse{}
	if supportsImageInputForModel(plainModel) {
		t.Fatal("model without vision compatibility should not support image input")
	}
}

func TestResolveReasoningConfig(t *testing.T) {
	t.Parallel()

	// Legacy data: reasoning compat without an explicit thinking_mode resolves to
	// toggle via the SupportsReasoning/ResolveThinkingMode bridge.
	toggleModel := modelcatalog.GetResponse{
		Model: modeldomain.Model{
			Config: modeldomain.ModelConfig{
				Compatibilities: []string{modeldomain.CompatReasoning},
			},
		},
	}
	// Adaptive-capable model (Claude 4.6+ family): user can turn thinking off,
	// but when enabled it uses adaptive thinking.
	adaptiveModel := modelcatalog.GetResponse{
		Model: modeldomain.Model{
			Config: modeldomain.ModelConfig{
				ThinkingMode:     modeldomain.ThinkingModeAdaptive,
				ReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max"},
			},
		},
	}
	codexModel := modelcatalog.GetResponse{
		Model: modeldomain.Model{
			Config: modeldomain.ModelConfig{
				ThinkingMode:     modeldomain.ThinkingModeToggle,
				ReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max"},
			},
		},
	}
	noneEffortModel := modelcatalog.GetResponse{
		Model: modeldomain.Model{
			Config: modeldomain.ModelConfig{
				ThinkingMode:     modeldomain.ThinkingModeToggle,
				ReasoningEfforts: []string{"none", "minimal", "low", "medium", "high"},
			},
		},
	}
	// Legacy Anthropic (<=4.5): toggle mode advertising only the implicit
	// low/medium/high base. On the Anthropic wire this must stay non-adaptive so
	// the SDK sends thinking{type:"enabled", budget_tokens:N}.
	legacyAnthropicModel := modelcatalog.GetResponse{
		Model: modeldomain.Model{
			Config: modeldomain.ModelConfig{
				ThinkingMode:     modeldomain.ThinkingModeToggle,
				ReasoningEfforts: []string{"low", "medium", "high"},
			},
		},
	}
	// Cloud-variant Claude 4.6+: the registry left it toggle (no
	// supports_adaptive_thinking) but it advertises 4.6+ effort tiers, so the
	// Anthropic wire promotes it to adaptive to stay off the legacy budget path.
	cloudEffortModel := modelcatalog.GetResponse{
		Model: modeldomain.Model{
			Config: modeldomain.ModelConfig{
				ThinkingMode:     modeldomain.ThinkingModeToggle,
				ReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max"},
			},
		},
	}
	plainModel := modelcatalog.GetResponse{}

	tests := []struct {
		name          string
		model         modelcatalog.GetResponse
		botSettings   setting.Settings
		requestEffort string
		clientType    string
		want          *modelexecution.ReasoningConfig
	}{
		{
			name:          "disable overrides bot default",
			model:         toggleModel,
			botSettings:   setting.Settings{ReasoningEnabled: true, ReasoningEffort: modeldomain.ReasoningEffortHigh},
			requestEffort: reasoningEffortDisable,
			want:          &modelexecution.ReasoningConfig{Disabled: true},
		},
		{
			name:          "legacy adaptive request enables toggle with default effort",
			model:         toggleModel,
			requestEffort: reasoningEffortAdaptive,
			want:          &modelexecution.ReasoningConfig{Active: true, Effort: modeldomain.ReasoningEffortMedium},
		},
		{
			name:          "unsupported none effort falls back to bot default",
			model:         toggleModel,
			botSettings:   setting.Settings{ReasoningEnabled: true, ReasoningEffort: modeldomain.ReasoningEffortHigh},
			requestEffort: modeldomain.ReasoningEffortNone,
			want:          &modelexecution.ReasoningConfig{Active: true, Effort: modeldomain.ReasoningEffortHigh},
		},
		{
			name:          "explicit none effort is preserved when model supports it",
			model:         noneEffortModel,
			botSettings:   setting.Settings{ReasoningEnabled: true, ReasoningEffort: modeldomain.ReasoningEffortHigh},
			requestEffort: modeldomain.ReasoningEffortNone,
			want:          &modelexecution.ReasoningConfig{Active: true, Effort: modeldomain.ReasoningEffortNone},
		},
		{
			name:          "explicit effort is trimmed",
			model:         toggleModel,
			requestEffort: " low ",
			want:          &modelexecution.ReasoningConfig{Active: true, Effort: modeldomain.ReasoningEffortLow},
		},
		{
			name:        "bot default is used when no request override",
			model:       toggleModel,
			botSettings: setting.Settings{ReasoningEnabled: true, ReasoningEffort: " high "},
			want:        &modelexecution.ReasoningConfig{Active: true, Effort: modeldomain.ReasoningEffortHigh},
		},
		{
			name:        "bot default falls back to medium",
			model:       toggleModel,
			botSettings: setting.Settings{ReasoningEnabled: true},
			want:        &modelexecution.ReasoningConfig{Active: true, Effort: modeldomain.ReasoningEffortMedium},
		},
		{
			name:        "disabled bot explicitly disables reasoning",
			model:       toggleModel,
			botSettings: setting.Settings{ReasoningEnabled: false, ReasoningEffort: modeldomain.ReasoningEffortHigh},
			want:        &modelexecution.ReasoningConfig{Disabled: true},
		},
		{
			name:          "adaptive model can still be disabled",
			model:         adaptiveModel,
			requestEffort: reasoningEffortDisable,
			want:          &modelexecution.ReasoningConfig{Disabled: true},
		},
		{
			name:          "adaptive model honors explicit effort",
			model:         adaptiveModel,
			requestEffort: modeldomain.ReasoningEffortXHigh,
			want:          &modelexecution.ReasoningConfig{Active: true, Adaptive: true, Effort: modeldomain.ReasoningEffortXHigh},
		},
		{
			name:          "generic openai compatibility drops max and falls back to medium",
			model:         adaptiveModel,
			requestEffort: modeldomain.ReasoningEffortMax,
			clientType:    string(modeldomain.ClientTypeOpenAICompletions),
			want:          &modelexecution.ReasoningConfig{Active: true, Adaptive: true, Effort: modeldomain.ReasoningEffortMedium},
		},
		{
			name:          "codex wire preserves max",
			model:         codexModel,
			requestEffort: modeldomain.ReasoningEffortMax,
			clientType:    string(modeldomain.ClientTypeOpenAICodex),
			want:          &modelexecution.ReasoningConfig{Active: true, Effort: modeldomain.ReasoningEffortMax},
		},
		{
			name:          "anthropic wire preserves max",
			model:         adaptiveModel,
			requestEffort: modeldomain.ReasoningEffortMax,
			clientType:    string(modeldomain.ClientTypeAnthropicMessages),
			want:          &modelexecution.ReasoningConfig{Active: true, Adaptive: true, Effort: modeldomain.ReasoningEffortMax},
		},
		{
			name:        "legacy anthropic stays non-adaptive for budget path",
			model:       legacyAnthropicModel,
			botSettings: setting.Settings{ReasoningEnabled: true, ReasoningEffort: modeldomain.ReasoningEffortHigh},
			clientType:  string(modeldomain.ClientTypeAnthropicMessages),
			want:        &modelexecution.ReasoningConfig{Active: true, Effort: modeldomain.ReasoningEffortHigh},
		},
		{
			name:        "anthropic cloud variant with effort tiers is promoted to adaptive",
			model:       cloudEffortModel,
			botSettings: setting.Settings{ReasoningEnabled: true, ReasoningEffort: modeldomain.ReasoningEffortHigh},
			clientType:  string(modeldomain.ClientTypeAnthropicMessages),
			want:        &modelexecution.ReasoningConfig{Active: true, Adaptive: true, Effort: modeldomain.ReasoningEffortHigh},
		},
		{
			name:        "non-anthropic effort tiers are not promoted to adaptive",
			model:       cloudEffortModel,
			botSettings: setting.Settings{ReasoningEnabled: true, ReasoningEffort: modeldomain.ReasoningEffortHigh},
			clientType:  string(modeldomain.ClientTypeOpenAICompletions),
			want:        &modelexecution.ReasoningConfig{Active: true, Effort: modeldomain.ReasoningEffortHigh},
		},
		{
			name:          "model without reasoning ignores request",
			model:         plainModel,
			requestEffort: modeldomain.ReasoningEffortHigh,
			want:          nil,
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

// modelSelectionFakeQueries is an in-memory model/provider/session reader fake.
type modelSelectionFakeQueries struct {
	models         map[string]modelcatalog.Record
	provider       modelcatalog.ResolvedProvider
	sessionModelID string
}

func (f *modelSelectionFakeQueries) ResolveModelProvider(_ context.Context, id string) (modelcatalog.ResolvedProvider, error) {
	if id != f.provider.ID {
		return modelcatalog.ResolvedProvider{}, pgx.ErrNoRows
	}
	return f.provider, nil
}

func (f *modelSelectionFakeQueries) LatestSessionModelID(_ context.Context, _ string) (string, error) {
	if f.sessionModelID == "" {
		return "", pgx.ErrNoRows
	}
	return f.sessionModelID, nil
}

type modelSelectionStore struct {
	modelcatalog.Store
	models map[string]modelcatalog.Record
}

func (s modelSelectionStore) GetByID(_ context.Context, id string) (modelcatalog.Record, error) {
	for _, model := range s.models {
		if model.ID == id {
			return model, nil
		}
	}
	return modelcatalog.Record{}, modelcatalog.ErrModelNotFound
}

func (s modelSelectionStore) GetByModelID(_ context.Context, modelID string) (modelcatalog.Record, error) {
	model, ok := s.models[modelID]
	if !ok {
		return modelcatalog.Record{}, modelcatalog.ErrModelNotFound
	}
	return model, nil
}

func newModelSelectionService(t *testing.T, fake *modelSelectionFakeQueries) *Service {
	t.Helper()
	return &Service{
		modelsService:         modelcatalog.NewService(slog.New(slog.DiscardHandler), modelSelectionStore{models: fake.models}),
		modelProviderResolver: fake,
		latestSessionModels:   fake,
	}
}

func modelSelectionProviderRow(_ *testing.T, id string, clientType string, enable bool) modelcatalog.ResolvedProvider {
	return modelcatalog.ResolvedProvider{
		ID:         id,
		Name:       "provider-" + id,
		ClientType: modeldomain.ClientType(clientType),
		Enable:     enable,
	}
}

func modelSelectionModelRow(_ *testing.T, id string, modelID string, providerID string, modelType modeldomain.ModelType, enable bool) modelcatalog.Record {
	return modelcatalog.Record{
		ID:         id,
		ModelID:    modelID,
		Name:       modelID,
		ProviderID: providerID,
		Type:       modelType,
		Enable:     enable,
		Config:     []byte(`{}`),
	}
}

func TestSelectChatModelFallsBackToSessionLastModel(t *testing.T) {
	ctx := context.Background()
	provider := modelSelectionProviderRow(t, "00000000-0000-0000-0000-000000000601", "openai-completions", true)
	model := modelSelectionModelRow(t, "00000000-0000-0000-0000-000000000602", "gpt-session", provider.ID, modeldomain.ModelTypeChat, true)
	fake := &modelSelectionFakeQueries{
		models:         map[string]modelcatalog.Record{model.ModelID: model},
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
	got, prov, err := resolver.selectChatModel(ctx, req, setting.Settings{})
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
	_, _, err := resolver.selectChatModel(ctx, req, setting.Settings{})
	if err == nil || !strings.Contains(err.Error(), "chat model not configured") {
		t.Fatalf("selectChatModel without any model error = %v, want chat model not configured", err)
	}
}

func TestFetchChatModelRejectsDisabledModel(t *testing.T) {
	ctx := context.Background()
	provider := modelSelectionProviderRow(t, "00000000-0000-0000-0000-000000000101", "openai-completions", true)
	model := modelSelectionModelRow(t, "00000000-0000-0000-0000-000000000102", "gpt-disabled", provider.ID, modeldomain.ModelTypeChat, false)
	fake := &modelSelectionFakeQueries{
		models:   map[string]modelcatalog.Record{model.ModelID: model},
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
	model := modelSelectionModelRow(t, "00000000-0000-0000-0000-000000000202", "gpt-provider-disabled", provider.ID, modeldomain.ModelTypeChat, true)
	fake := &modelSelectionFakeQueries{
		models:   map[string]modelcatalog.Record{model.ModelID: model},
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
	model := modelSelectionModelRow(t, "00000000-0000-0000-0000-000000000302", "gpt-enabled", provider.ID, modeldomain.ModelTypeChat, true)
	fake := &modelSelectionFakeQueries{
		models:   map[string]modelcatalog.Record{model.ModelID: model},
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
	model := modelSelectionModelRow(t, "00000000-0000-0000-0000-000000000402", "qwen-image", provider.ID, modeldomain.ModelTypeChat, true)
	model.Config = []byte(`{"compatibilities":["image-output"]}`)
	fake := &modelSelectionFakeQueries{
		models:   map[string]modelcatalog.Record{model.ModelID: model},
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
	model := modelSelectionModelRow(t, "00000000-0000-0000-0000-000000000502", "wan2.7-image-pro", provider.ID, modeldomain.ModelTypeChat, true)
	fake := &modelSelectionFakeQueries{
		models:   map[string]modelcatalog.Record{model.ModelID: model},
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

	model := modelcatalog.GetResponse{
		ModelID: "openrouter/auto",
		Model: modeldomain.Model{
			Type:   modeldomain.ModelTypeChat,
			Enable: true,
			Config: modeldomain.ModelConfig{
				Compatibilities: []string{modeldomain.CompatToolCall, modeldomain.CompatImageOutput},
			},
		},
	}
	if err := validateSelectedChatModel(model, modelcatalog.ResolvedProvider{}); err != nil {
		t.Fatalf("validateSelectedChatModel() error = %v, want nil", err)
	}
}

func TestValidateSelectedChatModelAllowsGoogleImageOutputModel(t *testing.T) {
	t.Parallel()

	model := modelcatalog.GetResponse{
		ModelID: "gemini-2.5-flash-image-preview",
		Model: modeldomain.Model{
			Type:   modeldomain.ModelTypeChat,
			Enable: true,
			Config: modeldomain.ModelConfig{
				Compatibilities: []string{modeldomain.CompatImageOutput},
			},
		},
	}
	provider := modelcatalog.ResolvedProvider{ClientType: modeldomain.ClientTypeGoogleGenerativeAI}
	if err := validateSelectedChatModel(model, provider); err != nil {
		t.Fatalf("validateSelectedChatModel() error = %v, want nil", err)
	}
}

func TestIsKnownStandaloneImageModelID(t *testing.T) {
	t.Parallel()

	for _, id := range []string{
		"qwen-image-2.0", "wan2.7-image", "z-image-turbo",
		"flux-schnell", "stable-diffusion-3.5-large-turbo",
		"gpt-image-1", "dall-e-3", "doubao-seedream-4-0-250828",
	} {
		if !isKnownStandaloneImageModelID(id) {
			t.Errorf("isKnownStandaloneImageModelID(%q) = false, want true", id)
		}
	}
	for _, id := range []string{
		"gpt-4o", "qwen-max", "deepseek-chat", "",
		// Chat models that merely share a leading token must not match: the
		// "wan"/"flux" prefixes are scoped to image-model naming conventions.
		"wanjuan-chat", "want-to-talk", "fluxion-7b", "fluent-chat",
	} {
		if isKnownStandaloneImageModelID(id) {
			t.Errorf("isKnownStandaloneImageModelID(%q) = true, want false", id)
		}
	}
}

func TestIsImageOnlyChatModelToolCallEscape(t *testing.T) {
	t.Parallel()

	// A model whose name looks like an image model but which advertises tool
	// calling must not be classified as image-only — tool calling is the
	// override that lets a name collision be used as a chat model.
	toolCaller := modelcatalog.GetResponse{
		ModelID: "wan2.7-omni",
		Model: modeldomain.Model{
			Config: modeldomain.ModelConfig{Compatibilities: []string{modeldomain.CompatToolCall, modeldomain.CompatImageOutput}},
		},
	}
	if isImageOnlyChatModel(toolCaller, modelcatalog.ResolvedProvider{}) {
		t.Fatal("a tool-calling model must not be treated as image-only, even with an image-like name")
	}

	// Without tool calling, the same name is still rejected.
	imageOnly := modelcatalog.GetResponse{
		ModelID: "wan2.7-image",
		Model:   modeldomain.Model{Config: modeldomain.ModelConfig{Compatibilities: []string{modeldomain.CompatImageOutput}}},
	}
	if !isImageOnlyChatModel(imageOnly, modelcatalog.ResolvedProvider{}) {
		t.Fatal("a non-tool-calling image model name should be treated as image-only")
	}
}
