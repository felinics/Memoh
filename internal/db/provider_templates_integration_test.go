//go:build integration

package db_test

import (
	"context"
	"io/fs"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/team"
)

func TestProviderTemplateMigrationUpgradesWithoutBackfill(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	dsn := pool.Config().ConnString()
	steps := countMigrationsFromProviderTemplates(t)
	if steps < 1 {
		t.Fatal("provider template migration 0114 is missing")
	}

	stepDown(t, dsn, steps)

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	if _, err := conn.Exec(ctx, "SELECT set_config('memoh.team_id', $1, false)", team.DefaultTeamID); err != nil {
		conn.Release()
		t.Fatalf("set team context: %v", err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO public.providers (name, client_type, config, metadata)
		VALUES ('Existing Provider', 'openai-completions', '{}'::jsonb, '{}'::jsonb)
	`); err != nil {
		conn.Release()
		t.Fatalf("seed existing provider: %v", err)
	}
	conn.Release()

	stepUp(t, dsn, steps)

	var providerCount, linkedProviderCount, templateCount int
	if err := pool.QueryRow(ctx, `SELECT count(*), count(provider_template_id) FROM public.providers`).Scan(&providerCount, &linkedProviderCount); err != nil {
		t.Fatalf("inspect upgraded providers: %v", err)
	}
	if providerCount != 1 || linkedProviderCount != 0 {
		t.Fatalf("upgraded providers = %d, linked = %d; existing rows must remain untouched", providerCount, linkedProviderCount)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM template.provider_templates`).Scan(&templateCount); err != nil {
		t.Fatalf("count provider templates: %v", err)
	}
	if templateCount != 0 {
		t.Fatalf("migration must not backfill provider templates, got %d rows", templateCount)
	}

	var providerTemplateIndexColumns []string
	if err := pool.QueryRow(ctx, `
		SELECT array_agg(attr.attname ORDER BY key.ord)
		  FROM pg_index idx
		  JOIN LATERAL unnest(idx.indkey) WITH ORDINALITY key(attnum, ord) ON true
		  JOIN pg_attribute attr ON attr.attrelid = idx.indrelid AND attr.attnum = key.attnum
		 WHERE idx.indexrelid = 'public.idx_providers_provider_template_id'::regclass
	`).Scan(&providerTemplateIndexColumns); err != nil {
		t.Fatalf("inspect provider template index: %v", err)
	}
	if len(providerTemplateIndexColumns) != 2 || providerTemplateIndexColumns[0] != "team_id" || providerTemplateIndexColumns[1] != "provider_template_id" {
		t.Fatalf("provider template index columns = %v, want [team_id provider_template_id]", providerTemplateIndexColumns)
	}

	var searchTypeUnique bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1
		    FROM pg_constraint con
		   WHERE con.conrelid = 'public.search_providers'::regclass
		     AND con.contype = 'u'
		     AND ARRAY(
		       SELECT attr.attname
		         FROM unnest(con.conkey) WITH ORDINALITY key(attnum, ord)
		         JOIN pg_attribute attr ON attr.attrelid = con.conrelid AND attr.attnum = key.attnum
		        ORDER BY key.ord
		     ) = ARRAY['team_id', 'provider']::name[]
		)
	`).Scan(&searchTypeUnique); err != nil {
		t.Fatalf("inspect search provider uniqueness: %v", err)
	}
	if !searchTypeUnique {
		t.Fatal("incremental migration must add per-team search provider type uniqueness")
	}
}

func countMigrationsFromProviderTemplates(t *testing.T) int {
	t.Helper()
	entries, err := fs.ReadDir(postgresMigrationsFS(t), ".")
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	count := 0
	for _, entry := range entries {
		if entry.Name() >= "0114_" && strings.HasSuffix(entry.Name(), ".up.sql") {
			count++
		}
	}
	return count
}

func TestProviderTemplateSchemaIsGlobal(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)

	for _, table := range []string{"provider_templates", "provider_template_models"} {
		t.Run(table, func(t *testing.T) {
			var exists, hasTeamID, rowSecurity, forceRowSecurity bool
			if err := pool.QueryRow(ctx, `
				SELECT to_regclass('template.' || quote_ident($1)) IS NOT NULL,
				       EXISTS (
				         SELECT 1
				           FROM information_schema.columns
				          WHERE table_schema = 'template'
				            AND table_name = $1
				            AND column_name = 'team_id'
				       ),
				       COALESCE((SELECT relrowsecurity FROM pg_class WHERE oid = to_regclass('template.' || quote_ident($1))), false),
				       COALESCE((SELECT relforcerowsecurity FROM pg_class WHERE oid = to_regclass('template.' || quote_ident($1))), false)
			`, table).Scan(&exists, &hasTeamID, &rowSecurity, &forceRowSecurity); err != nil {
				t.Fatalf("inspect template table: %v", err)
			}
			if !exists {
				t.Fatalf("template.%s does not exist", table)
			}
			if hasTeamID {
				t.Errorf("template.%s must not contain team_id", table)
			}
			if rowSecurity || forceRowSecurity {
				t.Errorf("template.%s must remain a global catalog without tenant RLS", table)
			}
		})
	}
}

func TestProviderInstancesLinkToGlobalTemplates(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)

	for _, table := range []string{"providers"} {
		t.Run(table, func(t *testing.T) {
			var count int
			if err := pool.QueryRow(ctx, `
				SELECT count(*)
				  FROM pg_constraint con
				  JOIN pg_class child ON child.oid = con.conrelid
				  JOIN pg_namespace child_ns ON child_ns.oid = child.relnamespace
				  JOIN pg_class parent ON parent.oid = con.confrelid
				  JOIN pg_namespace parent_ns ON parent_ns.oid = parent.relnamespace
				 WHERE con.contype = 'f'
				   AND child_ns.nspname = 'public'
				   AND child.relname = $1
				   AND parent_ns.nspname = 'template'
				   AND parent.relname = 'provider_templates'
				   AND array_length(con.conkey, 1) = 1
			`, table).Scan(&count); err != nil {
				t.Fatalf("inspect provider template link: %v", err)
			}
			if count != 1 {
				t.Errorf("public.%s must have exactly one single-column link to template.provider_templates, got %d", table, count)
			}

			var setNullColumns []string
			if err := pool.QueryRow(ctx, `
				SELECT ARRAY(
				  SELECT attr.attname
				    FROM unnest(con.confdelsetcols) WITH ORDINALITY key(attnum, ord)
				    JOIN pg_attribute attr
				      ON attr.attrelid = con.conrelid
				     AND attr.attnum = key.attnum
				   ORDER BY key.ord
				)
				  FROM pg_constraint con
				 WHERE con.conrelid = 'public.providers'::regclass
				   AND con.conname = 'providers_provider_template_id_fkey'
				   AND con.confdeltype = 'n'
			`).Scan(&setNullColumns); err != nil {
				t.Fatalf("inspect provider template delete action: %v", err)
			}
			if len(setNullColumns) != 1 || setNullColumns[0] != "provider_template_id" {
				t.Fatalf("provider template SET NULL columns = %v, want [provider_template_id]", setNullColumns)
			}
		})
	}

	for _, table := range []string{"fetch_providers", "memory_providers", "email_providers"} {
		t.Run(table+"_has_no_template_link", func(t *testing.T) {
			var hasColumn bool
			if err := pool.QueryRow(ctx, `
				SELECT EXISTS (
				  SELECT 1
				    FROM information_schema.columns
				   WHERE table_schema = 'public'
				     AND table_name = $1
				     AND column_name = 'provider_template_id'
				)
			`, table).Scan(&hasColumn); err != nil {
				t.Fatalf("inspect provider template column: %v", err)
			}
			if hasColumn {
				t.Errorf("public.%s must not link to the backend provider template catalog", table)
			}
		})
	}
}

func TestSearchProviderTypeIsUniquePerTeam(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)

	var columns []string
	rows, err := pool.Query(ctx, `
		SELECT a.attname
		  FROM pg_constraint con
		  JOIN LATERAL unnest(con.conkey) WITH ORDINALITY key(attnum, ord) ON true
		  JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = key.attnum
		 WHERE con.conrelid = 'public.search_providers'::regclass
		   AND con.contype = 'u'
		   AND ARRAY(
		     SELECT attr.attname
		       FROM unnest(con.conkey) WITH ORDINALITY inner_key(attnum, ord)
		       JOIN pg_attribute attr
		         ON attr.attrelid = con.conrelid
		        AND attr.attnum = inner_key.attnum
		      ORDER BY inner_key.ord
		   ) = ARRAY['team_id', 'provider']::name[]
		 ORDER BY key.ord
	`)
	if err != nil {
		t.Fatalf("inspect search provider uniqueness: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			t.Fatalf("scan search provider uniqueness: %v", err)
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate search provider uniqueness: %v", err)
	}
	if len(columns) != 2 || columns[0] != "team_id" || columns[1] != "provider" {
		t.Fatalf("search provider unique columns = %v, want [team_id provider]", columns)
	}
}
