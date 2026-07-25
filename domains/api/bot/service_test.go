package bot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/memohai/memoh/domains/api/access/acl"
	runtimedomain "github.com/memohai/memoh/domains/runtime"
)

const (
	testOwnerID = "00000000-0000-0000-0000-000000000001"
	testBotID   = "00000000-0000-0000-0000-000000000002"
)

type botStoreFake struct {
	BotStore
	getByID func(context.Context, string) (Record, error)
	create  func(context.Context, CreateInput) (Record, error)
	update  func(context.Context, UpdateInput) (Record, error)
	status  func(context.Context, string, string) error
	delete  func(context.Context, string) error
}

func (f *botStoreFake) GetBotByID(ctx context.Context, id string) (Record, error) {
	if f.getByID == nil {
		return baseRecord(), nil
	}
	return f.getByID(ctx, id)
}

func (f *botStoreFake) CreateBot(ctx context.Context, input CreateInput) (Record, error) {
	if f.create == nil {
		return baseRecord(), nil
	}
	return f.create(ctx, input)
}

func (f *botStoreFake) UpdateBot(ctx context.Context, input UpdateInput) (Record, error) {
	if f.update == nil {
		row := baseRecord()
		row.Metadata = input.Metadata
		return row, nil
	}
	return f.update(ctx, input)
}

func (f *botStoreFake) UpdateBotStatus(ctx context.Context, id, status string) error {
	if f.status == nil {
		return nil
	}
	return f.status(ctx, id, status)
}

func (f *botStoreFake) DeleteBot(ctx context.Context, id string) error {
	if f.delete == nil {
		return nil
	}
	return f.delete(ctx, id)
}

type userReaderFake struct {
	get func(context.Context, string) (UserRecord, error)
}

func (f userReaderFake) GetUser(ctx context.Context, id string) (UserRecord, error) {
	return f.get(ctx, id)
}

type containerReaderFake struct {
	get func(context.Context, string) (ContainerRecord, error)
}

