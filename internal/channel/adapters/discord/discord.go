package discord

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/common"
	"github.com/felinics/memoh/internal/media"
	"github.com/felinics/memoh/internal/redact"
)

const (
	inboundDedupTTL            = time.Minute
	discordMaxLength           = 2000
	discordMaxURLActionButtons = 25
	discordMaxURLActionLabel   = 80
	discordMaxURLActionURL     = 512
	discordMaxAllowedMentions  = 100
)

// assetOpener reads stored asset bytes by content hash.
type assetOpener interface {
	Open(ctx context.Context, botID, contentHash string) (io.ReadCloser, media.Asset, error)
}

type DiscordAdapter struct {
	logger          *slog.Logger
	mu              sync.RWMutex
	sessions        map[string]*discordgo.Session // keyed by config ID + bot token
	handlerRemovers map[string]func()             // keyed by config ID + bot token
	seenMessages    map[string]time.Time          // keyed by config ID + message ID
	assets          assetOpener
}

func NewDiscordAdapter(log *slog.Logger) *DiscordAdapter {
	if log == nil {
		log = slog.Default()
	}
	return &DiscordAdapter{
		logger:          log.With(slog.String("adapter", "discord")),
		sessions:        make(map[string]*discordgo.Session),
		handlerRemovers: make(map[string]func()),
		seenMessages:    make(map[string]time.Time),
	}
}

// SetAssetOpener configures the asset opener for reading stored attachments by content hash.
func (a *DiscordAdapter) SetAssetOpener(opener assetOpener) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.assets = opener
}

func (*DiscordAdapter) Type() channel.ChannelType {
	return Type
}

func (*DiscordAdapter) Descriptor() channel.Descriptor {
	return channel.Descriptor{
		Type:        Type,
		DisplayName: "Discord",
		Capabilities: channel.ChannelCapabilities{
			Text:           true,
			Markdown:       true,
			RichText:       true,
			URLButtons:     true,
			Reply:          true,
			Attachments:    true,
			Media:          true,
			Streaming:      true,
			BlockStreaming: true,
			Reactions:      true,
		},
		ConfigSchema: channel.ConfigSchema{
			Version: 1,
			Fields: map[string]channel.FieldSchema{
				"botToken": {
					Type:     channel.FieldSecret,
					Required: true,
					Title:    "Bot Token",
				},
			},
		},
		UserConfigSchema: channel.ConfigSchema{
			Version: 1,
			Fields: map[string]channel.FieldSchema{
				"user_id":    {Type: channel.FieldString},
				"channel_id": {Type: channel.FieldString},
				"guild_id":   {Type: channel.FieldString},
				"username":   {Type: channel.FieldString},
			},
		},
		TargetSpec: channel.TargetSpec{
			Format: "channel_id | user_id",
			Hints: []channel.TargetHint{
				{Label: "Channel ID", Example: "1234567890123456789"},
				{Label: "User ID", Example: "1234567890123456789"},
			},
		},
	}
}

