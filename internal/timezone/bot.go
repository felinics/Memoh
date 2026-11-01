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

// ResolveBot returns a bot's effective timezone: the bot's own timezone, then
// its owner's, then fallback. A nil fallback means UTC.
//
// Agent turns and the scheduler must both use this, so a cron pattern the
// agent writes fires at the time the agent meant. Who sent the current message
// never affects the result.
func ResolveBot(ctx context.Context, q BotLookup, botID pgtype.UUID, fallback *time.Location, logger *slog.Logger) (*time.Location, string) {
	if fallback == nil {
		fallback = time.UTC
	}
	if q == nil || !botID.Valid {
		return fallback, fallback.String()
	}
	bot, err := q.GetBotByID(ctx, botID)
	if err != nil {
		return fallback, fallback.String()
	}
	if loc, name, err := resolveStored(bot.Timezone.String); loc != nil {
		return loc, name
	} else if err != nil && logger != nil {
		logger.WarnContext(ctx, "invalid bot timezone",
			slog.String("bot_id", botID.String()),
			slog.String("timezone", bot.Timezone.String),
			slog.Any("error", err))
	}
	if bot.OwnerUserID.Valid {
		owner, err := q.GetUserByID(ctx, bot.OwnerUserID)
		if err == nil {
			if loc, name, err := resolveStored(owner.Timezone); loc != nil {
				return loc, name
			} else if err != nil && logger != nil {
				logger.WarnContext(ctx, "invalid bot owner timezone",
					slog.String("bot_id", botID.String()),
					slog.String("user_id", bot.OwnerUserID.String()),
					slog.String("timezone", owner.Timezone),
					slog.Any("error", err))
			}
		}
	}
	return fallback, fallback.String()
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
