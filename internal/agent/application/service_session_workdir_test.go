package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/workdir"
	"github.com/felinics/memoh/internal/workspace"
)

type sessionWorkdirSessionService struct {
	thread sessionpkg.Thread
}

func (s sessionWorkdirSessionService) Get(context.Context, string) (sessionpkg.Thread, error) {
	return s.thread, nil
}

func (s sessionWorkdirSessionService) UpdateTitle(context.Context, string, string) (sessionpkg.Thread, error) {
	return s.thread, nil
}

func (s sessionWorkdirSessionService) UpdateMetadata(context.Context, string, map[string]any) (sessionpkg.Thread, error) {
	return s.thread, nil
}

type fakeSessionWorkdirResolver struct {
	resolved workdir.Resolved
	err      error
}

func (f fakeSessionWorkdirResolver) ResolveForSession(context.Context, string, string) (workdir.Resolved, error) {
	return f.resolved, f.err
}

func workdirBoundService(resolved workdir.Resolved) *Service {
	return &Service{
		sessionService:   sessionWorkdirSessionService{thread: sessionpkg.Thread{ID: "s1", BotID: "bot-1", WorkdirID: "p1"}},
		workdirs:         fakeSessionWorkdirResolver{resolved: resolved},
		workspaceTargets: workspaceRequestTargetService{},
	}
}

func TestPrepareWorkspaceRequestRejectsWorkdirTargetConflict(t *testing.T) {
	service := workdirBoundService(workdir.Resolved{
		WorkdirID: "p1", TargetID: workspace.WorkspaceTargetNative, Kind: workdir.TargetKindNative, WorkDir: "/data/proj",
	})
	req := ChatRequest{BotID: "bot-1", ThreadID: "s1", UserID: "user-1", WorkspaceTargetID: "computer-b"}
	if _, _, err := service.prepareWorkspaceRequest(t.Context(), req); !errors.Is(err, ErrWorkspaceTargetWorkdirConflict) {
		t.Fatalf("error = %v, want ErrWorkspaceTargetWorkdirConflict", err)
	}
}

func TestPrepareWorkspaceRequestInjectsNativeWorkdirTargetWithoutWorkspaceRead(t *testing.T) {
	// A native workdir pins the same workspace every chat session already
	// uses — it must not demand workspace_read just to keep chatting.
	// botPermissions is deliberately nil: any permission check would panic
	// the "checker not configured" branch into an error.
	service := workdirBoundService(workdir.Resolved{
		WorkdirID: "p1", TargetID: workspace.WorkspaceTargetNative, Kind: workdir.TargetKindNative, WorkDir: "/data/proj",
	})
	ctx, got, err := service.prepareWorkspaceRequest(t.Context(), ChatRequest{BotID: "bot-1", ThreadID: "s1"})
	if err != nil {
		t.Fatalf("prepare error = %v", err)
	}
	if got.WorkspaceTargetID != workspace.WorkspaceTargetNative {
		t.Fatalf("WorkspaceTargetID = %q, want native", got.WorkspaceTargetID)
	}
	if targetID := workspace.WorkspaceTargetFromContext(ctx); targetID != workspace.WorkspaceTargetNative {
		t.Fatalf("context target = %q, want native", targetID)
	}
}

func TestPrepareWorkspaceRequestRemoteWorkdirRequiresWorkspaceRead(t *testing.T) {
	resolved := workdir.Resolved{
		WorkdirID: "p1", TargetID: "computer-b", Kind: workdir.TargetKindRemote, WorkDir: "/Users/alice/code",
	}

	denied := workdirBoundService(resolved)
	denied.botPermissions = workspaceRequestPermission(false)
	req := ChatRequest{BotID: "bot-1", ThreadID: "s1", UserID: "user-1"}
	if _, _, err := denied.prepareWorkspaceRequest(t.Context(), req); err == nil || !strings.Contains(err.Error(), "workspace_read") {
		t.Fatalf("denied error = %v, want workspace_read denial", err)
	}

	allowed := workdirBoundService(resolved)
	allowed.botPermissions = workspaceRequestPermission(true)
	ctx, got, err := allowed.prepareWorkspaceRequest(t.Context(), req)
	if err != nil {
		t.Fatalf("allowed error = %v", err)
	}
	if got.WorkspaceTargetID != "computer-b" {
		t.Fatalf("WorkspaceTargetID = %q, want computer-b", got.WorkspaceTargetID)
	}
	if targetID := workspace.WorkspaceTargetFromContext(ctx); targetID != "computer-b" {
		t.Fatalf("context target = %q, want computer-b", targetID)
	}
}

func TestPrepareWorkspaceRequestAcceptsMatchingExplicitTarget(t *testing.T) {
	service := workdirBoundService(workdir.Resolved{
		WorkdirID: "p1", TargetID: "computer-b", Kind: workdir.TargetKindRemote, WorkDir: "/Users/alice/code",
	})
	service.botPermissions = workspaceRequestPermission(true)
	req := ChatRequest{BotID: "bot-1", ThreadID: "s1", UserID: "user-1", WorkspaceTargetID: "computer-b"}
	if _, _, err := service.prepareWorkspaceRequest(t.Context(), req); err != nil {
		t.Fatalf("matching explicit target must pass, got %v", err)
	}
}

func TestResolveSessionWorkdirBindingFailsClosed(t *testing.T) {
	// A resolution failure must fail the turn, not silently degrade to the
	// default workspace: running in the wrong directory is the bug class
	// workdir bindings exist to eliminate.
	service := workdirBoundService(workdir.Resolved{})
	service.workdirs = fakeSessionWorkdirResolver{err: errors.New("boom")}
	if _, _, err := service.prepareWorkspaceRequest(t.Context(), ChatRequest{BotID: "bot-1", ThreadID: "s1"}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error = %v, want propagated resolution failure", err)
	}
}
