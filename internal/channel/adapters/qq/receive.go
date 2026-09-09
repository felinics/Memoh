package qq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/felinics/memoh/internal/channel"
)

const (
	qqIntentGuilds              = 1 << 0
	qqIntentGuildMembers        = 1 << 1
	qqIntentPublicGuildMessages = 1 << 30
	qqIntentGroupAndC2C         = 1 << 25
)

var qqIntentLevels = []int{
	qqIntentPublicGuildMessages | qqIntentGroupAndC2C,
	qqIntentPublicGuildMessages | qqIntentGuildMembers,
}

type MessageAttachment struct {
	ContentType string `json:"content_type"`
	FileName    string `json:"filename,omitempty"`
	Height      int    `json:"height,omitempty"`
	Width       int    `json:"width,omitempty"`
	Size        int64  `json:"size,omitempty"`
	URL         string `json:"url"`
	VoiceWavURL string `json:"voice_wav_url,omitempty"`
}

type MessageReference struct {
	MessageID string `json:"message_id,omitempty"`
}

type C2CAuthor struct {
	ID          string `json:"id,omitempty"`
	UnionOpenID string `json:"union_openid,omitempty"`
	UserOpenID  string `json:"user_openid"`
}

type GroupAuthor struct {
	ID           string `json:"id,omitempty"`
	MemberOpenID string `json:"member_openid"`
}

type GuildAuthor struct {
	ID       string `json:"id"`
	Username string `json:"username,omitempty"`
	Bot      bool   `json:"bot,omitempty"`
}

type C2CMessageEvent struct {
	Author           C2CAuthor           `json:"author"`
	Content          string              `json:"content"`
	ID               string              `json:"id"`
	Timestamp        string              `json:"timestamp"`
	Attachments      []MessageAttachment `json:"attachments,omitempty"`
	MessageReference *MessageReference   `json:"message_reference,omitempty"`
}

type GroupMessageEvent struct {
	Author           GroupAuthor         `json:"author"`
	Content          string              `json:"content"`
	ID               string              `json:"id"`
	Timestamp        string              `json:"timestamp"`
	GroupID          string              `json:"group_id,omitempty"`
	GroupOpenID      string              `json:"group_openid"`
	Attachments      []MessageAttachment `json:"attachments,omitempty"`
	MessageReference *MessageReference   `json:"message_reference,omitempty"`
}

type GuildMessageEvent struct {
	ID               string              `json:"id"`
	ChannelID        string              `json:"channel_id"`
	GuildID          string              `json:"guild_id,omitempty"`
	Content          string              `json:"content"`
	Timestamp        string              `json:"timestamp"`
	Author           GuildAuthor         `json:"author"`
	Attachments      []MessageAttachment `json:"attachments,omitempty"`
	MessageReference *MessageReference   `json:"message_reference,omitempty"`
}

type wsPayload struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d,omitempty"`
	S  int             `json:"s,omitempty"`
	T  string          `json:"t,omitempty"`
}

type InboundEvent struct {
	Type         string
	C2CMessage   *C2CMessageEvent
	GroupMessage *GroupMessageEvent
	GuildMessage *GuildMessageEvent
}

type gatewayWriter struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

type heartbeatHandle struct {
	cancel context.CancelFunc
	done   <-chan struct{}
}

func (w *gatewayWriter) WriteJSON(v any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(defaultWriteTimeout))
	return w.conn.WriteJSON(v)
}

func (a *QQAdapter) Connect(ctx context.Context, cfg channel.ChannelConfig, handler channel.InboundHandler) (channel.Connection, error) {
	parsed, err := parseConfig(cfg.Credentials)
	if err != nil {
		return nil, err
	}

	connCtx, cancel := context.WithCancel(ctx)
	go a.runReceiver(connCtx, cfg, parsed, handler)

	return channel.NewConnection(cfg, func(context.Context) error {
		cancel()
		return nil
	}), nil
}

