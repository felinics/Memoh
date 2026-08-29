package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/adapters/feishu/wsclient"
	"github.com/felinics/memoh/internal/channel/common"
	"github.com/felinics/memoh/internal/media"
)

type assetOpener interface {
	Open(ctx context.Context, botID, contentHash string) (io.ReadCloser, media.Asset, error)
}

// FeishuAdapter implements the channel.Adapter, channel.Sender, and channel.Receiver interfaces for Feishu.
type FeishuAdapter struct {
	logger *slog.Logger
	assets assetOpener
}

const processingBusyReactionType = "Typing"

type messageReactionAPI interface {
	Create(ctx context.Context, req *larkim.CreateMessageReactionReq, options ...larkcore.RequestOptionFunc) (*larkim.CreateMessageReactionResp, error)
	Delete(ctx context.Context, req *larkim.DeleteMessageReactionReq, options ...larkcore.RequestOptionFunc) (*larkim.DeleteMessageReactionResp, error)
}

type processingReactionGateway interface {
	Add(ctx context.Context, messageID, reactionType string) (string, error)
	Remove(ctx context.Context, messageID, reactionID string) error
}

type larkProcessingReactionGateway struct {
	api messageReactionAPI
}

func (g *larkProcessingReactionGateway) Add(ctx context.Context, messageID, reactionType string) (string, error) {
	if g == nil || g.api == nil {
		return "", errors.New("feishu reaction api not configured")
	}
	req := larkim.NewCreateMessageReactionReqBuilder().
		MessageId(messageID).
		Body(larkim.NewCreateMessageReactionReqBodyBuilder().
			ReactionType(larkim.NewEmojiBuilder().EmojiType(reactionType).Build()).
			Build()).
		Build()
	resp, err := g.api.Create(ctx, req)
	if err != nil {
		return "", err
	}
	if resp == nil || !resp.Success() {
		code := 0
		msg := ""
		if resp != nil {
			code = resp.Code
			msg = resp.Msg
		}
		return "", fmt.Errorf("feishu add reaction failed: %s (code: %d)", msg, code)
	}
	if resp.Data == nil || resp.Data.ReactionId == nil || strings.TrimSpace(*resp.Data.ReactionId) == "" {
		return "", errors.New("feishu add reaction failed: empty reaction id")
	}
	return strings.TrimSpace(*resp.Data.ReactionId), nil
}

func (g *larkProcessingReactionGateway) Remove(ctx context.Context, messageID, reactionID string) error {
	if g == nil || g.api == nil {
		return errors.New("feishu reaction api not configured")
	}
	req := larkim.NewDeleteMessageReactionReqBuilder().
		MessageId(messageID).
		ReactionId(reactionID).
		Build()
	resp, err := g.api.Delete(ctx, req)
	if err != nil {
		return err
	}
	if resp == nil || !resp.Success() {
		code := 0
		msg := ""
		if resp != nil {
			code = resp.Code
			msg = resp.Msg
		}
		return fmt.Errorf("feishu remove reaction failed: %s (code: %d)", msg, code)
	}
	return nil
}

// NewFeishuAdapter creates a FeishuAdapter with the given logger.
func NewFeishuAdapter(log *slog.Logger) *FeishuAdapter {
	if log == nil {
		log = slog.Default()
	}
	return &FeishuAdapter{
		logger: log.With(slog.String("adapter", "feishu")),
	}
}

// SetAssetOpener injects media asset reader for content_hash attachment delivery.
func (a *FeishuAdapter) SetAssetOpener(opener assetOpener) {
	a.assets = opener
}

// Type returns the Feishu channel type.
func (*FeishuAdapter) Type() channel.ChannelType {
	return Type
}

