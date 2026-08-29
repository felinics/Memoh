package weixin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/channel"
)

// Type is the channel type identifier for WeChat.
const Type channel.ChannelType = "weixin"

// WeixinAdapter is the Memoh channel adapter for personal WeChat via the Tencent iLink API.
type WeixinAdapter struct {
	logger       *slog.Logger
	client       *Client
	contextCache *contextTokenCache
	assets       assetOpener
}

// NewWeixinAdapter creates a new WeChat adapter.
func NewWeixinAdapter(log *slog.Logger) *WeixinAdapter {
	if log == nil {
		log = slog.Default()
	}
	return &WeixinAdapter{
		logger:       log.With(slog.String("adapter", "weixin")),
		client:       NewClient(log),
		contextCache: newContextTokenCache(24 * time.Hour),
	}
}

// SetAssetOpener configures the media asset reader for outbound attachments.
func (a *WeixinAdapter) SetAssetOpener(opener assetOpener) {
	a.assets = opener
}

func (*WeixinAdapter) Type() channel.ChannelType { return Type }

func (*WeixinAdapter) Descriptor() channel.Descriptor {
	return channel.Descriptor{
		Type:        Type,
		DisplayName: "WeChat",
		Capabilities: channel.ChannelCapabilities{
			Text:           true,
			Attachments:    true,
			Media:          true,
			Reply:          true,
			BlockStreaming: true,
			ChatTypes:      []string{channel.ConversationTypePrivate},
		},
		OutboundPolicy: channel.OutboundPolicy{
			TextChunkLimit: 4000,
		},
		ConfigSchema: channel.ConfigSchema{
			Version: 1,
			Fields: map[string]channel.FieldSchema{
				"token":              {Type: channel.FieldSecret, Required: true, Title: "Token"},
				"pollTimeoutSeconds": {Type: channel.FieldNumber, Title: "Poll Timeout (s)"},
				"enableTyping":       {Type: channel.FieldBool, Title: "Enable Typing Indicator"},
			},
		},
		UserConfigSchema: channel.ConfigSchema{
			Version: 1,
			Fields: map[string]channel.FieldSchema{
				"user_id": {Type: channel.FieldString, Required: true, Title: "WeChat User ID"},
			},
		},
		TargetSpec: channel.TargetSpec{
			Format: "<user_id>",
			Hints: []channel.TargetHint{
				{Label: "User ID", Example: "abc123@im.wechat"},
			},
		},
	}
}

// -- ConfigNormalizer --

func (*WeixinAdapter) NormalizeConfig(raw map[string]any) (map[string]any, error) {
	return normalizeConfig(raw)
}

func (*WeixinAdapter) NormalizeUserConfig(raw map[string]any) (map[string]any, error) {
	return normalizeUserConfig(raw)
}

// -- TargetResolver --

func (*WeixinAdapter) NormalizeTarget(raw string) string { return normalizeTarget(raw) }

func (*WeixinAdapter) ResolveTarget(userConfig map[string]any) (string, error) {
	return resolveTarget(userConfig)
}

// -- BindingMatcher --

func (*WeixinAdapter) MatchBinding(config map[string]any, criteria channel.BindingCriteria) bool {
	return matchBinding(config, criteria)
}

func (*WeixinAdapter) BuildUserConfig(identity channel.Identity) map[string]any {
	return buildUserConfig(identity)
}

// -- Receiver (long-poll) --

func (a *WeixinAdapter) Connect(ctx context.Context, cfg channel.ChannelConfig, handler channel.InboundHandler) (channel.Connection, error) {
	parsed, err := parseConfig(cfg.Credentials)
	if err != nil {
		return nil, err
	}

	connCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	go func() {
		defer close(done)
		a.pollLoop(connCtx, cfg, parsed, handler)
	}()

	stop := func(context.Context) error {
		cancel()
		<-done
		return nil
	}
	return channel.NewConnection(cfg, stop), nil
}