func (a *QQAdapter) runReceiver(ctx context.Context, cfg channel.ChannelConfig, parsed Config, handler channel.InboundHandler) {
	backoffs := []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second}
	attempt := 0
	for ctx.Err() == nil {
		healthySession, err := a.serveConnection(ctx, cfg, parsed, handler)
		if err == nil || ctx.Err() != nil {
			return
		}
		if a.logger != nil {
			a.logger.Warn("qq receiver reconnect", slog.String("config_id", cfg.ID), slog.Any("error", err))
		}
		delay, nextAttempt := nextReconnectDelay(backoffs, attempt, healthySession)
		attempt = nextAttempt
		if !sleepContext(ctx, delay) {
			return
		}
	}
}

func (a *QQAdapter) serveConnection(ctx context.Context, cfg channel.ChannelConfig, parsed Config, handler channel.InboundHandler) (bool, error) {
	client := a.getOrCreateClient(cfg, parsed)
	gatewayURL, err := client.gatewayURL(ctx)
	if err != nil {
		return false, err
	}

	conn, resp, err := a.dialer.DialContext(ctx, gatewayURL, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(defaultReadTimeout))
	writer := &gatewayWriter{conn: conn}

	session := a.loadSession(cfg.ID)
	var heartbeatSeq atomic.Int64
	heartbeatSeq.Store(int64(session.LastSeq))
	healthySession := false

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	defer close(done)

	var heartbeat heartbeatHandle
	defer func() {
		if heartbeat.cancel != nil {
			heartbeat.cancel()
		}
	}()

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			var closeErr *websocket.CloseError
			if errors.As(err, &closeErr) {
				return a.handleGatewayClose(cfg.ID, client, &session, closeErr, healthySession)
			}
			return healthySession, err
		}
		_ = conn.SetReadDeadline(time.Now().Add(defaultReadTimeout))

		var payload wsPayload
		if err := json.Unmarshal(data, &payload); err != nil {
			return healthySession, fmt.Errorf("qq websocket payload decode: %w", err)
		}
		if payload.S > 0 {
			session.LastSeq = payload.S
			a.saveSession(cfg.ID, session)
			heartbeatSeq.Store(int64(payload.S))
		}

		switch payload.Op {
		case 10:
			if err := handleHello(ctx, writer, client, &session, payload.D); err != nil {
				return healthySession, err
			}
			if heartbeat.cancel != nil {
				heartbeat.cancel()
			}
			interval := parseHeartbeatInterval(payload.D)
			heartbeat = startHeartbeat(ctx, writer, interval, func() int {
				return int(heartbeatSeq.Load())
			})
		case 0:
			dispatchHealthy, err := a.handleDispatch(ctx, cfg, handler, payload.T, payload.D, &session)
			if err != nil {
				return healthySession, err
			}
			healthySession = healthySession || dispatchHealthy
		case 7:
			return healthySession, errors.New("qq gateway requested reconnect")
		case 9:
			a.adjustSessionAfterInvalid(cfg.ID, &session)
			return healthySession, errors.New("qq invalid session")
		case 11:
			continue
		}
	}
}

func (a *QQAdapter) handleGatewayClose(configID string, client *qqClient, session *sessionState, closeErr *websocket.CloseError, healthySession bool) (bool, error) {
	switch closeErr.Code {
	case 4004:
		if client != nil {
			client.clearToken()
		}
	case 4006, 4007, 4009:
		a.clearSession(configID)
	case 4914, 4915:
		a.adjustSessionAfterIntentClose(configID, session)
		return healthySession, fmt.Errorf("qq gateway closed with intent code %d", closeErr.Code)
	}
	return healthySession, closeErr
}

func handleHello(ctx context.Context, writer *gatewayWriter, client *qqClient, session *sessionState, _ json.RawMessage) error {
	token, err := client.accessToken(ctx)
	if err != nil {
		return err
	}
	if session.SessionID != "" && session.LastSeq > 0 {
		payload := map[string]any{
			"op": 6,
			"d": map[string]any{
				"token":      "QQBot " + token,
				"session_id": session.SessionID,
				"seq":        session.LastSeq,
			},
		}
		return writer.WriteJSON(payload)
	}
	intentLevel := session.IntentLevel
	if intentLevel < 0 || intentLevel >= len(qqIntentLevels) {
		intentLevel = 0
	}
	session.IntentLevel = intentLevel
	return writer.WriteJSON(map[string]any{
		"op": 2,
		"d": map[string]any{
			"token":   "QQBot " + token,
			"intents": qqIntentLevels[intentLevel],
			"shard":   []int{0, 1},
		},
	})
}