// Descriptor returns the Feishu channel metadata.
func (*FeishuAdapter) Descriptor() channel.Descriptor {
	return channel.Descriptor{
		Type:        Type,
		DisplayName: "Feishu",
		Capabilities: channel.ChannelCapabilities{
			Text:           true,
			Markdown:       true,
			RichText:       true,
			Attachments:    true,
			Media:          true,
			Reactions:      true,
			Reply:          true,
			Streaming:      true,
			BlockStreaming: true,
		},
		OutboundPolicy: channel.OutboundPolicy{
			RichTextChunkLimit: feishuStreamMaxRunes,
		},
		ConfigSchema: channel.ConfigSchema{
			Version: 2,
			Fields: map[string]channel.FieldSchema{
				"appId":     {Type: channel.FieldString, Required: true, Title: "App ID"},
				"appSecret": {Type: channel.FieldSecret, Required: true, Title: "App Secret"},
				"encryptKey": {
					Type:  channel.FieldSecret,
					Title: "Encrypt Key",
				},
				"verificationToken": {
					Type:  channel.FieldSecret,
					Title: "Verification Token",
				},
				"region": {
					Type:        channel.FieldEnum,
					Title:       "Region",
					Description: "API endpoint region: feishu.cn or larksuite.com",
					Enum:        []string{regionFeishu, regionLark},
					Example:     regionFeishu,
				},
				"inboundMode": {
					Type:        channel.FieldEnum,
					Title:       "Inbound Mode",
					Description: "Choose websocket long-connection or webhook callback for inbound messages",
					Enum:        []string{inboundModeWebsocket, inboundModeWebhook},
					Example:     inboundModeWebsocket,
				},
			},
		},
		UserConfigSchema: channel.ConfigSchema{
			Version: 1,
			Fields: map[string]channel.FieldSchema{
				"open_id": {Type: channel.FieldString},
				"user_id": {Type: channel.FieldString},
			},
		},
		TargetSpec: channel.TargetSpec{
			Format: "open_id:xxx | user_id:xxx | chat_id:xxx",
			Hints: []channel.TargetHint{
				{Label: "Open ID", Example: "open_id:ou_xxx"},
				{Label: "User ID", Example: "user_id:ou_xxx"},
				{Label: "Chat ID", Example: "chat_id:oc_xxx"},
			},
		},
	}
}

// ProcessingStarted adds a transient reaction to indicate the inbound message is being processed.
func (a *FeishuAdapter) ProcessingStarted(ctx context.Context, cfg channel.ChannelConfig, _ channel.InboundMessage, info channel.ProcessingStatusInfo) (channel.ProcessingStatusHandle, error) {
	messageID := strings.TrimSpace(info.SourceMessageID)
	if messageID == "" {
		return channel.ProcessingStatusHandle{}, nil
	}
	gateway, err := a.processingReactionGateway(cfg)
	if err != nil {
		return channel.ProcessingStatusHandle{}, err
	}
	token, err := addProcessingReaction(ctx, gateway, messageID, processingBusyReactionType)
	if err != nil {
		return channel.ProcessingStatusHandle{}, err
	}
	return channel.ProcessingStatusHandle{Token: token}, nil
}

// ProcessingCompleted removes the transient processing reaction before output is sent.
func (a *FeishuAdapter) ProcessingCompleted(ctx context.Context, cfg channel.ChannelConfig, _ channel.InboundMessage, info channel.ProcessingStatusInfo, handle channel.ProcessingStatusHandle) error {
	messageID := strings.TrimSpace(info.SourceMessageID)
	reactionID := strings.TrimSpace(handle.Token)
	if messageID == "" || reactionID == "" {
		return nil
	}
	gateway, err := a.processingReactionGateway(cfg)
	if err != nil {
		return err
	}
	return removeProcessingReaction(ctx, gateway, messageID, reactionID)
}

// ProcessingFailed removes the transient processing reaction when chat processing fails.
func (a *FeishuAdapter) ProcessingFailed(ctx context.Context, cfg channel.ChannelConfig, msg channel.InboundMessage, info channel.ProcessingStatusInfo, handle channel.ProcessingStatusHandle, _ error) error {
	return a.ProcessingCompleted(ctx, cfg, msg, info, handle)
}

func (*FeishuAdapter) processingReactionGateway(cfg channel.ChannelConfig) (processingReactionGateway, error) {
	feishuCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		return nil, err
	}
	client := feishuCfg.newClient()
	gateway := &larkProcessingReactionGateway{api: client.Im.MessageReaction}
	return gateway, nil
}

// React adds an emoji reaction to a message (implements channel.Reactor).
// The target parameter is unused for Feishu; reactions are keyed by message_id.
func (a *FeishuAdapter) React(ctx context.Context, cfg channel.ChannelConfig, _ string, messageID string, emoji string) error {
	gateway, err := a.processingReactionGateway(cfg)
	if err != nil {
		return err
	}
	_, err = gateway.Add(ctx, messageID, emoji)
	return err
}

