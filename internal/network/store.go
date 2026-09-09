package network

import (
	"context"
	"encoding/json"

	"github.com/felinics/memoh/internal/db"
)

func (s *Service) Resolve(ctx context.Context, botID string) (BotOverlayConfig, error) {
	return s.GetBotConfig(ctx, botID)
}

func (s *Service) GetBotConfig(ctx context.Context, botID string) (BotOverlayConfig, error) {
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return BotOverlayConfig{}, err
	}
	row, err := s.queries.GetBotOverlayConfig(ctx, pgBotID)
	if err != nil {
		return BotOverlayConfig{}, err
	}
	return s.normalizeBotConfig(
		row.OverlayEnabled,
		row.OverlayProvider,
		decodeJSONMap(row.OverlayConfig),
	)
}

func decodeJSONMap(raw []byte) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}
