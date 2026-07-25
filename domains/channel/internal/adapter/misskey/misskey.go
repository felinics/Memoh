package misskey

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/domains/channel/internal/common"
	"github.com/memohai/memoh/internal/redact"
	"github.com/memohai/memoh/internal/textutil"
)

const (
	misskeyMaxNoteLength   = 3000
	misskeyReconnectDelay  = 5 * time.Second
	misskeyPingInterval    = 30 * time.Second
	misskeyWriteTimeout    = 10 * time.Second
	misskeyReadBufferSize  = 1 << 16
	misskeyWriteBufferSize = 1 << 16
)

// MisskeyAdapter implements the gateway.Adapter interfaces for Misskey.
type MisskeyAdapter struct {
	logger *slog.Logger
	mu     sync.RWMutex
	me     map[string]*meResponse // keyed by config ID
}

// NewMisskeyAdapter creates a MisskeyAdapter with the given logger.
func NewMisskeyAdapter(log *slog.Logger) *MisskeyAdapter {
	if log == nil {
		log = slog.Default()
	}
	return &MisskeyAdapter{
		logger: log.With(slog.String("adapter", "misskey")),
		me:     make(map[string]*meResponse),
	}
}

// Type returns the Misskey channel type.
func (*MisskeyAdapter) Type() gateway.ChannelType {
	return Type
}

// Descriptor returns the Misskey channel metadata.
func (*MisskeyAdapter) Descriptor() gateway.Descriptor {
	return gateway.Descriptor{
		Type:        Type,
		DisplayName: "Misskey",
		Capabilities: gateway.ChannelCapabilities{
			Text:           true,
			Markdown:       true,
			Reply:          true,
			Reactions:      true,
			Attachments:    false,
			Media:          false,
			Streaming:      false,
			BlockStreaming: true,
			Edit:           false,
		},
		OutboundPolicy: gateway.OutboundPolicy{
			TextChunkLimit: misskeyMaxNoteLength,
			ChunkerMode:    gateway.ChunkerModeMarkdown,
		},
		ConfigSchema: gateway.ConfigSchema{
			Version: 1,
			Fields: map[string]gateway.FieldSchema{
				"instanceURL": {
					Type:        gateway.FieldString,
					Required:    true,
					Title:       "Instance URL",
					Description: "Misskey instance URL (e.g. https://misskey.io)",
					Example:     "https://misskey.io",
				},
				"accessToken": {
					Type:     gateway.FieldSecret,
					Required: true,
					Title:    "Access Token",
				},
			},
		},
		UserConfigSchema: gateway.ConfigSchema{
			Version: 1,
			Fields: map[string]gateway.FieldSchema{
				"username": {Type: gateway.FieldString},
				"user_id":  {Type: gateway.FieldString},
			},
		},
		TargetSpec: gateway.TargetSpec{
			Format: "user_id | @username",
			Hints: []gateway.TargetHint{
				{Label: "User ID", Example: "9abcdef123456789"},
				{Label: "Username", Example: "@alice"},
			},
		},
	}
}

// --- ConfigNormalizer ---

// NormalizeConfig validates and normalizes a Misskey channel configuration map.
func (*MisskeyAdapter) NormalizeConfig(raw map[string]any) (map[string]any, error) {
	return normalizeConfig(raw)
}

// NormalizeUserConfig validates and normalizes a Misskey user-binding configuration map.
func (*MisskeyAdapter) NormalizeUserConfig(raw map[string]any) (map[string]any, error) {
	return normalizeUserConfig(raw)
}

// --- TargetResolver ---

// NormalizeTarget normalizes a Misskey delivery target string.
func (*MisskeyAdapter) NormalizeTarget(raw string) string {
	return normalizeTarget(raw)
}

// ResolveTarget derives a delivery target from a Misskey user-binding configuration.
func (*MisskeyAdapter) ResolveTarget(userConfig map[string]any) (string, error) {
	return resolveTarget(userConfig)
}

// --- BindingMatcher ---

// MatchBinding reports whether a Misskey user binding matches the given criteria.
func (*MisskeyAdapter) MatchBinding(config map[string]any, criteria gateway.BindingCriteria) bool {
	return matchBinding(config, criteria)
}

// BuildUserConfig constructs a Misskey user-binding config from an Identity.
func (*MisskeyAdapter) BuildUserConfig(identity gateway.Identity) map[string]any {
	return buildUserConfig(identity)
}

