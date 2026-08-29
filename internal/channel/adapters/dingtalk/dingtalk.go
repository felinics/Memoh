package dingtalk

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/felinics/dingtalk-stream-sdk-go/chatbot"
	dtsdk "github.com/felinics/dingtalk-stream-sdk-go/client"

	"github.com/felinics/memoh/internal/channel"
)

// DingTalkAdapter implements the Memoh channel adapter for DingTalk bots.
// It uses the DingTalk Stream SDK (WebSocket) for inbound messages and
// the DingTalk OpenAPI (HTTP) for outbound messages.
type DingTalkAdapter struct {
	logger *slog.Logger

	// mu guards the clients and apiClients maps.
	mu         sync.RWMutex
	clients    map[string]*dtsdk.StreamClient
	apiClients map[string]*apiClient

	// webhookCache stores recent sessionWebhook contexts by config ID and msgId.
	webhookCache *sessionWebhookCache
}

// NewDingTalkAdapter creates a new DingTalkAdapter.
func NewDingTalkAdapter(log *slog.Logger) *DingTalkAdapter {
	if log == nil {
		log = slog.Default()
	}
	return &DingTalkAdapter{
		logger:       log.With(slog.String("adapter", "dingtalk")),
		clients:      make(map[string]*dtsdk.StreamClient),
		apiClients:   make(map[string]*apiClient),
		webhookCache: newSessionWebhookCache(30 * time.Minute),
	}
}

// SetAssetOpener is a no-op placeholder to match the adapter registration pattern.
func (*DingTalkAdapter) SetAssetOpener(_ any) {}

func (*DingTalkAdapter) Type() channel.ChannelType { return Type }

func (*DingTalkAdapter) Descriptor() channel.Descriptor {
	return channel.Descriptor{
		Type:        Type,
		DisplayName: "DingTalk",
		Capabilities: channel.ChannelCapabilities{
			Text:           true,
			Markdown:       true,
			Attachments:    true,
			Media:          true,
			BlockStreaming: true,
			// Reply is handled via session webhook (fast path in Send).
			// The OpenAPI path silently ignores the reply context.
			Reply:     true,
			ChatTypes: []string{channel.ConversationTypePrivate, channel.ConversationTypeGroup},
		},
		ConfigSchema: channel.ConfigSchema{
			Version: 1,
			Fields: map[string]channel.FieldSchema{
				"appKey":    {Type: channel.FieldString, Required: true, Title: "App Key (Client ID)"},
				"appSecret": {Type: channel.FieldSecret, Required: true, Title: "App Secret (Client Secret)"},
			},
		},
		UserConfigSchema: channel.ConfigSchema{
			Version: 1,
			Fields: map[string]channel.FieldSchema{
				"user_id":              {Type: channel.FieldString, Title: "User ID (single chat)"},
				"open_conversation_id": {Type: channel.FieldString, Title: "Open Conversation ID (group chat)"},
				"display_name":         {Type: channel.FieldString, Title: "Display Name"},
			},
		},
		TargetSpec: channel.TargetSpec{
			Format: "user:{userId} | group:{openConversationId}",
			Hints: []channel.TargetHint{
				{Label: "User", Example: "user:user123"},
				{Label: "Group", Example: "group:cidXXXXXXXX"},
			},
		},
	}
}

// NormalizeConfig validates and normalizes a DingTalk channel config map.
func (*DingTalkAdapter) NormalizeConfig(raw map[string]any) (map[string]any, error) {
	return normalizeConfig(raw)
}

// NormalizeUserConfig validates and normalizes a DingTalk user binding config map.
func (*DingTalkAdapter) NormalizeUserConfig(raw map[string]any) (map[string]any, error) {
	return normalizeUserConfig(raw)
}

// NormalizeTarget normalizes a raw target string to canonical form.
func (*DingTalkAdapter) NormalizeTarget(raw string) string { return normalizeTarget(raw) }

// ResolveTarget converts a user config map to a canonical delivery target string.
func (*DingTalkAdapter) ResolveTarget(userConfig map[string]any) (string, error) {
	return resolveTarget(userConfig)
}

// MatchBinding checks whether a user binding config matches the given criteria.
func (*DingTalkAdapter) MatchBinding(config map[string]any, criteria channel.BindingCriteria) bool {
	return matchBinding(config, criteria)
}

