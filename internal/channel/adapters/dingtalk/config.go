package dingtalk

import (
	"errors"
	"strings"

	"github.com/felinics/memoh/internal/channel"
)

type adapterConfig struct {
	AppKey    string
	AppSecret string
}

// UserConfig holds per-user delivery target data for DingTalk.
type UserConfig struct {
	// UserID is the recipient's DingTalk userId, used for single/private chat.
	UserID string
	// OpenConversationID is the group's openConversationId, used for group chat.
	OpenConversationID string
	// DisplayName is an optional human-readable label.
	DisplayName string
}

func normalizeConfig(raw map[string]any) (map[string]any, error) {
	cfg, err := parseConfig(raw)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"appKey":    cfg.AppKey,
		"appSecret": cfg.AppSecret,
	}
	return out, nil
}

func normalizeUserConfig(raw map[string]any) (map[string]any, error) {
	cfg, err := parseUserConfig(raw)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if cfg.UserID != "" {
		out["user_id"] = cfg.UserID
	}
	if cfg.OpenConversationID != "" {
		out["open_conversation_id"] = cfg.OpenConversationID
	}
	if cfg.DisplayName != "" {
		out["display_name"] = cfg.DisplayName
	}
	return out, nil
}

func parseConfig(raw map[string]any) (adapterConfig, error) {
	cfg := adapterConfig{
		AppKey:    strings.TrimSpace(channel.ReadString(raw, "appKey", "app_key")),
		AppSecret: strings.TrimSpace(channel.ReadString(raw, "appSecret", "app_secret")),
	}
	if cfg.AppKey == "" || cfg.AppSecret == "" {
		return adapterConfig{}, errors.New("dingtalk appKey and appSecret are required")
	}
	return cfg, nil
}

func parseUserConfig(raw map[string]any) (UserConfig, error) {
	cfg := UserConfig{
		UserID:             strings.TrimSpace(channel.ReadString(raw, "userId", "user_id")),
		OpenConversationID: strings.TrimSpace(channel.ReadString(raw, "openConversationId", "open_conversation_id")),
		DisplayName:        strings.TrimSpace(channel.ReadString(raw, "displayName", "display_name")),
	}
	if cfg.UserID == "" && cfg.OpenConversationID == "" {
		return UserConfig{}, errors.New("dingtalk user config requires user_id or open_conversation_id")
	}
	return cfg, nil
}

// resolveTarget converts a UserConfig to the canonical target string.
func resolveTarget(raw map[string]any) (string, error) {
	cfg, err := parseUserConfig(raw)
	if err != nil {
		return "", err
	}
	if cfg.OpenConversationID != "" {
		return "group:" + cfg.OpenConversationID, nil
	}
	return "user:" + cfg.UserID, nil
}

// normalizeTarget normalizes a raw target string to canonical form.
func normalizeTarget(raw string) string {
	kind, id, ok := parseTarget(raw)
	if !ok {
		return ""
	}
	return kind + ":" + id
}

// parseTarget parses a target string into (kind, id).
// kind is "user" or "group".
func parseTarget(raw string) (kind, id string, ok bool) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", "", false
	}
	lv := strings.ToLower(v)
	switch {
	case strings.HasPrefix(lv, "user:"):
		id = strings.TrimSpace(v[len("user:"):])
		return "user", id, id != ""
	case strings.HasPrefix(lv, "user_id:"):
		id = strings.TrimSpace(v[len("user_id:"):])
		return "user", id, id != ""
	case strings.HasPrefix(lv, "group:"):
		id = strings.TrimSpace(v[len("group:"):])
		return "group", id, id != ""
	case strings.HasPrefix(lv, "open_conversation_id:"):
		id = strings.TrimSpace(v[len("open_conversation_id:"):])
		return "group", id, id != ""
	default:
		// Bare value: treat as userId (private chat)
		return "user", v, true
	}
}

func matchBinding(raw map[string]any, criteria channel.BindingCriteria) bool {
	cfg, err := parseUserConfig(raw)
	if err != nil {
		return false
	}
	// Match against staff_id or user_id to handle both ISV and enterprise bots.
	if v := strings.TrimSpace(criteria.Attribute("staff_id")); v != "" && v == cfg.UserID {
		return true
	}
	if v := strings.TrimSpace(criteria.Attribute("user_id")); v != "" && v == cfg.UserID {
		return true
	}
	if v := strings.TrimSpace(criteria.Attribute("open_conversation_id")); v != "" && v == cfg.OpenConversationID {
		return true
	}
	if criteria.SubjectID != "" {
		if criteria.SubjectID == cfg.UserID || criteria.SubjectID == cfg.OpenConversationID {
			return true
		}
	}
	return false
}

func buildUserConfig(identity channel.Identity) map[string]any {
	out := map[string]any{}
	// Prefer staff_id (DingTalk staffId required by OpenAPI batchSend).
	// user_id may be senderId/unionId which is rejected by the API in ISV apps.
	userID := strings.TrimSpace(identity.Attribute("staff_id"))
	if userID == "" {
		userID = strings.TrimSpace(identity.Attribute("user_id"))
	}
	if userID != "" {
		out["user_id"] = userID
	}
	if v := strings.TrimSpace(identity.Attribute("open_conversation_id")); v != "" {
		out["open_conversation_id"] = v
	}
	if v := strings.TrimSpace(identity.DisplayName); v != "" {
		out["display_name"] = v
	}
	return out
}
