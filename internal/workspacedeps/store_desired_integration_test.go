//go:build integration

package workspacedeps

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
	"github.com/jackc/pgx/v5"
)

func seedDesiredInstallation(t *testing.T, ctx context.Context, store *postgresStore, key InstallationKey, id, version string) DesiredInstallation {
	t.Helper()
	_, err := store.ClaimOperation(ctx, UpsertInstallation{InstallationKey: key, Source: InstallationSourceManaged, Status: StatusInstalling}, id)
	if err != nil {
		t.Fatal(err)
	}
	target := DesiredInstallation{InstallationKey: key, Version: version, SourceURL: "https://registry.example", RegistryID: "memoh", DefinitionRevision: strings.Repeat("a", 64), ManifestDigest: "sha256:" + strings.Repeat("b", 64), AuthorizedByActor: "user:original", Platform: Platform{OS: "linux", Arch: "amd64", Libc: "glibc"}, InstallationID: id, PayloadPath: "/var/lib/memoh/deps/codex/installs/" + id, StoreRoot: "/var/lib/memoh/deps", Entrypoints: map[string]string{"codex": "/data/.memoh/deps/codex/current/bin/codex"}}
	rec, err := store.FinishAuthorized(ctx, target, id)
	if err != nil {
		t.Fatal(err)
	}
	if rec.LastOperationID != id || rec.OperationID != "" {
		t.Fatalf("terminal lost operation identity: %+v", rec)
	}
	target, err = store.GetDesired(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func TestDesiredAuthorizationSurvivesObservationAndConcurrentQueue(t *testing.T) {
	ctx := t.Context()
	pool := openDependencyPostgres(t, ctx)
	store := NewPostgresStore(postgresstore.NewQueries(dbsqlc.New(pool))).(*postgresStore)
	key := InstallationKey{BotID: createDependencyBot(t, ctx, pool), DependencyID: "codex"}
	target := seedDesiredInstallation(t, ctx, store, key, strings.Repeat("1", 32), "0.154.0")
	drift := "99.0.0"
	source := "https://untrusted.example"
	revision := strings.Repeat("c", 64)
	if _, err := store.UpdateObserved(ctx, key, ObservedUpdate{InstalledVersion: &drift, SourceURL: &source, DefinitionRevision: &revision}); err != nil {
		t.Fatal(err)
	}
	kept, err := store.GetDesired(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if kept.Version != target.Version || kept.SourceURL != target.SourceURL || kept.DefinitionRevision != target.DefinitionRevision || kept.AuthorizedByActor != target.AuthorizedByActor {
		t.Fatalf("observation changed authority: %+v", kept)
	}
	const count = 12
	start := make(chan struct{})
	out := make(chan error, count)
	for i := range count {
		go func() {
			<-start
			_, err := store.QueueRepair(ctx, target, fmt.Sprintf("%032x", i+10), time.Now(), false)
			out <- err
		}()
	}
	close(start)
	success := 0
	for range count {
		err := <-out
		if err == nil {
			success++
		} else if !errors.Is(err, ErrDesiredTargetChanged) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("queue winners=%d", success)
	}
	queued, err := store.GetDesired(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if queued.RepairStatus != RepairQueued || !validReceiptID(queued.RepairOperationID) {
		t.Fatalf("queue not durable: %+v", queued)
	}
	// New store instance reads the same persisted request after Server restart.
	restarted := NewPostgresStore(postgresstore.NewQueries(dbsqlc.New(pool))).(*postgresStore)
	restored, err := restarted.GetDesired(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if restored.RepairOperationID != queued.RepairOperationID {
		t.Fatal("restart lost queue identity")
	}
}

func TestDesiredPreparationFailureCannotClearPeerServerRepairClaim(t *testing.T) {
	ctx := t.Context()
	pool := openDependencyPostgres(t, ctx)
	store := NewPostgresStore(postgresstore.NewQueries(dbsqlc.New(pool))).(*postgresStore)
	peer := NewPostgresStore(postgresstore.NewQueries(dbsqlc.New(pool))).(*postgresStore)
	for _, cause := range []error{ErrDefinitionUnavailable, errors.New("filesystem observation disconnected")} {
		t.Run(cause.Error(), func(t *testing.T) {
			key := InstallationKey{BotID: createDependencyBot(t, ctx, pool), DependencyID: "codex"}
			target := seedDesiredInstallation(t, ctx, store, key, strings.Repeat("1", 32), "0.154.0")
			queued, err := store.QueueRepair(ctx, target, strings.Repeat("2", 32), time.Now(), false)
			if err != nil {
				t.Fatal(err)
			}
			observed := make(chan struct{})
			resume := make(chan struct{})
			result := make(chan error, 1)
			go func() {
				close(observed)
				<-resume
				svc := &Service{store: store, now: time.Now}
				result <- svc.recordRepairPreparationFailure(ctx, queued, cause)
			}()
			<-observed
			_, claimErr := peer.ClaimRepair(ctx, queued, nil)
			close(resume)
			if claimErr != nil {
				t.Fatal(claimErr)
			}
			if err := <-result; !errors.Is(err, ErrDesiredTargetChanged) {
				t.Fatalf("stale observation overwrote peer claim: %v", err)
			}
			active, err := peer.GetDesired(ctx, key)
			if err != nil || active.RepairStatus != RepairInstalling || active.RepairOperationID != queued.RepairOperationID || active.RepairAttempts != 1 || active.RepairLastErrorCode != "" {
				t.Fatalf("peer execution lost its durable claim: %+v %v", active, err)
			}
			if _, err := peer.FinishRepair(ctx, active, active.RepairOperationID); err != nil {
				t.Fatalf("peer could not complete its owned repair: %v", err)
			}
		})
	}
}

func TestDesiredOperationIntentIsAtomicAndSurvivesServerRestart(t *testing.T) {
	ctx := t.Context()
	pool := openDependencyPostgres(t, ctx)
	store := NewPostgresStore(postgresstore.NewQueries(dbsqlc.New(pool))).(*postgresStore)
	peer := NewPostgresStore(postgresstore.NewQueries(dbsqlc.New(pool))).(*postgresStore)
	key := InstallationKey{BotID: createDependencyBot(t, ctx, pool), DependencyID: "codex"}
	target := seedDesiredInstallation(t, ctx, store, key, strings.Repeat("1", 32), "0.154.0")
	operationID := strings.Repeat("2", 32)
	intent := &OperationReceipt{ID: operationID, DependencyID: key.DependencyID, Action: catalog.ActionUpdate, RequestedVersion: "0.155.0", SourceURL: target.SourceURL, RegistryID: target.RegistryID, DefinitionRevision: target.DefinitionRevision, ManifestDigest: target.ManifestDigest, AuthorizedByActor: "manager:alice", StoreRoot: target.StoreRoot, PayloadPath: "/var/lib/memoh/deps/codex/installs/" + operationID}
	expected := *intent
	claimed, err := store.ClaimOperation(ctx, UpsertInstallation{InstallationKey: key, Source: InstallationSourceManaged, Status: StatusUpdating, OperationIntent: intent}, operationID)
	if err != nil || !reflect.DeepEqual(claimed.OperationIntent, &expected) {
		t.Fatalf("claim did not atomically retain approved intent: %+v %v", claimed, err)
	}
	intent.RequestedVersion = "99.0.0"
	intent.AuthorizedByActor = "workspace:forged"
	if _, err := peer.ClaimOperation(ctx, UpsertInstallation{InstallationKey: key, Source: InstallationSourceManaged, Status: StatusInstalling, OperationIntent: intent}, strings.Repeat("3", 32)); !errors.Is(err, ErrBusy) {
		t.Fatalf("second server replaced active intent: %v", err)
	}
	read, err := peer.Get(ctx, key)
	if err != nil || !reflect.DeepEqual(read.OperationIntent, &expected) {
		t.Fatalf("restart lost the immutable approval: %+v %v", read.OperationIntent, err)
	}
	read.Status = StatusFailed
	finished, err := peer.FinishOperation(ctx, key, operationID, &read)
	if err != nil || finished.OperationIntent != nil {
		t.Fatalf("terminal operation retained execution intent: %+v %v", finished, err)
	}
	target, err = peer.GetDesired(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := peer.QueueRepair(ctx, target, strings.Repeat("4", 32), time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	repairIntent := expected
	repairIntent.ID, repairIntent.Action = queued.RepairOperationID, catalog.ActionReinstall
	repairIntent.Repair, repairIntent.DesiredRevision, repairIntent.RequestedVersion = true, queued.Revision, queued.Version
	claimed, err = peer.ClaimRepair(ctx, queued, &repairIntent)
	if err != nil || !reflect.DeepEqual(claimed.OperationIntent, &repairIntent) {
		t.Fatalf("repair claim lost its frozen authority: %+v %v", claimed.OperationIntent, err)
	}
	recovered, err := store.Get(ctx, key)
	if err != nil || !reflect.DeepEqual(recovered.OperationIntent, &repairIntent) {
		t.Fatalf("peer cannot recover claimed intent: %+v %v", recovered.OperationIntent, err)
	}
	finished, err = store.FinishRepair(ctx, queued, queued.RepairOperationID)
	if err != nil || finished.OperationIntent != nil {
		t.Fatalf("completed repair retained execution intent: %+v %v", finished, err)
	}
}

func TestDesiredRepairCannotResurrectRemovedOrUpdatedTarget(t *testing.T) {
	ctx := t.Context()
	pool := openDependencyPostgres(t, ctx)
	store := NewPostgresStore(postgresstore.NewQueries(dbsqlc.New(pool))).(*postgresStore)
	for _, remove := range []bool{false, true} {
		t.Run(fmt.Sprint(remove), func(t *testing.T) {
			key := InstallationKey{BotID: createDependencyBot(t, ctx, pool), DependencyID: "codex"}
			target := seedDesiredInstallation(t, ctx, store, key, strings.Repeat("1", 32), "0.154.0")
			queued, err := store.QueueRepair(ctx, target, strings.Repeat("2", 32), time.Now(), false)
			if err != nil {
				t.Fatal(err)
			}
			status := StatusUpdating
			if remove {
				status = StatusRemoving
			}
			managing := strings.Repeat("3", 32)
			if _, err := store.ClaimOperation(ctx, UpsertInstallation{InstallationKey: key, Source: InstallationSourceManaged, Status: status, AuthorizedByActor: "user:remover"}, managing); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ClaimRepair(ctx, queued, nil); !errors.Is(err, ErrBusy) {
				t.Fatalf("stale claim: %v", err)
			}
			if _, err := store.FinishRepair(ctx, queued, queued.RepairOperationID); !errors.Is(err, ErrBusy) {
				t.Fatalf("stale finish: %v", err)
			}
			if _, err := store.SetRepairResult(ctx, queued, RepairReady, 0, nil, ""); !errors.Is(err, ErrDesiredTargetChanged) {
				t.Fatalf("stale result: %v", err)
			}
			after, err := store.GetDesired(ctx, key)
			if remove {
				if !errors.Is(err, ErrInstallationNotFound) {
					t.Fatalf("remove admission retained authority: %+v %v", after, err)
				}
				var count int
				if err := pool.QueryRow(ctx, "SELECT count(*) FROM bot_dependency_authorization_events WHERE bot_id=$1 AND dependency_id=$2", key.BotID, key.DependencyID).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 2 {
					t.Fatalf("missing durable authorize/revoke audit: %d", count)
				}
				var actor string
				if err := pool.QueryRow(ctx, "SELECT actor FROM bot_dependency_authorization_events WHERE bot_id=$1 AND dependency_id=$2 AND action='revoke'", key.BotID, key.DependencyID).Scan(&actor); err != nil {
					t.Fatal(err)
				}
				if actor != "user:remover" {
					t.Fatalf("revoke lost actor audit: %q", actor)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if after.Revision == queued.Revision || after.Version != target.Version || after.AuthorizedByActor != target.AuthorizedByActor {
					t.Fatalf("management claim lost prior target or queue fence: %+v", after)
				}
			}
		})
	}
}

func TestDesiredRepairFinishPreservesAuthorizationAndFencesOwnership(t *testing.T) {
	ctx := t.Context()
	pool := openDependencyPostgres(t, ctx)
	store := NewPostgresStore(postgresstore.NewQueries(dbsqlc.New(pool))).(*postgresStore)
	key := InstallationKey{BotID: createDependencyBot(t, ctx, pool), DependencyID: "codex"}
	original := seedDesiredInstallation(t, ctx, store, key, strings.Repeat("1", 32), "0.154.0")
	target, err := store.QueueRepair(ctx, original, strings.Repeat("2", 32), time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimRepair(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	stale := target
	stale.Revision = strings.Repeat("3", 32)
	if _, err := store.FinishRepair(ctx, stale, target.RepairOperationID); !errors.Is(err, ErrBusy) {
		t.Fatalf("revision fence: %v", err)
	}
	rec, err := store.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if rec.OperationID != target.RepairOperationID || rec.Status != StatusInstalling {
		t.Fatal("rejected finish changed installation")
	}
	target.InstallationID = target.RepairOperationID
	target.PayloadPath = "/var/lib/memoh/deps/codex/installs/" + target.InstallationID
	if _, err := store.FinishRepair(ctx, target, target.RepairOperationID); err != nil {
		t.Fatal(err)
	}
	after, err := store.GetDesired(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != original.Revision || after.AuthorizedByActor != original.AuthorizedByActor || after.AuthorizedByOperationID != original.AuthorizedByOperationID || !after.AutoRepairAuthorizedAt.Equal(original.AutoRepairAuthorizedAt) || after.RepairStatus != RepairReady || after.PayloadPath != target.PayloadPath || after.RepairAttempts != 1 {
		t.Fatalf("repair changed authority or lost result: %+v", after)
	}
}

func TestDesiredQueueBackoffAndTeamBoundary(t *testing.T) {
	ctx := t.Context()
	pool := openDependencyPostgres(t, ctx)
	store := NewPostgresStore(postgresstore.NewQueries(dbsqlc.New(pool))).(*postgresStore)
	key := InstallationKey{BotID: createDependencyBot(t, ctx, pool), DependencyID: "codex"}
	target := seedDesiredInstallation(t, ctx, store, key, strings.Repeat("1", 32), "0.154.0")
	now := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	queued, err := store.QueueRepair(ctx, target, strings.Repeat("2", 32), now, false)
	if err != nil {
		t.Fatal(err)
	}
	next := now.Add(time.Minute)
	target, err = store.SetRepairResult(ctx, queued, RepairBackoff, 1, &next, "workspace_dependency.repair_failed")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueRepair(ctx, target, strings.Repeat("3", 32), now, false); !errors.Is(err, ErrDesiredTargetChanged) {
		t.Fatalf("early retry: %v", err)
	}
	if _, err := store.QueueRepair(ctx, target, strings.Repeat("3", 32), next, false); err != nil {
		t.Fatalf("due retry: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	role := pgx.Identifier{"desired_scope_" + strings.ReplaceAll(key.BotID, "-", "")[:8]}.Sanitize()
	for _, sql := range []string{"CREATE ROLE " + role + " NOLOGIN", "GRANT USAGE ON SCHEMA public TO " + role, "GRANT SELECT,UPDATE ON bot_dependency_desired_installations TO " + role, "SET LOCAL ROLE " + role, "SELECT set_config('memoh.team_id','ffffffff-ffff-4fff-8fff-ffffffffffff',true)"} {
		if _, err := tx.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	scoped := NewPostgresStore(postgresstore.NewQueries(dbsqlc.New(tx))).(*postgresStore)
	if _, err := scoped.GetDesired(ctx, key); !errors.Is(err, ErrInstallationNotFound) {
		t.Fatalf("cross-team read: %v", err)
	}
	if _, err := scoped.QueueRepair(ctx, target, strings.Repeat("4", 32), next, true); !errors.Is(err, ErrDesiredTargetChanged) {
		t.Fatalf("cross-team queue: %v", err)
	}
}

// A second worker may finish its healthy-payload probe after the first worker
// claimed the same queued operation. Its stale observation cannot cancel the
// active owner's publication by clearing the shared operation identity.
func TestDesiredStaleQueuedProbeCannotClearInstallingOwner(t *testing.T) {
	ctx := t.Context()
	pool := openDependencyPostgres(t, ctx)
	store := NewPostgresStore(postgresstore.NewQueries(dbsqlc.New(pool))).(*postgresStore)
	key := InstallationKey{BotID: createDependencyBot(t, ctx, pool), DependencyID: "codex"}
	target := seedDesiredInstallation(t, ctx, store, key, strings.Repeat("1", 32), "0.154.0")
	queued, err := store.QueueRepair(ctx, target, strings.Repeat("2", 32), time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimRepair(ctx, queued, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetRepairResult(ctx, queued, RepairReady, 0, nil, ""); !errors.Is(err, ErrDesiredTargetChanged) {
		t.Fatalf("stale queued result cleared current owner: %v", err)
	}
	installing, err := store.GetDesired(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if installing.RepairStatus != RepairInstalling || installing.RepairOperationID != queued.RepairOperationID {
		t.Fatalf("active repair mutated: %+v", installing)
	}
	if _, err := store.FinishRepair(ctx, installing, installing.RepairOperationID); err != nil {
		t.Fatalf("active owner lost its commit: %v", err)
	}
}

func TestDesiredCompetingManagementAndRepairHaveOneOwner(t *testing.T) {
	ctx := t.Context()
	pool := openDependencyPostgres(t, ctx)
	store := NewPostgresStore(postgresstore.NewQueries(dbsqlc.New(pool))).(*postgresStore)
	for attempt := range 16 {
		key := InstallationKey{BotID: createDependencyBot(t, ctx, pool), DependencyID: "codex"}
		target := seedDesiredInstallation(t, ctx, store, key, strings.Repeat("1", 32), "0.154.0")
		queued, err := store.QueueRepair(ctx, target, strings.Repeat("2", 32), time.Now(), false)
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		type outcome struct {
			repair bool
			row    Installation
			err    error
		}
		outcomes := make(chan outcome, 2)
		go func() { <-start; row, err := store.ClaimRepair(ctx, queued, nil); outcomes <- outcome{true, row, err} }()
		go func() {
			<-start
			row, err := store.ClaimOperation(ctx, UpsertInstallation{InstallationKey: key, Source: InstallationSourceManaged, Status: StatusUpdating}, strings.Repeat("3", 32))
			outcomes <- outcome{false, row, err}
		}()
		close(start)
		wins := 0
		for range 2 {
			result := <-outcomes
			if result.err == nil {
				wins++
			} else if !errors.Is(result.err, ErrBusy) {
				t.Fatalf("attempt %d repair=%v returned non-conflict error: %v", attempt, result.repair, result.err)
			}
		}
		if wins != 1 {
			t.Fatalf("attempt %d had %d owners", attempt, wins)
		}
		row, err := store.Get(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		desired, err := store.GetDesired(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		switch row.OperationID {
		case queued.RepairOperationID:
			if desired.RepairStatus != RepairInstalling || desired.RepairOperationID != queued.RepairOperationID {
				t.Fatalf("repair claim and desired diverged: %+v %+v", row, desired)
			}
		case strings.Repeat("3", 32):
			if desired.Revision == queued.Revision || desired.RepairOperationID != "" {
				t.Fatalf("management owner retained stale queue: %+v", desired)
			}
		default:
			t.Fatalf("unknown operation owner: %+v", row)
		}
	}
}
