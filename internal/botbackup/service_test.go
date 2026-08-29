package botbackup

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/bots"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

func TestReadZipEntriesRejectsZipSlip(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("../manifest.json")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := w.Write([]byte(`{}`)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := readZipEntries(buf.Bytes()); err == nil {
		t.Fatal("readZipEntries() accepted zip slip path")
	}
}

func TestNormalizeExportOptionsDefaultsAllSections(t *testing.T) {
	opts := NormalizeExportOptions(ExportOptions{})
	if len(opts.Sections) != len(AllExportSections) {
		t.Fatalf("default export should include all sections, got %v", opts.Sections)
	}
	opts = NormalizeExportOptions(ExportOptions{Sections: []Section{SectionHistory}})
	if opts.wants(SectionWorkspace) {
		t.Fatal("explicit non-default scope should not include workspace")
	}
	if !opts.wants(SectionHistory) {
		t.Fatal("explicit history scope should include history")
	}
	if !opts.wants(SectionProfile) {
		t.Fatal("profile is always exported")
	}
}

func TestWriteJSONPreservesSensitiveValues(t *testing.T) {
	var buf bytes.Buffer
	manifest := Manifest{}
	writer := &zipBackupWriter{
		zw:       zip.NewWriter(&buf),
		manifest: &manifest,
		checksum: map[string]string{},
	}
	value := []map[string]any{{
		"name": "provider",
		"config": map[string]any{
			"api_key":  "secret-value",
			"base_url": "https://example.com",
		},
	}}
	if err := writer.writeJSON("dependencies/providers.json", "providers", value, ExportOptions{}); err != nil {
		t.Fatalf("writeJSON() error = %v", err)
	}
	if err := writer.zw.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	entries, err := readZipEntries(buf.Bytes())
	if err != nil {
		t.Fatalf("readZipEntries() error = %v", err)
	}
	raw := string(entries["dependencies/providers.json"].data)
	if !strings.Contains(raw, "secret-value") {
		t.Fatalf("sensitive value was not preserved: %s", raw)
	}
}

func TestRebuildRestoredHistoryTurnsLinksVisibleLinearTurns(t *testing.T) {
	ctx := context.Background()
	botID := mustTestPGUUID(t, "00000000-0000-0000-0000-000000000701")
	sessionID := mustTestPGUUID(t, "00000000-0000-0000-0000-000000000702")
	userID := mustTestPGUUID(t, "00000000-0000-0000-0000-000000000711")
	assistantID := mustTestPGUUID(t, "00000000-0000-0000-0000-000000000712")
	toolID := mustTestPGUUID(t, "00000000-0000-0000-0000-000000000713")
	finalID := mustTestPGUUID(t, "00000000-0000-0000-0000-000000000714")
	nextUserID := mustTestPGUUID(t, "00000000-0000-0000-0000-000000000715")
	nextAssistantID := mustTestPGUUID(t, "00000000-0000-0000-0000-000000000716")
	q := &recordingRestoredTurnQueries{}

	err := rebuildRestoredHistoryTurns(ctx, q, botID, []restoredHistoryMessage{
		{id: userID, sessionID: sessionID, role: "user"},
		{id: assistantID, sessionID: sessionID, role: "assistant"},
		{id: toolID, sessionID: sessionID, role: "tool"},
		{id: finalID, sessionID: sessionID, role: "assistant"},
		{id: nextUserID, sessionID: sessionID, role: "user"},
		{id: nextAssistantID, sessionID: sessionID, role: "assistant"},
	})
	if err != nil {
		t.Fatalf("rebuildRestoredHistoryTurns() error = %v", err)
	}
	if got, want := len(q.turns), 2; got != want {
		t.Fatalf("created turns = %d, want %d", got, want)
	}
	if got := q.turns[0].RequestMessageID.String(); got != userID.String() {
		t.Fatalf("first request message = %s, want %s", got, userID.String())
	}
	if got := q.turns[1].RequestMessageID.String(); got != nextUserID.String() {
		t.Fatalf("second request message = %s, want %s", got, nextUserID.String())
	}
	if got, want := len(q.binds), 2; got != want {
		t.Fatalf("assistant binds = %d, want %d", got, want)
	}
	if got := q.binds[0].AssistantMessageID.String(); got != assistantID.String() {
		t.Fatalf("first assistant bind = %s, want %s", got, assistantID.String())
	}
	wantSeq := []int64{1, 2, 3, 4, 1, 2}
	if got, want := len(q.links), len(wantSeq); got != want {
		t.Fatalf("links = %d, want %d", got, want)
	}
	for i, want := range wantSeq {
		if got := q.links[i].TurnMessageSeq.Int64; got != want {
			t.Fatalf("link %d seq = %d, want %d", i, got, want)
		}
	}
}

func TestRebuildRestoredHistoryTurnsKeepsOrphanAssistantSeq(t *testing.T) {
	ctx := context.Background()
	botID := mustTestPGUUID(t, "00000000-0000-0000-0000-000000000741")
	sessionID := mustTestPGUUID(t, "00000000-0000-0000-0000-000000000742")
	assistantID := mustTestPGUUID(t, "00000000-0000-0000-0000-000000000743")
	toolID := mustTestPGUUID(t, "00000000-0000-0000-0000-000000000744")
	finalID := mustTestPGUUID(t, "00000000-0000-0000-0000-000000000745")
	q := &recordingRestoredTurnQueries{}

	err := rebuildRestoredHistoryTurns(ctx, q, botID, []restoredHistoryMessage{
		{id: assistantID, sessionID: sessionID, role: "assistant"},
		{id: toolID, sessionID: sessionID, role: "tool"},
		{id: finalID, sessionID: sessionID, role: "assistant"},
	})
	if err != nil {
		t.Fatalf("rebuildRestoredHistoryTurns() error = %v", err)
	}
	if got, want := len(q.turns), 1; got != want {
		t.Fatalf("created turns = %d, want %d", got, want)
	}
	if got := q.turns[0].AssistantMessageID.String(); got != assistantID.String() {
		t.Fatalf("orphan assistant message = %s, want %s", got, assistantID.String())
	}
	wantSeq := []int64{2, 3, 4}
	if got, want := len(q.links), len(wantSeq); got != want {
		t.Fatalf("links = %d, want %d", got, want)
	}
	for i, want := range wantSeq {
		if got := q.links[i].TurnMessageSeq.Int64; got != want {
			t.Fatalf("link %d seq = %d, want %d", i, got, want)
		}
	}
}

func TestRebindRestoredForkMetadataMapsImportedIDs(t *testing.T) {
	oldSessionID := mustTestPGUUID(t, "00000000-0000-0000-0000-000000000781")
	newSessionID := mustTestPGUUID(t, "00000000-0000-0000-0000-000000000782")
	oldMessageID := mustTestPGUUID(t, "00000000-0000-0000-0000-000000000783")
	newMessageID := mustTestPGUUID(t, "00000000-0000-0000-0000-000000000784")
	oldForkMessageID := mustTestPGUUID(t, "00000000-0000-0000-0000-000000000785")
	newForkMessageID := mustTestPGUUID(t, "00000000-0000-0000-0000-000000000786")
	raw := []byte(`{"forked_from":{"session_id":"` + oldSessionID.String() + `","message_id":"` + oldMessageID.String() + `","fork_message_id":"` + oldForkMessageID.String() + `"}}`)

	got, changed := rebindRestoredForkMetadata(raw, map[string]pgtype.UUID{
		oldSessionID.String(): newSessionID,
	}, map[string]pgtype.UUID{
		oldMessageID.String():     newMessageID,
		oldForkMessageID.String(): newForkMessageID,
	})
	if !changed {
		t.Fatal("rebindRestoredForkMetadata changed = false, want true")
	}
	var decoded struct {
		ForkedFrom struct {
			SessionID     string `json:"session_id"`
			MessageID     string `json:"message_id"`
			ForkMessageID string `json:"fork_message_id"`
		} `json:"forked_from"`
	}
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("unmarshal rebound metadata: %v", err)
	}
	if decoded.ForkedFrom.SessionID != newSessionID.String() ||
		decoded.ForkedFrom.MessageID != newMessageID.String() ||
		decoded.ForkedFrom.ForkMessageID != newForkMessageID.String() {
		t.Fatalf("rebound fork metadata = %+v", decoded.ForkedFrom)
	}
}

