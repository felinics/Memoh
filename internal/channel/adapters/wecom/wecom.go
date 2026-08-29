package wecom

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/felinics/memoh/internal/channel"
)

const Type channel.ChannelType = "wecom"

type wsClientFactory func(opts WSClientOptions) *WSClient

type WeComAdapter struct {
	logger *slog.Logger

	mu      sync.RWMutex
	clients map[string]*WSClient
	http    *HTTPClient
	cache   *callbackContextCache

	newWSClient wsClientFactory
}

func NewWeComAdapter(log *slog.Logger) *WeComAdapter {
	if log == nil {
		log = slog.Default()
	}
	return &WeComAdapter{
		logger:      log.With(slog.String("adapter", "wecom")),
		clients:     make(map[string]*WSClient),
		http:        NewHTTPClient(HTTPClientOptions{Logger: log}),
		cache:       newCallbackContextCache(24 * time.Hour),
		newWSClient: NewWSClient,
	}
}

func (*WeComAdapter) Type() channel.ChannelType { return Type }

func (*WeComAdapter) Descriptor() channel.Descriptor {
	return channel.Descriptor{
		Type:        Type,
		DisplayName: "WeCom",
		Capabilities: channel.ChannelCapabilities{
			Text:           true,
			Markdown:       true,
			Attachments:    true,
			Media:          true,
			Reply:          true,
			Streaming:      true,
			BlockStreaming: true,
			ChatTypes:      []string{channel.ConversationTypePrivate, channel.ConversationTypeGroup},
		},
		ConfigSchema: channel.ConfigSchema{
			Version: 1,
			Fields: map[string]channel.FieldSchema{
				"botId":               {Type: channel.FieldString, Required: true, Title: "Bot ID"},
				"secret":              {Type: channel.FieldSecret, Required: true, Title: "Secret"},
				"wsUrl":               {Type: channel.FieldString, Title: "WebSocket URL", Example: defaultWSURL},
				"heartbeatSeconds":    {Type: channel.FieldNumber, Title: "Heartbeat Seconds"},
				"ackTimeoutSeconds":   {Type: channel.FieldNumber, Title: "Ack Timeout Seconds"},
				"writeTimeoutSeconds": {Type: channel.FieldNumber, Title: "Write Timeout Seconds"},
				"readTimeoutSeconds":  {Type: channel.FieldNumber, Title: "Read Timeout Seconds"},
			},
		},
		UserConfigSchema: channel.ConfigSchema{
			Version: 1,
			Fields: map[string]channel.FieldSchema{
				"chat_id": {Type: channel.FieldString},
				"user_id": {Type: channel.FieldString},
			},
		},
		TargetSpec: channel.TargetSpec{
			Format: "chat_id:xxx | user_id:xxx",
			Hints: []channel.TargetHint{
				{Label: "Chat ID", Example: "chat_id:work_abc"},
				{Label: "User ID", Example: "user_id:zhangsan"},
			},
		},
	}
}

func (*WeComAdapter) NormalizeConfig(raw map[string]any) (map[string]any, error) {
	return normalizeConfig(raw)
}

func (*WeComAdapter) NormalizeUserConfig(raw map[string]any) (map[string]any, error) {
	return normalizeUserConfig(raw)
}

func (*WeComAdapter) NormalizeTarget(raw string) string { return normalizeTarget(raw) }

func (*WeComAdapter) ResolveTarget(userConfig map[string]any) (string, error) {
	return resolveTarget(userConfig)
}

func (*WeComAdapter) MatchBinding(config map[string]any, criteria channel.BindingCriteria) bool {
	return matchBinding(config, criteria)
}

func (*WeComAdapter) BuildUserConfig(identity channel.Identity) map[string]any {
	return buildUserConfig(identity)
}

func (*WeComAdapter) DiscoverSelf(ctx context.Context, credentials map[string]any) (map[string]any, string, error) {
	_ = ctx
	cfg, err := parseConfig(credentials)
	if err != nil {
		return nil, "", err
	}
	externalID := strings.TrimSpace(cfg.BotID)
	identity := map[string]any{
		"bot_id":   externalID,
		"aibot_id": externalID,
	}
	return identity, externalID, nil
}

