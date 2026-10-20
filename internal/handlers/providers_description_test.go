package handlers

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/providers"
	"github.com/felinics/memoh/internal/reasoning"
)

func descriptionPointer(value string) *string { return &value }

func TestMergeDiscoveredConfigFillsOnlyMissingDescription(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		existing   *string
		discovered *string
		want       *string
		changed    bool
	}{
		{name: "fills missing", discovered: descriptionPointer("Template"), want: descriptionPointer("Template"), changed: true},
		{name: "preserves user value", existing: descriptionPointer("Custom"), discovered: descriptionPointer("Template"), want: descriptionPointer("Custom")},
		{name: "preserves explicit clear", existing: descriptionPointer(""), discovered: descriptionPointer("Template"), want: descriptionPointer("")},
		{name: "ignores missing discovery"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := mergeDiscoveredConfig(
				models.LegacyModelConfig{Description: tt.existing},
				models.LegacyModelConfig{Description: tt.discovered},
			)
			if changed != tt.changed {
				t.Fatalf("changed = %v, want %v", changed, tt.changed)
			}
			if tt.want == nil {
				if got.Description != nil {
					t.Fatalf("description = %q, want nil", *got.Description)
				}
				return
			}
			if got.Description == nil || *got.Description != *tt.want {
				t.Fatalf("description = %v, want %q", got.Description, *tt.want)
			}
		})
	}
}

func TestImportedCompatibilitiesRequireExplicitDefaultsForUnknownModels(t *testing.T) {
	t.Parallel()

	unknown := providers.RemoteModel{ID: "custom-chat"}
	if got := importedCompatibilities(unknown, models.ModelTypeChat, nil, true); len(got) != 0 {
		t.Fatalf("unknown capabilities = %#v, want none", got)
	}
	if got := importedCompatibilities(unknown, models.ModelTypeChat, []string{models.CompatToolCall}, true); len(got) != 1 || got[0] != models.CompatToolCall {
		t.Fatalf("explicit defaults = %#v", got)
	}

	trusted := providers.RemoteModel{
		ID: "template-chat", CapabilitiesKnown: true, Compatibilities: []string{models.CompatToolCall},
	}
	if got := importedCompatibilities(trusted, models.ModelTypeChat, []string{models.CompatVision}, false); len(got) != 1 || got[0] != models.CompatToolCall {
		t.Fatalf("trusted capabilities = %#v, want template values", got)
	}
	if got := importedCompatibilities(providers.RemoteModel{ID: "template-without-capabilities"}, models.ModelTypeChat, []string{models.CompatVision}, false); len(got) != 0 {
		t.Fatalf("template without capabilities = %#v, want none", got)
	}
}

func TestPresetMetadataDisablesImplicitCapabilityDefaults(t *testing.T) {
	t.Parallel()

	unknown := providers.RemoteModel{ID: "provider-only-model"}
	provider := providers.GetResponse{
		Metadata: map[string]any{
			"preset": map[string]any{"source": "openai.yaml"},
		},
	}
	if got := importedCompatibilities(
		unknown,
		models.ModelTypeChat,
		[]string{models.CompatToolCall, models.CompatReasoning},
		allowCustomModelCapabilityDefaults(provider),
	); len(got) != 0 {
		t.Fatalf("preset mismatch capabilities = %#v, want none", got)
	}

	custom := providers.GetResponse{}
	if got := importedCompatibilities(
		unknown,
		models.ModelTypeChat,
		[]string{models.CompatToolCall},
		allowCustomModelCapabilityDefaults(custom),
	); len(got) != 1 || got[0] != models.CompatToolCall {
		t.Fatalf("custom provider defaults = %#v, want explicit default", got)
	}
}

func TestProviderTemplateIDDisablesImplicitCapabilityDefaults(t *testing.T) {
	t.Parallel()

	providersWithTemplates := []providers.GetResponse{
		{ProviderTemplateID: "template-id"},
		{Metadata: map[string]any{"registry": map[string]any{"source": "deepseek.yaml"}}},
	}
	for _, provider := range providersWithTemplates {
		if allowCustomModelCapabilityDefaults(provider) {
			t.Fatal("template provider must not receive implicit capability defaults")
		}
	}
}

