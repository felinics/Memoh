package core

import (
	"context"
	"testing"
	"time"

	"go.uber.org/fx/fxtest"

	"github.com/memohai/memoh/domains/runtime/workspace"
)

type reconciliationManager struct {
	workspace.Service
	started chan struct{}
	stopped chan struct{}
}

func (m *reconciliationManager) ReconcileContainers(ctx context.Context) {
	close(m.started)
	<-ctx.Done()
	close(m.stopped)
}

func TestContainerReconciliationUsesApplicationLifetime(t *testing.T) {
	manager := &reconciliationManager{
		started: make(chan struct{}),
		stopped: make(chan struct{}),
	}
	lifecycle := fxtest.NewLifecycle(t, fxtest.EnforceTimeout(true))
	startContainerReconciliation(lifecycle, manager, nil, nil)

	lifecycle.RequireStart()
	waitForLifecycleSignal(t, manager.started, "reconciliation to start")

	lifecycle.RequireStop()
	waitForLifecycleSignal(t, manager.stopped, "reconciliation to stop")
}

func waitForLifecycleSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for %s", description)
	}
}
