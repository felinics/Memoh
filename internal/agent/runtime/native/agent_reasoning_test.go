package native

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	anthropicmessages "github.com/felinics/twilight/provider/anthropic/messages"
	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/background"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

type recordingReasoningProvider struct {
	params sdk.Request
}

func (*recordingReasoningProvider) Name() string {
	return "openai-completions"
}

func (*recordingReasoningProvider) ListModels(context.Context) ([]sdk.Model, error) {
	return nil, nil
}

func (*recordingReasoningProvider) Test(context.Context) *sdk.ProviderTestResult {
	return &sdk.ProviderTestResult{Status: sdk.ProviderStatusOK}
}

func (*recordingReasoningProvider) TestModel(context.Context, string) (*sdk.ModelTestResult, error) {
	return &sdk.ModelTestResult{Supported: true}, nil
}

func (p *recordingReasoningProvider) DoGenerate(_ context.Context, params sdk.Request) (sdk.ModelResult, error) {
	p.params = params
	return sdk.ModelResult{
		Text:         "ok",
		FinishReason: sdk.FinishReasonStop,
	}, nil
}

func (*recordingReasoningProvider) DoStream(context.Context, sdk.Request) (<-chan sdk.StreamPart, error) {
	return nil, nil
}

func TestAgentGeneratePreservesDeepSeekReasoningDisabled(t *testing.T) {
	t.Parallel()

	provider := &recordingReasoningProvider{}
	cfg := RunConfig{
		Model: &sdk.Model{
			ID:       "deepseek-v4-flash",
			Provider: provider,
			Type:     sdk.ModelTypeChat,
		},
		ReasoningConfig:       &models.ReasoningConfig{Disabled: true},
		ChatCompletionsCompat: models.ChatCompletionsCompatDeepSeek,
	}

	if _, err := New(Deps{}).Generate(context.Background(), cfg); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if provider.params.ReasoningEffort == nil {
		t.Fatal("expected reasoning effort to be set")
	}
	if got := *provider.params.ReasoningEffort; got != "none" {
		t.Fatalf("expected reasoning effort none, got %q", got)
	}
}

type recordingPromptCacheProvider struct {
	mu     sync.Mutex
	calls  int
	params []sdk.Request
}

func (*recordingPromptCacheProvider) Name() string {
	return string(models.ClientTypeAnthropicMessages)
}

func (*recordingPromptCacheProvider) ListModels(context.Context) ([]sdk.Model, error) {
	return nil, nil
}

func (*recordingPromptCacheProvider) Test(context.Context) *sdk.ProviderTestResult {
	return &sdk.ProviderTestResult{Status: sdk.ProviderStatusOK}
}

func (*recordingPromptCacheProvider) TestModel(context.Context, string) (*sdk.ModelTestResult, error) {
	return &sdk.ModelTestResult{Supported: true}, nil
}

func (p *recordingPromptCacheProvider) DoGenerate(_ context.Context, params sdk.Request) (sdk.ModelResult, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.params = append(p.params, cloneGenerateParams(params))
	p.mu.Unlock()

	if call == 1 {
		return sdk.ModelResult{
			FinishReason: sdk.FinishReasonToolCalls,
			ToolCalls: []sdk.ToolCall{{
				ToolCallID: "call-1",
				ToolName:   "noop",
				Input:      toolexec.ArgumentsFromValue(map[string]any{}),
			}},
		}, nil
	}
	return sdk.ModelResult{
		Text:         "ok",
		FinishReason: sdk.FinishReasonStop,
	}, nil
}

func (*recordingPromptCacheProvider) DoStream(context.Context, sdk.Request) (<-chan sdk.StreamPart, error) {
	return nil, nil
}

func (p *recordingPromptCacheProvider) snapshotParams() []sdk.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]sdk.Request, len(p.params))
	for i := range p.params {
		out[i] = cloneGenerateParams(p.params[i])
	}
	return out
}

func cloneGenerateParams(params sdk.Request) sdk.Request {
	cloned := params
	cloned.Messages = cloneMessages(params.Messages)
	if params.Tools != nil {
		cloned.Tools = append([]sdk.ToolDefinition(nil), params.Tools...)
	}
	return cloned
}