func TestMergeManagedDiscoveredConfigReplacesCapabilities(t *testing.T) {
	t.Parallel()

	existing := models.LegacyModelConfig{
		Compatibilities:     []string{models.CompatVision, models.CompatToolCall, models.CompatReasoning},
		ReasoningEfforts:    []string{models.ReasoningEffortDisable, models.ReasoningEffortHigh},
		ThinkingMode:        models.ThinkingModeToggle,
		ReasoningDialect:    reasoning.DialectTier,
		ReasoningOffSupport: reasoning.OffSupportAccepted,
		ReasoningDefaultOn:  boolPointer(true),
		ThinkingBudgetMin:   intPointer(128),
		ThinkingBudgetMax:   intPointer(32768),
	}
	discovered := models.LegacyModelConfig{
		Compatibilities: []string{models.CompatToolCall},
		ThinkingMode:    models.ThinkingModeNone,
	}

	got, changed := mergeManagedDiscoveredConfig(existing, discovered)
	if !changed {
		t.Fatal("expected managed capability refresh to report a change")
	}
	if len(got.Compatibilities) != 1 || got.Compatibilities[0] != models.CompatToolCall {
		t.Fatalf("compatibilities = %#v", got.Compatibilities)
	}
	if len(got.ReasoningEfforts) != 0 {
		t.Fatalf("reasoning efforts = %#v, want empty", got.ReasoningEfforts)
	}
	if got.ThinkingMode != models.ThinkingModeNone {
		t.Fatalf("thinking mode = %q, want none", got.ThinkingMode)
	}
	if got.ReasoningDialect != "" || got.ReasoningOffSupport != "" || got.ReasoningDefaultOn != nil ||
		got.ThinkingBudgetMin != nil || got.ThinkingBudgetMax != nil {
		t.Fatalf("managed refresh kept revoked wire metadata: %+v", got)
	}
}

func TestMergeDiscoveredConfigRefreshesTrustedReasoningMetadata(t *testing.T) {
	t.Parallel()

	existing := models.LegacyModelConfig{
		Compatibilities:  []string{models.CompatReasoning},
		ReasoningEfforts: []string{models.ReasoningEffortDisable, models.ReasoningEffortLow},
	}
	discovered := models.LegacyModelConfig{
		ReasoningEfforts:    []string{models.ReasoningEffortLow, models.ReasoningEffortHigh},
		ThinkingMode:        models.ThinkingModeToggle,
		ReasoningDialect:    reasoning.DialectBudget,
		ReasoningOffSupport: reasoning.OffSupportRejected,
		ReasoningDefaultOn:  boolPointer(false),
		ThinkingBudgetMin:   intPointer(0),
		ThinkingBudgetMax:   intPointer(24576),
	}

	got, changed := mergeDiscoveredConfig(existing, discovered)
	if !changed {
		t.Fatal("expected trusted metadata refresh to report a change")
	}
	// Generic discovery cannot report out-of-band off support, so the template's
	// disable declaration survives while the active tiers refresh.
	if !slices.Equal(got.ReasoningEfforts, []string{
		models.ReasoningEffortDisable,
		models.ReasoningEffortLow,
		models.ReasoningEffortHigh,
	}) {
		t.Fatalf("reasoning efforts = %#v", got.ReasoningEfforts)
	}
	if got.ReasoningDialect != reasoning.DialectBudget || got.ReasoningOffSupport != reasoning.OffSupportRejected {
		t.Fatalf("wire declarations = dialect %q, off %q", got.ReasoningDialect, got.ReasoningOffSupport)
	}
	if got.ReasoningDefaultOn == nil || *got.ReasoningDefaultOn {
		t.Fatalf("reasoning_default_on = %v, want false", got.ReasoningDefaultOn)
	}
	if got.ThinkingBudgetMin == nil || *got.ThinkingBudgetMin != 0 || got.ThinkingBudgetMax == nil || *got.ThinkingBudgetMax != 24576 {
		t.Fatalf("budget range = %v..%v, want 0..24576", got.ThinkingBudgetMin, got.ThinkingBudgetMax)
	}
}