func (f containerReaderFake) GetContainerByBotID(ctx context.Context, id string) (ContainerRecord, error) {
	return f.get(ctx, id)
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

func baseRecord() Record {
	return Record{
		ID: testBotID, OwnerUserID: testOwnerID, Name: "test-bot",
		DisplayName: "Test Bot", IsActive: true, Status: BotStatusCreating,
		Metadata: []byte(`{}`),
	}
}

func TestAuthorizeAccess(t *testing.T) {
	store := &botStoreFake{getByID: func(context.Context, string) (Record, error) {
		return baseRecord(), nil
	}}
	svc := NewService(nil, store, nil, nil, nil)
	tests := []struct {
		name    string
		userID  string
		admin   bool
		wantErr error
	}{
		{name: "owner", userID: testOwnerID},
		{name: "admin", userID: "00000000-0000-0000-0000-000000000003", admin: true},
		{name: "stranger", userID: "00000000-0000-0000-0000-000000000003", wantErr: ErrBotAccessDenied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.AuthorizeAccess(t.Context(), tt.userID, testBotID, tt.admin)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("AuthorizeAccess() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestCreateRejectsUnknownACLPreset(t *testing.T) {
	createCalled := false
	store := &botStoreFake{create: func(context.Context, CreateInput) (Record, error) {
		createCalled = true
		return Record{}, nil
	}}
	users := userReaderFake{get: func(context.Context, string) (UserRecord, error) {
		return UserRecord{ID: testOwnerID}, nil
	}}
	svc := NewService(nil, store, nil, users, nil)
	_, err := svc.Create(t.Context(), testOwnerID, CreateBotRequest{DisplayName: "test", AclPreset: "unknown"})
	if !errors.Is(err, acl.ErrUnknownPreset) {
		t.Fatalf("Create() error = %v, want acl.ErrUnknownPreset", err)
	}
	if createCalled {
		t.Fatal("bot row should not be created")
	}
}

func TestCreateTreatsMissingOwnerAsError(t *testing.T) {
	users := userReaderFake{get: func(context.Context, string) (UserRecord, error) {
		return UserRecord{}, ErrOwnerUserNotFound
	}}
	svc := NewService(nil, &botStoreFake{}, nil, users, nil)
	_, err := svc.Create(t.Context(), testOwnerID, CreateBotRequest{})
	if !errors.Is(err, ErrOwnerUserNotFound) {
		t.Fatalf("Create() error = %v, want ErrOwnerUserNotFound", err)
	}
}

func TestRunCreateLifecycleSetsUpContainerBeforeReady(t *testing.T) {
	events := make([]string, 0, 2)
	store := &botStoreFake{status: func(_ context.Context, _, status string) error {
		events = append(events, status)
		return nil
	}}
	lifecycle := &fakeContainerLifecycle{onSetup: func() { events = append(events, "setup") }}
	svc := NewService(nil, store, nil, nil, nil)
	svc.SetContainerLifecycle(lifecycle)
	if err := svc.runCreateLifecycle(t.Context(), testBotID); err != nil {
		t.Fatalf("runCreateLifecycle() error = %v", err)
	}
	if got, want := strings.Join(events, ","), "setup,ready"; got != want {
		t.Fatalf("events = %q, want %q", got, want)
	}
}

func TestRunCreateLifecycleRecordsSetupFailureAndLeavesBotReady(t *testing.T) {
	events := make([]string, 0, 3)
	var persisted []byte
	store := &botStoreFake{
		getByID: func(context.Context, string) (Record, error) {
			row := baseRecord()
			row.Metadata = []byte(`{"workspace":{"image":"workspace:latest"},"keep":true}`)
			return row, nil
		},
		update: func(_ context.Context, input UpdateInput) (Record, error) {
			events = append(events, "metadata")
			persisted = append([]byte(nil), input.Metadata...)
			row := baseRecord()
			row.Metadata = input.Metadata
			return row, nil
		},
		status: func(context.Context, string, string) error {
			events = append(events, "status")
			return nil
		},
	}
	lifecycle := &fakeContainerLifecycle{
		onSetup:  func() { events = append(events, "setup") },
		setupErr: errors.New("pull https://user:pass@example.test/image?token=abc failed: dial tcp 127.0.0.1:7897"),
	}
	svc := NewService(nil, store, nil, nil, nil)
	svc.SetContainerLifecycle(lifecycle)
	if err := svc.runCreateLifecycle(t.Context(), testBotID); err != nil {
		t.Fatalf("runCreateLifecycle() error = %v", err)
	}
	if got, want := strings.Join(events, ","), "setup,metadata,status"; got != want {
		t.Fatalf("events = %q, want %q", got, want)
	}
	setupError := requireLastSetupError(t, persisted)
	message := setupError["message"].(string)
	if !strings.Contains(message, "127.0.0.1:7897") || strings.Contains(message, "user:pass") || strings.Contains(message, "abc") {
		t.Fatalf("sanitized message = %q", message)
	}
}

func TestRunCreateLifecycleReturnsContractErrorAfterLeavingBotReady(t *testing.T) {
	status := ""
	store := &botStoreFake{status: func(_ context.Context, _, next string) error {
		status = next
		return nil
	}}
	setupErr := errors.Join(runtimedomain.ErrWorkspaceImageIncompatible, errors.New("missing toolkit"))
	svc := NewService(nil, store, nil, nil, nil)
	svc.SetContainerLifecycle(&fakeContainerLifecycle{setupErr: setupErr})
	err := svc.runCreateLifecycle(t.Context(), testBotID)
	if !errors.Is(err, runtimedomain.ErrWorkspaceImageIncompatible) || status != BotStatusReady {
		t.Fatalf("error = %v, status = %q", err, status)
	}
}

func TestClearContainerSetupFailure(t *testing.T) {
	var persisted []byte
	store := &botStoreFake{
		getByID: func(context.Context, string) (Record, error) {
			row := baseRecord()
			row.Metadata = []byte(`{"workspace":{"image":"workspace:latest","last_setup_error":{"message":"old"}}}`)
			return row, nil
		},
		update: func(_ context.Context, input UpdateInput) (Record, error) {
			persisted = append([]byte(nil), input.Metadata...)
			return baseRecord(), nil
		},
	}
	svc := NewService(nil, store, nil, nil, nil)
	if err := svc.ClearContainerSetupFailure(t.Context(), testBotID); err != nil {
		t.Fatalf("ClearContainerSetupFailure() error = %v", err)
	}
	metadata := decodePersistedMetadata(t, persisted)
	workspaceMetadata := metadata["workspace"].(map[string]any)
	if _, ok := workspaceMetadata["last_setup_error"]; ok {
		t.Fatalf("last_setup_error was not removed: %#v", workspaceMetadata)
	}
}

func TestListChecksReportsSetupFailureAsSingleIssue(t *testing.T) {
	store := &botStoreFake{getByID: func(context.Context, string) (Record, error) {
		row := baseRecord()
		row.Status = BotStatusReady
		row.Metadata = []byte(`{"workspace":{"last_setup_error":{"phase":"setup","message":"image pull failed","at":"2026-06-08T10:00:00Z"}}}`)
		return row, nil
	}}
	containers := containerReaderFake{get: func(context.Context, string) (ContainerRecord, error) {
		return ContainerRecord{}, ErrContainerNotFound
	}}
	svc := NewService(nil, store, nil, nil, containers)
	checks, err := svc.ListChecks(t.Context(), testBotID)
	if err != nil {
		t.Fatalf("ListChecks() error = %v", err)
	}
	state, count := summarizeChecks(checks)
	if state != BotCheckStateIssue || count != 1 {
		t.Fatalf("summary = (%q, %d), want (%q, 1)", state, count, BotCheckStateIssue)
	}
}

func TestRecordContainerSetupFailureTruncatesLongMessages(t *testing.T) {
	var persisted []byte
	store := &botStoreFake{
		getByID: func(context.Context, string) (Record, error) { return baseRecord(), nil },
		update: func(_ context.Context, input UpdateInput) (Record, error) {
			persisted = append([]byte(nil), input.Metadata...)
			return baseRecord(), nil
		},
	}
	svc := NewService(nil, store, nil, nil, nil)
	if err := svc.RecordContainerSetupFailure(t.Context(), testBotID, "start", errors.New(strings.Repeat("x", 5000))); err != nil {
		t.Fatalf("RecordContainerSetupFailure() error = %v", err)
	}
	if message := requireLastSetupError(t, persisted)["message"].(string); len([]rune(message)) > 4096 {
		t.Fatalf("message length = %d", len([]rune(message)))
	}
}

func TestSanitizeDiagnosticMessage(t *testing.T) {
	message := sanitizeDiagnosticMessage("dial https://admin:secret@example.com?token=abc123", "fallback")
	if message != "dial https://***:***@example.com?token=***" {
		t.Fatalf("message = %q", message)
	}
}

func requireLastSetupError(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	metadata := decodePersistedMetadata(t, payload)
	return metadata["workspace"].(map[string]any)["last_setup_error"].(map[string]any)
}

func decodePersistedMetadata(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	var metadata map[string]any
	if err := json.Unmarshal(payload, &metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	return metadata
}
