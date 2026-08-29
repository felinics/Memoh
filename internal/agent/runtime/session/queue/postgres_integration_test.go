package queue_test

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/runtime/session/queue"
	"github.com/felinics/memoh/internal/agent/turn"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/dbtest"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

var (
	queueMigration    sync.Once
	queueMigrationErr error
)

func TestPostgresQueuesFIFOIsolationReplayAndTerminalRace(t *testing.T) {
	ctx := context.Background()
	pool := openQueuePostgres(t, ctx)
	botID, sessionID, runID := createQueueFixture(t, ctx, pool)
	queries := postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))

	enqueueSteer := func(itemID, invocation string) (queue.SteerItem, error) {
		var item queue.SteerItem
		err := queries.InTx(ctx, func(txq dbstore.Queries) error {
			if _, err := txq.LockSessionForQueueAdmission(ctx, dbsqlc.LockSessionForQueueAdmissionParams{BotID: mustUUID(t, botID), SessionID: mustUUID(t, sessionID)}); err != nil {
				return err
			}
			var err error
			item, err = queue.NewPostgresStore(txq).EnqueueSteer(ctx, botID, sessionID, itemID, invocation, []byte(`{"text":"steer"}`))
			return err
		})
		return item, err
	}
	enqueueFollowUp := func(itemID, invocation string) (queue.FollowUpItem, error) {
		var item queue.FollowUpItem
		err := queries.InTx(ctx, func(txq dbstore.Queries) error {
			if _, err := txq.LockSessionForQueueAdmission(ctx, dbsqlc.LockSessionForQueueAdmissionParams{BotID: mustUUID(t, botID), SessionID: mustUUID(t, sessionID)}); err != nil {
				return err
			}
			var err error
			item, err = queue.NewPostgresStore(txq).EnqueueFollowUp(ctx, botID, sessionID, itemID, invocation, []byte(`{"text":"follow"}`))
			return err
		})
		return item, err
	}

	first, err := enqueueSteer(uuid.NewString(), "steer-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := enqueueSteer(uuid.NewString(), "steer-2")
	if err != nil {
		t.Fatal(err)
	}
	third, err := enqueueSteer(uuid.NewString(), "steer-3")
	if err != nil {
		t.Fatal(err)
	}
	if first.Position >= second.Position || first.TargetRunID != runID || second.TargetRunID != runID {
		t.Fatalf("steer order/target = (%d,%d,%s,%s)", first.Position, second.Position, first.TargetRunID, second.TargetRunID)
	}
	firstFollow, err := enqueueFollowUp(uuid.NewString(), "steer-1")
	if err != nil {
		t.Fatal(err)
	}
	secondFollow, err := enqueueFollowUp(uuid.NewString(), "follow-2")
	if err != nil {
		t.Fatal(err)
	}
	if firstFollow.EnqueuedDuringRunID != runID || secondFollow.EnqueuedDuringRunID != runID {
		t.Fatalf("follow-up runs = %s/%s, want %s", firstFollow.EnqueuedDuringRunID, secondFollow.EnqueuedDuringRunID, runID)
	}

	store := queue.NewPostgresStore(queries)
	var reorderedSteers []queue.SteerItem
	if err := queries.InTx(ctx, func(txq dbstore.Queries) error {
		if _, err := txq.LockSessionForQueueAdmission(ctx, dbsqlc.LockSessionForQueueAdmissionParams{BotID: mustUUID(t, botID), SessionID: mustUUID(t, sessionID)}); err != nil {
			return err
		}
		var err error
		reorderedSteers, err = queue.NewPostgresStore(txq).ReorderSteer(ctx, sessionID, queue.SteerPendingRef{ItemID: third.ID}, queue.SteerPendingRef{ItemID: first.ID})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := steerIDs(reorderedSteers); strings.Join(got, ",") != strings.Join([]string{string(third.ID), string(first.ID), string(second.ID)}, ",") {
		t.Fatalf("reordered steer IDs = %v", got)
	}
	var reorderedFollowUps []queue.FollowUpItem
	if err := queries.InTx(ctx, func(txq dbstore.Queries) error {
		if _, err := txq.LockSessionForQueueAdmission(ctx, dbsqlc.LockSessionForQueueAdmissionParams{BotID: mustUUID(t, botID), SessionID: mustUUID(t, sessionID)}); err != nil {
			return err
		}
		var err error
		reorderedFollowUps, err = queue.NewPostgresStore(txq).ReorderFollowUp(ctx, sessionID, queue.FollowUpPendingRef{ItemID: secondFollow.ID}, queue.FollowUpPendingRef{ItemID: firstFollow.ID})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := followUpIDs(reorderedFollowUps); strings.Join(got, ",") != strings.Join([]string{string(secondFollow.ID), string(firstFollow.ID)}, ",") {
		t.Fatalf("reordered follow-up IDs = %v", got)
	}

	run := sessionruntime.RunHandle{BotID: botID, SessionID: sessionID, RunID: runID, OwnerID: "owner", FencingToken: 1}
	if _, err := store.ClaimSteer(ctx, string(third.ID), run); err != nil {
		t.Fatal(err)
	}
	if err := queries.InTx(ctx, func(txq dbstore.Queries) error {
		if _, err := txq.LockSessionForQueueAdmission(ctx, dbsqlc.LockSessionForQueueAdmissionParams{BotID: mustUUID(t, botID), SessionID: mustUUID(t, sessionID)}); err != nil {
			return err
		}
		_, err := queue.NewPostgresStore(txq).ReorderSteer(ctx, sessionID, queue.SteerPendingRef{ItemID: third.ID}, queue.SteerPendingRef{ItemID: first.ID})
		return err
	}); !errors.Is(err, queue.ErrNotPending) {
		t.Fatalf("reorder claimed steer = %v, want ErrNotPending", err)
	}

	steers, err := store.PendingSteer(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	follows, err := store.PendingFollowUp(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steers) != 2 || len(follows) != 2 {
		t.Fatalf("isolated lengths = steer %d, follow-up %d", len(steers), len(follows))
	}
	if got := steerIDs(steers); strings.Join(got, ",") != strings.Join([]string{string(first.ID), string(second.ID)}, ",") {
		t.Fatalf("pending steer IDs after rejected reorder = %v", got)
	}

	replayed, err := enqueueSteer(uuid.NewString(), "steer-1")
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ID != first.ID || replayed.Position != steers[0].Position {
		t.Fatalf("replay = %#v, want durable item %s at current position %d", replayed, first.ID, steers[0].Position)
	}
	if _, err := func() (queue.SteerItem, error) {
		var item queue.SteerItem
		err := queries.InTx(ctx, func(txq dbstore.Queries) error {
			if _, err := txq.LockSessionForQueueAdmission(ctx, dbsqlc.LockSessionForQueueAdmissionParams{BotID: mustUUID(t, botID), SessionID: mustUUID(t, sessionID)}); err != nil {
				return err
			}
			var err error
			item, err = queue.NewPostgresStore(txq).EnqueueSteer(ctx, botID, sessionID, uuid.NewString(), "steer-1", []byte(`{"text":"different"}`))
			return err
		})
		return item, err
	}(); !errors.Is(err, queue.ErrInvocationConflict) {
		t.Fatalf("conflicting replay = %v, want ErrInvocationConflict", err)
	}

	if _, err := pool.Exec(ctx, "UPDATE session_runs SET state='completed' WHERE run_id=$1", runID); err != nil {
		t.Fatal(err)
	}
	replayedAfterTerminal, err := enqueueSteer(uuid.NewString(), "steer-1")
	if err != nil || replayedAfterTerminal.ID != first.ID {
		t.Fatalf("replay after terminal = %#v, %v", replayedAfterTerminal, err)
	}
	if _, err := enqueueSteer(uuid.NewString(), "after-terminal"); !errors.Is(err, queue.ErrNoActiveRun) {
		t.Fatalf("enqueue after terminal = %v, want ErrNoActiveRun", err)
	}
}

func TestPostgresPromoteFollowUpToSteerIsAtomicAndIdempotent(t *testing.T) {
	ctx := context.Background()
	pool := openQueuePostgres(t, ctx)
	botID, sessionID, runID := createQueueFixture(t, ctx, pool)
	queries := postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))

	enqueueFollowUp := func(text string) queue.FollowUpItem {
		var item queue.FollowUpItem
		if err := queries.InTx(ctx, func(txq dbstore.Queries) error {
			if _, err := txq.LockSessionForQueueAdmission(ctx, dbsqlc.LockSessionForQueueAdmissionParams{
				BotID: mustUUID(t, botID), SessionID: mustUUID(t, sessionID),
			}); err != nil {
				return err
			}
			var err error
			item, err = queue.NewPostgresStore(txq).EnqueueFollowUp(
				ctx, botID, sessionID, uuid.NewString(), uuid.NewString(), []byte(`{"text":"`+text+`"}`),
			)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return item
	}
	promote := func(item queue.FollowUpItem) (queue.PromoteFollowUpResult, error) {
		var result queue.PromoteFollowUpResult
		err := queries.InTx(ctx, func(txq dbstore.Queries) error {
			if _, err := txq.LockSessionForQueueAdmission(ctx, dbsqlc.LockSessionForQueueAdmissionParams{
				BotID: mustUUID(t, botID), SessionID: mustUUID(t, sessionID),
			}); err != nil {
				return err
			}
			var err error
			result, err = queue.NewPromotionCoordinator(txq).PromoteFollowUpToSteer(ctx, queue.PromoteFollowUpRequest{
				BotID: botID, SessionID: sessionID,
				FollowUp: queue.FollowUpPendingRef{ItemID: item.ID},
			})
			return err
		})
		return result, err
	}

	first := enqueueFollowUp("promote me")
	result, err := promote(first)
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Steer.ID) != string(first.ID) || result.Steer.TargetRunID != runID || string(result.Steer.Payload) != string(first.Payload) {
		t.Fatalf("promotion result = %#v, source = %#v", result, first)
	}
	store := queue.NewPostgresStore(queries)
	if pending, err := store.PendingFollowUp(ctx, sessionID); err != nil || len(pending) != 0 {
		t.Fatalf("pending follow-ups after promotion = %#v, %v", pending, err)
	}
	if pending, err := store.PendingSteer(ctx, sessionID); err != nil || len(pending) != 1 || pending[0].ID != result.Steer.ID {
		t.Fatalf("pending steers after promotion = %#v, %v", pending, err)
	}

	replayed, err := promote(first)
	if err != nil || replayed.Steer.ID != result.Steer.ID {
		t.Fatalf("promotion replay = %#v, %v", replayed, err)
	}

	second := enqueueFollowUp("must survive")
	if _, err := pool.Exec(ctx, "UPDATE session_runs SET state='completed' WHERE run_id=$1", runID); err != nil {
		t.Fatal(err)
	}
	if _, err := promote(second); !errors.Is(err, queue.ErrNoActiveRun) {
		t.Fatalf("promotion without active run = %v, want ErrNoActiveRun", err)
	}
	pending, err := store.PendingFollowUp(ctx, sessionID)
	if err != nil || len(pending) != 1 || pending[0].ID != second.ID {
		t.Fatalf("follow-up changed after failed promotion = %#v, %v", pending, err)
	}
	if _, err := store.SteerByID(ctx, queue.SteerItemID(second.ID)); !errors.Is(err, queue.ErrInvalidReference) {
		t.Fatalf("failed promotion created steer = %v", err)
	}
}

func steerIDs(items []queue.SteerItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, string(item.ID))
	}
	return ids
}

func followUpIDs(items []queue.FollowUpItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, string(item.ID))
	}
	return ids
}

