package bots

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/acl"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/workspace"
)

// fakeRow implements pgx.Row with a custom scan function.
type fakeRow struct {
	scanFunc func(dest ...any) error
}

func (r *fakeRow) Scan(dest ...any) error {
	return r.scanFunc(dest...)
}

// fakeDBTX implements sqlc.DBTX for unit testing.
type fakeDBTX struct {
	queryRowFunc func(ctx context.Context, sql string, args ...any) pgx.Row
	execFunc     func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func (d *fakeDBTX) Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
	if d.execFunc != nil {
		return d.execFunc(ctx, sql, args...)
	}
	return pgconn.CommandTag{}, nil
}

func (*fakeDBTX) Query(context.Context, string, ...interface{}) (pgx.Rows, error) {
	return nil, nil
}

func (d *fakeDBTX) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if d.queryRowFunc != nil {
		return d.queryRowFunc(ctx, sql, args...)
	}
	return &fakeRow{scanFunc: func(_ ...any) error { return pgx.ErrNoRows }}
}

// makeBotRow creates a fakeRow that populates a sqlc.GetBotByIDRow via Scan.
// Column order: id, owner_user_id, name, display_name, avatar_url, timezone, is_active, status,
// reasoning_effort,
// chat_model_id, search_provider_id, memory_provider_id,
// compaction_enabled, compaction_threshold, compaction_target_percent, compaction_model_id,
// metadata, created_at, updated_at.
func makeBotRow(botID, ownerUserID pgtype.UUID) *fakeRow {
	return &fakeRow{
		scanFunc: func(dest ...any) error {
			if len(dest) < 19 {
				return pgx.ErrNoRows
			}
			*dest[0].(*pgtype.UUID) = botID
			*dest[1].(*pgtype.UUID) = ownerUserID
			*dest[2].(*string) = "test-bot" // Name
			*dest[3].(*pgtype.Text) = pgtype.Text{String: "test-bot", Valid: true}
			*dest[4].(*pgtype.Text) = pgtype.Text{}
			*dest[5].(*pgtype.Text) = pgtype.Text{}
			*dest[6].(*bool) = true
			*dest[7].(*string) = BotStatusReady
			*dest[8].(*string) = "medium"            // ReasoningEffort
			*dest[9].(*pgtype.UUID) = pgtype.UUID{}  // ChatModelID
			*dest[10].(*pgtype.UUID) = pgtype.UUID{} // SearchProviderID
			*dest[11].(*pgtype.UUID) = pgtype.UUID{} // MemoryProviderID
			*dest[12].(*bool) = false                // CompactionEnabled
			*dest[13].(*int32) = 100000              // CompactionThreshold
			*dest[14].(*pgtype.Int4) = pgtype.Int4{} // CompactionTargetPercent
			*dest[15].(*pgtype.UUID) = pgtype.UUID{} // CompactionModelID
			*dest[16].(*[]byte) = []byte(`{}`)
			*dest[17].(*pgtype.Timestamptz) = pgtype.Timestamptz{}
			*dest[18].(*pgtype.Timestamptz) = pgtype.Timestamptz{}
			return nil
		},
	}
}

func mustParseUUID(s string) pgtype.UUID {
	var u pgtype.UUID
	_ = u.Scan(s)
	return u
}