func cloneMessages(messages []sdk.Message) []sdk.Message {
	if messages == nil {
		return nil
	}
	out := make([]sdk.Message, len(messages))
	for i, msg := range messages {
		out[i] = msg
		if msg.Content == nil {
			continue
		}
		out[i].Content = make([]sdk.MessagePart, len(msg.Content))
		for j, part := range msg.Content {
			out[i].Content[j] = cloneMessagePart(part)
		}
	}
	return out
}

func cloneMessagePart(part sdk.MessagePart) sdk.MessagePart {
	switch p := part.(type) {
	case sdk.TextPart:
		if p.CacheControl != nil {
			cc := *p.CacheControl
			p.CacheControl = &cc
		}
		if p.ProviderMetadata != nil {
			p.ProviderMetadata = p.ProviderMetadata.Clone()
		}
		return p
	case sdk.ReasoningPart:
		if p.ProviderMetadata != nil {
			p.ProviderMetadata = p.ProviderMetadata.Clone()
		}
		return p
	case sdk.ImagePart:
		if p.CacheControl != nil {
			cc := *p.CacheControl
			p.CacheControl = &cc
		}
		return p
	case sdk.FilePart:
		if p.CacheControl != nil {
			cc := *p.CacheControl
			p.CacheControl = &cc
		}
		return p
	default:
		return part
	}
}

func TestAgentGenerateBackgroundPrepareKeepsCachedAnthropicSystemPromoted(t *testing.T) {
	t.Parallel()

	provider := &recordingPromptCacheProvider{}
	anthropicProvider := anthropicmessages.New(anthropicmessages.WithAPIKey("test"))
	model := anthropicProvider.ChatModel("claude-test")
	model.Provider = provider

	cfg := RunConfig{
		Model:             model,
		Messages:          []sdk.Message{sdk.UserMessage("run")},
		System:            "base system\n\n## Tool usage\n\nusage text",
		PromptCacheTTL:    models.PromptCacheTTL5m,
		SupportsToolCall:  true,
		BackgroundManager: background.New(nil),
		Identity: SessionContext{
			BotID:     "bot-1",
			SessionID: "session-1",
		},
	}

	testTools := []toolexec.Tool{{
		Name: "noop",
		Execute: func(*toolexec.ToolExecContext, sdk.ToolArguments) (sdk.ToolOutput, error) {
			return toolexec.OutputFromValue("ok"), nil
		},
	}}
	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: testTools}})

	if _, err := a.Generate(context.Background(), cfg); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	params := provider.snapshotParams()
	if len(params) < 2 {
		t.Fatalf("expected at least 2 generation calls, got %d", len(params))
	}
	for i, p := range params {
		if p.System != "" {
			t.Fatalf("call %d system = %q, want empty because Anthropic prompt cache promoted it to a cached system message", i+1, p.System)
		}
	}
	if len(params[0].Messages) == 0 || params[0].Messages[0].Role != sdk.MessageRoleSystem {
		t.Fatalf("expected first message to be promoted cached system message, got %#v", params[0].Messages)
	}
	if len(params[1].Messages) == 0 || params[1].Messages[0].Role != sdk.MessageRoleSystem {
		t.Fatalf("expected second call to preserve promoted system message, got %#v", params[1].Messages)
	}
}

