package db

import (
	"strings"
	"testing"
)

func TestCompactionArtifactMigrationPreservesPublished0108Schema(t *testing.T) {
	t.Parallel()

	up := readEmbeddedMigration(t, "postgres/legacy/v1/migrations/0108_compaction_artifacts.up.sql")
	down := readEmbeddedMigration(t, "postgres/legacy/v1/migrations/0108_compaction_artifacts.down.sql")

	if !strings.HasPrefix(up, "-- 0108_compaction_artifacts\n-- Persist summary artifact") ||
		!strings.HasPrefix(down, "-- 0108_compaction_artifacts\n-- Remove summary artifact") {
		t.Fatal("0108 migration pair is missing the required name and description headers")
	}
	if !strings.Contains(up, "superseded_by UUID REFERENCES bot_history_message_compacts(id) ON DELETE SET NULL") ||
		!strings.Contains(up, "CREATE INDEX IF NOT EXISTS idx_compacts_active_session") {
		t.Fatal("0108 up migration no longer matches its published schema")
	}
	for _, repair := range []string{
		"compacts_supersession_markers_check",
		"compacts_not_self_superseded_check",
		"idx_compacts_session_lineage",
		"DROP CONSTRAINT IF EXISTS bot_history_message_compacts_superseded_by_fkey",
	} {
		if strings.Contains(up, repair) || strings.Contains(down, repair) {
			t.Fatalf("0108 migration pair contains post-publication repair %q", repair)
		}
	}
	for _, removed := range []string{
		"DROP INDEX IF EXISTS idx_compacts_active_session",
		"DROP COLUMN IF EXISTS superseded_at",
		"DROP COLUMN IF EXISTS superseded_by",
		"DROP COLUMN IF EXISTS parent_ids",
		"DROP COLUMN IF EXISTS coverage",
	} {
		if !strings.Contains(down, removed) {
			t.Fatalf("0108 down migration does not reverse published object %q", removed)
		}
	}
}

func TestCompactionArtifactCanonicalSchemaMatchesPublished0108(t *testing.T) {
	t.Parallel()

	baseline := readEmbeddedMigration(t, "postgres/legacy/v1/migrations/0001_init.up.sql")

	for _, required := range []string{
		"superseded_by UUID REFERENCES bot_history_message_compacts(id) ON DELETE SET NULL",
		"CREATE INDEX IF NOT EXISTS idx_compacts_active_session",
	} {
		if !strings.Contains(baseline, required) {
			t.Fatalf("canonical 0001 schema is missing published 0108 object %q", required)
		}
	}
	for _, deferred := range []string{
		"bot_history_message_compact_parent_edges",
		"sync_compaction_artifact_parent_edges",
		"compacts_supersession_markers_check",
		"compacts_not_self_superseded_check",
		"idx_compacts_session_lineage",
	} {
		if strings.Contains(baseline, deferred) {
			t.Fatalf("canonical 0001 schema contains %q, which belongs to the artifact-writer PR", deferred)
		}
	}
}