// --- SelfDiscoverer ---

// DiscoverSelf retrieves the bot's own identity from the Misskey iam.
func (*MisskeyAdapter) DiscoverSelf(ctx context.Context, credentials map[string]any) (map[string]any, string, error) {
	cfg, err := parseConfig(credentials)
	if err != nil {
		return nil, "", err
	}
	me, err := getMe(ctx, cfg)
	if err != nil {
		return nil, "", fmt.Errorf("misskey discover self: %w", err)
	}
	identity := map[string]any{
		"user_id":  me.ID,
		"username": me.Username,
	}
	if me.Name != "" {
		identity["name"] = me.Name
	}
	if me.AvatarURL != "" {
		identity["avatar_url"] = me.AvatarURL
	}
	return identity, me.ID, nil
}

// --- Receiver ---

// Connect starts a WebSocket streaming connection to receive Misskey mentions.
func (a *MisskeyAdapter) Connect(ctx context.Context, cfg gateway.ChannelConfig, handler gateway.InboundHandler) (gateway.Connection, error) {
	if a.logger != nil {
		a.logger.InfoContext(ctx, "start", slog.String("config_id", cfg.ID))
	}
	mkCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		return nil, err
	}
	redact.SetSecrets("misskey:"+cfg.ID, mkCfg.AccessToken)

	// Fetch self info for mention detection.
	me, err := getMe(ctx, mkCfg)
	if err != nil {
		return nil, fmt.Errorf("misskey get self: %w", err)
	}
	a.mu.Lock()
	a.me[cfg.ID] = me
	a.mu.Unlock()

	connCtx, cancel := context.WithCancel(ctx)
	go a.runStreamLoop(connCtx, cfg, mkCfg, me, handler)

	stop := func(_ context.Context) error {
		if a.logger != nil {
			a.logger.InfoContext(ctx, "stop", slog.String("config_id", cfg.ID))
		}
		cancel()
		return nil
	}
	return gateway.NewConnection(cfg, stop), nil
}

func (a *MisskeyAdapter) runStreamLoop(ctx context.Context, cfg gateway.ChannelConfig, mkCfg Config, me *meResponse, handler gateway.InboundHandler) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := a.runStream(ctx, cfg, mkCfg, me, handler); err != nil {
			if a.logger != nil {
				a.logger.WarnContext(ctx, "stream disconnected", slog.String("config_id", cfg.ID), slog.Any("error", err))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(misskeyReconnectDelay):
		}
	}
}

func (a *MisskeyAdapter) runStream(ctx context.Context, cfg gateway.ChannelConfig, mkCfg Config, me *meResponse, handler gateway.InboundHandler) error {
	dialer := websocket.Dialer{
		ReadBufferSize:  misskeyReadBufferSize,
		WriteBufferSize: misskeyWriteBufferSize,
	}
	conn, resp, err := dialer.DialContext(ctx, mkCfg.streamURL(), nil) //nolint:bodyclose // resp.Body is closed below
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return fmt.Errorf("misskey ws dial: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if a.logger != nil {
		a.logger.InfoContext(ctx, "stream connected", slog.String("config_id", cfg.ID))
	}

	// Subscribe to main channel to receive mentions.
	connectMsg := map[string]any{
		"type": "connect",
		"body": map[string]any{
			"channel": "main",
			"id":      "memoh-main",
		},
	}
	if err := conn.WriteJSON(connectMsg); err != nil {
		return fmt.Errorf("misskey ws connect main: %w", err)
	}

	// Start ping ticker.
	pingDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(misskeyPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-pingDone:
				return
			case <-ticker.C:
				_ = conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(misskeyWriteTimeout))
			}
		}
	}()
	defer close(pingDone)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		_, msgBytes, readErr := conn.ReadMessage()
		if readErr != nil {
			return fmt.Errorf("misskey ws read: %w", readErr)
		}
		a.handleStreamMessage(ctx, cfg, me, handler, msgBytes)
	}
}

// streamMessage represents a message received from the Misskey streaming API.
type streamMessage struct {
	Type string          `json:"type"`
	Body json.RawMessage `json:"body"`
}

// streamChannelBody is the body of a channel event.
type streamChannelBody struct {
	ID   string          `json:"id"`
	Type string          `json:"type"`
	Body json.RawMessage `json:"body"`
}

