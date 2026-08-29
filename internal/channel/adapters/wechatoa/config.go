package wechatoa

import (
	"errors"
	"strings"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/common"
)

const (
	encryptionModePlain  = "plain"
	encryptionModeCompat = "compat"
	encryptionModeSafe   = "safe"
)

type Config struct {
	AppID          string
	AppSecret      string
	Token          string
	EncodingAESKey string
	EncryptionMode string
	HTTPProxy      common.HTTPProxyConfig
}

type UserConfig struct {
	OpenID  string
	UnionID string
}

func normalizeConfig(raw map[string]any) (map[string]any, error) {
	cfg, err := parseConfig(raw)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"appId":          cfg.AppID,
		"appSecret":      cfg.AppSecret,
		"token":          cfg.Token,
		"encryptionMode": cfg.EncryptionMode,
	}
	if cfg.EncodingAESKey != "" {
		out["encodingAESKey"] = cfg.EncodingAESKey
	}
	common.NormalizeHTTPProxyConfig(out, cfg.HTTPProxy)
	return out, nil
}

func parseConfig(raw map[string]any) (Config, error) {
	cfg := Config{
		AppID:          strings.TrimSpace(channel.ReadString(raw, "appId", "app_id")),
		AppSecret:      strings.TrimSpace(channel.ReadString(raw, "appSecret", "app_secret")),
		Token:          strings.TrimSpace(channel.ReadString(raw, "token")),
		EncodingAESKey: strings.TrimSpace(channel.ReadString(raw, "encodingAESKey", "encoding_aes_key")),
		EncryptionMode: normalizeEncryptionMode(channel.ReadString(raw, "encryptionMode", "encryption_mode")),
	}
	proxyCfg, err := common.ParseHTTPProxyConfig(raw)
	if err != nil {
		return Config{}, err
	}
	cfg.HTTPProxy = proxyCfg
	if cfg.AppID == "" || cfg.AppSecret == "" || cfg.Token == "" {
		return Config{}, errors.New("wechatoa appId, appSecret and token are required")
	}
	if cfg.EncryptionMode == "" {
		cfg.EncryptionMode = encryptionModeSafe
	}
	if (cfg.EncryptionMode == encryptionModeCompat || cfg.EncryptionMode == encryptionModeSafe) && cfg.EncodingAESKey == "" {
		return Config{}, errors.New("wechatoa encodingAESKey is required in compat/safe mode")
	}
	return cfg, nil
}

func normalizeEncryptionMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "security", "secure", encryptionModeSafe:
		return encryptionModeSafe
	case encryptionModePlain:
		return encryptionModePlain
	case encryptionModeCompat, "compatible":
		return encryptionModeCompat
	default:
		return ""
	}
}

func normalizeUserConfig(raw map[string]any) (map[string]any, error) {
	cfg, err := parseUserConfig(raw)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if cfg.OpenID != "" {
		out["openid"] = cfg.OpenID
	}
	if cfg.UnionID != "" {
		out["unionid"] = cfg.UnionID
	}
	return out, nil
}

func parseUserConfig(raw map[string]any) (UserConfig, error) {
	cfg := UserConfig{
		OpenID:  strings.TrimSpace(channel.ReadString(raw, "openid", "open_id")),
		UnionID: strings.TrimSpace(channel.ReadString(raw, "unionid", "union_id")),
	}
	if cfg.OpenID == "" && cfg.UnionID == "" {
		return UserConfig{}, errors.New("wechatoa user config requires openid or unionid")
	}
	return cfg, nil
}

func normalizeTarget(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return ""
	}
	v = strings.TrimPrefix(v, "wechatoa:")
	v = strings.TrimPrefix(v, "wechat:")
	v = strings.TrimSpace(v)
	lower := strings.ToLower(v)
	if strings.HasPrefix(lower, "openid:") {
		id := strings.TrimSpace(v[len("openid:"):])
		if id == "" {
			return ""
		}
		return "openid:" + id
	}
	return "openid:" + v
}

func resolveTarget(raw map[string]any) (string, error) {
	cfg, err := parseUserConfig(raw)
	if err != nil {
		return "", err
	}
	if cfg.OpenID == "" {
		return "", errors.New("wechatoa openid is required")
	}
	return "openid:" + cfg.OpenID, nil
}

func parseTarget(raw string) (string, bool) {
	v := normalizeTarget(raw)
	if v == "" {
		return "", false
	}
	return strings.TrimSpace(v[len("openid:"):]), true
}

func matchBinding(config map[string]any, criteria channel.BindingCriteria) bool {
	uc, err := parseUserConfig(config)
	if err != nil {
		return false
	}
	if subject := strings.TrimSpace(criteria.SubjectID); subject != "" && subject == uc.OpenID {
		return true
	}
	if value := strings.TrimSpace(criteria.Attribute("openid")); value != "" && value == uc.OpenID {
		return true
	}
	if value := strings.TrimSpace(criteria.Attribute("unionid")); value != "" && value == uc.UnionID {
		return true
	}
	return false
}

func buildUserConfig(identity channel.Identity) map[string]any {
	out := map[string]any{}
	if v := strings.TrimSpace(identity.Attribute("openid")); v != "" {
		out["openid"] = v
	}
	if v := strings.TrimSpace(identity.Attribute("unionid")); v != "" {
		out["unionid"] = v
	}
	return out
}
