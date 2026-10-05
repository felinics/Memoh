package botbackup

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/acl"
	"github.com/felinics/memoh/internal/bots"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	modelpkg "github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/settings"
)

// secretCause stands for database error text that must never reach a warning:
// warnings are returned to the client and an export writes them into the file.
const secretCause = "SECRET duplicate key value violates unique constraint"

const skippedStepBotID = "00000000-0000-0000-0000-0000000000b1"

// failingQueries fails the one query named by failing with secretCause and
// returns empty results from the other queries it overrides.
type failingQueries struct {
	dbstore.Queries
	failing string
}

func (q failingQueries) result(method string) error {
	if q.failing == method {
		return errString(secretCause)
	}
	return nil
}

func (q failingQueries) ListSessionsByBot(context.Context, pgtype.UUID) ([]dbsqlc.ListSessionsByBotRow, error) {
	return nil, q.result("ListSessionsByBot")
}

func (q failingQueries) ListSessionDiscussCursorsByBot(context.Context, pgtype.UUID) ([]dbsqlc.BotSessionDiscussCursor, error) {
	return nil, q.result("ListSessionDiscussCursorsByBot")
}

func (q failingQueries) ListSessionEventsByBot(context.Context, pgtype.UUID) ([]dbsqlc.BotSessionEvent, error) {
	return nil, q.result("ListSessionEventsByBot")
}

func (q failingQueries) ListAllMessagesForBackup(context.Context, pgtype.UUID) ([]dbsqlc.ListAllMessagesForBackupRow, error) {
	return nil, q.result("ListAllMessagesForBackup")
}

func (q failingQueries) ListMessageAssetsBatch(context.Context, []pgtype.UUID) ([]dbsqlc.ListMessageAssetsBatchRow, error) {
	return nil, q.result("ListMessageAssetsBatch")
}

func (q failingQueries) GetModelByID(context.Context, pgtype.UUID) (dbsqlc.Model, error) {
	return dbsqlc.Model{}, q.result("GetModelByID")
}

func (q failingQueries) CreateBotACLRule(context.Context, dbsqlc.CreateBotACLRuleParams) (dbsqlc.BotAclRule, error) {
	return dbsqlc.BotAclRule{}, q.result("CreateBotACLRule")
}

func (q failingQueries) UpsertBotWorkspaceResourceLimits(context.Context, dbsqlc.UpsertBotWorkspaceResourceLimitsParams) (dbsqlc.BotWorkspaceResourceLimit, error) {
	return dbsqlc.BotWorkspaceResourceLimit{}, q.result("UpsertBotWorkspaceResourceLimits")
}

type failingWorkspace struct{}

func (failingWorkspace) ExportData(context.Context, string) (io.ReadCloser, error) {
	return nil, errString(secretCause)
}

func (failingWorkspace) ImportData(context.Context, string, io.Reader) error {
	return errString(secretCause)
}

func (failingWorkspace) CountData(context.Context, string) (int, error) { return 0, nil }

type noopACPRuntimes struct{}

func (noopACPRuntimes) BeginBotHistoryReset(ctx context.Context, _ string) (context.Context, func(), error) {
	return ctx, func() {}, nil
}

// archiveFailingWorkdirStore creates workdirs but cannot archive them.
type archiveFailingWorkdirStore struct {
	*fakeWorkdirStore
}

func (archiveFailingWorkdirStore) ArchiveWorkdir(context.Context, string, string) error {
	return errString(secretCause)
}

func captureLogger() (*slog.Logger, *bytes.Buffer) {
	var logs bytes.Buffer
	return slog.New(slog.NewJSONHandler(&logs, nil)), &logs
}

// warnRecords returns every record at WARN or above, so a duplicate event
// shows up whatever message it was logged with.
func warnRecords(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(logs.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		if record["level"] == slog.LevelWarn.String() || record["level"] == slog.LevelError.String() {
			out = append(out, record)
		}
	}
	return out
}

