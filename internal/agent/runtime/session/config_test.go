package sessionruntime

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/config"
)

func TestNewManagerFromConfigDefersRedisIOUntilStart(t *testing.T) {
	t.Parallel()

	manager, err := NewManagerFromConfig(slog.Default(), config.SessionRuntimeConfig{
		Backend:       config.SessionRuntimeBackendRedis,
		StateTTL:      "1h",
		OwnerLeaseTTL: "30s",
		Redis: config.SessionRuntimeRedisConfig{
			URL:       "redis://127.0.0.1:1/0",
			KeyPrefix: "memoh:test:deferred-start:",
		},
	}, nil, nil)
	if err != nil {
		t.Fatalf("construct manager without Redis I/O: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Start(ctx); err == nil {
		t.Fatal("start unexpectedly succeeded against unavailable Redis")
	}
}
