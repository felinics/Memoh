//go:build integration

package db_test

import (
	"context"
	"io/fs"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/team"
)

// 0146 adds the session (model, effort) pair columns. The chain must step
// over it in both directions, the canonical init must already contain it, and
// the sqlc write queries must implement the revision fence and the
// runtime-switch clearing against the real schema (constraints included).
func TestSessionModelPreferenceMigrationAndWriteFence(t *testing.T) {
	ctx := context.Background()
	dsn := teamMigrationDSN(t)
	pool := freshMigratedDB(t)

	t.Run("chain is reversible and 0146 is idempotent", func(t *testing.T) {
		assertPreferenceColumns(t, ctx, pool, true)
		migrateTo(t, dsn, 145)
		assertPreferenceColumns(t, ctx, pool, false)
		migrateUpAll(t, dsn)
		assertPreferenceColumns(t, ctx, pool, true)
		up, err := fs.ReadFile(postgresMigrationsFS(t), "0146_session_model_preference.up.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(up)); err != nil {
			t.Fatalf("re-applying 0146 up: %v", err)
		}
		assertPreferenceColumns(t, ctx, pool, true)
	})

	t.Run("write fence and runtime-switch clearing on the real schema", func(t *testing.T) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, "SELECT set_config('memoh.team_id', $1, true)", team.DefaultTeamID); err != nil {
			t.Fatal(err)
		}
		var userID, providerID, modelID, botID pgtype.UUID
		mustScan(t, tx.QueryRow(ctx, `INSERT INTO users (username) VALUES ('pref-owner') RETURNING id`), &userID)
		if _, err := tx.Exec(ctx, `INSERT INTO team_members (team_id, user_id) VALUES ($1, $2)`, team.DefaultTeamID, userID); err != nil {
			t.Fatal(err)
		}
		mustScan(t, tx.QueryRow(ctx, `INSERT INTO providers (name, config) VALUES ('pref-provider', '{}') RETURNING id`), &providerID)
		mustScan(t, tx.QueryRow(ctx, `INSERT INTO models (provider_id, model_id, name, type) VALUES ($1, 'pref/model', 'Pref', 'chat') RETURNING id`, providerID), &modelID)
		mustScan(t, tx.QueryRow(ctx, `INSERT INTO bots (team_id, owner_user_id, name) VALUES ($1, $2, 'pref-bot') RETURNING id`, team.DefaultTeamID, userID), &botID)
		var direct, native pgtype.UUID
		mustScan(t, tx.QueryRow(ctx, `INSERT INTO bot_sessions (team_id, bot_id, type, session_mode, runtime_type) VALUES ($1, $2, 'chat', 'chat', 'codex') RETURNING id`, team.DefaultTeamID, botID), &direct)
		mustScan(t, tx.QueryRow(ctx, `INSERT INTO bot_sessions (team_id, bot_id, type, session_mode, runtime_type) VALUES ($1, $2, 'chat', 'chat', 'model') RETURNING id`, team.DefaultTeamID, botID), &native)

		q := sqlc.New(tx)
		revisionOf := func(id pgtype.UUID) pgtype.UUID {
			var rev pgtype.UUID
			mustScan(t, tx.QueryRow(ctx, `SELECT model_preference_revision FROM bot_sessions WHERE id = $1`, id), &rev)
			return rev
		}
		text := func(v string) pgtype.Text { return pgtype.Text{String: v, Valid: v != ""} }

		// A send writes the pair unconditionally and rotates the revision.
		unset := revisionOf(direct)
		send := sqlc.UpdateSessionModelPreferenceParams{ID: direct, PreferredExternalModelID: text("newer"), PreferredReasoningEffort: text("high")}
		if err := q.UpdateSessionModelPreference(ctx, send); err != nil {
			t.Fatal(err)
		}
		first := revisionOf(direct)
		if !first.Valid || first == unset {
			t.Fatalf("send did not rotate the revision: %v -> %v", unset, first)
		}
		// A picker PATCH that read before that send must not win.
		stale := sqlc.CompareAndSetSessionModelPreferenceParams{ID: direct, RuntimeType: "codex", ExpectedRevision: unset, PreferredExternalModelID: text("older")}
		if n, err := q.CompareAndSetSessionModelPreference(ctx, stale); err != nil || n != 0 {
			t.Fatalf("stale PATCH updated %d rows: %v", n, err)
		}
		// An identical send still rotates, so a PATCH holding the previous
		// revision is fenced as well.
		if err := q.UpdateSessionModelPreference(ctx, send); err != nil {
			t.Fatal(err)
		}
		stale.ExpectedRevision = first
		if n, err := q.CompareAndSetSessionModelPreference(ctx, stale); err != nil || n != 0 {
			t.Fatalf("identical send did not advance the fence: %d %v", n, err)
		}
		var model, effort string
		mustScan(t, tx.QueryRow(ctx, `SELECT preferred_external_model_id, preferred_reasoning_effort FROM bot_sessions WHERE id = $1`, direct), &model, &effort)
		if model != "newer" || effort != "high" {
			t.Fatalf("pair = %s/%s", model, effort)
		}
		// A PATCH holding the current revision goes through.
		stale.ExpectedRevision = revisionOf(direct)
		if n, err := q.CompareAndSetSessionModelPreference(ctx, stale); err != nil || n != 1 {
			t.Fatalf("fresh PATCH rejected: %d %v", n, err)
		}

		// Changing runtime or Agent clears the pair (different model
		// namespace) and rotates the revision; the same runtime keeps it.
		switchTo := func(runtime string) sqlc.BotSession {
			row, err := q.UpdateSessionTypeAndMetadata(ctx, sqlc.UpdateSessionTypeAndMetadataParams{
				ID: direct, Type: "chat", SessionMode: "chat", RuntimeType: runtime, RuntimeMetadata: []byte("{}"), Metadata: []byte("{}"),
			})
			if err != nil {
				t.Fatalf("switch to %s: %v", runtime, err)
			}
			return row
		}
		before := revisionOf(direct)
		if row := switchTo("codex"); row.PreferredExternalModelID.String != "older" || row.ModelPreferenceRevision != before {
			t.Fatalf("same runtime lost preference or rotated: %+v", row)
		}
		row := switchTo("claude-code")
		if row.PreferredExternalModelID.Valid || row.PreferredChatModelID.Valid || row.PreferredReasoningEffort.Valid {
			t.Fatalf("runtime switch retained the old namespace: %+v", row)
		}
		if row.ModelPreferenceRevision == before {
			t.Fatal("runtime switch must invalidate pending picker writes")
		}

		// Native pair: deleting the model clears only the FK column. The
		// effort survives as a half pair, which the readers treat as no memory.
		if err := q.UpdateSessionModelPreference(ctx, sqlc.UpdateSessionModelPreferenceParams{ID: native, PreferredChatModelID: modelID, PreferredReasoningEffort: text("low")}); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM models WHERE id = $1`, modelID); err != nil {
			t.Fatal(err)
		}
		var nativeModel pgtype.UUID
		var nativeEffort pgtype.Text
		mustScan(t, tx.QueryRow(ctx, `SELECT preferred_chat_model_id, preferred_reasoning_effort FROM bot_sessions WHERE id = $1`, native), &nativeModel, &nativeEffort)
		if nativeModel.Valid || nativeEffort.String != "low" {
			t.Fatalf("expected SET NULL half pair, got %v/%v", nativeModel, nativeEffort)
		}
	})

	t.Run("canonical init contains the pair columns", func(t *testing.T) {
		dsn := teamMigrationDSN(t)
		pool := resetToEmpty(t)
		applyCanonicalInitOnly(t, dsn)
		assertPreferenceColumns(t, ctx, pool, true)
	})
}

func assertPreferenceColumns(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want bool) {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM information_schema.columns
WHERE table_schema = 'public' AND table_name = 'bot_sessions'
  AND column_name IN ('preferred_chat_model_id', 'preferred_reasoning_effort', 'preferred_external_model_id', 'model_preference_revision')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	var fk bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'bot_sessions_preferred_chat_model_id_fkey')`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if want && (n != 4 || !fk) {
		t.Fatalf("expected 4 preference columns with FK, got %d columns fk=%v", n, fk)
	}
	if !want && (n != 0 || fk) {
		t.Fatalf("expected preference columns absent, got %d columns fk=%v", n, fk)
	}
}

func mustScan(t *testing.T, row interface{ Scan(dest ...any) error }, dest ...any) {
	t.Helper()
	if err := row.Scan(dest...); err != nil {
		t.Fatal(err)
	}
}