func (a *WeixinAdapter) pollLoop(ctx context.Context, cfg channel.ChannelConfig, parsed adapterConfig, handler channel.InboundHandler) {
	const (
		maxConsecutiveFailures = 3
		backoffDelay           = 30 * time.Second
		retryDelay             = 2 * time.Second
		sessionPauseDuration   = 1 * time.Hour
	)

	var getUpdatesBuf string
	var consecutiveFailures int

	a.logger.Info("weixin poll loop started",
		slog.String("config_id", cfg.ID),
		slog.String("bot_id", cfg.BotID),
		slog.String("base_url", parsed.BaseURL),
	)

	for {
		select {
		case <-ctx.Done():
			a.logger.Info("weixin poll loop stopped", slog.String("config_id", cfg.ID))
			return
		default:
		}

		resp, err := a.client.GetUpdates(ctx, parsed, getUpdatesBuf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			consecutiveFailures++
			a.logger.Error("weixin getupdates error",
				slog.String("config_id", cfg.ID),
				slog.Any("error", err),
				slog.Int("failures", consecutiveFailures),
			)
			if consecutiveFailures >= maxConsecutiveFailures {
				consecutiveFailures = 0
				sleepCtx(ctx, backoffDelay)
			} else {
				sleepCtx(ctx, retryDelay)
			}
			continue
		}

		// Handle API-level errors.
		isAPIError := (resp.Ret != 0) || (resp.ErrCode != 0)
		if isAPIError {
			if resp.ErrCode == sessionExpiredErrCode || resp.Ret == sessionExpiredErrCode {
				a.logger.Error("weixin session expired, pausing",
					slog.String("config_id", cfg.ID),
					slog.Int("errcode", resp.ErrCode),
				)
				sleepCtx(ctx, sessionPauseDuration)
				consecutiveFailures = 0
				continue
			}
			consecutiveFailures++
			a.logger.Error("weixin getupdates api error",
				slog.String("config_id", cfg.ID),
				slog.Int("ret", resp.Ret),
				slog.Int("errcode", resp.ErrCode),
				slog.String("errmsg", resp.ErrMsg),
			)
			if consecutiveFailures >= maxConsecutiveFailures {
				consecutiveFailures = 0
				sleepCtx(ctx, backoffDelay)
			} else {
				sleepCtx(ctx, retryDelay)
			}
			continue
		}

		consecutiveFailures = 0

		if resp.GetUpdatesBuf != "" {
			getUpdatesBuf = resp.GetUpdatesBuf
		}

		for _, msg := range resp.Msgs {
			inbound, ok := buildInboundMessage(msg)
			if !ok {
				continue
			}

			// Cache context_token for outbound replies.
			if strings.TrimSpace(msg.ContextToken) != "" {
				cacheKey := cfg.ID + ":" + strings.TrimSpace(msg.FromUserID)
				a.contextCache.Put(cacheKey, msg.ContextToken)
			}

			inbound.BotID = cfg.BotID

			if err := handler(ctx, cfg, inbound); err != nil {
				a.logger.Error("weixin inbound handler error",
					slog.String("config_id", cfg.ID),
					slog.String("from", msg.FromUserID),
					slog.Any("error", err),
				)
			}
		}
	}
}

// -- StreamSender (block-streaming: buffer deltas, send final as one message) --

func (a *WeixinAdapter) OpenStream(_ context.Context, cfg channel.ChannelConfig, target string, _ channel.StreamOptions) (channel.PreparedOutboundStream, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, errors.New("weixin target is required")
	}
	return &weixinBlockStream{
		adapter: a,
		cfg:     cfg,
		target:  target,
	}, nil
}

// -- Sender --

func (a *WeixinAdapter) Send(ctx context.Context, cfg channel.ChannelConfig, msg channel.PreparedOutboundMessage) error {
	parsed, err := parseConfig(cfg.Credentials)
	if err != nil {
		return err
	}
	target := strings.TrimSpace(msg.Target)
	if target == "" {
		return errors.New("weixin target is required")
	}

	cacheKey := cfg.ID + ":" + target
	contextToken, ok := a.contextCache.Get(cacheKey)
	if !ok {
		return fmt.Errorf("weixin: no context_token cached for target %s (reply-only channel — message can only be sent after receiving an inbound message)", target)
	}

	// Send attachments first if present (media + text in one flow).
	if len(msg.Message.Attachments) > 0 {
		return a.sendWithAttachments(ctx, parsed, target, contextToken, msg.Message)
	}

	text := strings.TrimSpace(msg.Message.Message.PlainText())
	if text == "" {
		return errors.New("weixin: message is empty")
	}
	return sendText(ctx, a.client, parsed, target, text, contextToken)
}