func (a *QQAdapter) handleDispatch(ctx context.Context, cfg channel.ChannelConfig, handler channel.InboundHandler, eventType string, raw json.RawMessage, session *sessionState) (bool, error) {
	switch eventType {
	case "READY":
		var ready struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(raw, &ready); err != nil {
			return false, err
		}
		session.SessionID = strings.TrimSpace(ready.SessionID)
		a.saveSession(cfg.ID, *session)
		return true, nil
	case "RESUMED":
		a.saveSession(cfg.ID, *session)
		return true, nil
	case "C2C_MESSAGE_CREATE":
		var event C2CMessageEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			return false, err
		}
		a.dispatchInbound(ctx, cfg, handler, InboundEvent{Type: eventType, C2CMessage: &event})
		return false, nil
	case "GROUP_AT_MESSAGE_CREATE":
		var event GroupMessageEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			return false, err
		}
		a.dispatchInbound(ctx, cfg, handler, InboundEvent{Type: eventType, GroupMessage: &event})
		return false, nil
	case "AT_MESSAGE_CREATE":
		var event GuildMessageEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			return false, err
		}
		a.dispatchInbound(ctx, cfg, handler, InboundEvent{Type: eventType, GuildMessage: &event})
		return false, nil
	default:
		return false, nil
	}
}

func (a *QQAdapter) dispatchInbound(ctx context.Context, cfg channel.ChannelConfig, handler channel.InboundHandler, inbound InboundEvent) {
	msg, ok := eventToInboundMessage(inbound, cfg.BotID)
	if !ok {
		return
	}
	go func() {
		if err := handler(ctx, cfg, msg); err != nil && a.logger != nil {
			a.logger.Error("qq handle inbound failed", slog.String("config_id", cfg.ID), slog.Any("error", err))
		}
	}()
}

func startHeartbeat(parent context.Context, writer *gatewayWriter, interval time.Duration, seqValue func() int) heartbeatHandle {
	ctx, cancel := context.WithCancel(parent)
	ticker := time.NewTicker(interval)
	done := make(chan struct{})
	go runHeartbeat(ctx, writer, ticker, seqValue, done)
	return heartbeatHandle{
		cancel: cancel,
		done:   done,
	}
}

func runHeartbeat(ctx context.Context, writer *gatewayWriter, ticker *time.Ticker, seqValue func() int, done chan<- struct{}) {
	defer ticker.Stop()
	if done != nil {
		defer close(done)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = writer.WriteJSON(map[string]any{
				"op": 1,
				"d":  seqValue(),
			})
		}
	}
}

func (a *QQAdapter) adjustSessionAfterInvalid(configID string, session *sessionState) {
	session.SessionID = ""
	session.LastSeq = 0
	a.saveSession(configID, *session)
}

func (a *QQAdapter) adjustSessionAfterIntentClose(configID string, session *sessionState) {
	session.SessionID = ""
	session.LastSeq = 0
	if last := len(qqIntentLevels) - 1; last >= 0 {
		switch {
		case session.IntentLevel < 0:
			session.IntentLevel = 0
		case session.IntentLevel < last:
			session.IntentLevel++
		default:
			session.IntentLevel = last
		}
	}
	a.saveSession(configID, *session)
}

func parseHeartbeatInterval(raw json.RawMessage) time.Duration {
	var hello struct {
		HeartbeatInterval int `json:"heartbeat_interval"`
	}
	if err := json.Unmarshal(raw, &hello); err != nil {
		return 30 * time.Second
	}
	if hello.HeartbeatInterval <= 0 {
		return 30 * time.Second
	}
	return time.Duration(hello.HeartbeatInterval) * time.Millisecond
}

