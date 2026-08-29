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
// language, reasoning_effort,
// chat_model_id, search_provider_id, memory_provider_id,
// compaction_enabled, compaction_threshold, compaction_target_percent, compaction_model_id,
// metadata, created_at, updated_at.
func makeBotRow(botID, ownerUserID pgtype.UUID) *fakeRow {
	return &fakeRow{
		scanFunc: func(dest ...any) error {
			if len(dest) < 20 {
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
			*dest[8].(*string) = "en"                // Language
			*dest[9].(*string) = "medium"            // ReasoningEffort
			*dest[10].(*pgtype.UUID) = pgtype.UUID{} // ChatModelID
			*dest[11].(*pgtype.UUID) = pgtype.UUID{} // SearchProviderID
			*dest[12].(*pgtype.UUID) = pgtype.UUID{} // MemoryProviderID
			*dest[13].(*bool) = false                // CompactionEnabled
			*dest[14].(*int32) = 100000              // CompactionThreshold
			*dest[15].(*pgtype.Int4) = pgtype.Int4{} // CompactionTargetPercent
			*dest[16].(*pgtype.UUID) = pgtype.UUID{} // CompactionModelID
			*dest[17].(*[]byte) = []byte(`{}`)
			*dest[18].(*pgtype.Timestamptz) = pgtype.Timestamptz{}
			*dest[19].(*pgtype.Timestamptz) = pgtype.Timestamptz{}
			return nil
		},
	}
}

func mustParseUUID(s string) pgtype.UUID {
	var u pgtype.UUID
	_ = u.Scan(s)
	return u
}

type fakeContainerLifecycle struct {
	onSetup    func()
	setupBotID string
	setupErr   error
}

func (f *fakeContainerLifecycle) SetupBotContainer(_ context.Context, botID string) error {
	if f.onSetup != nil {
		f.onSetup()
	}
	f.setupBotID = botID
	return f.setupErr
}

func (*fakeContainerLifecycle) CleanupBotContainer(context.Context, string, bool) error {
	return nil
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

func TestRunCreateLifecycleSetsUpContainerBeforeReady(t *testing.T) {
	botUUID := mustParseUUID("00000000-0000-0000-0000-000000000002")
	botID := botUUID.String()
	events := make([]string, 0, 2)

	db := &fakeDBTX{
		execFunc: func(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
			if strings.Contains(sql, "UPDATE bots") && strings.Contains(sql, "SET status = $2") {
				events = append(events, "status")
				if got := args[0].(pgtype.UUID); got != botUUID {
					t.Fatalf("expected status update for %s, got %s", botUUID, got)
				}
				if got := args[1].(string); got != BotStatusReady {
					t.Fatalf("expected status %q, got %q", BotStatusReady, got)
				}
			}
			return pgconn.CommandTag{}, nil
		},
	}
	lifecycle := &fakeContainerLifecycle{
		onSetup: func() {
			events = append(events, "setup")
		},
	}
	svc := NewService(nil, postgresstore.NewQueries(sqlc.New(db)))
	svc.SetContainerLifecycle(lifecycle)

	if err := svc.runCreateLifecycle(context.Background(), botID); err != nil {
		t.Fatalf("run create lifecycle: %v", err)
	}
	if lifecycle.setupBotID != botID {
		t.Fatalf("expected setup for bot %s, got %s", botID, lifecycle.setupBotID)
	}
	if len(events) != 2 || events[0] != "setup" || events[1] != "status" {
		t.Fatalf("expected setup before ready status update, got events %v", events)
	}
}

func TestRunCreateLifecycleRecordsSetupFailureAndLeavesBotReady(t *testing.T) {
	botUUID := mustParseUUID("00000000-0000-0000-0000-000000000002")
	ownerUUID := mustParseUUID("00000000-0000-0000-0000-000000000001")
	botID := botUUID.String()
	events := make([]string, 0, 3)
	var persisted []byte

	db := &fakeDBTX{
		queryRowFunc: func(_ context.Context, query string, args ...any) pgx.Row {
			switch {
			case strings.Contains(query, "SELECT id, owner_user_id") && strings.Contains(query, "FROM bots"):
				return makeGetBotRowWithMetadata(botUUID, ownerUUID, []byte(`{"workspace":{"image":"ghcr.io/felinics/workspace:latest"},"keep":true}`))
			case strings.Contains(query, "UPDATE bots") && strings.Contains(query, "metadata = $7"):
				events = append(events, "metadata")
				if got := args[1].(string); got != "test-bot" {
					t.Fatalf("expected update to preserve bot name, got %q", got)
				}
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
		execFunc: func(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
			if strings.Contains(query, "UPDATE bots") && strings.Contains(query, "SET status = $2") {
				events = append(events, "status")
				if got := args[1].(string); got != BotStatusReady {
					t.Fatalf("expected bot to remain %q, got %q", BotStatusReady, got)
				}
			}
			return pgconn.CommandTag{}, nil
		},
	}
	lifecycle := &fakeContainerLifecycle{
		onSetup: func() {
			events = append(events, "setup")
		},
		setupErr: errors.New("pull https://user:pass@registry.example.test/image?token=abc123 failed: proxyconnect tcp: dial tcp 127.0.0.1:7897: connect: connection refused"),
	}
	svc := NewService(nil, postgresstore.NewQueries(sqlc.New(db)))
	svc.SetContainerLifecycle(lifecycle)

	if err := svc.runCreateLifecycle(context.Background(), botID); err != nil {
		t.Fatalf("run create lifecycle: %v", err)
	}
	if len(events) != 3 || events[0] != "setup" || events[1] != "metadata" || events[2] != "status" {
		t.Fatalf("expected setup, metadata, status events, got %v", events)
	}

	setupError := requireLastSetupError(t, persisted)
	if setupError["phase"] != "setup" {
		t.Fatalf("phase = %#v, want setup", setupError["phase"])
	}
	message, _ := setupError["message"].(string)
	if !strings.Contains(message, "127.0.0.1:7897") {
		t.Fatalf("message = %q, want proxy failure details", message)
	}
	if strings.Contains(message, "user:pass") || strings.Contains(message, "abc123") {
		t.Fatalf("message should redact credentials and tokens, got %q", message)
	}
}

func TestRunCreateLifecycleReturnsContractErrorAfterLeavingBotReady(t *testing.T) {
	botUUID := mustParseUUID("00000000-0000-0000-0000-000000000002")
	botID := botUUID.String()
	status := ""

	db := &fakeDBTX{
		queryRowFunc: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return &fakeRow{scanFunc: func(_ ...any) error { return pgx.ErrNoRows }}
		},
		execFunc: func(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
			if strings.Contains(query, "UPDATE bots") && strings.Contains(query, "SET status = $2") {
				status = args[1].(string)
			}
			return pgconn.CommandTag{}, nil
		},
	}
	setupErr := errors.Join(
		workspace.ErrWorkspaceImageIncompatible,
		errors.New("missing /opt/memoh/toolkit/bin/node"),
	)
	svc := NewService(nil, postgresstore.NewQueries(sqlc.New(db)))
	svc.SetContainerLifecycle(&fakeContainerLifecycle{setupErr: setupErr})

	err := svc.runCreateLifecycle(context.Background(), botID)
	if !errors.Is(err, workspace.ErrWorkspaceImageIncompatible) {
		t.Fatalf("run create lifecycle error = %v, want image incompatibility", err)
	}
	if status != BotStatusReady {
		t.Fatalf("bot status = %q, want %q", status, BotStatusReady)
	}
}

func TestRunCreateLifecycleClearsSetupFailureAfterSuccess(t *testing.T) {
	botUUID := mustParseUUID("00000000-0000-0000-0000-000000000002")
	ownerUUID := mustParseUUID("00000000-0000-0000-0000-000000000001")
	botID := botUUID.String()
	var persisted []byte

	db := &fakeDBTX{
		queryRowFunc: func(_ context.Context, query string, args ...any) pgx.Row {
			switch {
			case strings.Contains(query, "SELECT id, owner_user_id") && strings.Contains(query, "FROM bots"):
				return makeGetBotRowWithMetadata(botUUID, ownerUUID, []byte(`{"workspace":{"image":"ghcr.io/felinics/workspace:latest","last_setup_error":{"phase":"setup","message":"old failure","at":"2026-06-08T10:00:00Z"}}}`))
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
	svc.SetContainerLifecycle(&fakeContainerLifecycle{})

	if err := svc.runCreateLifecycle(context.Background(), botID); err != nil {
		t.Fatalf("run create lifecycle: %v", err)
	}

	metadata := decodePersistedMetadata(t, persisted)
	workspace := metadata["workspace"].(map[string]any)
	if _, ok := workspace["last_setup_error"]; ok {
		t.Fatalf("last_setup_error should be cleared, metadata=%#v", metadata)
	}
	if workspace["image"] != "ghcr.io/felinics/workspace:latest" {
		t.Fatalf("workspace image was not preserved: %#v", workspace)
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