func TestAuthorizeAccess(t *testing.T) {
	ownerUUID := mustParseUUID("00000000-0000-0000-0000-000000000001")
	botUUID := mustParseUUID("00000000-0000-0000-0000-000000000002")
	strangerUUID := mustParseUUID("00000000-0000-0000-0000-000000000003")
	ownerID := ownerUUID.String()
	botID := botUUID.String()
	strangerID := strangerUUID.String()

	tests := []struct {
		name      string
		userID    string
		isAdmin   bool
		wantErr   bool
		wantErrIs error
	}{
		{
			name:    "owner always allowed",
			userID:  ownerID,
			wantErr: false,
		},
		{
			name:    "admin always allowed",
			userID:  strangerID,
			isAdmin: true,
			wantErr: false,
		},
		{
			name:      "stranger denied",
			userID:    strangerID,
			wantErr:   true,
			wantErrIs: ErrBotAccessDenied,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := &fakeDBTX{
				queryRowFunc: func(_ context.Context, _ string, args ...any) pgx.Row {
					_ = args
					return makeBotRow(botUUID, ownerUUID)
				},
			}
			svc := NewService(nil, postgresstore.NewQueries(sqlc.New(db)))

			_, err := svc.AuthorizeAccess(context.Background(), tt.userID, botID, tt.isAdmin)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.wantErrIs != nil && err.Error() != tt.wantErrIs.Error() {
					t.Fatalf("expected error %q, got %q", tt.wantErrIs, err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestCreateRejectsUnknownACLPreset(t *testing.T) {
	ownerUUID := mustParseUUID("00000000-0000-0000-0000-000000000001")
	createCalled := false

	db := &fakeDBTX{
		queryRowFunc: func(_ context.Context, sql string, _ ...any) pgx.Row {
			switch {
			case strings.Contains(sql, "FROM users") && strings.Contains(sql, "id = $1"):
				return &fakeRow{scanFunc: func(_ ...any) error { return nil }}
			case strings.Contains(sql, "INSERT INTO bots"):
				createCalled = true
				return &fakeRow{scanFunc: func(_ ...any) error { return nil }}
			default:
				return &fakeRow{scanFunc: func(_ ...any) error { return pgx.ErrNoRows }}
			}
		},
	}

	svc := NewService(nil, postgresstore.NewQueries(sqlc.New(db)))
	_, err := svc.Create(context.Background(), ownerUUID.String(), CreateBotRequest{
		DisplayName: "test-bot",
		AclPreset:   "not_a_real_preset",
	})
	if !errors.Is(err, acl.ErrUnknownPreset) {
		t.Fatalf("expected ErrUnknownPreset, got %v", err)
	}
	if createCalled {
		t.Fatal("bot row should not be created when acl preset is invalid")
	}
}

func TestCreateTreatsStoreNotFoundAsMissingOwner(t *testing.T) {
	ownerUUID := mustParseUUID("00000000-0000-0000-0000-000000000001")
	createCalled := false

	dbtx := &fakeDBTX{
		queryRowFunc: func(_ context.Context, sql string, _ ...any) pgx.Row {
			switch {
			case strings.Contains(sql, "FROM users") && strings.Contains(sql, "id = $1"):
				return &fakeRow{scanFunc: func(_ ...any) error { return db.ErrNotFound }}
			case strings.Contains(sql, "INSERT INTO bots"):
				createCalled = true
				return &fakeRow{scanFunc: func(_ ...any) error { return nil }}
			default:
				return &fakeRow{scanFunc: func(_ ...any) error { return pgx.ErrNoRows }}
			}
		},
	}

	svc := NewService(nil, postgresstore.NewQueries(sqlc.New(dbtx)))
	_, err := svc.Create(context.Background(), ownerUUID.String(), CreateBotRequest{
		DisplayName: "test-bot",
		AclPreset:   "allow_all",
	})
	if !errors.Is(err, ErrOwnerUserNotFound) {
		t.Fatalf("expected ErrOwnerUserNotFound, got %v", err)
	}
	if createCalled {
		t.Fatal("bot row should not be created when owner is missing")
	}
}

func TestListChecksReportsSetupFailureAsSingleIssue(t *testing.T) {
	botUUID := mustParseUUID("00000000-0000-0000-0000-000000000002")
	ownerUUID := mustParseUUID("00000000-0000-0000-0000-000000000001")
	metadata := []byte(`{"workspace":{"last_setup_error":{"phase":"setup","message":"image pull failed: proxyconnect tcp: dial tcp 127.0.0.1:7897: connect: connection refused","at":"2026-06-08T10:00:00Z"}}}`)

	db := &fakeDBTX{
		queryRowFunc: func(_ context.Context, query string, _ ...any) pgx.Row {
			switch {
			case strings.Contains(query, "SELECT id, owner_user_id") && strings.Contains(query, "FROM bots"):
				return makeGetBotRowWithMetadata(botUUID, ownerUUID, metadata)
			case strings.Contains(query, "FROM containers"):
				return &fakeRow{scanFunc: func(_ ...any) error { return pgx.ErrNoRows }}
			default:
				t.Fatalf("unexpected query: %s", query)
				return &fakeRow{scanFunc: func(_ ...any) error { return pgx.ErrNoRows }}
			}
		},
	}
	svc := NewService(nil, postgresstore.NewQueries(sqlc.New(db)))

	checks, err := svc.ListChecks(context.Background(), botUUID.String())
	if err != nil {
		t.Fatalf("ListChecks() error = %v", err)
	}
	initCheck := findBotCheck(t, checks, BotCheckTypeContainerInit)
	if initCheck.Status != BotCheckStatusError {
		t.Fatalf("container.init status = %q, want error", initCheck.Status)
	}
	if !strings.Contains(initCheck.Detail, "127.0.0.1:7897") {
		t.Fatalf("container.init detail = %q, want setup failure detail", initCheck.Detail)
	}
	recordCheck := findBotCheck(t, checks, BotCheckTypeContainerRecord)
	if recordCheck.Status != BotCheckStatusUnknown {
		t.Fatalf("container.record status = %q, want unknown", recordCheck.Status)
	}
	state, issueCount := summarizeChecks(checks)
	if state != BotCheckStateIssue || issueCount != 1 {
		t.Fatalf("summary = (%q, %d), want (%q, 1); checks=%#v", state, issueCount, BotCheckStateIssue, checks)
	}
}

func TestRecordContainerSetupFailureTruncatesLongMessages(t *testing.T) {
	botUUID := mustParseUUID("00000000-0000-0000-0000-000000000002")
	ownerUUID := mustParseUUID("00000000-0000-0000-0000-000000000001")
	var persisted []byte

	db := &fakeDBTX{
		queryRowFunc: func(_ context.Context, query string, args ...any) pgx.Row {
			switch {
			case strings.Contains(query, "SELECT id, owner_user_id") && strings.Contains(query, "FROM bots"):
				return makeGetBotRowWithMetadata(botUUID, ownerUUID, []byte(`{}`))
			case strings.Contains(query, "UPDATE bots") && strings.Contains(query, "metadata = $7"):
				payload, ok := args[6].([]byte)
				if !ok {
					t.Fatalf("metadata arg type = %T, want []byte", args[6])
				}
				persisted = append([]byte(nil), payload...)
				return makeUpdateBotProfileRowWithMetadata(botUUID, ownerUUID, payload)
			default:
				t.Fatalf("unexpected query: %s", query)
				return &fakeRow{scanFunc: func(_ ...any) error { return pgx.ErrNoRows }}
			}
		},
	}
	svc := NewService(nil, postgresstore.NewQueries(sqlc.New(db)))

	longMessage := strings.Repeat("x", 5000)
	if err := svc.RecordContainerSetupFailure(context.Background(), botUUID.String(), "start", errors.New(longMessage)); err != nil {
		t.Fatalf("RecordContainerSetupFailure() error = %v", err)
	}

	setupError := requireLastSetupError(t, persisted)
	message, _ := setupError["message"].(string)
	if len([]rune(message)) > 4096 {
		t.Fatalf("message length = %d, want <= 4096", len([]rune(message)))
	}
}

func TestSanitizeDiagnosticMessagePreservesTechnicalTerminology(t *testing.T) {
	message := sanitizeDiagnosticMessage("container runtime failed", "workspace operation failed")
	if message != "container runtime failed" {
		t.Fatalf("message = %q, want technical terminology preserved", message)
	}
}

func TestSanitizeDiagnosticMessageRedactsSecrets(t *testing.T) {
	message := sanitizeDiagnosticMessage("dial https://admin:secret@example.com?token=abc123", "workspace operation failed")
	if message != "dial https://***:***@example.com?token=***" {
		t.Fatalf("message = %q, want credentials and token redacted", message)
	}
}

func TestSanitizeDiagnosticMessageUsesCallerFallback(t *testing.T) {
	message := sanitizeDiagnosticMessage("", "workspace reachability check failed")
	if message != "workspace reachability check failed" {
		t.Fatalf("message = %q, want caller fallback", message)
	}
}

func findBotCheck(t *testing.T, checks []BotCheck, id string) BotCheck {
	t.Helper()
	for _, check := range checks {
		if check.ID == id {
			return check
		}
	}
	t.Fatalf("missing check %q in %#v", id, checks)
	return BotCheck{}
}

func requireLastSetupError(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	metadata := decodePersistedMetadata(t, payload)
	workspace, ok := metadata["workspace"].(map[string]any)
	if !ok {
		t.Fatalf("workspace metadata missing: %#v", metadata)
	}
	setupError, ok := workspace["last_setup_error"].(map[string]any)
	if !ok {
		t.Fatalf("last_setup_error missing: %#v", workspace)
	}
	return setupError
}

func decodePersistedMetadata(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	var metadata map[string]any
	if err := json.Unmarshal(payload, &metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	return metadata
}

func TestResolveNameSuffixesDerivedNameCollisions(t *testing.T) {
	taken := map[string]bool{"neko": true, "neko-2": true}
	ownerUUID := mustParseUUID("00000000-0000-0000-0000-000000000001")

	dbtx := &fakeDBTX{
		queryRowFunc: func(_ context.Context, sql string, args ...any) pgx.Row {
			if strings.Contains(sql, "FROM bots") && strings.Contains(sql, "name = $1") {
				name, _ := args[0].(string)
				if taken[name] {
					return makeBotRow(mustParseUUID("00000000-0000-0000-0000-0000000000aa"), ownerUUID)
				}
			}
			return &fakeRow{scanFunc: func(_ ...any) error { return pgx.ErrNoRows }}
		},
	}

	svc := NewService(nil, postgresstore.NewQueries(sqlc.New(dbtx)))

	// Derived names walk suffixes until free.
	got, err := svc.resolveName(context.Background(), "", "Neko", "")
	if err != nil {
		t.Fatalf("resolveName derived: %v", err)
	}
	if got != "neko-3" {
		t.Fatalf("expected neko-3, got %q", got)
	}

	// Free derived names stay unsuffixed.
	got, err = svc.resolveName(context.Background(), "", "Mimi", "")
	if err != nil {
		t.Fatalf("resolveName free derived: %v", err)
	}
	if got != "mimi" {
		t.Fatalf("expected mimi, got %q", got)
	}

	// Explicitly requested names still fail when taken.
	if _, err := svc.resolveName(context.Background(), "neko", "", ""); !errors.Is(err, ErrBotNameTaken) {
		t.Fatalf("expected ErrBotNameTaken, got %v", err)
	}
}

// fakeWorkspaceIntents records intents and answers awaits from a script.
type fakeWorkspaceIntents struct {
	ensured  []string
	images   []string
	absent   []string
	preserve []bool
	outcome  WorkspaceOutcome
	awaitErr error
}

func (f *fakeWorkspaceIntents) EnsurePresent(_ context.Context, botID, image string) (int64, error) {
	f.ensured = append(f.ensured, botID)
	f.images = append(f.images, image)
	return int64(len(f.ensured)), nil
}

func (f *fakeWorkspaceIntents) RequestAbsent(_ context.Context, botID string, preserve bool) (int64, error) {
	f.absent = append(f.absent, botID)
	f.preserve = append(f.preserve, preserve)
	return int64(len(f.absent)), nil
}

func (f *fakeWorkspaceIntents) AwaitSettled(context.Context, string, int64) (WorkspaceOutcome, error) {
	return f.outcome, f.awaitErr
}

func (f *fakeWorkspaceIntents) Current(context.Context, string) (WorkspaceOutcome, bool, error) {
	return f.outcome, true, nil
}

func TestWorkspaceOutcomeErrorKeepsBootstrapSentinel(t *testing.T) {
	err := workspaceOutcomeError(WorkspaceOutcome{Observed: WorkspaceObservedFailed, LastErrorPhase: WorkspacePhaseBootstrap, LastError: "write AGENTS.md: permission denied"})
	if !errors.Is(err, workspace.ErrWorkspaceTemplateBootstrapFailed) {
		t.Fatalf("bootstrap failure must map to the template sentinel, got %v", err)
	}
	err = workspaceOutcomeError(WorkspaceOutcome{Observed: WorkspaceObservedFailed, LastErrorPhase: "image_prepare", LastError: "pull access denied"})
	if errors.Is(err, workspace.ErrWorkspaceTemplateBootstrapFailed) {
		t.Fatalf("image failure must not map to the template sentinel, got %v", err)
	}
	if !strings.Contains(err.Error(), "image_prepare") || !strings.Contains(err.Error(), "pull access denied") {
		t.Fatalf("error should carry phase and message, got %v", err)
	}
}

func TestWorkspaceImageFromMetadata(t *testing.T) {
	if got := workspaceImageFromMetadata(map[string]any{"workspace": map[string]any{"image": "  ghcr.io/x/y:1 "}}); got != "ghcr.io/x/y:1" {
		t.Fatalf("image = %q", got)
	}
	if got := workspaceImageFromMetadata(map[string]any{"workspace": "nope"}); got != "" {
		t.Fatalf("malformed section should yield empty image, got %q", got)
	}
	if got := workspaceImageFromMetadata(nil); got != "" {
		t.Fatalf("nil metadata should yield empty image, got %q", got)
	}
}

func TestSetBotStatusFromWorkspaceRespectsDeleting(t *testing.T) {
	botUUID := mustParseUUID("00000000-0000-0000-0000-000000000002")
	ownerUUID := mustParseUUID("00000000-0000-0000-0000-000000000001")
	current := BotStatusDeleting
	updates := 0
	db := &fakeDBTX{
		queryRowFunc: func(_ context.Context, _ string, _ ...any) pgx.Row {
			row := makeBotRow(botUUID, ownerUUID)
			inner := row.scanFunc
			row.scanFunc = func(dest ...any) error {
				if err := inner(dest...); err != nil {
					return err
				}
				*dest[7].(*string) = current
				return nil
			}
			return row
		},
		execFunc: func(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
			if strings.Contains(query, "UPDATE bots") && strings.Contains(query, "SET status = $2") {
				updates++
				current = args[1].(string)
			}
			return pgconn.CommandTag{}, nil
		},
	}
	svc := NewService(nil, postgresstore.NewQueries(sqlc.New(db)))
	if err := svc.SetBotStatusFromWorkspace(context.Background(), botUUID.String(), BotStatusReady); err != nil {
		t.Fatal(err)
	}
	if updates != 0 || current != BotStatusDeleting {
		t.Fatalf("a deleting bot must keep its status; updates=%d status=%q", updates, current)
	}
	current = BotStatusCreating
	if err := svc.SetBotStatusFromWorkspace(context.Background(), botUUID.String(), BotStatusFailed); err != nil {
		t.Fatal(err)
	}
	if updates != 1 || current != BotStatusFailed {
		t.Fatalf("creating -> failed should be written once; updates=%d status=%q", updates, current)
	}
	if err := svc.SetBotStatusFromWorkspace(context.Background(), botUUID.String(), BotStatusFailed); err != nil {
		t.Fatal(err)
	}
	if updates != 1 {
		t.Fatalf("unchanged status must not be rewritten; updates=%d", updates)
	}
}

func TestRunDeleteLifecycleWaitsForWorkspaceAbsent(t *testing.T) {
	botUUID := mustParseUUID("00000000-0000-0000-0000-000000000002")
	botID := botUUID.String()
	var exec []string
	db := &fakeDBTX{
		execFunc: func(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
			switch {
			case strings.Contains(query, "UPDATE bots") && strings.Contains(query, "SET status = $2"):
				exec = append(exec, "status:"+args[1].(string))
			case strings.Contains(query, "DELETE FROM bots"):
				exec = append(exec, "delete")
			}
			return pgconn.CommandTag{}, nil
		},
	}
	intents := &fakeWorkspaceIntents{outcome: WorkspaceOutcome{Desired: "absent", Observed: WorkspaceObservedAbsent}}
	svc := NewService(nil, postgresstore.NewQueries(sqlc.New(db)))
	svc.SetWorkspaceIntents(intents)

	svc.runDeleteLifecycle(context.Background(), botID, BotStatusReady)
	if len(intents.absent) != 1 || intents.absent[0] != botID || intents.preserve[0] {
		t.Fatalf("delete must request a non-preserving absent workspace, got %v/%v", intents.absent, intents.preserve)
	}
	if len(exec) == 0 || exec[len(exec)-1] != "delete" {
		t.Fatalf("bot row should be deleted after the workspace is absent; exec=%v", exec)
	}
	for _, e := range exec {
		if strings.HasPrefix(e, "status:") {
			t.Fatalf("no status revert expected on success; exec=%v", exec)
		}
	}
}

func TestRunDeleteLifecycleRevertsToPreviousStatusWhenWorkspaceLingers(t *testing.T) {
	botUUID := mustParseUUID("00000000-0000-0000-0000-000000000002")
	botID := botUUID.String()
	var exec []string
	db := &fakeDBTX{
		execFunc: func(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
			switch {
			case strings.Contains(query, "UPDATE bots") && strings.Contains(query, "SET status = $2"):
				exec = append(exec, "status:"+args[1].(string))
			case strings.Contains(query, "DELETE FROM bots"):
				exec = append(exec, "delete")
			}
			return pgconn.CommandTag{}, nil
		},
	}
	// The reconciler is still fighting a transient backend error.
	intents := &fakeWorkspaceIntents{outcome: WorkspaceOutcome{Desired: "absent", Observed: "removing", LastError: "operation in flight"}}
	svc := NewService(nil, postgresstore.NewQueries(sqlc.New(db)))
	svc.SetWorkspaceIntents(intents)

	svc.runDeleteLifecycle(context.Background(), botID, BotStatusFailed)
	if len(exec) != 1 || exec[0] != "status:"+BotStatusFailed {
		t.Fatalf("a failed bot whose deletion lingers must revert to failed, not ready, and not be deleted; exec=%v", exec)
	}
}