func (a *WeComAdapter) Connect(ctx context.Context, cfg channel.ChannelConfig, handler channel.InboundHandler) (channel.Connection, error) {
	parsed, err := parseConfig(cfg.Credentials)
	if err != nil {
		return nil, err
	}
	client := a.newWSClient(WSClientOptions{
		URL:                parsed.WSURL,
		Logger:             a.logger,
		HeartbeatInterval:  time.Duration(secondsOrDefault(parsed.HeartbeatSeconds, 30)) * time.Second,
		AckTimeout:         time.Duration(secondsOrDefault(parsed.AckTimeoutSeconds, 8)) * time.Second,
		WriteTimeout:       time.Duration(secondsOrDefault(parsed.WriteTimeoutSeconds, 8)) * time.Second,
		ReadTimeout:        time.Duration(secondsOrDefault(parsed.ReadTimeoutSeconds, 70)) * time.Second,
		ReconnectBaseDelay: 1 * time.Second,
		ReconnectMaxDelay:  30 * time.Second,
	})

	key := wecomClientCacheKey(cfg.ID, parsed.BotID)
	a.mu.Lock()
	a.clients[key] = client
	a.mu.Unlock()

	connCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := client.Run(connCtx, AuthCredentials{
			BotID:      parsed.BotID,
			Credential: parsed.Credential,
		}, func(frameCtx context.Context, frame WSFrame) error {
			return a.handleFrame(frameCtx, cfg, frame, handler)
		})
		if err != nil && connCtx.Err() == nil {
			a.logger.Error("wecom websocket stopped",
				slog.String("config_id", cfg.ID),
				slog.Any("error", err),
			)
		}
	}()

	stop := func(context.Context) error {
		cancel()
		_ = client.Close()
		<-done
		a.mu.Lock()
		if current, ok := a.clients[key]; ok && current == client {
			delete(a.clients, key)
		}
		a.mu.Unlock()
		return nil
	}
	return channel.NewConnection(cfg, stop), nil
}

func (a *WeComAdapter) Send(ctx context.Context, cfg channel.ChannelConfig, msg channel.PreparedOutboundMessage) error {
	logical := msg.LogicalMessage()
	targetKind, targetID, ok := parseTarget(logical.Target)
	if !ok {
		return errors.New("wecom target is required")
	}
	parsed, err := parseConfig(cfg.Credentials)
	if err != nil {
		return err
	}
	client := a.getClient(cfg.ID, parsed.BotID)
	if client == nil {
		return errors.New("wecom connection is not active")
	}
	if logical.Message.IsEmpty() {
		return errors.New("message is required")
	}
	var (
		payload  any
		cmd      string
		reqID    string
		buildErr error
	)
	if ctxMeta, ok := a.lookupCallbackContext(cfg.ID, msg.Message.Message.Reply); ok {
		payload, cmd, reqID, buildErr = buildPreparedRespondPayload(ctx, msg.Message, ctxMeta.ReqID)
	} else {
		_ = targetKind
		payload, cmd, reqID, buildErr = buildSendPayload(logical.Message, targetID)
	}
	if buildErr != nil {
		return buildErr
	}
	ack, err := client.Reply(ctx, reqID, cmd, payload)
	if err != nil {
		return err
	}
	if ack.ErrCode != 0 {
		return fmt.Errorf("wecom send failed: %s (code: %d)", strings.TrimSpace(ack.ErrMsg), ack.ErrCode)
	}
	return nil
}

func (a *WeComAdapter) OpenStream(ctx context.Context, cfg channel.ChannelConfig, target string, opts channel.StreamOptions) (channel.PreparedOutboundStream, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, errors.New("wecom target is required")
	}
	reply := opts.Reply
	if reply == nil && strings.TrimSpace(opts.SourceMessageID) != "" {
		reply = &channel.ReplyRef{
			Target:    target,
			MessageID: strings.TrimSpace(opts.SourceMessageID),
		}
	}
	return &wecomOutboundStream{
		adapter: a,
		cfg:     cfg,
		target:  target,
		reply:   reply,
	}, nil
}

type wecomOutboundStream struct {
	adapter *WeComAdapter
	cfg     channel.ChannelConfig
	target  string
	reply   *channel.ReplyRef

	mu          sync.Mutex
	closed      atomic.Bool
	finalSent   atomic.Bool
	textBuilder strings.Builder
	attachments []channel.PreparedAttachment
	final       *channel.PreparedMessage
	streamID    string
	lastPreview string
}

