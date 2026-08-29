package application

import (
	"context"
	"log/slog"
	"testing"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/contextview"
	"github.com/felinics/memoh/internal/hooks"
)

func TestDiscussRetainsResolvedHookSystemSections(t *testing.T) {
	agent := &fakeAgentStreamer{}
	baseConfig := native.RunConfig{
		System:       "base system",
		ContextScope: contextfrag.Scope{BotID: "bot-1", SessionID: "sess-1"},
	}
	baseConfig.ContextSourceFrags = contextview.CollectProviderSourceFrags(context.Background(), baseConfig)
	hookBuild := buildHookSystemSections([]promptHookOutput{{
		Event: hooks.EventBeforePromptBuild,
		Result: hooks.Result{AppendSystemSections: []hooks.SystemSectionOutput{{
			HookName: "round-seven",
			Text:     "hook system",
		}}},
	}}, baseConfig.ContextScope)
	baseConfig.ContextSourceFrags = append(baseConfig.ContextSourceFrags, hookBuild.Frags...)
	resolver := &fakeDiscussService{resolveResult: ResolveRunConfigResult{
		RunConfig: baseConfig,
		ModelID:   "model-1",
	}}
	service := newDiscussTestService(&fakeRunner{}, agent, resolver)

	handle, err := service.StartTurn(context.Background(), discussCommand())
	if err != nil {
		t.Fatal(err)
	}
	drainDiscuss(t, handle)

	if agent.lastConfig == nil {
		t.Fatal("expected agent to be called")
	}
	rendered, err := contextview.ProviderRunConfigApplier(slog.New(slog.DiscardHandler))(
		context.Background(),
		*agent.lastConfig,
	)
	if err != nil {
		t.Fatalf("ApplyProviderRunConfig() error = %v", err)
	}
	if rendered.System != "base system\n\nhook system" {
		t.Fatalf("System = %q, want resolved hook system section", rendered.System)
	}
}
