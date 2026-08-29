package feishu

import (
	"errors"
	"strings"

	lark "github.com/larksuite/oapi-sdk-go/v3"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/redact"
)

const (
	regionFeishu = "feishu"
	regionLark   = "lark"

	inboundModeWebsocket = "websocket"
	inboundModeWebhook   = "webhook"
)

// Config holds the Feishu app credentials extracted from a channel configuration.
type Config struct {
	AppID             string
	AppSecret         string
	EncryptKey        string
	VerificationToken string
	Region            string
	InboundMode       string
}

// UserConfig holds the identifiers used to target a Feishu user.
type UserConfig struct {
	OpenID string
	UserID string
}

func normalizeConfig(raw map[string]any) (map[string]any, error) {
	cfg, err := parseConfig(raw)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"appId":       cfg.AppID,
		"appSecret":   cfg.AppSecret,
		"region":      cfg.Region,
		"inboundMode": cfg.InboundMode,
	}
	if cfg.EncryptKey != "" {
		result["encryptKey"] = cfg.EncryptKey
	}
	if cfg.VerificationToken != "" {
		result["verificationToken"] = cfg.VerificationToken
	}
	return result, nil
}

func normalizeUserConfig(raw map[string]any) (map[string]any, error) {
	cfg, err := parseUserConfig(raw)
	if err != nil {
		return nil, err
	}
	result := map[string]any{}
	if cfg.OpenID != "" {
		result["open_id"] = cfg.OpenID
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
	if cfg.OpenID != "" {
		return "open_id:" + cfg.OpenID, nil
	}
	if cfg.UserID != "" {
		return "user_id:" + cfg.UserID, nil
	}
	return "", errors.New("feishu binding is incomplete")
}

func matchBinding(raw map[string]any, criteria channel.BindingCriteria) bool {
	cfg, err := parseUserConfig(raw)
	if err != nil {
		return false
	}
	if value := strings.TrimSpace(criteria.Attribute("open_id")); value != "" && value == cfg.OpenID {
		return true
	}
	if value := strings.TrimSpace(criteria.Attribute("user_id")); value != "" && value == cfg.UserID {
		return true
	}
	if criteria.SubjectID != "" {
		if criteria.SubjectID == cfg.OpenID || criteria.SubjectID == cfg.UserID {
			return true
		}
	}
	return false
}

func buildUserConfig(identity channel.Identity) map[string]any {
	result := map[string]any{}
	if value := strings.TrimSpace(identity.Attribute("open_id")); value != "" {
		result["open_id"] = value
	}
	if value := strings.TrimSpace(identity.Attribute("user_id")); value != "" {
		result["user_id"] = value
	}
	return result
}

func parseConfig(raw map[string]any) (Config, error) {
	appID := strings.TrimSpace(channel.ReadString(raw, "appId", "app_id"))
	appSecret := strings.TrimSpace(channel.ReadString(raw, "appSecret", "app_secret"))
	encryptKey := strings.TrimSpace(channel.ReadString(raw, "encryptKey", "encrypt_key"))
	verificationToken := strings.TrimSpace(channel.ReadString(raw, "verificationToken", "verification_token"))
	region, err := normalizeRegion(channel.ReadString(raw, "region"))
	if err != nil {
		return Config{}, err
	}
	inboundMode, err := normalizeInboundMode(channel.ReadString(raw, "inboundMode", "inbound_mode"))
	if err != nil {
		return Config{}, err
	}
	if appID == "" || appSecret == "" {
		return Config{}, errors.New("feishu appId and appSecret are required")
	}
	if inboundMode == inboundModeWebhook && encryptKey == "" && verificationToken == "" {
		return Config{}, errors.New("feishu webhook mode requires encrypt_key or verification_token")
	}
	return Config{
		AppID:             appID,
		AppSecret:         appSecret,
		EncryptKey:        encryptKey,
		VerificationToken: verificationToken,
		Region:            region,
		InboundMode:       inboundMode,
	}, nil
}

func parseUserConfig(raw map[string]any) (UserConfig, error) {
	openID := strings.TrimSpace(channel.ReadString(raw, "openId", "open_id"))
	userID := strings.TrimSpace(channel.ReadString(raw, "userId", "user_id"))
	if openID == "" && userID == "" {
		return UserConfig{}, errors.New("feishu user config requires open_id or user_id")
	}
	return UserConfig{OpenID: openID, UserID: userID}, nil
}

func normalizeTarget(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "open_id:") || strings.HasPrefix(value, "user_id:") || strings.HasPrefix(value, "chat_id:") {
		return value
	}
	if strings.HasPrefix(value, "ou_") {
		return "open_id:" + value
	}
	if strings.HasPrefix(value, "oc_") {
		return "chat_id:" + value
	}
	return "open_id:" + value
}

func normalizeRegion(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", regionFeishu, "cn", "china":
		return regionFeishu, nil
	case regionLark, "global", "intl", "international":
		return regionLark, nil
	default:
		return "", errors.New("feishu region must be feishu or lark")
	}
}

func normalizeInboundMode(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", inboundModeWebsocket:
		return inboundModeWebsocket, nil
	case inboundModeWebhook:
		return inboundModeWebhook, nil
	default:
		return "", errors.New("feishu inbound_mode must be websocket or webhook")
	}
}

func (c Config) openBaseURL() string {
	if c.Region == regionLark {
		return lark.LarkBaseUrl
	}
	return lark.FeishuBaseUrl
}

func (c Config) registerIMErrorSecrets() {
	redact.SetSecrets("feishu:"+c.AppID, c.AppSecret, c.EncryptKey, c.VerificationToken)
}

func (c Config) newClient() *lark.Client {
	c.registerIMErrorSecrets()
	return lark.NewClient(c.AppID, c.AppSecret, lark.WithOpenBaseUrl(c.openBaseURL()))
}
