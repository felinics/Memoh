package db

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const listCompactionArtifactLineageBySessionSQL = `
SELECT c.id, c.bot_id
FROM bot_history_message_compacts c
JOIN bot_sessions owner_session
  ON owner_session.id = $1
 AND owner_session.bot_id = c.bot_id
 AND owner_session.team_id = c.team_id
WHERE c.team_id = public.memoh_current_team_id()
  AND c.session_id = owner_session.id
  AND c.compaction_epoch = owner_session.compaction_epoch
  AND (
    c.status = 'ok'
    OR EXISTS (
      SELECT 1
      FROM bot_history_message_compacts parent
      WHERE parent.team_id = c.team_id
        AND parent.bot_id = owner_session.bot_id
        AND parent.session_id = owner_session.id
        AND parent.compaction_epoch = owner_session.compaction_epoch
        AND parent.status = 'ok'
        AND parent.superseded_by = c.id
    )
  )
ORDER BY c.anchor_start_ms ASC, c.started_at ASC, c.id ASC
`

const listCompactionArtifactParentIDsBySuccessorSQL = `
SELECT parent.id
FROM bot_history_message_compacts parent
JOIN bot_sessions owner_session
  ON owner_session.id = $1::uuid
 AND owner_session.bot_id = $2
 AND owner_session.team_id = parent.team_id
WHERE parent.team_id = public.memoh_current_team_id()
  AND parent.superseded_by = $3
  AND parent.bot_id = owner_session.bot_id
  AND parent.session_id = owner_session.id
  AND parent.compaction_epoch = owner_session.compaction_epoch
  AND parent.status = 'ok'
  AND EXISTS (
    SELECT 1
    FROM bot_history_message_compacts successor
    WHERE successor.team_id = parent.team_id
      AND successor.id = parent.superseded_by
      AND successor.bot_id = owner_session.bot_id
      AND successor.session_id = owner_session.id
      AND successor.compaction_epoch = owner_session.compaction_epoch
  )
ORDER BY parent.id ASC
`

const clearHistoryBySessionSQL = `
WITH target_session AS MATERIALIZED (
  SELECT session.id
  FROM bot_sessions session
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = $1
  FOR UPDATE
),
invalidated_session AS (
  UPDATE bot_sessions session
  SET compaction_epoch = session.compaction_epoch + 1
  FROM target_session target
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = target.id
  RETURNING session.id
),
target_compaction_artifacts AS MATERIALIZED (
  SELECT compact.id
  FROM bot_history_message_compacts compact
  WHERE compact.team_id = public.memoh_current_team_id()
    AND compact.session_id = $1
    AND (SELECT count(*) FROM target_session) >= 0
  ORDER BY compact.id
  FOR UPDATE
),
deleted_compaction_artifacts AS (
  DELETE FROM bot_history_message_compacts AS compact
  USING target_compaction_artifacts target
  WHERE compact.team_id = public.memoh_current_team_id()
    AND compact.id = target.id
  RETURNING compact.id
),
target_messages AS MATERIALIZED (
  SELECT message.id
  FROM bot_history_messages message
  WHERE message.team_id = public.memoh_current_team_id()
    AND message.session_id = $1
    AND (SELECT count(*) FROM target_session) >= 0
    AND (SELECT count(*) FROM deleted_compaction_artifacts) >= 0
  ORDER BY message.id
  FOR UPDATE
)
DELETE FROM bot_history_messages AS message
USING target_messages target
WHERE message.team_id = public.memoh_current_team_id()
  AND message.id = target.id
`

const clearHistoryByBotSQL = `
WITH target_sessions AS MATERIALIZED (
  SELECT session.id
  FROM bot_sessions session
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.bot_id = $1
  ORDER BY session.id
  FOR UPDATE
),
invalidated_sessions AS (
  UPDATE bot_sessions session
  SET compaction_epoch = session.compaction_epoch + 1
  FROM target_sessions target
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = target.id
  RETURNING session.id
),
target_compaction_artifacts AS MATERIALIZED (
  SELECT compact.id
  FROM bot_history_message_compacts compact
  WHERE compact.team_id = public.memoh_current_team_id()
    AND compact.bot_id = $1
    AND (SELECT count(*) FROM target_sessions) >= 0
  ORDER BY compact.id
  FOR UPDATE
),
deleted_compaction_artifacts AS (
  DELETE FROM bot_history_message_compacts AS compact
  USING target_compaction_artifacts target
  WHERE compact.team_id = public.memoh_current_team_id()
    AND compact.id = target.id
  RETURNING compact.id
),
target_messages AS MATERIALIZED (
  SELECT message.id
  FROM bot_history_messages message
  WHERE message.team_id = public.memoh_current_team_id()
    AND message.bot_id = $1
    AND (SELECT count(*) FROM target_sessions) >= 0
    AND (SELECT count(*) FROM deleted_compaction_artifacts) >= 0
  ORDER BY message.id
  FOR UPDATE
)
DELETE FROM bot_history_messages AS message
USING target_messages target
WHERE message.team_id = public.memoh_current_team_id()
  AND message.id = target.id
`