func (s *wecomOutboundStream) Push(ctx context.Context, event channel.PreparedStreamEvent) error {
	if s.adapter == nil {
		return errors.New("wecom stream not configured")
	}
	if s.closed.Load() {
		return errors.New("wecom stream is closed")
	}
	if s.finalSent.Load() {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	switch event.Type {
	case channel.StreamEventStatus,
		channel.StreamEventPhaseStart,
		channel.StreamEventPhaseEnd,
		channel.StreamEventToolCallStart,
		channel.StreamEventAgentStart,
		channel.StreamEventAgentEnd,
		channel.StreamEventProcessingStarted,
		channel.StreamEventProcessingCompleted,
		channel.StreamEventProcessingFailed:
		return nil
	case channel.StreamEventToolCallEnd:
		return s.sendToolCallSummary(ctx, event.ToolCall)
	case channel.StreamEventDelta:
		if strings.TrimSpace(event.Delta) == "" || event.Phase == channel.StreamPhaseReasoning {
			return nil
		}
		s.mu.Lock()
		s.textBuilder.WriteString(event.Delta)
		s.mu.Unlock()
		return s.pushPreview(ctx)
	case channel.StreamEventAttachment:
		if len(event.Attachments) == 0 {
			return nil
		}
		s.mu.Lock()
		s.attachments = append(s.attachments, event.Attachments...)
		s.mu.Unlock()
		return nil
	case channel.StreamEventFinal:
		if event.Final == nil {
			return nil
		}
		s.mu.Lock()
		final := event.Final.Message
		s.final = &final
		s.mu.Unlock()
		return s.flush(ctx)
	case channel.StreamEventError:
		text := strings.TrimSpace(event.Error)
		if text == "" {
			return nil
		}
		s.mu.Lock()
		s.final = &channel.PreparedMessage{
			Message: channel.Message{Format: channel.MessageFormatPlain, Text: "Error: " + text},
		}
		s.mu.Unlock()
		return s.flush(ctx)
	}
	return nil
}

func (s *wecomOutboundStream) Close(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	s.closed.Store(true)
	if s.finalSent.Load() {
		return nil
	}
	return s.flush(ctx)
}

func (s *wecomOutboundStream) flush(ctx context.Context) error {
	if s.finalSent.Load() {
		return nil
	}
	msg, streamID := s.snapshotMessage(true)
	if msg.LogicalMessage().IsEmpty() {
		return nil
	}
	if ctxMeta, ok := s.adapter.lookupCallbackContext(s.cfg.ID, msg.Message.Reply); ok {
		if err := s.adapter.sendRespondStream(ctx, s.cfg, msg, ctxMeta.ReqID, streamID, true); err != nil {
			return err
		}
		s.finalSent.Store(true)
		return nil
	}
	if err := s.adapter.Send(ctx, s.cfg, channel.PreparedOutboundMessage{
		Target:  s.target,
		Message: msg,
	}); err != nil {
		return err
	}
	s.finalSent.Store(true)
	return nil
}

// sendToolCallSummary emits a best-effort terminal summary of a tool call.
// WeCom lacks message-edit APIs in the one-shot send path, so only the
// completed / failed state is surfaced — the running state is intentionally
// suppressed to avoid duplicate messages.
func (s *wecomOutboundStream) sendToolCallSummary(ctx context.Context, tc *channel.StreamToolCall) error {
	if s.finalSent.Load() {
		return nil
	}
	text := strings.TrimSpace(channel.RenderToolCallMessage(channel.BuildToolCallEnd(tc)))
	if text == "" {
		return nil
	}
	msg := channel.PreparedMessage{
		Message: channel.Message{Format: channel.MessageFormatPlain, Text: text},
	}
	return s.adapter.Send(ctx, s.cfg, channel.PreparedOutboundMessage{
		Target:  s.target,
		Message: msg,
	})
}

func (s *wecomOutboundStream) pushPreview(ctx context.Context) error {
	if s.finalSent.Load() {
		return nil
	}
	msg, streamID := s.snapshotMessage(false)
	text := strings.TrimSpace(msg.LogicalMessage().PlainText())
	if text == "" {
		return nil
	}
	s.mu.Lock()
	if s.lastPreview == text {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	if ctxMeta, ok := s.adapter.lookupCallbackContext(s.cfg.ID, msg.Message.Reply); ok {
		if err := s.adapter.sendRespondStream(ctx, s.cfg, msg, ctxMeta.ReqID, streamID, false); err != nil {
			return err
		}
		s.mu.Lock()
		s.lastPreview = text
		s.mu.Unlock()
	}
	return nil
}

func (s *wecomOutboundStream) snapshotMessage(includeAttachments bool) (channel.PreparedMessage, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg := channel.PreparedMessage{}
	if s.final != nil {
		msg = *s.final
	}
	if strings.TrimSpace(msg.Message.Text) == "" {
		msg.Message.Text = strings.TrimSpace(s.textBuilder.String())
	}
	if includeAttachments && len(msg.Attachments) == 0 && len(s.attachments) > 0 {
		msg.Attachments = append(msg.Attachments, s.attachments...)
	}
	if len(msg.Message.Attachments) == 0 && len(msg.Attachments) > 0 {
		msg.Message.Attachments = make([]channel.Attachment, 0, len(msg.Attachments))
		for _, att := range msg.Attachments {
			msg.Message.Attachments = append(msg.Message.Attachments, att.Logical)
		}
	}
	if msg.Message.Reply == nil && s.reply != nil {
		msg.Message.Reply = s.reply
	}
	if s.streamID == "" {
		s.streamID = NewReqID("stream")
	}
	return msg, s.streamID
}

func (a *WeComAdapter) sendRespondStream(ctx context.Context, cfg channel.ChannelConfig, msg channel.PreparedMessage, reqID string, streamID string, finish bool) error {
	parsed, err := parseConfig(cfg.Credentials)
	if err != nil {
		return err
	}
	client := a.getClient(cfg.ID, parsed.BotID)
	if client == nil {
		return errors.New("wecom connection is not active")
	}
	payload, cmd, ackReqID, err := buildPreparedRespondPayloadWithStream(ctx, msg, reqID, streamID, finish)
	if err != nil {
		return err
	}
	ack, err := client.Reply(ctx, ackReqID, cmd, payload)
	if err != nil {
		return err
	}
	if ack.ErrCode != 0 {
		return fmt.Errorf("wecom send failed: %s (code: %d)", strings.TrimSpace(ack.ErrMsg), ack.ErrCode)
	}
	return nil
}

func secondsOrDefault(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}