func (a *WeixinAdapter) sendWithAttachments(ctx context.Context, cfg adapterConfig, target, contextToken string, msg channel.PreparedMessage) error {
	text := strings.TrimSpace(msg.Message.PlainText())

	for i, att := range msg.Attachments {
		caption := ""
		if i == 0 {
			caption = text
		}

		r, err := openAttachment(ctx, att)
		if err != nil {
			return fmt.Errorf("weixin: open attachment: %w", err)
		}

		switch att.Logical.Type {
		case channel.AttachmentImage, channel.AttachmentGIF:
			if err := sendImageFromReader(ctx, a.client, cfg, target, contextToken, caption, r, a.logger); err != nil {
				_ = r.Close()
				return err
			}
		case channel.AttachmentVideo:
			data, readErr := io.ReadAll(r)
			_ = r.Close()
			if readErr != nil {
				return fmt.Errorf("weixin: read video: %w", readErr)
			}
			if err := sendMediaBytes(ctx, a.client, cfg, target, contextToken, caption, data, UploadMediaVideo, ItemTypeVideo, a.logger); err != nil {
				return err
			}
		default:
			name := strings.TrimSpace(att.Name)
			if name == "" {
				name = "file"
			}
			if err := sendFileFromReader(ctx, a.client, cfg, target, contextToken, caption, name, r, a.logger); err != nil {
				_ = r.Close()
				return err
			}
		}
		_ = r.Close()
	}

	return nil
}

func openAttachment(ctx context.Context, att channel.PreparedAttachment) (io.ReadCloser, error) {
	if att.Kind != channel.PreparedAttachmentUpload {
		return nil, fmt.Errorf("weixin attachment requires upload source, got %s", att.Kind)
	}
	if att.Open == nil {
		return nil, errors.New("weixin attachment upload is not openable")
	}
	return att.Open(ctx)
}

// -- AttachmentResolver (for inbound media download/decrypt) --

func (*WeixinAdapter) ResolveAttachment(_ context.Context, cfg channel.ChannelConfig, attachment channel.Attachment) (channel.AttachmentPayload, error) {
	parsed, err := parseConfig(cfg.Credentials)
	if err != nil {
		return channel.AttachmentPayload{}, err
	}

	encryptedQP := ""
	aesKey := ""
	if attachment.Metadata != nil {
		if v, ok := attachment.Metadata["encrypt_query_param"].(string); ok {
			encryptedQP = strings.TrimSpace(v)
		}
		if v, ok := attachment.Metadata["aes_key"].(string); ok {
			aesKey = strings.TrimSpace(v)
		}
	}
	if encryptedQP == "" {
		encryptedQP = strings.TrimSpace(attachment.PlatformKey)
	}
	if encryptedQP == "" {
		return channel.AttachmentPayload{}, errors.New("weixin: no encrypt_query_param for attachment")
	}

	var data []byte
	if aesKey != "" {
		data, err = downloadAndDecrypt(parsed.CDNBaseURL, encryptedQP, aesKey)
	} else {
		data, err = downloadPlain(parsed.CDNBaseURL, encryptedQP)
	}
	if err != nil {
		return channel.AttachmentPayload{}, fmt.Errorf("weixin: download attachment: %w", err)
	}

	mime := resolveMIME(attachment)
	return channel.AttachmentPayload{
		Reader: io.NopCloser(bytes.NewReader(data)),
		Mime:   mime,
		Name:   strings.TrimSpace(attachment.Name),
		Size:   int64(len(data)),
	}, nil
}

func resolveMIME(att channel.Attachment) string {
	if strings.TrimSpace(att.Mime) != "" {
		return att.Mime
	}
	switch att.Type {
	case channel.AttachmentImage:
		return "image/jpeg"
	case channel.AttachmentVoice, channel.AttachmentAudio:
		return "audio/silk"
	case channel.AttachmentVideo:
		return "video/mp4"
	default:
		return "application/octet-stream"
	}
}

// -- ProcessingStatusNotifier (typing indicator) --

