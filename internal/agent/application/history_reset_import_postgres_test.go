package application

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/botbackup"
	dbpkg "github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/errs"
)

// historyBackup is a backup of the bot's history the way an export writes it:
// the sessions and messages the exporter reads, as JSON entries of a zip.
func (h wsStepHistoryHarness) historyBackup(t *testing.T) []byte {
	t.Helper()
	ctx := context.Background()
	q := dbsqlc.New(h.pool)
	botID := dbpkg.ParseUUIDOrEmpty(h.botID)
	sessions, err := q.ListSessionsByBot(ctx, botID)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := q.ListAllMessagesForBackup(ctx, botID)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for path, value := range map[string]any{
		botbackup.ManifestPath:  botbackup.Manifest{SchemaVersion: botbackup.BackupSchemaVersion, SourceBotID: h.botID},
		"bot/profile.json":      map[string]string{"id": h.botID},
		"history/sessions.json": sessions,
		"history/messages.json": messages,
	} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		w, err := zw.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// liveSessions are the bot's sessions that are not deleted.
func (h wsStepHistoryHarness) liveSessions(t *testing.T) []string {
	t.Helper()
	rows, err := h.pool.Query(context.Background(), `SELECT id::text FROM bot_sessions WHERE bot_id = $1 AND deleted_at IS NULL`, h.botID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

// An overwrite import that replaces the history takes the same reset as a
// clear, entered through the import service: a projection it cannot clear
// fails the import before any history is replaced; otherwise the runs of the
// replaced sessions leave the live projection and the ledger fallback, a
// client watching one of them is cleared, and a turn in the restored session
// runs as usual.
func TestPostgresHistoryResetReplaceImport(t *testing.T) {
	backend := &failingUpdateBackend{MemoryBackend: sessionruntime.NewMemoryBackend()}
	h := newWSStepHistoryHarnessOn(t, wsStepHistorySuccess, backend)
	ctx := context.Background()
	view := h.watch(t)
	h.run(t, nil)
	before := mustSnapshot(t, h.manager, h.botID, h.sessionID).CurrentRunView
	if before == nil {
		t.Fatal("finished run is missing from the live snapshot before the import")
	}
	kept, err := h.messages.ListBySession(ctx, h.sessionID)
	if err != nil || len(kept) == 0 {
		t.Fatalf("history before the import = (%d rows, %v)", len(kept), err)
	}
	view.drain()

	backup := h.historyBackup(t)
	importer := botbackup.New(botbackup.Params{
		DB: h.pool, Queries: postgresstore.NewQueriesWithPool(h.pool, dbsqlc.New(h.pool)), ACPRuntimes: h.manager,
	})
	opts := botbackup.ImportOptions{
		Mode: botbackup.ImportModeOverwrite, TargetBotID: h.botID,
		Sections: map[botbackup.Section]botbackup.ImportStrategy{botbackup.SectionHistory: botbackup.StrategyReplace},
	}

	backend.fail.Store(true)
	_, err = importer.Import(ctx, "", backup, opts, "")
	backend.fail.Store(false)
	if !errors.Is(err, botbackup.ErrHistoryResetUnavailable) || errs.FaultOf(err) != apperror.FaultDependency {
		t.Fatalf("import with an uncleared projection = %v (fault %q), want a dependency failure", err, errs.FaultOf(err))
	}
	if rows, err := h.messages.ListBySession(ctx, h.sessionID); err != nil || len(rows) != len(kept) {
		t.Fatalf("history after the failed import = (%d rows, %v), want the %d rows kept", len(rows), err, len(kept))
	}
	if got := h.liveSessions(t); len(got) != 1 || got[0] != h.sessionID {
		t.Fatalf("sessions after the failed import = %v, want only %s", got, h.sessionID)
	}
	if got := mustSnapshot(t, h.manager, h.botID, h.sessionID).CurrentRunView; got == nil || got.RunID != before.RunID {
		t.Fatalf("live run after the failed import = %s, want %s", describeRun(got), describeRun(before))
	}

	result, err := importer.Import(ctx, "", backup, opts, "")
	if err != nil || len(result.Warnings) != 0 {
		t.Fatalf("import = (%+v, %v), want it clean", result, err)
	}
	if got := mustSnapshot(t, h.manager, h.botID, h.sessionID).CurrentRunView; got != nil {
		t.Errorf("live snapshot of the replaced session holds %s", describeRun(got))
	}
	view.awaitCleared(before.RunID)
	if got := mustSnapshot(t, h.restarted(t), h.botID, h.sessionID).CurrentRunView; got != nil {
		t.Errorf("ledger fallback of the replaced session reports %s", describeRun(got))
	}

	sessions := h.liveSessions(t)
	if len(sessions) != 1 || sessions[0] == h.sessionID {
		t.Fatalf("sessions after the import = %v, want one restored session", sessions)
	}
	restored := h
	restored.sessionID = sessions[0]
	if rows, err := restored.messages.ListBySession(ctx, restored.sessionID); err != nil || len(rows) != len(kept) {
		t.Fatalf("restored history = (%d rows, %v), want %d", len(rows), err, len(kept))
	}
	if got := mustSnapshot(t, restored.manager, restored.botID, restored.sessionID).CurrentRunView; got != nil {
		t.Fatalf("restored session reports %s before any turn", describeRun(got))
	}
	next := restored.watch(t)
	restored.run(t, nil)
	after := mustSnapshot(t, restored.manager, restored.botID, restored.sessionID).CurrentRunView
	if after == nil || after.RunID == before.RunID {
		t.Fatalf("live run after a turn in the restored session = %s, want a new run", describeRun(after))
	}
	next.awaitRun(after.RunID)
}