func TestScrubImportedProfileACPSecrets(t *testing.T) {
	for _, tc := range []struct {
		name         string
		warnings     []string
		wantWarnings []string
	}{
		{
			name:         "adds warning",
			wantWarnings: []string{acpManagedSecretsWarning},
		},
		{
			name:         "dedupes warning",
			warnings:     []string{acpManagedSecretsWarning},
			wantWarnings: []string{acpManagedSecretsWarning},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &importState{warnings: append([]string(nil), tc.warnings...)}
			profile := bots.Bot{
				Metadata: map[string]any{
					"acp": map[string]any{
						"agents": map[string]any{
							"hermes": map[string]any{
								"enabled":    true,
								"setup_mode": "api_key",
								"managed": map[string]any{
									"provider": "openrouter",
									"model":    "nousresearch/hermes",
									"api_key":  "secret-value",
								},
							},
						},
					},
				},
			}

			scrubbed := scrubImportedProfileACPSecrets(profile, state)
			raw, err := json.Marshal(scrubbed.Metadata)
			if err != nil {
				t.Fatalf("marshal metadata: %v", err)
			}
			if strings.Contains(string(raw), "secret-value") || strings.Contains(string(raw), `"api_key":`) {
				t.Fatalf("imported profile kept ACP secret: %s", raw)
			}
			if len(state.warnings) != len(tc.wantWarnings) {
				t.Fatalf("warnings = %v, want %v", state.warnings, tc.wantWarnings)
			}
			for i := range tc.wantWarnings {
				if state.warnings[i] != tc.wantWarnings[i] {
					t.Fatalf("warnings = %v, want %v", state.warnings, tc.wantWarnings)
				}
			}
		})
	}
}