func (a *WeixinAdapter) ProcessingStarted(ctx context.Context, cfg channel.ChannelConfig, _ channel.InboundMessage, info channel.ProcessingStatusInfo) (channel.ProcessingStatusHandle, error) {
	parsed, err := parseConfig(cfg.Credentials)
	if err != nil || !parsed.EnableTyping {
		return channel.ProcessingStatusHandle{}, nil
	}
	target := strings.TrimSpace(info.ReplyTarget)
	if target == "" {
		return channel.ProcessingStatusHandle{}, nil
	}

	cacheKey := cfg.ID + ":" + target
	contextToken, _ := a.contextCache.Get(cacheKey)

	configResp, err := a.client.GetConfig(ctx, parsed, target, contextToken)
	if err != nil || strings.TrimSpace(configResp.TypingTicket) == "" {
		return channel.ProcessingStatusHandle{}, nil
	}

	_ = a.client.SendTyping(ctx, parsed, target, configResp.TypingTicket, TypingStatusTyping)
	return channel.ProcessingStatusHandle{Token: configResp.TypingTicket}, nil
}

func (a *WeixinAdapter) ProcessingCompleted(ctx context.Context, cfg channel.ChannelConfig, _ channel.InboundMessage, info channel.ProcessingStatusInfo, handle channel.ProcessingStatusHandle) error {
	if strings.TrimSpace(handle.Token) == "" {
		return nil
	}
	parsed, err := parseConfig(cfg.Credentials)
	if err != nil || !parsed.EnableTyping {
		return nil
	}
	target := strings.TrimSpace(info.ReplyTarget)
	if target == "" {
		return nil
	}
	return a.client.SendTyping(ctx, parsed, target, handle.Token, TypingStatusCancel)
}

func (a *WeixinAdapter) ProcessingFailed(ctx context.Context, cfg channel.ChannelConfig, msg channel.InboundMessage, info channel.ProcessingStatusInfo, handle channel.ProcessingStatusHandle, _ error) error {
	return a.ProcessingCompleted(ctx, cfg, msg, info, handle)
}

// weixinBlockStream buffers streaming deltas and sends the final message as one Send call.
type weixinBlockStream struct {
	adapter     *WeixinAdapter
	cfg         channel.ChannelConfig
	target      string
	textBuilder strings.Builder
	attachments []channel.PreparedAttachment
	final       *channel.PreparedMessage
	closed      bool
}

func (s *weixinBlockStream) Push(_ context.Context, event channel.PreparedStreamEvent) error {
	if s.closed {
		return nil
	}
	switch event.Type {
	case channel.StreamEventDelta:
		if strings.TrimSpace(event.Delta) != "" && event.Phase != channel.StreamPhaseReasoning {
			s.textBuilder.WriteString(event.Delta)
		}
	case channel.StreamEventAttachment:
		s.attachments = append(s.attachments, event.Attachments...)
	case channel.StreamEventFinal:
		if event.Final != nil {
			msg := event.Final.Message
			s.final = &msg
		}
	}
	return nil
}

func (s *weixinBlockStream) Close(ctx context.Context) error {
	if s.closed {
		return nil
	}
	s.closed = true

	msg := channel.PreparedMessage{Message: channel.Message{Format: channel.MessageFormatPlain}}
	if s.final != nil {
		msg = *s.final
	}
	if strings.TrimSpace(msg.Message.Text) == "" {
		msg.Message.Text = strings.TrimSpace(s.textBuilder.String())
	}
	if len(msg.Attachments) == 0 && len(s.attachments) > 0 {
		msg.Attachments = append(msg.Attachments, s.attachments...)
		msg.Message.Attachments = weixinLogicalAttachments(msg.Attachments)
	}
	if msg.Message.IsEmpty() && len(msg.Attachments) == 0 {
		return nil
	}
	return s.adapter.Send(ctx, s.cfg, channel.PreparedOutboundMessage{
		Target:  s.target,
		Message: msg,
	})
}

func weixinLogicalAttachments(attachments []channel.PreparedAttachment) []channel.Attachment {
	if len(attachments) == 0 {
		return nil
	}
	logical := make([]channel.Attachment, 0, len(attachments))
	for _, att := range attachments {
		logical = append(logical, att.Logical)
	}
	return logical
}

// sleepCtx sleeps for the given duration or until the context is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
