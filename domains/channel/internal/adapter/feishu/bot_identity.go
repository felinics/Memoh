package feishu

import (
	"context"
	"log/slog"
	"strings"

	"github.com/memohai/memoh/domains/channel/gateway"
)

func resolveConfiguredBotOpenID(cfg gateway.ChannelConfig) string {
	if value := strings.TrimSpace(gateway.ReadString(cfg.SelfIdentity, "open_id", "openId")); value != "" {
		return value
	}
	external := strings.TrimSpace(cfg.ExternalIdentity)
	if external == "" {
		return ""
	}
	if strings.HasPrefix(external, "open_id:") {
		return strings.TrimSpace(strings.TrimPrefix(external, "open_id:"))
	}
	// Legacy records may persist raw open_id without prefix.
	if !strings.Contains(external, ":") {
		return external
	}
	return ""
}

func (a *FeishuAdapter) resolveBotOpenID(ctx context.Context, cfg gateway.ChannelConfig) string {
	if openID := resolveConfiguredBotOpenID(cfg); openID != "" {
		return openID
	}
	discovered, externalID, err := a.DiscoverSelf(ctx, cfg.Credentials)
	if err != nil {
		if a != nil && a.logger != nil {
			a.logger.WarnContext(ctx, "discover self fallback failed", slog.String("config_id", cfg.ID), slog.Any("error", err))
		}
		return ""
	}
	if discoveredOpenID := strings.TrimSpace(gateway.ReadString(discovered, "open_id", "openId")); discoveredOpenID != "" {
		return discoveredOpenID
	}
	return resolveConfiguredBotOpenID(gateway.ChannelConfig{ExternalIdentity: externalID})
}
