package contextview_test

import (
	"context"
	"reflect"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	agentpkg "github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/contextview"
)

func TestSpawnFragmentsRenderLegacyProviderBytes(t *testing.T) {
	t.Parallel()
	index := 1
	rc := agentpkg.RunConfig{
		System: agentpkg.SpawnSystemPrompt(sessionmode.Subagent), SessionType: sessionmode.Subagent,
		Messages:                       []sdk.Message{sdk.UserMessage("history"), sdk.UserMessage("  current  ")},
		ContextCurrentUserMessageIndex: &index, ContextQueryMaterialized: true,
	}
	rc.ContextSourceFrags = agentpkg.SpawnContextSourceFrags(rc)
	wantSystem := rc.System
	wantMessages := rc.Messages

	got := contextview.ApplyProviderRunConfig(context.Background(), nil, rc)
	if got.System != wantSystem || !reflect.DeepEqual(got.Messages, wantMessages) {
		t.Fatalf("spawn provider bytes changed: system %q messages %#v", got.System, got.Messages)
	}
}

func TestSpawnCustomSystemFallbackPreservesExactBytes(t *testing.T) {
	t.Parallel()
	rc := agentpkg.RunConfig{
		System: "  custom spawn system\n", SessionType: sessionmode.Subagent,
		Messages: []sdk.Message{sdk.UserMessage("history")}, ContextQueryMaterialized: true,
	}
	rc.ContextSourceFrags = agentpkg.SpawnContextSourceFrags(rc)
	got := contextview.ApplyProviderRunConfig(context.Background(), nil, rc)
	if got.System != rc.System || !reflect.DeepEqual(got.Messages, rc.Messages) {
		t.Fatalf("custom spawn fallback changed bytes: system %q messages %#v", got.System, got.Messages)
	}
}
