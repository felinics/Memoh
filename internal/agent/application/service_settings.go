package application

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/settings"
)

func (s *Service) loadBotSettings(ctx context.Context, botID string) (settings.Settings, error) {
	if s.settingsService == nil {
		return settings.Settings{}, errors.New("settings service not configured")
	}
	return s.settingsService.GetBot(ctx, botID)
}

func (s *Service) loadBotRuntimeInfo(ctx context.Context, botID string) (native.BotInfo, bool) {
	info := native.BotInfo{ID: strings.TrimSpace(botID)}
	if s.queries == nil {
		return info, false
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return info, false
	}
	row, err := s.queries.GetBotByID(ctx, botUUID)
	if err != nil {
		s.logger.Debug("failed to load bot metadata for loop detection",
			slog.String("bot_id", botID),
			slog.Any("error", err),
		)
		return info, false
	}
	info.Name = strings.TrimSpace(row.Name)
	if row.DisplayName.Valid {
		info.DisplayName = strings.TrimSpace(row.DisplayName.String)
	}
	if row.Timezone.Valid {
		info.Timezone = strings.TrimSpace(row.Timezone.String)
	}
	return info, parseLoopDetectionEnabledFromMetadata(row.Metadata)
}

func parseLoopDetectionEnabledFromMetadata(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	var metadata map[string]any
	if err := json.Unmarshal(payload, &metadata); err != nil || metadata == nil {
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
