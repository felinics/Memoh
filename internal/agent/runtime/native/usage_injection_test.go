package native

import (
	"context"
	"strings"
	"sync"
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/internal/agent/sessionmode"
	tools "github.com/memohai/memoh/internal/agent/tool"
)

// usageTestProvider is a ToolProvider that also implements tools.ToolUsage. It
// returns a tool (and therefore contributes usage) only when emitTool is true,
// letting us assert that usage guidance is gated by tool registration.
type usageTestProvider struct {
	emitTool      bool
	usage         string
	requireTool   tools.ToolName
	missingMarker string
	sessionSeen   *tools.SessionContext
}

func (p *usageTestProvider) Tools(_ context.Context, session tools.SessionContext) ([]sdk.Tool, error) {
	if p.sessionSeen != nil {
		*p.sessionSeen = session
	}
	if !p.emitTool {
		return nil, nil
	}
	return []sdk.Tool{{Name: "fake_tool", Description: "fake"}}, nil
}

func (p *usageTestProvider) Usage(_ context.Context, _ tools.SessionContext, available tools.AvailableTools) string {
	if !p.requireTool.IsZero() {
		if available.Has(p.requireTool) {
			return p.usage
		}
		return p.missingMarker
	}
	return p.usage
}

// plainTestProvider returns a tool but does NOT implement tools.ToolUsage.
type plainTestProvider struct{}

func (plainTestProvider) Tools(_ context.Context, _ tools.SessionContext) ([]sdk.Tool, error) {
	return []sdk.Tool{{Name: tools.ToolRead().String(), Description: "plain"}}, nil
}

func newTestAgent(providers ...tools.ToolProvider) *Agent {
	a := New(Deps{})
	a.SetToolProviders(providers)
	return a
}

const usageMarker = "USAGE_MARKER_xyz"

type usageRecordingProvider struct {
	mu     sync.Mutex
	params []sdk.GenerateParams
}

func (*usageRecordingProvider) Name() string { return "usage-recording" }

func (*usageRecordingProvider) ListModels(context.Context) ([]sdk.Model, error) { return nil, nil }

func (*usageRecordingProvider) Test(context.Context) *sdk.ProviderTestResult {
	return &sdk.ProviderTestResult{Status: sdk.ProviderStatusOK}
}

func (*usageRecordingProvider) TestModel(context.Context, string) (*sdk.ModelTestResult, error) {
	return &sdk.ModelTestResult{Supported: true}, nil
}

func (p *usageRecordingProvider) DoGenerate(_ context.Context, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
	p.mu.Lock()
	p.params = append(p.params, params)
	p.mu.Unlock()
	return &sdk.GenerateResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
}

func (*usageRecordingProvider) DoStream(context.Context, sdk.GenerateParams) (*sdk.StreamResult, error) {
	return nil, nil
}

func (p *usageRecordingProvider) lastParams() sdk.GenerateParams {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.params) == 0 {
		return sdk.GenerateParams{}
	}
	return p.params[len(p.params)-1]
}

func TestGenerateInjectsToolUsageIntoModelSystem(t *testing.T) {
	t.Parallel()
	modelProvider := &usageRecordingProvider{}
	a := newTestAgent(&usageTestProvider{emitTool: true, usage: usageMarker})

	if _, err := a.Generate(context.Background(), RunConfig{
		Model: &sdk.Model{
			ID:       "usage-model",
			Provider: modelProvider,
			Type:     sdk.ModelTypeChat,
		},
		System:           "base system",
		Messages:         []sdk.Message{sdk.UserMessage("hi")},
		SupportsToolCall: true,
	}); err != nil {
		t.Fatalf("Generate error: %v", err)
	}

	params := modelProvider.lastParams()
	wantSystem := "base system\n\n## Tool usage\n\n" + usageMarker
	if params.System != wantSystem {
		t.Fatalf("expected model system %q, got %q", wantSystem, params.System)
	}
}

