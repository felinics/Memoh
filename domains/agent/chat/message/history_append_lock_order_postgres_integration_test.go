package message_test

import (
	"context"
	"testing"
	"time"

	agentsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
)

func TestPostgresHistoryAppendAndAssetLinkShareSessionLockOrder(t *testing.T) {
	fixture := setupCommittedCompactionFixture(t)
	ctx := context.Background()

	assetTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin asset transaction: %v", err)
	}
	defer func() { _ = assetTx.Rollback(ctx) }()
	if _, err := assetTx.Exec(ctx, `
		SELECT id FROM agent.bot_sessions WHERE id = $1 FOR UPDATE
	`, fixture.sessionID); err != nil {
		t.Fatalf("lock asset owner session: %v", err)
	}

	appendTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin history append: %v", err)
	}
	defer func() { _ = appendTx.Rollback(ctx) }()
	appendPID := postgresBackendPID(t, appendTx)
	appended := make(chan error, 1)
	go func() {
		_, err := agentsqlc.New(appendTx).CreateMessageInHistoryTurnByRequestAndBind(ctx,
			agentsqlc.CreateMessageInHistoryTurnByRequestAndBindParams{
				Role:             "assistant",
				SessionID:        mustTestUUID(t, fixture.sessionID),
				RequestMessageID: mustTestUUID(t, fixture.user.ID),
				BotID:            mustTestUUID(t, fixture.botID),
				Content:          []byte(`{"role":"assistant","content":"concurrent"}`),
				Metadata:         []byte("{}"),
				Usage:            []byte("{}"),
				SessionMode:      "chat",
				RuntimeType:      "model",
			})
		appended <- err
	}()
	waitForPostgresLock(t, fixture.pool, appendPID)

	if _, err := agentsqlc.New(assetTx).CreateMessageAsset(ctx, agentsqlc.CreateMessageAssetParams{
		MessageID:   mustTestUUID(t, fixture.user.ID),
		Role:        "attachment",
		Ordinal:     0,
		ContentHash: "sha256:append-lock-order",
		Name:        "append.png",
		Metadata:    []byte("{}"),
	}); err != nil {
		t.Fatalf("link asset while history append waits for session: %v", err)
	}
	if err := assetTx.Commit(ctx); err != nil {
		t.Fatalf("commit asset link: %v", err)
	}
	select {
	case err := <-appended:
		if err != nil {
			t.Fatalf("append after asset commit: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("history append did not resume after asset commit")
	}
	if err := appendTx.Commit(ctx); err != nil {
		t.Fatalf("commit history append: %v", err)
	}
}
