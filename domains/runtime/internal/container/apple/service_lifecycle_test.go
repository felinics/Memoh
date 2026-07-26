package apple

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/memohai/memoh/domains/runtime/container"
)

type fakeProcessManager struct {
	socketPath string
	startFn    func(context.Context) error
	stopFn     func() error
	starts     atomic.Int32
	stops      atomic.Int32

	mu       sync.Mutex
	startCtx context.Context
}

func (m *fakeProcessManager) Start(ctx context.Context) error {
	m.starts.Add(1)
	m.mu.Lock()
	m.startCtx = ctx
	m.mu.Unlock()
	if m.startFn != nil {
		return m.startFn(ctx)
	}
	return nil
}

func (m *fakeProcessManager) Stop() error {
	m.stops.Add(1)
	if m.stopFn != nil {
		return m.stopFn()
	}
	return nil
}

func (m *fakeProcessManager) SocketPath() string { return m.socketPath }

func (m *fakeProcessManager) context() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.startCtx
}

type fakeAppleClient struct {
	appleClient
	isServing func(context.Context) (bool, error)
	closes    atomic.Int32
}

func (c *fakeAppleClient) Close() error {
	c.closes.Add(1)
	return nil
}

func (c *fakeAppleClient) IsServing(ctx context.Context) (bool, error) {
	if c.isServing != nil {
		return c.isServing(ctx)
	}
	return true, nil
}

