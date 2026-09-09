package workspacedeps

import (
	"errors"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

func TestColdRuntimeQueuesMissingDependencyWithoutFetchingOnTurn(t *testing.T) {
	p, _, server := providerFixture(t)
	fixture := newServiceFixture(t)
	fixture.svc.provider = p
	fixture.svc.catalog = catalog.Empty()
	_, err := fixture.svc.ResolveLauncher(t.Context(), "bot1", "codex")
	var missing *external.DependencyMissingError
	if !errors.As(err, &missing) {
		t.Fatalf("missing feedback: %v", err)
	}
	if server.hits.Load() != 0 {
		t.Fatal("runtime performed a catalog network request")
	}
}
