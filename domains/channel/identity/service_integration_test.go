//go:build ignore

package identity_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/memohai/memoh/domains/channel/identity"
	channelpostgres "github.com/memohai/memoh/domains/channel/internal/postgres/gateway"
	"github.com/memohai/memoh/internal/db"
)

func setupIntegrationTest(t *testing.T) (*identity.Service, func()) {
	t.Helper()

	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("skip integration test: TEST_POSTGRES_DSN is not set")
	}

	ctx := context.Background()
	pool, err := db.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Skipf("skip integration test: cannot connect to database: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("skip integration test: database ping failed: %v", err)
	}

	store := channelpostgres.NewIdentityStoreFromPool(pool)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	svc := identity.NewService(logger, store)

	return svc, func() { pool.Close() }
}

func TestIntegrationResolveByChannelIdentityStability(t *testing.T) {
	svc, cleanup := setupIntegrationTest(t)
	defer cleanup()

	ctx := context.Background()
	key := fmt.Sprintf("ext_%d", time.Now().UnixNano())

	first, err := svc.ResolveByChannelIdentity(ctx, "feishu", key, "first", nil)
	if err != nil {
		t.Fatalf("first resolve failed: %v", err)
	}
	second, err := svc.ResolveByChannelIdentity(ctx, "feishu", key, "second", nil)
	if err != nil {
		t.Fatalf("second resolve failed: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("expected stable channelIdentity id, got %s and %s", first.ID, second.ID)
	}
}
