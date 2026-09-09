package wechatoa

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/felinics/memoh/internal/channel"
)

const Type channel.ChannelType = "wechatoa"

type WeChatOAAdapter struct {
	logger *slog.Logger

	mu      sync.RWMutex
	clients map[string]*apiClient
}

func NewWeChatOAAdapter(log *slog.Logger) *WeChatOAAdapter {
	if log == nil {
		log = slog.Default()
	}
	return &WeChatOAAdapter{
		logger:  log.With(slog.String("adapter", "wechatoa")),
		clients: make(map[string]*apiClient),
	}
}

func (*WeChatOAAdapter) Type() channel.ChannelType { return Type }

func (*WeChatOAAdapter) Descriptor() channel.Descriptor {
	return channel.Descriptor{
		Type:        Type,
		DisplayName: "WeChat Official Account",
		Capabilities: channel.ChannelCapabilities{
			Text:           true,
			Attachments:    true,
			Media:          true,
			Reply:          true,
			BlockStreaming: true,
			ChatTypes:      []string{channel.ConversationTypePrivate},
		},
		ConfigSchema: channel.ConfigSchema{
			Version: 1,
			Fields: map[string]channel.FieldSchema{
				"appId":          {Type: channel.FieldString, Required: true, Order: 0, Title: "App ID"},
				"appSecret":      {Type: channel.FieldSecret, Required: true, Order: 10, Title: "App Secret"},
				"token":          {Type: channel.FieldSecret, Required: true, Order: 20, Title: "Token"},
				"encodingAESKey": {Type: channel.FieldSecret, Order: 30, Title: "Encoding AES Key"},
				"httpProxyUrl": {
					Type:        channel.FieldSecret,
					Order:       40,
					Title:       "HTTP Proxy URL",
					Description: "Optional outbound HTTP proxy URL for WeChat API requests, e.g. http://user:pass@host:port. Explicit adapter proxy overrides HTTP_PROXY/HTTPS_PROXY.",
				},
				"encryptionMode": {
					Type:     channel.FieldEnum,
					Order:    50,
					Title:    "Encryption Mode",
					Required: true,
					Example:  encryptionModeSafe,
					Enum: []string{
						encryptionModeSafe,
						encryptionModeCompat,
						encryptionModePlain,
					},
				},
			},
		},
		UserConfigSchema: channel.ConfigSchema{
			Version: 1,
			Fields: map[string]channel.FieldSchema{
				"openid":  {Type: channel.FieldString},
				"unionid": {Type: channel.FieldString},
			},
		},
		TargetSpec: channel.TargetSpec{
			Format: "openid:xxx",
			Hints: []channel.TargetHint{
				{Label: "OpenID", Example: "openid:o6_bmjrPTlm6_2sgVt7hMZOPfL2M"},
			},
		},
		OutboundPolicy: channel.OutboundPolicy{
			TextChunkLimit: 600,
		},
	}
}

func (*WeChatOAAdapter) NormalizeConfig(raw map[string]any) (map[string]any, error) {
	return normalizeConfig(raw)
}

func (*WeChatOAAdapter) NormalizeUserConfig(raw map[string]any) (map[string]any, error) {
	return normalizeUserConfig(raw)
}

func (*WeChatOAAdapter) NormalizeTarget(raw string) string {
	return normalizeTarget(raw)
}

func (*WeChatOAAdapter) ResolveTarget(userConfig map[string]any) (string, error) {
	return resolveTarget(userConfig)
}

func (*WeChatOAAdapter) MatchBinding(config map[string]any, criteria channel.BindingCriteria) bool {
	return matchBinding(config, criteria)
}

func (*WeChatOAAdapter) BuildUserConfig(identity channel.Identity) map[string]any {
	return buildUserConfig(identity)
}

func (*WeChatOAAdapter) DiscoverSelf(ctx context.Context, credentials map[string]any) (map[string]any, string, error) {
	_ = ctx
	cfg, err := parseConfig(credentials)
	if err != nil {
		return nil, "", err
	}
	id := strings.TrimSpace(cfg.AppID)
	return map[string]any{"app_id": id}, id, nil
}

func (*WeChatOAAdapter) Connect(ctx context.Context, cfg channel.ChannelConfig, handler channel.InboundHandler) (channel.Connection, error) {
	_ = ctx
	_ = handler
	if _, err := parseConfig(cfg.Credentials); err != nil {
		return nil, err
	}
	return channel.NewConnection(cfg, func(context.Context) error { return nil }), nil
}

