//go:build integration

package workspacedeps

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
)

func TestPostgresOperationClaimsFenceCompetingServersAndStaleReceipts(t *testing.T) {
	ctx := t.Context()
	pool := openDependencyPostgres(t, ctx)
	store := NewPostgresStore(postgresstore.NewQueries(dbsqlc.New(pool))).(*postgresStore)
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing=%v", existing), func(t *testing.T) {
			key := InstallationKey{BotID: createDependencyBot(t, ctx, pool), WorkspaceTargetID: "native", DependencyID: "codex"}
			if existing {
				_, err := store.Upsert(ctx, UpsertInstallation{InstallationKey: key, Source: InstallationSourceImage, Status: StatusInstalled, InstalledVersion: "old-version", ManifestDigest: "sha256:old", SourceURL: "https://old.example", RegistryID: "memoh", DefinitionRevision: "old-revision"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.SetStatus(ctx, key, StatusFailed, "previous failure"); err != nil {
					t.Fatal(err)
				}
			}
			const contenders = 12
			start := make(chan struct{})
			type attempt struct {
				token string
				row   Installation
				err   error
			}
			results := make(chan attempt, contenders)
			for i := range contenders {
				go func() {
					token := fmt.Sprintf("%032x", i+1)
					<-start
					row, err := store.ClaimOperation(ctx, UpsertInstallation{InstallationKey: key, Source: InstallationSourceManaged, Status: StatusInstalling, InstalledVersion: "new-intent", ManifestDigest: "sha256:new", SourceURL: "https://new.example", RegistryID: "memoh", DefinitionRevision: "new-revision"}, token)
					results <- attempt{token: token, row: row, err: err}
				}()
			}
			close(start)
			var winner attempt
			winners := 0
			for range contenders {
				result := <-results
				if result.err == nil {
					winners++
					winner = result
				} else if !errors.Is(result.err, ErrBusy) {
					t.Fatal(result.err)
				}
			}
			if winners != 1 {
				t.Fatalf("concurrent claim winners: %d", winners)
			}
			claimed, err := store.Get(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			if claimed.OperationID != winner.token || claimed.Status != StatusInstalling || claimed.LastError != "" {
				t.Fatalf("bad owner: %+v", claimed)
			}
			if existing && (claimed.Source != InstallationSourceImage || claimed.InstalledVersion != "old-version" || claimed.DefinitionRevision != "old-revision" || claimed.SourceURL != "https://old.example" || claimed.ManifestDigest != "sha256:old") {
				t.Fatalf("claim discarded prior installation facts: %+v", claimed)
			}
			// Observations that read the old terminal row before the claim cannot
			// write through the owner fence when they resume on another Server.
			if _, err := store.Upsert(ctx, UpsertInstallation{InstallationKey: key, Source: InstallationSourceImage, Status: StatusInstalled}); !errors.Is(err, ErrBusy) {
				t.Fatalf("unowned upsert: %v", err)
			}
			if _, err := store.SetStatus(ctx, key, StatusMissing, "stale observation"); !errors.Is(err, ErrBusy) {
				t.Fatalf("unowned status update: %v", err)
			}
			staleVersion := "stale-version"
			if _, err := store.UpdateObserved(ctx, key, ObservedUpdate{InstalledVersion: &staleVersion}); !errors.Is(err, ErrBusy) {
				t.Fatalf("unowned observation: %v", err)
			}
			if err := store.Delete(ctx, key); !errors.Is(err, ErrBusy) {
				t.Fatalf("unowned remove: %v", err)
			}
			owned, err := store.Get(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			if owned.OperationID != winner.token || owned.Status != StatusInstalling || owned.InstalledVersion != claimed.InstalledVersion || owned.Source != claimed.Source || owned.LastError != "" {
				t.Fatalf("unowned write changed active operation: %+v", owned)
			}
			checked := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
			terminal := Installation{Source: InstallationSourceManaged, Status: StatusInstalled, InstalledVersion: "done-version", LatestVersion: "next-version", LastCheckedAt: &checked, ManifestDigest: "sha256:done", SourceURL: "https://done.example", RegistryID: "memoh", DefinitionRevision: "done-revision"}
			done, err := store.FinishOperation(ctx, key, winner.token, &terminal)
			if err != nil {
				t.Fatal(err)
			}
			if done.OperationID != "" || done.Status != terminal.Status || done.Source != terminal.Source || done.InstalledVersion != terminal.InstalledVersion || done.LatestVersion != terminal.LatestVersion || done.LastCheckedAt == nil || !done.LastCheckedAt.Equal(checked) || done.ManifestDigest != terminal.ManifestDigest || done.SourceURL != terminal.SourceURL || done.RegistryID != terminal.RegistryID || done.DefinitionRevision != terminal.DefinitionRevision {
				t.Fatalf("incomplete finish: %+v", done)
			}
			newerToken := strings.Repeat("f", 32)
			newer, err := store.ClaimOperation(ctx, UpsertInstallation{InstallationKey: key, Source: InstallationSourceManaged, Status: StatusRemoving}, newerToken)
			if err != nil {
				t.Fatal(err)
			}
			if newer.OperationID != newerToken {
				t.Fatal("new operation not owned")
			}
			terminal.Status = StatusFailed
			terminal.LastError = "stale interrupted attempt"
			if _, err := store.FinishOperation(ctx, key, winner.token, &terminal); !errors.Is(err, ErrBusy) {
				t.Fatalf("stale terminal write: %v", err)
			}
			if _, err := store.FinishOperation(ctx, key, winner.token, nil); !errors.Is(err, ErrBusy) {
				t.Fatalf("stale remove: %v", err)
			}
			unchanged, err := store.Get(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			if unchanged.OperationID != newerToken || unchanged.Status != StatusRemoving || unchanged.InstalledVersion != "done-version" || unchanged.LastError != "" {
				t.Fatalf("stale receipt changed owner: %+v", unchanged)
			}
			deleted, err := store.FinishOperation(ctx, key, newerToken, nil)
			if err != nil {
				t.Fatal(err)
			}
			if deleted.ID != newer.ID || deleted.OperationID != newerToken {
				t.Fatalf("remove did not return owned row: %+v", deleted)
			}
			if _, err := store.Get(ctx, key); !errors.Is(err, ErrInstallationNotFound) {
				t.Fatalf("removed record still present: %v", err)
			}
			if _, err := store.FinishOperation(ctx, key, newerToken, nil); !errors.Is(err, ErrBusy) {
				t.Fatalf("duplicate removal: %v", err)
			}
		})
	}
}

func TestPostgresOperationFinishCannotCrossTeam(t *testing.T) {
	ctx := t.Context()
	pool := openDependencyPostgres(t, ctx)
	store := NewPostgresStore(postgresstore.NewQueries(dbsqlc.New(pool))).(*postgresStore)
	key := InstallationKey{BotID: createDependencyBot(t, ctx, pool), WorkspaceTargetID: "native", DependencyID: "codex"}
	token := strings.Repeat("a", 32)
	if _, err := store.ClaimOperation(ctx, UpsertInstallation{InstallationKey: key, Source: InstallationSourceManaged, Status: StatusInstalling}, token); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	role := pgx.Identifier{"operation_scope_" + strings.ReplaceAll(key.BotID, "-", "")[:8]}.Sanitize()
	for _, sql := range []string{"CREATE ROLE " + role + " NOLOGIN", "GRANT USAGE ON SCHEMA public TO " + role, "GRANT SELECT,UPDATE,DELETE ON bot_dependency_installations TO " + role, "SET LOCAL ROLE " + role, "SELECT set_config('memoh.team_id','ffffffff-ffff-4fff-8fff-ffffffffffff',true)"} {
		if _, err := tx.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	scoped := NewPostgresStore(postgresstore.NewQueries(dbsqlc.New(tx))).(*postgresStore)
	terminal := Installation{Source: InstallationSourceManaged, Status: StatusFailed, LastError: "cross-team attempt"}
	if _, err := scoped.FinishOperation(ctx, key, token, &terminal); !errors.Is(err, ErrBusy) {
		t.Fatalf("cross-team finish: %v", err)
	}
	if _, err := scoped.FinishOperation(ctx, key, token, nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("cross-team remove: %v", err)
	}
	own, err := store.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if own.OperationID != token || own.Status != StatusInstalling || own.LastError != "" {
		t.Fatalf("cross-team write succeeded: %+v", own)
	}
}
