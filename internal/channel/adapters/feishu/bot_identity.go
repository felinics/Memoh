package feishu

import (
	"context"
	"log/slog"
	"strings"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/errlog"
	"github.com/felinics/memoh/internal/errs"
)

func resolveConfiguredBotOpenID(cfg channel.ChannelConfig) string {
	if value := strings.TrimSpace(channel.ReadString(cfg.SelfIdentity, "open_id", "openId")); value != "" {
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

func (a *FeishuAdapter) resolveBotOpenID(ctx context.Context, cfg channel.ChannelConfig) string {
	if openID := resolveConfiguredBotOpenID(cfg); openID != "" {
		return openID
	}
	discovered, externalID, err := a.DiscoverSelf(ctx, cfg.Credentials)
	if err != nil {
		if a != nil && a.logger != nil {
			result := errlog.Event(ctx, "channel.feishu.bot_identity", errs.Wrap(err, "discover feishu self", slog.String("config_id", cfg.ID)), errlog.Options{})
			a.logger.LogAttrs(ctx, result.Level, "discover self fallback failed", result.Attrs()...)
		}
		return ""
	}
	if discoveredOpenID := strings.TrimSpace(channel.ReadString(discovered, "open_id", "openId")); discoveredOpenID != "" {
		return discoveredOpenID
	}
	return resolveConfiguredBotOpenID(channel.ChannelConfig{ExternalIdentity: externalID})
}
