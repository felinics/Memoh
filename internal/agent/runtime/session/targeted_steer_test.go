package sessionruntime

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestTargetedSteerRejectsSuccessorRunAndConflictingReplay(t *testing.T) {
	b, key, handle := liveQueueFixture(t)
	runTargetedSteerContract(t, b, key, handle)
}

func runTargetedSteerContract(t *testing.T, b interface {
	targetedSteerBackend
	PendingQueues(context.Context, Key, int) ([]SteerItem, []FollowUpItem, error)
}, key Key, handle RunHandle,
) {
	t.Helper()
	ctx := context.Background()
	if _, err := b.EnqueueSteerForRun(ctx, key, "old-run", "wrong", "wrong", []byte("old request")); !errors.Is(err, ErrCommandTargetMismatch) {
		t.Fatalf("steer for old run = %v, want mismatch", err)
	}
	item, err := b.EnqueueSteerForRun(ctx, key, handle.RunID, "first", "first", []byte("message"))
	if err != nil || item.TargetRunID != handle.RunID {
		t.Fatalf("steer = %+v, %v", item, err)
	}
	if _, err := b.EnqueueSteerForRun(ctx, key, "other-run", "first", "first", []byte("message")); !errors.Is(err, ErrCommandTargetMismatch) {
		t.Fatalf("replay for different run = %v, want mismatch", err)
	}
	items, _, err := b.PendingQueues(ctx, key, 0)
	if err != nil || len(items) != 1 || items[0].TargetRunID != handle.RunID {
		t.Fatalf("pending = %+v, %v", items, err)
	}
}

func TestRedisTargetedSteerRejectsSuccessorRunOptional(t *testing.T) {
	redisURL := os.Getenv("MEMOH_TEST_REDIS_URL")
	if redisURL == "" {
		t.Skip("MEMOH_TEST_REDIS_URL is not set")
	}
	ctx := context.Background()
	prefix := uniqueRuntimeBackendPrefix("targeted-steer")
	first, err := NewRedisBackend(ctx, RedisOptions{URL: redisURL, KeyPrefix: prefix, StateTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := NewRedisBackend(ctx, RedisOptions{URL: redisURL, KeyPrefix: prefix, StateTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	key := Key{BotID: "targeted-bot", SessionID: "targeted-session"}
	ref := RunRef{BotID: key.BotID, SessionID: key.SessionID, RunID: "successor", OwnerID: "owner", Generation: "generation", FencingToken: 1}
	_, _, err = first.StartRun(ctx, key, ref, func(snapshot Snapshot, _ bool) (Snapshot, bool, error) {
		snapshot.BotID, snapshot.SessionID = key.BotID, key.SessionID
		snapshot.CurrentRunView = &CurrentRunView{RunID: ref.RunID, OwnerID: ref.OwnerID, Generation: ref.Generation, Status: RunStatusRunning, SteerSupported: true}
		return snapshot, true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	runTargetedSteerContract(t, second, key, RunHandle{RunID: ref.RunID})
}