type recordingRestoredTurnQueries struct {
	turns []dbsqlc.CreateHistoryTurnParams
	binds []dbsqlc.BindHistoryTurnAssistantByRequestParams
	links []dbsqlc.LinkMessageToHistoryTurnParams
}

func (q *recordingRestoredTurnQueries) CreateHistoryTurn(_ context.Context, arg dbsqlc.CreateHistoryTurnParams) (dbstore.HistoryTurn, error) {
	q.turns = append(q.turns, arg)
	return dbstore.HistoryTurn{ID: mustPGUUID(fmt.Sprintf("00000000-0000-0000-0000-%012d", len(q.turns)))}, nil
}

func (q *recordingRestoredTurnQueries) BindHistoryTurnAssistantByRequest(_ context.Context, arg dbsqlc.BindHistoryTurnAssistantByRequestParams) (dbstore.HistoryTurn, error) {
	q.binds = append(q.binds, arg)
	return dbstore.HistoryTurn{}, nil
}

func (q *recordingRestoredTurnQueries) LinkMessageToHistoryTurn(_ context.Context, arg dbsqlc.LinkMessageToHistoryTurnParams) (pgtype.UUID, error) {
	q.links = append(q.links, arg)
	return arg.MessageID, nil
}

func mustTestPGUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	out, err := parsePGUUID(value)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", value, err)
	}
	return out
}

func mustPGUUID(value string) pgtype.UUID {
	out, err := parsePGUUID(value)
	if err != nil {
		panic(err)
	}
	return out
}

func parsePGUUID(value string) (pgtype.UUID, error) {
	var out pgtype.UUID
	if err := out.Scan(value); err != nil {
		return pgtype.UUID{}, err
	}
	return out, nil
}

