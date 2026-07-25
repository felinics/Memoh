package application

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/memohai/memoh/internal/timezone"
)

// resolveTimezone resolves the effective timezone for a request.
// Priority: bot timezone > user timezone > system default.
func (s *Service) resolveTimezone(ctx context.Context, botID, userID string) (string, *time.Location) {
	fallbackName, fallbackLocation := s.systemTimezoneDefaults()

	// 1. Try bot timezone first.
	if name, loc, ok := s.loadBotTimezone(ctx, botID); ok {
		return name, loc
	}

	// 2. Fall back to user timezone.
	if name, loc, ok := s.loadUserTimezone(ctx, userID); ok {
		return name, loc
	}

	return fallbackName, fallbackLocation
}

func (s *Service) systemTimezoneDefaults() (string, *time.Location) {
	if s.clockLocation != nil {
		return s.clockLocation.String(), s.clockLocation
	}
	return timezone.DefaultName, timezone.MustResolve(timezone.DefaultName)
}

func (s *Service) loadBotTimezone(ctx context.Context, botID string) (string, *time.Location, bool) {
	if s.bots == nil || strings.TrimSpace(botID) == "" {
		return "", nil, false
	}
	bot, err := s.bots.GetForAccess(ctx, botID)
	if err != nil {
		return "", nil, false
	}
	tz := strings.TrimSpace(bot.Timezone)
	if tz == "" {
		return "", nil, false
	}
	loc, name, err := timezone.Resolve(tz)
	if err != nil {
		if s.logger != nil {
			s.logger.WarnContext(ctx, "resolve bot timezone failed",
				slog.String("bot_id", botID),
				slog.String("timezone", tz),
				slog.Any("error", err),
			)
		}
		return "", nil, false
	}
	return name, loc, true
}

func (s *Service) loadUserTimezone(ctx context.Context, userID string) (string, *time.Location, bool) {
	if s.accounts == nil || strings.TrimSpace(userID) == "" {
		return "", nil, false
	}
	account, err := s.accounts.Get(ctx, strings.TrimSpace(userID))
	if err != nil {
		return "", nil, false
	}
	tz := strings.TrimSpace(account.Timezone)
	if tz == "" {
		return "", nil, false
	}
	loc, name, err := timezone.Resolve(tz)
	if err != nil {
		if s.logger != nil {
			s.logger.WarnContext(ctx, "resolve user timezone failed",
				slog.String("user_id", userID),
				slog.String("timezone", tz),
				slog.Any("error", err),
			)
		}
		return "", nil, false
	}
	return name, loc, true
}
