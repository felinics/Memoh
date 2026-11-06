//go:build integration

package db_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/team"
)

type migratedDependencyRef struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
	Retired  bool   `json:"retired,omitempty"`
}

// dependencyPublication builds a release document and archive whose digests
// match the way the Server verifies them: the revision is sha256 of the
// release bytes and the release names sha256 of the archive.
type dependencyPublication struct {
	revision string
	release  []byte
	archive  []byte
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func newDependencyPublication(t *testing.T, id, content string) dependencyPublication {
	t.Helper()
	archive := []byte("archive of " + id + " " + content)
	release, err := json.Marshal(map[string]any{
		"dependency_id": id,
		"artifact":      map[string]any{"digest": sha256Hex(archive), "size": len(archive)},
		"icon":          map[string]any{"digest": sha256Hex([]byte("icon " + id))},
	})
	if err != nil {
		t.Fatal(err)
	}
	return dependencyPublication{revision: sha256Hex(release), release: release, archive: archive}
}

func TestGlobalDependencyCatalogMigrationCopiesVerifiedRows(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	dsn := teamMigrationDSN(t)
	steps := countMigrationsFrom(t, "0173_global_dependency_catalog.up.sql")
	stepDown(t, dsn, steps)

	const (
		source      = "https://supermarket.example"
		otherSource = "https://other.example"
		teamTwo     = "00000000-0000-4000-8000-000000000173"
		teamThree   = "00000000-0000-4000-8000-000000000273"
	)
	if _, err := pool.Exec(ctx, `INSERT INTO teams (id) VALUES ($1), ($2)`, teamTwo, teamThree); err != nil {
		t.Fatalf("seed teams: %v", err)
	}

	a1 := newDependencyPublication(t, "alpha", "1")
	a2 := newDependencyPublication(t, "alpha", "2")
	b0 := newDependencyPublication(t, "beta", "0")
	b1 := newDependencyPublication(t, "beta", "1")
	e1 := newDependencyPublication(t, "epsilon", "1")
	// The release hashes to its revision, but the stored archive does not match
	// the digest the release declares.
	badArchive := newDependencyPublication(t, "gamma", "1")
	badArchive.archive = []byte("tampered archive")
	// The release bytes neither hash to the revision nor parse as JSON.
	badRelease := dependencyPublication{revision: sha256Hex([]byte("expected")), release: []byte("\xff not json"), archive: []byte("x")}

	now := time.Now().UTC().Truncate(time.Microsecond)
	oldest := now.Add(-72 * time.Hour)
	type definitionRow struct {
		teamID, source, id string
		pub                dependencyPublication
		createdAt          time.Time
	}
	for _, row := range []definitionRow{
		{team.DefaultTeamID, source, "alpha", a1, now.Add(-time.Hour)},
		{teamTwo, source, "alpha", a1, oldest},
		{teamTwo, source, "alpha", a2, now},
		{team.DefaultTeamID, source, "beta", b1, now},
		{teamThree, source, "beta", b0, now},
		{teamThree, source, "epsilon", e1, now},
		{team.DefaultTeamID, source, "gamma", badArchive, now},
		{teamTwo, source, "delta", badRelease, now},
		{team.DefaultTeamID, otherSource, "gamma", badArchive, now},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO public.workspace_dependency_definitions
				(team_id, source_url, registry_id, dependency_id, revision, release_bytes, artifact_bytes, icon_digest, created_at)
			VALUES ($1, $2, 'memoh', $3, $4, $5, $6, $7, $8)`,
			row.teamID, row.source, row.id, row.pub.revision, row.pub.release, row.pub.archive, sha256Hex([]byte("icon "+row.id)), row.createdAt,
		); err != nil {
			t.Fatalf("seed definition %s/%s: %v", row.teamID, row.id, err)
		}
	}

	catalogBytes := func(refs ...migratedDependencyRef) []byte {
		data, err := json.Marshal(map[string]any{"entries": refs})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	type catalogRow struct {
		teamID, source string
		data           []byte
		generation     int64
		fetchedAt      time.Time
	}
	newest := now.Add(-time.Hour)
	for _, row := range []catalogRow{
		// Older catalog: beta is current here and gamma is unverified.
		{team.DefaultTeamID, source, catalogBytes(
			migratedDependencyRef{ID: "alpha", Revision: a1.revision},
			migratedDependencyRef{ID: "beta", Revision: b1.revision},
			migratedDependencyRef{ID: "gamma", Revision: badArchive.revision},
		), 7, now.Add(-2 * time.Hour)},
		// Newest catalog: the base. Its delta entry has no verified definition.
		{teamTwo, source, catalogBytes(
			migratedDependencyRef{ID: "alpha", Revision: a2.revision},
			migratedDependencyRef{ID: "delta", Revision: badRelease.revision},
		), 2, newest},
		// Oldest catalog: an older beta and a retired epsilon.
		{teamThree, source, catalogBytes(
			migratedDependencyRef{ID: "beta", Revision: b0.revision},
			migratedDependencyRef{ID: "epsilon", Revision: e1.revision, Retired: true},
		), 9, now.Add(-3 * time.Hour)},
		// A source whose only reference is unverified.
		{team.DefaultTeamID, otherSource, catalogBytes(
			migratedDependencyRef{ID: "gamma", Revision: badArchive.revision},
		), 1, now},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO public.workspace_dependency_catalogs (team_id, source_url, catalog_bytes, generation, fetched_at)
			VALUES ($1, $2, $3, $4, $5)`,
			row.teamID, row.source, row.data, row.generation, row.fetchedAt,
		); err != nil {
			t.Fatalf("seed catalog %s: %v", row.teamID, err)
		}
	}

	// Stop at 0173: later migrations drop the per-team tables read below.
	stepUp(t, dsn, 1)
	assertGlobalDependencyCatalog(t, pool, source, otherSource, oldest, newest, []migratedDependencyRef{
		{ID: "alpha", Revision: a2.revision},
		{ID: "beta", Revision: b1.revision, Retired: true},
		{ID: "epsilon", Revision: e1.revision, Retired: true},
	}, map[string]string{
		"alpha/" + a1.revision:   "",
		"alpha/" + a2.revision:   "",
		"beta/" + b0.revision:    "",
		"beta/" + b1.revision:    "",
		"epsilon/" + e1.revision: "",
	}, a1.revision)

	var forced int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_class
		WHERE oid IN ('public.workspace_dependency_definitions'::regclass, 'public.workspace_dependency_catalogs'::regclass)
		  AND relrowsecurity AND relforcerowsecurity`).Scan(&forced); err != nil {
		t.Fatalf("inspect legacy RLS: %v", err)
	}
	if forced != 2 {
		t.Fatalf("legacy tables with FORCE RLS restored = %d, want 2", forced)
	}

	// Down drops only the global tables; the legacy rows remain, so the copy
	// runs again with the same result.
	stepDown(t, dsn, 1)
	var globalTables, legacyDefinitions int
	if err := pool.QueryRow(ctx, `
		SELECT (to_regclass('template.workspace_dependency_definitions') IS NOT NULL)::int
		     + (to_regclass('template.workspace_dependency_catalogs') IS NOT NULL)::int,
		       (SELECT count(*) FROM public.workspace_dependency_definitions)`).Scan(&globalTables, &legacyDefinitions); err != nil {
		t.Fatalf("inspect after down: %v", err)
	}
	if globalTables != 0 || legacyDefinitions != 9 {
		t.Fatalf("after down: global tables=%d legacy definitions=%d, want 0 and 9", globalTables, legacyDefinitions)
	}
	stepUp(t, dsn, 1)
	var definitions, catalogs int
	if err := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM template.workspace_dependency_definitions),
		       (SELECT count(*) FROM template.workspace_dependency_catalogs)`).Scan(&definitions, &catalogs); err != nil {
		t.Fatalf("inspect after re-up: %v", err)
	}
	if definitions != 5 || catalogs != 1 {
		t.Fatalf("after re-up: definitions=%d catalogs=%d, want 5 and 1", definitions, catalogs)
	}
}

