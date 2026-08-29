package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/bots"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/workspace"
)

type workspaceRequestTargetService struct{}

func (workspaceRequestTargetService) ResolveWorkspaceTarget(_ context.Context, _ string, targetID string) (workspace.ResolvedWorkspaceTarget, error) {
	targetID = strings.TrimSpace(targetID)
	if targetID == "" || targetID == workspace.WorkspaceTargetNative {
		return workspace.ResolvedWorkspaceTarget{
			TargetID: workspace.WorkspaceTargetNative,
			Kind:     workspace.WorkspaceTargetNative,
			Name:     "Server Workspace",
		}, nil
	}
	return workspace.ResolvedWorkspaceTarget{
		TargetID: targetID,
		Kind:     workspace.WorkspaceTargetRemote,
		Name:     "Computer B",
	}, nil
}

// workspaceRequestRemotePrimaryService models a bot whose Primary workspace is
// a connected computer: ambient resolution (no requested target) lands remote.
type workspaceRequestRemotePrimaryService struct{}

func (workspaceRequestRemotePrimaryService) ResolveWorkspaceTarget(_ context.Context, _ string, targetID string) (workspace.ResolvedWorkspaceTarget, error) {
	id := strings.TrimSpace(targetID)
	if id == "" {
		id = "computer-primary"
	}
	return workspace.ResolvedWorkspaceTarget{
		TargetID: id,
		Kind:     workspace.WorkspaceTargetRemote,
		Name:     "Primary Computer",
	}, nil
}

type workspaceRequestPermission bool

func (allowed workspaceRequestPermission) HasBotPermission(_ context.Context, _, _, permission string) (bool, error) {
	return bool(allowed) && permission == bots.PermissionWorkspaceRead, nil
}

