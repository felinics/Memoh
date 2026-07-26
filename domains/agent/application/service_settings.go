package application

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/memohai/memoh/domains/agent/engine"
	"github.com/memohai/memoh/domains/api/bot/setting"
)

func (s *Service) loadBotSettings(ctx context.Context, botID string) (setting.Settings, error) {
	if s.settingsService == nil {
		return setting.Settings{}, errors.New("settings service not configured")
	}
	return s.settingsService.GetBot(ctx, botID)
}

func (s *Service) loadBotRuntimeInfo(ctx context.Context, botID string) (engine.BotInfo, bool) {
	info := engine.BotInfo{ID: strings.TrimSpace(botID)}
	if s.bots == nil {
		return info, false
	}
	bot, err := s.bots.GetForAccess(ctx, botID)
	if err != nil {
		s.logger.DebugContext(ctx, "failed to load bot metadata for loop detection",
			slog.String("bot_id", botID),
			slog.Any("error", err),
		)
		return info, false
	}
	info.Name = strings.TrimSpace(bot.Name)
	info.DisplayName = strings.TrimSpace(bot.DisplayName)
	info.Timezone = strings.TrimSpace(bot.Timezone)
	return info, loopDetectionEnabled(bot.Metadata)
}

func loopDetectionEnabled(metadata map[string]any) bool {
	if metadata == nil {
		return false
	}
	features, ok := metadata["features"].(map[string]any)
	if !ok {
		return false
	}
	loopDetection, ok := features["loop_detection"].(map[string]any)
	if !ok {
		return false
	}
	enabled, ok := loopDetection["enabled"].(bool)
	if !ok {
		return false
	}
	return enabled
}
