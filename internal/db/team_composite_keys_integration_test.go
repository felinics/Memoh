//go:build integration

package db_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestTeamCompositeKeys verifies the team-key migration:
//   - existing primary keys stay stable and gain a team-prefixed unique key
//   - every team table has a root FK (team_id) -> teams(id)
//   - every business FK is composite and carries team_id
//   - ON DELETE SET NULL clears only the original reference column
//   - a cross-team child insert is rejected; same-team self-reference works
func TestTeamCompositeKeys(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)

	// (1) Every team table has either a team-prefixed primary key or the
	// helper unique key used to preserve an existing global primary key.
	rows, err := pool.Query(ctx, `
		SELECT c.relname,
		       bool_or((SELECT a.attname FROM pg_attribute a
		         WHERE a.attrelid=con.conrelid AND a.attnum=con.conkey[1])='team_id') AS team_primary,
		       count(helper.oid)
		  FROM pg_constraint con
		  JOIN pg_class c ON c.oid = con.conrelid
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		  LEFT JOIN pg_constraint helper
		    ON helper.conrelid=con.conrelid AND helper.contype='u'
		   AND helper.conname LIKE 'memoh_team_key_%'
		 WHERE con.contype = 'p' AND n.nspname = 'public'
		   AND EXISTS (SELECT 1 FROM pg_attribute a
		                WHERE a.attrelid=c.oid AND a.attname='team_id' AND NOT a.attisdropped)
		 GROUP BY c.relname`)
	if err != nil {
		t.Fatalf("enumerate PKs: %v", err)
	}
	for rows.Next() {
		var tbl string
		var teamPrimary bool
		var helperCount int
		if err := rows.Scan(&tbl, &teamPrimary, &helperCount); err != nil {
			t.Fatalf("scan pk: %v", err)
		}
		if !teamPrimary && helperCount != 1 {
			t.Errorf("%s must have a team-prefixed PK or helper key, got pk=%v helpers=%d", tbl, teamPrimary, helperCount)
		}
	}
	rows.Close()

	// (2) SET NULL must never include team_id in its target column list.
	var unsafeSetNull int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_constraint con
		  JOIN pg_class c ON c.oid = con.conrelid
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE con.contype = 'f' AND con.confdeltype = 'n'
		   AND n.nspname = 'public'
		   AND ((con.confdelsetcols IS NULL AND EXISTS (
		       SELECT 1 FROM pg_attribute a
		        WHERE a.attrelid=con.conrelid AND a.attnum=ANY(con.conkey)
		          AND a.attname='team_id')) OR EXISTS (
		       SELECT 1 FROM pg_attribute a
		        WHERE a.attrelid=con.conrelid AND a.attnum=ANY(con.confdelsetcols)
		          AND a.attname='team_id'))`).Scan(&unsafeSetNull); err != nil {
		t.Fatalf("count SET NULL: %v", err)
	}
	if unsafeSetNull != 0 {
		t.Errorf("found %d SET NULL FKs that can clear team_id", unsafeSetNull)
	}

	// (3) Every team table must have a root FK (team_id) -> teams(id).
	assertRootFKs(ctx, t, pool)

	// (4) Every non-root business FK must include team_id.
	assertBusinessFKsCarryTeamID(ctx, t, pool)

	// (5) Cross-team child insert rejected; same-team self-ref works.
	assertCrossTeamFKRejected(ctx, t, pool)
}

// TestRuntimeResourceIDsRemainGlobalUUIDKeys protects the identity contract
// used by process-local registries and filesystem/runtime resources. These
// resources are keyed by their globally unique database IDs; team_id is an
// isolation qualifier and must not replace or join the stable primary key.
func TestRuntimeResourceIDsRemainGlobalUUIDKeys(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)

	tables := []string{
		"bot_channel_configs",
		"bot_channel_routes",
		"bot_sessions",
		"bots",
		"channel_identities",
		"containers",
		"fetch_providers",
		"mcp_connections",
		"models",
		"providers",
		"schedule",
		"search_providers",
		"storage_providers",
	}
	for _, table := range tables {
		t.Run(table, func(t *testing.T) {
			rows, err := pool.Query(ctx, `
				SELECT a.attname,
				       pg_catalog.format_type(a.atttypid, a.atttypmod),
				       COALESCE(pg_catalog.pg_get_expr(d.adbin, d.adrelid), '')
				  FROM pg_constraint con
				  JOIN LATERAL unnest(con.conkey) WITH ORDINALITY key(attnum, ord) ON true
				  JOIN pg_attribute a
				    ON a.attrelid = con.conrelid AND a.attnum = key.attnum
				  LEFT JOIN pg_attrdef d
				    ON d.adrelid = a.attrelid AND d.adnum = a.attnum
				 WHERE con.contype = 'p'
				   AND con.conrelid = ('public.' || quote_ident($1))::regclass
				 ORDER BY key.ord`, table)
			if err != nil {
				t.Fatalf("query primary key: %v", err)
			}
			defer rows.Close()

			type keyColumn struct {
				name, dataType, defaultExpr string
			}
			var columns []keyColumn
			for rows.Next() {
				var column keyColumn
				if err := rows.Scan(&column.name, &column.dataType, &column.defaultExpr); err != nil {
					t.Fatalf("scan primary key: %v", err)
				}
				columns = append(columns, column)
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("iterate primary key: %v", err)
			}
			if len(columns) != 1 || columns[0].name != "id" || columns[0].dataType != "uuid" {
				t.Fatalf("runtime resource primary key = %#v, want single UUID id", columns)
			}
			if columns[0].defaultExpr != "gen_random_uuid()" {
				t.Fatalf("runtime resource id default = %q, want gen_random_uuid()", columns[0].defaultExpr)
			}
		})
	}
}

func assertRootFKs(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT c.relname FROM pg_class c
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE c.relkind = 'r' AND n.nspname = 'public'
		   AND c.relname NOT IN ('schema_migrations', 'teams', 'users')`)
	if err != nil {
		t.Fatalf("enumerate tables: %v", err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()

	for _, tbl := range tables {
		var hasRootFK bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_constraint con
				  JOIN pg_class rt ON rt.oid = con.confrelid
				  JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = ANY(con.conkey)
				 WHERE con.contype = 'f'
				   AND con.conrelid = ('public.'||quote_ident($1))::regclass
				   AND rt.relname = 'teams'
				   AND a.attname = 'team_id'
			)`, tbl).Scan(&hasRootFK); err != nil {
			t.Fatalf("check root FK on %s: %v", tbl, err)
		}
		if !hasRootFK {
			t.Errorf("team table %s is missing a root FK (team_id) -> teams(id)", tbl)
		}
	}
}

func assertBusinessFKsCarryTeamID(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	// Every FK from a team table to another team table (parent != teams)
	// must include the team_id column in its key.
	rows, err := pool.Query(ctx, `
		SELECT c.relname, con.conname, rt.relname AS parent
		  FROM pg_constraint con
		  JOIN pg_class c ON c.oid = con.conrelid
		  JOIN pg_class rt ON rt.oid = con.confrelid
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		  JOIN pg_namespace rn ON rn.oid = rt.relnamespace
		 WHERE con.contype = 'f' AND n.nspname = 'public'
		   AND rn.nspname = 'public'
		   AND rt.relname NOT IN ('teams', 'users')
		   AND NOT EXISTS (
		       SELECT 1 FROM pg_attribute a
		        WHERE a.attrelid = con.conrelid AND a.attnum = ANY(con.conkey)
		          AND a.attname = 'team_id')`)
	if err != nil {
		t.Fatalf("enumerate business FKs: %v", err)
	}
	for rows.Next() {
		var tbl, fk, parent string
		_ = rows.Scan(&tbl, &fk, &parent)
		t.Errorf("business FK %s on %s -> %s does not include team_id", fk, tbl, parent)
	}
	rows.Close()
}

func assertCrossTeamFKRejected(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	const t1 = "00000000-0000-0000-0000-000000000001"
	const t2 = "00000000-0000-0000-0000-0000000000f2"
	if _, err := pool.Exec(ctx, `INSERT INTO teams (id, slug) VALUES ($1, 'ct2')`, t2); err != nil {
		t.Fatalf("seed team2: %v", err)
	}
	// Create a provider in t1, then try to create a model in t2 that references
	// it — must fail, because the composite FK (team_id, provider_id) ->
	// providers(team_id, id) cannot match across teams.
	var providerID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO providers (team_id, name, client_type) VALUES ($1, 'p', 'openai-completions') RETURNING id`, t1,
	).Scan(&providerID); err != nil {
		t.Fatalf("insert provider t1: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO models (team_id, provider_id, model_id, type) VALUES ($1, $2, 'm', 'chat')`, t2, providerID,
	); sqlState(err) != "23503" {
		t.Fatalf("cross-team model->provider FK must be rejected (23503), got %q", sqlState(err))
	}
	// Same-team reference succeeds.
	if _, err := pool.Exec(ctx,
		`INSERT INTO models (team_id, provider_id, model_id, type) VALUES ($1, $2, 'm', 'chat')`, t1, providerID,
	); err != nil {
		t.Fatalf("same-team model->provider must succeed: %v", err)
	}
}