// assertSkippedEvent checks that the cause was recorded exactly once, as the
// event of the named step.
func assertSkippedEvent(t *testing.T, logs *bytes.Buffer, operation, step string) {
	t.Helper()
	records := warnRecords(t, logs)
	if len(records) != 1 {
		t.Fatalf("got %d WARN records, want exactly one event: %s", len(records), logs.String())
	}
	record := records[0]
	if record["level"] != slog.LevelWarn.String() {
		t.Fatalf("event level = %v, want WARN", record["level"])
	}
	want := map[string]string{
		"msg":       "bot backup step skipped",
		"operation": operation,
		"step":      step,
		"bot_id":    skippedStepBotID,
	}
	for key, value := range want {
		if record[key] != value {
			t.Fatalf("event %s = %v, want %q: %v", key, record[key], value, record)
		}
	}
	if text, _ := record["error"].(string); !strings.Contains(text, secretCause) {
		t.Fatalf("event error = %v, want the cause", record["error"])
	}
}

func assertNoCause(t *testing.T, warnings []string) {
	t.Helper()
	for _, warning := range warnings {
		if strings.Contains(warning, "SECRET") {
			t.Fatalf("warning %q carries the cause", warning)
		}
	}
}

func TestCollectHistoryWarningOmitsCause(t *testing.T) {
	t.Parallel()

	cases := []struct {
		failing string
		warning string
		step    string
	}{
		{"ListSessionsByBot", "sessions export failed", "sessions"},
		{"ListSessionDiscussCursorsByBot", "discuss cursors export failed", "discuss_cursors"},
		{"ListSessionEventsByBot", "session events export failed", "session_events"},
		{"ListAllMessagesForBackup", "messages export failed", "messages"},
		{"ListMessageAssetsBatch", "message assets export failed", "message_assets"},
	}
	for _, tc := range cases {
		t.Run(tc.step, func(t *testing.T) {
			t.Parallel()
			logger, logs := captureLogger()
			svc := &Service{logger: logger, queries: failingQueries{failing: tc.failing}}

			_, warnings := svc.collectHistory(context.Background(), skippedStepBotID, true)

			if !slices.Equal(warnings, []string{tc.warning}) {
				t.Fatalf("warnings = %q, want [%q]", warnings, tc.warning)
			}
			assertNoCause(t, warnings)
			assertSkippedEvent(t, logs, exportOperation, tc.step)
		})
	}
}

// The dependency warning keeps the ID the user configured and never the
// cause, which may be a database failure rather than a missing model.
func TestCollectDependenciesRecordsCause(t *testing.T) {
	t.Parallel()

	const modelID = "00000000-0000-0000-0000-0000000000c1"
	logger, logs := captureLogger()
	queries := failingQueries{failing: "GetModelByID"}
	svc := &Service{logger: logger, models: modelpkg.NewService(slog.New(slog.DiscardHandler), queries)}

	_, warnings := svc.collectDependencies(context.Background(), skippedStepBotID, settings.Settings{ChatModelID: modelID})

	if want := []string{"model dependency missing: " + modelID}; !slices.Equal(warnings, want) {
		t.Fatalf("warnings = %q, want %q", warnings, want)
	}
	assertNoCause(t, warnings)
	assertSkippedEvent(t, logs, exportOperation, "model_dependency")
}