// Unreact removes the bot's reaction from a message (implements channel.Reactor).
// For Feishu, this requires the reaction_id which we don't have here, so we pass
// the emoji as reaction_id. If the caller stored the reaction_id from React, they
// should pass it as emoji. This is a best-effort operation.
func (a *FeishuAdapter) Unreact(ctx context.Context, cfg channel.ChannelConfig, _ string, messageID string, reactionID string) error {
	if strings.TrimSpace(reactionID) == "" {
		return nil
	}
	gateway, err := a.processingReactionGateway(cfg)
	if err != nil {
		return err
	}
	return gateway.Remove(ctx, messageID, reactionID)
}

func addProcessingReaction(ctx context.Context, gateway processingReactionGateway, messageID, reactionType string) (string, error) {
	if gateway == nil {
		return "", errors.New("processing reaction gateway is nil")
	}
	msgID := strings.TrimSpace(messageID)
	if msgID == "" {
		return "", nil
	}
	rxType := strings.TrimSpace(reactionType)
	if rxType == "" {
		return "", errors.New("processing reaction type is empty")
	}
	return gateway.Add(ctx, msgID, rxType)
}

func removeProcessingReaction(ctx context.Context, gateway processingReactionGateway, messageID, reactionID string) error {
	if gateway == nil {
		return errors.New("processing reaction gateway is nil")
	}
	msgID := strings.TrimSpace(messageID)
	rxID := strings.TrimSpace(reactionID)
	if msgID == "" || rxID == "" {
		return nil
	}
	return gateway.Remove(ctx, msgID, rxID)
}

// DiscoverSelf retrieves the bot's own identity from the Feishu platform.
func (*FeishuAdapter) DiscoverSelf(ctx context.Context, credentials map[string]any) (map[string]any, string, error) {
	cfg, err := parseConfig(credentials)
	if err != nil {
		return nil, "", err
	}
	client := cfg.newClient()
	resp, err := client.Get(ctx, "/open-apis/bot/v3/info", nil, larkcore.AccessTokenTypeTenant)
	if err != nil {
		return nil, "", fmt.Errorf("feishu discover self: %w", err)
	}
	var body struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Bot  struct {
			OpenID    string `json:"open_id"`
			AppName   string `json:"app_name"`
			AvatarURL string `json:"avatar_url"`
		} `json:"bot"`
	}
	if err := json.Unmarshal(resp.RawBody, &body); err != nil {
		return nil, "", fmt.Errorf("feishu discover self: parse response: %w", err)
	}
	if body.Code != 0 {
		return nil, "", fmt.Errorf("feishu discover self: %s (code: %d)", body.Msg, body.Code)
	}
	openID := strings.TrimSpace(body.Bot.OpenID)
	if openID == "" {
		return nil, "", errors.New("feishu discover self: empty open_id")
	}
	identity := map[string]any{
		"open_id": openID,
	}
	if name := strings.TrimSpace(body.Bot.AppName); name != "" {
		identity["name"] = name
	}
	if avatar := strings.TrimSpace(body.Bot.AvatarURL); avatar != "" {
		identity["avatar_url"] = avatar
	}
	return identity, openID, nil
}

// NormalizeConfig validates and normalizes a Feishu channel configuration map.
func (*FeishuAdapter) NormalizeConfig(raw map[string]any) (map[string]any, error) {
	return normalizeConfig(raw)
}

// NormalizeUserConfig validates and normalizes a Feishu user-binding configuration map.
func (*FeishuAdapter) NormalizeUserConfig(raw map[string]any) (map[string]any, error) {
	return normalizeUserConfig(raw)
}

// NormalizeTarget normalizes a Feishu delivery target string.
func (*FeishuAdapter) NormalizeTarget(raw string) string {
	return normalizeTarget(raw)
}

// ResolveTarget derives a delivery target from a Feishu user-binding configuration.
func (*FeishuAdapter) ResolveTarget(userConfig map[string]any) (string, error) {
	return resolveTarget(userConfig)
}

// MatchBinding reports whether a Feishu user binding matches the given criteria.
func (*FeishuAdapter) MatchBinding(config map[string]any, criteria channel.BindingCriteria) bool {
	return matchBinding(config, criteria)
}