func TestWorkspaceStoredVerbatimAsTarGz(t *testing.T) {
	// Build a workspace tar.gz as the container would return it.
	var workspace bytes.Buffer
	gw := gzip.NewWriter(&workspace)
	tw := tar.NewWriter(gw)
	body := []byte("hello workspace")
	if err := tw.WriteHeader(&tar.Header{Name: "notes/readme.txt", Typeflag: tar.TypeReg, Mode: 0o640, Size: int64(len(body))}); err != nil {
		t.Fatalf("WriteHeader(file) error = %v", err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatalf("Write(file) error = %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close() error = %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip Close() error = %v", err)
	}
	original := workspace.Bytes()

	// Store it verbatim as the single workspace entry, as writeWorkspace does.
	var backup bytes.Buffer
	manifest := Manifest{}
	writer := &zipBackupWriter{
		zw:       zip.NewWriter(&backup),
		manifest: &manifest,
		checksum: map[string]string{},
	}
	if err := writer.writeStream(workspaceArchivePath, bytes.NewReader(original), 0o640, time.Time{}, zip.Store); err != nil {
		t.Fatalf("writeStream() error = %v", err)
	}
	if err := writer.zw.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	entries, err := readZipEntries(backup.Bytes())
	if err != nil {
		t.Fatalf("readZipEntries() error = %v", err)
	}
	// The workspace must be a single nested tar.gz, not exploded files.
	if !hasEntry(entries, workspaceArchivePath) {
		t.Fatalf("workspace archive entry missing; entries=%v", entries)
	}
	if !hasWorkspaceEntries(entries) {
		t.Fatal("hasWorkspaceEntries should be true")
	}

	// The already-gzipped blob must be stored (not deflated again) to avoid
	// pointless double compression.
	if method := workspaceEntryMethod(t, backup.Bytes()); method != zip.Store {
		t.Fatalf("workspace entry method = %d, want zip.Store (%d)", method, zip.Store)
	}

	// The blob round-trips byte-for-byte (no re-packing).
	got, err := workspaceArchive(entries)
	if err != nil {
		t.Fatalf("workspaceArchive() error = %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatal("workspace archive was not preserved verbatim")
	}

	// File listing reads names straight from the tar.gz headers.
	names := workspaceFileList(entries, sectionItemLimit)
	if len(names) != 1 || names[0] != "notes/readme.txt" {
		t.Fatalf("workspaceFileList = %v, want [notes/readme.txt]", names)
	}
	if n := countWorkspaceFiles(entries); n != 1 {
		t.Fatalf("countWorkspaceFiles = %d, want 1", n)
	}

	plain, err := readTarGzFile(got, "notes/readme.txt")
	if err != nil {
		t.Fatalf("read workspace file: %v", err)
	}
	if string(plain) != string(body) {
		t.Fatalf("workspace file = %q, want %q", plain, body)
	}
}

// workspaceEntryMethod returns the zip compression method used for the
// workspace archive entry within a backup zip.
func workspaceEntryMethod(t *testing.T, raw []byte) uint16 {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("zip.NewReader() error = %v", err)
	}
	for _, file := range zr.File {
		if file.Name == workspaceArchivePath {
			return file.Method
		}
	}
	t.Fatalf("workspace entry %q not found in zip", workspaceArchivePath)
	return 0
}

func readTarGzFile(raw []byte, name string) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer func() { _ = gr.Close() }()
	tr := tar.NewReader(gr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil, io.ErrUnexpectedEOF
		}
		if err != nil {
			return nil, err
		}
		if header.Name != name {
			continue
		}
		return io.ReadAll(tr)
	}
}

func TestIsWorkspaceRestoreRetryable(t *testing.T) {
	retryable := []string{
		"get workspace runtime: not found",
		"No such container: workspace-123",
		"workspace is not reachable: connection refused",
	}
	for _, msg := range retryable {
		if !isWorkspaceRestoreRetryable(errString(msg)) {
			t.Fatalf("expected retryable error: %s", msg)
		}
	}
	if isWorkspaceRestoreRetryable(io.ErrUnexpectedEOF) {
		t.Fatal("unexpected retryable generic error")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
