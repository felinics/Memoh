package identities_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/channel/identities"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

func setupChannelIdentityIdentityIntegrationTest(t *testing.T) (*identities.Service, dbstore.Queries, func()) {
	t.Helper()

	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("skip integration test: TEST_POSTGRES_DSN is not set")
	}

	ctx := context.Background()
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Skipf("skip integration test: cannot connect to database: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("skip integration test: database ping failed: %v", err)
	}

	queries := postgresstore.NewQueries(sqlc.New(pool))
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	svc := identities.NewService(logger, queries)
	return svc, queries, func() { pool.Close() }
}

func TestChannelIdentityResolveChannelIdentityStable(t *testing.T) {
	svc, _, cleanup := setupChannelIdentityIdentityIntegrationTest(t)
	defer cleanup()

	ctx := context.Background()
	externalID := fmt.Sprintf("stable_%d", time.Now().UnixNano())
	first, err := svc.ResolveByChannelIdentity(ctx, "feishu", externalID, "first", nil)
	if err != nil {
		t.Fatalf("first resolve failed: %v", err)
	}
	second, err := svc.ResolveByChannelIdentity(ctx, "feishu", externalID, "second", nil)
	if err != nil {
		t.Fatalf("second resolve failed: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("expected same channelIdentity id, got %s and %s", first.ID, second.ID)
	}
}