func eventToInboundMessage(event InboundEvent, botID string) (channel.InboundMessage, bool) {
	switch event.Type {
	case "C2C_MESSAGE_CREATE":
		if event.C2CMessage == nil {
			return channel.InboundMessage{}, false
		}
		payload := event.C2CMessage
		subjectID := strings.TrimSpace(payload.Author.UserOpenID)
		if subjectID == "" {
			return channel.InboundMessage{}, false
		}
		return channel.InboundMessage{
			Channel: Type,
			BotID:   strings.TrimSpace(botID),
			Message: channel.Message{
				ID:          strings.TrimSpace(payload.ID),
				Format:      channel.MessageFormatPlain,
				Text:        parseFaceTags(strings.TrimSpace(payload.Content)),
				Attachments: toInboundAttachments(payload.Attachments),
				Reply:       qqReplyRef(payload.MessageReference, "c2c:"+subjectID),
			},
			ReplyTarget: "c2c:" + subjectID,
			Sender: channel.Identity{
				SubjectID: subjectID,
				Attributes: map[string]string{
					"user_openid":  subjectID,
					"union_openid": strings.TrimSpace(payload.Author.UnionOpenID),
				},
			},
			Conversation: channel.Conversation{
				ID:   subjectID,
				Type: channel.ConversationTypePrivate,
			},
			ReceivedAt: parseTimestamp(payload.Timestamp),
			Source:     "qq",
			Metadata: map[string]any{
				"is_mentioned": false,
			},
		}, true
	case "GROUP_AT_MESSAGE_CREATE":
		if event.GroupMessage == nil {
			return channel.InboundMessage{}, false
		}
		payload := event.GroupMessage
		subjectID := strings.TrimSpace(payload.Author.MemberOpenID)
		groupID := strings.TrimSpace(payload.GroupOpenID)
		if subjectID == "" || groupID == "" {
			return channel.InboundMessage{}, false
		}
		return channel.InboundMessage{
			Channel: Type,
			BotID:   strings.TrimSpace(botID),
			Message: channel.Message{
				ID:          strings.TrimSpace(payload.ID),
				Format:      channel.MessageFormatPlain,
				Text:        parseFaceTags(strings.TrimSpace(payload.Content)),
				Attachments: toInboundAttachments(payload.Attachments),
				Reply:       qqReplyRef(payload.MessageReference, "group:"+groupID),
			},
			ReplyTarget: "group:" + groupID,
			Sender: channel.Identity{
				SubjectID: subjectID,
				Attributes: map[string]string{
					"user_openid":  subjectID,
					"group_openid": groupID,
				},
			},
			Conversation: channel.Conversation{
				ID:   groupID,
				Type: channel.ConversationTypeGroup,
			},
			ReceivedAt: parseTimestamp(payload.Timestamp),
			Source:     "qq",
			Metadata: map[string]any{
				"is_mentioned": true,
				"group_id":     strings.TrimSpace(payload.GroupID),
				"group_openid": groupID,
			},
		}, true
	case "AT_MESSAGE_CREATE":
		if event.GuildMessage == nil {
			return channel.InboundMessage{}, false
		}
		payload := event.GuildMessage
		subjectID := strings.TrimSpace(payload.Author.ID)
		channelID := strings.TrimSpace(payload.ChannelID)
		if subjectID == "" || channelID == "" {
			return channel.InboundMessage{}, false
		}
		conversationID := channelID
		conversationType := channel.ConversationTypeGroup
		threadID := ""
		guildID := strings.TrimSpace(payload.GuildID)
		if guildID != "" {
			conversationID = guildID
			threadID = channelID
			conversationType = channel.ConversationTypeThread
		}
		return channel.InboundMessage{
			Channel: Type,
			BotID:   strings.TrimSpace(botID),
			Message: channel.Message{
				ID:          strings.TrimSpace(payload.ID),
				Format:      channel.MessageFormatPlain,
				Text:        parseFaceTags(strings.TrimSpace(payload.Content)),
				Attachments: toInboundAttachments(payload.Attachments),
				Reply:       qqReplyRef(payload.MessageReference, "channel:"+channelID),
			},
			ReplyTarget: "channel:" + channelID,
			Sender: channel.Identity{
				SubjectID:   subjectID,
				DisplayName: strings.TrimSpace(payload.Author.Username),
				Attributes: map[string]string{
					"user_id":    subjectID,
					"channel_id": channelID,
					"guild_id":   strings.TrimSpace(payload.GuildID),
				},
			},
			Conversation: channel.Conversation{
				ID:       conversationID,
				Type:     conversationType,
				ThreadID: threadID,
			},
			ReceivedAt: parseTimestamp(payload.Timestamp),
			Source:     "qq",
			Metadata: map[string]any{
				"is_mentioned": true,
				"guild_id":     guildID,
				"channel_id":   channelID,
			},
		}, true
	default:
		return channel.InboundMessage{}, false
	}
}

