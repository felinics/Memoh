package slack

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

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/common"
	"github.com/felinics/memoh/internal/media"
)

const (
	inboundDedupTTL             = time.Minute
	slackMaxLength              = 40000
	slackMaxMessageBlocks       = 50
	slackMaxActionBlockElements = 25
	slackMaxSectionText         = 3000
	slackMaxButtonText          = 75
	slackMaxButtonURL           = 3000
	channelNameTTL              = 5 * time.Minute
)

// assetOpener reads stored asset bytes by content hash.
type assetOpener interface {
	Open(ctx context.Context, botID, contentHash string) (io.ReadCloser, media.Asset, error)
}

type slackConnection struct {
	api    *slack.Client
	sm     *socketmode.Client
	cancel context.CancelFunc
}

type cachedSlackChannelName struct {
	name     string
	chatType string
	cachedAt time.Time
}

type cachedSlackUserName struct {
	displayName string
	cachedAt    time.Time
}

type SlackAdapter struct {
	logger           *slog.Logger
	mu               sync.RWMutex
	connections      map[string]*slackConnection       // keyed by config ID
	seenMessages     map[string]time.Time              // keyed by configID:messageTS
	channelNames     map[string]cachedSlackChannelName // keyed by configID:channelID
	userNames        map[string]cachedSlackUserName    // keyed by configID:userID
	assets           assetOpener
	apiFactory       func(Config, ...slack.Option) *slack.Client
	authTest         func(*slack.Client) (*slack.AuthTestResponse, error)
	openConversation func(context.Context, *slack.Client, *slack.OpenConversationParameters) (*slack.Channel, bool, bool, error)
	socketOpen       func(Config) (*slack.Client, *socketmode.Client)
	socketRun        func(context.Context, *socketmode.Client) error
	historyFetch     func(context.Context, *slack.Client, *slack.GetConversationHistoryParameters) (*slack.GetConversationHistoryResponse, error)
}

var (
	_ channel.Sender                   = (*SlackAdapter)(nil)
	_ channel.StreamSender             = (*SlackAdapter)(nil)
	_ channel.Reactor                  = (*SlackAdapter)(nil)
	_ channel.Receiver                 = (*SlackAdapter)(nil)
	_ channel.AttachmentResolver       = (*SlackAdapter)(nil)
	_ channel.SelfDiscoverer           = (*SlackAdapter)(nil)
	_ channel.ConfigNormalizer         = (*SlackAdapter)(nil)
	_ channel.TargetResolver           = (*SlackAdapter)(nil)
	_ channel.BindingMatcher           = (*SlackAdapter)(nil)
	_ channel.ProcessingStatusNotifier = (*SlackAdapter)(nil)
)

func NewSlackAdapter(log *slog.Logger) *SlackAdapter {
	if log == nil {
		log = slog.Default()
	}
	return &SlackAdapter{
		logger:       log.With(slog.String("adapter", "slack")),
		connections:  make(map[string]*slackConnection),
		seenMessages: make(map[string]time.Time),
		channelNames: make(map[string]cachedSlackChannelName),
		userNames:    make(map[string]cachedSlackUserName),
		apiFactory: func(cfg Config, options ...slack.Option) *slack.Client {
			opts := []slack.Option{
				slack.OptionRetry(3),
			}
			opts = append(opts, options...)
			return slack.New(cfg.BotToken, opts...)
		},
		authTest: func(api *slack.Client) (*slack.AuthTestResponse, error) {
			return api.AuthTest()
		},
		openConversation: func(ctx context.Context, api *slack.Client, params *slack.OpenConversationParameters) (*slack.Channel, bool, bool, error) {
			return api.OpenConversationContext(ctx, params)
		},
		socketOpen: newSocketModeClient,
		socketRun: func(ctx context.Context, sm *socketmode.Client) error {
			return sm.RunContext(ctx)
		},
		historyFetch: func(ctx context.Context, api *slack.Client, params *slack.GetConversationHistoryParameters) (*slack.GetConversationHistoryResponse, error) {
			return api.GetConversationHistoryContext(ctx, params)
		},
	}
}

// SetAssetOpener configures the asset opener for reading stored attachments by content hash.
func (a *SlackAdapter) SetAssetOpener(opener assetOpener) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.assets = opener
}

func (*SlackAdapter) Type() channel.ChannelType {
	return Type
}

func (*SlackAdapter) Descriptor() channel.Descriptor {
	return channel.Descriptor{
		Type:        Type,
		DisplayName: "Slack",
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
			Threads:        true,
		},
		OutboundPolicy: channel.OutboundPolicy{
			RichTextChunkLimit: slackMaxLength,
		},
		ConfigSchema: channel.ConfigSchema{
			Version: 1,
			Fields: map[string]channel.FieldSchema{
				"botToken": {
					Type:        channel.FieldSecret,
					Required:    true,
					Title:       "Bot Token",
					Description: "Slack Bot User OAuth Token (xoxb-...)",
				},
				"appToken": {
					Type:        channel.FieldSecret,
					Required:    true,
					Title:       "App-Level Token",
					Description: "Slack App-Level Token for Socket Mode (xapp-...)",
				},
			},
		},
		UserConfigSchema: channel.ConfigSchema{
			Version: 1,
			Fields: map[string]channel.FieldSchema{
				"user_id":    {Type: channel.FieldString},
				"channel_id": {Type: channel.FieldString},
				"username":   {Type: channel.FieldString},
			},
		},
		TargetSpec: channel.TargetSpec{
			Format: "channel_id | user_id",
			Hints: []channel.TargetHint{
				{Label: "Channel ID", Example: "C0123456789"},
				{Label: "User ID", Example: "U0123456789"},
			},
		},
	}
}