// BuildUserConfig constructs a Feishu user-binding config from an Identity.
func (*FeishuAdapter) BuildUserConfig(identity channel.Identity) map[string]any {
	return buildUserConfig(identity)
}

// Connect establishes a WebSocket connection to Feishu and forwards inbound messages to the handler.
func (a *FeishuAdapter) Connect(ctx context.Context, cfg channel.ChannelConfig, handler channel.InboundHandler) (channel.Connection, error) {
	if a.logger != nil {
		a.logger.Info("start", slog.String("config_id", cfg.ID))
	}
	feishuCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		if a.logger != nil {
			a.logger.Error("decode config failed", slog.String("config_id", cfg.ID), slog.Any("error", err))
		}
		return nil, err
	}
	if feishuCfg.InboundMode == inboundModeWebhook {
		if a.logger != nil {
			a.logger.Info("webhook mode enabled; websocket connect skipped", slog.String("config_id", cfg.ID))
		}
		return channel.NewConnection(cfg, func(context.Context) error { return nil }), nil
	}
	botOpenID := a.resolveBotOpenID(ctx, cfg)
	if a.logger != nil {
		a.logger.Info("bot identity", slog.String("config_id", cfg.ID), slog.String("bot_open_id", botOpenID))
	}
	connCtx, cancel := context.WithCancel(ctx)
	feishuCfg.registerIMErrorSecrets()
	eventDispatcher := a.buildEventDispatcher(connCtx, cfg, feishuCfg, botOpenID, handler)

	// done closes when the reconnect goroutine exits, which only
	// happens after wsclient.Run tears down its websocket. Stop
	// waits on it so a follow-up Connect can't race a stale conn.
	done := make(chan struct{})

	stop := func(stopCtx context.Context) error {
		cancel()
		select {
		case <-done:
			return nil
		case <-stopCtx.Done():
			// Return the error so Manager.ensureConnection won't
			// start a replacement while the old goroutine may
			// still hold a websocket.
			if a.logger != nil {
				a.logger.Warn("stop timed out waiting for goroutine to exit",
					slog.String("config_id", cfg.ID),
					slog.Any("error", stopCtx.Err()),
				)
			}
			return stopCtx.Err()
		}
	}
	conn := channel.NewConnection(cfg, stop)
	go func() {
		defer close(done)
		const reconnectDelay = 3 * time.Second
		for {
			if connCtx.Err() != nil {
				return
			}
			client := wsclient.New(wsclient.Config{
				AppID:     feishuCfg.AppID,
				AppSecret: feishuCfg.AppSecret,
				Domain:    feishuCfg.openBaseURL(),
				Logger:    a.logger,
			})
			err := client.Run(connCtx, eventDispatcher)
			if connCtx.Err() != nil {
				return
			}
			if err != nil {
				// Client-side errors (auth failed, forbidden) are
				// terminal. Retrying just hammers the API; flip
				// running to false so Manager.refresh sees the
				// channel as down instead of silently healthy.
				var ce *larkws.ClientError
				if errors.As(err, &ce) {
					if a.logger != nil {
						a.logger.Error("websocket client error; not reconnecting",
							slog.String("config_id", cfg.ID),
							slog.Int("code", ce.Code),
							slog.Any("error", err),
						)
					}
					conn.MarkStopped()
					return
				}
				if a.logger != nil {
					a.logger.Error("websocket client exited", slog.String("config_id", cfg.ID), slog.Any("error", err))
				}
			} else if a.logger != nil {
				a.logger.Warn("websocket client exited without error; reconnecting", slog.String("config_id", cfg.ID))
			}
			timer := time.NewTimer(reconnectDelay)
			select {
			case <-connCtx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	return conn, nil
}

// buildEventDispatcher returns the lark SDK dispatcher that decodes
// inbound websocket frames. We build one per Connect so handlers can
// capture connCtx and bail when the connection is torn down.
func (a *FeishuAdapter) buildEventDispatcher(
	connCtx context.Context,
	cfg channel.ChannelConfig,
	feishuCfg Config,
	botOpenID string,
	handler channel.InboundHandler,
) *dispatcher.EventDispatcher {
	eventDispatcher := dispatcher.NewEventDispatcher(
		feishuCfg.VerificationToken,
		feishuCfg.EncryptKey,
	)
	eventDispatcher.OnP2MessageReceiveV1(func(_ context.Context, event *larkim.P2MessageReceiveV1) error {
		if connCtx.Err() != nil {
			return nil
		}
		msg := extractFeishuInbound(event, botOpenID, a.logger)
		text := msg.Message.PlainText()
		rawMessageID := ""
		rawMessageType := ""
		rawContent := ""
		if event != nil && event.Event != nil && event.Event.Message != nil {
			if event.Event.Message.MessageId != nil {
				rawMessageID = strings.TrimSpace(*event.Event.Message.MessageId)
			}
			if event.Event.Message.MessageType != nil {
				rawMessageType = strings.TrimSpace(*event.Event.Message.MessageType)
			}
			if event.Event.Message.Content != nil {
				rawContent = common.SummarizeText(*event.Event.Message.Content)
			}
		}
		if a.logger != nil {
			a.logger.Debug("feishu inbound extracted",
				slog.String("config_id", cfg.ID),
				slog.String("message_id", rawMessageID),
				slog.String("message_type", rawMessageType),
				slog.String("text", common.SummarizeText(text)),
				slog.Int("attachments", len(msg.Message.Attachments)),
				slog.String("raw_content_prefix", rawContent),
			)
		}
		if text == "" && len(msg.Message.Attachments) == 0 {
			if a.logger != nil {
				a.logger.Info(
					"inbound ignored empty payload",
					slog.String("config_id", cfg.ID),
					slog.String("message_id", rawMessageID),
					slog.String("message_type", rawMessageType),
					slog.String("chat_type", msg.Conversation.Type),
				)
			}
			return nil
		}
		a.enrichSenderProfile(connCtx, cfg, event, &msg)
		a.enrichQuotedMessage(connCtx, cfg, &msg, botOpenID)
		msg.BotID = cfg.BotID
		if a.logger != nil {
			isMentioned := false
			if msg.Metadata != nil {
				if v, ok := msg.Metadata["is_mentioned"].(bool); ok {
					isMentioned = v
				}
			}
			a.logger.Info(
				"inbound received",
				slog.String("config_id", cfg.ID),
				slog.String("message_id", rawMessageID),
				slog.String("message_type", rawMessageType),
				slog.String("route_key", msg.RoutingKey()),
				slog.String("chat_type", msg.Conversation.Type),
				slog.Bool("is_mentioned", isMentioned),
				slog.String("text", common.SummarizeText(text)),
			)
		}
		go func() {
			if err := handler(connCtx, cfg, msg); err != nil && a.logger != nil {
				a.logger.Error("handle inbound failed", slog.String("config_id", cfg.ID), slog.Any("error", err))
			}
		}()
		return nil
	})
	eventDispatcher.OnP2MessageReadV1(func(_ context.Context, _ *larkim.P2MessageReadV1) error {
		return nil
	})
	// Ignore reaction lifecycle events explicitly to avoid SDK "not found handler" noise logs.
	// These events are expected because the adapter uses reactions for processing status.
	eventDispatcher.OnP2MessageReactionCreatedV1(func(_ context.Context, _ *larkim.P2MessageReactionCreatedV1) error {
		return nil
	})
	eventDispatcher.OnP2MessageReactionDeletedV1(func(_ context.Context, _ *larkim.P2MessageReactionDeletedV1) error {
		return nil
	})
	return eventDispatcher
}

// Send delivers an outbound message to Feishu, handling attachments, rich text, and replies.
func (a *FeishuAdapter) Send(ctx context.Context, cfg channel.ChannelConfig, msg channel.PreparedOutboundMessage) error {
	feishuCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		if a.logger != nil {
			a.logger.Error("decode config failed", slog.String("config_id", cfg.ID), slog.Any("error", err))
		}
		return err
	}

	receiveID, receiveType, err := resolveFeishuReceiveID(strings.TrimSpace(msg.Target))
	if err != nil {
		return err
	}

	client := feishuCfg.newClient()

	if len(msg.Message.Attachments) > 0 {
		for _, att := range msg.Message.Attachments {
			if err := a.sendAttachment(ctx, client, receiveID, receiveType, att); err != nil {
				return err
			}
		}
		return nil
	}

	msgType, content, err := buildFeishuOutboundContent(msg.Message.Message)
	if err != nil {
		return err
	}

	reqBuilder := larkim.NewCreateMessageReqBodyBuilder().
		ReceiveId(receiveID).
		MsgType(msgType).
		Content(content).
		Uuid(uuid.NewString())

	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(receiveType).
		Body(reqBuilder.Build()).
		Build()

	if msg.Message.Message.Reply != nil && msg.Message.Message.Reply.MessageID != "" {
		replyReq := larkim.NewReplyMessageReqBuilder().
			MessageId(msg.Message.Message.Reply.MessageID).
			Body(larkim.NewReplyMessageReqBodyBuilder().
				Content(content).
				MsgType(msgType).
				Uuid(uuid.NewString()).
				Build()).
			Build()
		resp, err := client.Im.Message.Reply(ctx, replyReq)
		return a.handleReplyResponse(cfg.ID, resp, err)
	}

	resp, err := client.Im.Message.Create(ctx, req)
	return a.handleResponse(cfg.ID, resp, err)
}

func buildFeishuOutboundContent(msg channel.Message) (string, string, error) {
	if len(msg.Parts) > 0 {
		if body := renderFeishuMessagePartsLarkMD(msg); body != "" {
			if len([]rune(body)) > feishuStreamMaxRunes {
				content, err := buildFeishuTextContent(normalizeFeishuStreamText(channel.RenderPartsAsPlain(msg.Parts)))
				if err != nil {
					return "", "", err
				}
				return larkim.MsgTypeText, content, nil
			}
			content, err := buildFeishuCardContent(body)
			if err != nil {
				return "", "", err
			}
			return larkim.MsgTypeInteractive, content, nil
		}
	}
	body := strings.TrimSpace(msg.PlainText())
	if body == "" {
		return "", "", errors.New("message is required")
	}
	var content string
	var err error
	if msg.Format == channel.MessageFormatPlain {
		content, err = buildFeishuPlainCardContent(body)
	} else {
		content, err = buildFeishuCardContent(body)
	}
	if err != nil {
		return "", "", err
	}
	return larkim.MsgTypeInteractive, content, nil
}

func buildFeishuTextContent(body string) (string, error) {
	content, err := json.Marshal(map[string]string{"text": body})
	if err != nil {
		return "", fmt.Errorf("failed to marshal content: %w", err)
	}
	return string(content), nil
}

// OpenStream opens a Feishu streaming session.
// The adapter strategy uses one interactive card and patches it incrementally.
func (a *FeishuAdapter) OpenStream(ctx context.Context, cfg channel.ChannelConfig, target string, opts channel.StreamOptions) (channel.PreparedOutboundStream, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, errors.New("feishu target is required")
	}
	feishuCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		return nil, err
	}
	receiveID, receiveType, err := resolveFeishuReceiveID(target)
	if err != nil {
		return nil, err
	}
	client := feishuCfg.newClient()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return &feishuOutboundStream{
		adapter:       a,
		cfg:           cfg,
		target:        target,
		reply:         opts.Reply,
		client:        client,
		receiveID:     receiveID,
		receiveType:   receiveType,
		patchInterval: feishuStreamPatchInterval,
	}, nil
}

