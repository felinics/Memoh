package qq

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/felinics/memoh/internal/channel"
	identitypkg "github.com/felinics/memoh/internal/channel/identities"
	routepkg "github.com/felinics/memoh/internal/channel/route"
	"github.com/felinics/memoh/internal/media"
	"github.com/felinics/memoh/internal/redact"
)

const (
	defaultAPIBaseURL     = "https://api.sgroup.qq.com"
	qqOAuthEndpoint       = "https://bots.qq.com/app/getAppAccessToken"
	defaultChunkLimit     = 2000
	defaultReadTimeout    = 45 * time.Second
	defaultWriteTimeout   = 15 * time.Second
	qqVerificationTimeout = 15 * time.Second
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
	// inputHints tracks per-turn input-notify renewal loops keyed by the
	// source message id, so Completed/Failed can stop them.
	inputHints map[string]chan struct{}
	assets     assetOpener
	identity   channelIdentityResolver
	routes     routeResolver
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
		inputHints: make(map[string]chan struct{}),
	}
}

func (*QQAdapter) Type() channel.ChannelType {
	return Type
}

func (*QQAdapter) Descriptor() channel.Descriptor {
	return channel.Descriptor{
		Type:        Type,
		DisplayName: "QQ",
		Capabilities: channel.ChannelCapabilities{
			Text:           true,
			Markdown:       true,
			Attachments:    true,
			Media:          true,
			Reply:          true,
			BlockStreaming: true,
			ChatTypes:      []string{channel.ConversationTypePrivate, channel.ConversationTypeGroup, channel.ConversationTypeThread},
		},
		OutboundPolicy: channel.OutboundPolicy{
			TextChunkLimit:      defaultChunkLimit,
			ChunkerMode:         channel.ChunkerModeMarkdown,
			MediaOrder:          channel.OutboundOrderTextFirst,
			InlineTextWithMedia: true,
		},
		ConfigSchema: channel.ConfigSchema{
			Version: 1,
			Fields: map[string]channel.FieldSchema{
				"appId": {
					Type:     channel.FieldString,
					Required: true,
					Title:    "App ID",
				},
				"clientSecret": {
					Type:     channel.FieldSecret,
					Required: true,
					Title:    "Client Secret",
				},
				"markdownSupport": {
					Type:        channel.FieldBool,
					Title:       "Markdown Support",
					Description: "Enable QQ markdown message mode for C2C and group replies when the bot has permission.",
				},
				"enableInputHint": {
					Type:        channel.FieldBool,
					Title:       "Input Hint",
					Description: "Send QQ input-notify hints for direct messages while the bot is processing.",
				},
				"enableStreaming": {
					Type:        channel.FieldBool,
					Title:       "Stream Messages",
					Description: "Stream direct-message replies via QQ stream messages (typewriter effect). Falls back to a single buffered message on failure.",
				},
			},
		},
		UserConfigSchema: channel.ConfigSchema{
			Version: 1,
			Fields: map[string]channel.FieldSchema{
				"target_type": {
					Type:     channel.FieldEnum,
					Required: true,
					Title:    "Target Type",
					Enum:     []string{"c2c", "group", "channel"},
				},
				"target_id": {
					Type:     channel.FieldString,
					Required: true,
					Title:    "Target ID",
				},
			},
		},
		TargetSpec: channel.TargetSpec{
			Format: "c2c:<openid> | group:<group_openid> | channel:<channel_id>",
			Hints: []channel.TargetHint{
				{Label: "Direct", Example: "c2c:00112233445566778899AABBCCDDEEFF"},
				{Label: "Group", Example: "group:00112233445566778899AABBCCDDEEFF"},
				{Label: "Channel", Example: "channel:1234567890"},
			},
		},
	}
}

func (*QQAdapter) SelfIdentityPolicy() channel.SelfIdentityPolicy {
	return channel.SelfIdentityPolicy{
		RefreshOnCredentialsChange: true,
		RequireDiscoveryOnEnable:   true,
		RequiredSelfIdentityKey:    "app_id",
		DiscoveryErrorMessage:      "qq bot credential verification failed",
		MissingIdentityMessage:     "qq bot credential verification returned no app id",
	}
}

