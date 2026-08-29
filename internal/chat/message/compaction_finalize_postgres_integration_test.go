package message

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func TestPostgresCompactionCompletionWaitsForEpochMutation(t *testing.T) {
	fixture := setupCommittedCompactionFixture(t)
	ctx := context.Background()
	log := fixture.createLog(t)
	messageIDs := fixtureMessageIDs(t, fixture)
	marked, err := dbsqlc.New(fixture.pool).MarkMessagesCompacted(ctx, dbsqlc.MarkMessagesCompactedParams{
		CompactID:          log.ID,
		MessageIds:         messageIDs,
		ExpectedCompactIds: emptyCompactionClaims(len(messageIDs)),
	})
	if err != nil || marked != int64(len(messageIDs)) {
		t.Fatalf("claim compaction sources = %d, %v", marked, err)
	}

	assetTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin asset mutation: %v", err)
	}
	defer func() { _ = assetTx.Rollback(ctx) }()
	if _, err := dbsqlc.New(assetTx).CreateMessageAsset(ctx, dbsqlc.CreateMessageAssetParams{
		MessageID:   mustTestUUID(t, fixture.assistant.ID),
		Role:        "attachment",
		Ordinal:     0,
		ContentHash: "sha256:finalize-overlap",
		Name:        "overlap.png",
		Metadata:    []byte("{}"),
	}); err != nil {
		t.Fatalf("mutate claimed source asset: %v", err)
	}

	completeTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin completion: %v", err)
	}
	defer func() { _ = completeTx.Rollback(ctx) }()
	completePID := postgresBackendPID(t, completeTx)
	completed := make(chan error, 1)
	go func() {
		_, err := dbsqlc.New(completeTx).CompleteCompactionLog(ctx, successfulCompaction(log.ID))
		completed <- err
	}()
	waitForPostgresLock(t, fixture.pool, completePID)

	if err := assetTx.Commit(ctx); err != nil {
		t.Fatalf("commit source mutation: %v", err)
	}
	select {
	case err := <-completed:
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("completion after epoch mutation = %v, want pgx.ErrNoRows", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("completion did not resume after source mutation")
	}
	if err := completeTx.Commit(ctx); err != nil {
		t.Fatalf("commit rejected completion: %v", err)
	}

	var status string
	if err := fixture.pool.QueryRow(ctx, `
		SELECT status FROM bot_history_message_compacts WHERE id = $1
	`, log.ID).Scan(&status); err != nil {
		t.Fatalf("read rejected compaction status: %v", err)
	}
	if status != "pending" {
		t.Fatalf("rejected compaction status = %q, want pending", status)
	}
	if got := fixture.epoch(t); got != 1 {
		t.Fatalf("source mutation epoch = %d, want 1", got)
	}
}
