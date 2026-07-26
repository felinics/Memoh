package setting

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	settingpersistence "github.com/memohai/memoh/domains/api/bot/setting/persistence"
	apisqlc "github.com/memohai/memoh/domains/api/internal/postgres/sqlc"

	"github.com/memohai/memoh/internal/db"
)

const testBotID = "11111111-1111-1111-1111-111111111111"

type queriesFake struct {
	queries
	row          apisqlc.GetSettingsByBotIDRow
	upsertRow    apisqlc.UpsertBotSettingsRow
	overlayRow   apisqlc.GetBotOverlayConfigRow
	upsertParams apisqlc.UpsertBotSettingsParams
	err          error
}

func (f *queriesFake) GetSettingsByBotID(context.Context, pgtype.UUID) (apisqlc.GetSettingsByBotIDRow, error) {
	return f.row, f.err
}

func (f *queriesFake) GetBotOverlayConfig(context.Context, pgtype.UUID) (apisqlc.GetBotOverlayConfigRow, error) {
	return f.overlayRow, f.err
}

func (f *queriesFake) UpsertBotSettings(_ context.Context, params apisqlc.UpsertBotSettingsParams) (apisqlc.UpsertBotSettingsRow, error) {
	f.upsertParams = params
	return f.upsertRow, f.err
}

func TestRuntimeSettingsReaderReturnsTypedProjection(t *testing.T) {
	t.Parallel()

	store := newStore(&queriesFake{row: apisqlc.GetSettingsByBotIDRow{
		ToolApprovalConfig: []byte(`{"enabled":true,"read":{"mode":"deny"}}`),
		DisplayEnabled:     true,
	}})

	projection, err := store.FindBotRuntimeSettings(t.Context(), testBotID)
	if err != nil {
		t.Fatalf("FindBotRuntimeSettings() error = %v", err)
	}
	if !projection.DisplayEnabled || !projection.ToolApprovalConfig.Enabled {
		t.Fatalf("FindBotRuntimeSettings() = %#v", projection)
	}
	if projection.ToolApprovalConfig.Read.Mode != settingpersistence.ToolApprovalDeny {
		t.Fatalf("read mode = %q, want %q", projection.ToolApprovalConfig.Read.Mode, settingpersistence.ToolApprovalDeny)
	}
}

func TestGetOverlayReturnsRawProjection(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"exit_node":"100.64.0.10"}`)
	store := newStore(&queriesFake{overlayRow: apisqlc.GetBotOverlayConfigRow{
		OverlayEnabled:  true,
		OverlayProvider: "tailscale",
		OverlayConfig:   raw,
	}})
	got, err := store.GetOverlay(t.Context(), testBotID)
	if err != nil {
		t.Fatalf("GetOverlay() error = %v", err)
	}
	if !got.Enabled || got.Provider != "tailscale" || !bytes.Equal(got.Config, raw) {
		t.Fatalf("GetOverlay() = %#v", got)
	}
}

func TestGetReturnsPersistedOwnerReferences(t *testing.T) {
	t.Parallel()

	chatModelID := mustUUID(t, "22222222-2222-2222-2222-222222222222")
	memoryProviderID := mustUUID(t, "33333333-3333-3333-3333-333333333333")
	approval := []byte(`{"enabled":true}`)
	overlay := []byte(`{"exit_node":"node-a"}`)
	store := newStore(&queriesFake{row: apisqlc.GetSettingsByBotIDRow{
		ChatModelID:        chatModelID,
		MemoryProviderID:   memoryProviderID,
		ToolApprovalConfig: approval,
		OverlayConfig:      overlay,
	}})

	got, err := store.Get(t.Context(), testBotID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.ChatModelID != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("ChatModelID = %q", got.ChatModelID)
	}
	if got.MemoryProviderID != "33333333-3333-3333-3333-333333333333" {
		t.Fatalf("MemoryProviderID = %q", got.MemoryProviderID)
	}
	if !bytes.Equal(got.ToolApprovalConfig, approval) || !bytes.Equal(got.OverlayConfig, overlay) {
		t.Fatalf("raw configs = %q, %q", got.ToolApprovalConfig, got.OverlayConfig)
	}
	got.ToolApprovalConfig[0] = '!'
	if approval[0] == '!' {
		t.Fatal("Get() returned aliased config bytes")
	}
}

func TestUpsertUsesOwnerGeneratedParamsAndReturnsPersistedReferences(t *testing.T) {
	t.Parallel()

	persistedChatModelID := mustUUID(t, "44444444-4444-4444-4444-444444444444")
	fake := &queriesFake{upsertRow: apisqlc.UpsertBotSettingsRow{
		ChatModelID: persistedChatModelID,
	}}
	store := newStore(fake)

	got, err := store.Upsert(t.Context(), settingpersistence.UpsertInput{
		BotID:              testBotID,
		ChatModelID:        "44444444-4444-4444-4444-444444444444",
		MemoryProviderID:   "55555555-5555-5555-5555-555555555555",
		FetchProviderIDSet: true,
		FetchProviderID:    "66666666-6666-6666-6666-666666666666",
		ToolApprovalConfig: []byte(`{"enabled":true}`),
	})
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if got.ChatModelID != "44444444-4444-4444-4444-444444444444" {
		t.Fatalf("ChatModelID = %q", got.ChatModelID)
	}
	if db.UUIDString(fake.upsertParams.ID) != testBotID {
		t.Fatalf("upsert bot ID = %q", db.UUIDString(fake.upsertParams.ID))
	}
	if db.UUIDString(fake.upsertParams.MemoryProviderID) != "55555555-5555-5555-5555-555555555555" {
		t.Fatalf("memory provider ID = %q", db.UUIDString(fake.upsertParams.MemoryProviderID))
	}
	if !fake.upsertParams.FetchProviderIDSet || db.UUIDString(fake.upsertParams.FetchProviderID) != "66666666-6666-6666-6666-666666666666" {
		t.Fatalf("fetch provider params = %#v", fake.upsertParams)
	}
}

func TestMapQueryErrorPreservesPostgresCause(t *testing.T) {
	t.Parallel()

	err := mapQueryError(pgx.ErrNoRows)
	if !errors.Is(err, settingpersistence.ErrNotFound) || !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("mapQueryError() = %v", err)
	}
}

func mustUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	id, err := optionalUUID(value)
	if err != nil {
		t.Fatalf("optionalUUID(%q) error = %v", value, err)
	}
	return id
}
