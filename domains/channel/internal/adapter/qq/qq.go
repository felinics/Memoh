package qq

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/memohai/memoh/domains/channel/gateway"
	identitypkg "github.com/memohai/memoh/domains/channel/identity"
	routepkg "github.com/memohai/memoh/domains/channel/route"
	"github.com/memohai/memoh/domains/media"
	"github.com/memohai/memoh/internal/redact"
)

const (
	defaultAPIBaseURL   = "https://api.sgroup.qq.com"
	qqOAuthEndpoint     = "https://bots.qq.com/app/getAppAccessToken"
	defaultChunkLimit   = 2000
	defaultReadTimeout  = 45 * time.Second
	defaultWriteTimeout = 15 * time.Second
)

type assetOpener interface {
	Open(ctx context.Context, botID, contentHash string) (io.ReadCloser, media.Asset, error)
}

type sessionState struct {
	SessionID   string
	LastSeq     int
	IntentLevel int
}

type channelIdentityResolver interface {
	GetByID(ctx context.Context, channelIdentityID string) (identitypkg.ChannelIdentity, error)
	ListCanonicalChannelIdentities(ctx context.Context, channelIdentityID string) ([]identitypkg.ChannelIdentity, error)
}

type routeResolver interface {
	GetByID(ctx context.Context, routeID string) (routepkg.Route, error)
}

type QQAdapter struct {
	logger     *slog.Logger
	httpClient *http.Client
	dialer     *websocket.Dialer
	apiBaseURL string
	tokenURL   string

	mu       sync.Mutex
	clients  map[string]*qqClient
	sessions map[string]sessionState
	assets   assetOpener
	identity channelIdentityResolver
	routes   routeResolver
}

func NewQQAdapter(log *slog.Logger) *QQAdapter {
	if log == nil {
		log = slog.Default()
	}
	return &QQAdapter{
		logger: log.With(slog.String("adapter", "qq")),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		dialer: &websocket.Dialer{
			HandshakeTimeout: 15 * time.Second,
		},
		apiBaseURL: defaultAPIBaseURL,
		tokenURL:   qqOAuthEndpoint,
		clients:    make(map[string]*qqClient),
		sessions:   make(map[string]sessionState),
	}
}

func (*QQAdapter) Type() gateway.ChannelType {
	return Type
}

func (*QQAdapter) Descriptor() gateway.Descriptor {
	return gateway.Descriptor{
		Type:        Type,
		DisplayName: "QQ",
		Capabilities: gateway.ChannelCapabilities{
			Text:           true,
			Markdown:       true,
			Attachments:    true,
			Media:          true,
			Reply:          true,
			BlockStreaming: true,
			ChatTypes:      []string{gateway.ConversationTypePrivate, gateway.ConversationTypeGroup, gateway.ConversationTypeThread},
		},
		OutboundPolicy: gateway.OutboundPolicy{
			TextChunkLimit:      defaultChunkLimit,
			ChunkerMode:         gateway.ChunkerModeMarkdown,
			MediaOrder:          gateway.OutboundOrderTextFirst,
			InlineTextWithMedia: true,
		},
		ConfigSchema: gateway.ConfigSchema{
			Version: 1,
			Fields: map[string]gateway.FieldSchema{
				"appId": {
					Type:     gateway.FieldString,
					Required: true,
					Title:    "App ID",
				},
				"clientSecret": {
					Type:     gateway.FieldSecret,
					Required: true,
					Title:    "Client Secret",
				},
				"markdownSupport": {
					Type:        gateway.FieldBool,
					Title:       "Markdown Support",
					Description: "Enable QQ markdown message mode for C2C and group replies when the bot has permission.",
				},
				"enableInputHint": {
					Type:        gateway.FieldBool,
					Title:       "Input Hint",
					Description: "Send QQ input-notify hints for direct messages while the bot is processing.",
				},
			},
		},
		UserConfigSchema: gateway.ConfigSchema{
			Version: 1,
			Fields: map[string]gateway.FieldSchema{
				"target_type": {
					Type:     gateway.FieldEnum,
					Required: true,
					Title:    "Target Type",
					Enum:     []string{"c2c", "group", "channel"},
				},
				"target_id": {
					Type:     gateway.FieldString,
					Required: true,
					Title:    "Target ID",
				},
			},
		},
		TargetSpec: gateway.TargetSpec{
			Format: "c2c:<openid> | group:<group_openid> | channel:<channel_id>",
			Hints: []gateway.TargetHint{
				{Label: "Direct", Example: "c2c:00112233445566778899AABBCCDDEEFF"},
				{Label: "Group", Example: "group:00112233445566778899AABBCCDDEEFF"},
				{Label: "Channel", Example: "channel:1234567890"},
			},
		},
	}
}