func (a *SlackAdapter) newAPIClient(cfg Config, options ...slack.Option) *slack.Client {
	if a != nil && a.apiFactory != nil {
		return a.apiFactory(cfg, options...)
	}
	opts := []slack.Option{
		slack.OptionRetry(3),
	}
	opts = append(opts, options...)
	return slack.New(cfg.BotToken, opts...)
}

func newSocketModeClient(cfg Config) (*slack.Client, *socketmode.Client) {
	api := slack.New(
		cfg.BotToken,
		slack.OptionRetry(3),
		slack.OptionAppLevelToken(cfg.AppToken),
	)
	return api, socketmode.New(api)
}

func (a *SlackAdapter) getOrCreateConnection(channelCfg channel.ChannelConfig, cfg Config) (*slackConnection, error) {
	a.mu.RLock()
	conn, ok := a.connections[channelCfg.ID]
	a.mu.RUnlock()
	if ok {
		return conn, nil
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if c, ok := a.connections[channelCfg.ID]; ok {
		return c, nil
	}

	socketOpen := a.socketOpen
	if socketOpen == nil {
		socketOpen = newSocketModeClient
	}
	api, sm := socketOpen(cfg)

	conn = &slackConnection{
		api: api,
		sm:  sm,
	}
	a.connections[channelCfg.ID] = conn
	return conn, nil
}

func (a *SlackAdapter) Connect(ctx context.Context, cfg channel.ChannelConfig, handler channel.InboundHandler) (channel.Connection, error) {
	if a.logger != nil {
		a.logger.Info("start", slog.String("config_id", cfg.ID))
	}

	slackCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		return nil, err
	}

	conn, err := a.getOrCreateConnection(cfg, slackCfg)
	if err != nil {
		return nil, err
	}

	// Discover self identity for filtering bot's own messages
	authTest := a.authTest
	if authTest == nil {
		authTest = func(api *slack.Client) (*slack.AuthTestResponse, error) {
			return api.AuthTest()
		}
	}
	authResp, err := authTest(conn.api)
	if err != nil {
		a.clearConnection(cfg.ID)
		return nil, fmt.Errorf("slack auth test: %w", err)
	}
	selfUserID := authResp.UserID

	smCtx, cancel := context.WithCancel(ctx)
	conn.cancel = cancel
	connectedCh := make(chan struct{})
	startErrCh := make(chan error, 1)
	var startupOnce sync.Once
	signalConnected := func() {
		startupOnce.Do(func() {
			close(connectedCh)
		})
	}
	signalStartupError := func(err error) {
		if err == nil {
			err = errors.New("slack socket mode startup failed")
		}
		select {
		case startErrCh <- err:
		default:
		}
	}

	go func() {
		for {
			select {
			case <-smCtx.Done():
				return
			case evt, ok := <-conn.sm.Events:
				if !ok {
					return
				}
				switch evt.Type {
				case socketmode.EventTypeConnected:
					signalConnected()
				case socketmode.EventTypeInvalidAuth:
					signalStartupError(errors.New("slack socket mode invalid auth"))
				case socketmode.EventTypeConnectionError:
					if connErr, ok := evt.Data.(*slack.ConnectionErrorEvent); ok && connErr != nil && connErr.ErrorObj != nil {
						signalStartupError(fmt.Errorf("slack socket mode connect: %w", connErr.ErrorObj))
					} else {
						signalStartupError(errors.New("slack socket mode connect failed"))
					}
				}
				a.handleSocketModeEvent(smCtx, conn, evt, cfg, handler, selfUserID)
			}
		}
	}()

	go func() {
		socketRun := a.socketRun
		if socketRun == nil {
			socketRun = func(ctx context.Context, sm *socketmode.Client) error {
				return sm.RunContext(ctx)
			}
		}
		if err := socketRun(smCtx, conn.sm); err != nil {
			if !errors.Is(err, context.Canceled) {
				signalStartupError(fmt.Errorf("slack socket mode run: %w", err))
			}
			if a.logger != nil && !errors.Is(err, context.Canceled) {
				a.logger.Error("socket mode run error", slog.String("config_id", cfg.ID), slog.Any("error", err))
			}
		}
	}()

	select {
	case <-connectedCh:
	case err := <-startErrCh:
		cancel()
		a.clearConnection(cfg.ID)
		return nil, err
	case <-ctx.Done():
		cancel()
		a.clearConnection(cfg.ID)
		return nil, ctx.Err()
	}

	stop := func(_ context.Context) error {
		if a.logger != nil {
			a.logger.Info("stop", slog.String("config_id", cfg.ID))
		}
		cancel()
		a.clearConnection(cfg.ID)
		return nil
	}

	return channel.NewConnection(cfg, stop), nil
}

