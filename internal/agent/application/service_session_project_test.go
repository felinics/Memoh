package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	sessionpkg "github.com/memohai/memoh/internal/chat/thread"
	"github.com/memohai/memoh/internal/project"
	"github.com/memohai/memoh/internal/workspace"
)

type sessionProjectSessionService struct {
	thread sessionpkg.Thread
}

func (s sessionProjectSessionService) Get(context.Context, string) (sessionpkg.Thread, error) {
	return s.thread, nil
}

func (s sessionProjectSessionService) UpdateTitle(context.Context, string, string) (sessionpkg.Thread, error) {
	return s.thread, nil
}

func (s sessionProjectSessionService) UpdateMetadata(context.Context, string, map[string]any) (sessionpkg.Thread, error) {
	return s.thread, nil
}

type fakeSessionProjectResolver struct {
	resolved project.Resolved
	err      error
}

func (f fakeSessionProjectResolver) ResolveForSession(context.Context, string, string) (project.Resolved, error) {
	return f.resolved, f.err
}

func projectBoundService(resolved project.Resolved) *Service {
	return &Service{
		sessionService:   sessionProjectSessionService{thread: sessionpkg.Thread{ID: "s1", BotID: "bot-1", ProjectID: "p1"}},
		projects:         fakeSessionProjectResolver{resolved: resolved},
		workspaceTargets: workspaceRequestTargetService{},
	}
}

func TestPrepareWorkspaceRequestRejectsProjectTargetConflict(t *testing.T) {
	service := projectBoundService(project.Resolved{
		ProjectID: "p1", TargetID: workspace.WorkspaceTargetNative, Kind: project.TargetKindNative, WorkDir: "/data/proj",
	})
	req := ChatRequest{BotID: "bot-1", ThreadID: "s1", UserID: "user-1", WorkspaceTargetID: "computer-b"}
	if _, _, err := service.prepareWorkspaceRequest(t.Context(), req); !errors.Is(err, ErrWorkspaceTargetProjectConflict) {
		t.Fatalf("error = %v, want ErrWorkspaceTargetProjectConflict", err)
	}
}

func TestPrepareWorkspaceRequestInjectsNativeProjectTargetWithoutWorkspaceRead(t *testing.T) {
	// A native project pins the same workspace every chat session already
	// uses — it must not demand workspace_read just to keep chatting.
	// botPermissions is deliberately nil: any permission check would panic
	// the "checker not configured" branch into an error.
	service := projectBoundService(project.Resolved{
		ProjectID: "p1", TargetID: workspace.WorkspaceTargetNative, Kind: project.TargetKindNative, WorkDir: "/data/proj",
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

func TestPrepareWorkspaceRequestRemoteProjectRequiresWorkspaceRead(t *testing.T) {
	resolved := project.Resolved{
		ProjectID: "p1", TargetID: "computer-b", Kind: project.TargetKindRemote, WorkDir: "/Users/alice/code",
	}

	denied := projectBoundService(resolved)
	denied.botPermissions = workspaceRequestPermission(false)
	req := ChatRequest{BotID: "bot-1", ThreadID: "s1", UserID: "user-1"}
	if _, _, err := denied.prepareWorkspaceRequest(t.Context(), req); err == nil || !strings.Contains(err.Error(), "workspace_read") {
		t.Fatalf("denied error = %v, want workspace_read denial", err)
	}

	allowed := projectBoundService(resolved)
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
	service := projectBoundService(project.Resolved{
		ProjectID: "p1", TargetID: "computer-b", Kind: project.TargetKindRemote, WorkDir: "/Users/alice/code",
	})
	service.botPermissions = workspaceRequestPermission(true)
	req := ChatRequest{BotID: "bot-1", ThreadID: "s1", UserID: "user-1", WorkspaceTargetID: "computer-b"}
	if _, _, err := service.prepareWorkspaceRequest(t.Context(), req); err != nil {
		t.Fatalf("matching explicit target must pass, got %v", err)
	}
}

func TestResolveSessionProjectBindingFailsClosed(t *testing.T) {
	// A resolution failure must fail the turn, not silently degrade to the
	// default workspace: running in the wrong directory is the bug class
	// project bindings exist to eliminate.
	service := projectBoundService(project.Resolved{})
	service.projects = fakeSessionProjectResolver{err: errors.New("boom")}
	if _, _, err := service.prepareWorkspaceRequest(t.Context(), ChatRequest{BotID: "bot-1", ThreadID: "s1"}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error = %v, want propagated resolution failure", err)
	}
}
