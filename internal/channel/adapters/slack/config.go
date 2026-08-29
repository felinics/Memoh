package slack

import (
	"errors"
	"strings"

	"github.com/felinics/memoh/internal/channel"
)

// Config holds the Slack bot credentials extracted from a channel configuration.
type Config struct {
	BotToken string // xoxb-...
	AppToken string // xapp-... (required for Socket Mode)
}

// UserConfig holds the identifiers used to target a Slack user or channel.
type UserConfig struct {
	UserID    string
	ChannelID string
	Username  string
}

func normalizeConfig(raw map[string]any) (map[string]any, error) {
	cfg, err := parseConfig(raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"botToken": cfg.BotToken,
		"appToken": cfg.AppToken,
	}, nil
}

func normalizeUserConfig(raw map[string]any) (map[string]any, error) {
	cfg, err := parseUserConfig(raw)
	if err != nil {
		return nil, err
	}
	result := map[string]any{}
	if cfg.UserID != "" {
		result["user_id"] = cfg.UserID
	}
	if cfg.ChannelID != "" {
		result["channel_id"] = cfg.ChannelID
	}
	if cfg.Username != "" {
		result["username"] = cfg.Username
	}
	return result, nil
}

func resolveTarget(raw map[string]any) (string, error) {
	cfg, err := parseUserConfig(raw)
	if err != nil {
		return "", err
	}
	if cfg.ChannelID != "" {
		return cfg.ChannelID, nil
	}
	if cfg.UserID != "" {
		return cfg.UserID, nil
	}
	return "", errors.New("slack binding is incomplete")
}

func matchBinding(raw map[string]any, criteria channel.BindingCriteria) bool {
	cfg, err := parseUserConfig(raw)
	if err != nil {
		return false
	}
	if value := strings.TrimSpace(criteria.Attribute("user_id")); value != "" && value == cfg.UserID {
		return true
	}
	if value := strings.TrimSpace(criteria.Attribute("channel_id")); value != "" && value == cfg.ChannelID {
		return true
	}
	if value := strings.TrimSpace(criteria.Attribute("username")); value != "" && strings.EqualFold(value, cfg.Username) {
		return true
	}
	if criteria.SubjectID != "" {
		if criteria.SubjectID == cfg.UserID || criteria.SubjectID == cfg.ChannelID {
			return true
		}
	}
	return false
}

func buildUserConfig(identity channel.Identity) map[string]any {
	result := map[string]any{}
	if value := strings.TrimSpace(identity.Attribute("user_id")); value != "" {
		result["user_id"] = value
	}
	if value := strings.TrimSpace(identity.Attribute("channel_id")); value != "" {
		result["channel_id"] = value
	}
	if value := strings.TrimSpace(identity.Attribute("username")); value != "" {
		result["username"] = value
	}
	return result
}

func parseConfig(raw map[string]any) (Config, error) {
	botToken := strings.TrimSpace(channel.ReadString(raw, "botToken", "bot_token"))
	if botToken == "" {
		return Config{}, errors.New("slack botToken is required")
	}
	appToken := strings.TrimSpace(channel.ReadString(raw, "appToken", "app_token"))
	if appToken == "" {
		return Config{}, errors.New("slack appToken is required for Socket Mode")
	}
	return Config{BotToken: botToken, AppToken: appToken}, nil
}

func parseUserConfig(raw map[string]any) (UserConfig, error) {
	userID := strings.TrimSpace(channel.ReadString(raw, "userId", "user_id"))
	channelID := strings.TrimSpace(channel.ReadString(raw, "channelId", "channel_id"))
	username := strings.TrimSpace(channel.ReadString(raw, "username"))

	if userID == "" && channelID == "" {
		return UserConfig{}, errors.New("slack user config requires user_id or channel_id")
	}

	return UserConfig{
		UserID:    userID,
		ChannelID: channelID,
		Username:  username,
	}, nil
}

func normalizeTarget(raw string) string {
	return strings.TrimSpace(raw)
}
