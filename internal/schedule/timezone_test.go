package schedule

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

const timezoneTestOwnerID = "77777777-7777-7777-7777-777777777777"

type timezoneQueries struct {
	executionQueries
	bot     sqlc.GetBotByIDRow
	botErr  error
	owner   sqlc.User
	userErr error
}

func (q *timezoneQueries) GetBotByID(_ context.Context, _ pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return q.bot, q.botErr
}

func (q *timezoneQueries) GetUserByID(_ context.Context, _ pgtype.UUID) (sqlc.User, error) {
	return q.owner, q.userErr
}

func TestResolveBotLocationPriority(t *testing.T) {
	t.Parallel()

	botID := mustUUID(t, execTestBotID)
	ownerID := mustUUID(t, timezoneTestOwnerID)
	defaultLocation := time.FixedZone("system-default", -5*60*60)

	tests := []struct {
		name          string
		botTimezone   pgtype.Text
		ownerTimezone string
		userErr       error
		want          string
	}{
		{
			name:          "bot timezone has priority",
			botTimezone:   pgtype.Text{String: "Asia/Tokyo", Valid: true},
			ownerTimezone: "Asia/Shanghai",
			want:          "Asia/Tokyo",
		},
		{
			name:          "owner timezone fills empty bot timezone",
			ownerTimezone: "Asia/Shanghai",
			want:          "Asia/Shanghai",
		},
		{
			name:          "owner timezone fills invalid bot timezone",
			botTimezone:   pgtype.Text{String: "Not/A_Zone", Valid: true},
			ownerTimezone: "Europe/Paris",
			want:          "Europe/Paris",
		},
		{
			name:          "missing owner timezone uses system default",
			ownerTimezone: "  ",
			want:          defaultLocation.String(),
		},
		{
			name:          "invalid owner timezone uses system default",
			ownerTimezone: "Not/A_Zone",
			want:          defaultLocation.String(),
		},
		{
			name:    "owner lookup failure uses system default",
			userErr: errors.New("lookup failed"),
			want:    defaultLocation.String(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			queries := &timezoneQueries{
				bot: sqlc.GetBotByIDRow{
					ID:          botID,
					OwnerUserID: ownerID,
					Timezone:    tt.botTimezone,
				},
				owner:   sqlc.User{ID: ownerID, Timezone: tt.ownerTimezone},
				userErr: tt.userErr,
			}
			svc := &Service{
				queries:         queries,
				logger:          slog.Default(),
				defaultLocation: defaultLocation,
			}

			if got := svc.resolveBotLocation(context.Background(), botID); got.String() != tt.want {
				t.Fatalf("resolveBotLocation() = %q, want %q", got.String(), tt.want)
			}
		})
	}
}

func TestResolveBotLocationUsesDefaultWhenBotLookupFails(t *testing.T) {
	t.Parallel()

	defaultLocation := time.FixedZone("system-default", 2*60*60)
	queries := &timezoneQueries{botErr: errors.New("lookup failed")}
	svc := &Service{queries: queries, logger: slog.Default(), defaultLocation: defaultLocation}

	if got := svc.resolveBotLocation(context.Background(), mustUUID(t, execTestBotID)); got != defaultLocation {
		t.Fatalf("resolveBotLocation() = %v, want system default", got)
	}
}