func (a *SlackAdapter) handleSocketModeEvent(
	ctx context.Context,
	conn *slackConnection,
	evt socketmode.Event,
	cfg channel.ChannelConfig,
	handler channel.InboundHandler,
	selfUserID string,
) {
	switch evt.Type {
	case socketmode.EventTypeEventsAPI:
		eventsAPIEvent, ok := evt.Data.(slackevents.EventsAPIEvent)
		if !ok {
			return
		}
		if evt.Request != nil {
			conn.sm.Ack(*evt.Request)
		}

		if eventsAPIEvent.Type != slackevents.CallbackEvent {
			return
		}

		switch ev := eventsAPIEvent.InnerEvent.Data.(type) {
		case *slackevents.MessageEvent:
			a.handleMessageEvent(ctx, conn, ev, cfg, handler, selfUserID)
		case *slackevents.AppMentionEvent:
			a.handleAppMentionEvent(ctx, conn, ev, cfg, handler, selfUserID)
		}

	case socketmode.EventTypeConnecting:
		if a.logger != nil {
			a.logger.Info("connecting to Slack Socket Mode", slog.String("config_id", cfg.ID))
		}

	case socketmode.EventTypeConnected:
		if a.logger != nil {
			a.logger.Info("connected to Slack Socket Mode", slog.String("config_id", cfg.ID))
		}

	case socketmode.EventTypeConnectionError:
		if a.logger != nil {
			a.logger.Error("Slack Socket Mode connection error", slog.String("config_id", cfg.ID))
		}

	case socketmode.EventTypeInteractive:
		if evt.Request != nil {
			conn.sm.Ack(*evt.Request)
		}

	case socketmode.EventTypeSlashCommand:
		if evt.Request != nil {
			conn.sm.Ack(*evt.Request)
		}
	}
}

func (a *SlackAdapter) handleMessageEvent(
	ctx context.Context,
	conn *slackConnection,
	ev *slackevents.MessageEvent,
	cfg channel.ChannelConfig,
	handler channel.InboundHandler,
	selfUserID string,
) {
	if ev.BotID != "" || ev.User == "" || ev.User == selfUserID {
		return
	}

	// Skip message subtypes that aren't regular messages
	if ev.SubType != "" && ev.SubType != "file_share" {
		return
	}

	text := strings.TrimSpace(ev.Text)
	attachments := a.collectAttachments(ev.Message)
	if text == "" && len(attachments) == 0 {
		return
	}

	if a.isDuplicateInbound(cfg.ID, ev.TimeStamp) {
		return
	}

	chatType := channel.ConversationTypeGroup
	switch ev.ChannelType {
	case "im":
		chatType = channel.ConversationTypePrivate
	case "mpim":
		chatType = channel.ConversationTypeGroup
	case "group":
		chatType = channel.ConversationTypeGroup
	}

	// Resolve user display name
	displayName := a.resolveUserDisplayName(conn.api, cfg.ID, ev.User)

	isMentioned := strings.Contains(ev.Text, "<@"+selfUserID+">")

	threadID := ev.ThreadTimeStamp
	if ev.Message != nil && strings.TrimSpace(ev.Message.ThreadTimestamp) != "" {
		threadID = strings.TrimSpace(ev.Message.ThreadTimestamp)
	}
	parentUserID := ""
	if ev.Message != nil {
		parentUserID = strings.TrimSpace(ev.Message.ParentUserId)
	}
	// Slack reply semantics are thread-level: ParentUserId is the author of
	// the thread's root message. A reply inside a thread the bot started is
	// directed at the bot; without this flag, plain-text ask_user replies in
	// channels are silently dropped by isDirectedAtBot.
	isReplyToBot := parentUserID != "" && parentUserID == strings.TrimSpace(selfUserID)
	replyRef := buildSlackReplyRef(ev.Channel, ev.TimeStamp, threadID, parentUserID)
	conversationName, _ := a.lookupConversationInfo(ctx, conn.api, cfg.ID, ev.Channel)

	msg := channel.InboundMessage{
		Channel: Type,
		Message: channel.Message{
			ID:          ev.TimeStamp,
			Format:      channel.MessageFormatPlain,
			Text:        text,
			Attachments: attachments,
			Reply:       replyRef,
		},
		BotID:       cfg.BotID,
		ReplyTarget: ev.Channel,
		Sender: channel.Identity{
			SubjectID:   ev.User,
			DisplayName: displayName,
			Attributes:  slackIdentityAttributes(ev.User, "", ev.ChannelType, ev.Channel),
		},
		Conversation: channel.Conversation{
			ID:       ev.Channel,
			Type:     chatType,
			Name:     conversationName,
			ThreadID: threadID,
		},
		ReceivedAt: time.Now().UTC(),
		Source:     "slack",
		Metadata: map[string]any{
			"bot_alias":       strings.TrimSpace(selfUserID),
			"channel_type":    ev.ChannelType,
			"channel_name":    conversationName,
			"is_mentioned":    isMentioned,
			"is_reply_to_bot": isReplyToBot,
			"thread_ts":       threadID,
			"subtype":         ev.SubType,
		},
	}

	if a.logger != nil {
		a.logger.Info("inbound received",
			slog.String("config_id", cfg.ID),
			slog.String("chat_type", chatType),
			slog.String("user_id", ev.User),
			slog.String("text", common.SummarizeText(text)),
		)
	}

	go func() {
		if err := handler(ctx, cfg, msg); err != nil && a.logger != nil {
			a.logger.Error("handle inbound failed", slog.String("config_id", cfg.ID), slog.Any("error", err))
		}
	}()
}

