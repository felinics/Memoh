package weixin

import (
	"errors"
	"strconv"
	"strings"

	"github.com/felinics/memoh/internal/channel"
)

const (
	defaultBaseURL    = "https://ilinkai.weixin.qq.com"
	defaultCDNBaseURL = "https://novac2c.cdn.weixin.qq.com/c2c"
	defaultBotType    = "3"
)

type adapterConfig struct {
	Token              string
	BaseURL            string
	CDNBaseURL         string
	PollTimeoutSeconds int
	EnableTyping       bool
}

func normalizeConfig(raw map[string]any) (map[string]any, error) {
	cfg, err := parseConfig(raw)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"token":   cfg.Token,
		"baseUrl": cfg.BaseURL,
	}
	if cfg.PollTimeoutSeconds > 0 {
		out["pollTimeoutSeconds"] = cfg.PollTimeoutSeconds
	}
	if cfg.EnableTyping {
		out["enableTyping"] = true
	}
	return out, nil
}

func parseConfig(raw map[string]any) (adapterConfig, error) {
	cfg := adapterConfig{
		Token:      strings.TrimSpace(channel.ReadString(raw, "token")),
		BaseURL:    strings.TrimSpace(channel.ReadString(raw, "baseUrl", "base_url")),
		CDNBaseURL: defaultCDNBaseURL,
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if v, ok := readInt(raw, "pollTimeoutSeconds", "poll_timeout_seconds"); ok {
		cfg.PollTimeoutSeconds = v
	}
	if v, ok := readBool(raw, "enableTyping", "enable_typing"); ok {
		cfg.EnableTyping = v
	}
	if cfg.Token == "" {
		return adapterConfig{}, errors.New("weixin token is required")
	}
	return cfg, nil
}

func normalizeTarget(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return ""
	}
	v = strings.TrimPrefix(v, "weixin:")
	return strings.TrimSpace(v)
}

func normalizeUserConfig(raw map[string]any) (map[string]any, error) {
	userID := strings.TrimSpace(channel.ReadString(raw, "userId", "user_id"))
	if userID == "" {
		return nil, errors.New("weixin user_id is required")
	}
	return map[string]any{"user_id": userID}, nil
}

func resolveTarget(raw map[string]any) (string, error) {
	userID := strings.TrimSpace(channel.ReadString(raw, "userId", "user_id"))
	if userID == "" {
		return "", errors.New("weixin user config requires user_id")
	}
	return userID, nil
}

func matchBinding(config map[string]any, criteria channel.BindingCriteria) bool {
	userID := strings.TrimSpace(channel.ReadString(config, "userId", "user_id"))
	if userID == "" {
		return false
	}
	if criteria.SubjectID != "" && criteria.SubjectID == userID {
		return true
	}
	if v := strings.TrimSpace(criteria.Attribute("user_id")); v != "" && v == userID {
		return true
	}
	return false
}

func buildUserConfig(identity channel.Identity) map[string]any {
	out := map[string]any{}
	if v := strings.TrimSpace(identity.SubjectID); v != "" {
		out["user_id"] = v
	}
	return out
}

func readInt(raw map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case int:
			return v, true
		case int32:
			return int(v), true
		case int64:
			return int(v), true
		case float64:
			return int(v), true
		case float32:
			return int(v), true
		case string:
			trimmed := strings.TrimSpace(v)
			if trimmed == "" {
				continue
			}
			parsed, err := strconv.Atoi(trimmed)
			if err != nil {
				continue
			}
			return parsed, true
		}
	}
	return 0, false
}

func readBool(raw map[string]any, keys ...string) (bool, bool) {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case bool:
			return v, true
		case string:
			b, err := strconv.ParseBool(strings.TrimSpace(v))
			if err == nil {
				return b, true
			}
		}
	}
	return false, false
}
