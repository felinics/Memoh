package assembly

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dockerclient "github.com/docker/docker/client"

	"github.com/memohai/memoh/domains/runtime/container"
)

func TestNewServiceDockerSlot(t *testing.T) {
	managed, err := NewService(t.Context(), Deps{
		Log:     slog.Default(),
		Backend: container.BackendDocker,
	})
	if err != nil {
		t.Fatalf("NewService docker returned error: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := managed.Stop(ctx); err != nil {
			t.Errorf("Stop docker service: %v", err)
		}
	})
	imageSvc, ok := managed.Service.(container.ImageService)
	if !ok {
		t.Fatal("docker service should expose optional ImageService")
	}
	_, imgErr := imageSvc.GetImage(context.Background(), "memohai/definitely-missing:test")
	switch {
	case container.IsNotFound(imgErr):
		return
	case imgErr != nil && dockerclient.IsErrConnectionFailed(imgErr):
		t.Skipf("docker daemon unavailable: %v", imgErr)
	default:
		t.Fatalf("docker GetImage error = %v, want not found (or skip if daemon unreachable)", imgErr)
	}
}

func TestNewServiceRejectsUnknownBackend(t *testing.T) {
	if _, err := NewService(t.Context(), Deps{
		Log:     slog.Default(),
		Backend: "unknown",
	}); err == nil {
		t.Fatal("expected unknown backend error")
	}
}

func TestNewServiceAppleDefersProcessStart(t *testing.T) {
	missingBinary := filepath.Join(t.TempDir(), "missing-socktainer")
	managed, err := NewService(t.Context(), Deps{
		Log:     slog.Default(),
		Backend: container.BackendApple,
		Apple: AppleOptions{
			BinaryPath: missingBinary,
			SocketPath: filepath.Join(t.TempDir(), "socktainer.sock"),
		},
	})
	if err != nil {
		t.Fatalf("NewService apple returned error before lifecycle start: %v", err)
	}
	if managed.Service == nil {
		t.Fatal("NewService apple returned a nil service")
	}

	err = managed.Start(t.Context())
	if err == nil {
		t.Fatal("Start apple service with missing binary returned nil error")
	}
	if !strings.Contains(err.Error(), missingBinary) {
		t.Fatalf("Start apple error = %v, want missing binary path", err)
	}
	if err := managed.Stop(t.Context()); err != nil {
		t.Fatalf("Stop apple service after failed start: %v", err)
	}
}

func TestManagedServiceLifecycle(t *testing.T) {
	type contextKey string
	ctx := context.WithValue(t.Context(), contextKey("lifecycle"), "managed-service")
	wantStartErr := errors.New("start failed")
	wantStopErr := errors.New("stop failed")
	var starts, stops int
	var startCtx, stopCtx context.Context
	managed := &ManagedService{
		start: func(got context.Context) error {
			starts++
			startCtx = got
			return wantStartErr
		},
		stop: func(got context.Context) error {
			stops++
			stopCtx = got
			return wantStopErr
		},
	}

	if starts != 0 || stops != 0 {
		t.Fatalf("lifecycle ran during construction: starts=%d stops=%d", starts, stops)
	}
	if err := managed.Start(ctx); !errors.Is(err, wantStartErr) {
		t.Fatalf("Start error = %v, want %v", err, wantStartErr)
	}
	if starts != 1 || stops != 0 {
		t.Fatalf("after Start: starts=%d stops=%d, want 1/0", starts, stops)
	}
	if startCtx != ctx || startCtx.Value(contextKey("lifecycle")) != "managed-service" {
		t.Fatal("Start did not receive the original context")
	}
	if err := managed.Stop(ctx); !errors.Is(err, wantStopErr) {
		t.Fatalf("Stop error = %v, want %v", err, wantStopErr)
	}
	if starts != 1 || stops != 1 {
		t.Fatalf("after Stop: starts=%d stops=%d, want 1/1", starts, stops)
	}
	if stopCtx != ctx || stopCtx.Value(contextKey("lifecycle")) != "managed-service" {
		t.Fatal("Stop did not receive the original context")
	}
}

func TestManagedCloserHonorsContextAndRetainsCloseResult(t *testing.T) {
	closeEntered := make(chan struct{})
	releaseClose := make(chan struct{})
	wantErr := errors.New("close failed")
	closeCalls := 0
	closer := newManagedCloser(func() error {
		closeCalls++
		close(closeEntered)
		<-releaseClose
		return wantErr
	})

	stopCtx, cancelStop := context.WithCancel(t.Context())
	cancelStop()
	if err := closer.Stop(stopCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop error = %v, want context canceled", err)
	}
	waitCtx, cancelWait := context.WithTimeout(t.Context(), time.Second)
	defer cancelWait()
	select {
	case <-closeEntered:
	case <-waitCtx.Done():
		t.Fatal("Stop did not initiate close")
	}
	close(releaseClose)

	if err := closer.Stop(t.Context()); !errors.Is(err, wantErr) {
		t.Fatalf("second Stop error = %v, want %v", err, wantErr)
	}
	if closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1", closeCalls)
	}
}