func (a *FeishuAdapter) handleReplyResponse(configID string, resp *larkim.ReplyMessageResp, err error) error {
	if err != nil {
		if a.logger != nil {
			a.logger.Error("reply failed", slog.String("config_id", configID), slog.Any("error", err))
		}
		return err
	}
	if resp == nil || !resp.Success() {
		code := 0
		msg := ""
		if resp != nil {
			code = resp.Code
			msg = resp.Msg
		}
		if a.logger != nil {
			a.logger.Error("reply failed", slog.String("config_id", configID), slog.Int("code", code), slog.String("error_message", msg))
		}
		return fmt.Errorf("feishu reply failed: %s (code: %d)", msg, code)
	}
	if a.logger != nil {
		a.logger.Info("reply success", slog.String("config_id", configID))
	}
	return nil
}

func (a *FeishuAdapter) handleResponse(configID string, resp *larkim.CreateMessageResp, err error) error {
	if err != nil {
		if a.logger != nil {
			a.logger.Error("send failed", slog.String("config_id", configID), slog.Any("error", err))
		}
		return err
	}
	if resp == nil || !resp.Success() {
		code := 0
		msg := ""
		if resp != nil {
			code = resp.Code
			msg = resp.Msg
		}
		if a.logger != nil {
			a.logger.Error("send failed", slog.String("config_id", configID), slog.Int("code", code), slog.String("error_message", msg))
		}
		return fmt.Errorf("feishu send failed: %s (code: %d)", msg, code)
	}
	if a.logger != nil {
		a.logger.Info("send success", slog.String("config_id", configID))
	}
	return nil
}

