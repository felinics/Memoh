package assembly

import (
	"context"
	"log/slog"
	"testing"

	"github.com/memohai/memoh/domains/runtime/container"
	runtimenetwork "github.com/memohai/memoh/domains/runtime/network"
)

type stubNetworkConfigReader struct{}

func (stubNetworkConfigReader) GetBotOverlayConfig(context.Context, string) (runtimenetwork.BotOverlayConfig, error) {
	return runtimenetwork.BotOverlayConfig{}, nil
}

type stubContainerService struct{ container.Service }

func TestNewNetworkRequiresDeps(t *testing.T) {
	if _, err := NewNetwork(NetworkDeps{}); err == nil {
		t.Fatal("expected missing deps error")
	}
	if _, err := NewNetwork(NetworkDeps{Container: stubContainerService{}, ConfigReader: stubNetworkConfigReader{}}); err == nil {
		t.Fatal("expected missing pool error")
	}
}

func TestNewNetworkRejectsNilContainer(t *testing.T) {
	if _, err := NewNetwork(NetworkDeps{
		Log:          slog.Default(),
		ConfigReader: stubNetworkConfigReader{},
	}); err == nil {
		t.Fatal("expected container required error")
	}
}
