//go:build integration

package db_test

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestOpenCodeGoMigrationRollbackPreservesProviders(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	const otherTeam = "00000000-0000-0000-0000-000000000002"
	if _, err := pool.Exec(ctx, `
		INSERT INTO teams (id, slug) VALUES ($1, 'other');
	`, otherTeam); err != nil {
		t.Fatalf("create other team: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO providers (name, client_type, team_id) VALUES
		('legacy', 'openai-completions', '00000000-0000-0000-0000-000000000001'),
		('go', 'opencode-go', $1)
	`, otherTeam); err != nil {
		t.Fatalf("create providers: %v", err)
	}

	role := fmt.Sprintf("opencode_migration_%d_%d", os.Getpid(), teamTestDBSeq.Add(1))
	quotedRole := pgx.Identifier{role}.Sanitize()
	if _, err := pool.Exec(ctx, "CREATE ROLE "+quotedRole+" NOLOGIN NOSUPERUSER NOCREATEROLE NOBYPASSRLS"); err != nil {
		t.Fatalf("create migration role: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "REASSIGN OWNED BY "+quotedRole+" TO "+pgx.Identifier{pool.Config().ConnConfig.User}.Sanitize())
		_, _ = pool.Exec(ctx, "DROP OWNED BY "+quotedRole)
		_, _ = pool.Exec(ctx, "DROP ROLE "+quotedRole)
	})
	if _, err := pool.Exec(ctx, "ALTER TABLE public.providers OWNER TO "+quotedRole); err != nil {
		t.Fatalf("assign table owner: %v", err)
	}
	if _, err := pool.Exec(ctx, "GRANT USAGE ON SCHEMA public TO "+quotedRole); err != nil {
		t.Fatalf("grant schema access: %v", err)
	}
	conn, err := pgx.Connect(ctx, teamMigrationDSN(t))
	if err != nil {
		t.Fatalf("connect migration role: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "SET ROLE "+quotedRole); err != nil {
		t.Fatalf("set migration role: %v", err)
	}
	down, err := fs.ReadFile(postgresMigrationsFS(t), "0160_opencode_go.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, err := fs.ReadFile(postgresMigrationsFS(t), "0160_opencode_go.up.sql")
	if err != nil {
		t.Fatal(err)
	}

	assertState := func(wantGo bool, wantProviders int) {
		t.Helper()
		var enabled, forced, validated bool
		var definition string
		if err := pool.QueryRow(ctx, `
			SELECT c.relrowsecurity, c.relforcerowsecurity, k.convalidated,
			       pg_get_constraintdef(k.oid)
			FROM pg_class c JOIN pg_constraint k ON k.conrelid = c.oid
			WHERE c.oid = 'public.providers'::regclass
			  AND k.conname = 'providers_client_type_check'
		`).Scan(&enabled, &forced, &validated, &definition); err != nil {
			t.Fatalf("inspect provider constraint: %v", err)
		}
		if !enabled || !forced || !validated || strings.Contains(definition, "opencode-go") != wantGo {
			t.Fatalf("unexpected provider constraint/RLS state: enabled=%t forced=%t validated=%t constraint=%s", enabled, forced, validated, definition)
		}
		var count int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM providers").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != wantProviders {
			t.Fatalf("providers = %d, want %d", count, wantProviders)
		}
	}

	// No tenant context is available during migration. A Go provider belonging
	// to another team must still block rollback, without weakening RLS or losing
	// the original constraint when its replacement fails validation.
	if _, err := conn.Exec(ctx, string(down)); sqlState(err) != "23514" {
		t.Fatalf("rollback with Go provider: got %v, want check violation", err)
	}
	if _, err := conn.Exec(ctx, "ROLLBACK"); err != nil {
		t.Fatalf("end failed rollback transaction: %v", err)
	}
	assertState(true, 2)

	if _, err := pool.Exec(ctx, "DELETE FROM providers WHERE client_type = 'opencode-go'"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := conn.Exec(ctx, string(down)); err != nil {
			t.Fatalf("rollback with only legacy providers: %v", err)
		}
		assertState(false, 1)
	}
	if _, err := conn.Exec(ctx, string(up)); err != nil {
		t.Fatalf("upgrade again: %v", err)
	}
	assertState(true, 1)
}