func (a *DiscordAdapter) getOrCreateSession(token, configID string) (*discordgo.Session, error) {
	redact.SetSecrets("discord:"+configID, token)
	cacheKey := discordSessionCacheKey(configID, token)
	a.mu.RLock()
	session, ok := a.sessions[cacheKey]
	a.mu.RUnlock()
	if ok {
		return session, nil
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if s, ok := a.sessions[cacheKey]; ok {
		return s, nil
	}

	session, err := discordgo.New("Bot " + token)
	if err != nil {
		a.logger.Error("create session failed", slog.String("config_id", configID), slog.Any("error", err))
		return nil, err
	}

	session.Identify.Intents = discordgo.IntentsAll

	a.sessions[cacheKey] = session
	return session, nil
}

func discordSessionCacheKey(configID, token string) string {
	return strings.TrimSpace(configID) + "\x00" + strings.TrimSpace(token)
}

func (a *DiscordAdapter) Connect(ctx context.Context, cfg channel.ChannelConfig, handler channel.InboundHandler) (channel.Connection, error) {
	if a.logger != nil {
		a.logger.Info("start", slog.String("config_id", cfg.ID))
	}

	discordCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		return nil, err
	}

	session, err := a.getOrCreateSession(discordCfg.BotToken, cfg.ID)
	if err != nil {
		return nil, err
	}

	remove := session.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		if m.Author != nil && m.Author.Bot {
			return
		}

		if ctx.Err() != nil {
			return
		}

		if a.isDuplicateInbound(cfg.ID, m.ID) {
			return
		}

		text := strings.TrimSpace(m.Content)
		botID := s.State.User.ID
		if text == "" && len(m.Attachments) == 0 {
			return
		}

		rawText := text
		attachments := a.collectAttachments(m.Message)
		chatType := channel.ConversationTypePrivate
		if m.GuildID != "" {
			chatType = channel.ConversationTypeGroup
		}

		var replyRef *channel.ReplyRef
		if m.ReferencedMessage != nil {
			ref := m.ReferencedMessage
			replyRef = &channel.ReplyRef{
				MessageID:        ref.ID,
				Target:           m.ChannelID,
				Attachments:      a.collectAttachments(ref),
				AttachmentsKnown: true,
			}
			if ref.Author != nil {
				replyRef.Sender = strings.TrimSpace(ref.Author.Username)
			}
			preview := strings.TrimSpace(ref.Content)
			if len([]rune(preview)) > 200 {
				preview = string([]rune(preview)[:200]) + "..."
			}
			replyRef.Preview = preview
		}

		isMentioned := a.isBotMentioned(m.Message, botID)
		isReplyToBot := m.ReferencedMessage != nil &&
			m.ReferencedMessage.Author != nil &&
			m.ReferencedMessage.Author.ID == botID

		msg := channel.InboundMessage{
			Channel: Type,
			Message: channel.Message{
				ID:          m.ID,
				Format:      channel.MessageFormatPlain,
				Text:        text,
				Attachments: attachments,
				Reply:       replyRef,
			},
			BotID:       cfg.BotID,
			ReplyTarget: m.ChannelID,
			Sender: channel.Identity{
				SubjectID:   m.Author.ID,
				DisplayName: m.Author.Username,
				Attributes: map[string]string{
					"user_id":  m.Author.ID,
					"username": m.Author.Username,
				},
			},
			Conversation: channel.Conversation{
				ID:   m.ChannelID,
				Type: chatType,
			},
			ReceivedAt: time.Now().UTC(),
			Source:     "discord",
			Metadata: map[string]any{
				"guild_id":        m.GuildID,
				"is_mentioned":    isMentioned,
				"is_reply_to_bot": isReplyToBot,
				"bot_alias":       strings.TrimSpace(botID),
				"raw_text":        rawText,
			},
		}

		if a.logger != nil {
			a.logger.Info("inbound received",
				slog.String("config_id", cfg.ID),
				slog.String("chat_type", chatType),
				slog.String("user_id", m.Author.ID),
				slog.String("username", m.Author.Username),
				slog.String("text", common.SummarizeText(text)),
			)
		}

		go func() {
			if err := handler(ctx, cfg, msg); err != nil && a.logger != nil {
				a.logger.Error("handle inbound failed", slog.String("config_id", cfg.ID), slog.Any("error", err))
			}
		}()
	})

	sessionKey := discordSessionCacheKey(cfg.ID, discordCfg.BotToken)
	a.swapHandlerRemover(sessionKey, remove)

	if err := session.Open(); err != nil {
		return nil, fmt.Errorf("discord open connection: %w", err)
	}

	stop := func(_ context.Context) error {
		if a.logger != nil {
			a.logger.Info("stop", slog.String("config_id", cfg.ID))
		}
		remove := a.clearSessionState(sessionKey)
		if remove != nil {
			remove()
		}
		return session.Close()
	}

	return channel.NewConnection(cfg, stop), nil
}

func (a *DiscordAdapter) Send(ctx context.Context, cfg channel.ChannelConfig, msg channel.PreparedOutboundMessage) error {
	discordCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		return err
	}

	session, err := a.getOrCreateSession(discordCfg.BotToken, cfg.ID)
	if err != nil {
		return err
	}

	channelID := strings.TrimSpace(msg.Target)
	if channelID == "" {
		return errors.New("discord target is required")
	}
	return sendDiscordMessage(ctx, session, channelID, msg)
}

func (*DiscordAdapter) ValidatePreparedOutbound(_ context.Context, _ channel.ChannelConfig, _ string, msg channel.PreparedOutboundMessage) error {
	return validateDiscordPreparedOutbound(msg)
}

