package provider

import (
	"context"
	"log/slog"
	"testing"

	dockerclient "github.com/docker/docker/client"

	"github.com/felinics/memoh/internal/config"
	containerapi "github.com/felinics/memoh/internal/container"
)

func TestProvideServiceDockerSlot(t *testing.T) {
	svc, cleanup, err := ProvideService(context.Background(), slog.Default(), config.Config{}, containerapi.BackendDocker)
	if err != nil {
		t.Fatalf("ProvideService docker returned error: %v", err)
	}
	defer cleanup()
	imageSvc, ok := svc.(containerapi.ImageService)
	if !ok {
		t.Fatal("docker service should expose optional ImageService")
	}
	_, imgErr := imageSvc.GetImage(context.Background(), "memohai/definitely-missing:test")
	switch {
	case containerapi.IsNotFound(imgErr):
		return
	case imgErr != nil && dockerclient.IsErrConnectionFailed(imgErr):
		t.Skipf("docker daemon unavailable: %v", imgErr)
	default:
		t.Fatalf("docker GetImage error = %v, want not found (or skip if daemon unreachable)", imgErr)
	}
}

func TestProvideServiceRejectsUnknownBackend(t *testing.T) {
	if _, _, err := ProvideService(context.Background(), slog.Default(), config.Config{}, "unknown"); err == nil {
		t.Fatal("expected unknown backend error")
	}
}