func (a *SlackAdapter) handleAppMentionEvent(
	ctx context.Context,
	conn *slackConnection,
	ev *slackevents.AppMentionEvent,
	cfg channel.ChannelConfig,
	handler channel.InboundHandler,
	selfUserID string,
) {
	if ev.BotID != "" || ev.User == "" {
		return
	}

	text := strings.TrimSpace(ev.Text)
	if text == "" {
		return
	}

	attachments := a.fetchMessageAttachments(ctx, conn.api, ev.Channel, ev.TimeStamp)

	if a.isDuplicateInbound(cfg.ID, ev.TimeStamp) {
		return
	}

	displayName := a.resolveUserDisplayName(conn.api, cfg.ID, ev.User)

	threadID := ev.ThreadTimeStamp
	conversationName, conversationType := a.lookupConversationInfo(ctx, conn.api, cfg.ID, ev.Channel)
	if conversationType == "" {
		conversationType = channel.ConversationTypeGroup
	}
	replyRef := buildSlackReplyRef(ev.Channel, ev.TimeStamp, threadID, "")

	msg := channel.InboundMessage{
		Channel: Type,
		Message: channel.Message{
			ID:          ev.TimeStamp,
			Format:      channel.MessageFormatPlain,
			Text:        text,
			Attachments: attachments,
			Reply:       replyRef,
		},
		BotID:       cfg.BotID,
		ReplyTarget: ev.Channel,
		Sender: channel.Identity{
			SubjectID:   ev.User,
			DisplayName: displayName,
			Attributes: map[string]string{
				"user_id": ev.User,
			},
		},
		Conversation: channel.Conversation{
			ID:       ev.Channel,
			Type:     conversationType,
			Name:     conversationName,
			ThreadID: threadID,
		},
		ReceivedAt: time.Now().UTC(),
		Source:     "slack",
		Metadata: map[string]any{
			"bot_alias":    strings.TrimSpace(selfUserID),
			"channel_name": conversationName,
			"is_mentioned": true,
			"thread_ts":    threadID,
		},
	}

	if a.logger != nil {
		a.logger.Info("app mention received",
			slog.String("config_id", cfg.ID),
			slog.String("user_id", ev.User),
			slog.String("text", common.SummarizeText(text)),
		)
	}

	go func() {
		if err := handler(ctx, cfg, msg); err != nil && a.logger != nil {
			a.logger.Error("handle inbound failed", slog.String("config_id", cfg.ID), slog.Any("error", err))
		}
	}()
}

func (a *SlackAdapter) Send(ctx context.Context, cfg channel.ChannelConfig, msg channel.PreparedOutboundMessage) error {
	slackCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		return err
	}
	api := a.newAPIClient(slackCfg)
	target, err := a.resolveOutboundTarget(ctx, api, msg.Target)
	if err != nil {
		return err
	}

	return a.sendSlackMessage(ctx, api, target, msg)
}

func (*SlackAdapter) ValidatePreparedOutbound(_ context.Context, _ channel.ChannelConfig, _ string, msg channel.PreparedOutboundMessage) error {
	return validateSlackPreparedOutbound(msg)
}

func (a *SlackAdapter) sendSlackMessage(ctx context.Context, api *slack.Client, channelID string, msg channel.PreparedOutboundMessage) error {
	if err := validateSlackPreparedOutbound(msg); err != nil {
		return err
	}
	body := renderSlackOutboundBody(msg.Message.Message)
	displayText := truncateSlackText(body.Text)
	blockText := truncateSlackText(body.BlockText)
	text := displayText
	if text == "" && len(msg.Message.Message.Actions) > 0 {
		text = truncateSlackText(slackURLActionFallbackText(msg.Message.Message.Actions))
	}
	threadTS := ""
	if msg.Message.Message.Reply != nil && msg.Message.Message.Reply.MessageID != "" {
		threadTS = msg.Message.Message.Reply.MessageID
	}

	opts := []slack.MsgOption{
		slack.MsgOptionText(text, false),
	}
	if body.DisableMarkdown {
		opts = append(opts, slack.MsgOptionDisableMarkdown())
	}
	if len(msg.Message.Message.Actions) > 0 {
		blocks, err := slackURLActionBlocks(blockText, msg.Message.Message.Actions)
		if err != nil {
			return err
		}
		if len(blocks) > 0 {
			opts = append(opts, slack.MsgOptionBlocks(blocks...))
		}
	}

	if threadTS != "" {
		opts = append(opts, slack.MsgOptionTS(threadTS))
	}

	if len(msg.Message.Attachments) > 0 {
		for _, att := range msg.Message.Attachments {
			if err := a.uploadPreparedAttachment(ctx, api, channelID, threadTS, att); err != nil {
				if a.logger != nil {
					a.logger.Error("upload attachment failed", slog.Any("error", err))
				}
				return err
			}
		}
	}

	if text == "" && len(msg.Message.Attachments) > 0 {
		return nil
	}

	if text == "" {
		return errors.New("cannot send empty message")
	}

	_, _, err := api.PostMessageContext(ctx, channelID, opts...)
	return err
}

func validateSlackPreparedOutbound(msg channel.PreparedOutboundMessage) error {
	body := renderSlackOutboundBody(msg.Message.Message)
	displayText := truncateSlackText(body.Text)
	blockText := truncateSlackText(body.BlockText)
	text := displayText
	if text == "" && len(msg.Message.Message.Actions) > 0 {
		text = truncateSlackText(slackURLActionFallbackText(msg.Message.Message.Actions))
	}
	if len(msg.Message.Message.Actions) > 0 {
		if _, err := slackURLActionBlocks(blockText, msg.Message.Message.Actions); err != nil {
			return err
		}
	}
	if text == "" && len(msg.Message.Attachments) > 0 {
		return nil
	}
	if text == "" {
		return errors.New("cannot send empty message")
	}
	return nil
}

type slackOutboundBody struct {
	Text            string
	BlockText       string
	DisableMarkdown bool
}

