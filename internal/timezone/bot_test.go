package timezone

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

type botLookupStub struct {
	bot       sqlc.GetBotByIDRow
	botErr    error
	owner     sqlc.User
	ownerErr  error
	ownerHits int
}

func (s *botLookupStub) GetBotByID(context.Context, pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return s.bot, s.botErr
}

func (s *botLookupStub) GetUserByID(_ context.Context, id pgtype.UUID) (sqlc.User, error) {
	s.ownerHits++
	if id != s.bot.OwnerUserID {
		return sqlc.User{}, errors.New("unexpected user lookup")
	}
	return s.owner, s.ownerErr
}

func testUUID(b byte) pgtype.UUID {
	var id pgtype.UUID
	id.Valid = true
	for i := range id.Bytes {
		id.Bytes[i] = b
	}
	return id
}

func TestResolveBotPriority(t *testing.T) {
	t.Parallel()

	botID := testUUID(1)
	ownerID := testUUID(2)
	fallback := time.FixedZone("system-default", -5*60*60)

	tests := []struct {
		name        string
		botTZ       pgtype.Text
		noOwner     bool
		ownerTZ     string
		ownerErr    error
		botErr      error
		want        string
		wantLookups int
	}{
		{
			name:    "bot timezone wins over owner",
			botTZ:   pgtype.Text{String: "Asia/Tokyo", Valid: true},
			ownerTZ: "Asia/Shanghai",
			want:    "Asia/Tokyo",
		},
		{
			name:        "owner timezone fills unset bot timezone",
			ownerTZ:     "Asia/Shanghai",
			want:        "Asia/Shanghai",
			wantLookups: 1,
		},
		{
			name:        "owner timezone fills blank bot timezone",
			botTZ:       pgtype.Text{String: "  ", Valid: true},
			ownerTZ:     "Asia/Shanghai",
			want:        "Asia/Shanghai",
			wantLookups: 1,
		},
		{
			name:        "owner timezone fills invalid bot timezone",
			botTZ:       pgtype.Text{String: "Not/A_Zone", Valid: true},
			ownerTZ:     "Europe/Paris",
			want:        "Europe/Paris",
			wantLookups: 1,
		},
		{
			name:        "blank owner timezone uses fallback",
			ownerTZ:     "  ",
			want:        "system-default",
			wantLookups: 1,
		},
		{
			name:        "invalid owner timezone uses fallback",
			ownerTZ:     "Not/A_Zone",
			want:        "system-default",
			wantLookups: 1,
		},
		{
			name:        "owner lookup failure uses fallback",
			ownerErr:    errors.New("lookup failed"),
			want:        "system-default",
			wantLookups: 1,
		},
		{
			name:    "bot without owner uses fallback",
			noOwner: true,
			want:    "system-default",
		},
		{
			name:   "bot lookup failure uses fallback",
			botErr: errors.New("lookup failed"),
			want:   "system-default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stub := &botLookupStub{
				bot:      sqlc.GetBotByIDRow{ID: botID, OwnerUserID: ownerID, Timezone: tt.botTZ},
				botErr:   tt.botErr,
				owner:    sqlc.User{ID: ownerID, Timezone: tt.ownerTZ},
				ownerErr: tt.ownerErr,
			}
			if tt.noOwner {
				stub.bot.OwnerUserID = pgtype.UUID{}
			}

			loc, name := ResolveBot(context.Background(), stub, botID, fallback, nil)
			if name != tt.want || loc.String() != tt.want {
				t.Fatalf("ResolveBot() = (%q, %q), want %q", loc.String(), name, tt.want)
			}
			if stub.ownerHits != tt.wantLookups {
				t.Fatalf("owner lookups = %d, want %d", stub.ownerHits, tt.wantLookups)
			}
		})
	}
}

func TestResolveBotFallbacks(t *testing.T) {
	t.Parallel()

	stub := &botLookupStub{}

	if loc, name := ResolveBot(context.Background(), stub, testUUID(1), nil, nil); loc != time.UTC || name != DefaultName {
		t.Fatalf("nil fallback = (%v, %q), want UTC", loc, name)
	}
	if loc, name := ResolveBot(context.Background(), nil, testUUID(1), nil, nil); loc != time.UTC || name != DefaultName {
		t.Fatalf("nil lookup = (%v, %q), want UTC", loc, name)
	}

	fallback := time.FixedZone("system-default", 3*60*60)
	if loc, _ := ResolveBot(context.Background(), &botLookupStub{bot: sqlc.GetBotByIDRow{Timezone: pgtype.Text{String: "Asia/Tokyo", Valid: true}}}, pgtype.UUID{}, fallback, nil); loc != fallback {
		t.Fatalf("invalid bot id = %v, want fallback", loc)
	}
}