func assertGlobalDependencyCatalog(
	t *testing.T, pool *pgxpool.Pool, source, otherSource string, oldest, newest time.Time,
	wantEntries []migratedDependencyRef, wantDefinitions map[string]string, sharedRevision string,
) {
	t.Helper()
	ctx := context.Background()

	rows, err := pool.Query(ctx, `
		SELECT source_url, dependency_id, revision, release_bytes, artifact_bytes, created_at
		FROM template.workspace_dependency_definitions`)
	if err != nil {
		t.Fatalf("list global definitions: %v", err)
	}
	got := map[string]string{}
	for rows.Next() {
		var rowSource, id, revision string
		var release, archive []byte
		var createdAt time.Time
		if err := rows.Scan(&rowSource, &id, &revision, &release, &archive, &createdAt); err != nil {
			t.Fatal(err)
		}
		if rowSource != source {
			t.Errorf("definition %s copied for unexpected source %s", id, rowSource)
		}
		if sha256Hex(release) != revision {
			t.Errorf("definition %s/%s copied with mismatched release bytes", id, revision)
		}
		if revision == sharedRevision && !createdAt.Equal(oldest) {
			t.Errorf("deduplicated definition created_at = %v, want earliest %v", createdAt, oldest)
		}
		got[id+"/"+revision] = ""
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, wantDefinitions) {
		t.Fatalf("global definitions = %v, want %v", got, wantDefinitions)
	}

	var data []byte
	var generation int64
	var fetchedAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT catalog_bytes, generation, fetched_at
		FROM template.workspace_dependency_catalogs WHERE source_url = $1`, source).Scan(&data, &generation, &fetchedAt); err != nil {
		t.Fatalf("read global catalog: %v", err)
	}
	if generation != 1 || !fetchedAt.Equal(newest) {
		t.Errorf("catalog generation=%d fetched_at=%v, want 1 and the base row's %v", generation, fetchedAt, newest)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var index struct {
		Entries []migratedDependencyRef `json:"entries"`
	}
	if err := decoder.Decode(&index); err != nil {
		t.Fatalf("decode catalog %s: %v", data, err)
	}
	if !reflect.DeepEqual(index.Entries, wantEntries) {
		t.Fatalf("catalog entries = %+v, want %+v", index.Entries, wantEntries)
	}

	var otherCatalogs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM template.workspace_dependency_catalogs WHERE source_url = $1`, otherSource).Scan(&otherCatalogs); err != nil {
		t.Fatal(err)
	}
	if otherCatalogs != 0 {
		t.Fatalf("source without verified entries got a catalog row")
	}
}