func TestGeneratePlacesToolUsageBeforeWorkspaceInstructions(t *testing.T) {
	t.Parallel()
	modelProvider := &usageRecordingProvider{}
	a := newTestAgent(&usageTestProvider{emitTool: true, usage: usageMarker})

	if _, err := a.Generate(context.Background(), RunConfig{
		Model: &sdk.Model{
			ID:       "usage-model",
			Provider: modelProvider,
			Type:     sdk.ModelTypeChat,
		},
		System:           "base system\n\n## Workspace instruction files\n\nworkspace text",
		Messages:         []sdk.Message{sdk.UserMessage("hi")},
		SupportsToolCall: true,
	}); err != nil {
		t.Fatalf("Generate error: %v", err)
	}

	params := modelProvider.lastParams()
	wantSystem := "base system\n\n## Tool usage\n\n" + usageMarker + "\n\n## Workspace instruction files\n\nworkspace text"
	if params.System != wantSystem {
		t.Fatalf("expected model system %q, got %q", wantSystem, params.System)
	}
}

type usageStreamRecordingProvider struct {
	usageRecordingProvider
}

func (p *usageStreamRecordingProvider) DoStream(_ context.Context, params sdk.GenerateParams) (*sdk.StreamResult, error) {
	p.mu.Lock()
	p.params = append(p.params, params)
	p.mu.Unlock()

	ch := make(chan sdk.StreamPart, 4)
	go func() {
		defer close(ch)
		ch <- &sdk.StartPart{}
		ch <- &sdk.StartStepPart{}
		ch <- &sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop}
		ch <- &sdk.FinishPart{FinishReason: sdk.FinishReasonStop}
	}()
	return &sdk.StreamResult{Stream: ch}, nil
}

func TestStreamInjectsToolUsageIntoModelSystem(t *testing.T) {
	t.Parallel()
	modelProvider := &usageStreamRecordingProvider{}
	a := newTestAgent(&usageTestProvider{emitTool: true, usage: usageMarker})

	for range a.Stream(context.Background(), RunConfig{
		Model: &sdk.Model{
			ID:       "usage-model",
			Provider: modelProvider,
			Type:     sdk.ModelTypeChat,
		},
		System:           "base system",
		Messages:         []sdk.Message{sdk.UserMessage("hi")},
		SupportsToolCall: true,
	}) {
	}

	params := modelProvider.lastParams()
	wantSystem := "base system\n\n## Tool usage\n\n" + usageMarker
	if params.System != wantSystem {
		t.Fatalf("expected stream model system %q, got %q", wantSystem, params.System)
	}
}

func TestStreamPassesLiveToolStreamFlagToTools(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		live bool
	}{
		{name: "internal stream", live: false},
		{name: "live stream", live: true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			modelProvider := &usageStreamRecordingProvider{}
			var seen tools.SessionContext
			a := newTestAgent(&usageTestProvider{emitTool: true, sessionSeen: &seen})

			for range a.Stream(context.Background(), RunConfig{
				Model: &sdk.Model{
					ID:       "usage-model",
					Provider: modelProvider,
					Type:     sdk.ModelTypeChat,
				},
				System:           "base system",
				Messages:         []sdk.Message{sdk.UserMessage("hi")},
				SupportsToolCall: true,
				LiveToolStream:   tc.live,
			}) {
			}
			if seen.LiveStream != tc.live {
				t.Fatalf("tool session LiveStream = %v, want %v", seen.LiveStream, tc.live)
			}
		})
	}
}

func TestStreamOmitsToolUsageWhenToolCallingUnsupported(t *testing.T) {
	t.Parallel()
	modelProvider := &usageStreamRecordingProvider{}
	a := newTestAgent(&usageTestProvider{emitTool: true, usage: usageMarker})

	for range a.Stream(context.Background(), RunConfig{
		Model: &sdk.Model{
			ID:       "usage-model",
			Provider: modelProvider,
			Type:     sdk.ModelTypeChat,
		},
		System:           "base system",
		Messages:         []sdk.Message{sdk.UserMessage("hi")},
		SupportsToolCall: false,
	}) {
	}

	params := modelProvider.lastParams()
	if strings.Contains(params.System, "## Tool usage") || strings.Contains(params.System, usageMarker) {
		t.Fatalf("expected stream model system to omit tool usage when tools are unsupported, got %q", params.System)
	}
}

