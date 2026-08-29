package contextview

import (
	"context"
	"errors"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agentpkg "github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/models"
)

type namedEnvelopeProbeProvider struct {
	*envelopeProbeProvider
	name string
}

func (p namedEnvelopeProbeProvider) Name() string { return p.name }

// TestProviderEnvelopeAuthorityMatrix pins the cross-boundary contract across
// client types, thinking decisions, and assembly paths: the plan reserves the
// resolved output allowance, the request carries it only where the adapter
// would have resolved the same value, a fitting payload dispatches, and a
// payload that fits the window but not the allowance never reaches the
// provider. Fragment-first assembly rejects it while planning the budget;
// the fallback assembles the legacy payload and relies on the dispatch check.
func TestProviderEnvelopeAuthorityMatrix(t *testing.T) {
	t.Parallel()

	clients := []struct {
		name       string
		provider   string
		clientType models.ClientType
		reasoning  *models.ReasoningConfig
	}{
		{name: "anthropic", provider: "anthropic-mock", clientType: models.ClientTypeAnthropicMessages},
		{name: "anthropic adaptive", provider: "anthropic-mock", clientType: models.ClientTypeAnthropicMessages, reasoning: &models.ReasoningConfig{Active: true, Adaptive: true, Effort: models.ReasoningEffortHigh}},
		{name: "anthropic legacy high", provider: "anthropic-mock", clientType: models.ClientTypeAnthropicMessages, reasoning: &models.ReasoningConfig{Active: true, Effort: models.ReasoningEffortHigh}},
		{name: "completions", provider: "mock", clientType: models.ClientTypeOpenAICompletions},
		{name: "completions reasoning", provider: "mock", clientType: models.ClientTypeOpenAICompletions, reasoning: &models.ReasoningConfig{Active: true, Effort: models.ReasoningEffortHigh}},
		{name: "responses", provider: "openai-responses-mock", clientType: models.ClientTypeOpenAIResponses},
		{name: "codex", provider: "openai-codex-mock", clientType: models.ClientTypeOpenAICodex},
		{name: "google", provider: "google-mock", clientType: models.ClientTypeGoogleGenerativeAI},
		{name: "copilot", provider: "github-copilot-mock", clientType: models.ClientTypeGitHubCopilot},
	}
	paths := []struct {
		name   string
		config func(messages []sdk.Message, window int) agentpkg.RunConfig
	}{
		{name: "fragment-first", config: func(messages []sdk.Message, window int) agentpkg.RunConfig {
			currentIndex := len(messages) - 1
			return agentpkg.RunConfig{
				System:                         "you are helpful",
				Messages:                       messages,
				ContextCurrentUserMessageIndex: &currentIndex,
				ContextBudgetMaxTokens:         window,
			}
		}},
		{name: "fallback", config: buildErrorFragsFirstConfig},
	}
	const window = 200_000
	fitting := []sdk.Message{sdk.UserMessage("hello")}

	for _, client := range clients {
		for _, path := range paths {
			t.Run(client.name+"/"+path.name, func(t *testing.T) {
				t.Parallel()

				limits := models.ResolveGenerationLimits(client.clientType, client.reasoning, window)
				// Inside the window, outside the allowance: only the output reserve
				// can reject it, so an allowance that forgot the reserve would dispatch.
				overAllowanceTokens := window - limits.MaxOutputTokens + 1_000
				oversized := []sdk.Message{sdk.UserMessage(strings.Repeat("o", overAllowanceTokens*4*100/contextfrag.ProviderBudgetSafetyFactorPercent))}
				if cost := contextfrag.ProviderEnvelopeTokens("", oversized, nil); cost <= window-limits.MaxOutputTokens || cost >= window {
					t.Fatalf("oversized fixture costs %d tokens, want strictly between allowance %d and window %d", cost, window-limits.MaxOutputTokens, window)
				}
				var seen sdk.GenerateParams
				probe := &envelopeProbeProvider{handler: func(_ int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
					seen = params
					return &sdk.GenerateResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
				}}
				provider := namedEnvelopeProbeProvider{envelopeProbeProvider: probe, name: client.provider}
				run := func(messages []sdk.Message) (*contextfrag.LifecycleSnapshot, []contextfrag.MutationRecord, error) {
					cfg := path.config(messages, window)
					cfg.Model = &sdk.Model{ID: "model", Provider: provider, Type: sdk.ModelTypeChat}
					cfg.ReasoningConfig = client.reasoning
					cfg.Identity = agentpkg.SessionContext{BotID: "bot-1"}
					cfg.ContextMutations = contextfrag.NewMutationLedger()
					cfg.ContextLifecycle = contextfrag.NewLifecycleHolder()
					agent := agentpkg.New(agentpkg.Deps{ContextViewApplier: ProviderRunConfigApplier(nil)})
					_, err := agent.Generate(context.Background(), cfg)
					snapshot, _ := cfg.ContextLifecycle.Snapshot()
					return &snapshot, cfg.ContextMutations.Records(), err
				}

				snapshot, _, err := run(fitting)
				if err != nil {
					t.Fatalf("fitting payload failed: %v", err)
				}
				if probe.calls.Load() != 1 {
					t.Fatalf("provider calls = %d, want one dispatch for a fitting payload", probe.calls.Load())
				}
				if snapshot.BudgetPlan == nil || snapshot.BudgetPlan.OutputReserve != limits.MaxOutputTokens || snapshot.BudgetPlan.OutputReserveResolution != limits.Resolution {
					t.Fatalf("budget plan = %+v, want reserve %d (%s)", snapshot.BudgetPlan, limits.MaxOutputTokens, limits.Resolution)
				}
				switch {
				case limits.Requested && (seen.MaxTokens == nil || *seen.MaxTokens != limits.MaxOutputTokens):
					t.Fatalf("MaxTokens = %v, want the reserved %d on the request", seen.MaxTokens, limits.MaxOutputTokens)
				case !limits.Requested && seen.MaxTokens != nil:
					t.Fatalf("MaxTokens = %d, want the provider default to stand", *seen.MaxTokens)
				}

				probe.calls.Store(0)
				_, records, err := run(oversized)
				if !errors.Is(err, contextfrag.ErrBudgetUnsatisfied) && !errors.Is(err, contextfrag.ErrProtectedContextOverflow) {
					t.Fatalf("oversized payload error = %v, want a typed budget failure", err)
				}
				if probe.calls.Load() != 0 {
					t.Fatalf("provider calls = %d, want zero dispatches over the allowance", probe.calls.Load())
				}
				if !hasMutation(records, contextfrag.MutationContextBudgetFailure) {
					t.Fatalf("mutations = %+v, want a context budget failure", records)
				}
				if path.name == "fallback" && !hasMutation(records, contextfrag.MutationContextViewFallback) {
					t.Fatalf("mutations = %+v, want the fallback recorded alongside the failure", records)
				}
			})
		}
	}
}