func TestPostgresConcurrentEnqueueAllocatesUniqueFIFOPositions(t *testing.T) {
	ctx := context.Background()
	pool := openQueuePostgres(t, ctx)
	botID, sessionID, _ := createQueueFixture(t, ctx, pool)
	queries := postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))
	const count = 12
	positions := make(chan int64, count)
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := queries.InTx(ctx, func(txq dbstore.Queries) error {
				if _, err := txq.LockSessionForQueueAdmission(ctx, dbsqlc.LockSessionForQueueAdmissionParams{BotID: mustUUID(t, botID), SessionID: mustUUID(t, sessionID)}); err != nil {
					return err
				}
				item, err := queue.NewPostgresStore(txq).EnqueueSteer(ctx, botID, sessionID, uuid.NewString(), uuid.NewString(), []byte(`{"text":"x"}`))
				if err == nil {
					positions <- item.Position
				}
				return err
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	close(positions)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got := make([]int64, 0, count)
	for p := range positions {
		got = append(got, p)
	}
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	for i, p := range got {
		if i > 0 && p != got[i-1]+1 {
			t.Fatalf("positions not contiguous: %v", got)
		}
	}
}

func TestPostgresConcurrentInvocationPayloadConflictIsNotSilentlyAccepted(t *testing.T) {
	ctx := context.Background()
	pool := openQueuePostgres(t, ctx)
	botID, sessionID, _ := createQueueFixture(t, ctx, pool)
	queries := postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))
	results := make(chan error, 2)
	start := make(chan struct{})
	for _, payload := range []string{`{"text":"first"}`, `{"text":"second"}`} {
		payload := []byte(payload)
		go func() {
			<-start
			results <- queries.InTx(ctx, func(txq dbstore.Queries) error {
				_, err := queue.NewPostgresStore(txq).EnqueueSteer(ctx, botID, sessionID, uuid.NewString(), "same-invocation", payload)
				return err
			})
		}()
	}
	close(start)
	var successes, conflicts int
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			successes++
		case errors.Is(err, queue.ErrInvocationConflict):
			conflicts++
		default:
			t.Fatalf("concurrent enqueue error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent invocation outcomes = successes %d, conflicts %d", successes, conflicts)
	}
}