// misskeyNote represents a Misskey note (post).
type misskeyNote struct {
	ID         string       `json:"id"`
	Text       string       `json:"text"`
	CW         string       `json:"cw"`
	UserID     string       `json:"userId"`
	User       misskeyUser  `json:"user"`
	ReplyID    string       `json:"replyId"`
	RenoteID   string       `json:"renoteId"`
	CreatedAt  string       `json:"createdAt"`
	Mentions   []string     `json:"mentions"`
	Visibility string       `json:"visibility"`
	Reply      *misskeyNote `json:"reply"`
	Renote     *misskeyNote `json:"renote"`
}

type misskeyUser struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Name      string `json:"name"`
	Host      string `json:"host"`
	AvatarURL string `json:"avatarUrl"`
}

func (a *MisskeyAdapter) handleStreamMessage(ctx context.Context, cfg gateway.ChannelConfig, me *meResponse, handler gateway.InboundHandler, raw []byte) {
	var msg streamMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}

	if msg.Type == "channel" {
		var body streamChannelBody
		if err := json.Unmarshal(msg.Body, &body); err != nil {
			return
		}
		a.handleChannelEvent(ctx, cfg, me, handler, body)
	}
}

func (a *MisskeyAdapter) handleChannelEvent(ctx context.Context, cfg gateway.ChannelConfig, me *meResponse, handler gateway.InboundHandler, body streamChannelBody) {
	switch body.Type {
	case "mention", "reply":
		var note misskeyNote
		if err := json.Unmarshal(body.Body, &note); err != nil {
			if a.logger != nil {
				a.logger.WarnContext(ctx, "parse note failed", slog.String("config_id", cfg.ID), slog.Any("error", err))
			}
			return
		}
		// Skip notes from self.
		if note.UserID == me.ID {
			return
		}
		inbound, ok := a.buildInboundMessage(me, note)
		if !ok {
			return
		}
		a.logInbound(cfg.ID, inbound)
		go func() {
			if err := handler(ctx, cfg, inbound); err != nil && a.logger != nil {
				a.logger.ErrorContext(ctx, "handle inbound failed", slog.String("config_id", cfg.ID), slog.Any("error", err))
			}
		}()

	case "notification":
		// Handle notification-based mentions.
		var notif struct {
			Type string      `json:"type"`
			Note misskeyNote `json:"note"`
		}
		if err := json.Unmarshal(body.Body, &notif); err != nil {
			return
		}
		if notif.Type != "mention" && notif.Type != "reply" {
			return
		}
		if notif.Note.UserID == me.ID {
			return
		}
		inbound, ok := a.buildInboundMessage(me, notif.Note)
		if !ok {
			return
		}
		a.logInbound(cfg.ID, inbound)
		go func() {
			if err := handler(ctx, cfg, inbound); err != nil && a.logger != nil {
				a.logger.ErrorContext(ctx, "handle inbound failed", slog.String("config_id", cfg.ID), slog.Any("error", err))
			}
		}()
	}
}

func (*MisskeyAdapter) buildInboundMessage(me *meResponse, note misskeyNote) (gateway.InboundMessage, bool) {
	text := strings.TrimSpace(note.Text)
	forwardRef := buildMisskeyForwardRef(note)
	if text == "" && forwardRef == nil {
		return gateway.InboundMessage{}, false
	}

	// Strip the bot mention from the text.
	if me != nil && text != "" {
		mention := "@" + me.Username
		text = strings.TrimSpace(strings.Replace(text, mention, "", 1))
	}
	if text == "" && forwardRef == nil {
		return gateway.InboundMessage{}, false
	}

	senderID := note.UserID
	displayName := note.User.Name
	if displayName == "" {
		displayName = note.User.Username
	}
	attrs := map[string]string{
		"user_id":  note.UserID,
		"username": note.User.Username,
	}
	if note.User.Host != "" {
		attrs["host"] = note.User.Host
	}

	// Direct messages use "specified" visibility; others are group conversations.
	convType := gateway.ConversationTypeGroup
	if note.Visibility == "specified" {
		convType = gateway.ConversationTypePrivate
	}

	replyRef := buildMisskeyReplyRef(note)

	receivedAt := time.Now().UTC()
	if note.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339, note.CreatedAt); err == nil {
			receivedAt = t
		}
	}

	isMentioned := false
	if me != nil {
		for _, mid := range note.Mentions {
			if mid == me.ID {
				isMentioned = true
				break
			}
		}
	}
	// Replying to the bot's own note is directed at the bot even without an
	// explicit @-mention; without this flag, plain-text ask_user replies in
	// non-private visibility are silently dropped by isDirectedAtBot.
	isReplyToBot := me != nil && note.Reply != nil && strings.TrimSpace(note.Reply.UserID) == me.ID

	return gateway.InboundMessage{
		Channel: Type,
		Message: gateway.Message{
			ID:      note.ID,
			Format:  gateway.MessageFormatPlain,
			Text:    text,
			Reply:   replyRef,
			Forward: forwardRef,
		},
		ReplyTarget: note.ID,
		Sender: gateway.Identity{
			SubjectID:   senderID,
			DisplayName: displayName,
			Attributes:  attrs,
		},
		Conversation: gateway.Conversation{
			ID:   note.UserID,
			Type: convType,
		},
		ReceivedAt: receivedAt,
		Source:     "misskey",
		Metadata: map[string]any{
			"is_mentioned":    isMentioned,
			"is_reply_to_bot": isReplyToBot,
			"visibility":      note.Visibility,
			"note_id":         note.ID,
		},
	}, true
}

