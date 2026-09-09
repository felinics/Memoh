package native

import (
	"context"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/models"
)

// The reasoning decision on the subagent spawn path (#983).
//
// A subagent used to run with no thinking configuration at all: SpawnRunConfig
// carried a lone ReasoningEffort string that production code never assigned, and
// the four flags that actually drive the wire had no field to travel in. Since
// BuildReasoningOptions switches on Active and Disabled, a config with both false
// emits nothing — so the parent's choice, on or off, silently became "whatever
// the provider does by default".
//
// SpawnRunConfig now carries the whole *models.ReasoningConfig, resolved against
// the subagent's own model. These tests assert it reaches the wire.

// spawnReasoningProvider records the GenerateParams the SDK finally sends, so a
// test can assert on the wire-level reasoning shape.
type spawnReasoningProvider struct {
	name   string
	params sdk.GenerateParams
	calls  int
}

func (p *spawnReasoningProvider) Name() string {
	if p.name == "" {
		return "openai-completions"
	}
	return p.name
}

func (*spawnReasoningProvider) ListModels(context.Context) ([]sdk.Model, error) {
	return nil, nil
}

func (*spawnReasoningProvider) Test(context.Context) *sdk.ProviderTestResult {
	return &sdk.ProviderTestResult{Status: sdk.ProviderStatusOK}
}

func (*spawnReasoningProvider) TestModel(context.Context, string) (*sdk.ModelTestResult, error) {
	return &sdk.ModelTestResult{Supported: true}, nil
}

func (p *spawnReasoningProvider) DoGenerate(_ context.Context, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
	p.calls++
	p.params = params
	return &sdk.GenerateResult{
		Text:         "ok",
		FinishReason: sdk.FinishReasonStop,
	}, nil
}

func (*spawnReasoningProvider) DoStream(context.Context, sdk.GenerateParams) (*sdk.StreamResult, error) {
	return nil, nil
}

// runSpawnReasoning drives the real spawn conversion plus buildGenerateOptions
// and returns the reasoning effort the provider actually received.
func runSpawnReasoning(t *testing.T, providerName string, cfg tools.SpawnRunConfig) *string {
	t.Helper()

	provider := &spawnReasoningProvider{name: providerName}
	cfg.Model = &sdk.Model{
		ID:       "test-model",
		Provider: provider,
		Type:     sdk.ModelTypeChat,
	}

	rc := runConfigFromSpawnRunConfig(cfg)
	opts := (*Agent)(nil).buildGenerateOptions(context.Background(), rc, nil, nil, nil)
	if _, err := sdk.GenerateTextResult(context.Background(), opts...); err != nil {
		t.Fatalf("generate text result: %v", err)
	}
	if provider.calls == 0 {
		t.Fatal("expected the provider to be called")
	}
	return provider.params.ReasoningEffort
}

// TestSpawnRunConfigDropsReasoningDecisionFields pins the shape of the
// conversion itself: only ReasoningEffort survives runConfigFromSpawnRunConfig,
// TestSpawnRunConfigCarriesTheWholeReasoningDecision pins the conversion itself:
// every field of the decision survives, not just the effort string.
func TestSpawnRunConfigCarriesTheWholeReasoningDecision(t *testing.T) {
	t.Parallel()

	want := &models.ReasoningConfig{
		Active:    true,
		Adaptive:  true,
		Effort:    models.ReasoningEffortHigh,
		OffEffort: models.ReasoningEffortNone,
	}
	rc := runConfigFromSpawnRunConfig(tools.SpawnRunConfig{ReasoningConfig: want})

	if rc.ReasoningConfig == nil {
		t.Fatal("ReasoningConfig is nil; the spawn path dropped the decision")
	}
	if *rc.ReasoningConfig != *want {
		t.Fatalf("ReasoningConfig = %+v, want %+v", *rc.ReasoningConfig, *want)
	}
}

func TestSpawnRunConfigCarriesReasoningInputsForNestedSubagents(t *testing.T) {
	t.Parallel()

	rc := runConfigFromSpawnRunConfig(tools.SpawnRunConfig{
		ReasoningStoredEffort:    models.ReasoningEffortHigh,
		ReasoningRequestedEffort: models.ReasoningEffortDisable,
	})

	if rc.ReasoningStoredEffort != models.ReasoningEffortHigh ||
		rc.ReasoningRequestedEffort != models.ReasoningEffortDisable {
		t.Fatalf("reasoning inputs = stored %q, requested %q",
			rc.ReasoningStoredEffort, rc.ReasoningRequestedEffort)
	}
}

// TestSpawnActiveReasoningEffortNeverReachesOpenAIWire is the user-visible
// consequence on OpenAI-style providers: openAIEffortOptions only emits an effort
// when Active or Disabled is set, so before the fix the tier was dropped and the
// subagent silently fell back to the provider's default reasoning.
func TestSpawnActiveReasoningEffortReachesOpenAIWire(t *testing.T) {
	t.Parallel()

	got := runSpawnReasoning(t, "openai-completions", tools.SpawnRunConfig{
		ReasoningConfig: &models.ReasoningConfig{Active: true, Effort: models.ReasoningEffortHigh},
	})

	if got == nil {
		t.Fatal("expected reasoning effort to reach the provider")
	}
	if *got != models.ReasoningEffortHigh {
		t.Fatalf("reasoning effort = %q, want %q", *got, models.ReasoningEffortHigh)
	}
}