func sendDiscordMessage(ctx context.Context, session *discordgo.Session, channelID string, msg channel.PreparedOutboundMessage) error {
	if err := validateDiscordPreparedOutbound(msg); err != nil {
		return err
	}
	body := renderDiscordMessagePartsContent(msg.Message.Message)
	if body == "" {
		body = msg.Message.Message.Text
	}
	content := truncateDiscordText(body)

	// Build message send parameters
	messageSend := &discordgo.MessageSend{
		Content:         content,
		AllowedMentions: discordAllowedMentionsForMessage(msg.Message.Message),
	}
	if len(msg.Message.Message.Actions) > 0 {
		components, err := discordURLActionComponents(msg.Message.Message.Actions)
		if err != nil {
			return err
		}
		messageSend.Components = components
	}

	if msg.Message.Message.Reply != nil && msg.Message.Message.Reply.MessageID != "" {
		messageSend.Reference = &discordgo.MessageReference{
			ChannelID: channelID,
			MessageID: msg.Message.Message.Reply.MessageID,
		}
	}

	// Add attachments if present
	if len(msg.Message.Attachments) > 0 {
		files := make([]*discordgo.File, 0, len(msg.Message.Attachments))
		for _, att := range msg.Message.Attachments {
			file, err := discordPreparedAttachmentToFile(ctx, att)
			if err != nil {
				return err
			}
			files = append(files, file)
		}
		messageSend.Files = files

		// Discord requires non-empty content when sending files only
		if messageSend.Content == "" && len(messageSend.Files) > 0 {
			messageSend.Content = "\u200b"
		}
	}

	// Validate: must have content or files
	if messageSend.Content == "" && len(messageSend.Files) == 0 && len(messageSend.Components) == 0 {
		return errors.New("cannot send empty message: no content and no valid attachments")
	}

	_, err := session.ChannelMessageSendComplex(channelID, messageSend)
	return err
}

func validateDiscordPreparedOutbound(msg channel.PreparedOutboundMessage) error {
	body := renderDiscordMessagePartsContent(msg.Message.Message)
	if body == "" {
		body = msg.Message.Message.Text
	}
	content := truncateDiscordText(body)
	components := []discordgo.MessageComponent(nil)
	if len(msg.Message.Message.Actions) > 0 {
		var err error
		components, err = discordURLActionComponents(msg.Message.Message.Actions)
		if err != nil {
			return err
		}
	}
	if len(msg.Message.Attachments) > 0 {
		for _, att := range msg.Message.Attachments {
			if att.Kind != channel.PreparedAttachmentUpload {
				return fmt.Errorf("discord attachment requires upload source, got %s", att.Kind)
			}
			if att.Open == nil {
				return errors.New("discord attachment upload is not openable")
			}
		}
		return nil
	}
	if content == "" && len(components) == 0 {
		return errors.New("cannot send empty message: no content and no valid attachments")
	}
	return nil
}

func discordURLActionComponents(actions []channel.Action) ([]discordgo.MessageComponent, error) {
	if len(actions) == 0 {
		return nil, nil
	}
	if len(actions) > discordMaxURLActionButtons {
		return nil, fmt.Errorf("discord actions support at most %d url buttons", discordMaxURLActionButtons)
	}
	rows := make([]discordgo.MessageComponent, 0, (len(actions)+4)/5)
	buttons := make([]discordgo.MessageComponent, 0, 5)
	flush := func() {
		if len(buttons) == 0 {
			return
		}
		rows = append(rows, discordgo.ActionsRow{Components: buttons})
		buttons = make([]discordgo.MessageComponent, 0, 5)
	}
	for _, action := range actions {
		label := strings.TrimSpace(action.Label)
		rawURL := strings.TrimSpace(action.URL)
		if strings.TrimSpace(action.Value) != "" || rawURL == "" {
			return nil, errors.New("discord actions support url buttons only")
		}
		if !channel.IsHTTPURL(rawURL) {
			return nil, errors.New("discord action url must be http(s)")
		}
		if label == "" {
			label = rawURL
		}
		if utf8.RuneCountInString(rawURL) > discordMaxURLActionURL {
			return nil, fmt.Errorf("discord action url must be at most %d characters", discordMaxURLActionURL)
		}
		buttons = append(buttons, discordgo.Button{
			Label: truncateDiscordTextRunes(label, discordMaxURLActionLabel),
			Style: discordgo.LinkButton,
			URL:   rawURL,
		})
		if len(buttons) == 5 {
			flush()
		}
	}
	flush()
	return rows, nil
}