// BuildUserConfig constructs a user config map from a channel Identity.
func (*DingTalkAdapter) BuildUserConfig(identity channel.Identity) map[string]any {
	return buildUserConfig(identity)
}

// DiscoverSelf retrieves the bot's own identity from DingTalk.
// DiscoverSelf uses AppKey as the OpenAPI robotCode parameter (钉钉统一应用下二者一致)。
func (a *DingTalkAdapter) DiscoverSelf(ctx context.Context, credentials map[string]any) (map[string]any, string, error) {
	cfg, err := parseConfig(credentials)
	if err != nil {
		return nil, "", err
	}
	cli := newAPIClient(cfg.AppKey, cfg.AppSecret)
	info, err := cli.getBotInfo(ctx, cfg.AppKey)
	if err != nil {
		a.logger.Warn("dingtalk: getBotInfo failed, using appKey as identity",
			slog.String("app_key", cfg.AppKey),
			slog.Any("error", err),
		)
		return map[string]any{"app_key": cfg.AppKey}, cfg.AppKey, nil
	}
	externalID := strings.TrimSpace(info.Result.RobotCode)
	if externalID == "" {
		externalID = cfg.AppKey
	}
	return map[string]any{
		"app_key": cfg.AppKey,
		"name":    strings.TrimSpace(info.Result.Name),
	}, externalID, nil
}

// Connect establishes a DingTalk Stream WebSocket connection and begins receiving messages.
func (a *DingTalkAdapter) Connect(ctx context.Context, cfg channel.ChannelConfig, handler channel.InboundHandler) (channel.Connection, error) {
	parsed, err := parseConfig(cfg.Credentials)
	if err != nil {
		return nil, err
	}

	apiCli := newAPIClient(parsed.AppKey, parsed.AppSecret)

	streamCli := dtsdk.NewStreamClient(
		dtsdk.WithAppCredential(dtsdk.NewAppCredentialConfig(parsed.AppKey, parsed.AppSecret)),
		dtsdk.WithAutoReconnect(true),
	)

	streamCli.RegisterChatBotCallbackRouter(a.newChatBotHandler(cfg, handler))

	key := cfg.ID
	a.mu.Lock()
	a.clients[key] = streamCli
	a.apiClients[key] = apiCli
	a.mu.Unlock()

	if err := streamCli.Start(ctx); err != nil {
		a.mu.Lock()
		delete(a.clients, key)
		delete(a.apiClients, key)
		a.mu.Unlock()
		return nil, err
	}

	stop := func(context.Context) error {
		// Disable reconnect before closing to prevent the reconnect loop from restarting.
		streamCli.AutoReconnect = false
		streamCli.Close()
		a.mu.Lock()
		if current, ok := a.clients[key]; ok && current == streamCli {
			delete(a.clients, key)
		}
		delete(a.apiClients, key)
		a.mu.Unlock()
		return nil
	}
	return channel.NewConnection(cfg, stop), nil
}

// Send delivers an outbound message to a DingTalk user or group.
// It first tries the session webhook (if cached and valid), then falls back to the OpenAPI.
func (a *DingTalkAdapter) Send(ctx context.Context, cfg channel.ChannelConfig, msg channel.PreparedOutboundMessage) error {
	target := strings.TrimSpace(msg.Target)
	if target == "" {
		return errors.New("dingtalk: target is required")
	}
	logicalMsg := msg.Message.LogicalMessage()
	if logicalMsg.IsEmpty() && len(msg.Message.Attachments) == 0 {
		return errors.New("dingtalk: message is empty")
	}

	// Session webhook fast path: immediate reply without access_token round-trip.
	// Webhooks only support text/markdown; attachments fall through to the OpenAPI.
	if len(msg.Message.Attachments) == 0 {
		if whCtx, ok := a.lookupWebhook(cfg.ID, logicalMsg.Reply); ok && whCtx.isValid() {
			body, bodyErr := buildWebhookBody(logicalMsg)
			if bodyErr == nil {
				apiCli := a.getOrNewAPIClient(cfg)
				if webhookErr := apiCli.sendViaWebhook(ctx, whCtx.SessionWebhook, body); webhookErr == nil {
					return nil
				}
				// Webhook failed (possibly expired mid-flight); fall through to OpenAPI.
			}
		}
	}

	return a.sendViaAPI(ctx, cfg, target, logicalMsg, msg.Message.Attachments)
}

