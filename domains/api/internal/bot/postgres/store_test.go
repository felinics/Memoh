package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/domains/api/bot"
	apisqlc "github.com/memohai/memoh/domains/api/internal/postgres/sqlc"
	"github.com/memohai/memoh/domains/runtime/workspace"
	"github.com/memohai/memoh/internal/db"
)

const testBotID = "11111111-1111-1111-1111-111111111111"

type queriesFake struct {
	queries
	getBot        func(context.Context, pgtype.UUID) (apisqlc.GetBotByIDRow, error)
	updateProfile func(context.Context, apisqlc.UpdateBotProfileParams) (apisqlc.UpdateBotProfileRow, error)
}

func (f queriesFake) GetBotByID(ctx context.Context, id pgtype.UUID) (apisqlc.GetBotByIDRow, error) {
	return f.getBot(ctx, id)
}

func (f queriesFake) UpdateBotProfile(ctx context.Context, params apisqlc.UpdateBotProfileParams) (apisqlc.UpdateBotProfileRow, error) {
	return f.updateProfile(ctx, params)
}

func TestWorkspacePreferencesPreserveBotProfileAndUnrelatedMetadata(t *testing.T) {
	t.Parallel()

	id := mustUUID(t, testBotID)
	row := apisqlc.GetBotByIDRow{
		ID:          id,
		Name:        "agent",
		DisplayName: pgtype.Text{},
		AvatarUrl:   pgtype.Text{String: "avatar", Valid: true},
		Timezone:    pgtype.Text{String: "Asia/Tokyo", Valid: true},
		IsActive:    true,
		Metadata:    []byte(`{"keep":true,"workspace":{"gpu":{"devices":["nvidia.com/gpu=0"]}}}`),
	}
	var captured apisqlc.UpdateBotProfileParams
	store := NewStore(queriesFake{
		getBot: func(context.Context, pgtype.UUID) (apisqlc.GetBotByIDRow, error) {
			return row, nil
		},
		updateProfile: func(_ context.Context, params apisqlc.UpdateBotProfileParams) (apisqlc.UpdateBotProfileRow, error) {
			captured = params
			return apisqlc.UpdateBotProfileRow{}, nil
		},
	})

	preferences, found, err := store.LookupWorkspacePreferences(t.Context(), testBotID)
	if err != nil || !found {
		t.Fatalf("LookupWorkspacePreferences() = found %v, error %v", found, err)
	}
	if !preferences.HasGPU || len(preferences.GPU.Devices) != 1 {
		t.Fatalf("LookupWorkspacePreferences() = %#v", preferences)
	}
	if err := store.SetWorkspaceImagePreference(t.Context(), testBotID, "alpine:3.20"); err != nil {
		t.Fatalf("SetWorkspaceImagePreference() error = %v", err)
	}
	if captured.Name != row.Name || captured.DisplayName != row.DisplayName || captured.AvatarUrl != row.AvatarUrl ||
		captured.Timezone != row.Timezone || captured.IsActive != row.IsActive {
		t.Fatalf("profile fields changed: %#v", captured)
	}
	updated, err := workspace.DecodeWorkspacePreferences(captured.Metadata)
	if err != nil {
		t.Fatalf("DecodeWorkspacePreferences() error = %v", err)
	}
	if updated.Image != "alpine:3.20" || !updated.HasGPU {
		t.Fatalf("updated preferences = %#v", updated)
	}
	var metadata map[string]any
	if err := json.Unmarshal(captured.Metadata, &metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if metadata["keep"] != true {
		t.Fatalf("unrelated metadata = %#v", metadata["keep"])
	}
}

func TestWorkspaceProfileStoreMapsMissingBot(t *testing.T) {
	t.Parallel()

	store := NewStore(queriesFake{
		getBot: func(context.Context, pgtype.UUID) (apisqlc.GetBotByIDRow, error) {
			return apisqlc.GetBotByIDRow{}, pgx.ErrNoRows
		},
		updateProfile: func(context.Context, apisqlc.UpdateBotProfileParams) (apisqlc.UpdateBotProfileRow, error) {
			return apisqlc.UpdateBotProfileRow{}, nil
		},
	})

	_, found, err := store.LookupWorkspacePreferences(t.Context(), testBotID)
	if err != nil || found {
		t.Fatalf("LookupWorkspacePreferences() = found %v, error %v", found, err)
	}
	if err := store.RequireBot(t.Context(), testBotID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("RequireBot() error = %v, want db.ErrNotFound", err)
	}
}

func TestOwnerErrorMappingHidesDatabaseCauses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mapf func(error) error
		want error
	}{
		{name: "bot", mapf: mapBotError, want: bot.ErrBotNotFound},
		{name: "grant", mapf: mapGrantError, want: bot.ErrGrantNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.mapf(pgx.ErrNoRows)
			if !errors.Is(err, tt.want) {
				t.Fatalf("mapped error = %v, want %v", err, tt.want)
			}
			if errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("mapped error leaked pgx.ErrNoRows: %v", err)
			}
		})
	}

	duplicate := &pgconn.PgError{Code: "23505"}
	mapped := mapGrantError(duplicate)
	if !errors.Is(mapped, bot.ErrGrantExists) {
		t.Fatalf("mapGrantError(unique) = %v", mapped)
	}
	var leaked *pgconn.PgError
	if errors.As(mapped, &leaked) {
		t.Fatalf("mapGrantError(unique) leaked pgconn.PgError: %v", mapped)
	}
}

func mustUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		t.Fatalf("parse UUID %q: %v", value, err)
	}
	return id
}