func discordAllowedMentionsNone() *discordgo.MessageAllowedMentions {
	return &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}
}

func discordAllowedMentionsForMessage(msg channel.Message) *discordgo.MessageAllowedMentions {
	allowed := discordAllowedMentionsNone()
	allowed.Users = discordMentionUserIDs(msg.Parts)
	return allowed
}

func discordMentionUserIDs(parts []channel.MessagePart) []string {
	if len(parts) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	ids := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Type != channel.MessagePartMention {
			continue
		}
		id := strings.TrimSpace(part.ChannelIdentityID)
		if !isSafeDiscordMentionID(id) {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
		if len(ids) == discordMaxAllowedMentions {
			break
		}
	}
	return ids
}

func truncateDiscordText(text string) string {
	if utf8.RuneCountInString(text) <= discordMaxLength {
		return text
	}
	runes := []rune(text)
	return string(runes[:discordMaxLength-3]) + "..."
}

func truncateDiscordTextRunes(text string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

// discordPreparedAttachmentToFile converts a prepared attachment to discordgo.File.
func discordPreparedAttachmentToFile(ctx context.Context, att channel.PreparedAttachment) (*discordgo.File, error) {
	// Get file name
	name := att.Name
	if name == "" {
		name = "attachment"
		ext := mimeExtension(att.Mime)
		if ext != "" {
			name += ext
		}
	}

	if att.Kind != channel.PreparedAttachmentUpload {
		return nil, fmt.Errorf("discord attachment requires upload source, got %s", att.Kind)
	}
	if att.Open == nil {
		return nil, errors.New("discord attachment upload is not openable")
	}
	reader, err := att.Open(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	data, err := media.ReadAllWithLimit(reader, media.MaxAssetBytes)
	if err != nil {
		return nil, err
	}
	return &discordgo.File{
		Name:   name,
		Reader: bytes.NewReader(data),
	}, nil
}

// mimeExtension returns file extension for common mime types.
func mimeExtension(mime string) string {
	switch mime {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/ogg":
		return ".ogg"
	case "audio/wav":
		return ".wav"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	default:
		return ""
	}
}

func (a *DiscordAdapter) OpenStream(_ context.Context, cfg channel.ChannelConfig, target string, opts channel.StreamOptions) (channel.PreparedOutboundStream, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, errors.New("discord target is required")
	}

	discordCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		return nil, err
	}

	session, err := a.getOrCreateSession(discordCfg.BotToken, cfg.ID)
	if err != nil {
		return nil, err
	}

	return &discordOutboundStream{
		adapter: a,
		cfg:     cfg,
		target:  target,
		reply:   opts.Reply,
		session: session,
	}, nil
}

func (a *DiscordAdapter) ProcessingStarted(_ context.Context, cfg channel.ChannelConfig, _ channel.InboundMessage, info channel.ProcessingStatusInfo) (channel.ProcessingStatusHandle, error) {
	chatID := strings.TrimSpace(info.ReplyTarget)
	if chatID == "" {
		return channel.ProcessingStatusHandle{}, nil
	}

	discordCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		return channel.ProcessingStatusHandle{}, err
	}

	session, err := a.getOrCreateSession(discordCfg.BotToken, cfg.ID)
	if err != nil {
		return channel.ProcessingStatusHandle{}, err
	}

	// Discord typing indicator
	err = session.ChannelTyping(chatID)
	return channel.ProcessingStatusHandle{}, err
}

func (*DiscordAdapter) ProcessingCompleted(_ context.Context, _ channel.ChannelConfig, _ channel.InboundMessage, _ channel.ProcessingStatusInfo, _ channel.ProcessingStatusHandle) error {
	return nil
}

func (*DiscordAdapter) ProcessingFailed(_ context.Context, _ channel.ChannelConfig, _ channel.InboundMessage, _ channel.ProcessingStatusInfo, _ channel.ProcessingStatusHandle, _ error) error {
	return nil
}