func renderSlackOutboundBody(msg channel.Message) slackOutboundBody {
	if rich := renderSlackMessagePartsMrkdwn(msg); rich != "" {
		if utf8.RuneCountInString(rich) <= slackMaxLength {
			return slackOutboundBody{
				Text:      rich,
				BlockText: rich,
			}
		}
		if plain := strings.TrimSpace(channel.RenderPartsAsPlain(msg.Parts)); plain != "" {
			return slackOutboundPlainBody(plain)
		}
		return slackOutboundBody{
			Text:      rich,
			BlockText: rich,
		}
	}
	text := msg.PlainText()
	if msg.Format == channel.MessageFormatPlain {
		return slackOutboundPlainBody(text)
	}
	return slackOutboundBody{
		Text:      text,
		BlockText: text,
	}
}

func slackOutboundPlainBody(text string) slackOutboundBody {
	return slackOutboundBody{
		Text:            text,
		BlockText:       slackEscapeMrkdwn(text),
		DisableMarkdown: true,
	}
}

func slackDefaultBody(text string) slackOutboundBody {
	return slackOutboundBody{
		Text:      text,
		BlockText: text,
	}
}

func slackURLActionFallbackText(actions []channel.Action) string {
	labels := make([]string, 0, len(actions))
	for _, action := range actions {
		label := strings.TrimSpace(action.Label)
		if label != "" {
			labels = append(labels, label)
		}
	}
	return strings.Join(labels, " / ")
}

func slackURLActionBlocks(text string, actions []channel.Action) ([]slack.Block, error) {
	if len(actions) == 0 {
		return nil, nil
	}
	blocks := make([]slack.Block, 0, 2)
	if strings.TrimSpace(text) != "" {
		blocks = append(blocks, slackSectionTextBlocks(text)...)
	}
	elements := make([]slack.BlockElement, 0, slackMaxActionBlockElements)
	actionBlockIndex := 0
	flushActions := func() {
		if len(elements) == 0 {
			return
		}
		blockID := "memoh_url_actions"
		if actionBlockIndex > 0 || len(actions) > slackMaxActionBlockElements {
			blockID = fmt.Sprintf("memoh_url_actions_%d", actionBlockIndex)
		}
		blocks = append(blocks, slack.NewActionBlock(blockID, elements...))
		elements = make([]slack.BlockElement, 0, slackMaxActionBlockElements)
		actionBlockIndex++
	}
	for i, action := range actions {
		label := strings.TrimSpace(action.Label)
		rawURL := strings.TrimSpace(action.URL)
		if strings.TrimSpace(action.Value) != "" || rawURL == "" {
			return nil, errors.New("slack actions support url buttons only")
		}
		if !channel.IsHTTPURL(rawURL) {
			return nil, errors.New("slack action url must be http(s)")
		}
		if utf8.RuneCountInString(rawURL) > slackMaxButtonURL {
			return nil, fmt.Errorf("slack action url must be at most %d characters", slackMaxButtonURL)
		}
		if label == "" {
			label = rawURL
		}
		button := slack.NewButtonBlockElement(
			fmt.Sprintf("memoh_url_%d", i),
			fmt.Sprintf("url_%d", i),
			slack.NewTextBlockObject("plain_text", truncateSlackRunes(label, slackMaxButtonText), false, false),
		).WithURL(rawURL)
		elements = append(elements, button)
		if len(elements) == slackMaxActionBlockElements {
			flushActions()
		}
	}
	flushActions()
	if len(blocks) > slackMaxMessageBlocks {
		return nil, fmt.Errorf("slack message blocks must be at most %d blocks", slackMaxMessageBlocks)
	}
	return blocks, nil
}

func slackSectionTextBlocks(text string) []slack.Block {
	chunks := channel.ChunkMarkdownText(text, slackMaxSectionText)
	blocks := make([]slack.Block, 0, len(chunks))
	for _, chunk := range chunks {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		blocks = append(blocks, slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", truncateSlackRunes(chunk, slackMaxSectionText), false, false),
			nil,
			nil,
		))
	}
	return blocks
}

func (*SlackAdapter) uploadPreparedAttachment(ctx context.Context, api *slack.Client, channelID string, threadTS string, att channel.PreparedAttachment) error {
	if att.Kind != channel.PreparedAttachmentUpload {
		return fmt.Errorf("slack attachment requires upload source, got %s", att.Kind)
	}
	if att.Open == nil {
		return errors.New("slack attachment upload is not openable")
	}

	reader, err := att.Open(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()

	data, err := media.ReadAllWithLimit(reader, media.MaxAssetBytes)
	if err != nil {
		return err
	}

	name := strings.TrimSpace(att.Name)
	if name == "" {
		name = "attachment"
		if ext := mimeExtension(strings.TrimSpace(att.Mime)); ext != "" {
			name += ext
		}
	}

	_, err = api.UploadFileContext(ctx, slack.UploadFileParameters{
		Channel:         channelID,
		ThreadTimestamp: threadTS,
		Filename:        name,
		Reader:          bytes.NewReader(data),
		FileSize:        len(data),
	})
	return err
}

func (a *SlackAdapter) ResolveAttachment(ctx context.Context, cfg channel.ChannelConfig, attachment channel.Attachment) (channel.AttachmentPayload, error) {
	slackCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		return channel.AttachmentPayload{}, err
	}

	return a.resolveAttachmentWithClient(ctx, a.newAPIClient(slackCfg), attachment)
}