const deleteCompactionLogsByBotSQL = `
WITH target_sessions AS MATERIALIZED (
  SELECT session.id
  FROM bot_sessions session
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.bot_id = $1
  ORDER BY session.id
  FOR UPDATE
),
invalidated_sessions AS (
  UPDATE bot_sessions session
  SET compaction_epoch = session.compaction_epoch + 1
  FROM target_sessions target
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = target.id
  RETURNING session.id
),
target_compaction_logs AS MATERIALIZED (
  SELECT compact.id
  FROM bot_history_message_compacts compact
  WHERE compact.team_id = public.memoh_current_team_id()
    AND compact.bot_id = $1
    AND (SELECT count(*) FROM target_sessions) >= 0
  ORDER BY compact.id
  FOR UPDATE
)
DELETE FROM bot_history_message_compacts AS compacts
USING target_compaction_logs target
WHERE compacts.team_id = public.memoh_current_team_id()
  AND compacts.id = target.id
`

func TestCompactionArtifactMigrationPostgresPath(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	schema := "compaction_artifact_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := tx.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL search_path TO "+quotedSchema); err != nil {
		t.Fatalf("set search path: %v", err)
	}
	bindTeamQueryFixture(t, ctx, tx)
	if _, err := tx.Exec(ctx, `
CREATE TABLE bot_sessions (
  id UUID PRIMARY KEY,
  bot_id UUID NOT NULL,
  team_id UUID NOT NULL DEFAULT public.memoh_current_team_id()
);

CREATE TABLE bot_history_message_compacts (
  id UUID PRIMARY KEY,
  bot_id UUID NOT NULL,
  team_id UUID NOT NULL DEFAULT public.memoh_current_team_id(),
  session_id UUID,
  status TEXT NOT NULL DEFAULT 'pending',
  summary TEXT NOT NULL DEFAULT '',
  message_count INTEGER NOT NULL DEFAULT 0,
  error_message TEXT NOT NULL DEFAULT '',
  usage JSONB,
  model_id UUID,
  started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  completed_at TIMESTAMPTZ
);

CREATE TABLE bot_history_messages (
  id UUID PRIMARY KEY,
  bot_id UUID NOT NULL,
  team_id UUID NOT NULL DEFAULT public.memoh_current_team_id(),
  session_id UUID,
  compact_id UUID REFERENCES bot_history_message_compacts(id) ON DELETE SET NULL,
  turn_visible BOOLEAN NOT NULL DEFAULT true,
  turn_id UUID,
  turn_position BIGINT,
  turn_message_seq BIGINT,
  turn_superseded_at TIMESTAMPTZ
);
`); err != nil {
		t.Fatalf("create pre-0108 compaction schema: %v", err)
	}

	legacyID := "00000000-0000-0000-0000-00000000c001"
	repairArtifactID := "00000000-0000-0000-0000-00000000c101"
	clearedArtifactID := "00000000-0000-0000-0000-00000000c201"
	activeID := "00000000-0000-0000-0000-00000000d001"
	parentOneID := "00000000-0000-0000-0000-00000000d002"
	parentTwoID := "00000000-0000-0000-0000-00000000d003"
	unlinkedID := "00000000-0000-0000-0000-00000000d004"
	foreignArtifactID := "00000000-0000-0000-0000-00000000d101"
	crossOwnerArtifactID := "00000000-0000-0000-0000-00000000d102"
	logOnlyArtifactID := "00000000-0000-0000-0000-00000000d201"
	botID := "00000000-0000-0000-0000-00000000b001"
	repairBotID := "00000000-0000-0000-0000-00000000b002"
	clearedBotID := "00000000-0000-0000-0000-00000000b003"
	foreignBotID := "00000000-0000-0000-0000-00000000b101"
	logOnlyBotID := "00000000-0000-0000-0000-00000000b201"
	sessionID := "00000000-0000-0000-0000-00000000e001"
	repairSessionID := "00000000-0000-0000-0000-00000000e002"
	clearedSessionID := "00000000-0000-0000-0000-00000000e003"
	foreignSessionID := "00000000-0000-0000-0000-00000000e101"
	if _, err := tx.Exec(ctx, `
	INSERT INTO bot_sessions (id, bot_id)
	VALUES ($1, $2), ($3, $4), ($5, $6), ($7, $8)
		`, sessionID, botID, foreignSessionID, foreignBotID, repairSessionID, repairBotID, clearedSessionID, clearedBotID); err != nil {
		t.Fatalf("insert sessions: %v", err)
	}
	if _, err := tx.Exec(ctx, `
	INSERT INTO bot_history_message_compacts (id, bot_id, session_id, status, summary, message_count)
	VALUES
	  ($1, $2, $3, 'ok', 'legacy summary', 1),
	  ($4, $5, $6, 'ok', 'unsafe hidden summary', 1),
	  ($7, $8, $9, 'ok', 'deleted secret survives', 1)
	`, legacyID, botID, sessionID, repairArtifactID, repairBotID, repairSessionID, clearedArtifactID, clearedBotID, clearedSessionID); err != nil {
		t.Fatalf("insert pre-0108 legacy artifact: %v", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO bot_history_messages (
  id, bot_id, session_id, compact_id,
  turn_visible, turn_id, turn_position, turn_message_seq, turn_superseded_at
)
	VALUES
	  ('00000000-0000-0000-0000-00000000a001', $1, $2, $3, true, '00000000-0000-0000-0000-00000000f001', 1, 1, NULL),
	  ('00000000-0000-0000-0000-00000000a002', $4, $5, $6, false, '00000000-0000-0000-0000-00000000f002', 1, 1, now()),
	  ('00000000-0000-0000-0000-00000000a003', $7, $8, $9, true, '00000000-0000-0000-0000-00000000f003', 1, 1, NULL)
	`, botID, sessionID, legacyID, repairBotID, repairSessionID, repairArtifactID, clearedBotID, clearedSessionID, clearedArtifactID); err != nil {
		t.Fatalf("insert legacy sources: %v", err)
	}
	if _, err := tx.Exec(ctx, `
	DELETE FROM bot_history_messages WHERE id = '00000000-0000-0000-0000-00000000a003'
	`); err != nil {
		t.Fatalf("simulate legacy history clear: %v", err)
	}

	up0108 := readEmbeddedMigration(t, "postgres/legacy/v1/migrations/0108_compaction_artifacts.up.sql")
	if _, err := tx.Exec(ctx, up0108); err != nil {
		t.Fatalf("apply 0108 up: %v", err)
	}
	if _, err := tx.Exec(ctx, up0108); err != nil {
		t.Fatalf("reapply 0108 up (idempotency): %v", err)
	}
	assertIndexExists(t, ctx, tx, "idx_compacts_active_session", true)
	assertForeignKeyDeleteAction(t, ctx, tx, "bot_history_message_compacts", "bot_history_message_compacts_superseded_by_fkey", "n")

	up0109 := readEmbeddedMigration(t, "postgres/legacy/v1/migrations/0109_compaction_epoch.up.sql")
	if _, err := tx.Exec(ctx, up0109); err != nil {
		t.Fatalf("apply 0109 up: %v", err)
	}
	if _, err := tx.Exec(ctx, up0109); err != nil {
		t.Fatalf("reapply 0109 up (idempotency): %v", err)
	}
	assertIndexExists(t, ctx, tx, "idx_compacts_owner_epoch", true)
	assertForeignKeyDeleteAction(t, ctx, tx, "bot_history_message_compacts", "bot_history_message_compacts_session_id_fkey", "c")
	assertCompactionEpoch(t, ctx, tx, "bot_sessions", sessionID, 0)
	assertCompactionEpoch(t, ctx, tx, "bot_history_message_compacts", legacyID, 0)
	assertCompactionEpoch(t, ctx, tx, "bot_sessions", repairSessionID, 1)
	assertCompactionEpoch(t, ctx, tx, "bot_history_message_compacts", repairArtifactID, 0)
	assertCompactionEpoch(t, ctx, tx, "bot_sessions", clearedSessionID, 1)
	assertCompactionEpoch(t, ctx, tx, "bot_history_message_compacts", clearedArtifactID, 0)
	clearedLineage, err := listCompactionArtifactLineageBySession(ctx, tx, clearedSessionID)
	if err != nil {
		t.Fatalf("query cleared session lineage: %v", err)
	}
	if len(clearedLineage) != 0 {
		t.Fatalf("cleared session retained active lineage: %#v", clearedLineage)
	}

	var (
		legacyVersion  int32
		legacyCoverage string
		legacyStartMs  int64
		legacyEndMs    int64
		legacyLevel    int32
		legacyParents  int32
		legacyUnmarked bool
	)
	if err := tx.QueryRow(ctx, `
SELECT artifact_version, coverage::text, anchor_start_ms, anchor_end_ms, artifact_level,
       cardinality(parent_ids), superseded_by IS NULL AND superseded_at IS NULL
FROM bot_history_message_compacts
WHERE id = $1
`, legacyID).Scan(&legacyVersion, &legacyCoverage, &legacyStartMs, &legacyEndMs, &legacyLevel, &legacyParents, &legacyUnmarked); err != nil {
		t.Fatalf("read migrated legacy artifact: %v", err)
	}
	if legacyVersion != 1 || legacyCoverage != "[]" || legacyStartMs != 0 || legacyEndMs != 0 ||
		legacyLevel != 0 || legacyParents != 0 || !legacyUnmarked {
		t.Fatalf("legacy artifact defaults = (%d, %q, %d, %d, %d, %d, %v), want (1, \"[]\", 0, 0, 0, 0, true)",
			legacyVersion, legacyCoverage, legacyStartMs, legacyEndMs, legacyLevel, legacyParents, legacyUnmarked)
	}

	if _, err := tx.Exec(ctx, `
INSERT INTO bot_history_message_compacts (id, bot_id, session_id, status, summary, parent_ids)
VALUES ($1, $2, $3, 'ok', 'active', ARRAY[$4, $5]::uuid[])
`, activeID, botID, sessionID, parentOneID, parentTwoID); err != nil {
		t.Fatalf("insert active artifact: %v", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO bot_history_message_compacts (id, bot_id, session_id, status, summary, superseded_by, superseded_at)
VALUES
  ($2, $4, $5, 'ok', 'parent one', $1, now()),
  ($3, $4, $5, 'ok', 'parent two', $1, now())
`, activeID, parentOneID, parentTwoID, botID, sessionID); err != nil {
		t.Fatalf("insert parent artifacts: %v", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO bot_history_message_compacts (id, bot_id, session_id, status, summary)
VALUES ($1, $2, $3, 'ok', 'unlinked')
`, unlinkedID, botID, sessionID); err != nil {
		t.Fatalf("insert unlinked artifact: %v", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO bot_history_message_compacts (id, bot_id, session_id, status, summary)
VALUES ($1, $2, $3, 'ok', 'foreign artifact')
	`, foreignArtifactID, foreignBotID, foreignSessionID); err != nil {
		t.Fatalf("insert foreign artifact: %v", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO bot_history_message_compacts (id, bot_id, session_id, status, summary)
VALUES ($1, $2, $3, 'ok', 'cross-owner artifact')
	`, crossOwnerArtifactID, foreignBotID, sessionID); err != nil {
		t.Fatalf("insert cross-owner artifact: %v", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO bot_history_message_compacts (id, bot_id, status, summary)
VALUES ($1, $2, 'ok', 'log-only artifact')
	`, logOnlyArtifactID, logOnlyBotID); err != nil {
		t.Fatalf("insert log-only artifact: %v", err)
	}
	if _, err := tx.Exec(ctx, `
	INSERT INTO bot_history_messages (id, bot_id, session_id, compact_id)
	VALUES ('00000000-0000-0000-0000-00000000a101', $1, $2, $3)
		`, foreignBotID, foreignSessionID, foreignArtifactID); err != nil {
		t.Fatalf("insert compacted messages: %v", err)
	}

	assertGeneratedLineageQueries(t, ctx, tx, botID, sessionID, []string{parentOneID, parentTwoID}, activeID)

	if _, err := tx.Exec(ctx, clearHistoryBySessionSQL, sessionID); err != nil {
		t.Fatalf("delete session history through generated query: %v", err)
	}
	assertRowCount(t, ctx, tx, "bot_history_messages", 2)
	assertRowCount(t, ctx, tx, "bot_history_message_compacts", 4)
	assertCompactionEpoch(t, ctx, tx, "bot_sessions", sessionID, 1)

	if _, err := tx.Exec(ctx, clearHistoryByBotSQL, foreignBotID); err != nil {
		t.Fatalf("delete bot history through generated query: %v", err)
	}
	assertRowCount(t, ctx, tx, "bot_history_messages", 1)
	assertRowCount(t, ctx, tx, "bot_history_message_compacts", 3)
	assertCompactionEpoch(t, ctx, tx, "bot_sessions", foreignSessionID, 1)

	if _, err := tx.Exec(ctx, clearHistoryBySessionSQL, repairSessionID); err != nil {
		t.Fatalf("delete repaired session history through generated query: %v", err)
	}
	assertRowCount(t, ctx, tx, "bot_history_messages", 0)
	assertRowCount(t, ctx, tx, "bot_history_message_compacts", 2)
	assertCompactionEpoch(t, ctx, tx, "bot_sessions", repairSessionID, 2)
	if _, err := tx.Exec(ctx, clearHistoryBySessionSQL, clearedSessionID); err != nil {
		t.Fatalf("delete cleared session history through generated query: %v", err)
	}
	assertRowCount(t, ctx, tx, "bot_history_message_compacts", 1)
	assertCompactionEpoch(t, ctx, tx, "bot_sessions", clearedSessionID, 2)

	if _, err := tx.Exec(ctx, deleteCompactionLogsByBotSQL, botID); err != nil {
		t.Fatalf("fence in-flight compaction before deleting logs: %v", err)
	}
	assertCompactionEpoch(t, ctx, tx, "bot_sessions", sessionID, 2)

	if _, err := tx.Exec(ctx, deleteCompactionLogsByBotSQL, logOnlyBotID); err != nil {
		t.Fatalf("delete artifacts through generated query: %v", err)
	}
	assertRowCount(t, ctx, tx, "bot_history_message_compacts", 0)

	down0109 := readEmbeddedMigration(t, "postgres/legacy/v1/migrations/0109_compaction_epoch.down.sql")
	if _, err := tx.Exec(ctx, down0109); err != nil {
		t.Fatalf("apply 0109 down: %v", err)
	}
	assertIndexExists(t, ctx, tx, "idx_compacts_owner_epoch", false)
	assertForeignKeyDeleteAction(t, ctx, tx, "bot_history_message_compacts", "bot_history_message_compacts_session_id_fkey", "n")
	assertColumnExists(t, ctx, tx, schema, "bot_sessions", "compaction_epoch", false)
	assertColumnExists(t, ctx, tx, schema, "bot_history_message_compacts", "compaction_epoch", false)

	down0108 := readEmbeddedMigration(t, "postgres/legacy/v1/migrations/0108_compaction_artifacts.down.sql")
	if _, err := tx.Exec(ctx, down0108); err != nil {
		t.Fatalf("apply 0108 down: %v", err)
	}
	assertIndexExists(t, ctx, tx, "idx_compacts_active_session", false)
	var artifactColumns int
	if err := tx.QueryRow(ctx, `
SELECT count(*)
FROM information_schema.columns
WHERE table_schema = $1
  AND table_name = 'bot_history_message_compacts'
  AND column_name IN (
    'artifact_version', 'coverage', 'anchor_start_ms', 'anchor_end_ms',
    'artifact_level', 'parent_ids', 'superseded_by', 'superseded_at'
  )
`, schema).Scan(&artifactColumns); err != nil {
		t.Fatalf("inspect 0108 down reversal: %v", err)
	}
	if artifactColumns != 0 {
		t.Fatalf("0108 down migration left %d artifact columns behind", artifactColumns)
	}
	assertRowCount(t, ctx, tx, "bot_history_message_compacts", 0)
}

type compactionLineageRow struct {
	ID    uuid.UUID
	BotID uuid.UUID
}

func listCompactionArtifactLineageBySession(ctx context.Context, tx pgx.Tx, sessionID string) ([]compactionLineageRow, error) {
	rows, err := tx.Query(ctx, listCompactionArtifactLineageBySessionSQL, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []compactionLineageRow
	for rows.Next() {
		var row compactionLineageRow
		if err := rows.Scan(&row.ID, &row.BotID); err != nil {
			return nil, err
		}
		items = append(items, row)
	}
	return items, rows.Err()
}

func assertGeneratedLineageQueries(t *testing.T, ctx context.Context, tx pgx.Tx, botID, sessionID string, parentIDs []string, activeID string) {
	t.Helper()
	parsedBotID, err := uuid.Parse(botID)
	if err != nil {
		t.Fatalf("parse bot id: %v", err)
	}
	rows, err := listCompactionArtifactLineageBySession(ctx, tx, sessionID)
	if err != nil {
		t.Fatalf("query session lineage: %v", err)
	}
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if row.BotID != parsedBotID {
			t.Fatalf("session lineage query crossed bot ownership: row=%s bot=%s want=%s", row.ID, row.BotID, parsedBotID)
		}
		seen[row.ID.String()] = struct{}{}
	}
	for _, id := range append(parentIDs, activeID) {
		if _, ok := seen[id]; !ok {
			t.Fatalf("session lineage query omitted %s: %#v", id, seen)
		}
	}
	parentRows, err := tx.Query(ctx, listCompactionArtifactParentIDsBySuccessorSQL, sessionID, botID, activeID)
	if err != nil {
		t.Fatalf("query reverse lineage edges: %v", err)
	}
	defer parentRows.Close()
	gotParents := make([]string, 0, len(parentIDs))
	for parentRows.Next() {
		var parent uuid.UUID
		if err := parentRows.Scan(&parent); err != nil {
			t.Fatalf("scan reverse lineage parent: %v", err)
		}
		gotParents = append(gotParents, parent.String())
	}
	if err := parentRows.Err(); err != nil {
		t.Fatalf("iterate reverse lineage parents: %v", err)
	}
	if !reflect.DeepEqual(gotParents, parentIDs) {
		t.Fatalf("reverse lineage parents = %#v, want %#v", gotParents, parentIDs)
	}
}

func assertForeignKeyDeleteAction(t *testing.T, ctx context.Context, tx pgx.Tx, table, constraint, want string) {
	t.Helper()
	var got string
	if err := tx.QueryRow(ctx, `
SELECT confdeltype::text
FROM pg_constraint
WHERE conrelid = $1::regclass
  AND conname = $2
`, table, constraint).Scan(&got); err != nil {
		t.Fatalf("read %s delete action: %v", constraint, err)
	}
	if got != want {
		t.Fatalf("%s delete action = %q, want %q", constraint, got, want)
	}
}

func assertIndexExists(t *testing.T, ctx context.Context, tx pgx.Tx, index string, want bool) {
	t.Helper()
	var got bool
	if err := tx.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", index).Scan(&got); err != nil {
		t.Fatalf("inspect index %s: %v", index, err)
	}
	if got != want {
		t.Fatalf("index %s existence = %v, want %v", index, got, want)
	}
}

func assertCompactionEpoch(t *testing.T, ctx context.Context, tx pgx.Tx, table, id string, want int64) {
	t.Helper()
	var got int64
	query := "SELECT compaction_epoch FROM " + pgx.Identifier{table}.Sanitize() + " WHERE id = $1"
	if err := tx.QueryRow(ctx, query, id).Scan(&got); err != nil {
		t.Fatalf("read %s compaction epoch: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s compaction epoch = %d, want %d", table, got, want)
	}
}

func assertColumnExists(t *testing.T, ctx context.Context, tx pgx.Tx, schema, table, column string, want bool) {
	t.Helper()
	var got bool
	if err := tx.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM information_schema.columns
  WHERE table_schema = $1
    AND table_name = $2
    AND column_name = $3
)
`, schema, table, column).Scan(&got); err != nil {
		t.Fatalf("inspect %s.%s: %v", table, column, err)
	}
	if got != want {
		t.Fatalf("%s.%s existence = %v, want %v", table, column, got, want)
	}
}

func assertRowCount(t *testing.T, ctx context.Context, tx pgx.Tx, table string, want int) {
	t.Helper()
	var got int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s row count = %d, want %d", table, got, want)
	}
}
