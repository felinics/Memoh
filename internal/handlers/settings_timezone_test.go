package handlers

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/settings"
)

const (
	settingsTZBotID   = "22930000-0000-0000-0000-000000000001"
	settingsTZOwnerID = "22930000-0000-0000-0000-000000000002"
)

type settingsTimezoneQueries struct {
	dbstore.Queries
	botTimezone   pgtype.Text
	ownerTimezone string
}

func (q *settingsTimezoneQueries) GetBotByID(_ context.Context, id pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	if id != db.ParseUUIDOrEmpty(settingsTZBotID) {
		return sqlc.GetBotByIDRow{}, errors.New("bot not found")
	}
	return sqlc.GetBotByIDRow{ID: id, OwnerUserID: db.ParseUUIDOrEmpty(settingsTZOwnerID), Timezone: q.botTimezone}, nil
}

func (q *settingsTimezoneQueries) GetUserByID(_ context.Context, id pgtype.UUID) (sqlc.User, error) {
	return sqlc.User{ID: id, Timezone: q.ownerTimezone}, nil
}

func TestSettingsResponseCarriesResolvedTimezones(t *testing.T) {
	t.Parallel()

	system := time.FixedZone("system-default", 2*60*60)
	tests := []struct {
		name          string
		botTimezone   pgtype.Text
		ownerTimezone string
		want          settings.Settings
	}{
		{
			name:          "bot without a timezone follows its owner",
			ownerTimezone: "Asia/Shanghai",
			want: settings.Settings{
				EffectiveTimezone:       "Asia/Shanghai",
				InheritedTimezone:       "Asia/Shanghai",
				InheritedTimezoneSource: "owner",
			},
		},
		{
			name:          "bot timezone is effective while the owner's stays inherited",
			botTimezone:   pgtype.Text{String: "Asia/Tokyo", Valid: true},
			ownerTimezone: "Asia/Shanghai",
			want: settings.Settings{
				Timezone:                "Asia/Tokyo",
				EffectiveTimezone:       "Asia/Tokyo",
				InheritedTimezone:       "Asia/Shanghai",
				InheritedTimezoneSource: "owner",
			},
		},
		{
			name: "owner without a timezone falls back to the system",
			want: settings.Settings{
				EffectiveTimezone:       "system-default",
				InheritedTimezone:       "system-default",
				InheritedTimezoneSource: "system",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := &SettingsHandler{
				queries:        &settingsTimezoneQueries{botTimezone: tt.botTimezone, ownerTimezone: tt.ownerTimezone},
				systemLocation: system,
				logger:         slog.New(slog.DiscardHandler),
			}
			got := h.withTimezones(context.Background(), settingsTZBotID, settings.Settings{Timezone: tt.botTimezone.String})
			if got.Timezone != tt.want.Timezone ||
				got.EffectiveTimezone != tt.want.EffectiveTimezone ||
				got.InheritedTimezone != tt.want.InheritedTimezone ||
				got.InheritedTimezoneSource != tt.want.InheritedTimezoneSource {
				t.Fatalf("withTimezones() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