// DiscoverSelf validates the app credentials by acquiring an access token.
// QQ does not expose a separate current-bot identity endpoint, so the app ID is
// used as the stable external identity.
func (a *QQAdapter) DiscoverSelf(ctx context.Context, credentials map[string]any) (map[string]any, string, error) {
	cfg, err := parseConfig(credentials)
	if err != nil {
		return nil, "", err
	}
	httpClient := a.httpClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	tokenURL := strings.TrimSpace(a.tokenURL)
	if tokenURL == "" {
		tokenURL = qqOAuthEndpoint
	}
	callCtx, cancel := context.WithTimeout(ctx, qqVerificationTimeout)
	defer cancel()
	client := &qqClient{
		appID:        cfg.AppID,
		clientSecret: cfg.AppSecret,
		httpClient:   httpClient,
		logger:       a.logger,
		apiBaseURL:   a.apiBaseURL,
		tokenURL:     tokenURL,
		msgSeq:       make(map[string]int),
	}
	if _, err := client.accessToken(callCtx); err != nil {
		return nil, "", fmt.Errorf("qq discover self: %w", err)
	}
	return map[string]any{"app_id": cfg.AppID}, cfg.AppID, nil
}

func (*QQAdapter) ResolveOutboundCapabilities(cfg channel.ChannelConfig, target string, base channel.ChannelCapabilities) channel.ChannelCapabilities {
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

func (a *QQAdapter) ResolveOutboundTarget(ctx context.Context, _ channel.ChannelConfig, target string) (string, error) {
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

func (*QQAdapter) MatchBinding(config map[string]any, criteria channel.BindingCriteria) bool {
	return matchBinding(config, criteria)
}

func (*QQAdapter) BuildUserConfig(identity channel.Identity) map[string]any {
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

func (a *QQAdapter) ProcessingStarted(ctx context.Context, cfg channel.ChannelConfig, _ channel.InboundMessage, info channel.ProcessingStatusInfo) (channel.ProcessingStatusHandle, error) {
	parsed, err := parseConfig(cfg.Credentials)
	if err != nil {
		return channel.ProcessingStatusHandle{}, err
	}
	if !parsed.EnableInputHint || strings.TrimSpace(info.SourceMessageID) == "" {
		return channel.ProcessingStatusHandle{}, nil
	}
	target, err := parseTarget(info.ReplyTarget)
	if err != nil || target.Kind != qqTargetC2C {
		return channel.ProcessingStatusHandle{}, nil
	}
	client := a.getOrCreateClient(cfg, parsed)
	if err := client.sendInputHint(ctx, target.ID, info.SourceMessageID); err != nil {
		return channel.ProcessingStatusHandle{}, err
	}
	// The hint expires after input_second (max 60s); renew it so long turns
	// keep showing "typing" until Completed/Failed stops the loop.
	stop := make(chan struct{})
	token := strings.TrimSpace(info.SourceMessageID)
	a.mu.Lock()
	if old, ok := a.inputHints[token]; ok {
		close(old)
	}
	a.inputHints[token] = stop
	a.mu.Unlock()
	// The callback ctx is canceled as soon as ProcessingStarted returns
	// (callers wrap it in a short timeout), so the renewal loop must run on
	// a cancellation-free context and terminate via the stop channel alone.
	go a.renewInputHint(context.WithoutCancel(ctx), client, target.ID, token, stop)
	return channel.ProcessingStatusHandle{Token: token}, nil
}

func (a *QQAdapter) ProcessingCompleted(_ context.Context, _ channel.ChannelConfig, _ channel.InboundMessage, _ channel.ProcessingStatusInfo, handle channel.ProcessingStatusHandle) error {
	a.stopInputHintRenewal(handle.Token)
	return nil
}

func (a *QQAdapter) ProcessingFailed(_ context.Context, _ channel.ChannelConfig, _ channel.InboundMessage, _ channel.ProcessingStatusInfo, handle channel.ProcessingStatusHandle, _ error) error {
	a.stopInputHintRenewal(handle.Token)
	return nil
}

const (
	inputHintRenewInterval = 50 * time.Second
	// Hints ride the passive-reply quota (C2C: 4 per message), so the first
	// hint plus three renewals (~3 min of "typing") is the safe ceiling.
	inputHintMaxRenewals = 3
)

func (a *QQAdapter) renewInputHint(ctx context.Context, client *qqClient, openID, replyTo string, stop chan struct{}) {
	ticker := time.NewTicker(inputHintRenewInterval)
	defer ticker.Stop()
	for range inputHintMaxRenewals {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := client.sendInputHint(ctx, openID, replyTo); err != nil && a.logger != nil {
				a.logger.Debug("qq input hint renewal failed", slog.String("error", err.Error()))
			}
		}
	}
}

func (a *QQAdapter) stopInputHintRenewal(token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if stop, ok := a.inputHints[token]; ok {
		close(stop)
		delete(a.inputHints, token)
	}
}

func (a *QQAdapter) getOrCreateClient(cfg channel.ChannelConfig, parsed Config) *qqClient {
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