func TestAgentGenerateRunningTaskSummaryInjectsUserMessageNotSystem(t *testing.T) {
	t.Parallel()

	provider := &recordingPromptCacheProvider{}
	anthropicProvider := anthropicmessages.New(anthropicmessages.WithAPIKey("test"))
	model := anthropicProvider.ChatModel("claude-test")
	model.Provider = provider

	bgMgr := background.New(nil)
	taskCtx, cancelTask := context.WithCancel(context.Background())
	defer cancelTask()
	started := make(chan struct{})
	bgMgr.Spawn(taskCtx, "bot-1", "session-1", "npm test", "/repo", "Run tests", func(ctx context.Context, _, _ string, _ int32) (*bridge.ExecResult, error) {
		close(started)
		<-ctx.Done()
		return &bridge.ExecResult{ExitCode: -1}, ctx.Err()
	}, nil, nil)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("background task did not start")
	}

	cfg := RunConfig{
		Model:             model,
		Messages:          []sdk.Message{sdk.UserMessage("run")},
		System:            "base system\n\n## Tool usage\n\nusage text",
		PromptCacheTTL:    models.PromptCacheTTL5m,
		SupportsToolCall:  true,
		BackgroundManager: bgMgr,
		Identity: SessionContext{
			BotID:     "bot-1",
			SessionID: "session-1",
		},
	}

	testTools := []toolexec.Tool{{
		Name: "noop",
		Execute: func(*toolexec.ToolExecContext, sdk.ToolArguments) (sdk.ToolOutput, error) {
			return toolexec.OutputFromValue("ok"), nil
		},
	}}
	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: testTools}})

	if _, err := a.Generate(context.Background(), cfg); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	params := provider.snapshotParams()
	if len(params) < 2 {
		t.Fatalf("expected at least 2 generation calls, got %d", len(params))
	}
	firstText, firstCacheControl := firstSystemTextPart(t, params[0].Messages)
	if !strings.Contains(firstText, "base system") {
		t.Fatalf("first call system message missing base system:\n%s", firstText)
	}
	if strings.Contains(firstText, "Currently running background tasks:") {
		t.Fatalf("first call should not contain running task summary because PrepareStep runs between SDK steps, got:\n%s", firstText)
	}
	if firstCacheControl == nil || firstCacheControl.Type != "ephemeral" {
		t.Fatalf("first call system message cache control = %#v, want ephemeral", firstCacheControl)
	}
	if got := backgroundSummaryCount(params[0].Messages); got != 0 {
		t.Fatalf("first call summary messages = %d, want 0 before the first prepared step", got)
	}

	p := params[1]
	if p.System != "" {
		t.Fatalf("second call system = %q, want empty because Anthropic prompt cache promoted it to a cached system message", p.System)
	}
	cachedText, cachedCacheControl := firstSystemTextPart(t, p.Messages)
	if cachedText != firstText {
		t.Fatalf("second call cached system message must stay byte-identical:\nfirst: %q\nsecond: %q", firstText, cachedText)
	}
	if cachedCacheControl == nil || cachedCacheControl.Type != "ephemeral" {
		t.Fatalf("second call cached system message cache control = %#v, want ephemeral", cachedCacheControl)
	}
	for _, msg := range p.Messages {
		if msg.Role != sdk.MessageRoleSystem {
			continue
		}
		for _, part := range msg.Content {
			if tp, ok := part.(sdk.TextPart); ok && strings.Contains(tp.Text, "Currently running background tasks:") {
				t.Fatalf("running task summary must not ride a system message:\n%s", tp.Text)
			}
		}
	}
	if got := backgroundSummaryCount(p.Messages); got != 1 {
		t.Fatalf("second call summary messages = %d, want exactly 1", got)
	}
	last := p.Messages[len(p.Messages)-1]
	if last.Role != sdk.MessageRoleUser {
		t.Fatalf("second call last message role = %q, want summary as tail user message", last.Role)
	}
	summaryPart, ok := last.Content[0].(sdk.TextPart)
	if !ok || !strings.HasPrefix(summaryPart.Text, testBackgroundSummaryPrefix) {
		t.Fatalf("second call tail message is not the background summary: %#v", last.Content[0])
	}
	if !strings.Contains(summaryPart.Text, "Currently running background tasks:") || !strings.Contains(summaryPart.Text, "Run tests") {
		t.Fatalf("second call summary message missing running task summary:\n%s", summaryPart.Text)
	}
	if summaryPart.CacheControl != nil {
		t.Fatalf("second call summary message cache control = %#v, want nil", summaryPart.CacheControl)
	}
}

func firstSystemTextPart(t *testing.T, messages []sdk.Message) (string, *sdk.CacheControl) {
	t.Helper()
	return systemTextPartAt(t, messages, 0)
}

func systemTextPartAt(t *testing.T, messages []sdk.Message, index int) (string, *sdk.CacheControl) {
	t.Helper()
	if len(messages) <= index || messages[index].Role != sdk.MessageRoleSystem || len(messages[index].Content) == 0 {
		t.Fatalf("expected system message at index %d, got %#v", index, messages)
	}
	part, ok := messages[index].Content[0].(sdk.TextPart)
	if !ok {
		t.Fatalf("expected system text part at index %d, got %#v", index, messages[index].Content[0])
	}
	return part.Text, part.CacheControl
}