func boolPointer(value bool) *bool { return &value }
func intPointer(value int) *int    { return &value }

func TestMergeDiscoveredConfigRefreshesMaxOutputTokens(t *testing.T) {
	t.Parallel()

	existing := models.LegacyModelConfig{MaxOutputTokens: intPointer(4096)}

	got, changed := mergeDiscoveredConfig(existing, models.LegacyModelConfig{MaxOutputTokens: intPointer(8192)})
	if !changed || got.MaxOutputTokens == nil || *got.MaxOutputTokens != 8192 {
		t.Fatalf("max_output_tokens = %v changed = %v, want 8192 refreshed", got.MaxOutputTokens, changed)
	}
	got, changed = mergeDiscoveredConfig(existing, models.LegacyModelConfig{})
	if changed || got.MaxOutputTokens == nil || *got.MaxOutputTokens != 4096 {
		t.Fatalf("an endpoint that reports nothing must keep the stored value: %v changed = %v", got.MaxOutputTokens, changed)
	}
}

// A managed refresh reports the thinking mode of every model, "none" included.
// A model without reasoning must read the same back, or every refresh rewrites
// it and reports an update that changed nothing.
func TestMergeDiscoveredConfigLeavesNonReasoningModelsAlone(t *testing.T) {
	t.Parallel()

	existing := models.ModelConfig{ToolCall: true}.Legacy()
	discovered := models.LegacyModelConfig{ThinkingMode: reasoning.ModeNone, Compatibilities: []string{models.CompatToolCall}}
	if _, changed := mergeDiscoveredConfig(existing, discovered); changed {
		t.Fatal("a non-reasoning model changed on a refresh that discovered nothing new")
	}
	if _, changed := mergeManagedDiscoveredConfig(existing, discovered); changed {
		t.Fatal("a managed non-reasoning model changed on a refresh that discovered nothing new")
	}
}

func TestRefreshFromCatalogKeepsTheUsersChoices(t *testing.T) {
	t.Parallel()

	own := "My notes"
	window := 200000
	existing := models.ModelConfig{
		Description: &own,
		ToolCall:    false,
		Reasoning:   &models.ModelReasoning{Efforts: []string{"low"}},
	}
	catalogDescription := "Catalog text"
	catalog := models.ModelConfig{
		Description:   &catalogDescription,
		ToolCall:      true,
		ContextWindow: &window,
		Modalities:    &models.ModelModalities{Input: []models.Modality{models.ModalityText, models.ModalityImage}},
		Reasoning:     &models.ModelReasoning{Efforts: []string{"low", "high"}, Dialect: reasoning.DialectAdaptive},
	}

	got, changed := refreshFromCatalog("m", existing, catalog)
	if !changed || !got.ToolCall || got.ContextWindow == nil || *got.ContextWindow != window || !got.Accepts(models.ModalityImage) {
		t.Fatalf("refreshed = %+v, changed %v", got, changed)
	}
	if got.Description == nil || *got.Description != own {
		t.Fatalf("description = %v, want the user's", got.Description)
	}
	if _, again := refreshFromCatalog("m", got, catalog); again {
		t.Fatal("a second refresh with the same catalog reported a change")
	}

	empty, _ := refreshFromCatalog("m", models.ModelConfig{}, catalog)
	if empty.Description == nil || *empty.Description != catalogDescription {
		t.Fatalf("description = %v, want the catalog's when the model has none", empty.Description)
	}
}