func (a *FeishuAdapter) sendAttachment(ctx context.Context, client *lark.Client, receiveID, receiveType string, att channel.PreparedAttachment) error {
	var msgType string
	var contentMap map[string]string
	if att.Kind == channel.PreparedAttachmentNativeRef && strings.TrimSpace(att.NativeRef) != "" {
		if strings.HasPrefix(att.Mime, "image/") || att.Logical.Type == channel.AttachmentImage {
			msgType = larkim.MsgTypeImage
			contentMap = map[string]string{"image_key": strings.TrimSpace(att.NativeRef)}
		} else {
			msgType = larkim.MsgTypeFile
			contentMap = map[string]string{"file_key": strings.TrimSpace(att.NativeRef)}
		}
	} else {
		reader, resolvedMime, resolvedName, err := resolveAttachmentUploadReader(ctx, att)
		if err != nil {
			return err
		}
		defer func() {
			_ = reader.Close()
		}()
		typeProbe := att.Logical
		if strings.TrimSpace(typeProbe.Mime) == "" {
			typeProbe.Mime = strings.TrimSpace(resolvedMime)
		}
		if isFeishuImageAttachment(typeProbe) {
			uploadReq := larkim.NewCreateImageReqBuilder().
				Body(larkim.NewCreateImageReqBodyBuilder().
					ImageType(larkim.ImageTypeMessage).
					Image(reader).
					Build()).
				Build()
			uploadResp, err := client.Im.Image.Create(ctx, uploadReq)
			if err != nil {
				return fmt.Errorf("failed to upload image: %w", err)
			}
			if uploadResp == nil || !uploadResp.Success() {
				code, msg := 0, ""
				if uploadResp != nil {
					code, msg = uploadResp.Code, uploadResp.Msg
				}
				return fmt.Errorf("failed to upload image: %s (code: %d)", msg, code)
			}
			msgType = larkim.MsgTypeImage
			contentMap = map[string]string{"image_key": *uploadResp.Data.ImageKey}
		} else {
			fileType := resolveFeishuFileType(resolvedName, resolvedMime)
			fileName := strings.TrimSpace(resolvedName)
			if fileName == "" {
				fileName = "attachment"
			}
			uploadReq := larkim.NewCreateFileReqBuilder().
				Body(larkim.NewCreateFileReqBodyBuilder().
					FileType(fileType).
					FileName(fileName).
					File(reader).
					Build()).
				Build()
			uploadResp, err := client.Im.File.Create(ctx, uploadReq)
			if err != nil {
				return fmt.Errorf("failed to upload file: %w", err)
			}
			if uploadResp == nil || !uploadResp.Success() {
				code, msg := 0, ""
				if uploadResp != nil {
					code, msg = uploadResp.Code, uploadResp.Msg
				}
				return fmt.Errorf("failed to upload file: %s (code: %d)", msg, code)
			}
			msgType = larkim.MsgTypeFile
			contentMap = map[string]string{"file_key": *uploadResp.Data.FileKey}
		}
	}

	content, err := json.Marshal(contentMap)
	if err != nil {
		return fmt.Errorf("failed to marshal content: %w", err)
	}
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(receiveType).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(receiveID).
			MsgType(msgType).
			Content(string(content)).
			Uuid(uuid.NewString()).
			Build()).
		Build()

	sendResp, err := client.Im.Message.Create(ctx, req)
	return a.handleResponse("", sendResp, err)
}

