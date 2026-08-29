package wikistore

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	dbpkg "github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	adapters "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/memory/migrate"
)

func TestPostgresUpsertNodeConcurrentlyUnionsSourceRefs(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)

	const teamID = "00000000-0000-0000-0000-000000000001"
	suffix := time.Now().UnixNano()
	uuidTail := suffix % 1_000_000_000_000
	userID := fmt.Sprintf("00000000-0000-0000-0000-%012d", uuidTail)
	botID := fmt.Sprintf("10000000-0000-0000-0000-%012d", uuidTail)
	username := fmt.Sprintf("pr1003-%d", uuidTail)
	email := username + "@example.test"
	botName := fmt.Sprintf("pr1003-%d", uuidTail)
	fixtureStatements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users (id, username, email) VALUES ($1, $2, $3)`, []any{userID, username, email}},
		{`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'member')`, []any{teamID, userID}},
		{`INSERT INTO bots (team_id, id, owner_user_id, name) VALUES ($1, $2, $3, $4)`, []any{teamID, botID, userID, botName}},
	}
	for _, statement := range fixtureStatements {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("create postgres fixtures: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM bots WHERE team_id = $1 AND id = $2", teamID, botID)
		_, _ = pool.Exec(context.Background(), "DELETE FROM team_members WHERE team_id = $1 AND user_id = $2", teamID, userID)
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", userID)
	})
	nodeID := fmt.Sprintf("pr1003-concurrent-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM memory_nodes WHERE bot_id = $1 AND id = $2", botID, nodeID)
	})

	store := NewPostgres(dbsqlc.New(pool))
	base := migrate.NodeSpec{
		ID: nodeID, BotID: botID, Body: "shared fact", Hash: "hash", Layer: migrate.LayerNote,
		SourceMessageIDs: []string{"session/base"}, CapturedAt: time.Now().UTC(),
	}
	if _, err := store.UpsertNode(ctx, base); err != nil {
		t.Fatalf("insert base node: %v", err)
	}

	const writers = 24
	start := make(chan struct{})
	errCh := make(chan error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			node := base
			node.SourceMessageIDs = []string{fmt.Sprintf("session/message-%02d", i)}
			_, writeErr := store.UpsertNode(ctx, node)
			errCh <- writeErr
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for writeErr := range errCh {
		if writeErr != nil {
			t.Fatalf("concurrent upsert: %v", writeErr)
		}
	}

	got, err := store.GetNode(ctx, botID, nodeID)
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	sort.Strings(got.SourceMessageIDs)
	want := make([]string, 0, writers+1)
	want = append(want, "session/base")
	for i := range writers {
		want = append(want, fmt.Sprintf("session/message-%02d", i))
	}
	sort.Strings(want)
	if fmt.Sprint(got.SourceMessageIDs) != fmt.Sprint(want) {
		t.Fatalf("source refs = %v, want %v", got.SourceMessageIDs, want)
	}

	for i := 0; i < adapters.MaxSourceRefsPerMemory+20; i++ {
		node := base
		node.SourceMessageIDs = []string{fmt.Sprintf("session/capped-%03d", i)}
		if _, err := store.UpsertNode(ctx, node); err != nil {
			t.Fatalf("bounded upsert %d: %v", i, err)
		}
	}
	got, err = store.GetNode(ctx, botID, nodeID)
	if err != nil {
		t.Fatalf("get bounded node: %v", err)
	}
	if len(got.SourceMessageIDs) != adapters.MaxSourceRefsPerMemory {
		t.Fatalf("bounded source refs = %d, want %d", len(got.SourceMessageIDs), adapters.MaxSourceRefsPerMemory)
	}
	if got.SourceMessageIDs[0] != "session/capped-020" || got.SourceMessageIDs[len(got.SourceMessageIDs)-1] != "session/capped-083" {
		t.Fatalf("bounded refs retained range = %q...%q", got.SourceMessageIDs[0], got.SourceMessageIDs[len(got.SourceMessageIDs)-1])
	}
}
