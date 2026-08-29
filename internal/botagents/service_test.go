package botagents

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

const (
	testBotID   = "10000000-0000-4000-8000-000000000001"
	testAgentID = "20000000-0000-4000-8000-000000000002"
)

type fakeQueries struct {
	dbstore.Queries
	createParams sqlc.CreateBotAgentParams
	createRow    sqlc.BotAgent
	createErr    error
	getRow       sqlc.BotAgent
	getErr       error
	updateParams sqlc.UpdateBotAgentParams
	updateRow    sqlc.BotAgent
	updateErr    error
	deleteErr    error
	isDefault    bool
	transactions bool
	lockErr      error
	events       []string
}

func (f *fakeQueries) SupportsTransactions() bool { return f.transactions }

func (f *fakeQueries) InTx(_ context.Context, fn func(dbstore.Queries) error) error {
	f.events = append(f.events, "transaction")
	return fn(f)
}

func (f *fakeQueries) LockBotForAgentMutation(context.Context, pgtype.UUID) (pgtype.UUID, error) {
	f.events = append(f.events, "lock-bot")
	return testUUID(testBotID), f.lockErr
}

func (f *fakeQueries) BotAgentIsDefault(context.Context, sqlc.BotAgentIsDefaultParams) (bool, error) {
	return f.isDefault, nil
}

func (f *fakeQueries) CreateBotAgent(_ context.Context, params sqlc.CreateBotAgentParams) (sqlc.BotAgent, error) {
	f.createParams = params
	return f.createRow, f.createErr
}

func (*fakeQueries) FindActiveBotAgentByRuntimeProvider(context.Context, sqlc.FindActiveBotAgentByRuntimeProviderParams) (sqlc.BotAgent, error) {
	return sqlc.BotAgent{}, pgx.ErrNoRows
}

func (f *fakeQueries) GetBotAgentByID(context.Context, sqlc.GetBotAgentByIDParams) (sqlc.BotAgent, error) {
	return f.getRow, f.getErr
}

func (*fakeQueries) ListBotAgents(context.Context, pgtype.UUID) ([]sqlc.BotAgent, error) {
	return nil, nil
}

func (f *fakeQueries) SoftDeleteBotAgent(context.Context, sqlc.SoftDeleteBotAgentParams) (sqlc.BotAgent, error) {
	f.events = append(f.events, "delete-agent")
	return sqlc.BotAgent{}, f.deleteErr
}

func (f *fakeQueries) UpdateBotAgent(_ context.Context, params sqlc.UpdateBotAgentParams) (sqlc.BotAgent, error) {
	f.events = append(f.events, "update-agent")
	f.updateParams = params
	return f.updateRow, f.updateErr
}