func hasMutation(records []contextfrag.MutationRecord, kind contextfrag.MutationKind) bool {
	for _, record := range records {
		if record.Kind == kind {
			return true
		}
	}
	return false
}

// TestProviderEnvelopeEstimatorParity keeps the envelope estimator and the
// selection estimator identical for every message shape production emits.
func TestProviderEnvelopeEstimatorParity(t *testing.T) {
	t.Parallel()

	messages := map[string]sdk.Message{
		"text":         sdk.UserMessage(strings.Repeat("hello world ", 300)),
		"unicode":      sdk.AssistantMessage(strings.Repeat("上下文预算", 200)),
		"tool call":    assistantToolCallMessage("call-1", "lookup", "let me check"),
		"tool result":  toolResultMessage("call-1", "lookup", strings.Repeat("r", 5_000)),
		"image":        imageUserMessage(500_000, sdk.TextPart{Text: "what is this?"}),
		"reasoning":    {Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.ReasoningPart{Text: strings.Repeat("think ", 400)}, sdk.TextPart{Text: "answer"}}},
		"file":         fileUserMessage(2_000),
		"multi images": imageUserMessage(100_000, sdk.ImagePart{Image: "data:image/png;base64," + strings.Repeat("B", 100_000), MediaType: "image/png"}),
	}
	for name, message := range messages {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			envelope := contextfrag.ProviderEnvelopeTokens("", []sdk.Message{message}, nil)
			selection := contextfrag.ResolveProviderBudgetFragTokens(contextfrag.MessageFrag(contextfrag.MessageFragInput{Message: message}))
			if envelope != selection {
				t.Fatalf("envelope estimate %d != selection estimate %d", envelope, selection)
			}
			if envelope <= 0 {
				t.Fatal("estimate must be positive")
			}
		})
	}
	if images := contextfrag.ProviderEnvelopeTokens("", []sdk.Message{messages["multi images"]}, nil); images != 2*contextfrag.EstimateImageTokens {
		t.Fatalf("two images priced at %d, want %d", images, 2*contextfrag.EstimateImageTokens)
	}
}
