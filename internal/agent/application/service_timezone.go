package application

import (
	"context"
	"time"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/timezone"
)

// resolveTimezone resolves the bot's effective timezone: bot timezone, bot
// owner timezone, then the system default. It deliberately ignores who sent
// the current message, because the scheduler interprets every cron pattern
// the agent writes in this same timezone.
func (s *Service) resolveTimezone(ctx context.Context, botID string) (string, *time.Location) {
	loc, name := timezone.ResolveBot(ctx, s.queries, db.ParseUUIDOrEmpty(botID), s.clockLocation, s.logger)
	return name, loc
}
