package workspace

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/config"
	ctr "github.com/felinics/memoh/internal/container"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

type nativeRestartTestService struct {
	legacyRouteTestService
	client  *bridge.Client
	mu      sync.Mutex
	running bool
}

func (s *nativeRestartTestService) MCPClient(context.Context, string) (*bridge.Client, error) {
	return s.client, nil
}

func (s *nativeRestartTestService) GetTaskInfo(context.Context, string) (ctr.TaskInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return ctr.TaskInfo{}, ctr.ErrNotFound
	}
	return ctr.TaskInfo{Status: ctr.TaskStatusRunning}, nil
}

func (s *nativeRestartTestService) StartContainer(context.Context, string, *ctr.StartTaskOptions) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = true
	s.startCalls++
	return nil
}

func (s *nativeRestartTestService) StopContainer(context.Context, string, *ctr.StopTaskOptions) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = false
	return nil
}

func TestNativeStopStartWaitsForMaintenanceBeforeReady(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	const botID = "11111111-1111-1111-1111-111111111111"
	container := ctr.ContainerInfo{ID: "native-restart", Labels: map[string]string{BotLabelKey: botID}}
	svc := &nativeRestartTestService{legacyRouteTestService: legacyRouteTestService{created: true, container: container, byLabel: []ctr.ContainerInfo{container}}, client: newDataIOTestBridgeClient(t, t.TempDir()), running: true}
	manager := newLegacyRouteTestManager(t, svc, config.WorkspaceConfig{})
	entered, finish := make(chan struct{}), make(chan struct{})
	var calls []string
	manager.OnNativeWorkspaceQuiescent(func(ctx context.Context, id string, client *bridge.Client) error {
		if id != botID || client != svc.client {
			t.Errorf("wrong maintenance workspace: %s", id)
		}
		calls = append(calls, "maintenance")
		close(entered)
		select {
		case <-finish:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	manager.OnNativeWorkspaceReady(func(context.Context, string) { calls = append(calls, "ready") })
	if err := manager.StopBot(ctx, botID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- manager.EnsureNativeRunning(ctx, botID) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("restart skipped maintenance")
	}
	select {
	case err := <-done:
		t.Fatalf("restart returned before maintenance: %v", err)
	default:
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(calls, []string{"maintenance", "ready"}) {
		t.Fatalf("startup order: %v", calls)
	}
	if svc.startCalls != 1 {
		t.Fatalf("start calls=%d", svc.startCalls)
	}
}