func resolveAttachmentUploadReader(ctx context.Context, att channel.PreparedAttachment) (io.ReadCloser, string, string, error) {
	if att.Kind != channel.PreparedAttachmentUpload {
		return nil, "", "", fmt.Errorf("feishu attachment requires upload source, got %s", att.Kind)
	}
	if att.Open == nil {
		return nil, "", "", errors.New("feishu attachment upload is not openable")
	}
	reader, err := att.Open(ctx)
	if err != nil {
		return nil, "", "", err
	}
	return reader, strings.TrimSpace(att.Mime), strings.TrimSpace(att.Name), nil
}

// ResolveAttachment resolves a Feishu attachment reference to a byte stream.
// User-sent resources must be fetched via the message-resource API which
// requires both message_id and file_key. The message_id is expected in
// attachment.Metadata["message_id"].
func (*FeishuAdapter) ResolveAttachment(ctx context.Context, cfg channel.ChannelConfig, attachment channel.Attachment) (channel.AttachmentPayload, error) {
	platformKey := strings.TrimSpace(attachment.PlatformKey)
	if platformKey == "" {
		return channel.AttachmentPayload{}, errors.New("feishu attachment platform_key is required")
	}
	messageID := ""
	if attachment.Metadata != nil {
		if v, ok := attachment.Metadata["message_id"].(string); ok {
			messageID = strings.TrimSpace(v)
		}
	}
	if messageID == "" {
		return channel.AttachmentPayload{}, errors.New("feishu attachment metadata.message_id is required")
	}
	feishuCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		return channel.AttachmentPayload{}, err
	}
	client := feishuCfg.newClient()

	resourceType := "file"
	if isFeishuImageAttachment(attachment) {
		resourceType = "image"
	}
	req := larkim.NewGetMessageResourceReqBuilder().
		MessageId(messageID).
		FileKey(platformKey).
		Type(resourceType).
		Build()
	resp, err := client.Im.MessageResource.Get(ctx, req)
	if err != nil {
		return channel.AttachmentPayload{}, fmt.Errorf("download feishu resource: %w", err)
	}
	if !resp.Success() {
		return channel.AttachmentPayload{}, fmt.Errorf("download feishu resource: %s (code: %d)", resp.Msg, resp.Code)
	}
	if resp.File == nil {
		return channel.AttachmentPayload{}, errors.New("download feishu resource: empty payload")
	}
	mime := strings.TrimSpace(attachment.Mime)
	if mime == "" {
		if isFeishuImageAttachment(attachment) {
			mime = "image/png"
		} else {
			mime = "application/octet-stream"
		}
	}
	name := strings.TrimSpace(attachment.Name)
	if name == "" {
		name = strings.TrimSpace(resp.FileName)
	}
	return channel.AttachmentPayload{
		Reader: io.NopCloser(resp.File),
		Mime:   mime,
		Name:   name,
		Size:   attachment.Size,
	}, nil
}

