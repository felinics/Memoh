package network

import (
	"context"
)

func (s *Service) Resolve(ctx context.Context, botID string) (BotOverlayConfig, error) {
	return s.GetBotConfig(ctx, botID)
}

func (s *Service) GetBotConfig(ctx context.Context, botID string) (BotOverlayConfig, error) {
	record, err := s.configReader.GetBotOverlayConfig(ctx, botID)
	if err != nil {
		return BotOverlayConfig{}, err
	}
	return s.normalizeBotConfig(
		record.Enabled,
		record.Provider,
		record.Config,
	)
}
