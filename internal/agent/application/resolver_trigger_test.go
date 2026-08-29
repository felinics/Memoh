package application

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agentpkg "github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	turnpkg "github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/contextview"
)

const triggerTestContextWindow = 128000

type triggerCaptureProvider struct {
	mu     sync.Mutex
	params sdk.GenerateParams
}

func (*triggerCaptureProvider) Name() string { return "trigger-capture" }

func (*triggerCaptureProvider) ListModels(context.Context) ([]sdk.Model, error) { return nil, nil }

func (*triggerCaptureProvider) Test(context.Context) *sdk.ProviderTestResult {
	return &sdk.ProviderTestResult{Status: sdk.ProviderStatusOK}
}

func (*triggerCaptureProvider) TestModel(context.Context, string) (*sdk.ModelTestResult, error) {
	return &sdk.ModelTestResult{Supported: true}, nil
}

func (p *triggerCaptureProvider) DoGenerate(_ context.Context, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
	p.mu.Lock()
	p.params = params
	p.mu.Unlock()
	return &sdk.GenerateResult{Text: "done", FinishReason: sdk.FinishReasonStop}, nil
}

func (p *triggerCaptureProvider) DoStream(ctx context.Context, params sdk.GenerateParams) (*sdk.StreamResult, error) {
	result, err := p.DoGenerate(ctx, params)
	if err != nil {
		return nil, err
	}
	ch := make(chan sdk.StreamPart, 4)
	go func() {
		defer close(ch)
		ch <- &sdk.StartPart{}
		ch <- &sdk.StartStepPart{}
		ch <- &sdk.FinishStepPart{FinishReason: result.FinishReason}
		ch <- &sdk.FinishPart{FinishReason: result.FinishReason}
	}()
	return &sdk.StreamResult{Stream: ch}, nil
}

func (p *triggerCaptureProvider) lastParams() sdk.GenerateParams {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.params
}

type triggerViewRecorder struct {
	mu  sync.Mutex
	out agentpkg.RunConfig
}

func (v *triggerViewRecorder) apply(ctx context.Context, cfg agentpkg.RunConfig) (agentpkg.RunConfig, error) {
	out, err := contextview.ProviderRunConfigApplier(slog.Default())(ctx, cfg)
	v.mu.Lock()
	v.out = out
	v.mu.Unlock()
	return out, err
}

func (v *triggerViewRecorder) snapshot() agentpkg.RunConfig {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.out
}

func triggerResolvedRunConfig(provider sdk.Provider, rawQuery string, now time.Time, sessionType string) agentpkg.RunConfig {
	headerified := turnpkg.FormatUserHeader(turnpkg.UserMessageHeaderInput{
		DisplayName: "User",
		Time:        now,
	}, rawQuery)
	return agentpkg.RunConfig{
		Model:                     &sdk.Model{ID: "trigger-model", Provider: provider, Type: sdk.ModelTypeChat},
		Query:                     headerified,
		ContextScope:              contextfrag.Scope{BotID: "bot-1", ChatID: "bot-1"},
		ContextToolExchangePolicy: &contextfrag.ToolExchangePolicy{MinMessages: 10},
		ContextLifecycle:          contextfrag.NewLifecycleHolder(),
		ContextBudgetMaxTokens:    triggerTestContextWindow,
		SessionType:               sessionType,
	}
}

func runTriggerPromptPipeline(t *testing.T, cfg agentpkg.RunConfig, provider *triggerCaptureProvider) (agentpkg.RunConfig, sdk.GenerateParams) {
	t.Helper()
	ctx := context.Background()
	service := &Service{logger: slog.Default()}
	cfg = service.prepareRunConfig(ctx, cfg)

	recorder := &triggerViewRecorder{}
	agent := agentpkg.New(agentpkg.Deps{ContextViewApplier: recorder.apply})
	if _, err := agent.Generate(ctx, cfg); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	return recorder.snapshot(), provider.lastParams()
}

func triggerMessageText(message sdk.Message) string {
	for _, part := range message.Content {
		if text, ok := part.(sdk.TextPart); ok {
			return text.Text
		}
	}
	return ""
}

func assertTriggerCurrentRequest(t *testing.T, seen agentpkg.RunConfig, params sdk.GenerateParams, prompt string) {
	t.Helper()

	current := 0
	for _, frag := range seen.ContextFrags {
		if frag.Kind == contextfrag.KindCurrentUserMessage {
			current++
		}
		if frag.Slot == contextfrag.SlotHistory {
			t.Fatalf("trigger prompt became history: %#v", frag)
		}
	}
	if current != 1 {
		t.Fatalf("current fragments = %d, want exactly one: %#v", current, seen.ContextFrags)
	}

	if len(seen.Messages) != 1 || seen.Messages[0].Role != sdk.MessageRoleUser || triggerMessageText(seen.Messages[0]) != prompt {
		t.Fatalf("rendered messages = %#v, want exactly the rich trigger prompt", seen.Messages)
	}
	if len(params.Messages) != 1 || params.Messages[0].Role != sdk.MessageRoleUser || triggerMessageText(params.Messages[0]) != prompt {
		t.Fatalf("provider messages = %#v, want exactly the rich trigger prompt", params.Messages)
	}

	plan := seen.ContextManifest.BudgetPlan
	if plan == nil || plan.Window != triggerTestContextWindow {
		t.Fatalf("budget plan = %#v, want active %d-token window", plan, triggerTestContextWindow)
	}
	wantCurrentCost := contextfrag.ProviderBudgetTokensFromBytes(len(prompt))
	if plan.CurrentRequestCost != wantCurrentCost {
		t.Fatalf("CurrentRequestCost = %d, want conservative prompt cost %d", plan.CurrentRequestCost, wantCurrentCost)
	}
}

func TestTriggerScheduleAttachesPromptAsCurrentUserMessage(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 5, 9, 0, 0, 0, time.UTC)
	schedulePrompt := agentpkg.GenerateSchedulePrompt(agentpkg.Schedule{
		ID:      "sched-1",
		Name:    "daily digest",
		Command: "summarize inbox",
	})

	provider := &triggerCaptureProvider{}
	cfg := triggerResolvedRunConfig(provider, "summarize inbox", now, sessionmode.Schedule)
	cfg = attachCurrentTurnPrompt(cfg, schedulePrompt)

	seen, params := runTriggerPromptPipeline(t, cfg, provider)
	assertTriggerCurrentRequest(t, seen, params, schedulePrompt)
}
