package misskey

import (
	"errors"
	"strings"

	"github.com/felinics/memoh/internal/channel"
)

// Config holds the Misskey instance credentials extracted from a channel configuration.
type Config struct {
	InstanceURL string // Misskey instance URL (e.g. https://misskey.io)
	AccessToken string `json:"AccessToken"` //nolint:gosec // G117: token field, handled securely
}

// apiURL returns the base API URL with trailing slashes removed.
func (c Config) apiURL() string {
	return strings.TrimRight(c.InstanceURL, "/") + "/api"
}

// streamURL returns the WebSocket streaming URL.
func (c Config) streamURL() string {
	base := strings.TrimRight(c.InstanceURL, "/")
	// Replace http(s) with ws(s)
	base = strings.Replace(base, "https://", "wss://", 1)
	base = strings.Replace(base, "http://", "ws://", 1)
	return base + "/streaming?i=" + c.AccessToken
}

// UserConfig holds the identifiers used to target a Misskey user.
type UserConfig struct {
	Username string
	UserID   string
}

func normalizeConfig(raw map[string]any) (map[string]any, error) {
	cfg, err := parseConfig(raw)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"instanceURL": cfg.InstanceURL,
		"accessToken": cfg.AccessToken,
	}
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
	return result, nil
}

func resolveTarget(raw map[string]any) (string, error) {
	cfg, err := parseUserConfig(raw)
	if err != nil {
		return "", err
	}
	if cfg.UserID != "" {
		return cfg.UserID, nil
	}
	if cfg.Username != "" {
		return "@" + cfg.Username, nil
	}
	return "", errors.New("misskey binding is incomplete")
}

func normalizeTarget(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	// Strip common prefixes
	value = strings.TrimPrefix(value, "misskey:")
	value = strings.TrimSpace(value)
	return value
}

func matchBinding(raw map[string]any, criteria channel.BindingCriteria) bool {
	cfg, err := parseUserConfig(raw)
	if err != nil {
		return false
	}
	if value := strings.TrimSpace(criteria.Attribute("user_id")); value != "" && value == cfg.UserID {
		return true
	}
	if value := strings.TrimSpace(criteria.Attribute("username")); value != "" && strings.EqualFold(value, cfg.Username) {
		return true
	}
	if criteria.SubjectID != "" {
		if criteria.SubjectID == cfg.UserID || strings.EqualFold(criteria.SubjectID, cfg.Username) {
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
	return result
}

func parseConfig(raw map[string]any) (Config, error) {
	instanceURL := strings.TrimSpace(channel.ReadString(raw, "instanceURL", "instance_url"))
	if instanceURL == "" {
		return Config{}, errors.New("misskey instanceURL is required")
	}
	token := strings.TrimSpace(channel.ReadString(raw, "accessToken", "access_token"))
	if token == "" {
		return Config{}, errors.New("misskey accessToken is required")
	}
	return Config{InstanceURL: instanceURL, AccessToken: token}, nil
}

func parseUserConfig(raw map[string]any) (UserConfig, error) {
	username := strings.TrimSpace(channel.ReadString(raw, "username"))
	userID := strings.TrimSpace(channel.ReadString(raw, "userId", "user_id"))
	if username == "" && userID == "" {
		return UserConfig{}, errors.New("misskey user config requires username or user_id")
	}
	return UserConfig{Username: username, UserID: userID}, nil
}