// buildWorkspaceBundle is a bundle with a profile, settings and a workspace
// archive. The manifest carries warnings written by an older export.
func buildWorkspaceBundle(t *testing.T, manifestWarnings []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	manifest := Manifest{SchemaVersion: BackupSchemaVersion, SourceBotID: "src-bot", Warnings: manifestWarnings}
	w := &zipBackupWriter{zw: zip.NewWriter(&buf), manifest: &manifest, checksum: map[string]string{}}
	if err := w.writeJSON("bot/profile.json", "profile", bots.Bot{ID: "src-bot", DisplayName: "Src Bot", Timezone: "UTC"}, ExportOptions{}); err != nil {
		t.Fatalf("writeJSON(profile) error = %v", err)
	}
	if err := w.writeJSON("bot/settings.json", "settings", map[string]any{}, ExportOptions{}); err != nil {
		t.Fatalf("writeJSON(settings) error = %v", err)
	}
	if err := w.writeStream(workspaceArchivePath, bytes.NewReader(sampleTarGz(t)), 0o640, time.Time{}, zip.Store); err != nil {
		t.Fatalf("writeStream(workspace) error = %v", err)
	}
	if err := w.writeManifest(); err != nil {
		t.Fatalf("writeManifest() error = %v", err)
	}
	if err := w.zw.Close(); err != nil {
		t.Fatalf("zip Close() error = %v", err)
	}
	return buf.Bytes()
}

func TestImportWorkspaceWarningOmitsCause(t *testing.T) {
	t.Parallel()

	// A warning an older export wrote into the file is the user's own data and
	// is read back as it is.
	legacy := "sessions export failed: ERROR: relation does not exist (SQLSTATE 42P01)"
	logger, logs := captureLogger()
	svc := &Service{logger: logger, workspace: failingWorkspace{}, acpRuntimes: noopACPRuntimes{}}

	result, err := svc.Import(context.Background(), "user-1", buildWorkspaceBundle(t, []string{legacy}), ImportOptions{
		Mode:        ImportModeOverwrite,
		TargetBotID: skippedStepBotID,
		Sections:    map[Section]ImportStrategy{SectionWorkspace: StrategyMerge},
	}, "")
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}

	if want := []string{legacy, "workspace restore failed"}; !slices.Equal(result.Warnings, want) {
		t.Fatalf("warnings = %q, want %q", result.Warnings, want)
	}
	assertNoCause(t, result.Warnings)
	assertSkippedEvent(t, logs, importOperation, "workspace")
}

func newACLImportState(t *testing.T, createMode bool) *importState {
	t.Helper()
	raw, err := json.Marshal([]acl.Rule{{Enabled: true, Effect: acl.EffectAllow}})
	if err != nil {
		t.Fatalf("marshal acl rules: %v", err)
	}
	return &importState{
		entries:    map[string]backupZipEntry{"bot/acl_rules.json": {data: raw}},
		counts:     map[Section]int{},
		botID:      skippedStepBotID,
		createMode: createMode,
	}
}

// itemErr records the event; neither its caller nor the section's restore
// step records the same failure again.
func TestItemErrRecordsCauseOnce(t *testing.T) {
	t.Parallel()

	queries := failingQueries{failing: "CreateBotACLRule"}
	opts := ImportOptions{Mode: ImportModeOverwrite, Sections: map[Section]ImportStrategy{SectionACL: StrategyMerge}}

	logger, logs := captureLogger()
	svc := &Service{logger: logger, acl: acl.NewService(slog.New(slog.DiscardHandler), queries)}
	state := newACLImportState(t, false)
	if err := svc.applyRestore(context.Background(), "user-1", skippedStepBotID, settings.Settings{}, newDependencyMap(), opts, state); err != nil {
		t.Fatalf("applyRestore() error = %v", err)
	}
	if !slices.Equal(state.warnings, []string{"acl rule skipped"}) {
		t.Fatalf("warnings = %q, want [\"acl rule skipped\"]", state.warnings)
	}
	assertNoCause(t, state.warnings)
	assertSkippedEvent(t, logs, importOperation, "acl_rule")

	// Create mode returns the failure for the caller to roll back; the
	// request's result record carries it, so no event is recorded.
	logger, logs = captureLogger()
	svc = &Service{logger: logger, acl: acl.NewService(slog.New(slog.DiscardHandler), queries)}
	state = newACLImportState(t, true)
	if err := svc.applyRestore(context.Background(), "user-1", skippedStepBotID, settings.Settings{}, newDependencyMap(), ImportOptions{Sections: opts.Sections}, state); err == nil {
		t.Fatal("create mode applyRestore() error = nil, want the ACL failure")
	}
	if len(state.warnings) != 0 {
		t.Fatalf("create mode warnings = %q, want none", state.warnings)
	}
	if records := warnRecords(t, logs); len(records) != 0 {
		t.Fatalf("create mode recorded %d events, want none: %s", len(records), logs.String())
	}
}