// A gallery provider with a models.dev source imports that source's models, so
// the models its endpoint lists beyond them are as unknown as a template
// provider's and must not receive protocol-wide capability guesses. An entry
// without a source has only its endpoint, and its models get the defaults the
// gallery sends, as a custom provider's do.
func TestCatalogSourceDisablesImplicitCapabilityDefaults(t *testing.T) {
	t.Parallel()

	for _, metadata := range []map[string]any{
		{"catalog": "openai", "source": "openai"},
		{"source": "deepinfra"},
	} {
		if allowCustomModelCapabilityDefaults(providers.GetResponse{Metadata: metadata}) {
			t.Fatalf("provider with metadata %v must not receive implicit capability defaults", metadata)
		}
	}
	for _, metadata := range []map[string]any{
		{"catalog": "ollama"},
		{"catalog": "new-api"},
		{"source": " ", "other": "x"},
	} {
		if !allowCustomModelCapabilityDefaults(providers.GetResponse{Metadata: metadata}) {
			t.Fatalf("provider with metadata %v must receive the capability defaults", metadata)
		}
	}
}

// A refresh cannot tell the user's edits from what an earlier import wrote, so
// it never takes a capability away, and keeps what the edit form tunes while
// the catalog's controls still use it.
func TestRefreshFromCatalogKeepsTheUsersEdits(t *testing.T) {
	t.Parallel()

	minBudget, maxBudget, catalogMax, window := 1024, 8192, 32000, 400000
	enabled := true
	existing := models.ModelConfig{
		ToolCall:   true,
		Modalities: &models.ModelModalities{Input: []models.Modality{models.ModalityText, models.ModalityAudio}, Output: []models.Modality{models.ModalityText}},
		Reasoning: &models.ModelReasoning{
			Toggle:  true,
			Efforts: []string{"low", "medium"},
			Budget:  &models.ReasoningBudget{Min: &minBudget, Max: &maxBudget},
			Default: &models.ReasoningDefault{Enabled: &enabled, Effort: "medium"},
			Replay:  &models.ReasoningReplay{Field: models.ReasoningReplayDetails},
		},
	}

	t.Run("budget catalog keeps the user's range", func(t *testing.T) {
		catalog := models.ModelConfig{
			ContextWindow: &window,
			Modalities:    &models.ModelModalities{Input: []models.Modality{models.ModalityText, models.ModalityImage}, Output: []models.Modality{models.ModalityText}},
			Reasoning:     &models.ModelReasoning{Toggle: true, Off: reasoning.OffSupportAccepted, Dialect: reasoning.DialectBudget, Budget: &models.ReasoningBudget{Max: &catalogMax}},
		}
		got, changed := refreshFromCatalog("m", existing, catalog)
		if !changed || !got.ToolCall || !got.Accepts(models.ModalityAudio) || !got.Accepts(models.ModalityImage) ||
			got.ContextWindow == nil || *got.ContextWindow != window {
			t.Fatalf("refreshed = %+v, changed %v", got, changed)
		}
		r := got.Reasoning
		if r == nil || r.Dialect != reasoning.DialectBudget || r.Budget == nil || *r.Budget.Max != maxBudget ||
			r.Default == nil || r.Default.Enabled == nil || r.Default.Effort != "" ||
			r.Replay == nil || r.Replay.Field != models.ReasoningReplayDetails {
			t.Fatalf("reasoning = %+v, default %+v", r, r.Default)
		}
	})

	t.Run("tier catalog drops what its controls cannot use", func(t *testing.T) {
		catalog := models.ModelConfig{Reasoning: &models.ModelReasoning{Efforts: []string{"low", "high"}, Dialect: reasoning.DialectTier}}
		got, _ := refreshFromCatalog("m", existing, catalog)
		r := got.Reasoning
		if r == nil || r.Budget != nil || r.Default != nil || !slices.Equal(r.Efforts, []string{"low", "high"}) || r.Replay == nil {
			t.Fatalf("reasoning = %+v, default %+v", r, r.Default)
		}
	})

	t.Run("a catalog without reasoning or tool calling takes nothing away", func(t *testing.T) {
		kept, _ := refreshFromCatalog("m", existing, models.ModelConfig{ContextWindow: &window})
		if kept.Reasoning == nil || !kept.ToolCall {
			t.Fatalf("refreshed = %+v", kept)
		}
	})
}

