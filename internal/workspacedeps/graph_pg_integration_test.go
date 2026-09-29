//go:build integration

package workspacedeps

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
)

func TestPostgresGraphAdmissionFencesServersAndCommitsReferencesAtomically(t *testing.T) {
	ctx := t.Context()
	pool := openDependencyPostgres(t, ctx)
	bot := createDependencyBot(t, ctx, pool)
	makeStore := func() *postgresStore {
		return NewPostgresStore(postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))).(*postgresStore)
	}
	first, second := makeStore(), makeStore()
	owner := uuid.NewString()
	if _, err := first.ClaimGraph(ctx, bot, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := second.ClaimGraph(ctx, bot, uuid.NewString()); !errors.Is(err, ErrBusy) {
		t.Fatal("concurrent graph admitted", err)
	}
	graph := DependencyGraph{"parent": {Requires: []string{"old"}, Pending: []string{"new"}, Known: true, Explicit: true}}
	if err := first.WriteGraph(ctx, bot, owner, graph); err != nil {
		t.Fatal(err)
	}
	key := InstallationKey{BotID: bot, DependencyID: "parent"}
	token := strings.Repeat("a", 32)
	if _, err := first.ClaimOperation(ctx, UpsertInstallation{InstallationKey: key, GraphOwner: owner, Source: InstallationSourceManaged, Status: StatusUpdating}, token); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE bot_dependency_graphs SET lease_until = now() - interval '1 second' WHERE bot_id = $1", bot); err != nil {
		t.Fatal(err)
	}
	if _, err := second.ClaimGraph(ctx, bot, uuid.NewString()); !errors.Is(err, ErrBusy) {
		t.Fatal("stole graph from unfinished script", err)
	}
	terminal := Installation{Source: InstallationSourceManaged, Status: StatusInstalled, InstalledVersion: "2.0.0", Requires: []string{"new"}, RelationshipsKnown: true}
	if _, err := second.FinishOperation(ctx, key, token, &terminal); err != nil {
		t.Fatal(err)
	}
	// A recovered receipt commits the graph even without the original Server context.
	graph, err := second.ReadGraph(ctx, bot)
	if err != nil {
		t.Fatal(err)
	}
	rel := graph["parent"]
	if !rel.Explicit || !rel.Known || len(rel.Pending) != 0 || len(rel.Requires) != 1 || rel.Requires[0] != "new" {
		t.Fatal(graph)
	}
	next := uuid.NewString()
	if _, err := second.ClaimGraph(ctx, bot, next); err != nil {
		t.Fatal(err)
	}
	if err := first.WriteGraph(ctx, bot, owner, DependencyGraph{}); !errors.Is(err, ErrBusy) {
		t.Fatal("expired owner wrote graph", err)
	}
	if _, err := first.ClaimOperation(ctx, UpsertInstallation{InstallationKey: key, GraphOwner: owner, Source: InstallationSourceManaged, Status: StatusRemoving}, strings.Repeat("b", 32)); !errors.Is(err, ErrBusy) {
		t.Fatal("expired owner ran script", err)
	}
	if err := second.ReleaseGraph(ctx, bot, next); err != nil {
		t.Fatal(err)
	}
	const workers = 10
	var wg sync.WaitGroup
	start := make(chan struct{})
	winners := make(chan string, workers)
	failures := make(chan error, workers)
	for range workers {
		wg.Go(func() {
			<-start
			id := uuid.NewString()
			_, err := makeStore().ClaimGraph(ctx, bot, id)
			if err == nil {
				winners <- id
			} else if !errors.Is(err, ErrBusy) {
				failures <- err
			}
		})
	}
	close(start)
	wg.Wait()
	close(winners)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	if len(winners) != 1 {
		t.Fatalf("graph claim winners: %d", len(winners))
	}
}

func TestPostgresPlanPersistenceAndBotIsolation(t *testing.T) {
	ctx := t.Context()
	pool := openDependencyPostgres(t, ctx)
	bot := createDependencyBot(t, ctx, pool)
	other := createDependencyBot(t, ctx, pool)
	first := NewPostgresStore(postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))).(*postgresStore)
	plan := Plan{ID: uuid.NewString(), BotID: bot, Nodes: []PlanNode{{DependencyID: "node", Revision: strings.Repeat("a", 64), Action: "install"}}}
	if err := first.SavePlan(ctx, bot, plan); err != nil {
		t.Fatal(err)
	}
	second := NewPostgresStore(postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))).(*postgresStore)
	loaded, err := second.LoadPlan(ctx, bot, plan.ID)
	if err != nil || loaded.Nodes[0].Revision != plan.Nodes[0].Revision {
		t.Fatal(loaded, err)
	}
	if _, err := second.LoadPlan(ctx, other, plan.ID); err == nil {
		t.Fatal("plan crossed bots")
	}
}
