package application

import (
	"context"
	"strings"
	"testing"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/workspace"
)

type workspaceRequestTargetService struct{}

func (workspaceRequestTargetService) ResolveWorkspaceTarget(_ context.Context, _ string, targetID string) (workspace.ResolvedWorkspaceTarget, error) {
	return workspace.ResolvedWorkspaceTarget{
		TargetID: strings.TrimSpace(targetID),
		Kind:     workspace.WorkspaceTargetRemote,
		Name:     "Computer B",
	}, nil
}

type workspaceRequestPermission bool

func (allowed workspaceRequestPermission) HasBotPermission(_ context.Context, _, _, permission string) (bool, error) {
	return bool(allowed) && permission == bots.PermissionWorkspaceRead, nil
}

func TestPrepareWorkspaceRequestRequiresWorkspaceRead(t *testing.T) {
	base := ChatRequest{BotID: "bot-1", WorkspaceTargetID: "computer-b"}

	resolver := &Service{workspaceTargets: workspaceRequestTargetService{}}
	if _, _, err := resolver.prepareWorkspaceRequest(t.Context(), base); err == nil || !strings.Contains(err.Error(), "user id") {
		t.Fatalf("missing user error = %v", err)
	}

	base.UserID = "user-1"
	if _, _, err := resolver.prepareWorkspaceRequest(t.Context(), base); err == nil || !strings.Contains(err.Error(), "permission checker") {
		t.Fatalf("missing checker error = %v", err)
	}

	resolver.botPermissions = workspaceRequestPermission(false)
	if _, _, err := resolver.prepareWorkspaceRequest(t.Context(), base); err == nil || !strings.Contains(err.Error(), "workspace_read") {
		t.Fatalf("denied permission error = %v", err)
	}

	resolver.botPermissions = workspaceRequestPermission(true)
	ctx, got, err := resolver.prepareWorkspaceRequest(t.Context(), base)
	if err != nil {
		t.Fatalf("prepare allowed request: %v", err)
	}
	if got.WorkspaceTarget == nil || got.WorkspaceTarget.TargetID != "computer-b" {
		t.Fatalf("workspace snapshot = %#v", got.WorkspaceTarget)
	}
	if targetID := workspace.WorkspaceTargetFromContext(ctx); targetID != "computer-b" {
		t.Fatalf("context target = %q, want computer-b", targetID)
	}
}

func TestInjectWorkspaceTransitionRecordsMarksComputerChanges(t *testing.T) {
	records := []historyfrag.HistoryRecord{
		workspaceHistoryRecord("user", "first", "computer-b", "remote", "Computer B", "/work/b"),
		workspaceHistoryRecord("assistant", "done", "computer-b", "remote", "Computer B", "/work/b"),
		workspaceHistoryRecord("user", "continue", "native", "native", "Server Workspace", "/data"),
	}

	got := injectWorkspaceTransitionRecords(records)
	if len(got) != 4 || got[0].Synthetic || !got[2].Synthetic {
		t.Fatalf("records = %#v, want one marker, at the switch", recordTexts(got))
	}
	switchText := got[2].ModelMessage.TextContent()
	for _, want := range []string{"Computer B", "Server Workspace", "do not transfer"} {
		if !strings.Contains(switchText, want) {
			t.Fatalf("switch marker %q does not contain %q", switchText, want)
		}
	}
}

func TestInjectWorkspaceTransitionRecordsIgnoresLegacyStartingFolderChanges(t *testing.T) {
	records := []historyfrag.HistoryRecord{
		workspaceHistoryRecord("user", "first", "computer-a", "remote", "Computer A", "/work/one"),
		workspaceHistoryRecord("user", "second", "computer-a", "remote", "Computer A", "/work/two"),
	}

	if got := injectWorkspaceTransitionRecords(records); len(got) != 2 {
		t.Fatalf("records = %#v, want no marker for a folder change", recordTexts(got))
	}
}

// The marker a request sends is the one the next turn replays for its row,
// and where the history window starts never adds one: a turn whose earlier
// rows were compacted renders its rows exactly as it sent them.
func TestWorkspaceTransitionMarkerMatchesLiveAndReplay(t *testing.T) {
	a := workspaceHistoryRecord("user", "on a", "computer-a", "remote", "Computer A", "/a")
	b := workspaceHistoryRecord("user", "to b", "computer-b", "remote", "Computer B", "/b")
	targetB := &WorkspaceTarget{TargetID: "computer-b", Kind: "remote", Name: "Computer B"}

	if got := currentWorkspaceTransitionMessage(nil, targetB); got != nil {
		t.Fatalf("first request marker = %q, want none", got.TextContent())
	}
	if got := injectWorkspaceTransitionRecords([]historyfrag.HistoryRecord{b}); len(got) != 1 {
		t.Fatalf("window starting at a located row = %#v, want no marker", recordTexts(got))
	}
	if got := currentWorkspaceTransitionMessage([]historyfrag.HistoryRecord{b}, targetB); got != nil {
		t.Fatalf("unchanged target marker = %q, want none", got.TextContent())
	}

	live := currentWorkspaceTransitionMessage(injectWorkspaceTransitionRecords([]historyfrag.HistoryRecord{a}), targetB)
	replayed := injectWorkspaceTransitionRecords([]historyfrag.HistoryRecord{a, b})
	if live == nil || len(replayed) != 3 || live.TextContent() != replayed[1].ModelMessage.TextContent() {
		t.Fatalf("live %v and replay %#v disagree on the switch marker", live, recordTexts(replayed))
	}
}

func workspaceHistoryRecord(role, text, targetID, kind, name, path string) historyfrag.HistoryRecord {
	return historyfrag.HistoryRecord{
		ModelMessage: ModelMessage{Role: role, Content: newTextContent(text)},
		Metadata: map[string]any{
			"execution_location": map[string]any{
				"target_id":      targetID,
				"kind":           kind,
				"name":           name,
				"workspace_path": path,
			},
		},
	}
}

func TestHistoryMessagesDeriveWorkspaceMarkers(t *testing.T) {
	t.Parallel()

	raw := []historyfrag.HistoryRecord{
		workspaceHistoryRecord("user", "on a", "computer-a", "remote", "Computer A", "/a"),
		workspaceHistoryRecord("user", "to b", "computer-b", "remote", "Computer B", "/b"),
	}
	messages, retained := historyMessagesFromRecords(raw)
	if len(retained) != 3 || len(messages) != 3 || !retained[1].Synthetic {
		t.Fatalf("retained = %#v, want the switch marker between the two messages", recordTexts(retained))
	}
}
