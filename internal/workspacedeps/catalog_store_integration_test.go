//go:build integration

package workspacedeps

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/team"
)

func TestPostgresCatalogPersistsReleasesAndCompareAndSwap(t *testing.T) {
	ctx := t.Context()
	pool := openDependencyPostgres(t, ctx)
	store := NewPostgresCatalogStore(postgresstore.NewQueries(dbsqlc.New(pool)))
	server := newCatalogHTTPFixture(t)
	provider, err := NewRemoteCatalog(server.server.URL, store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.Snapshot(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	dep := result.Catalog.MustGet("codex")
	key := DefinitionKey{SourceURL: server.server.URL, DependencyID: dep.ID, Revision: dep.Revision}
	stored, err := store.GetDefinition(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Metadata) == 0 || len(stored.Archive) == 0 {
		t.Fatal("release was not persisted")
	}
	modified := stored
	modified.Metadata = []byte("different bytes")
	if err := store.PutDefinition(ctx, modified); err == nil {
		t.Fatal("accepted corrupt release bytes")
	}
	unchanged, err := store.GetDefinition(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(unchanged.Metadata, stored.Metadata) {
		t.Fatal("immutable release was overwritten")
	}
	index, err := store.GetCatalog(ctx, server.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if index.Generation != 1 || index.FetchedAt.IsZero() {
		t.Fatalf("catalog generation=%d fetched=%v", index.Generation, index.FetchedAt)
	}
	if ok, err := store.PutCatalog(ctx, index); err != nil || !ok {
		t.Fatalf("CAS: %v %v", ok, err)
	}
	if ok, err := store.PutCatalog(ctx, index); err != nil || ok {
		t.Fatalf("stale CAS: %v %v", ok, err)
	}
	server.server.Close()
	restarted, err := NewRemoteCatalog(server.server.URL, store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cached, err := restarted.Snapshot(ctx, true); err != nil || len(cached.Catalog.List()) != 5 || !cached.Stale {
		t.Fatalf("offline: %+v %v", cached, err)
	}
	if icon, err := restarted.Icon(ctx, dep.IconDigest); err != nil || len(icon) == 0 {
		t.Fatalf("cached icon: %v", err)
	}
	if _, err := store.GetCatalog(ctx, server.server.URL+"/different-source"); !errors.Is(err, ErrCatalogCacheMiss) {
		t.Fatalf("source isolation: %v", err)
	}
}

func TestPostgresCatalogTeamPolicies(t *testing.T) {
	ctx := t.Context()
	pool := openDependencyPostgres(t, ctx)
	store := NewPostgresCatalogStore(postgresstore.NewQueries(dbsqlc.New(pool)))
	source := "https://rls-" + uuid.NewString() + ".example"
	if ok, err := store.PutCatalog(ctx, CachedCatalog{SourceURL: source, Data: []byte(`{"entries":[]}`)}); err != nil || !ok {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	role := "deps_catalog_" + uuid.NewString()[:8]
	identifier := pgx.Identifier{role}.Sanitize()
	for _, statement := range []string{
		"CREATE ROLE " + identifier + " NOLOGIN",
		"GRANT USAGE ON SCHEMA public TO " + identifier,
		"GRANT SELECT, INSERT, UPDATE ON workspace_dependency_catalogs, workspace_dependency_definitions TO " + identifier,
		"SET LOCAL ROLE " + identifier,
	} {
		if _, err := tx.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	scoped := NewPostgresCatalogStore(postgresstore.NewQueries(dbsqlc.New(tx)))
	if _, err := scoped.GetCatalog(ctx, source); err != nil {
		t.Fatalf("own team: %v", err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('memoh.team_id',$1,true)", uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := scoped.GetCatalog(ctx, source); !errors.Is(err, ErrCatalogCacheMiss) {
		t.Fatalf("cross-team read: %v", err)
	}
	_, err = tx.Exec(ctx, "INSERT INTO workspace_dependency_catalogs(team_id,source_url,catalog_bytes) VALUES($1,$2,$3)", team.DefaultTeamID, source+"/other", []byte(`{"entries":[]}`))
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("cross-team write was not denied by RLS: %v", err)
	}
}

func TestPostgresInstallationDefinitionProvenance(t *testing.T) {
	ctx := t.Context()
	pool := openDependencyPostgres(t, ctx)
	botID := createDependencyBot(t, ctx, pool)
	store := newIntegrationStore(pool)
	key := InstallationKey{BotID: botID, WorkspaceTargetID: "native", DependencyID: "codex"}
	revision := "4111dba29084f12372fb90545b0e85f8fd5c6a87e74e9203068526bc69095ac2"
	created, err := store.Upsert(ctx, UpsertInstallation{InstallationKey: key, Source: InstallationSourceManaged, Status: StatusInstalled, InstalledVersion: "1.0.0", SourceURL: "https://catalog.example", RegistryID: "memoh", DefinitionRevision: revision})
	if err != nil {
		t.Fatal(err)
	}
	if created.SourceURL != "https://catalog.example" || created.RegistryID != "memoh" || created.DefinitionRevision != revision {
		t.Fatalf("provenance not recorded: %+v", created)
	}
	next := "681df9a9fed994c0dca215f2a41ac108435e254ee1687c4f7e8cd71470258473"
	updated, err := store.UpdateObserved(ctx, key, ObservedUpdate{DefinitionRevision: &next})
	if err != nil {
		t.Fatal(err)
	}
	if updated.DefinitionRevision != next || updated.SourceURL != created.SourceURL {
		t.Fatalf("provenance update: %+v", updated)
	}
}

func TestPostgresCatalogPrunesOnlyUnreferencedHistory(t *testing.T) {
	ctx := t.Context()
	pool := openDependencyPostgres(t, ctx)
	store := NewPostgresCatalogStore(postgresstore.NewQueries(dbsqlc.New(pool)))
	server := newCatalogHTTPFixture(t)
	p, err := NewRemoteCatalog(server.server.URL, store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := p.Snapshot(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	botID := createDependencyBot(t, ctx, pool)
	// Current intent may use another registry while rollback state still
	// references this source. Preserve every source for an installed identity.
	codex := first.Catalog.MustGet("codex")
	_, err = newIntegrationStore(pool).Upsert(ctx, UpsertInstallation{InstallationKey: InstallationKey{BotID: botID, WorkspaceTargetID: "native", DependencyID: "codex"}, Source: InstallationSourceManaged, Status: StatusInstalled, SourceURL: p.sourceURL + "/new-registry", RegistryID: "memoh", DefinitionRevision: codex.Revision})
	if err != nil {
		t.Fatal(err)
	}
	historical := map[string]DefinitionKey{}
	for _, dep := range first.Catalog.List() {
		historical[dep.ID] = DefinitionKey{SourceURL: p.sourceURL, DependencyID: dep.ID, Revision: dep.Revision}
		server.updateScript(t, dep.ID, "# next publication\n")
	}
	latest, err := p.Snapshot(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE workspace_dependency_definitions SET last_accessed_at = now() - interval '31 days' WHERE source_url = $1", p.sourceURL); err != nil {
		t.Fatal(err)
	}
	// A recent preview/download remains available throughout its grace period.
	if _, err := pool.Exec(ctx, "UPDATE workspace_dependency_definitions SET last_accessed_at = now() WHERE source_url=$1 AND dependency_id='uv' AND revision=$2", p.sourceURL, historical["uv"].Revision); err != nil {
		t.Fatal(err)
	}
	// Reading an old publication for a new preview renews its retention lease.
	other, err := store.GetDefinition(ctx, historical["node"])
	if err != nil {
		t.Fatal(err)
	}
	other.SourceURL += "/other-source"
	if err := store.PutDefinition(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE workspace_dependency_definitions SET last_accessed_at=now()-interval '31 days' WHERE source_url=$1", other.SourceURL); err != nil {
		t.Fatal(err)
	}
	if err := store.(*postgresCatalogStore).PruneDefinitions(ctx, p.sourceURL); err != nil {
		t.Fatal(err)
	}
	for id, key := range historical {
		_, err := store.GetDefinition(ctx, key)
		if id == "codex" || id == "uv" || id == "node" {
			if err != nil {
				t.Fatalf("removed rollback or recent publication %s: %v", id, err)
			}
		} else if !errors.Is(err, ErrCatalogCacheMiss) {
			t.Fatalf("unused historical %s retained: %v", id, err)
		}
	}
	for _, dep := range latest.Catalog.List() {
		if _, err := store.GetDefinition(ctx, DefinitionKey{SourceURL: p.sourceURL, DependencyID: dep.ID, Revision: dep.Revision}); err != nil {
			t.Fatalf("removed catalog publication %s: %v", dep.ID, err)
		}
	}
	if _, err := store.GetDefinition(ctx, other.DefinitionKey); err != nil {
		t.Fatalf("pruned another source: %v", err)
	}
}

func TestPostgresIconLookupDoesNotParseUnrelatedRelease(t *testing.T) {
	ctx := t.Context()
	pool := openDependencyPostgres(t, ctx)
	store := NewPostgresCatalogStore(postgresstore.NewQueries(dbsqlc.New(pool)))
	server := newCatalogHTTPFixture(t)
	p, err := NewRemoteCatalog(server.server.URL, store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Snapshot(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	dep := result.Catalog.MustGet("codex")
	if _, err := pool.Exec(ctx, `INSERT INTO workspace_dependency_definitions(source_url,registry_id,dependency_id,revision,release_bytes,artifact_bytes,icon_digest) VALUES($1,'memoh','corrupt-release',$2,$3,$3,'')`, p.sourceURL, strings.Repeat("a", 64), []byte("invalid JSON")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), "DELETE FROM workspace_dependency_definitions WHERE source_url=$1 AND dependency_id='corrupt-release'", p.sourceURL)
	})
	if icon, err := p.Icon(ctx, dep.IconDigest); err != nil || len(icon) == 0 {
		t.Fatalf("unrelated malformed release broke icon lookup: %v", err)
	}
	if _, err := p.Icon(ctx, strings.Repeat("b", 64)); !errors.Is(err, ErrDependencyNotFound) {
		t.Fatalf("missing digest: %v", err)
	}
	// Force index selection only to verify the predicate has an indexed access
	// path. Production remains free to choose a sequential scan for tiny tables.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan=off"); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, `EXPLAIN SELECT release_bytes FROM workspace_dependency_definitions WHERE team_id=public.memoh_current_team_id() AND source_url=$1 AND icon_digest=$2 LIMIT 1`, p.sourceURL, dep.IconDigest)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan += line + "\n"
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, "idx_workspace_dependency_definitions_icon") {
		t.Fatalf("icon lookup has no indexed path: %s", plan)
	}
}

func TestPostgresUpsertClearsPriorOperationFailure(t *testing.T) {
	ctx := t.Context()
	pool := openDependencyPostgres(t, ctx)
	store := newIntegrationStore(pool)
	key := InstallationKey{BotID: createDependencyBot(t, ctx, pool), WorkspaceTargetID: "native", DependencyID: "codex"}
	intent := UpsertInstallation{InstallationKey: key, Source: InstallationSourceManaged, Status: StatusInstalling}
	if _, err := store.Upsert(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetStatus(ctx, key, StatusFailed, "previous attempt failed"); err != nil {
		t.Fatal(err)
	}
	retried, err := store.Upsert(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	if retried.LastError != "" || retried.Status != StatusInstalling {
		t.Fatalf("retry kept prior failure: %+v", retried)
	}
}
