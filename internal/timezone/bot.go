package timezone

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// BotLookup is the subset of database queries needed to derive a bot's
// effective timezone.
type BotLookup interface {
	GetBotByID(ctx context.Context, id pgtype.UUID) (sqlc.GetBotByIDRow, error)
	GetUserByID(ctx context.Context, id pgtype.UUID) (sqlc.User, error)
}

// Source says where a bot's timezone comes from.
type Source string

const (
	SourceBot    Source = "bot"
	SourceOwner  Source = "owner"
	SourceSystem Source = "system"
)

// Zone is a resolved timezone and where it comes from.
type Zone struct {
	Location *time.Location
	Name     string
	Source   Source
}

// ResolveBot returns a bot's effective timezone: the bot's own timezone, then
// its owner's, then fallback. A nil fallback means UTC.
//
// Agent turns, the scheduler and the settings API must all use this, so a
// cron pattern fires at the time the agent meant and the time clients show.
// Who sent the current message never affects the result.
func ResolveBot(ctx context.Context, q BotLookup, botID pgtype.UUID, fallback *time.Location, logger *slog.Logger) Zone {
	bot, ok := loadBot(ctx, q, botID)
	if !ok {
		return systemZone(fallback)
	}
	if zone, ok := botZone(ctx, bot, logger); ok {
		return zone
	}
	return inheritedZone(ctx, q, bot, fallback, logger)
}

// ResolveBotWithInherited returns the bot's effective timezone together with
// the one it inherits when it has none of its own: its owner's, then
// fallback. Settings clients label the empty choice with the inherited one.
func ResolveBotWithInherited(ctx context.Context, q BotLookup, botID pgtype.UUID, fallback *time.Location, logger *slog.Logger) (effective, inherited Zone) {
	bot, ok := loadBot(ctx, q, botID)
	if !ok {
		zone := systemZone(fallback)
		return zone, zone
	}
	inherited = inheritedZone(ctx, q, bot, fallback, logger)
	if zone, ok := botZone(ctx, bot, logger); ok {
		return zone, inherited
	}
	return inherited, inherited
}

func loadBot(ctx context.Context, q BotLookup, botID pgtype.UUID) (sqlc.GetBotByIDRow, bool) {
	if q == nil || !botID.Valid {
		return sqlc.GetBotByIDRow{}, false
	}
	bot, err := q.GetBotByID(ctx, botID)
	if err != nil {
		return sqlc.GetBotByIDRow{}, false
	}
	return bot, true
}

func botZone(ctx context.Context, bot sqlc.GetBotByIDRow, logger *slog.Logger) (Zone, bool) {
	loc, name, err := resolveStored(bot.Timezone.String)
	if loc != nil {
		return Zone{Location: loc, Name: name, Source: SourceBot}, true
	}
	if err != nil && logger != nil {
		logger.WarnContext(ctx, "invalid bot timezone",
			slog.String("bot_id", bot.ID.String()),
			slog.String("timezone", bot.Timezone.String),
			slog.Any("error", err))
	}
	return Zone{}, false
}

func inheritedZone(ctx context.Context, q BotLookup, bot sqlc.GetBotByIDRow, fallback *time.Location, logger *slog.Logger) Zone {
	if bot.OwnerUserID.Valid {
		owner, err := q.GetUserByID(ctx, bot.OwnerUserID)
		if err == nil {
			loc, name, err := resolveStored(owner.Timezone)
			if loc != nil {
				return Zone{Location: loc, Name: name, Source: SourceOwner}
			}
			if err != nil && logger != nil {
				logger.WarnContext(ctx, "invalid bot owner timezone",
					slog.String("bot_id", bot.ID.String()),
					slog.String("user_id", bot.OwnerUserID.String()),
					slog.String("timezone", owner.Timezone),
					slog.Any("error", err))
			}
		}
	}
	return systemZone(fallback)
}

func systemZone(fallback *time.Location) Zone {
	if fallback == nil {
		fallback = time.UTC
	}
	return Zone{Location: fallback, Name: fallback.String(), Source: SourceSystem}
}

// resolveStored loads a stored timezone name. A blank name is unset and yields
// neither a location nor an error.
func resolveStored(raw string) (*time.Location, string, error) {
	tz := strings.TrimSpace(raw)
	if tz == "" {
		return nil, "", nil
	}
	return Resolve(tz)
}
