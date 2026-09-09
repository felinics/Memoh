package telegram

import (
	"errors"
	"strings"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/common"
)

const defaultAPIBaseURL = "https://api.telegram.org"

// Config holds the Telegram bot credentials extracted from a channel configuration.
type Config struct {
	BotToken   string
	APIBaseURL string // Reverse proxy base URL for regions where Telegram is blocked (e.g. China mainland)
	HTTPProxy  common.HTTPProxyConfig
}

// baseURL returns the effective base URL with trailing slashes removed.
func (c Config) baseURL() string {
	base := c.APIBaseURL
	if base == "" {
		base = defaultAPIBaseURL
	}
	return strings.TrimRight(base, "/")
}

// apiEndpoint returns the Sprintf-formatted API endpoint derived from the base URL.
func (c Config) apiEndpoint() string {
	return c.baseURL() + "/bot%s/%s"
}

// fileEndpoint returns the Sprintf-formatted file download endpoint derived from the base URL.
func (c Config) fileEndpoint() string {
	return c.baseURL() + "/file/bot%s/%s"
}

// UserConfig holds the identifiers used to target a Telegram user or group.
type UserConfig struct {
	Username string
	UserID   string
	ChatID   string
}

func normalizeConfig(raw map[string]any) (map[string]any, error) {
	cfg, err := parseConfig(raw)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"botToken": cfg.BotToken,
	}
	if cfg.APIBaseURL != "" {
		out["apiBaseURL"] = cfg.APIBaseURL
	}
	common.NormalizeHTTPProxyConfig(out, cfg.HTTPProxy)
	return out, nil
}

func normalizeUserConfig(raw map[string]any) (map[string]any, error) {
	cfg, err := parseUserConfig(raw)
	if err != nil {
		return nil, err
	}
	result := map[string]any{}
	if cfg.Username != "" {
		result["username"] = cfg.Username
	}
	if cfg.UserID != "" {
		result["user_id"] = cfg.UserID
	}
	if cfg.ChatID != "" {
		result["chat_id"] = cfg.ChatID
	}
	return result, nil
}

func resolveTarget(raw map[string]any) (string, error) {
	cfg, err := parseUserConfig(raw)
	if err != nil {
		return "", err
	}
	if cfg.ChatID != "" {
		return cfg.ChatID, nil
	}
	if cfg.UserID != "" {
		return cfg.UserID, nil
	}
	if cfg.Username != "" {
		name := cfg.Username
		if !strings.HasPrefix(name, "@") {
			name = "@" + name
		}
		return name, nil
	}
	return "", errors.New("telegram binding is incomplete")
}

func matchBinding(raw map[string]any, criteria channel.BindingCriteria) bool {
	cfg, err := parseUserConfig(raw)
	if err != nil {
		return false
	}
	if value := strings.TrimSpace(criteria.Attribute("chat_id")); value != "" && value == cfg.ChatID {
		return true
	}
	if value := strings.TrimSpace(criteria.Attribute("user_id")); value != "" && value == cfg.UserID {
		return true
	}
	if value := strings.TrimSpace(criteria.Attribute("username")); value != "" && strings.EqualFold(value, cfg.Username) {
		return true
	}
	if criteria.SubjectID != "" {
		if criteria.SubjectID == cfg.ChatID || criteria.SubjectID == cfg.UserID || strings.EqualFold(criteria.SubjectID, cfg.Username) {
			return true
		}
	}
	return false
}

func buildUserConfig(identity channel.Identity) map[string]any {
	result := map[string]any{}
	if value := strings.TrimSpace(identity.Attribute("username")); value != "" {
		result["username"] = value
	}
	if value := strings.TrimSpace(identity.Attribute("user_id")); value != "" {
		result["user_id"] = value
	}
	if value := strings.TrimSpace(identity.Attribute("chat_id")); value != "" {
		result["chat_id"] = value
	}
	return result
}

func parseConfig(raw map[string]any) (Config, error) {
	token := strings.TrimSpace(channel.ReadString(raw, "botToken", "bot_token"))
	if token == "" {
		return Config{}, errors.New("telegram botToken is required")
	}
	apiBaseURL := strings.TrimSpace(channel.ReadString(raw, "apiBaseURL", "api_base_url"))
	proxyCfg, err := common.ParseHTTPProxyConfig(raw)
	if err != nil {
		return Config{}, err
	}
	return Config{BotToken: token, APIBaseURL: apiBaseURL, HTTPProxy: proxyCfg}, nil
}

func parseUserConfig(raw map[string]any) (UserConfig, error) {
	username := strings.TrimSpace(channel.ReadString(raw, "username"))
	userID := strings.TrimSpace(channel.ReadString(raw, "userId", "user_id"))
	chatID := strings.TrimSpace(channel.ReadString(raw, "chatId", "chat_id"))
	if username == "" && userID == "" && chatID == "" {
		return UserConfig{}, errors.New("telegram user config requires username, user_id, or chat_id")
	}
	return UserConfig{
		Username: username,
		UserID:   userID,
		ChatID:   chatID,
	}, nil
}

func normalizeTarget(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "@") {
		return value
	}
	value = strings.TrimPrefix(value, "tg:")
	value = strings.TrimPrefix(value, "telegram:")
	value = strings.TrimPrefix(value, "t.me/")
	value = strings.TrimPrefix(value, "https://t.me/")
	value = strings.TrimPrefix(value, "http://t.me/")
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "@") {
		return value
	}
	if isTelegramChatID(value) {
		return value
	}
	return "@" + value
}

// isTelegramChatID returns true when s looks like a Telegram numeric chat ID,
// which may be negative (e.g. supergroup IDs like -1002280927535).
func isTelegramChatID(s string) bool {
	digits := s
	digits = strings.TrimPrefix(digits, "-")
	if len(digits) == 0 {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
