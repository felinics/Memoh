package application

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

const (
	timezoneTestBotID    = "22920000-0000-0000-0000-000000000001"
	timezoneTestOwnerID  = "22920000-0000-0000-0000-000000000002"
	timezoneTestMemberID = "22920000-0000-0000-0000-000000000003"
)

// timezoneTestQueries serves one bot and the accounts of its owner and of a
// team member, each with their own timezone.
type timezoneTestQueries struct {
	dbstore.Queries
	botTimezone pgtype.Text
	users       map[string]string
}

func (q *timezoneTestQueries) GetBotByID(_ context.Context, id pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	if id != db.ParseUUIDOrEmpty(timezoneTestBotID) {
		return sqlc.GetBotByIDRow{}, errors.New("bot not found")
	}
	return sqlc.GetBotByIDRow{
		ID:          id,
		OwnerUserID: db.ParseUUIDOrEmpty(timezoneTestOwnerID),
		Timezone:    q.botTimezone,
	}, nil
}

func (q *timezoneTestQueries) GetUserByID(_ context.Context, id pgtype.UUID) (sqlc.User, error) {
	tz, ok := q.users[id.String()]
	if !ok {
		return sqlc.User{}, errors.New("user not found")
	}
	return sqlc.User{ID: id, Timezone: tz}, nil
}

func newTimezoneTestService(botTimezone pgtype.Text) *Service {
	return &Service{
		logger:        slog.New(slog.DiscardHandler),
		clockLocation: time.UTC,
		queries: &timezoneTestQueries{
			botTimezone: botTimezone,
			users: map[string]string{
				timezoneTestOwnerID:  "Asia/Shanghai",
				timezoneTestMemberID: "America/New_York",
			},
		},
	}
}

func TestResolveTimezoneUsesBotEffectiveTimezone(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		botTimezone pgtype.Text
		want        string
	}{
		{name: "bot timezone", botTimezone: pgtype.Text{String: "Asia/Tokyo", Valid: true}, want: "Asia/Tokyo"},
		{name: "owner timezone when bot has none", want: "Asia/Shanghai"},
		{name: "owner timezone when bot timezone is invalid", botTimezone: pgtype.Text{String: "Not/A_Zone", Valid: true}, want: "Asia/Shanghai"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			name, loc := newTimezoneTestService(tt.botTimezone).resolveTimezone(context.Background(), timezoneTestBotID)
			if name != tt.want || loc.String() != tt.want {
				t.Fatalf("resolveTimezone() = (%q, %q), want %q", name, loc.String(), tt.want)
			}
		})
	}
}

func TestResolveTimezoneFallsBackToSystemDefault(t *testing.T) {
	t.Parallel()

	system := time.FixedZone("system-default", 2*60*60)
	svc := &Service{logger: slog.New(slog.DiscardHandler), clockLocation: system, queries: &timezoneTestQueries{}}
	if name, loc := svc.resolveTimezone(context.Background(), timezoneTestBotID); loc != system || name != "system-default" {
		t.Fatalf("resolveTimezone() = (%q, %v), want system default", name, loc)
	}
	if name, loc := (&Service{}).resolveTimezone(context.Background(), timezoneTestBotID); loc != time.UTC || name != "UTC" {
		t.Fatalf("resolveTimezone() without queries = (%q, %v), want UTC", name, loc)
	}
}

// The scheduler runs every cron pattern in the bot's effective timezone, so
// the timezone stated to the agent must not follow whoever sent the message.
func TestRuntimeContextTimezoneIgnoresSender(t *testing.T) {
	t.Parallel()

	svc := newTimezoneTestService(pgtype.Text{})
	for _, sender := range []struct {
		name   string
		userID string
	}{
		{name: "owner", userID: timezoneTestOwnerID},
		{name: "member in another timezone", userID: timezoneTestMemberID},
		{name: "unlinked channel identity", userID: ""},
	} {
		t.Run(sender.name, func(t *testing.T) {
			t.Parallel()
			sections, _ := svc.buildRuntimeContextSections(context.Background(), ChatRequest{
				BotID:                   timezoneTestBotID,
				UserID:                  sender.userID,
				SourceChannelIdentityID: "telegram-identity",
			}, "", "")
			markdown, _, _ := runtimeContextViaContextView(context.Background(), nil, sections, "")
			if !strings.Contains(markdown, "Timezone: Asia/Shanghai") {
				t.Fatalf("runtime context does not state the owner timezone:\n%s", markdown)
			}
		})
	}
}