// sendViaAPI sends a message through the DingTalk OpenAPI.
func (a *DingTalkAdapter) sendViaAPI(
	ctx context.Context,
	cfg channel.ChannelConfig,
	target string,
	logicalMsg channel.Message,
	prepared []channel.PreparedAttachment,
) error {
	parsed, err := parseConfig(cfg.Credentials)
	if err != nil {
		return err
	}
	apiCli := a.getOrNewAPIClient(cfg)

	// For Upload-kind image attachments, upload to DingTalk media first to get a mediaId.
	// DingTalk's sampleImageMsg requires a publicly accessible photoURL, which is unavailable
	// when using local filesystem storage. Uploading converts the local asset to a platform mediaId.
	resolved, err := resolveUploadAttachments(ctx, apiCli, prepared)
	if err != nil {
		return err
	}

	msgKey, msgParam, err := buildAPIPayload(logicalMsg, resolved)
	if err != nil {
		return err
	}

	kind, id, ok := parseTarget(target)
	if !ok {
		return errors.New("dingtalk: invalid target")
	}
	a.logger.Debug("dingtalk: sendViaAPI",
		slog.String("target", target),
		slog.String("kind", kind),
		slog.String("id", id),
		slog.String("robot_code", parsed.AppKey),
	)
	switch kind {
	case "user":
		return apiCli.sendToUser(ctx, parsed.AppKey, []string{id}, msgKey, msgParam)
	case "group":
		return apiCli.sendToGroup(ctx, parsed.AppKey, id, msgKey, msgParam)
	default:
		return errors.New("dingtalk: unknown target kind: " + kind)
	}
}

// resolveUploadAttachments uploads any Upload-kind attachments to DingTalk and
// returns a new slice where those entries are replaced by NativeRef attachments
// carrying the resulting mediaId. PublicURL and NativeRef attachments pass through unchanged.
func resolveUploadAttachments(
	ctx context.Context,
	apiCli *apiClient,
	prepared []channel.PreparedAttachment,
) ([]channel.PreparedAttachment, error) {
	if len(prepared) == 0 {
		return prepared, nil
	}
	result := make([]channel.PreparedAttachment, 0, len(prepared))
	for _, att := range prepared {
		if att.Kind != channel.PreparedAttachmentUpload || att.Open == nil {
			result = append(result, att)
			continue
		}
		mediaType := dingtalkMediaType(att)
		reader, err := att.Open(ctx)
		if err != nil {
			return nil, fmt.Errorf("dingtalk: open attachment for upload: %w", err)
		}
		mediaID, uploadErr := apiCli.uploadMedia(ctx, mediaType, att.Name, reader)
		_ = reader.Close()
		if uploadErr != nil {
			return nil, fmt.Errorf("dingtalk: upload media: %w", uploadErr)
		}
		uploaded := att
		uploaded.Kind = channel.PreparedAttachmentNativeRef
		uploaded.NativeRef = mediaID
		uploaded.Open = nil
		result = append(result, uploaded)
	}
	return result, nil
}

// dingtalkMediaType maps an attachment to the DingTalk media type string for upload.
func dingtalkMediaType(att channel.PreparedAttachment) string {
	switch att.Logical.Type {
	case channel.AttachmentImage, channel.AttachmentGIF:
		return "image"
	case channel.AttachmentAudio, channel.AttachmentVoice:
		return "voice"
	case channel.AttachmentVideo:
		return "video"
	default:
		return "file"
	}
}

// ResolveAttachment implements channel.AttachmentResolver. It downloads a file
// received by the bot (identified by its downloadCode stored in PlatformKey)
// via the DingTalk OpenAPI and returns a readable payload.
func (a *DingTalkAdapter) ResolveAttachment(ctx context.Context, cfg channel.ChannelConfig, att channel.Attachment) (channel.AttachmentPayload, error) {
	downloadCode := strings.TrimSpace(att.PlatformKey)
	if downloadCode == "" {
		return channel.AttachmentPayload{}, errors.New("dingtalk: attachment has no downloadCode (platform_key)")
	}
	parsed, err := parseConfig(cfg.Credentials)
	if err != nil {
		return channel.AttachmentPayload{}, fmt.Errorf("dingtalk: resolve attachment config: %w", err)
	}
	apiCli := a.getOrNewAPIClient(cfg)
	reader, mimeType, err := apiCli.downloadMessageFile(ctx, parsed.AppKey, downloadCode)
	if err != nil {
		return channel.AttachmentPayload{}, err
	}
	if strings.TrimSpace(att.Mime) != "" {
		mimeType = strings.TrimSpace(att.Mime)
	}
	return channel.AttachmentPayload{
		Reader: reader,
		Mime:   mimeType,
		Name:   strings.TrimSpace(att.Name),
	}, nil
}

