package db

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// settingsTestTx opens a rolled-back transaction on an isolated schema with
// the 0001 baseline and team fixtures applied, mirroring the schema every
// settings query runs against.
func settingsTestTx(t *testing.T, ctx context.Context) pgx.Tx {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })

	schema := "settings_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := tx.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL search_path TO "+quotedSchema+", public"); err != nil {
		t.Fatalf("set search path: %v", err)
	}
	baseline := readEmbeddedPreTeamInit(t)
	if _, err := tx.Exec(ctx, baseline); err != nil {
		t.Fatalf("apply 0001 baseline: %v", err)
	}
	bindTeamQueryFixture(t, ctx, tx)
	if _, err := tx.Exec(ctx, `
ALTER TABLE users ADD COLUMN team_id UUID NOT NULL DEFAULT public.memoh_current_team_id();
ALTER TABLE bots
  ADD COLUMN team_id UUID NOT NULL DEFAULT public.memoh_current_team_id(),
  ADD COLUMN default_bot_agent_id UUID;
ALTER TABLE models ADD COLUMN team_id UUID NOT NULL DEFAULT public.memoh_current_team_id();
ALTER TABLE search_providers ADD COLUMN team_id UUID NOT NULL DEFAULT public.memoh_current_team_id();
ALTER TABLE fetch_providers ADD COLUMN team_id UUID NOT NULL DEFAULT public.memoh_current_team_id();
ALTER TABLE memory_providers ADD COLUMN team_id UUID NOT NULL DEFAULT public.memoh_current_team_id();
`); err != nil {
		t.Fatalf("team-scope settings tables: %v", err)
	}
	return tx
}

func settingsTestUpsertParams(botID uuid.UUID) sqlc.UpsertBotSettingsParams {
	return sqlc.UpsertBotSettingsParams{
		Language:           "en",
		ReasoningEffort:    "medium",
		ChatRuntime:        "model",
		ChatAcpProjectMode: "project",
		ToolApprovalConfig: []byte(`{}`),
		OverlayProvider:    "",
		OverlayConfig:      []byte(`{}`),
		CommandUiLanguage:  "",
		ID:                 pgtype.UUID{Bytes: botID, Valid: true},
	}
}