func (*SlackAdapter) resolveAttachmentWithClient(ctx context.Context, api *slack.Client, attachment channel.Attachment) (channel.AttachmentPayload, error) {
	downloadURL := strings.TrimSpace(attachment.URL)
	if attachment.Size > media.MaxAssetBytes {
		return channel.AttachmentPayload{}, fmt.Errorf("%w: max %d bytes", media.ErrAssetTooLarge, media.MaxAssetBytes)
	}
	if downloadURL == "" {
		fileID := strings.TrimSpace(attachment.PlatformKey)
		if fileID == "" {
			return channel.AttachmentPayload{}, errors.New("slack attachment requires url or platform_key")
		}
		file, _, _, err := api.GetFileInfoContext(ctx, fileID, 0, 0)
		if err != nil {
			return channel.AttachmentPayload{}, fmt.Errorf("slack get file info: %w", err)
		}
		if file == nil {
			return channel.AttachmentPayload{}, errors.New("slack file info response is empty")
		}
		downloadURL = strings.TrimSpace(file.URLPrivateDownload)
		if downloadURL == "" {
			downloadURL = strings.TrimSpace(file.URLPrivate)
		}
		if strings.TrimSpace(attachment.Name) == "" {
			attachment.Name = strings.TrimSpace(file.Name)
		}
		if strings.TrimSpace(attachment.Mime) == "" {
			attachment.Mime = strings.TrimSpace(file.Mimetype)
		}
		if attachment.Size <= 0 {
			attachment.Size = int64(file.Size)
		}
		if attachment.Size > media.MaxAssetBytes {
			return channel.AttachmentPayload{}, fmt.Errorf("%w: max %d bytes", media.ErrAssetTooLarge, media.MaxAssetBytes)
		}
	}

	if downloadURL == "" {
		return channel.AttachmentPayload{}, errors.New("slack attachment download URL is empty")
	}

	reader, err := streamSlackAttachment(ctx, api, downloadURL, media.MaxAssetBytes)
	if err != nil {
		return channel.AttachmentPayload{}, err
	}

	return channel.AttachmentPayload{
		Reader: reader,
		Mime:   strings.TrimSpace(attachment.Mime),
		Name:   strings.TrimSpace(attachment.Name),
		Size:   attachment.Size,
	}, nil
}

func truncateSlackText(text string) string {
	if utf8.RuneCountInString(text) <= slackMaxLength {
		return text
	}
	runes := []rune(text)
	return string(runes[:slackMaxLength-3]) + "..."
}

func truncateSlackRunes(text string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

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

func (a *SlackAdapter) OpenStream(ctx context.Context, cfg channel.ChannelConfig, target string, opts channel.StreamOptions) (channel.PreparedOutboundStream, error) {
	target = strings.TrimSpace(target)
	slackCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		return nil, err
	}
	api := a.newAPIClient(slackCfg)
	target, err = a.resolveOutboundTarget(ctx, api, target)
	if err != nil {
		return nil, err
	}

	reply := opts.Reply
	if reply == nil && strings.TrimSpace(opts.SourceMessageID) != "" {
		reply = &channel.ReplyRef{
			Target:    target,
			MessageID: strings.TrimSpace(opts.SourceMessageID),
		}
	}

	return &slackOutboundStream{
		adapter: a,
		cfg:     cfg,
		target:  target,
		reply:   reply,
		api:     api,
	}, nil
}

func (*SlackAdapter) ProcessingStarted(_ context.Context, _ channel.ChannelConfig, _ channel.InboundMessage, _ channel.ProcessingStatusInfo) (channel.ProcessingStatusHandle, error) {
	// Slack does not have a public typing indicator API for bots
	return channel.ProcessingStatusHandle{}, nil
}

func (*SlackAdapter) ProcessingCompleted(_ context.Context, _ channel.ChannelConfig, _ channel.InboundMessage, _ channel.ProcessingStatusInfo, _ channel.ProcessingStatusHandle) error {
	return nil
}

func (*SlackAdapter) ProcessingFailed(_ context.Context, _ channel.ChannelConfig, _ channel.InboundMessage, _ channel.ProcessingStatusInfo, _ channel.ProcessingStatusHandle, _ error) error {
	return nil
}

func (a *SlackAdapter) React(ctx context.Context, cfg channel.ChannelConfig, target string, messageID string, emoji string) error {
	slackCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		return err
	}
	api := a.newAPIClient(slackCfg)
	target, err = a.resolveOutboundTarget(ctx, api, target)
	if err != nil {
		return err
	}

	emoji = resolveSlackEmoji(emoji)

	return api.AddReaction(emoji, slack.ItemRef{
		Channel:   target,
		Timestamp: messageID,
	})
}

func (a *SlackAdapter) Unreact(ctx context.Context, cfg channel.ChannelConfig, target string, messageID string, emoji string) error {
	slackCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		return err
	}
	api := a.newAPIClient(slackCfg)
	target, err = a.resolveOutboundTarget(ctx, api, target)
	if err != nil {
		return err
	}

	emoji = resolveSlackEmoji(emoji)

	return api.RemoveReaction(emoji, slack.ItemRef{
		Channel:   target,
		Timestamp: messageID,
	})
}

func (a *SlackAdapter) resolveOutboundTarget(ctx context.Context, api *slack.Client, target string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", errors.New("slack target is required")
	}
	if !strings.HasPrefix(target, "U") {
		return target, nil
	}
	openConversation := a.openConversation
	if openConversation == nil {
		openConversation = func(ctx context.Context, api *slack.Client, params *slack.OpenConversationParameters) (*slack.Channel, bool, bool, error) {
			return api.OpenConversationContext(ctx, params)
		}
	}
	conversation, _, _, err := openConversation(ctx, api, &slack.OpenConversationParameters{
		Users:    []string{target},
		ReturnIM: true,
	})
	if err != nil {
		return "", fmt.Errorf("slack open dm conversation: %w", err)
	}
	if conversation == nil || strings.TrimSpace(conversation.ID) == "" {
		return "", errors.New("slack open dm conversation returned empty channel")
	}
	return strings.TrimSpace(conversation.ID), nil
}

