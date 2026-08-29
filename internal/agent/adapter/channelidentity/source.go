// Package channelidentity adapts Channel configuration data to the neutral
// platform identity projection consumed by Agent application orchestration.
package channelidentity

import (
	"context"

	"github.com/felinics/memoh/internal/agent/application"
	"github.com/felinics/memoh/internal/channel"
)

type configSource interface {
	ListBotConfigs(ctx context.Context, botID string) ([]channel.ChannelConfig, error)
}

type Source struct {
	configs configSource
}

func NewSource(configs configSource) *Source {
	return &Source{configs: configs}
}

func (s *Source) ListPlatformIdentities(ctx context.Context, botID string) ([]application.PlatformIdentity, error) {
	if s == nil || s.configs == nil {
		return nil, nil
	}
	configs, err := s.configs.ListBotConfigs(ctx, botID)
	if err != nil {
		return nil, err
	}
	identities := make([]application.PlatformIdentity, 0, len(configs))
	for _, cfg := range configs {
		identities = append(identities, application.PlatformIdentity{
			ID:               cfg.ID,
			Platform:         cfg.ChannelType.String(),
			ExternalIdentity: cfg.ExternalIdentity,
			SelfIdentity:     cfg.SelfIdentity,
		})
	}
	return identities, nil
}