func TestCreateNormalizesDescriptor(t *testing.T) {
	row := testRow(true)
	fake := &fakeQueries{createRow: row}
	service := NewService(slog.Default(), fake)

	created, err := service.Create(context.Background(), testBotID, CreateRequest{
		Name:    "  Primary Codex  ",
		Runtime: " ACP ",
		Metadata: map[string]any{
			MetadataProviderKey: " CODEX ",
			"future":            "kept",
		},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.ID != testAgentID {
		t.Fatalf("Create() ID = %q, want %q", created.ID, testAgentID)
	}
	if fake.createParams.Name != "Primary Codex" || fake.createParams.Runtime != RuntimeACP {
		t.Fatalf("Create() params = %#v", fake.createParams)
	}
	var metadata map[string]any
	if err := json.Unmarshal(fake.createParams.Metadata, &metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if metadata[MetadataProviderKey] != "codex" || metadata["future"] != "kept" {
		t.Fatalf("normalized metadata = %#v", metadata)
	}
}

func TestCreateRejectsUnsupportedDescriptors(t *testing.T) {
	service := NewService(slog.Default(), &fakeQueries{})
	tests := []struct {
		name string
		req  CreateRequest
		want error
	}{
		{name: "native row", req: CreateRequest{Name: "Native", Runtime: "native", Metadata: map[string]any{"provider": "codex"}}, want: ErrInvalidRuntime},
		{name: "unknown provider", req: CreateRequest{Name: "Other", Runtime: RuntimeACP, Metadata: map[string]any{"provider": "other"}}, want: ErrInvalidMetadata},
		{name: "missing provider", req: CreateRequest{Name: "Other", Runtime: RuntimeACP, Metadata: map[string]any{}}, want: ErrInvalidMetadata},
		{name: "blank name", req: CreateRequest{Name: " ", Runtime: RuntimeACP, Metadata: map[string]any{"provider": "codex"}}, want: ErrInvalidMetadata},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.Create(context.Background(), testBotID, tc.req)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Create() error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestGetActiveRejectsDisabledAgent(t *testing.T) {
	fake := &fakeQueries{getRow: testRow(false)}
	service := NewService(slog.Default(), fake)
	_, err := service.GetActive(context.Background(), testBotID, testAgentID)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("GetActive() error = %v, want %v", err, ErrUnavailable)
	}
}

func TestUpdateAndDeleteProtectDefaultAgent(t *testing.T) {
	falseValue := false
	fake := &fakeQueries{
		getRow:    testRow(true),
		updateErr: pgx.ErrNoRows,
		deleteErr: pgx.ErrNoRows,
		isDefault: true,
	}
	service := NewService(slog.Default(), fake)

	if _, err := service.Update(context.Background(), testBotID, testAgentID, UpdateRequest{Enabled: &falseValue}); !errors.Is(err, ErrDefaultInUse) {
		t.Fatalf("Update() error = %v, want %v", err, ErrDefaultInUse)
	}
	if err := service.Delete(context.Background(), testBotID, testAgentID); !errors.Is(err, ErrDefaultInUse) {
		t.Fatalf("Delete() error = %v, want %v", err, ErrDefaultInUse)
	}
}

func TestUpdateAndDeleteLockBotBeforeAgentMutation(t *testing.T) {
	falseValue := false
	updateFake := &fakeQueries{
		getRow:       testRow(true),
		updateRow:    testRow(false),
		transactions: true,
	}
	if _, err := NewService(slog.Default(), updateFake).Update(
		context.Background(), testBotID, testAgentID, UpdateRequest{Enabled: &falseValue},
	); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	assertEvents(t, updateFake.events, []string{"transaction", "lock-bot", "update-agent"})

	deleteFake := &fakeQueries{transactions: true}
	if err := NewService(slog.Default(), deleteFake).Delete(context.Background(), testBotID, testAgentID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	assertEvents(t, deleteFake.events, []string{"transaction", "lock-bot", "delete-agent"})
}

func assertEvents(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %v, want %v", got, want)
		}
	}
}

func TestDescriptorForUsesTemporaryMetadataProvider(t *testing.T) {
	descriptor, err := DescriptorFor(BotAgent{
		ID:       testAgentID,
		Runtime:  " ACP ",
		Metadata: map[string]any{"provider": " Claude-Code "},
	})
	if err != nil {
		t.Fatalf("DescriptorFor() error = %v", err)
	}
	if descriptor.BotAgentID != testAgentID || descriptor.Runtime != RuntimeACP || descriptor.Provider != "claude-code" {
		t.Fatalf("DescriptorFor() = %#v", descriptor)
	}
}

func testRow(enabled bool) sqlc.BotAgent {
	now := pgtype.Timestamptz{Time: time.Unix(1_700_000_000, 0).UTC(), Valid: true}
	return sqlc.BotAgent{
		ID:        testUUID(testAgentID),
		BotID:     testUUID(testBotID),
		Name:      "Primary Codex",
		Runtime:   RuntimeACP,
		Enabled:   enabled,
		Metadata:  []byte(`{"provider":"codex"}`),
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func testUUID(value string) pgtype.UUID {
	return pgtype.UUID{Bytes: uuid.MustParse(value), Valid: true}
}