func TestPrepareWorkspaceRequestRequiresWorkspaceRead(t *testing.T) {
	base := ChatRequest{BotID: "bot-1", WorkspaceTargetID: "computer-b"}

	resolver := &Service{workspaceTargets: workspaceRequestTargetService{}}
	if _, _, err := resolver.prepareWorkspaceRequest(t.Context(), base); !errors.Is(err, ErrWorkspaceTargetNeedsActor) {
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
	if len(got) != 5 {
		t.Fatalf("record count = %d, want 5", len(got))
	}
	if got[0].ModelMessage.Role != "system" || !strings.Contains(got[0].ModelMessage.TextContent(), "Computer B") {
		t.Fatalf("initial marker = %#v", got[0].ModelMessage)
	}
	if got[3].ModelMessage.Role != "system" {
		t.Fatalf("switch marker role = %q, want system", got[3].ModelMessage.Role)
	}
	switchText := got[3].ModelMessage.TextContent()
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

	got := injectWorkspaceTransitionRecords(records)
	if len(got) != 3 {
		t.Fatalf("record count = %d, want 3", len(got))
	}
	if strings.Contains(got[0].ModelMessage.TextContent(), "starting_folder") {
		t.Fatalf("legacy workspace_path leaked into marker: %#v", got[0].ModelMessage)
	}
}

func TestWorkspaceTransitionRendererMatchesLiveAndReplay(t *testing.T) {
	current := &WorkspaceTarget{TargetID: "computer-b", Kind: "remote", Name: "Computer B"}
	service := &Service{}
	live := service.currentWorkspaceContextMessage(t.Context(), ChatRequest{WorkspaceTarget: current})
	if live == nil {
		t.Fatal("initial live context must include a workspace snapshot")
	}

	replayed := injectWorkspaceTransitionRecords([]historyfrag.HistoryRecord{
		workspaceHistoryRecord("user", "first", current.TargetID, current.Kind, current.Name, "/work/b"),
	})
	if len(replayed) != 2 {
		t.Fatalf("replayed records = %d, want marker + message", len(replayed))
	}
	if got, want := live.TextContent(), replayed[0].ModelMessage.TextContent(); got != want {
		t.Fatalf("initial live/replay marker mismatch:\nlive:   %s\nreplay: %s", got, want)
	}
}

func TestCurrentWorkspaceContextMessageOmitsUnchangedTarget(t *testing.T) {
	current := &WorkspaceTarget{TargetID: "computer-b", Kind: "remote", Name: "Computer B"}
	service := &Service{
		sessionService: &fakeBackgroundSessionService{
			getFn: func(context.Context, string) (sessionpkg.Thread, error) {
				return sessionpkg.Thread{Metadata: map[string]any{
					"workspace_target": map[string]any{
						"target_id": current.TargetID,
						"kind":      current.Kind,
						"name":      current.Name,
					},
				}}, nil
			},
		},
	}

	if got := service.currentWorkspaceContextMessage(t.Context(), ChatRequest{
		ThreadID:        "session-1",
		WorkspaceTarget: current,
	}); got != nil {
		t.Fatalf("unchanged workspace marker = %q, want nil", got.TextContent())
	}
}

func TestWorkspaceTransitionRendererMatchesLiveAndReplayAfterChange(t *testing.T) {
	previous := &WorkspaceTarget{TargetID: "computer-a", Kind: "remote", Name: "Computer A"}
	current := &WorkspaceTarget{TargetID: "computer-b", Kind: "remote", Name: "Computer B"}
	service := &Service{
		sessionService: &fakeBackgroundSessionService{
			getFn: func(context.Context, string) (sessionpkg.Thread, error) {
				return sessionpkg.Thread{Metadata: map[string]any{
					"workspace_target": map[string]any{
						"target_id": previous.TargetID,
						"kind":      previous.Kind,
						"name":      previous.Name,
					},
				}}, nil
			},
		},
	}
	live := service.currentWorkspaceContextMessage(t.Context(), ChatRequest{
		ThreadID:        "session-1",
		WorkspaceTarget: current,
	})
	if live == nil {
		t.Fatal("changed live context must include a workspace transition")
	}

	replayed := injectWorkspaceTransitionRecords([]historyfrag.HistoryRecord{
		workspaceHistoryRecord("user", "first", previous.TargetID, previous.Kind, previous.Name, "/work/a"),
		workspaceHistoryRecord("user", "second", current.TargetID, current.Kind, current.Name, "/work/b"),
	})
	if len(replayed) != 4 {
		t.Fatalf("replayed records = %d, want two markers + two messages", len(replayed))
	}
	if got, want := live.TextContent(), replayed[2].ModelMessage.TextContent(); got != want {
		t.Fatalf("changed live/replay marker mismatch:\nlive:   %s\nreplay: %s", got, want)
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

func TestTrimDerivesGoverningMarkersForEveryKeptRun(t *testing.T) {
	t.Parallel()

	raw := []historyfrag.HistoryRecord{
		workspaceHistoryRecord("user", strings.Repeat("work on a ", 30), "computer-a", "remote", "Computer A", "/a"),
		workspaceHistoryRecord("user", strings.Repeat("first b step ", 30), "computer-b", "remote", "Computer B", "/b"),
		workspaceHistoryRecord("assistant", strings.Repeat("second b step ", 30), "computer-b", "remote", "Computer B", "/b"),
		workspaceHistoryRecord("user", "current question", "computer-b", "remote", "Computer B", "/b"),
	}

	// Budget that cuts inside Computer B's run: whichever B messages survive,
	// the derived marker must govern them, the notice must be charged, and
	// the declared budget must stay a hard bound.
	budget := estimateMessageTokens(raw[3].ModelMessage) +
		estimateMessageTokens(raw[2].ModelMessage) +
		estimateMessageTokens(raw[2].ModelMessage)/2

	messages, retained, estimated := trimMessagesAndRecordsByTokens(nil, raw, budget)
	if estimated > budget {
		t.Fatalf("estimated = %d, want a hard bound at budget %d including markers and notice", estimated, budget)
	}
	if len(messages) == 0 || !strings.Contains(messages[0].TextContent(), "trimmed") {
		t.Fatalf("trim notice missing after a real cut: %#v", messages)
	}
	assertGovernedWorkspaceRuns(t, retained)

	// The kept B tail must be governed by a Computer B marker even though the
	// cut removed earlier B messages.
	foundB := false
	for index, record := range retained {
		if record.Synthetic || !strings.Contains(record.ModelMessage.TextContent(), "b step") &&
			!strings.Contains(record.ModelMessage.TextContent(), "current question") {
			continue
		}
		foundB = true
		governed := false
		for _, earlier := range retained[:index] {
			if earlier.Synthetic && strings.Contains(earlier.ModelMessage.TextContent(), "Computer B") {
				governed = true
			}
		}
		if !governed {
			t.Fatalf("kept Computer B message %q without a governing marker: %#v", record.ModelMessage.TextContent(), recordTexts(retained))
		}
	}
	if !foundB {
		t.Fatalf("budget unexpectedly dropped the whole B run: %#v", recordTexts(retained))
	}
}

func TestTrimDerivesMarkersForRequiredPullbacks(t *testing.T) {
	t.Parallel()

	required := workspaceHistoryRecord("user", strings.Repeat("required a question ", 20), "computer-a", "remote", "Computer A", "/a")
	required.Required = true
	raw := []historyfrag.HistoryRecord{
		required,
		workspaceHistoryRecord("assistant", strings.Repeat("filler between runs ", 40), "computer-a", "remote", "Computer A", "/a"),
		workspaceHistoryRecord("user", "current on b", "computer-b", "remote", "Computer B", "/b"),
	}
	budget := estimateMessageTokens(raw[0].ModelMessage) + estimateMessageTokens(raw[2].ModelMessage) + estimateMessageTokens(raw[2].ModelMessage)/2

	_, retained, _ := trimMessagesAndRecordsByTokens(nil, raw, budget)
	keptRequired := false
	for _, record := range retained {
		if record.Required {
			keptRequired = true
		}
	}
	if !keptRequired {
		t.Fatalf("required record dropped: %#v", recordTexts(retained))
	}
	assertGovernedWorkspaceRuns(t, retained)
}

func TestTrimWithoutBudgetDerivesMarkers(t *testing.T) {
	t.Parallel()

	raw := []historyfrag.HistoryRecord{
		workspaceHistoryRecord("user", "on a", "computer-a", "remote", "Computer A", "/a"),
		workspaceHistoryRecord("user", "to b", "computer-b", "remote", "Computer B", "/b"),
	}
	messages, retained, _ := trimMessagesAndRecordsByTokens(nil, raw, 0)
	if len(retained) != 4 || len(messages) != 4 {
		t.Fatalf("retained/messages = %d/%d, want two markers and two messages", len(retained), len(messages))
	}
	assertGovernedWorkspaceRuns(t, retained)
}

// assertGovernedWorkspaceRuns fails when any kept message carrying an
// execution location is not preceded by a marker for that location.
func assertGovernedWorkspaceRuns(t *testing.T, retained []historyfrag.HistoryRecord) {
	t.Helper()
	current := ""
	for index, record := range retained {
		if record.Synthetic {
			continue
		}
		target := workspaceTargetFromMetadata(record.Metadata)
		if target == nil {
			continue
		}
		governed := false
		for _, earlier := range retained[:index] {
			if earlier.Synthetic && strings.Contains(earlier.ModelMessage.TextContent(), "target_id=\""+target.TargetID+"\"") {
				governed = true
			}
		}
		if !governed {
			t.Fatalf("record %d (%s) is not governed by a marker for %s: %#v", index, record.ModelMessage.TextContent(), target.TargetID, recordTexts(retained))
		}
		current = target.TargetID
	}
	_ = current
}

func TestPrepareWorkspaceRequestAmbientRemotePrimaryRequiresWorkspaceRead(t *testing.T) {
	// No workdir binding and no explicit target: the turn still lands on the
	// bot's remote Primary computer, so it crosses the same permission
	// boundary as an explicit selection.
	req := ChatRequest{BotID: "bot-1", ThreadID: "s1", UserID: "user-1"}

	denied := &Service{
		workspaceTargets: workspaceRequestRemotePrimaryService{},
		botPermissions:   workspaceRequestPermission(false),
	}
	if _, _, err := denied.prepareWorkspaceRequest(t.Context(), req); err == nil || !strings.Contains(err.Error(), "workspace_read") {
		t.Fatalf("denied error = %v, want workspace_read denial", err)
	}

	allowed := &Service{
		workspaceTargets: workspaceRequestRemotePrimaryService{},
		botPermissions:   workspaceRequestPermission(true),
	}
	_, got, err := allowed.prepareWorkspaceRequest(t.Context(), req)
	if err != nil {
		t.Fatalf("allowed error = %v", err)
	}
	if got.WorkspaceTargetID != "computer-primary" {
		t.Fatalf("WorkspaceTargetID = %q, want computer-primary", got.WorkspaceTargetID)
	}
}

func TestPrepareWorkspaceRequestAmbientNativePrimaryStaysUngated(t *testing.T) {
	// Plain chat on a native-Primary bot must not demand workspace_read.
	// botPermissions is deliberately nil: any check would error loudly.
	service := &Service{workspaceTargets: workspaceRequestTargetService{}}
	_, got, err := service.prepareWorkspaceRequest(t.Context(), ChatRequest{BotID: "bot-1", ThreadID: "s1"})
	if err != nil {
		t.Fatalf("prepare error = %v", err)
	}
	if got.WorkspaceTargetID != workspace.WorkspaceTargetNative {
		t.Fatalf("WorkspaceTargetID = %q, want native", got.WorkspaceTargetID)
	}
}