// A catalog refresh compares the stored row with what the request carried.
// Both go through the same decoding, so a catalog entry with blank or repeated
// values reads as the row it produced and a second refresh changes nothing.
func TestRefreshFromCatalogIsStableAcrossNormalization(t *testing.T) {
	t.Parallel()

	var req providers.ImportModelsRequest
	raw := `{"models":[{"model_id":"m","config":{"schema_version":2,"tool_call":true,"reasoning":{"efforts":["","low","low","high"]}}}]}`
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatal(err)
	}
	catalog := req.Models[0].Config
	stored, changed := refreshFromCatalog("m", models.ModelConfig{}, catalog)
	if !changed || stored.Reasoning == nil || !slices.Equal(stored.Reasoning.Efforts, []string{"low", "high"}) {
		t.Fatalf("first refresh = %+v changed %v", stored, changed)
	}
	if _, again := refreshFromCatalog("m", stored, catalog); again {
		t.Fatal("a second refresh with the same catalog entry reported a change")
	}
}

// Create and Update store a config with the dialect its model id implies when
// the catalog declares none. A refresh must compare against that stored form,
// or every Claude thinking row reads as changed on every refresh.
func TestRefreshFromCatalogComparesTheStoredForm(t *testing.T) {
	t.Parallel()

	catalog := models.ModelConfig{ToolCall: true, Reasoning: &models.ModelReasoning{}}
	stored := catalog.StoredForm("claude-opus-5-thinking")
	if stored.Reasoning == nil || stored.Reasoning.Dialect != reasoning.DialectAdaptive {
		t.Fatalf("stored form = %+v, want the adaptive dialect inferred", stored.Reasoning)
	}
	if _, changed := refreshFromCatalog("claude-opus-5-thinking", stored, catalog); changed {
		t.Fatal("a refresh with the catalog entry the row was created from reported a change")
	}
}

// Discovery lists the disable token first, while the version 1 reading of a
// stored model puts it last, so the version 1 merge reports a change on every
// refresh of such a model. Whether the refresh writes is decided on the config
// it would store, which is the same.
func TestRefreshOfAnUnchangedDisableFirstModelStoresNothingNew(t *testing.T) {
	t.Parallel()

	discovered := models.LegacyModelConfig{
		Compatibilities:  []string{models.CompatToolCall, models.CompatReasoning},
		ThinkingMode:     reasoning.ModeToggle,
		ReasoningEfforts: []string{reasoning.EffortDisable, reasoning.EffortLow, reasoning.EffortHigh, reasoning.EffortMax},
	}
	ctx := models.ConfigContext{ModelID: "m", Type: models.ModelTypeChat, ClientType: string(models.ClientTypeOpenAICompletions)}
	existing := discovered.Upgrade(ctx).StoredForm(ctx.ModelID)
	for name, merge := range map[string]func(existing, discovered models.LegacyModelConfig) (models.LegacyModelConfig, bool){
		"discovered": mergeDiscoveredConfig,
		"managed":    mergeManagedDiscoveredConfig,
	} {
		merged, _ := merge(existing.Legacy(), discovered)
		next := models.MergeLegacyConfig(existing, merged, ctx)
		if !sameStoredConfig(ctx.ModelID, existing, next) {
			t.Errorf("%s: an unchanged model would be rewritten:\nstored %+v\nnext   %+v", name, existing.Reasoning, next.Reasoning)
		}
	}
	changed := discovered
	changed.ReasoningEfforts = []string{reasoning.EffortDisable, reasoning.EffortLow}
	merged, _ := mergeManagedDiscoveredConfig(existing.Legacy(), changed)
	if sameStoredConfig(ctx.ModelID, existing, models.MergeLegacyConfig(existing, merged, ctx)) {
		t.Fatal("a changed effort list read as the same stored config")
	}
}
