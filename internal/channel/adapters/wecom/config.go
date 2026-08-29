package wecom

import (
	"errors"
	"strconv"
	"strings"

	"github.com/felinics/memoh/internal/channel"
)

type adapterConfig struct {
	BotID               string
	Credential          string
	WSURL               string
	HeartbeatSeconds    int
	AckTimeoutSeconds   int
	WriteTimeoutSeconds int
	ReadTimeoutSeconds  int
}

type UserConfig struct {
	ChatID string
	UserID string
}

func normalizeConfig(raw map[string]any) (map[string]any, error) {
	cfg, err := parseConfig(raw)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"botId":  cfg.BotID,
		"secret": cfg.Credential,
	}
	if cfg.WSURL != "" {
		out["wsUrl"] = cfg.WSURL
	}
	if cfg.HeartbeatSeconds > 0 {
		out["heartbeatSeconds"] = cfg.HeartbeatSeconds
	}
	if cfg.AckTimeoutSeconds > 0 {
		out["ackTimeoutSeconds"] = cfg.AckTimeoutSeconds
	}
	if cfg.WriteTimeoutSeconds > 0 {
		out["writeTimeoutSeconds"] = cfg.WriteTimeoutSeconds
	}
	if cfg.ReadTimeoutSeconds > 0 {
		out["readTimeoutSeconds"] = cfg.ReadTimeoutSeconds
	}
	return out, nil
}

func normalizeUserConfig(raw map[string]any) (map[string]any, error) {
	cfg, err := parseUserConfig(raw)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if cfg.ChatID != "" {
		out["chat_id"] = cfg.ChatID
	}
	if cfg.UserID != "" {
		out["user_id"] = cfg.UserID
	}
	return out, nil
}

func parseConfig(raw map[string]any) (adapterConfig, error) {
	cfg := adapterConfig{
		BotID:      strings.TrimSpace(channel.ReadString(raw, "botId", "bot_id")),
		Credential: strings.TrimSpace(channel.ReadString(raw, "secret")),
		WSURL:      strings.TrimSpace(channel.ReadString(raw, "wsUrl", "ws_url")),
	}
	if value, ok := readInt(raw, "heartbeatSeconds", "heartbeat_seconds"); ok {
		cfg.HeartbeatSeconds = value
	}
	if value, ok := readInt(raw, "ackTimeoutSeconds", "ack_timeout_seconds"); ok {
		cfg.AckTimeoutSeconds = value
	}
	if value, ok := readInt(raw, "writeTimeoutSeconds", "write_timeout_seconds"); ok {
		cfg.WriteTimeoutSeconds = value
	}
	if value, ok := readInt(raw, "readTimeoutSeconds", "read_timeout_seconds"); ok {
		cfg.ReadTimeoutSeconds = value
	}
	if cfg.BotID == "" || cfg.Credential == "" {
		return adapterConfig{}, errors.New("wecom botId and secret are required")
	}
	return cfg, nil
}

func parseUserConfig(raw map[string]any) (UserConfig, error) {
	cfg := UserConfig{
		ChatID: strings.TrimSpace(channel.ReadString(raw, "chatId", "chat_id")),
		UserID: strings.TrimSpace(channel.ReadString(raw, "userId", "user_id")),
	}
	if cfg.ChatID == "" && cfg.UserID == "" {
		return UserConfig{}, errors.New("wecom user config requires chat_id or user_id")
	}
	return cfg, nil
}

func resolveTarget(raw map[string]any) (string, error) {
	cfg, err := parseUserConfig(raw)
	if err != nil {
		return "", err
	}
	if cfg.ChatID != "" {
		return "chat_id:" + cfg.ChatID, nil
	}
	return "user_id:" + cfg.UserID, nil
}

func normalizeTarget(raw string) string {
	kind, id, ok := parseTarget(raw)
	if !ok {
		return ""
	}
	return kind + ":" + id
}

func parseTarget(raw string) (kind string, id string, ok bool) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", "", false
	}
	v = strings.TrimPrefix(v, "wecom:")
	v = strings.TrimPrefix(v, "workwx:")
	v = strings.TrimSpace(v)
	lv := strings.ToLower(v)
	switch {
	case strings.HasPrefix(lv, "chat_id:"):
		id = strings.TrimSpace(v[len("chat_id:"):])
		return "chat_id", id, id != ""
	case strings.HasPrefix(lv, "chat:"):
		id = strings.TrimSpace(v[len("chat:"):])
		return "chat_id", id, id != ""
	case strings.HasPrefix(lv, "group:"):
		id = strings.TrimSpace(v[len("group:"):])
		return "chat_id", id, id != ""
	case strings.HasPrefix(lv, "user_id:"):
		id = strings.TrimSpace(v[len("user_id:"):])
		return "user_id", id, id != ""
	case strings.HasPrefix(lv, "user:"):
		id = strings.TrimSpace(v[len("user:"):])
		return "user_id", id, id != ""
	default:
		return "chat_id", v, true
	}
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
	if criteria.SubjectID != "" && (criteria.SubjectID == cfg.ChatID || criteria.SubjectID == cfg.UserID) {
		return true
	}
	return false
}

func buildUserConfig(identity channel.Identity) map[string]any {
	out := map[string]any{}
	if v := strings.TrimSpace(identity.Attribute("chat_id")); v != "" {
		out["chat_id"] = v
	}
	if v := strings.TrimSpace(identity.Attribute("user_id")); v != "" {
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