func (a *SlackAdapter) DiscoverSelf(_ context.Context, credentials map[string]any) (map[string]any, string, error) {
	cfg, err := parseConfig(credentials)
	if err != nil {
		return nil, "", err
	}

	api := a.newAPIClient(cfg)
	resp, err := api.AuthTest()
	if err != nil {
		return nil, "", fmt.Errorf("slack auth test: %w", err)
	}

	identity := map[string]any{
		"user_id":  resp.UserID,
		"bot_id":   resp.BotID,
		"team_id":  resp.TeamID,
		"username": resp.User,
		"team":     resp.Team,
	}

	return identity, resp.UserID, nil
}

func (*SlackAdapter) NormalizeConfig(raw map[string]any) (map[string]any, error) {
	return normalizeConfig(raw)
}

func (*SlackAdapter) NormalizeUserConfig(raw map[string]any) (map[string]any, error) {
	return normalizeUserConfig(raw)
}

func (*SlackAdapter) NormalizeTarget(raw string) string {
	return normalizeTarget(raw)
}

func (*SlackAdapter) ResolveTarget(userConfig map[string]any) (string, error) {
	return resolveTarget(userConfig)
}

func (*SlackAdapter) MatchBinding(config map[string]any, criteria channel.BindingCriteria) bool {
	return matchBinding(config, criteria)
}

func (*SlackAdapter) BuildUserConfig(identity channel.Identity) map[string]any {
	return buildUserConfig(identity)
}