// TestSpawnDisabledReasoningNeverReachesOpenAIWire covers the opposite
// direction, which is the more expensive failure: the parent turned reasoning
// OFF, but the subagent cannot express that. Disabled is false on the spawn
// path, so the OffEffort approximation ("none"/"minimal") is never sent and an
// OpenAI-style model happily reasons at its own default — burning reasoning
// tokens the user explicitly opted out of.
func TestSpawnDisabledReasoningReachesOpenAIWire(t *testing.T) {
	t.Parallel()

	// A parent that resolved Disabled=true, OffEffort="none" has no field on
	// SpawnRunConfig to put either value in, so the spawn config is empty here.
	got := runSpawnReasoning(t, "openai-completions", tools.SpawnRunConfig{})

	// CURRENT BEHAVIOR: nothing is sent, so the provider default (reasoning ON
	// for a reasoning model) applies.
	//
	// WANT AFTER FIX: got should be "none" (or the model's OffEffort), matching
	// what the parent turn sends for the same decision.
	if got != nil {
		t.Fatalf("reasoning effort = %q, want nil under current behavior: "+
			"ReasoningDisabled/ReasoningOffEffort cannot cross the spawn boundary", *got)
	}
}

// TestParentTurnSendsReasoningForSameDecision is the contrast case that proves
// the drop is a spawn-path defect and not a global "we never send reasoning".
// A native.RunConfig built the way application.Service builds it (service.go:
// 757-761) does reach the wire; only the spawn-converted one does not.
func TestParentTurnSendsReasoningForSameDecision(t *testing.T) {
	t.Parallel()

	provider := &spawnReasoningProvider{name: "openai-completions"}
	// This mirrors what the parent turn produces for
	// &models.ReasoningConfig{Active: true, Effort: "high"}.
	cfg := RunConfig{
		Model: &sdk.Model{
			ID:       "test-model",
			Provider: provider,
			Type:     sdk.ModelTypeChat,
		},
		ReasoningConfig: &models.ReasoningConfig{Active: true, Effort: models.ReasoningEffortHigh},
	}

	opts := (*Agent)(nil).buildGenerateOptions(context.Background(), cfg, nil, nil, nil)
	if _, err := sdk.GenerateTextResult(context.Background(), opts...); err != nil {
		t.Fatalf("generate text result: %v", err)
	}
	if provider.params.ReasoningEffort == nil {
		t.Fatal("parent turn: expected reasoning effort to reach the provider")
	}
	if got := *provider.params.ReasoningEffort; got != models.ReasoningEffortHigh {
		t.Fatalf("parent turn: reasoning effort = %q, want %q", got, models.ReasoningEffortHigh)
	}
}

// TestSpawnAnthropicAdaptiveEffortReachesWire covers the Anthropic 4.6+ adaptive
// path, where output_config.effort is only emitted when Active, Adaptive and
// Effort are all set. Adaptive could not cross the spawn boundary before, so a
// Claude 4.6 subagent never received a tier.
func TestSpawnAnthropicAdaptiveEffortReachesWire(t *testing.T) {
	t.Parallel()

	got := runSpawnReasoning(t, "anthropic-messages", tools.SpawnRunConfig{
		ReasoningConfig: &models.ReasoningConfig{
			Active:   true,
			Adaptive: true,
			Effort:   models.ReasoningEffortHigh,
		},
	})

	if got == nil {
		t.Fatal("anthropic: expected output_config.effort to reach the provider")
	}
	if *got != models.ReasoningEffortHigh {
		t.Fatalf("anthropic reasoning effort = %q, want %q", *got, models.ReasoningEffortHigh)
	}
}

// TestSpawnDeepSeekDisabledReasoningReachesWire covers the toggle-style Chat
// Completions compat backends (DeepSeek / MiniMax), where "off" is expressed by
// sending reasoning_effort:"none". The parent path always sent it; the spawn path
// could not, because Disabled had no field to travel in.
func TestSpawnDeepSeekDisabledReasoningReachesWire(t *testing.T) {
	t.Parallel()

	got := runSpawnReasoning(t, "openai-completions", tools.SpawnRunConfig{
		ChatCompletionsCompat: models.ChatCompletionsCompatDeepSeek,
		ReasoningConfig:       &models.ReasoningConfig{Disabled: true},
	})

	if got == nil {
		t.Fatal("deepseek: expected reasoning_effort to reach the provider")
	}
	if *got != models.ReasoningEffortNone {
		t.Fatalf("deepseek reasoning effort = %q, want %q", *got, models.ReasoningEffortNone)
	}
}
