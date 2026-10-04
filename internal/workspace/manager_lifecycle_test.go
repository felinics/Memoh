package workspace

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/felinics/memoh/internal/config"
	ctr "github.com/felinics/memoh/internal/container"
	"github.com/felinics/memoh/internal/hooks"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// restartHookProvider counts how often the hook service reached for a
// workspace bridge. A restart that reaches the hook service asks for one; a
// restart that skips the hook never asks.
type restartHookProvider struct {
	clients int
}

func (p *restartHookProvider) MCPClient(context.Context, string) (*bridge.Client, error) {
	p.clients++
	return nil, errors.New("test provider has no workspace client")
}

// runningTaskTestService reports a running task so tests can exercise the
// "already running" branch of EnsureNativeRunning.
type runningTaskTestService struct {
	legacyRouteTestService
	task ctr.TaskInfo
}

func (s *runningTaskTestService) GetTaskInfo(context.Context, string) (ctr.TaskInfo, error) {
	return s.task, nil
}

func newRestartHookManager(t *testing.T, svc runtimeService) (*Manager, *restartHookProvider) {
	t.Helper()
	provider := &restartHookProvider{}
	m := newLegacyRouteTestManager(t, svc, config.WorkspaceConfig{})
	m.hookService = hooks.NewService(slog.New(slog.DiscardHandler), provider)
	return m, provider
}

// A stopped task is restarted by EnsureNativeRunning on the reconcile and
// container-start paths, which never run startWithResolvedConfig. The
// WorkspaceStart hook has to fire for those restarts too, otherwise workspace
// boot hooks are silently skipped after a restart or a reboot.
func TestEnsureNativeRunningEmitsWorkspaceStartHookOnRestart(t *testing.T) {
	botID := "00000000-0000-0000-0000-000000000001"
	container := ctr.ContainerInfo{ID: "workspace-restart", Image: "memohai/workspace:debian"}
	svc := &legacyRouteTestService{
		created:   true,
		container: container,
		byLabel:   []ctr.ContainerInfo{container},
	}
	m, provider := newRestartHookManager(t, svc)

	if err := m.EnsureNativeRunning(context.Background(), botID); err != nil {
		t.Fatalf("EnsureNativeRunning: %v", err)
	}
	if svc.startCalls != 1 {
		t.Fatalf("StartContainer calls = %d, want 1", svc.startCalls)
	}
	if provider.clients == 0 {
		t.Fatal("WorkspaceStart hook was not emitted for a restarted workspace task")
	}
}

// Nothing starts when the task is already running, so the hook must stay
// silent: EnsureNativeRunning runs before many workspace operations.
func TestEnsureNativeRunningSkipsWorkspaceStartHookWhenTaskRuns(t *testing.T) {
	botID := "00000000-0000-0000-0000-000000000002"
	container := ctr.ContainerInfo{ID: "workspace-running", Image: "memohai/workspace:debian"}
	svc := &runningTaskTestService{
		legacyRouteTestService: legacyRouteTestService{
			created:   true,
			container: container,
			byLabel:   []ctr.ContainerInfo{container},
		},
		task: ctr.TaskInfo{Status: ctr.TaskStatusRunning},
	}
	m, provider := newRestartHookManager(t, svc)

	if err := m.EnsureNativeRunning(context.Background(), botID); err != nil {
		t.Fatalf("EnsureNativeRunning: %v", err)
	}
	if svc.startCalls != 0 {
		t.Fatalf("StartContainer calls = %d, want 0", svc.startCalls)
	}
	if provider.clients != 0 {
		t.Fatalf("hook service calls = %d, want 0 for an already running task", provider.clients)
	}
}