func (a *WeChatOAAdapter) HandleWebhook(ctx context.Context, cfg channel.ChannelConfig, handler channel.InboundHandler, r *http.Request, w http.ResponseWriter) error {
	parsed, err := parseConfig(cfg.Credentials)
	if err != nil {
		return err
	}
	verifier, err := newSecurityVerifier(parsed.Token, parsed.EncodingAESKey, parsed.AppID)
	if err != nil {
		return err
	}
	switch r.Method {
	case http.MethodGet:
		return handleVerifyRequest(verifier, parsed.EncryptionMode, r, w)
	case http.MethodPost:
		return a.handleInbound(ctx, verifier, parsed.EncryptionMode, cfg, handler, r, w)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return nil
	}
}

func (a *WeChatOAAdapter) Send(ctx context.Context, cfg channel.ChannelConfig, msg channel.PreparedOutboundMessage) error {
	openID, ok := parseTarget(msg.Target)
	if !ok {
		return errors.New("wechatoa target is required")
	}
	client, err := a.clientForConfig(cfg.Credentials)
	if err != nil {
		return err
	}
	if a.logger != nil {
		a.logger.Debug("wechatoa send start",
			slog.String("config_id", cfg.ID),
			slog.String("target", openID),
			slog.Bool("has_text", strings.TrimSpace(msg.Message.Message.PlainText()) != ""),
			slog.Int("attachments", len(msg.Message.Attachments)),
		)
	}
	if err := client.sendPreparedMessage(ctx, openID, msg.Message); err != nil {
		if a.logger != nil {
			a.logger.Error("wechatoa send failed",
				slog.String("config_id", cfg.ID),
				slog.String("target", openID),
				slog.Any("error", err),
			)
		}
		return err
	}
	if a.logger != nil {
		a.logger.Info("wechatoa send success",
			slog.String("config_id", cfg.ID),
			slog.String("target", openID),
		)
	}
	return nil
}

func (a *WeChatOAAdapter) ResolveAttachment(ctx context.Context, cfg channel.ChannelConfig, attachment channel.Attachment) (channel.AttachmentPayload, error) {
	client, err := a.clientForConfig(cfg.Credentials)
	if err != nil {
		return channel.AttachmentPayload{}, err
	}
	mediaID := strings.TrimSpace(attachment.PlatformKey)
	if mediaID == "" {
		return channel.AttachmentPayload{}, errors.New("wechatoa platform_key(media_id) is required")
	}
	return client.downloadMedia(ctx, mediaID)
}

func (a *WeChatOAAdapter) OpenStream(_ context.Context, cfg channel.ChannelConfig, target string, opts channel.StreamOptions) (channel.PreparedOutboundStream, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, errors.New("wechatoa target is required")
	}
	return &outboundStream{
		adapter: a,
		cfg:     cfg,
		target:  target,
		opts:    opts,
	}, nil
}

type outboundStream struct {
	adapter     *WeChatOAAdapter
	cfg         channel.ChannelConfig
	target      string
	opts        channel.StreamOptions
	closed      bool
	textBuilder strings.Builder
	attachments []channel.PreparedAttachment
	final       *channel.PreparedMessage
	mu          sync.Mutex
}

func (s *outboundStream) Push(_ context.Context, event channel.PreparedStreamEvent) error {
	if s.closed {
		return errors.New("wechatoa stream is closed")
	}
	switch event.Type {
	case channel.StreamEventDelta:
		if strings.TrimSpace(event.Delta) == "" || event.Phase == channel.StreamPhaseReasoning {
			return nil
		}
		s.mu.Lock()
		s.textBuilder.WriteString(event.Delta)
		s.mu.Unlock()
	case channel.StreamEventAttachment:
		if len(event.Attachments) == 0 {
			return nil
		}
		s.mu.Lock()
		s.attachments = append(s.attachments, event.Attachments...)
		s.mu.Unlock()
	case channel.StreamEventFinal:
		if event.Final != nil {
			msg := event.Final.Message
			s.mu.Lock()
			s.final = &msg
			s.mu.Unlock()
		}
	}
	return nil
}

func (s *outboundStream) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
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
		msg.Message.Attachments = logicalAttachments(s.attachments)
	}
	s.mu.Unlock()

	if msg.Message.IsEmpty() && len(msg.Attachments) == 0 {
		return nil
	}
	return s.adapter.Send(ctx, s.cfg, channel.PreparedOutboundMessage{
		Target:  s.target,
		Message: msg,
	})
}

func logicalAttachments(attachments []channel.PreparedAttachment) []channel.Attachment {
	if len(attachments) == 0 {
		return nil
	}
	logical := make([]channel.Attachment, 0, len(attachments))
	for _, att := range attachments {
		logical = append(logical, att.Logical)
	}
	return logical
}