func buildMisskeyReplyRef(note misskeyNote) *gateway.ReplyRef {
	messageID := strings.TrimSpace(note.ReplyID)
	if note.Reply != nil && strings.TrimSpace(note.Reply.ID) != "" {
		messageID = strings.TrimSpace(note.Reply.ID)
	}
	reply := &gateway.ReplyRef{MessageID: messageID}
	if note.Reply != nil {
		reply.Sender = misskeyUserDisplayName(note.Reply.User)
		reply.Preview = trimMisskeyPreview(note.Reply.Text)
	}
	if reply.MessageID == "" && reply.Sender == "" && reply.Preview == "" {
		return nil
	}
	return reply
}

func buildMisskeyForwardRef(note misskeyNote) *gateway.ForwardRef {
	messageID := strings.TrimSpace(note.RenoteID)
	if note.Renote != nil && strings.TrimSpace(note.Renote.ID) != "" {
		messageID = strings.TrimSpace(note.Renote.ID)
	}
	forward := &gateway.ForwardRef{MessageID: messageID}
	if note.Renote != nil {
		forward.FromUserID = strings.TrimSpace(note.Renote.UserID)
		forward.Sender = misskeyUserDisplayName(note.Renote.User)
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(note.Renote.CreatedAt)); err == nil {
			forward.Date = t.Unix()
		}
	}
	if forward.MessageID == "" && forward.FromUserID == "" && forward.Sender == "" && forward.Date == 0 {
		return nil
	}
	return forward
}

func misskeyUserDisplayName(user misskeyUser) string {
	if name := strings.TrimSpace(user.Name); name != "" {
		return name
	}
	return strings.TrimSpace(user.Username)
}

func trimMisskeyPreview(value string) string {
	preview := strings.TrimSpace(value)
	if len([]rune(preview)) > 200 {
		return string([]rune(preview)[:200]) + "..."
	}
	return preview
}

func (a *MisskeyAdapter) logInbound(configID string, msg gateway.InboundMessage) {
	if a.logger == nil {
		return
	}
	a.logger.Info("inbound received",
		slog.String("config_id", configID),
		slog.String("user_id", msg.Sender.Attribute("user_id")),
		slog.String("username", msg.Sender.Attribute("username")),
		slog.String("text", common.SummarizeText(msg.Message.Text)),
	)
}

// --- Sender ---

// Send delivers an outbound message to Misskey by creating a note.
func (a *MisskeyAdapter) Send(ctx context.Context, cfg gateway.ChannelConfig, msg gateway.PreparedOutboundMessage) error {
	mkCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		return err
	}
	text := strings.TrimSpace(msg.Message.Message.PlainText())
	if text == "" {
		return errors.New("message text is required")
	}
	text = textutil.TruncateRunesWithSuffix(text, misskeyMaxNoteLength, "...")

	replyID := strings.TrimSpace(msg.Target)
	visibility := "home"

	_, err = createNote(ctx, mkCfg, text, replyID, visibility)
	if err != nil {
		if a.logger != nil {
			a.logger.ErrorContext(ctx, "send note failed", slog.String("config_id", cfg.ID), slog.Any("error", err))
		}
		return err
	}
	return nil
}