func TestGenerateOmitsToolUsageWhenToolCallingUnsupported(t *testing.T) {
	t.Parallel()
	modelProvider := &usageRecordingProvider{}
	a := newTestAgent(&usageTestProvider{emitTool: true, usage: usageMarker})

	if _, err := a.Generate(context.Background(), RunConfig{
		Model: &sdk.Model{
			ID:       "usage-model",
			Provider: modelProvider,
			Type:     sdk.ModelTypeChat,
		},
		System:           "base system",
		Messages:         []sdk.Message{sdk.UserMessage("hi")},
		SupportsToolCall: false,
	}); err != nil {
		t.Fatalf("Generate error: %v", err)
	}

	params := modelProvider.lastParams()
	if strings.Contains(params.System, "## Tool usage") || strings.Contains(params.System, usageMarker) {
		t.Fatalf("expected model system to omit tool usage when tools are unsupported, got %q", params.System)
	}
}

func TestAssembleToolsInjectsUsageWhenProviderEmitsTools(t *testing.T) {
	t.Parallel()
	a := newTestAgent(&usageTestProvider{emitTool: true, usage: usageMarker})

	gotTools, usage, err := a.assembleTools(context.Background(), RunConfig{}, tools.StreamEmitter(func(tools.ToolStreamEvent) {}), true)
	if err != nil {
		t.Fatalf("assembleTools error: %v", err)
	}
	if len(gotTools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(gotTools))
	}
	if !strings.Contains(usage, usageMarker) {
		t.Fatalf("expected usage to contain %q, got %q", usageMarker, usage)
	}
	if !strings.Contains(usage, "## Tool usage") {
		t.Fatalf("expected usage to carry the section header, got %q", usage)
	}
}

func TestAssembleToolsOmitsUsageWhenProviderEmitsNoTools(t *testing.T) {
	t.Parallel()
	a := newTestAgent(&usageTestProvider{emitTool: false, usage: usageMarker})

	gotTools, usage, err := a.assembleTools(context.Background(), RunConfig{}, tools.StreamEmitter(func(tools.ToolStreamEvent) {}), true)
	if err != nil {
		t.Fatalf("assembleTools error: %v", err)
	}
	if len(gotTools) != 0 {
		t.Fatalf("expected 0 tools, got %d", len(gotTools))
	}
	if usage != "" {
		t.Fatalf("expected no usage when the provider registers no tools, got %q", usage)
	}
}

func TestAssembleToolsDoesNotExposeAskUserWithoutCapability(t *testing.T) {
	t.Parallel()
	a := newTestAgent(&tools.AskUserProvider{})

	gotTools, usage, err := a.assembleTools(context.Background(), RunConfig{
		SessionType: sessionmode.Chat,
	}, tools.StreamEmitter(func(tools.ToolStreamEvent) {}), true)
	if err != nil {
		t.Fatalf("assembleTools error: %v", err)
	}
	if len(gotTools) != 0 {
		t.Fatalf("expected ask_user to be omitted without user input capability, got %d tools", len(gotTools))
	}
	if usage != "" {
		t.Fatalf("expected no ask_user usage without user input capability, got %q", usage)
	}

	gotTools, usage, err = a.assembleTools(context.Background(), RunConfig{
		SessionType:         sessionmode.Chat,
		CanRequestUserInput: true,
	}, tools.StreamEmitter(func(tools.ToolStreamEvent) {}), true)
	if err != nil {
		t.Fatalf("assembleTools with capability error: %v", err)
	}
	if len(gotTools) != 1 || gotTools[0].Name != tools.ToolAskUser().String() {
		t.Fatalf("expected ask_user when capability is present, got %#v", gotTools)
	}
	if !strings.Contains(usage, tools.ToolAskUser().String()) {
		t.Fatalf("expected ask_user usage when capability is present, got %q", usage)
	}
}