func TestPostgresClaimReclaimFencingAndSingleOutstanding(t *testing.T) {
	ctx := context.Background()
	pool := openQueuePostgres(t, ctx)
	botID, sessionID, runID := createQueueFixture(t, ctx, pool)
	queries := postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))
	enqueue := func(invocation string) queue.SteerItem {
		t.Helper()
		var item queue.SteerItem
		if err := queries.InTx(ctx, func(txq dbstore.Queries) error {
			if _, err := txq.LockSessionForQueueAdmission(ctx, dbsqlc.LockSessionForQueueAdmissionParams{BotID: mustUUID(t, botID), SessionID: mustUUID(t, sessionID)}); err != nil {
				return err
			}
			var err error
			item, err = queue.NewPostgresStore(txq).EnqueueSteer(ctx, botID, sessionID, uuid.NewString(), invocation, []byte(`{"text":"fenced"}`))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return item
	}
	first, second := enqueue("fence-1"), enqueue("fence-2")
	store := queue.NewPostgresStore(queries)
	current := sessionruntime.RunHandle{BotID: botID, SessionID: sessionID, RunID: runID, OwnerID: "owner", FencingToken: 1}
	stale := current
	stale.OwnerID = "not-the-owner"
	if _, err := store.ClaimSteer(ctx, string(first.ID), stale); !errors.Is(err, queue.ErrNotPending) {
		t.Fatalf("claim with stale owner = %v, want ErrNotPending", err)
	}
	claimed, err := store.ClaimSteer(ctx, string(first.ID), current)
	if err != nil {
		t.Fatal(err)
	}
	oldRef := queue.SteerClaimRef{ItemID: claimed.ID, RunID: runID, OwnerID: current.OwnerID, FencingToken: current.FencingToken}
	if _, err := store.ClaimSteer(ctx, string(second.ID), current); !errors.Is(err, queue.ErrNotPending) {
		t.Fatalf("second outstanding claim = %v, want ErrNotPending", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE session_runs SET owner_id='successor', owner_since=now(), fencing_token=2 WHERE run_id=$1", runID); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplySteer(ctx, oldRef); !errors.Is(err, queue.ErrInvalidReference) {
		t.Fatalf("stale owner applied claim = %v, want ErrInvalidReference", err)
	}
	successor := current
	successor.OwnerID = "successor"
	successor.FencingToken = 2
	_, successorRef, err := store.ReclaimSteer(ctx, string(first.ID), successor)
	if err != nil {
		t.Fatal(err)
	}
	if successorRef.OwnerID != "successor" || successorRef.FencingToken != 2 {
		t.Fatalf("successor ref = %#v", successorRef)
	}
	if err := store.ApplySteer(ctx, oldRef); !errors.Is(err, queue.ErrInvalidReference) {
		t.Fatalf("old ref applied after reclaim = %v, want ErrInvalidReference", err)
	}
	if err := store.ApplySteer(ctx, successorRef); err != nil {
		t.Fatal(err)
	}
	var status string
	var claimRunID pgtype.UUID
	if err := pool.QueryRow(ctx, "SELECT status, claim_run_id FROM session_steer_queue WHERE item_id=$1", first.ID).Scan(&status, &claimRunID); err != nil {
		t.Fatal(err)
	}
	if status != string(queue.Applied) || claimRunID.Valid {
		t.Fatalf("applied row status/claim = %s/%v", status, claimRunID)
	}
	if _, err := store.ClaimSteer(ctx, string(second.ID), successor); err != nil {
		t.Fatalf("claim after prior applied: %v", err)
	}
}

func TestPostgresRejectClaimClearsClaimColumns(t *testing.T) {
	ctx := context.Background()
	pool := openQueuePostgres(t, ctx)
	botID, sessionID, runID := createQueueFixture(t, ctx, pool)
	queries := postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))
	var item queue.SteerItem
	if err := queries.InTx(ctx, func(txq dbstore.Queries) error {
		var err error
		item, err = queue.NewPostgresStore(txq).EnqueueSteer(ctx, botID, sessionID, uuid.NewString(), uuid.NewString(), []byte(`{"text":"reject"}`))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	run := sessionruntime.RunHandle{BotID: botID, SessionID: sessionID, RunID: runID, OwnerID: "owner", FencingToken: 1}
	if _, err := queue.NewPostgresStore(queries).ClaimSteer(ctx, string(item.ID), run); err != nil {
		t.Fatal(err)
	}
	if err := queries.InTx(ctx, func(txq dbstore.Queries) error {
		return txq.RejectSteerItemsForRun(ctx, mustUUID(t, runID))
	}); err != nil {
		t.Fatal(err)
	}
	var status string
	var claimRunID pgtype.UUID
	var claimOwnerID pgtype.Text
	var claimFencingToken pgtype.Int8
	if err := pool.QueryRow(ctx, `SELECT status, claim_run_id, claim_owner_id, claim_fencing_token FROM session_steer_queue WHERE item_id=$1`, mustUUID(t, string(item.ID))).Scan(&status, &claimRunID, &claimOwnerID, &claimFencingToken); err != nil {
		t.Fatal(err)
	}
	if status != string(queue.Rejected) || claimRunID.Valid || claimOwnerID.Valid || claimFencingToken.Valid {
		t.Fatalf("rejected claim columns = status %s run %v owner %v fence %v", status, claimRunID, claimOwnerID, claimFencingToken)
	}
}

func TestPostgresFinalHandoffIsAtomicAndReplayable(t *testing.T) {
	ctx := context.Background()
	pool := openQueuePostgres(t, ctx)
	botID, sessionID, runID := createQueueFixture(t, ctx, pool)
	queries := postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))
	enqueueSteer := func() queue.SteerItem {
		t.Helper()
		var item queue.SteerItem
		if err := queries.InTx(ctx, func(txq dbstore.Queries) error {
			if _, err := txq.LockSessionForQueueAdmission(ctx, dbsqlc.LockSessionForQueueAdmissionParams{BotID: mustUUID(t, botID), SessionID: mustUUID(t, sessionID)}); err != nil {
				return err
			}
			var err error
			item, err = queue.NewPostgresStore(txq).EnqueueSteer(ctx, botID, sessionID, uuid.NewString(), uuid.NewString(), []byte(`{"text":"steer first"}`))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return item
	}
	enqueueFollowUp := func(text string) queue.FollowUpItem {
		t.Helper()
		var item queue.FollowUpItem
		if err := queries.InTx(ctx, func(txq dbstore.Queries) error {
			if _, err := txq.LockSessionForQueueAdmission(ctx, dbsqlc.LockSessionForQueueAdmissionParams{BotID: mustUUID(t, botID), SessionID: mustUUID(t, sessionID)}); err != nil {
				return err
			}
			var err error
			item, err = queue.NewPostgresStore(txq).EnqueueFollowUp(ctx, botID, sessionID, uuid.NewString(), uuid.NewString(), []byte(`{"text":"`+text+`"}`))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return item
	}
	steer := enqueueSteer()
	firstFollow, secondFollow := enqueueFollowUp("first"), enqueueFollowUp("second")
	run := sessionruntime.RunHandle{BotID: botID, SessionID: sessionID, RunID: runID, OwnerID: "owner", FencingToken: 1}
	coordinator := queue.NewPostgresCoordinator(queries, queries.InTx)
	firstResult, err := coordinator.CommitStep(ctx, queue.CommitStepRequest{
		Run:  run,
		Kind: queue.StepFinal,
		Persist: func(ctx context.Context, txq dbstore.Queries) error {
			_, err := txq.UpdateSessionTitle(ctx, dbsqlc.UpdateSessionTitleParams{ID: mustUUID(t, sessionID), Title: "history-step-1"})
			return err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if firstResult.Action != queue.ContinueWithSteer || firstResult.Steer == nil || firstResult.Steer.ID != steer.ID || firstResult.SteerClaim == nil {
		t.Fatalf("steer-priority result = %#v", firstResult)
	}
	var runState string
	if err := pool.QueryRow(ctx, "SELECT state FROM session_runs WHERE run_id=$1", runID).Scan(&runState); err != nil {
		t.Fatal(err)
	}
	if runState != "running" {
		t.Fatalf("run state after steer handoff = %s", runState)
	}

	badRef := *firstResult.SteerClaim
	badRef.OwnerID = "stale-owner"
	_, err = coordinator.CommitStep(ctx, queue.CommitStepRequest{
		Run: run, StepIndex: 1, Kind: queue.StepFinal, Steer: &badRef,
		Persist: func(ctx context.Context, txq dbstore.Queries) error {
			_, err := txq.UpdateSessionTitle(ctx, dbsqlc.UpdateSessionTitleParams{ID: mustUUID(t, sessionID), Title: "must-rollback"})
			return err
		},
	})
	if !errors.Is(err, queue.ErrInvalidReference) {
		t.Fatalf("bad claim commit = %v, want ErrInvalidReference", err)
	}
	var title, steerStatus string
	if err := pool.QueryRow(ctx, "SELECT title FROM bot_sessions WHERE id=$1", sessionID).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT status FROM session_steer_queue WHERE item_id=$1", steer.ID).Scan(&steerStatus); err != nil {
		t.Fatal(err)
	}
	if title != "history-step-1" || steerStatus != string(queue.Claimed) {
		t.Fatalf("rollback title/status = %q/%s", title, steerStatus)
	}

	finalResult, err := coordinator.CommitStep(ctx, queue.CommitStepRequest{
		Run: run, StepIndex: 1, Kind: queue.StepFinal, Steer: firstResult.SteerClaim,
		Persist: func(ctx context.Context, txq dbstore.Queries) error {
			_, err := txq.UpdateSessionTitle(ctx, dbsqlc.UpdateSessionTitleParams{ID: mustUUID(t, sessionID), Title: "history-final"})
			return err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if finalResult.Action != queue.StartContinuation || finalResult.FollowUp == nil || finalResult.FollowUp.ID != firstFollow.ID || finalResult.ContinuationRunID == "" {
		t.Fatalf("final handoff result = %#v", finalResult)
	}
	continuationID := finalResult.ContinuationRunID
	var sourceID pgtype.UUID
	var continuationState string
	var continuationOwner pgtype.Text
	if err := pool.QueryRow(ctx, "SELECT state, owner_id, source_follow_up_item_id FROM session_runs WHERE run_id=$1", continuationID).Scan(&continuationState, &continuationOwner, &sourceID); err != nil {
		t.Fatal(err)
	}
	if continuationState != "accepted" || continuationOwner.Valid || sourceID.String() != string(firstFollow.ID) {
		t.Fatalf("continuation state/owner/source = %s/%v/%s", continuationState, continuationOwner, sourceID.String())
	}
	if err := pool.QueryRow(ctx, "SELECT state FROM session_runs WHERE run_id=$1", runID).Scan(&runState); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT title FROM bot_sessions WHERE id=$1", sessionID).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT status FROM session_steer_queue WHERE item_id=$1", steer.ID).Scan(&steerStatus); err != nil {
		t.Fatal(err)
	}
	if runState != "completed" || title != "history-final" || steerStatus != string(queue.Applied) {
		t.Fatalf("committed state/title/steer = %s/%q/%s", runState, title, steerStatus)
	}
	pending, err := queue.NewPostgresStore(queries).PendingFollowUp(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != secondFollow.ID {
		t.Fatalf("follow-up pending after handoff = %#v", pending)
	}

	persistCalled := false
	replayed, err := coordinator.CommitStep(ctx, queue.CommitStepRequest{
		Run: run, StepIndex: 1, Kind: queue.StepFinal,
		Persist: func(context.Context, dbstore.Queries) error {
			persistCalled = true
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if persistCalled || replayed.Action != queue.StartContinuation || replayed.ContinuationRunID != continuationID || replayed.FollowUp == nil || replayed.FollowUp.ID != firstFollow.ID {
		t.Fatalf("replayed handoff = %#v, persistCalled=%v", replayed, persistCalled)
	}
	ownerless, err := queue.NewPostgresStore(queries).ListOwnerlessContinuations(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ownerless) != 1 || ownerless[0].RunID != continuationID {
		t.Fatalf("ownerless continuations = %#v", ownerless)
	}
	orphans, err := dbsqlc.New(pool).ListOrphanedSessionRuns(ctx, dbsqlc.ListOrphanedSessionRunsParams{GraceSeconds: -1, BatchSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, orphan := range orphans {
		if orphan.RunID.String() == continuationID {
			t.Fatalf("continuation %s leaked into generic orphan scan", continuationID)
		}
	}
	type acquireResult struct {
		handle sessionruntime.RunHandle
		won    bool
		err    error
	}
	acquired := make(chan acquireResult, 2)
	for _, owner := range []string{"recovery-a", "recovery-b"} {
		owner := owner
		go func() {
			handle, won, err := queue.NewPostgresStore(queries).AcquireContinuationRun(ctx, continuationID, owner, "generation-1")
			acquired <- acquireResult{handle: handle, won: won, err: err}
		}()
	}
	var winner sessionruntime.RunHandle
	wins := 0
	for range 2 {
		result := <-acquired
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.won {
			wins++
			winner = result.handle
		}
	}
	if wins != 1 || winner.RunID != continuationID || winner.FencingToken <= 0 {
		t.Fatalf("continuation acquisition wins/handle = %d/%#v", wins, winner)
	}
	claimedFollow, err := queue.NewPostgresStore(queries).ClaimAssignedFollowUp(ctx, string(firstFollow.ID), winner)
	if err != nil {
		t.Fatal(err)
	}
	followRef := queue.FollowUpClaimRef{ItemID: claimedFollow.ID, RunID: winner.RunID, OwnerID: winner.OwnerID, FencingToken: winner.FencingToken}
	if err := queue.NewPostgresStore(queries).ApplyFollowUp(ctx, followRef); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO session_runs(run_id,bot_id,session_id,invocation_id,turn_id,turn_position,state,input_json,input_fingerprint) VALUES($1,$2,$3,$4,$5,99,'accepted','{}',$6)`, uuid.New(), botID, sessionID, uuid.NewString(), uuid.New(), "busy"); !dbpkg.IsUniqueViolation(err) {
		t.Fatalf("ordinary admission while continuation owns session = %v, want active-run unique violation", err)
	}

	lostBotID, lostSessionID, lostRunID := createQueueFixture(t, ctx, pool)
	var lostSource queue.FollowUpItem
	if err := queries.InTx(ctx, func(txq dbstore.Queries) error {
		if _, err := txq.LockSessionForQueueAdmission(ctx, dbsqlc.LockSessionForQueueAdmissionParams{BotID: mustUUID(t, lostBotID), SessionID: mustUUID(t, lostSessionID)}); err != nil {
			return err
		}
		var err error
		lostSource, err = queue.NewPostgresStore(txq).EnqueueFollowUp(ctx, lostBotID, lostSessionID, uuid.NewString(), uuid.NewString(), []byte(`{"text":"cannot recover"}`))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	lostResult, err := coordinator.CommitStep(ctx, queue.CommitStepRequest{
		Run:  sessionruntime.RunHandle{BotID: lostBotID, SessionID: lostSessionID, RunID: lostRunID, OwnerID: "owner", FencingToken: 1},
		Kind: queue.StepFinal,
	})
	if err != nil || lostResult.Action != queue.StartContinuation {
		t.Fatalf("lost continuation handoff = %#v, %v", lostResult, err)
	}
	if err := coordinator.RejectLostContinuation(ctx, lostResult.ContinuationRunID, 0); err != nil {
		t.Fatal(err)
	}
	var lostState, lostSourceStatus string
	if err := pool.QueryRow(ctx, "SELECT state FROM session_runs WHERE run_id=$1", lostResult.ContinuationRunID).Scan(&lostState); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT status FROM session_follow_up_queue WHERE item_id=$1", lostSource.ID).Scan(&lostSourceStatus); err != nil {
		t.Fatal(err)
	}
	if lostState != "lost" || lostSourceStatus != string(queue.Rejected) {
		t.Fatalf("lost continuation/source states = %s/%s", lostState, lostSourceStatus)
	}
}

func TestPostgresContinuationWaitsForParentRuntimeAndAppliesFollowUp(t *testing.T) {
	ctx := context.Background()
	pool := openQueuePostgres(t, ctx)
	botID, sessionID, parentRunID := createQueueFixture(t, ctx, pool)
	queries := postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))
	store := queue.NewPostgresStore(queries)
	coordinator := queue.NewPostgresCoordinator(queries, queries.InTx)

	var source queue.FollowUpItem
	if err := queries.InTx(ctx, func(txq dbstore.Queries) error {
		if _, err := txq.LockSessionForQueueAdmission(ctx, dbsqlc.LockSessionForQueueAdmissionParams{
			BotID: mustUUID(t, botID), SessionID: mustUUID(t, sessionID),
		}); err != nil {
			return err
		}
		var err error
		source, err = queue.NewPostgresStore(txq).EnqueueFollowUp(
			ctx, botID, sessionID, uuid.NewString(), uuid.NewString(), []byte(`{"text":"continue after parent"}`),
		)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{OwnerID: "owner"})
	t.Cleanup(func() { _ = manager.Close() })
	parentRuntime, err := manager.StartRunHandle(
		ctx, botID, sessionID, parentRunID, make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1),
	)
	if err != nil {
		t.Fatal(err)
	}

	handoff, err := coordinator.CommitStep(ctx, queue.CommitStepRequest{
		Run: sessionruntime.RunHandle{
			BotID: botID, SessionID: sessionID, RunID: parentRunID,
			OwnerID: "owner", FencingToken: 1,
		},
		StepIndex:  0,
		CommitHash: "parent-final",
		Kind:       queue.StepFinal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if handoff.Action != queue.StartContinuation || handoff.FollowUp == nil || handoff.FollowUp.ID != source.ID {
		t.Fatalf("parent handoff = %#v", handoff)
	}

	type continuationStart struct {
		handle  sessionruntime.RunHandle
		claimed queue.FollowUpItem
		err     error
	}
	started := make(chan continuationStart, 1)
	go func() {
		if err := manager.WaitRunRelease(ctx, botID, sessionID, parentRunID); err != nil {
			started <- continuationStart{err: err}
			return
		}
		liveGeneration, err := manager.LivenessGeneration(ctx)
		if err != nil {
			started <- continuationStart{err: err}
			return
		}
		handle, won, err := store.AcquireContinuationRun(ctx, handoff.ContinuationRunID, manager.OwnerID(), liveGeneration)
		if err != nil || !won {
			if err == nil {
				err = errors.New("continuation ownership was not acquired")
			}
			started <- continuationStart{err: err}
			return
		}
		runCtx, cancelCause := context.WithCancelCause(ctx)
		localHandle, err := manager.StartExistingRun(
			runCtx, handle,
			func(context.Context, sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error) {
				return sessionruntime.RunAdmissionView{}, nil
			},
			cancelCause, make(chan struct{}, 1), func() { cancelCause(context.Canceled) }, make(chan turn.InjectMessage, 1),
		)
		if err != nil {
			started <- continuationStart{err: err}
			return
		}
		claimed, err := store.ClaimAssignedFollowUp(ctx, string(source.ID), localHandle)
		started <- continuationStart{handle: localHandle, claimed: claimed, err: err}
	}()

	select {
	case result := <-started:
		t.Fatalf("continuation started before parent runtime release: %#v", result)
	case <-time.After(25 * time.Millisecond):
	}
	if err := manager.FinishRun(ctx, parentRuntime, sessionruntime.RunStatusCompleted, ""); err != nil {
		t.Fatal(err)
	}

	var continuation continuationStart
	select {
	case continuation = <-started:
		if continuation.err != nil {
			t.Fatal(continuation.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("continuation did not start after parent runtime release")
	}
	if continuation.claimed.Claim == nil || continuation.claimed.Status != queue.Claimed {
		t.Fatalf("continuation follow-up claim = %#v", continuation.claimed)
	}
	var durableLiveGeneration string
	if err := pool.QueryRow(ctx, "SELECT live_generation FROM session_runs WHERE run_id=$1", handoff.ContinuationRunID).Scan(&durableLiveGeneration); err != nil {
		t.Fatal(err)
	}
	expectedLiveGeneration, err := manager.LivenessGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if durableLiveGeneration != expectedLiveGeneration {
		t.Fatalf("continuation live generation = %q, want backend generation %q", durableLiveGeneration, expectedLiveGeneration)
	}

	result, err := coordinator.CommitStep(ctx, queue.CommitStepRequest{
		Run:        continuation.handle,
		StepIndex:  0,
		CommitHash: "continuation-final",
		Kind:       queue.StepFinal,
		FollowUp:   continuation.claimed.Claim,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != queue.StopCurrent {
		t.Fatalf("continuation final action = %s, want %s", result.Action, queue.StopCurrent)
	}
	stored, err := store.FollowUpByID(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != queue.Applied {
		t.Fatalf("follow-up status = %s, want %s", stored.Status, queue.Applied)
	}
	var continuationState string
	if err := pool.QueryRow(ctx, "SELECT state FROM session_runs WHERE run_id=$1", handoff.ContinuationRunID).Scan(&continuationState); err != nil {
		t.Fatal(err)
	}
	if continuationState != "completed" {
		t.Fatalf("continuation run state = %s, want completed", continuationState)
	}
	if err := manager.FinishRun(ctx, continuation.handle, sessionruntime.RunStatusCompleted, ""); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresMultipleFollowUpsChainInFIFOOrder(t *testing.T) {
	ctx := context.Background()
	pool := openQueuePostgres(t, ctx)
	botID, sessionID, parentRunID := createQueueFixture(t, ctx, pool)
	queries := postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))
	store := queue.NewPostgresStore(queries)
	coordinator := queue.NewPostgresCoordinator(queries, queries.InTx)

	items := make([]queue.FollowUpItem, 0, 3)
	for _, text := range []string{"first", "second", "third"} {
		var item queue.FollowUpItem
		if err := queries.InTx(ctx, func(txq dbstore.Queries) error {
			if _, err := txq.LockSessionForQueueAdmission(ctx, dbsqlc.LockSessionForQueueAdmissionParams{
				BotID: mustUUID(t, botID), SessionID: mustUUID(t, sessionID),
			}); err != nil {
				return err
			}
			var err error
			item, err = queue.NewPostgresStore(txq).EnqueueFollowUp(
				ctx, botID, sessionID, uuid.NewString(), uuid.NewString(), []byte(`{"text":"`+text+`"}`),
			)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		items = append(items, item)
	}

	current := sessionruntime.RunHandle{
		BotID: botID, SessionID: sessionID, RunID: parentRunID,
		OwnerID: "owner", FencingToken: 1,
	}
	var claim *queue.FollowUpClaimRef
	for index, want := range items {
		result, err := coordinator.CommitStep(ctx, queue.CommitStepRequest{
			Run: current, StepIndex: 0, CommitHash: uuid.NewString(), Kind: queue.StepFinal, FollowUp: claim,
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.Action != queue.StartContinuation || result.FollowUp == nil || result.FollowUp.ID != want.ID {
			t.Fatalf("handoff %d = %#v, want follow-up %s", index, result, want.ID)
		}
		if err := coordinator.ReconcileTerminalRun(ctx, sessionruntime.TerminalRun{
			RunID: current.RunID, BotID: botID, SessionID: sessionID,
			FencingToken: current.FencingToken, State: "completed",
		}); err != nil {
			t.Fatal(err)
		}
		pending, err := store.PendingFollowUp(ctx, sessionID)
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) != len(items)-index-1 {
			t.Fatalf("pending after handoff %d = %#v", index, pending)
		}

		next, won, err := store.AcquireContinuationRun(
			ctx, result.ContinuationRunID, "owner", uuid.NewString(),
		)
		if err != nil || !won {
			t.Fatalf("acquire continuation %d = %#v, won=%v, err=%v", index, next, won, err)
		}
		claimed, err := store.ClaimAssignedFollowUp(ctx, string(want.ID), next)
		if err != nil {
			t.Fatal(err)
		}
		current = next
		claim = claimed.Claim
	}

	result, err := coordinator.CommitStep(ctx, queue.CommitStepRequest{
		Run: current, StepIndex: 0, CommitHash: uuid.NewString(), Kind: queue.StepFinal, FollowUp: claim,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != queue.StopCurrent {
		t.Fatalf("last continuation action = %s, want %s", result.Action, queue.StopCurrent)
	}
	for _, item := range items {
		stored, err := store.FollowUpByID(ctx, item.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Status != queue.Applied {
			t.Fatalf("follow-up %s status = %s, want applied", item.ID, stored.Status)
		}
	}
}

func TestPostgresLostContinuationRejectsItsSourceAndRemainingFollowUps(t *testing.T) {
	ctx := context.Background()
	pool := openQueuePostgres(t, ctx)
	botID, sessionID, parentRunID := createQueueFixture(t, ctx, pool)
	queries := postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))
	store := queue.NewPostgresStore(queries)
	coordinator := queue.NewPostgresCoordinator(queries, queries.InTx)

	items := make([]queue.FollowUpItem, 0, 2)
	for _, text := range []string{"source", "remaining"} {
		var item queue.FollowUpItem
		if err := queries.InTx(ctx, func(txq dbstore.Queries) error {
			if _, err := txq.LockSessionForQueueAdmission(ctx, dbsqlc.LockSessionForQueueAdmissionParams{
				BotID: mustUUID(t, botID), SessionID: mustUUID(t, sessionID),
			}); err != nil {
				return err
			}
			var err error
			item, err = queue.NewPostgresStore(txq).EnqueueFollowUp(
				ctx, botID, sessionID, uuid.NewString(), uuid.NewString(), []byte(`{"text":"`+text+`"}`),
			)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		items = append(items, item)
	}

	handoff, err := coordinator.CommitStep(ctx, queue.CommitStepRequest{
		Run: sessionruntime.RunHandle{
			BotID: botID, SessionID: sessionID, RunID: parentRunID,
			OwnerID: "owner", FencingToken: 1,
		},
		StepIndex: 0, CommitHash: uuid.NewString(), Kind: queue.StepFinal,
	})
	if err != nil {
		t.Fatal(err)
	}
	continuation, won, err := store.AcquireContinuationRun(ctx, handoff.ContinuationRunID, "owner", uuid.NewString())
	if err != nil || !won {
		t.Fatalf("acquire continuation = %#v, won=%v, err=%v", continuation, won, err)
	}
	if _, err := store.ClaimAssignedFollowUp(ctx, string(items[0].ID), continuation); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE session_runs SET state='lost' WHERE run_id=$1", continuation.RunID); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.ReconcileTerminalRun(ctx, sessionruntime.TerminalRun{
		RunID: continuation.RunID, BotID: botID, SessionID: sessionID,
		FencingToken: continuation.FencingToken, State: "lost",
	}); err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		stored, err := store.FollowUpByID(ctx, item.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Status != queue.Rejected {
			t.Fatalf("follow-up %s status = %s, want rejected", item.ID, stored.Status)
		}
	}
}

func openQueuePostgres(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if os.Getenv("MEMOH_TEST_POSTGRES_REQUIRED") == "1" {
			t.Fatal("queue PostgreSQL test required but TEST_POSTGRES_DSN is not set")
		}
		t.Skip("set TEST_POSTGRES_DSN to run queue PostgreSQL integration")
	}
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if os.Getenv("TEST_POSTGRES_BOOTSTRAP_SCHEMA") == "1" {
		queueMigration.Do(func() { queueMigrationErr = dbtest.MigratePostgresUp(dsn) })
		if queueMigrationErr != nil {
			t.Fatal(queueMigrationErr)
		}
	}
	return pool
}

func createQueueFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (string, string, string) {
	t.Helper()
	userID, botID, sessionID, runID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	name := "queue-test-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `WITH u AS (INSERT INTO users(id,username,is_active) VALUES($1,$2,true) RETURNING id) INSERT INTO team_members(user_id,role) SELECT id,'admin' FROM u`, userID, name); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO bots(id,owner_user_id,name) VALUES($1,$2,$3)`, botID, userID, name); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO bot_sessions(id,bot_id,channel_type,runtime_type) VALUES($1,$2,'local','model')`, sessionID, botID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO session_runs(run_id,bot_id,session_id,invocation_id,turn_id,turn_position,state,input_json,input_fingerprint,owner_id,owner_since,fencing_token) VALUES($1,$2,$3,$4,$5,1,'running','{}',$6,'owner',now(),1)`, runID, botID, sessionID, uuid.NewString(), uuid.New(), "input"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM bots WHERE id=$1", botID)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=$1", userID)
	})
	return botID.String(), sessionID.String(), runID.String()
}

func mustUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	parsed, err := dbpkg.ParseUUID(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