// OpenStream creates a new accumulating outbound stream for the given target.
func (a *DingTalkAdapter) OpenStream(ctx context.Context, cfg channel.ChannelConfig, target string, opts channel.StreamOptions) (channel.PreparedOutboundStream, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, errors.New("dingtalk: target is required")
	}
	reply := opts.Reply
	if reply == nil && strings.TrimSpace(opts.SourceMessageID) != "" {
		reply = &channel.ReplyRef{
			Target:    target,
			MessageID: strings.TrimSpace(opts.SourceMessageID),
		}
	}
	return &dingtalkOutboundStream{
		adapter: a,
		cfg:     cfg,
		target:  target,
		reply:   reply,
	}, nil
}

// newChatBotHandler returns the DingTalk SDK chatbot callback for the given channel config.
func (a *DingTalkAdapter) newChatBotHandler(cfg channel.ChannelConfig, handler channel.InboundHandler) chatbot.IChatBotMessageHandler {
	return func(ctx context.Context, data *chatbot.BotCallbackDataModel) ([]byte, error) {
		if data == nil {
			return nil, nil
		}

		// Cache the session webhook so that Send can use the fast-reply path.
		if strings.TrimSpace(data.MsgId) != "" && strings.TrimSpace(data.SessionWebhook) != "" {
			a.rememberWebhook(cfg.ID, data.MsgId, sessionWebhookContext{
				SessionWebhook: data.SessionWebhook,
				ExpiredTime:    data.SessionWebhookExpiredTime,
				ConversationID: data.ConversationId,
				SenderID:       data.SenderId,
			})
		}

		a.logger.Debug("dingtalk: inbound callback",
			slog.String("sender_id", data.SenderId),
			slog.String("sender_staff_id", data.SenderStaffId),
			slog.String("sender_corp_id", data.SenderCorpId),
			slog.String("conversation_type", data.ConversationType),
			slog.String("conversation_id", data.ConversationId),
		)

		if handler == nil {
			return nil, nil
		}
		msg, ok := buildInboundMessage(data)
		if !ok {
			return nil, nil
		}
		msg.BotID = cfg.BotID
		if err := handler(ctx, cfg, msg); err != nil {
			a.logger.Error("dingtalk: inbound handler error",
				slog.String("config_id", cfg.ID),
				slog.Any("error", err),
			)
		}
		return nil, nil
	}
}

func (a *DingTalkAdapter) getOrNewAPIClient(cfg channel.ChannelConfig) *apiClient {
	a.mu.RLock()
	cli := a.apiClients[cfg.ID]
	a.mu.RUnlock()
	if cli != nil {
		return cli
	}
	parsed, err := parseConfig(cfg.Credentials)
	if err != nil {
		return newAPIClient("", "")
	}
	return newAPIClient(parsed.AppKey, parsed.AppSecret)
}

func (a *DingTalkAdapter) lookupWebhook(configID string, reply *channel.ReplyRef) (sessionWebhookContext, bool) {
	if reply == nil {
		return sessionWebhookContext{}, false
	}
	msgID := strings.TrimSpace(reply.MessageID)
	if msgID == "" {
		return sessionWebhookContext{}, false
	}
	return a.webhookCache.get(configID, msgID)
}

func (a *DingTalkAdapter) rememberWebhook(configID, msgID string, whCtx sessionWebhookContext) {
	msgID = strings.TrimSpace(msgID)
	if msgID == "" || strings.TrimSpace(whCtx.SessionWebhook) == "" {
		return
	}
	if whCtx.CreatedAt.IsZero() {
		whCtx.CreatedAt = time.Now().UTC()
	}
	a.webhookCache.put(configID, msgID, whCtx)
}