func (a *SlackAdapter) isDuplicateInbound(token, messageTS string) bool {
	if strings.TrimSpace(token) == "" || strings.TrimSpace(messageTS) == "" {
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

	seenKey := token + ":" + messageTS
	if _, ok := a.seenMessages[seenKey]; ok {
		return true
	}
	a.seenMessages[seenKey] = now
	return false
}

func (a *SlackAdapter) clearConnection(appToken string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if conn, ok := a.connections[appToken]; ok {
		if conn.cancel != nil {
			conn.cancel()
		}
		delete(a.connections, appToken)
	}
}

func (a *SlackAdapter) resolveUserDisplayName(api *slack.Client, configID, userID string) string {
	configID = strings.TrimSpace(configID)
	userID = strings.TrimSpace(userID)
	if api == nil || configID == "" || userID == "" {
		return userID
	}
	cacheKey := configID + ":" + userID

	expireBefore := time.Now().UTC().Add(-channelNameTTL)

	a.mu.RLock()
	cached, ok := a.userNames[cacheKey]
	a.mu.RUnlock()
	if ok && cached.cachedAt.After(expireBefore) {
		return cached.displayName
	}

	userInfo, err := api.GetUserInfo(userID)
	if err != nil {
		return userID
	}
	displayName := strings.TrimSpace(userInfo.Profile.DisplayName)
	if displayName == "" {
		displayName = strings.TrimSpace(userInfo.RealName)
	}
	if displayName == "" {
		displayName = strings.TrimSpace(userInfo.Name)
	}
	if displayName == "" {
		displayName = userID
	}

	a.mu.Lock()
	a.userNames[cacheKey] = cachedSlackUserName{displayName: displayName, cachedAt: time.Now().UTC()}
	a.mu.Unlock()
	return displayName
}

func (*SlackAdapter) collectAttachments(msg *slack.Msg) []channel.Attachment {
	if msg == nil || len(msg.Files) == 0 {
		return nil
	}

	attachments := make([]channel.Attachment, 0, len(msg.Files))
	for _, file := range msg.Files {
		attachment := channel.Attachment{
			Type:           channel.AttachmentFile,
			PlatformKey:    strings.TrimSpace(file.ID),
			SourcePlatform: Type.String(),
			Name:           strings.TrimSpace(file.Name),
			Size:           int64(file.Size),
			Mime:           strings.TrimSpace(file.Mimetype),
		}

		switch {
		case strings.HasPrefix(file.Mimetype, "image/"):
			attachment.Type = channel.AttachmentImage
		case strings.HasPrefix(file.Mimetype, "video/"):
			attachment.Type = channel.AttachmentVideo
		case strings.HasPrefix(file.Mimetype, "audio/"):
			attachment.Type = channel.AttachmentAudio
		}

		attachments = append(attachments, attachment)
	}

	return attachments
}

func (a *SlackAdapter) fetchMessageAttachments(ctx context.Context, api *slack.Client, channelID string, ts string) []channel.Attachment {
	if api == nil || strings.TrimSpace(channelID) == "" || strings.TrimSpace(ts) == "" {
		return nil
	}
	historyFetch := a.historyFetch
	if historyFetch == nil {
		historyFetch = func(ctx context.Context, api *slack.Client, params *slack.GetConversationHistoryParameters) (*slack.GetConversationHistoryResponse, error) {
			return api.GetConversationHistoryContext(ctx, params)
		}
	}
	resp, err := historyFetch(ctx, api, &slack.GetConversationHistoryParameters{
		ChannelID: channelID,
		Oldest:    ts,
		Latest:    ts,
		Inclusive: true,
		Limit:     1,
	})
	if err != nil || resp == nil || len(resp.Messages) == 0 {
		return nil
	}
	msg := resp.Messages[0].Msg
	return a.collectAttachments(&msg)
}

func streamSlackAttachment(ctx context.Context, api *slack.Client, downloadURL string, maxBytes int64) (io.ReadCloser, error) {
	if api == nil {
		return nil, errors.New("slack client is required")
	}
	if maxBytes <= 0 {
		return nil, fmt.Errorf("%w: max %d bytes", media.ErrAssetTooLarge, maxBytes)
	}
	streamCtx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()

	go func() {
		writer := &limitedSlackPipeWriter{
			pipe:     pw,
			maxBytes: maxBytes,
		}
		err := api.GetFileContext(streamCtx, downloadURL, writer)
		if err == nil && writer.err != nil {
			err = writer.err
		}
		if err != nil {
			if errors.Is(err, media.ErrAssetTooLarge) {
				err = fmt.Errorf("%w: max %d bytes", media.ErrAssetTooLarge, maxBytes)
			} else {
				err = fmt.Errorf("slack download file: %w", err)
			}
			_ = pw.CloseWithError(err)
			return
		}
		_ = pw.Close()
	}()

	return &slackAttachmentStream{
		ReadCloser: pr,
		cancel:     cancel,
	}, nil
}

type slackAttachmentStream struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (s *slackAttachmentStream) Close() error {
	if s == nil {
		return nil
	}
	if s.cancel != nil {
		s.cancel()
	}
	if s.ReadCloser == nil {
		return nil
	}
	return s.ReadCloser.Close()
}

type limitedSlackPipeWriter struct {
	pipe     *io.PipeWriter
	maxBytes int64
	written  int64
	err      error
}

func (w *limitedSlackPipeWriter) Write(p []byte) (int, error) {
	if w == nil || w.pipe == nil {
		return 0, errors.New("pipe writer is required")
	}
	if w.maxBytes <= 0 {
		w.err = media.ErrAssetTooLarge
		return 0, w.err
	}
	remaining := w.maxBytes - w.written
	if remaining <= 0 {
		w.err = media.ErrAssetTooLarge
		return 0, w.err
	}
	if int64(len(p)) <= remaining {
		n, err := w.pipe.Write(p)
		w.written += int64(n)
		return n, err
	}
	allowed := p[:remaining]
	n, err := w.pipe.Write(allowed)
	w.written += int64(n)
	if err != nil {
		return n, err
	}
	w.err = media.ErrAssetTooLarge
	return n, w.err
}

func slackIdentityAttributes(userID, username, channelType, channelID string) map[string]string {
	attrs := map[string]string{}
	if value := strings.TrimSpace(userID); value != "" {
		attrs["user_id"] = value
	}
	if value := strings.TrimSpace(username); value != "" {
		attrs["username"] = value
	}
	if strings.TrimSpace(channelType) == "im" {
		if value := strings.TrimSpace(channelID); value != "" {
			attrs["channel_id"] = value
		}
	}
	return attrs
}

func buildSlackReplyRef(channelID, timestamp, threadTimestamp, parentUserID string) *channel.ReplyRef {
	threadTimestamp = strings.TrimSpace(threadTimestamp)
	if threadTimestamp == "" || threadTimestamp == strings.TrimSpace(timestamp) {
		return nil
	}
	ref := &channel.ReplyRef{
		Target:    strings.TrimSpace(channelID),
		MessageID: threadTimestamp,
	}
	if sender := strings.TrimSpace(parentUserID); sender != "" {
		ref.Sender = sender
	}
	return ref
}

func (a *SlackAdapter) lookupConversationName(ctx context.Context, api *slack.Client, configID, channelID string) string {
	name, _ := a.lookupConversationInfo(ctx, api, configID, channelID)
	return name
}

func (a *SlackAdapter) lookupConversationInfo(ctx context.Context, api *slack.Client, configID, channelID string) (string, string) {
	configID = strings.TrimSpace(configID)
	channelID = strings.TrimSpace(channelID)
	if api == nil || configID == "" || channelID == "" {
		return "", ""
	}

	cacheKey := configID + ":" + channelID
	expireBefore := time.Now().UTC().Add(-channelNameTTL)

	a.mu.RLock()
	cached, ok := a.channelNames[cacheKey]
	a.mu.RUnlock()
	if ok && cached.cachedAt.After(expireBefore) {
		return cached.name, cached.chatType
	}

	name, chatType, err := a.fetchConversationInfo(ctx, api, channelID)
	if err != nil {
		if a.logger != nil {
			a.logger.Debug("resolve slack conversation name failed",
				slog.String("channel_id", channelID),
				slog.Any("error", err),
			)
		}
		return "", ""
	}
	if name == "" && chatType == "" {
		return "", ""
	}

	a.mu.Lock()
	a.channelNames[cacheKey] = cachedSlackChannelName{name: name, chatType: chatType, cachedAt: time.Now().UTC()}
	a.mu.Unlock()
	return name, chatType
}

func (*SlackAdapter) fetchConversationInfo(ctx context.Context, api *slack.Client, channelID string) (string, string, error) {
	info, err := api.GetConversationInfoContext(ctx, &slack.GetConversationInfoInput{
		ChannelID: channelID,
	})
	if err != nil {
		return "", "", err
	}
	if info == nil {
		return "", "", nil
	}

	name := strings.TrimSpace(info.Name)
	if name == "" {
		name = strings.TrimSpace(info.NameNormalized)
	}
	chatType := channel.ConversationTypeGroup
	switch {
	case info.IsIM:
		chatType = channel.ConversationTypePrivate
	case info.IsMpIM:
		chatType = channel.ConversationTypeGroup
	case info.IsPrivate:
		chatType = channel.ConversationTypeGroup
	}
	return name, chatType, nil
}