func (*QQAdapter) ResolveOutboundCapabilities(cfg gateway.ChannelConfig, target string, base gateway.ChannelCapabilities) gateway.ChannelCapabilities {
	caps := base
	parsed, err := parseConfig(cfg.Credentials)
	if err != nil {
		return caps
	}
	parsedTarget, err := parseTarget(target)
	if err != nil {
		return caps
	}
	caps.Markdown = parsed.MarkdownSupport && parsedTarget.Kind != qqTargetChannel
	if parsedTarget.Kind == qqTargetChannel {
		caps.Attachments = false
		caps.Media = false
	}
	return caps
}

func (a *QQAdapter) ResolveOutboundTarget(ctx context.Context, _ gateway.ChannelConfig, target string) (string, error) {
	return a.resolveTarget(ctx, target)
}

func (*QQAdapter) NormalizeConfig(raw map[string]any) (map[string]any, error) {
	return normalizeConfig(raw)
}

func (*QQAdapter) NormalizeUserConfig(raw map[string]any) (map[string]any, error) {
	return normalizeUserConfig(raw)
}

func (*QQAdapter) NormalizeTarget(raw string) string {
	return normalizeTarget(raw)
}

func (*QQAdapter) ResolveTarget(userConfig map[string]any) (string, error) {
	return resolveTarget(userConfig)
}

func (*QQAdapter) MatchBinding(config map[string]any, criteria gateway.BindingCriteria) bool {
	return matchBinding(config, criteria)
}

func (*QQAdapter) BuildUserConfig(identity gateway.Identity) map[string]any {
	return buildUserConfig(identity)
}

func (a *QQAdapter) SetAssetOpener(opener assetOpener) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.assets = opener
}

func (a *QQAdapter) SetChannelIdentityResolver(resolver channelIdentityResolver) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.identity = resolver
}

func (a *QQAdapter) SetRouteResolver(resolver routeResolver) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.routes = resolver
}

func (a *QQAdapter) ProcessingStarted(ctx context.Context, cfg gateway.ChannelConfig, _ gateway.InboundMessage, info gateway.ProcessingStatusInfo) (gateway.ProcessingStatusHandle, error) {
	parsed, err := parseConfig(cfg.Credentials)
	if err != nil {
		return gateway.ProcessingStatusHandle{}, err
	}
	if !parsed.EnableInputHint || strings.TrimSpace(info.SourceMessageID) == "" {
		return gateway.ProcessingStatusHandle{}, nil
	}
	target, err := parseTarget(info.ReplyTarget)
	if err != nil || target.Kind != qqTargetC2C {
		return gateway.ProcessingStatusHandle{}, nil
	}
	client := a.getOrCreateClient(cfg, parsed)
	if err := client.sendInputHint(ctx, target.ID, info.SourceMessageID); err != nil {
		return gateway.ProcessingStatusHandle{}, err
	}
	return gateway.ProcessingStatusHandle{}, nil
}

func (*QQAdapter) ProcessingCompleted(context.Context, gateway.ChannelConfig, gateway.InboundMessage, gateway.ProcessingStatusInfo, gateway.ProcessingStatusHandle) error {
	return nil
}

func (*QQAdapter) ProcessingFailed(context.Context, gateway.ChannelConfig, gateway.InboundMessage, gateway.ProcessingStatusInfo, gateway.ProcessingStatusHandle, error) error {
	return nil
}

func (a *QQAdapter) getOrCreateClient(cfg gateway.ChannelConfig, parsed Config) *qqClient {
	redact.SetSecrets("qq:"+parsed.AppID, parsed.AppSecret)
	a.mu.Lock()
	defer a.mu.Unlock()

	existing, ok := a.clients[cfg.ID]
	if ok && existing.matches(parsed) {
		return existing
	}

	client := &qqClient{
		appID:        parsed.AppID,
		clientSecret: parsed.AppSecret,
		httpClient:   a.httpClient,
		logger:       a.logger,
		apiBaseURL:   a.apiBaseURL,
		tokenURL:     a.tokenURL,
		msgSeq:       make(map[string]int),
	}
	a.clients[cfg.ID] = client
	return client
}

func (a *QQAdapter) loadSession(configID string) sessionState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessions[configID]
}

func (a *QQAdapter) saveSession(configID string, state sessionState) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessions[configID] = state
}

func (a *QQAdapter) clearSession(configID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, configID)
}