// --- Reactor ---

// React adds an emoji reaction to a message (implements gateway.Reactor).
func (*MisskeyAdapter) React(ctx context.Context, cfg gateway.ChannelConfig, _ string, messageID string, emoji string) error {
	mkCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		return err
	}
	// Misskey reactions use format like ":emoji:" or unicode emoji.
	if !strings.HasPrefix(emoji, ":") {
		emoji = ":" + emoji + ":"
	}
	return createReaction(ctx, mkCfg, messageID, emoji)
}

// Unreact removes the bot's reaction from a message (implements gateway.Reactor).
func (*MisskeyAdapter) Unreact(ctx context.Context, cfg gateway.ChannelConfig, _ string, messageID string, _ string) error {
	mkCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		return err
	}
	return deleteReaction(ctx, mkCfg, messageID)
}

// --- ProcessingStatusNotifier ---

// ProcessingStarted is a no-op for Misskey (no typing indicator API).
func (*MisskeyAdapter) ProcessingStarted(_ context.Context, _ gateway.ChannelConfig, _ gateway.InboundMessage, _ gateway.ProcessingStatusInfo) (gateway.ProcessingStatusHandle, error) {
	return gateway.ProcessingStatusHandle{}, nil
}

// ProcessingCompleted is a no-op for Misskey.
func (*MisskeyAdapter) ProcessingCompleted(_ context.Context, _ gateway.ChannelConfig, _ gateway.InboundMessage, _ gateway.ProcessingStatusInfo, _ gateway.ProcessingStatusHandle) error {
	return nil
}

// ProcessingFailed is a no-op for Misskey.
func (*MisskeyAdapter) ProcessingFailed(_ context.Context, _ gateway.ChannelConfig, _ gateway.InboundMessage, _ gateway.ProcessingStatusInfo, _ gateway.ProcessingStatusHandle, _ error) error {
	return nil
}

// --- StreamSender (block-streaming: buffer deltas, send final as one message) ---

// OpenStream opens a block-streaming session that buffers all deltas and sends
// the final message as a single note when the stream is closed.
func (a *MisskeyAdapter) OpenStream(_ context.Context, cfg gateway.ChannelConfig, target string, _ gateway.StreamOptions) (gateway.PreparedOutboundStream, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, errors.New("misskey target is required")
	}
	return &misskeyBlockStream{
		adapter: a,
		cfg:     cfg,
		target:  target,
	}, nil
}

// misskeyBlockStream buffers streaming deltas and sends the final message as
// one Send call when the stream is closed.
type misskeyBlockStream struct {
	adapter     *MisskeyAdapter
	cfg         gateway.ChannelConfig
	target      string
	textBuilder strings.Builder
	attachments []gateway.PreparedAttachment
	final       *gateway.PreparedMessage
	closed      bool
}

func (s *misskeyBlockStream) Push(_ context.Context, event gateway.PreparedStreamEvent) error {
	if s.closed {
		return nil
	}
	switch event.Type {
	case gateway.StreamEventDelta:
		if strings.TrimSpace(event.Delta) != "" && event.Phase != gateway.StreamPhaseReasoning {
			s.textBuilder.WriteString(event.Delta)
		}
	case gateway.StreamEventAttachment:
		s.attachments = append(s.attachments, event.Attachments...)
	case gateway.StreamEventFinal:
		if event.Final != nil {
			msg := event.Final.Message
			s.final = &msg
		}
	}
	return nil
}

func (s *misskeyBlockStream) Close(ctx context.Context) error {
	if s.closed {
		return nil
	}
	s.closed = true

	prepared := gateway.PreparedMessage{Message: gateway.Message{Format: gateway.MessageFormatPlain}}
	if s.final != nil {
		prepared = *s.final
	}
	if strings.TrimSpace(prepared.Message.Text) == "" {
		prepared.Message.Text = strings.TrimSpace(s.textBuilder.String())
	}
	if len(prepared.Attachments) == 0 && len(s.attachments) > 0 {
		prepared.Attachments = append(prepared.Attachments, s.attachments...)
	}
	if prepared.Message.IsEmpty() {
		return nil
	}
	return s.adapter.Send(ctx, s.cfg, gateway.PreparedOutboundMessage{
		Target:  s.target,
		Message: prepared,
	})
}
