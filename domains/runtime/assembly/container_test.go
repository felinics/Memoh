package assembly

import (
	"context"
	"log/slog"
	"testing"

	dockerclient "github.com/docker/docker/client"

	"github.com/memohai/memoh/domains/runtime/container"
)

func TestNewServiceDockerSlot(t *testing.T) {
	svc, cleanup, err := NewService(context.Background(), Deps{
		Log:     slog.Default(),
		Backend: container.BackendDocker,
	})
	if err != nil {
		t.Fatalf("NewService docker returned error: %v", err)
	}
	defer cleanup()
	imageSvc, ok := svc.(container.ImageService)
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
	if _, _, err := NewService(context.Background(), Deps{
		Log:     slog.Default(),
		Backend: "unknown",
	}); err == nil {
		t.Fatal("expected unknown backend error")
	}
}
