//go:build integration

package db_test

import (
	"context"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func TestSessionModelPreferenceWriteFenceAndMigration(t *testing.T) {
	ctx := context.Background()
	pool := resetToEmpty(t)
	// A minimal pre-0146 fixture makes row retention and the upgrade delta
	// explicit; the separate canonical/team tests exercise the full schema.
	_, err := pool.Exec(ctx, `
 CREATE FUNCTION public.memoh_current_team_id() RETURNS uuid LANGUAGE sql AS $$ SELECT '00000000-0000-0000-0000-000000000001'::uuid $$;
 CREATE TABLE models(team_id uuid NOT NULL,id uuid PRIMARY KEY,UNIQUE(team_id,id));
 CREATE TABLE bot_sessions(id uuid PRIMARY KEY,team_id uuid NOT NULL,runtime_type text NOT NULL,title text,updated_at timestamptz NOT NULL,deleted_at timestamptz);
 INSERT INTO bot_sessions VALUES ('00000000-0000-0000-0000-000000000021','00000000-0000-0000-0000-000000000001','codex','legacy keep','2026-01-01Z',NULL);
 `)
	if err != nil {
		t.Fatal(err)
	}
	upBytes, err := fs.ReadFile(postgresMigrationsFS(t), "0146_session_model_preference.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	downBytes, err := fs.ReadFile(postgresMigrationsFS(t), "0146_session_model_preference.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, down := string(upBytes), string(downBytes)
	for n := 0; n < 2; n++ {
		if _, err = pool.Exec(ctx, up); err != nil {
			t.Fatal(err)
		}
	}
	var id pgtype.UUID
	_ = id.Scan("00000000-0000-0000-0000-000000000021")
	old, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Rollback(ctx)
	var before pgtype.UUID
	if err = old.QueryRow(ctx, "SELECT model_preference_revision FROM bot_sessions WHERE id=$1", id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	q := sqlc.New(pool)
	send := sqlc.UpdateSessionModelPreferenceParams{ID: id, PreferredExternalModelID: pgtype.Text{String: "newer", Valid: true}, PreferredReasoningEffort: pgtype.Text{String: "high", Valid: true}}
	if err = q.UpdateSessionModelPreference(ctx, send); err != nil {
		t.Fatal(err)
	}
	stale := sqlc.CompareAndSetSessionModelPreferenceParams{ID: id, RuntimeType: "codex", ExpectedRevision: before, PreferredExternalModelID: pgtype.Text{String: "older", Valid: true}}
	n, err := sqlc.New(old).CompareAndSetSessionModelPreference(ctx, stale)
	if err != nil || n != 0 {
		t.Fatalf("stale PATCH updated %d rows: %v", n, err)
	}
	if err = old.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var firstRevision pgtype.UUID
	if err = pool.QueryRow(ctx, "SELECT model_preference_revision FROM bot_sessions WHERE id=$1", id).Scan(&firstRevision); err != nil {
		t.Fatal(err)
	}
	if err = q.UpdateSessionModelPreference(ctx, send); err != nil {
		t.Fatal(err)
	}
	stale.ExpectedRevision = firstRevision
	n, err = q.CompareAndSetSessionModelPreference(ctx, stale)
	if err != nil || n != 0 {
		t.Fatalf("identical send did not advance fence: %d %v", n, err)
	}
	var current pgtype.UUID
	var model, effort string
	if err = pool.QueryRow(ctx, "SELECT model_preference_revision,preferred_external_model_id,preferred_reasoning_effort FROM bot_sessions WHERE id=$1", id).Scan(&current, &model, &effort); err != nil {
		t.Fatal(err)
	}
	if model != "newer" || effort != "high" {
		t.Fatalf("pair=%s/%s", model, effort)
	}
	stale.ExpectedRevision = current
	n, err = q.CompareAndSetSessionModelPreference(ctx, stale)
	if err != nil || n != 1 {
		t.Fatalf("fresh PATCH rejected: %d %v", n, err)
	}
	for i := 0; i < 2; i++ {
		if _, err = pool.Exec(ctx, down); err != nil {
			t.Fatal(err)
		}
	}
	var intact bool
	if err = pool.QueryRow(ctx, "SELECT title='legacy keep' AND updated_at='2026-01-01Z' FROM bot_sessions WHERE id=$1", id).Scan(&intact); err != nil || !intact {
		t.Fatalf("legacy row changed: %v %v", intact, err)
	}
	if _, err = pool.Exec(ctx, up); err != nil {
		t.Fatal(err)
	}
	// Changing an empty session's runtime must not carry model IDs into a
	// different Agent namespace. Execute the actual sqlc source query.
	if _, err = pool.Exec(ctx, `ALTER TABLE bot_sessions ADD COLUMN type text, ADD COLUMN session_mode text,
 ADD COLUMN bot_agent_id uuid, ADD COLUMN runtime_metadata jsonb, ADD COLUMN metadata jsonb,
 ADD COLUMN runtime_config_epoch bigint NOT NULL DEFAULT 0`); err != nil {
		t.Fatal(err)
	}
	if err = q.UpdateSessionModelPreference(ctx, send); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("../../db/postgres/queries/sessions.sql")
	if err != nil {
		t.Fatal(err)
	}
	query := strings.SplitN(strings.SplitN(string(source), "-- name: UpdateSessionTypeAndMetadata :one", 2)[1], "-- name:", 2)[0]
	query = strings.NewReplacer("sqlc.arg(id)", "$1", "sqlc.arg(type)", "$2", "sqlc.arg(session_mode)", "$3",
		"sqlc.arg(runtime_type)", "$4", "sqlc.arg(bot_agent_id)", "$5", "sqlc.arg(runtime_metadata)", "$6", "sqlc.arg(metadata)", "$7").Replace(query)
	if _, err = pool.Exec(ctx, query, id, "chat", "chat", "codex", nil, "{}", "{}"); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, "SELECT preferred_external_model_id FROM bot_sessions WHERE id=$1", id).Scan(&model); err != nil || model != "newer" {
		t.Fatalf("same runtime lost preference: %q %v", model, err)
	}
	if _, err = pool.Exec(ctx, query, id, "chat", "chat", "claudecode", nil, "{}", "{}"); err != nil {
		t.Fatal(err)
	}
	var cleared bool
	if err = pool.QueryRow(ctx, "SELECT preferred_chat_model_id IS NULL AND preferred_external_model_id IS NULL AND preferred_reasoning_effort IS NULL FROM bot_sessions WHERE id=$1", id).Scan(&cleared); err != nil || !cleared {
		t.Fatalf("runtime switch retained old namespace: %v %v", cleared, err)
	}
}
