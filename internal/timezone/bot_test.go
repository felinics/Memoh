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
		wantSource  Source
		wantLookups int
	}{
		{
			name:       "bot timezone wins over owner",
			botTZ:      pgtype.Text{String: "Asia/Tokyo", Valid: true},
			ownerTZ:    "Asia/Shanghai",
			want:       "Asia/Tokyo",
			wantSource: SourceBot,
		},
		{
			name:        "owner timezone fills unset bot timezone",
			ownerTZ:     "Asia/Shanghai",
			want:        "Asia/Shanghai",
			wantSource:  SourceOwner,
			wantLookups: 1,
		},
		{
			name:        "owner timezone fills blank bot timezone",
			botTZ:       pgtype.Text{String: "  ", Valid: true},
			ownerTZ:     "Asia/Shanghai",
			want:        "Asia/Shanghai",
			wantSource:  SourceOwner,
			wantLookups: 1,
		},
		{
			name:        "owner timezone fills invalid bot timezone",
			botTZ:       pgtype.Text{String: "Not/A_Zone", Valid: true},
			ownerTZ:     "Europe/Paris",
			want:        "Europe/Paris",
			wantSource:  SourceOwner,
			wantLookups: 1,
		},
		{
			name:        "blank owner timezone uses fallback",
			ownerTZ:     "  ",
			want:        "system-default",
			wantSource:  SourceSystem,
			wantLookups: 1,
		},
		{
			name:        "invalid owner timezone uses fallback",
			ownerTZ:     "Not/A_Zone",
			want:        "system-default",
			wantSource:  SourceSystem,
			wantLookups: 1,
		},
		{
			name:        "owner lookup failure uses fallback",
			ownerErr:    errors.New("lookup failed"),
			want:        "system-default",
			wantSource:  SourceSystem,
			wantLookups: 1,
		},
		{
			name:       "bot without owner uses fallback",
			noOwner:    true,
			want:       "system-default",
			wantSource: SourceSystem,
		},
		{
			name:       "bot lookup failure uses fallback",
			botErr:     errors.New("lookup failed"),
			want:       "system-default",
			wantSource: SourceSystem,
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

			zone := ResolveBot(context.Background(), stub, botID, fallback, nil)
			if zone.Name != tt.want || zone.Location.String() != tt.want || zone.Source != tt.wantSource {
				t.Fatalf("ResolveBot() = (%q, %q, %q), want (%q, %q)", zone.Location.String(), zone.Name, zone.Source, tt.want, tt.wantSource)
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

	if zone := ResolveBot(context.Background(), stub, testUUID(1), nil, nil); zone.Location != time.UTC || zone.Name != DefaultName || zone.Source != SourceSystem {
		t.Fatalf("nil fallback = %+v, want UTC from system", zone)
	}
	if zone := ResolveBot(context.Background(), nil, testUUID(1), nil, nil); zone.Location != time.UTC || zone.Name != DefaultName {
		t.Fatalf("nil lookup = %+v, want UTC", zone)
	}

	fallback := time.FixedZone("system-default", 3*60*60)
	if zone := ResolveBot(context.Background(), &botLookupStub{bot: sqlc.GetBotByIDRow{Timezone: pgtype.Text{String: "Asia/Tokyo", Valid: true}}}, pgtype.UUID{}, fallback, nil); zone.Location != fallback {
		t.Fatalf("invalid bot id = %+v, want fallback", zone)
	}
}

func TestResolveBotWithInherited(t *testing.T) {
	t.Parallel()

	botID := testUUID(1)
	ownerID := testUUID(2)
	fallback := time.FixedZone("system-default", -5*60*60)

	tests := []struct {
		name          string
		botTZ         pgtype.Text
		ownerTZ       string
		botErr        error
		wantEffective string
		wantSource    Source
		wantInherited string
		wantInhSource Source
	}{
		{
			name:          "bot timezone is effective, owner timezone is inherited",
			botTZ:         pgtype.Text{String: "Asia/Tokyo", Valid: true},
			ownerTZ:       "Asia/Shanghai",
			wantEffective: "Asia/Tokyo",
			wantSource:    SourceBot,
			wantInherited: "Asia/Shanghai",
			wantInhSource: SourceOwner,
		},
		{
			name:          "without a bot timezone both are the owner's",
			ownerTZ:       "Asia/Shanghai",
			wantEffective: "Asia/Shanghai",
			wantSource:    SourceOwner,
			wantInherited: "Asia/Shanghai",
			wantInhSource: SourceOwner,
		},
		{
			name:          "bot timezone over the system default",
			botTZ:         pgtype.Text{String: "Asia/Tokyo", Valid: true},
			wantEffective: "Asia/Tokyo",
			wantSource:    SourceBot,
			wantInherited: "system-default",
			wantInhSource: SourceSystem,
		},
		{
			name:          "bot lookup failure uses the system default for both",
			botErr:        errors.New("lookup failed"),
			wantEffective: "system-default",
			wantSource:    SourceSystem,
			wantInherited: "system-default",
			wantInhSource: SourceSystem,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stub := &botLookupStub{
				bot:    sqlc.GetBotByIDRow{ID: botID, OwnerUserID: ownerID, Timezone: tt.botTZ},
				botErr: tt.botErr,
				owner:  sqlc.User{ID: ownerID, Timezone: tt.ownerTZ},
			}

			effective, inherited := ResolveBotWithInherited(context.Background(), stub, botID, fallback, nil)
			if effective.Name != tt.wantEffective || effective.Source != tt.wantSource {
				t.Fatalf("effective = (%q, %q), want (%q, %q)", effective.Name, effective.Source, tt.wantEffective, tt.wantSource)
			}
			if inherited.Name != tt.wantInherited || inherited.Source != tt.wantInhSource {
				t.Fatalf("inherited = (%q, %q), want (%q, %q)", inherited.Name, inherited.Source, tt.wantInherited, tt.wantInhSource)
			}
		})
	}
}