func qqReplyRef(ref *MessageReference, target string) *channel.ReplyRef {
	if ref == nil {
		return nil
	}
	messageID := strings.TrimSpace(ref.MessageID)
	if messageID == "" {
		return nil
	}
	return &channel.ReplyRef{
		Target:    strings.TrimSpace(target),
		MessageID: messageID,
	}
}

func toInboundAttachments(items []MessageAttachment) []channel.Attachment {
	if len(items) == 0 {
		return nil
	}
	result := make([]channel.Attachment, 0, len(items))
	for _, item := range items {
		attachmentURL := normalizeQQURL(item.URL)
		attType := inferAttachmentType(item)
		if attType == channel.AttachmentVoice && strings.TrimSpace(item.VoiceWavURL) != "" {
			attachmentURL = normalizeQQURL(item.VoiceWavURL)
		}
		att := channel.NormalizeInboundChannelAttachment(channel.Attachment{
			Type:           attType,
			URL:            attachmentURL,
			Name:           strings.TrimSpace(item.FileName),
			Mime:           strings.TrimSpace(item.ContentType),
			Size:           item.Size,
			Width:          item.Width,
			Height:         item.Height,
			SourcePlatform: Type.String(),
			Metadata: map[string]any{
				"voice_wav_url": normalizeQQURL(item.VoiceWavURL),
			},
		})
		result = append(result, att)
	}
	return result
}

func inferAttachmentType(att MessageAttachment) channel.AttachmentType {
	contentType := strings.ToLower(strings.TrimSpace(att.ContentType))
	name := strings.ToLower(strings.TrimSpace(att.FileName))
	switch {
	case strings.HasPrefix(contentType, "image/gif"), strings.HasSuffix(name, ".gif"):
		return channel.AttachmentGIF
	case strings.HasPrefix(contentType, "image/"):
		return channel.AttachmentImage
	case strings.HasPrefix(contentType, "video/"):
		return channel.AttachmentVideo
	case strings.HasPrefix(contentType, "audio/"), strings.TrimSpace(att.VoiceWavURL) != "":
		return channel.AttachmentVoice
	default:
		return channel.AttachmentFile
	}
}

func normalizeQQURL(raw string) string {
	value := strings.TrimSpace(raw)
	if strings.HasPrefix(value, "//") {
		return "https:" + value
	}
	return value
}

func parseTimestamp(raw string) time.Time {
	if ts, err := time.Parse(time.RFC3339, strings.TrimSpace(raw)); err == nil {
		return ts.UTC()
	}
	return time.Now().UTC()
}

func parseFaceTags(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return faceTagPattern.ReplaceAllStringFunc(text, func(match string) string {
		value, err := decodeFaceTag(match)
		if err != nil {
			return match
		}
		return "【表情: " + value + "】"
	})
}

func sleepContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextReconnectDelay(backoffs []time.Duration, attempt int, healthySession bool) (time.Duration, int) {
	if len(backoffs) == 0 {
		return 0, attempt
	}
	if healthySession {
		attempt = 0
	}
	delay := backoffs[intMin(attempt, len(backoffs)-1)]
	return delay, attempt + 1
}

func intMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}
