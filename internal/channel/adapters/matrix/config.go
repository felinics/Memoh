package matrix

import (
	"errors"
	"strconv"
	"strings"

	"github.com/felinics/memoh/internal/channel"
)

type Config struct {
	HomeserverURL      string
	AccessToken        string //nolint:gosec // intentional: operator-supplied Matrix access token in channel config
	UserID             string
	SyncTimeoutSeconds int
	AutoJoinInvites    bool
}

type UserConfig struct {
	RoomID string
	UserID string
}

func normalizeConfig(raw map[string]any) (map[string]any, error) {
	cfg, err := parseConfig(raw)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"homeserverUrl":      cfg.HomeserverURL,
		"accessToken":        cfg.AccessToken,
		"userId":             cfg.UserID,
		"syncTimeoutSeconds": cfg.SyncTimeoutSeconds,
		"autoJoinInvites":    cfg.AutoJoinInvites,
	}
	return out, nil
}

func normalizeUserConfig(raw map[string]any) (map[string]any, error) {
	cfg, err := parseUserConfig(raw)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if cfg.RoomID != "" {
		out["room_id"] = cfg.RoomID
	}
	if cfg.UserID != "" {
		out["user_id"] = cfg.UserID
	}
	return out, nil
}

func resolveTarget(raw map[string]any) (string, error) {
	cfg, err := parseUserConfig(raw)
	if err != nil {
		return "", err
	}
	if cfg.RoomID != "" {
		return cfg.RoomID, nil
	}
	if cfg.UserID != "" {
		return cfg.UserID, nil
	}
	return "", errors.New("matrix user config requires room_id or user_id")
}

func matchBinding(raw map[string]any, criteria channel.BindingCriteria) bool {
	cfg, err := parseUserConfig(raw)
	if err != nil {
		return false
	}
	if cfg.UserID != "" && strings.EqualFold(strings.TrimSpace(criteria.SubjectID), cfg.UserID) {
		return true
	}
	return false
}

func buildUserConfig(identity channel.Identity) map[string]any {
	userID := strings.TrimSpace(identity.Attribute("user_id"))
	if userID == "" {
		userID = strings.TrimSpace(identity.SubjectID)
	}
	if userID == "" {
		return map[string]any{}
	}
	return map[string]any{"user_id": userID}
}

func parseConfig(raw map[string]any) (Config, error) {
	homeserverURL := normalizeHomeserverURL(channel.ReadString(raw, "homeserverUrl", "homeserver_url", "homeserver"))
	accessToken := strings.TrimSpace(channel.ReadString(raw, "accessToken", "access_token"))
	userID := strings.TrimSpace(channel.ReadString(raw, "userId", "user_id"))
	if homeserverURL == "" {
		return Config{}, errors.New("matrix homeserverUrl is required")
	}
	if accessToken == "" {
		return Config{}, errors.New("matrix accessToken is required")
	}
	if userID == "" {
		return Config{}, errors.New("matrix userId is required")
	}
	timeout := readInt(raw, 30, "syncTimeoutSeconds", "sync_timeout_seconds")
	if timeout < 0 {
		timeout = 0
	}
	autoJoinInvites := readBool(raw, true, "autoJoinInvites", "auto_join_invites")
	return Config{
		HomeserverURL:      homeserverURL,
		AccessToken:        accessToken,
		UserID:             userID,
		SyncTimeoutSeconds: timeout,
		AutoJoinInvites:    autoJoinInvites,
	}, nil
}

func parseUserConfig(raw map[string]any) (UserConfig, error) {
	roomID := normalizeTarget(channel.ReadString(raw, "roomId", "room_id"))
	userID := normalizeTarget(channel.ReadString(raw, "userId", "user_id"))
	if roomID == "" && userID == "" {
		return UserConfig{}, errors.New("matrix user config requires room_id or user_id")
	}
	if roomID != "" && !strings.HasPrefix(roomID, "!") && !strings.HasPrefix(roomID, "#") {
		return UserConfig{}, errors.New("matrix room_id must start with ! or #")
	}
	if userID != "" && !strings.HasPrefix(userID, "@") {
		return UserConfig{}, errors.New("matrix user_id must start with @")
	}
	return UserConfig{RoomID: roomID, UserID: userID}, nil
}

func normalizeTarget(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	for _, prefix := range []string{"matrix:", "room:", "user:"} {
		if strings.HasPrefix(strings.ToLower(value), prefix) {
			value = strings.TrimSpace(value[len(prefix):])
			break
		}
	}
	return value
}

func normalizeHomeserverURL(raw string) string {
	value := strings.TrimSpace(raw)
	return strings.TrimRight(value, "/")
}

func readInt(raw map[string]any, fallback int, keys ...string) int {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case int:
			return v
		case int64:
			return int(v)
		case float64:
			return int(v)
		case string:
			parsed, err := strconv.Atoi(strings.TrimSpace(v))
			if err == nil {
				return parsed
			}
		}
	}
	return fallback
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

func targetKind(target string) string {
	value := normalizeTarget(target)
	switch {
	case strings.HasPrefix(value, "!") || strings.HasPrefix(value, "#"):
		return "room"
	case strings.HasPrefix(value, "@"):
		return "user"
	default:
		return ""
	}
}

func validateTarget(target string) error {
	kind := targetKind(target)
	if kind == "" {
		return errors.New("matrix target must be a room id/alias or user id")
	}
	return nil
}