// TestUpsertBotSettingsClearsCompactionModelOverride pins the explicit-set
// semantics of the compaction model override: submitting the field with a
// NULL value must clear the override back to chat-model inheritance, while
// omitting the field must leave the stored override untouched.
func TestUpsertBotSettingsClearsCompactionModelOverride(t *testing.T) {
	ctx := context.Background()
	tx := settingsTestTx(t, ctx)

	var userID, botID, providerID, modelID uuid.UUID
	if err := tx.QueryRow(ctx, `
INSERT INTO users (username, is_active, metadata) VALUES ('override-owner', true, '{}') RETURNING id
`).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := tx.QueryRow(ctx, `
INSERT INTO providers (name, config) VALUES ('override-provider', '{}') RETURNING id
`).Scan(&providerID); err != nil {
		t.Fatalf("insert provider: %v", err)
	}
	if err := tx.QueryRow(ctx, `
INSERT INTO models (provider_id, model_id, name, type) VALUES ($1, 'override/model', 'Override', 'chat') RETURNING id
`, providerID).Scan(&modelID); err != nil {
		t.Fatalf("insert model: %v", err)
	}
	if err := tx.QueryRow(ctx, `
INSERT INTO bots (owner_user_id, name, type, status, metadata, compaction_model_id)
VALUES ($1, 'override-bot', 'personal', 'ready', '{}', $2) RETURNING id
`, userID, modelID).Scan(&botID); err != nil {
		t.Fatalf("insert bot: %v", err)
	}

	queries := sqlc.New(tx)
	params := settingsTestUpsertParams(botID)

	// Omitting the override (set=false) keeps the stored model.
	if _, err := queries.UpsertBotSettings(ctx, params); err != nil {
		t.Fatalf("upsert without touching override: %v", err)
	}
	var stored *string
	if err := tx.QueryRow(ctx, `SELECT compaction_model_id::text FROM bots WHERE id = $1`, botID).Scan(&stored); err != nil {
		t.Fatalf("read override: %v", err)
	}
	if stored == nil || *stored != modelID.String() {
		t.Fatalf("override after untouched upsert = %v, want %s", stored, modelID)
	}

	// Submitting the field with NULL clears the override back to inheritance.
	params.CompactionModelIDSet = true
	if _, err := queries.UpsertBotSettings(ctx, params); err != nil {
		t.Fatalf("upsert clearing override: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT compaction_model_id::text FROM bots WHERE id = $1`, botID).Scan(&stored); err != nil {
		t.Fatalf("read cleared override: %v", err)
	}
	if stored != nil {
		t.Fatalf("override after explicit clear = %v, want NULL so the bot inherits the chat model", *stored)
	}
}

func TestUpsertBotSettingsCompactionTargetPercentExplicitSet(t *testing.T) {
	ctx := context.Background()
	tx := settingsTestTx(t, ctx)

	var userID, botID uuid.UUID
	if err := tx.QueryRow(ctx, `
INSERT INTO users (username, is_active, metadata)
VALUES ('target-percent-owner', true, '{}')
RETURNING id
`).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := tx.QueryRow(ctx, `
INSERT INTO bots (
  owner_user_id,
  name,
  type,
  status,
  metadata,
  compaction_target_percent
)
VALUES ($1, 'target-percent-bot', 'personal', 'ready', '{}', 55)
RETURNING id
`, userID).Scan(&botID); err != nil {
		t.Fatalf("insert bot: %v", err)
	}

	queries := sqlc.New(tx)
	params := settingsTestUpsertParams(botID)

	if _, err := queries.UpsertBotSettings(ctx, params); err != nil {
		t.Fatalf("upsert preserving target: %v", err)
	}
	assertCompactionTargetPercent(t, ctx, tx, botID, settingsIntPointer(55))

	params.CompactionTargetPercentSet = true
	params.CompactionTargetPercent = pgtype.Int4{Int32: 30, Valid: true}
	if _, err := queries.UpsertBotSettings(ctx, params); err != nil {
		t.Fatalf("upsert setting target: %v", err)
	}
	assertCompactionTargetPercent(t, ctx, tx, botID, settingsIntPointer(30))

	params.CompactionTargetPercent = pgtype.Int4{}
	if _, err := queries.UpsertBotSettings(ctx, params); err != nil {
		t.Fatalf("upsert clearing target: %v", err)
	}
	assertCompactionTargetPercent(t, ctx, tx, botID, nil)
}

func assertCompactionTargetPercent(t *testing.T, ctx context.Context, tx pgx.Tx, botID uuid.UUID, want *int) {
	t.Helper()
	var got *int
	if err := tx.QueryRow(ctx, `SELECT compaction_target_percent FROM bots WHERE id = $1`, botID).Scan(&got); err != nil {
		t.Fatalf("read compaction target: %v", err)
	}
	if want == nil {
		if got != nil {
			t.Fatalf("compaction target = %d, want NULL", *got)
		}
		return
	}
	if got == nil || *got != *want {
		t.Fatalf("compaction target = %v, want %d", got, *want)
	}
}

func settingsIntPointer(value int) *int {
	return &value
}

// TestNewBotDefaultsToCompactionEnabled pins the zero-config default: a
// freshly created bot compacts out of the box, and resetting settings
// returns to that default rather than to the legacy opt-in.
func TestNewBotDefaultsToCompactionEnabled(t *testing.T) {
	ctx := context.Background()
	tx := settingsTestTx(t, ctx)

	var userID, botID uuid.UUID
	if err := tx.QueryRow(ctx, `
INSERT INTO users (username, is_active, metadata) VALUES ('default-on-owner', true, '{}') RETURNING id
`).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := tx.QueryRow(ctx, `
INSERT INTO bots (owner_user_id, name, type, status, metadata)
VALUES ($1, 'default-on-bot', 'personal', 'ready', '{}') RETURNING id
`, userID).Scan(&botID); err != nil {
		t.Fatalf("insert bot: %v", err)
	}

	var enabled bool
	var threshold int
	var target *int
	if err := tx.QueryRow(ctx, `SELECT compaction_enabled, compaction_threshold, compaction_target_percent FROM bots WHERE id = $1`, botID).Scan(&enabled, &threshold, &target); err != nil {
		t.Fatalf("read defaults: %v", err)
	}
	if !enabled || threshold != 0 || target != nil {
		t.Fatalf("new bot defaults = enabled %t threshold %d target %v, want automatic 50%% trigger and 40%% target defaults", enabled, threshold, target)
	}

	if _, err := tx.Exec(ctx, `UPDATE bots SET compaction_enabled = false, compaction_target_percent = 70 WHERE id = $1`, botID); err != nil {
		t.Fatalf("disable compaction: %v", err)
	}
	queries := sqlc.New(tx)
	if err := queries.DeleteSettingsByBotID(ctx, pgtype.UUID{Bytes: botID, Valid: true}); err != nil {
		t.Fatalf("reset settings: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT compaction_enabled, compaction_threshold, compaction_target_percent FROM bots WHERE id = $1`, botID).Scan(&enabled, &threshold, &target); err != nil {
		t.Fatalf("read reset values: %v", err)
	}
	if !enabled || threshold != 0 || target != nil {
		t.Fatalf("reset settings = enabled %t threshold %d target %v, want the same defaults a new bot gets", enabled, threshold, target)
	}
}
