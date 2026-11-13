//go:build integration

package workspacedeps

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

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

// The catalog belongs to the configured source, not to a team: a runtime role
// without BYPASSRLS reads the same rows under any team context or none.
func TestPostgresCatalogIsSharedAcrossTeams(t *testing.T) {
	ctx := t.Context()
	pool := openDependencyPostgres(t, ctx)
	store := NewPostgresCatalogStore(postgresstore.NewQueries(dbsqlc.New(pool)))
	source := "https://shared-" + uuid.NewString() + ".example"
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
		"CREATE ROLE " + identifier + " NOLOGIN NOBYPASSRLS",
		"GRANT USAGE ON SCHEMA template TO " + identifier,
		"GRANT SELECT, INSERT, UPDATE ON template.workspace_dependency_catalogs, template.workspace_dependency_definitions TO " + identifier,
		"SET LOCAL ROLE " + identifier,
	} {
		if _, err := tx.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	scoped := NewPostgresCatalogStore(postgresstore.NewQueries(dbsqlc.New(tx)))
	for _, teamID := range []string{team.DefaultTeamID, uuid.NewString(), ""} {
		if _, err := tx.Exec(ctx, "SELECT set_config('memoh.team_id',$1,true)", teamID); err != nil {
			t.Fatal(err)
		}
		got, err := scoped.GetCatalog(ctx, source)
		if err != nil || got.Generation != 1 {
			t.Fatalf("team %q: generation=%d err=%v", teamID, got.Generation, err)
		}
	}
	// Another team's publish advances the one shared generation.
	if ok, err := scoped.PutCatalog(ctx, CachedCatalog{SourceURL: source, Data: []byte(`{"entries":[]}`), Generation: 1}); err != nil || !ok {
		t.Fatalf("publish from another team: %v %v", ok, err)
	}
	if got, err := scoped.GetCatalog(ctx, source); err != nil || got.Generation != 2 {
		t.Fatalf("shared generation=%d err=%v", got.Generation, err)
	}
}