func isFeishuImageAttachment(att channel.Attachment) bool {
	if att.Type == channel.AttachmentImage || att.Type == channel.AttachmentGIF {
		return true
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(att.Mime)), "image/")
}

// resolveFeishuFileType maps MIME type and filename to a Feishu file type constant.
func resolveFeishuFileType(name, mime string) string {
	lower := strings.ToLower(mime)
	lowerName := strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.Contains(lower, "mp4") || strings.Contains(lower, "video"):
		return larkim.FileTypeMp4
	case strings.Contains(lower, "pdf"):
		return larkim.FileTypePdf
	case strings.Contains(lower, "word") || strings.Contains(lower, "msword") || strings.HasSuffix(lowerName, ".doc") || strings.HasSuffix(lowerName, ".docx"):
		return larkim.FileTypeDoc
	case strings.Contains(lower, "excel") || strings.Contains(lower, "spreadsheet") || strings.HasSuffix(lowerName, ".xls") || strings.HasSuffix(lowerName, ".xlsx"):
		return larkim.FileTypeXls
	case strings.Contains(lower, "powerpoint") || strings.Contains(lower, "presentation") || strings.HasSuffix(lowerName, ".ppt") || strings.HasSuffix(lowerName, ".pptx"):
		return larkim.FileTypePpt
	case strings.Contains(lower, "zip") || strings.Contains(lower, "compressed") || strings.Contains(lower, "archive"):
		return larkim.FileTypeStream
	case strings.HasSuffix(lowerName, ".zip") || strings.HasSuffix(lowerName, ".tar") || strings.HasSuffix(lowerName, ".tgz") || strings.HasSuffix(lowerName, ".tar.gz") || strings.HasSuffix(lowerName, ".rar") || strings.HasSuffix(lowerName, ".7z") || strings.HasSuffix(lowerName, ".gz") || strings.HasSuffix(lowerName, ".bz2") || strings.HasSuffix(lowerName, ".xz"):
		return larkim.FileTypeStream
	default:
		return larkim.FileTypeStream
	}
}