func (a *DiscordAdapter) React(_ context.Context, cfg channel.ChannelConfig, target string, messageID string, emoji string) error {
	discordCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		return err
	}

	session, err := a.getOrCreateSession(discordCfg.BotToken, cfg.ID)
	if err != nil {
		return err
	}

	return session.MessageReactionAdd(target, messageID, emoji)
}

func (a *DiscordAdapter) Unreact(_ context.Context, cfg channel.ChannelConfig, target string, messageID string, emoji string) error {
	discordCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		return err
	}

	session, err := a.getOrCreateSession(discordCfg.BotToken, cfg.ID)
	if err != nil {
		return err
	}

	return session.MessageReactionRemove(target, messageID, emoji, "@me")
}

func (*DiscordAdapter) NormalizeConfig(raw map[string]any) (map[string]any, error) {
	return normalizeConfig(raw)
}

func (*DiscordAdapter) NormalizeUserConfig(raw map[string]any) (map[string]any, error) {
	return normalizeUserConfig(raw)
}

func (*DiscordAdapter) NormalizeTarget(raw string) string {
	return normalizeTarget(raw)
}

func (*DiscordAdapter) ResolveTarget(userConfig map[string]any) (string, error) {
	return resolveTarget(userConfig)
}

func (*DiscordAdapter) MatchBinding(config map[string]any, criteria channel.BindingCriteria) bool {
	return matchBinding(config, criteria)
}

func (*DiscordAdapter) BuildUserConfig(identity channel.Identity) map[string]any {
	return buildUserConfig(identity)
}

func (*DiscordAdapter) collectAttachments(msg *discordgo.Message) []channel.Attachment {
	if msg == nil || len(msg.Attachments) == 0 {
		return nil
	}

	attachments := make([]channel.Attachment, 0, len(msg.Attachments))
	for _, att := range msg.Attachments {
		attachment := channel.Attachment{
			Type:           channel.AttachmentFile,
			URL:            att.URL,
			PlatformKey:    att.ID,
			SourcePlatform: Type.String(),
			Name:           att.Filename,
			Size:           int64(att.Size),
		}

		if att.ContentType != "" {
			switch {
			case strings.HasPrefix(att.ContentType, "image/"):
				attachment.Type = channel.AttachmentImage
				attachment.Width = att.Width
				attachment.Height = att.Height
			case strings.HasPrefix(att.ContentType, "video/"):
				attachment.Type = channel.AttachmentVideo
			case strings.HasPrefix(att.ContentType, "audio/"):
				attachment.Type = channel.AttachmentAudio
			}
		}

		attachments = append(attachments, attachment)
	}

	return attachments
}

func (*DiscordAdapter) isBotMentioned(msg *discordgo.Message, botID string) bool {
	if msg == nil {
		return false
	}

	for _, mention := range msg.Mentions {
		if mention != nil && mention.ID == botID {
			return true
		}
	}

	if msg.MentionEveryone {
		return true
	}

	botMention := "<@" + botID + ">"
	botNickMention := "<@!" + botID + ">"
	content := strings.ToLower(msg.Content)
	return strings.Contains(content, strings.ToLower(botMention)) ||
		strings.Contains(content, strings.ToLower(botNickMention))
}

func (a *DiscordAdapter) isDuplicateInbound(configID, messageID string) bool {
	if strings.TrimSpace(configID) == "" || strings.TrimSpace(messageID) == "" {
		return false
	}

	now := time.Now().UTC()
	expireBefore := now.Add(-inboundDedupTTL)

	a.mu.Lock()
	defer a.mu.Unlock()

	for key, seenAt := range a.seenMessages {
		if seenAt.Before(expireBefore) {
			delete(a.seenMessages, key)
		}
	}

	seenKey := configID + ":" + messageID
	if _, ok := a.seenMessages[seenKey]; ok {
		return true
	}
	a.seenMessages[seenKey] = now
	return false
}

func (a *DiscordAdapter) swapHandlerRemover(sessionKey string, remove func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if oldRemove := a.handlerRemovers[sessionKey]; oldRemove != nil {
		oldRemove()
	}
	a.handlerRemovers[sessionKey] = remove
}

func (a *DiscordAdapter) clearSessionState(sessionKey string) func() {
	a.mu.Lock()
	defer a.mu.Unlock()
	remove := a.handlerRemovers[sessionKey]
	delete(a.handlerRemovers, sessionKey)
	delete(a.sessions, sessionKey)
	return remove
}