func newResourceLimitsImportState(t *testing.T, createMode bool) *importState {
	t.Helper()
	raw, err := json.Marshal(backupWorkspaceResourceLimits{CPUMillicores: 1000})
	if err != nil {
		t.Fatalf("marshal resource limits: %v", err)
	}
	return &importState{
		entries:    map[string]backupZipEntry{"bot/workspace_resource_limits.json": {data: raw}},
		counts:     map[Section]int{},
		botID:      skippedStepBotID,
		createMode: createMode,
	}
}

func TestRestoreStepRecordsCauseOnce(t *testing.T) {
	t.Parallel()

	queries := failingQueries{failing: "UpsertBotWorkspaceResourceLimits"}
	// The workspace section brings the resource limits along; the bundle has
	// no workspace archive, so nothing else is restored.
	opts := ImportOptions{Mode: ImportModeOverwrite, Sections: map[Section]ImportStrategy{SectionWorkspace: StrategyMerge}}

	logger, logs := captureLogger()
	svc := &Service{logger: logger, queries: queries}
	state := newResourceLimitsImportState(t, false)
	if err := svc.applyRestore(context.Background(), "user-1", skippedStepBotID, settings.Settings{}, newDependencyMap(), opts, state); err != nil {
		t.Fatalf("applyRestore() error = %v", err)
	}
	if !slices.Equal(state.warnings, []string{"workspace resource limits import failed"}) {
		t.Fatalf("warnings = %q, want [\"workspace resource limits import failed\"]", state.warnings)
	}
	assertNoCause(t, state.warnings)
	assertSkippedEvent(t, logs, importOperation, "workspace_resource_limits")

	logger, logs = captureLogger()
	svc = &Service{logger: logger, queries: queries}
	state = newResourceLimitsImportState(t, true)
	if err := svc.applyRestore(context.Background(), "user-1", skippedStepBotID, settings.Settings{}, newDependencyMap(), ImportOptions{Sections: opts.Sections}, state); err == nil {
		t.Fatal("create mode applyRestore() error = nil, want the resource limits failure")
	}
	if len(state.warnings) != 0 {
		t.Fatalf("create mode warnings = %q, want none", state.warnings)
	}
	if records := warnRecords(t, logs); len(records) != 0 {
		t.Fatalf("create mode recorded %d events, want none: %s", len(records), logs.String())
	}
}

// A workdir name belongs to the user and stays in the warning; the cause
// does not.
func TestRestoreWorkdirArchiveFlagWarningOmitsCause(t *testing.T) {
	t.Parallel()

	logger, logs := captureLogger()
	svc := &Service{logger: logger, workdirs: archiveFailingWorkdirStore{&fakeWorkdirStore{}}}
	state := newWorkdirImportState(t, []backupWorkdir{{ID: "src-archived", Name: "old", Path: "/data/site", Archived: true}})
	state.botID = skippedStepBotID

	if err := svc.restoreWorkdirs(context.Background(), skippedStepBotID, "user-1", state); err != nil {
		t.Fatalf("restoreWorkdirs() error = %v", err)
	}
	if want := []string{"workdir archive flag restore failed for old"}; !slices.Equal(state.warnings, want) {
		t.Fatalf("warnings = %q, want %q", state.warnings, want)
	}
	assertNoCause(t, state.warnings)
	assertSkippedEvent(t, logs, importOperation, "workdir_archive_flag")
}