func TestAssembleToolsIgnoresProvidersWithoutUsage(t *testing.T) {
	t.Parallel()
	a := newTestAgent(plainTestProvider{})

	gotTools, usage, err := a.assembleTools(context.Background(), RunConfig{}, tools.StreamEmitter(func(tools.ToolStreamEvent) {}), true)
	if err != nil {
		t.Fatalf("assembleTools error: %v", err)
	}
	if len(gotTools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(gotTools))
	}
	if usage != "" {
		t.Fatalf("expected no usage from a provider that does not implement ToolUsage, got %q", usage)
	}
}

// TestAssembleToolsGatesUsagePerProvider proves the gating is per-provider, not
// global: with a mix of providers in one session, only the usage of providers
// that themselves registered tools appears. The no-tool provider's usage must
// be dropped even though another provider did contribute tools.
func TestAssembleToolsGatesUsagePerProvider(t *testing.T) {
	t.Parallel()
	const (
		markerActive = "USAGE_ACTIVE_aaa"
		markerSilent = "USAGE_SILENT_bbb"
	)
	a := newTestAgent(
		&usageTestProvider{emitTool: true, usage: markerActive},
		&usageTestProvider{emitTool: false, usage: markerSilent},
		plainTestProvider{},
	)

	gotTools, usage, err := a.assembleTools(context.Background(), RunConfig{}, tools.StreamEmitter(func(tools.ToolStreamEvent) {}), true)
	if err != nil {
		t.Fatalf("assembleTools error: %v", err)
	}
	if len(gotTools) != 2 {
		t.Fatalf("expected 2 tools (active usage provider + plain provider), got %d", len(gotTools))
	}
	if !strings.Contains(usage, markerActive) {
		t.Fatalf("expected usage from the tool-emitting provider, got %q", usage)
	}
	if strings.Contains(usage, markerSilent) {
		t.Fatalf("usage from a provider that registered no tools must be dropped, got %q", usage)
	}
}

func TestAssembleToolsKeepsFirstDuplicateToolAndItsUsage(t *testing.T) {
	t.Parallel()
	const (
		firstUsage  = "USAGE_FIRST_PROVIDER"
		secondUsage = "USAGE_DUPLICATE_PROVIDER"
	)
	a := newTestAgent(
		&usageTestProvider{emitTool: true, usage: firstUsage},
		&usageTestProvider{emitTool: true, usage: secondUsage},
	)

	gotTools, usage, err := a.assembleTools(
		context.Background(),
		RunConfig{},
		tools.StreamEmitter(func(tools.ToolStreamEvent) {}),
		true,
	)
	if err != nil {
		t.Fatalf("assembleTools error: %v", err)
	}
	if len(gotTools) != 1 || gotTools[0].Name != "fake_tool" {
		t.Fatalf("expected only the first duplicate tool, got %#v", gotTools)
	}
	if !strings.Contains(usage, firstUsage) {
		t.Fatalf("expected usage from the retained provider, got %q", usage)
	}
	if strings.Contains(usage, secondUsage) {
		t.Fatalf("usage from a provider with no retained tools must be omitted, got %q", usage)
	}
}

func TestAssembleToolsPassesCompleteAvailableToolSetToUsage(t *testing.T) {
	t.Parallel()
	const (
		markerPresent = "USAGE_DEPENDENCY_PRESENT"
		markerMissing = "USAGE_DEPENDENCY_MISSING"
	)
	a := newTestAgent(
		&usageTestProvider{
			emitTool:      true,
			usage:         markerPresent,
			requireTool:   tools.ToolRead(),
			missingMarker: markerMissing,
		},
		plainTestProvider{},
	)

	gotTools, usage, err := a.assembleTools(context.Background(), RunConfig{}, tools.StreamEmitter(func(tools.ToolStreamEvent) {}), true)
	if err != nil {
		t.Fatalf("assembleTools error: %v", err)
	}
	if len(gotTools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(gotTools))
	}
	if !strings.Contains(usage, markerPresent) {
		t.Fatalf("expected usage to see tool from another provider, got %q", usage)
	}
	if strings.Contains(usage, markerMissing) {
		t.Fatalf("expected complete available tool set, got missing marker in %q", usage)
	}
}
