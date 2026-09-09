package line

import (
	"errors"
	"strings"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/redact"
)

const Type channel.ChannelType = "line"

const (
	configKeyChannelSecret      = "channel_secret"
	configKeyChannelAccessToken = "channel_access_token"
	userConfigKeyUserID         = "user_id"
)

type Config struct {
	ChannelSecret      string
	ChannelAccessToken string
}

type UserConfig struct {
	UserID string
}

func normalizeConfig(raw map[string]any) (map[string]any, error) {
	cfg, err := parseConfig(raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		configKeyChannelSecret:      cfg.ChannelSecret,
		configKeyChannelAccessToken: cfg.ChannelAccessToken,
	}, nil
}

func parseConfig(raw map[string]any) (Config, error) {
	secret := strings.TrimSpace(channel.ReadString(raw, configKeyChannelSecret, "channelSecret"))
	token := strings.TrimSpace(channel.ReadString(raw, configKeyChannelAccessToken, "channelAccessToken"))
	if secret == "" {
		return Config{}, errors.New("line channel_secret is required")
	}
	if token == "" {
		return Config{}, errors.New("line channel_access_token is required")
	}
	return Config{ChannelSecret: secret, ChannelAccessToken: token}, nil
}

func parseConfigForUse(raw map[string]any) (Config, error) {
	cfg, err := parseConfig(raw)
	if err != nil {
		return Config{}, err
	}
	registerConfigSecrets(cfg)
	return cfg, nil
}

func registerConfigSecrets(cfg Config) {
	redact.SetSecrets("line:"+hashValue(cfg.ChannelAccessToken), cfg.ChannelSecret, cfg.ChannelAccessToken)
}

func normalizeUserConfig(raw map[string]any) (map[string]any, error) {
	cfg, err := parseUserConfig(raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{userConfigKeyUserID: cfg.UserID}, nil
}

func parseUserConfig(raw map[string]any) (UserConfig, error) {
	userID := normalizeTarget(channel.ReadString(raw, "userId", userConfigKeyUserID))
	if userID == "" {
		return UserConfig{}, errors.New("line user config requires user_id")
	}
	return UserConfig{UserID: userID}, nil
}

func normalizeTarget(raw string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(raw), "line:"), "user:"))
}

func resolveTarget(raw map[string]any) (string, error) {
	cfg, err := parseUserConfig(raw)
	if err != nil {
		return "", err
	}
	return cfg.UserID, nil
}

func matchBinding(raw map[string]any, criteria channel.BindingCriteria) bool {
	cfg, err := parseUserConfig(raw)
	if err != nil {
		return false
	}
	if value := strings.TrimSpace(criteria.Attribute(userConfigKeyUserID)); value != "" && value == cfg.UserID {
		return true
	}
	return strings.TrimSpace(criteria.SubjectID) == cfg.UserID
}

func buildUserConfig(identity channel.Identity) map[string]any {
	if value := strings.TrimSpace(identity.Attribute(userConfigKeyUserID)); value != "" {
		return map[string]any{userConfigKeyUserID: value}
	}
	if value := strings.TrimSpace(identity.SubjectID); value != "" {
		return map[string]any{userConfigKeyUserID: value}
	}
	return map[string]any{}
}