func TestServiceLifecycleIsExplicitAndIdempotent(t *testing.T) {
	manager := &fakeProcessManager{socketPath: "/tmp/fake-socktainer.sock"}
	client := &fakeAppleClient{}
	managerCreations := atomic.Int32{}
	svc := newService(testLogger(), func() processManager {
		managerCreations.Add(1)
		return manager
	}, func(string) (appleClient, error) {
		return client, nil
	})

	if got := managerCreations.Load(); got != 0 {
		t.Fatalf("constructor created %d managers, want 0", got)
	}
	if got := manager.starts.Load(); got != 0 {
		t.Fatalf("constructor started manager %d times, want 0", got)
	}

	startCtx, cancelStart := context.WithCancel(t.Context())
	if err := svc.Start(startCtx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := svc.Start(t.Context()); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if got := manager.starts.Load(); got != 1 {
		t.Fatalf("manager starts = %d, want 1", got)
	}

	cancelStart()
	select {
	case <-manager.context().Done():
		t.Fatal("canceling the completed startup context stopped the process context")
	default:
	}

	if err := svc.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-manager.context().Done():
	default:
		t.Fatal("Close did not cancel the process context")
	}
	if err := svc.Close(t.Context()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if got := manager.stops.Load(); got != 1 {
		t.Fatalf("manager stops = %d, want 1", got)
	}
	if got := client.closes.Load(); got != 1 {
		t.Fatalf("client closes = %d, want 1", got)
	}
	if err := svc.Start(t.Context()); !errors.Is(err, container.ErrRuntime) {
		t.Fatalf("Start after Close error = %v, want runtime stopped error", err)
	}
	if got := managerCreations.Load(); got != 1 {
		t.Fatalf("Start after Close created %d managers, want 1", got)
	}
}

func TestServiceStartHonorsStartupCancellation(t *testing.T) {
	manager := &fakeProcessManager{
		socketPath: "/tmp/fake-socktainer.sock",
		startFn: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	clientCreations := atomic.Int32{}
	svc := newService(testLogger(), func() processManager {
		return manager
	}, func(string) (appleClient, error) {
		clientCreations.Add(1)
		return &fakeAppleClient{}, nil
	})
	closeServiceOnCleanup(t, svc)

	startCtx, cancelStart := context.WithCancel(t.Context())
	cancelStart()
	if err := svc.Start(startCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start error = %v, want context canceled", err)
	}
	if got := manager.stops.Load(); got != 1 {
		t.Fatalf("manager stops after failed Start = %d, want 1", got)
	}
	if got := clientCreations.Load(); got != 0 {
		t.Fatalf("client creations after canceled Start = %d, want 0", got)
	}
}

func TestServiceStartHonorsStartupDeadline(t *testing.T) {
	manager := &fakeProcessManager{
		socketPath: "/tmp/fake-socktainer.sock",
		startFn: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	svc := newService(testLogger(), func() processManager {
		return manager
	}, func(string) (appleClient, error) {
		return &fakeAppleClient{}, nil
	})
	closeServiceOnCleanup(t, svc)

	startCtx, cancelStart := context.WithTimeout(t.Context(), 0)
	defer cancelStart()
	if err := svc.Start(startCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Start error = %v, want deadline exceeded", err)
	}
	if got := manager.stops.Load(); got != 1 {
		t.Fatalf("manager stops after timed-out Start = %d, want 1", got)
	}
}

func TestServiceStartCleansUpClientConstructionFailure(t *testing.T) {
	manager := &fakeProcessManager{socketPath: "/tmp/fake-socktainer.sock"}
	wantErr := errors.New("client construction failed")
	svc := newService(testLogger(), func() processManager {
		return manager
	}, func(string) (appleClient, error) {
		return nil, wantErr
	})
	closeServiceOnCleanup(t, svc)

	if err := svc.Start(t.Context()); !errors.Is(err, wantErr) {
		t.Fatalf("Start error = %v, want %v", err, wantErr)
	}
	if got := manager.stops.Load(); got != 1 {
		t.Fatalf("manager stops after client failure = %d, want 1", got)
	}
	select {
	case <-manager.context().Done():
	default:
		t.Fatal("failed Start did not cancel the process context")
	}
}

func TestServiceCloseHonorsContextAndRetainsStopResult(t *testing.T) {
	stopEntered := make(chan struct{})
	releaseStop := make(chan struct{})
	wantErr := errors.New("stop failed")
	manager := &fakeProcessManager{
		socketPath: "/tmp/fake-socktainer.sock",
		stopFn: func() error {
			close(stopEntered)
			<-releaseStop
			return wantErr
		},
	}
	svc := newService(testLogger(), func() processManager {
		return manager
	}, func(string) (appleClient, error) {
		return &fakeAppleClient{}, nil
	})
	if err := svc.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	stopCtx, cancelStop := context.WithCancel(t.Context())
	cancelStop()
	if err := svc.Close(stopCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close error = %v, want context canceled", err)
	}
	waitCtx, cancelWait := context.WithTimeout(t.Context(), time.Second)
	defer cancelWait()
	select {
	case <-stopEntered:
	case <-waitCtx.Done():
		t.Fatal("Close did not initiate manager stop")
	}
	close(releaseStop)

	if err := svc.Close(t.Context()); !errors.Is(err, wantErr) {
		t.Fatalf("second Close error = %v, want %v", err, wantErr)
	}
	if got := manager.stops.Load(); got != 1 {
		t.Fatalf("manager stops = %d, want 1", got)
	}
}

func TestServiceCloseRacingHealthCheckCannotRestart(t *testing.T) {
	healthEntered := make(chan struct{})
	releaseHealth := make(chan struct{})
	client := &fakeAppleClient{
		isServing: func(context.Context) (bool, error) {
			close(healthEntered)
			<-releaseHealth
			return false, nil
		},
	}
	manager := &fakeProcessManager{socketPath: "/tmp/fake-socktainer.sock"}
	managerCreations := atomic.Int32{}
	svc := newService(testLogger(), func() processManager {
		managerCreations.Add(1)
		return manager
	}, func(string) (appleClient, error) {
		return client, nil
	})
	if err := svc.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	healthResult := make(chan error, 1)
	go func() {
		_, err := svc.ensureHealthy(t.Context())
		healthResult <- err
	}()
	<-healthEntered

	closeResult := make(chan error, 1)
	go func() { closeResult <- svc.Close(t.Context()) }()
	waitCtx, cancelWait := context.WithTimeout(t.Context(), time.Second)
	defer cancelWait()
	select {
	case <-manager.context().Done():
	case <-waitCtx.Done():
		t.Fatal("Close did not signal the process context")
	}
	close(releaseHealth)

	if err := <-healthResult; !errors.Is(err, errServiceStopped) {
		t.Fatalf("ensureHealthy error = %v, want stopped", err)
	}
	if err := <-closeResult; err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := managerCreations.Load(); got != 1 {
		t.Fatalf("health check created %d managers during Close, want 1", got)
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func closeServiceOnCleanup(t *testing.T, svc *Service) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := svc.Close(ctx); err != nil {
			t.Errorf("Close during cleanup: %v", err)
		}
	})
}