func TestDropTeamDependencyCatalogMigration(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	dsn := teamMigrationDSN(t)
	stepDown(t, dsn, countMigrationsFrom(t, "0173_global_dependency_catalog.up.sql"))
	stepUp(t, dsn, 1)

	const source = "https://supermarket.example"
	late := newDependencyPublication(t, "late", "1")
	corrupt := newDependencyPublication(t, "corrupt", "1")
	corrupt.archive = []byte("tampered archive")
	// A Server of the previous release writes into the per-team table while 0173
	// is already applied.
	for _, row := range []struct {
		id  string
		pub dependencyPublication
	}{{"late", late}, {"corrupt", corrupt}} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO public.workspace_dependency_definitions
				(team_id, source_url, registry_id, dependency_id, revision, release_bytes, artifact_bytes, icon_digest)
			VALUES ($1, $2, 'memoh', $3, $4, $5, $6, '')`,
			team.DefaultTeamID, source, row.id, row.pub.revision, row.pub.release, row.pub.archive,
		); err != nil {
			t.Fatalf("seed definition %s: %v", row.id, err)
		}
	}

	publicTables := func() int {
		var n int
		if err := pool.QueryRow(ctx, `
			SELECT (to_regclass('public.workspace_dependency_definitions') IS NOT NULL)::int
			     + (to_regclass('public.workspace_dependency_catalogs') IS NOT NULL)::int`).Scan(&n); err != nil {
			t.Fatalf("inspect public tables: %v", err)
		}
		return n
	}

	stepUp(t, dsn, 1)
	var lateCount, corruptCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE dependency_id = 'late'),
		       count(*) FILTER (WHERE dependency_id = 'corrupt')
		FROM template.workspace_dependency_definitions WHERE source_url = $1`, source).Scan(&lateCount, &corruptCount); err != nil {
		t.Fatalf("inspect template definitions: %v", err)
	}
	if lateCount != 1 || corruptCount != 0 {
		t.Fatalf("copied definitions: verified=%d corrupt=%d, want 1 and 0", lateCount, corruptCount)
	}
	if n := publicTables(); n != 0 {
		t.Fatalf("public tables after up = %d, want 0", n)
	}

	stepDown(t, dsn, 1)
	if n := publicTables(); n != 2 {
		t.Fatalf("public tables after down = %d, want 2", n)
	}
	var forced, policies int
	if err := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM pg_class
		        WHERE oid IN ('public.workspace_dependency_definitions'::regclass, 'public.workspace_dependency_catalogs'::regclass)
		          AND relrowsecurity AND relforcerowsecurity),
		       (SELECT count(*) FROM pg_policies
		        WHERE schemaname = 'public' AND tablename IN ('workspace_dependency_definitions', 'workspace_dependency_catalogs'))`).Scan(&forced, &policies); err != nil {
		t.Fatalf("inspect restored RLS: %v", err)
	}
	if forced != 2 || policies != 8 {
		t.Fatalf("after down: FORCE RLS tables=%d policies=%d, want 2 and 8", forced, policies)
	}

	stepUp(t, dsn, 1)
	if n := publicTables(); n != 0 {
		t.Fatalf("public tables after re-up = %d, want 0", n)
	}
}
