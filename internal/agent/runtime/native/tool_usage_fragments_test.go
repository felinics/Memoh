package native

import (
	"context"
	"testing"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	tools "github.com/felinics/memoh/internal/agent/tool"
)

func TestAssembleToolsReturnsStructuredUsageFragments(t *testing.T) {
	t.Parallel()

	a := newTestAgent(
		&usageTestProvider{emitTool: true, toolName: "zeta_tool", usage: "Zeta usage"},
		&usageTestProvider{emitTool: true, toolName: "alpha_tool", usage: "Alpha 用法"},
	)

	_, flat, frags, _, err := a.assembleTools(
		context.Background(),
		RunConfig{},
		tools.StreamEmitter(func(tools.ToolStreamEvent) {}),
		true,
	)
	if err != nil {
		t.Fatalf("assembleTools error: %v", err)
	}

	wantFlat := "## Tool usage\n\nZeta usage\n\nAlpha 用法"
	if flat != wantFlat {
		t.Fatalf("flat usage = %q, want %q", flat, wantFlat)
	}
	if got := contextfrag.Render(frags).System; got != wantFlat {
		t.Fatalf("structured usage renders %q, want byte-identical %q", got, wantFlat)
	}

	wantIDs := []string{
		"system.tool_usage.header",
		"system.tool_usage.zeta_tool",
		"system.tool_usage.alpha_tool",
	}
	wantCapabilities := []string{"zeta_tool", "zeta_tool", "alpha_tool"}
	if len(frags) != len(wantIDs) {
		t.Fatalf("structured usage fragments = %d, want %d", len(frags), len(wantIDs))
	}
	for i := range frags {
		if frags[i].ID != wantIDs[i] {
			t.Errorf("fragment %d ID = %q, want %q", i, frags[i].ID, wantIDs[i])
		}
		if frags[i].RequiredCapability != wantCapabilities[i] {
			t.Errorf(
				"fragment %d required capability = %q, want %q",
				i,
				frags[i].RequiredCapability,
				wantCapabilities[i],
			)
		}
		if frags[i].Render.GroupID != toolUsageGroupID || frags[i].Render.GroupJoiner != "\n\n" {
			t.Errorf("fragment %d render policy = %+v, want tool-usage group with blank-line join", i, frags[i].Render)
		}
	}
}
