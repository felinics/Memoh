package qq

import (
	"errors"
	"strings"

	"github.com/felinics/memoh/internal/channel"
)

type Config struct {
	AppID           string
	AppSecret       string
	MarkdownSupport bool
	EnableInputHint bool
}

type UserConfig struct {
	TargetType string
	TargetID   string
}

func normalizeConfig(raw map[string]any) (map[string]any, error) {
	cfg, err := parseConfig(raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"appId":           cfg.AppID,
		"clientSecret":    cfg.AppSecret,
		"markdownSupport": cfg.MarkdownSupport,
		"enableInputHint": cfg.EnableInputHint,
	}, nil
}

func normalizeUserConfig(raw map[string]any) (map[string]any, error) {
	cfg, err := parseUserConfig(raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"target_type": cfg.TargetType,
		"target_id":   cfg.TargetID,
	}, nil
}

func resolveTarget(raw map[string]any) (string, error) {
	cfg, err := parseUserConfig(raw)
	if err != nil {
		return "", err
	}
	return cfg.TargetType + ":" + cfg.TargetID, nil
}

func matchBinding(raw map[string]any, criteria channel.BindingCriteria) bool {
	cfg, err := parseUserConfig(raw)
	if err != nil {
		return false
	}
	subjectID := strings.TrimSpace(criteria.SubjectID)
	if cfg.TargetType == "c2c" && subjectID != "" && subjectID == cfg.TargetID {
		return true
	}
	if cfg.TargetType == "c2c" && strings.TrimSpace(criteria.Attribute("user_openid")) == cfg.TargetID {
		return true
	}
	if cfg.TargetType == "group" && strings.TrimSpace(criteria.Attribute("group_openid")) == cfg.TargetID {
		return true
	}
	if cfg.TargetType == "channel" && strings.TrimSpace(criteria.Attribute("channel_id")) == cfg.TargetID {
		return true
	}
	return false
}

func buildUserConfig(identity channel.Identity) map[string]any {
	targetID := strings.TrimSpace(identity.Attribute("user_openid"))
	if targetID == "" {
		targetID = strings.TrimSpace(identity.SubjectID)
	}
	if targetID == "" {
		return map[string]any{}
	}
	return map[string]any{
		"target_type": "c2c",
		"target_id":   targetID,
	}
}

func parseConfig(raw map[string]any) (Config, error) {
	appID := strings.TrimSpace(channel.ReadString(raw, "appId", "app_id"))
	clientSecret := strings.TrimSpace(channel.ReadString(raw, "clientSecret", "client_secret"))
	if appID == "" {
		return Config{}, errors.New("qq appId is required")
	}
	if clientSecret == "" {
		return Config{}, errors.New("qq clientSecret is required")
	}
	return Config{
		AppID:           appID,
		AppSecret:       clientSecret,
		MarkdownSupport: readBool(raw, true, "markdownSupport", "markdown_support"),
		EnableInputHint: readBool(raw, true, "enableInputHint", "enable_input_hint"),
	}, nil
}

func parseUserConfig(raw map[string]any) (UserConfig, error) {
	targetType := strings.ToLower(strings.TrimSpace(channel.ReadString(raw, "targetType", "target_type")))
	targetID := strings.TrimSpace(channel.ReadString(raw, "targetId", "target_id"))
	if targetType == "" || targetID == "" {
		switch {
		case strings.TrimSpace(channel.ReadString(raw, "userOpenid", "user_openid")) != "":
			targetType = "c2c"
			targetID = strings.TrimSpace(channel.ReadString(raw, "userOpenid", "user_openid"))
		case strings.TrimSpace(channel.ReadString(raw, "groupOpenid", "group_openid")) != "":
			targetType = "group"
			targetID = strings.TrimSpace(channel.ReadString(raw, "groupOpenid", "group_openid"))
		case strings.TrimSpace(channel.ReadString(raw, "channelId", "channel_id")) != "":
			targetType = "channel"
			targetID = strings.TrimSpace(channel.ReadString(raw, "channelId", "channel_id"))
		}
	}
	if targetType == "" || targetID == "" {
		return UserConfig{}, errors.New("qq user config requires target_type and target_id")
	}
	switch targetType {
	case "c2c", "group", "channel":
	default:
		return UserConfig{}, errors.New("qq target_type must be c2c, group, or channel")
	}
	return UserConfig{TargetType: targetType, TargetID: targetID}, nil
}

func normalizeTarget(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	for _, prefix := range []string{"qq:", "qqbot:"} {
		if strings.HasPrefix(strings.ToLower(value), prefix) {
			value = strings.TrimSpace(value[len(prefix):])
			break
		}
	}
	for _, targetType := range []string{"c2c:", "group:", "channel:"} {
		if strings.HasPrefix(strings.ToLower(value), targetType) {
			return strings.ToLower(targetType[:len(targetType)-1]) + ":" + strings.TrimSpace(value[len(targetType):])
		}
	}
	return "c2c:" + value
}

func readBool(raw map[string]any, fallback bool, keys ...string) bool {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case bool:
			return v
		case string:
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "true", "1", "yes", "on":
				return true
			case "false", "0", "no", "off":
				return false
			}
		}
	}
	return fallback
}