func TestPostgresInstallationDefinitionProvenance(t *testing.T) {
	ctx := t.Context()
	pool := openDependencyPostgres(t, ctx)
	botID := createDependencyBot(t, ctx, pool)
	store := newIntegrationStore(pool)
	key := InstallationKey{BotID: botID, DependencyID: "codex"}
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

// Definitions are immutable and append-only: reading one, including during
// catalog hydration, leaves the row untouched, and a new publication keeps the
// previous revision available for rollback.
func TestPostgresDefinitionReadsDoNotWrite(t *testing.T) {
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
	codex := first.Catalog.MustGet("codex")
	historical := DefinitionKey{SourceURL: p.sourceURL, DependencyID: codex.ID, Revision: codex.Revision}
	rowVersion := func() string {
		t.Helper()
		var xmin string
		if err := pool.QueryRow(ctx, `SELECT xmin::text FROM template.workspace_dependency_definitions WHERE source_url=$1 AND registry_id='memoh' AND dependency_id=$2 AND revision=$3`,
			historical.SourceURL, historical.DependencyID, historical.Revision).Scan(&xmin); err != nil {
			t.Fatal(err)
		}
		return xmin
	}
	before := rowVersion()
	if _, err := store.GetDefinition(ctx, historical); err != nil {
		t.Fatal(err)
	}
	if _, err := p.StoredDefinition(ctx, historical); err != nil {
		t.Fatal(err)
	}
	server.updateScript(t, codex.ID, "# next publication\n")
	latest, err := p.Snapshot(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Catalog.MustGet("codex").Revision == codex.Revision {
		t.Fatal("fixture did not publish a new revision")
	}
	if after := rowVersion(); after != before {
		t.Fatalf("definition row rewritten by reads: xmin %s -> %s", before, after)
	}
	if _, err := store.GetDefinition(ctx, historical); err != nil {
		t.Fatalf("previous publication removed: %v", err)
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
	if _, err := pool.Exec(ctx, `INSERT INTO template.workspace_dependency_definitions(source_url,registry_id,dependency_id,revision,release_bytes,artifact_bytes,icon_digest) VALUES($1,'memoh','corrupt-release',$2,$3,$3,'')`, p.sourceURL, strings.Repeat("a", 64), []byte("invalid JSON")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), "DELETE FROM template.workspace_dependency_definitions WHERE source_url=$1 AND dependency_id='corrupt-release'", p.sourceURL)
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
	rows, err := tx.Query(ctx, `EXPLAIN SELECT release_bytes FROM template.workspace_dependency_definitions WHERE source_url=$1 AND icon_digest=$2 LIMIT 1`, p.sourceURL, dep.IconDigest)
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
	if !strings.Contains(plan, "idx_template_workspace_dependency_definitions_icon") {
		t.Fatalf("icon lookup has no indexed path: %s", plan)
	}
}

func TestPostgresUpsertClearsPriorOperationFailure(t *testing.T) {
	ctx := t.Context()
	pool := openDependencyPostgres(t, ctx)
	store := newIntegrationStore(pool)
	key := InstallationKey{BotID: createDependencyBot(t, ctx, pool), DependencyID: "codex"}
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

type catalogTeamKey struct{}

// teamRoutedCatalogStore sends each call to the connection bound to the team
// in its context, the way a multi-team deployment scopes queries per request.
type teamRoutedCatalogStore struct {
	CatalogStore
	teams           map[string]CatalogStore
	definitionReads atomic.Int64
}

func (s *teamRoutedCatalogStore) route(ctx context.Context) CatalogStore {
	if teamID, ok := ctx.Value(catalogTeamKey{}).(string); ok {
		return s.teams[teamID]
	}
	return s.CatalogStore
}

func (s *teamRoutedCatalogStore) GetCatalog(ctx context.Context, source string) (CachedCatalog, error) {
	return s.route(ctx).GetCatalog(ctx, source)
}

func (s *teamRoutedCatalogStore) GetDefinition(ctx context.Context, key DefinitionKey) (CachedDefinition, error) {
	s.definitionReads.Add(1)
	return s.route(ctx).GetDefinition(ctx, key)
}

// Reads that alternate between teams share one hydrated snapshot: only the
// first read verifies the stored definitions.
func TestRemoteCatalogSnapshotIsSharedAcrossTeams(t *testing.T) {
	ctx := t.Context()
	pool := openDependencyPostgres(t, ctx)
	server := newCatalogHTTPFixture(t)
	publisher, err := NewRemoteCatalog(server.server.URL, NewPostgresCatalogStore(postgresstore.NewQueries(dbsqlc.New(pool))), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	published, err := publisher.Snapshot(ctx, true)
	if err != nil {
		t.Fatal(err)
	}

	teams := map[string]CatalogStore{}
	for _, teamID := range []string{team.DefaultTeamID, uuid.NewString()} {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(ctx)) })
		if _, err := tx.Exec(ctx, "SELECT set_config('memoh.team_id',$1,true)", teamID); err != nil {
			t.Fatal(err)
		}
		teams[teamID] = NewPostgresCatalogStore(postgresstore.NewQueries(dbsqlc.New(tx)))
	}
	store := &teamRoutedCatalogStore{CatalogStore: NewPostgresCatalogStore(postgresstore.NewQueries(dbsqlc.New(pool))), teams: teams}
	reader, err := NewRemoteCatalog(server.server.URL, store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	server.server.Close()

	var hydrated int64
	for range 2 {
		for teamID := range teams {
			result, err := reader.Cached(context.WithValue(ctx, catalogTeamKey{}, teamID))
			if err != nil {
				t.Fatalf("team %s: %v", teamID, err)
			}
			if result.Catalog.Fingerprint() != published.Catalog.Fingerprint() {
				t.Fatalf("team %s read a different catalog", teamID)
			}
			if hydrated == 0 {
				hydrated = store.definitionReads.Load()
			}
		}
	}
	if hydrated != int64(len(published.Catalog.List())) || store.definitionReads.Load() != hydrated {
		t.Fatalf("definition reads = %d after first hydrate %d; want the snapshot reused across teams", store.definitionReads.Load(), hydrated)
	}
}
