package matrix

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	attachmentpkg "github.com/felinics/memoh/internal/attachment"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/common"
	"github.com/felinics/memoh/internal/media"
	"github.com/felinics/memoh/internal/textutil"
)

const Type channel.ChannelType = "matrix"

const (
	matrixDefaultTimeout  = 30 * time.Second
	matrixEditThrottle    = 1200 * time.Millisecond
	matrixRoutingStateKey = "_matrix"
)

type assetOpener interface {
	Open(ctx context.Context, botID, contentHash string) (io.ReadCloser, media.Asset, error)
}

type MatrixAdapter struct {
	logger     *slog.Logger
	httpClient *http.Client
	saveSince  func(context.Context, string, string) error
	assets     assetOpener

	txnMu sync.Mutex
	txnID uint64

	seenMu sync.Mutex
	seen   map[string]map[string]time.Time

	roomTypeMu sync.Mutex
	roomTypes  map[string]map[string]string

	directRoomMu sync.Mutex
	directRooms  map[string]map[string]string
}

type matrixSyncResponse struct {
	NextBatch   string `json:"next_batch"`
	AccountData struct {
		Events []matrixSyncEvent `json:"events"`
	} `json:"account_data"`
	Rooms struct {
		Join   map[string]matrixSyncJoinedRoom  `json:"join"`
		Invite map[string]matrixSyncInvitedRoom `json:"invite"`
	} `json:"rooms"`
}

type matrixSyncJoinedRoom struct {
	Timeline struct {
		Events []matrixEvent `json:"events"`
	} `json:"timeline"`
	Summary matrixRoomSummary `json:"summary"`
}

type matrixSyncInvitedRoom struct {
	InviteState struct {
		Events []matrixEvent `json:"events"`
	} `json:"invite_state"`
}

type matrixRoomSummary struct {
	JoinedMemberCount  int `json:"m.joined_member_count"`
	InvitedMemberCount int `json:"m.invited_member_count"`
}

type matrixSyncEvent struct {
	Type    string         `json:"type"`
	Content map[string]any `json:"content"`
}

type matrixJoinedMembersResponse struct {
	Joined map[string]matrixJoinedMember `json:"joined"`
}

type matrixJoinedRoomsResponse struct {
	JoinedRooms []string `json:"joined_rooms"`
}

type matrixJoinedMember struct {
	DisplayName string `json:"display_name,omitempty"`
	AvatarURL   string `json:"avatar_url,omitempty"`
}

type matrixEvent struct {
	EventID        string                 `json:"event_id"`
	Sender         string                 `json:"sender"`
	Type           string                 `json:"type"`
	OriginServerTS int64                  `json:"origin_server_ts"`
	Content        map[string]any         `json:"content"`
	Unsigned       map[string]any         `json:"unsigned"`
	RoomID         string                 `json:"room_id"`
	StateKey       *string                `json:"state_key,omitempty"`
	Metadata       map[string]interface{} `json:"-"`
}

type matrixSendResponse struct {
	EventID string `json:"event_id"`
}

type matrixCreateRoomRequest struct {
	Invite   []string `json:"invite,omitempty"`
	IsDirect bool     `json:"is_direct,omitempty"`
	Preset   string   `json:"preset,omitempty"`
	Topic    string   `json:"topic,omitempty"`
	Name     string   `json:"name,omitempty"`
}

type matrixCreateRoomResponse struct {
	RoomID string `json:"room_id"`
}

type matrixRoomAliasResponse struct {
	RoomID string `json:"room_id"`
}

type matrixUploadResponse struct {
	ContentURI string `json:"content_uri"`
}

type matrixVersionsResponse struct {
	Versions []string `json:"versions"`
}

type matrixWhoAmIResponse struct {
	UserID   string `json:"user_id"`
	DeviceID string `json:"device_id,omitempty"`
	IsGuest  bool   `json:"is_guest,omitempty"`
}

type matrixErrorResponse struct {
	ErrCode string `json:"errcode"`
	Error   string `json:"error"`
}

var matrixMentionHrefPattern = regexp.MustCompile(`https://matrix\.to/#/(@[^"'<\s]+)`)

func NewMatrixAdapter(log *slog.Logger) *MatrixAdapter {
	if log == nil {
		log = slog.Default()
	}
	return &MatrixAdapter{
		logger: log.With(slog.String("adapter", "matrix")),
		httpClient: &http.Client{
			Timeout: matrixDefaultTimeout,
		},
		seen:        make(map[string]map[string]time.Time),
		roomTypes:   make(map[string]map[string]string),
		directRooms: make(map[string]map[string]string),
	}
}

func (a *MatrixAdapter) SetAssetOpener(opener assetOpener) {
	a.assets = opener
}

func (a *MatrixAdapter) SetSyncStateSaver(fn func(context.Context, string, string) error) {
	if a == nil {
		return
	}
	a.saveSince = fn
}

func (*MatrixAdapter) Type() channel.ChannelType {
	return Type
}

func (*MatrixAdapter) Descriptor() channel.Descriptor {
	return channel.Descriptor{
		Type:        Type,
		DisplayName: "Matrix",
		Capabilities: channel.ChannelCapabilities{
			Text:           true,
			Markdown:       true,
			RichText:       true,
			Attachments:    true,
			Media:          true,
			Reply:          true,
			Streaming:      true,
			BlockStreaming: true,
			Edit:           true,
			ChatTypes:      []string{"direct", "group"},
		},
		OutboundPolicy: channel.OutboundPolicy{
			MediaOrder: channel.OutboundOrderTextFirst,
		},
		ConfigSchema: channel.ConfigSchema{
			Version: 3,
			Fields: map[string]channel.FieldSchema{
				"homeserverUrl": {
					Type:        channel.FieldString,
					Required:    true,
					Title:       "Homeserver URL",
					Description: "Matrix homeserver base URL, e.g. https://matrix.example.com",
				},
				"userId": {
					Type:        channel.FieldString,
					Required:    true,
					Title:       "User ID",
					Description: "Matrix bot/user ID, e.g. @memoh:example.com",
				},
				"accessToken": {
					Type:     channel.FieldSecret,
					Required: true,
					Title:    "Access Token",
				},
				"syncTimeoutSeconds": {
					Type:        channel.FieldNumber,
					Title:       "Sync Timeout Seconds",
					Description: "Long-poll timeout for /sync requests",
					Example:     30,
				},
				"autoJoinInvites": {
					Type:  channel.FieldBool,
					Title: "Auto-Join Invites",
				},
			},
		},
		UserConfigSchema: channel.ConfigSchema{
			Version: 1,
			Fields: map[string]channel.FieldSchema{
				"room_id": {
					Type:        channel.FieldString,
					Title:       "Room ID or Alias",
					Description: "Preferred outbound target, e.g. !roomid:example.com or #alias:example.com",
				},
				"user_id": {
					Type:        channel.FieldString,
					Title:       "User ID",
					Description: "Optional direct-message target, e.g. @alice:example.com",
				},
			},
		},
		TargetSpec: channel.TargetSpec{
			Format: "!room:server | #alias:server | @user:server",
			Hints: []channel.TargetHint{
				{Label: "Room ID", Example: "!abcdef:matrix.org"},
				{Label: "Room Alias", Example: "#ops:example.com"},
				{Label: "User ID", Example: "@alice:example.com"},
			},
		},
	}
}

func (*MatrixAdapter) NormalizeConfig(raw map[string]any) (map[string]any, error) {
	return normalizeConfig(raw)
}

func (*MatrixAdapter) NormalizeUserConfig(raw map[string]any) (map[string]any, error) {
	return normalizeUserConfig(raw)
}

func (*MatrixAdapter) NormalizeTarget(raw string) string {
	return normalizeTarget(raw)
}

func (*MatrixAdapter) ResolveTarget(userConfig map[string]any) (string, error) {
	return resolveTarget(userConfig)
}

func (*MatrixAdapter) MatchBinding(config map[string]any, criteria channel.BindingCriteria) bool {
	return matchBinding(config, criteria)
}

func (*MatrixAdapter) BuildUserConfig(identity channel.Identity) map[string]any {
	return buildUserConfig(identity)
}

func (a *MatrixAdapter) Connect(ctx context.Context, cfg channel.ChannelConfig, handler channel.InboundHandler) (channel.Connection, error) {
	parsed, err := parseConfig(cfg.Credentials)
	if err != nil {
		return nil, err
	}
	if err := a.validateConnection(ctx, parsed); err != nil {
		return nil, err
	}
	connCtx, cancel := context.WithCancel(ctx)
	go a.runSyncLoop(connCtx, cfg, parsed, handler)
	return channel.NewConnection(cfg, func(context.Context) error {
		cancel()
		return nil
	}), nil
}

func (a *MatrixAdapter) validateConnection(ctx context.Context, cfg Config) error {
	if err := a.validateHomeserver(ctx, cfg); err != nil {
		return err
	}
	whoami, err := a.validateAccessToken(ctx, cfg)
	if err != nil {
		return err
	}
	resolvedUserID := strings.TrimSpace(whoami.UserID)
	if resolvedUserID == "" {
		return errors.New("matrix access token check failed: homeserver returned empty user_id")
	}
	if !strings.EqualFold(resolvedUserID, strings.TrimSpace(cfg.UserID)) {
		return fmt.Errorf("matrix access token check failed: token belongs to %s, expected %s", resolvedUserID, strings.TrimSpace(cfg.UserID))
	}
	return nil
}

func (a *MatrixAdapter) validateHomeserver(ctx context.Context, cfg Config) error {
	data, _, statusCode, err := a.performRequest(ctx, http.MethodGet, cfg.HomeserverURL+"/_matrix/client/versions", nil, "", "")
	if err != nil {
		return fmt.Errorf("matrix homeserver check failed: %w", err)
	}
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("matrix homeserver check failed: %s", matrixHTTPErrorSummary(statusCode, data))
	}
	var resp matrixVersionsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return fmt.Errorf("matrix homeserver check failed: invalid /versions response: %w", err)
	}
	if len(resp.Versions) == 0 {
		return errors.New("matrix homeserver check failed: /_matrix/client/versions returned no supported versions")
	}
	return nil
}

func (a *MatrixAdapter) validateAccessToken(ctx context.Context, cfg Config) (matrixWhoAmIResponse, error) {
	data, _, statusCode, err := a.performRequest(ctx, http.MethodGet, cfg.HomeserverURL+"/_matrix/client/v3/account/whoami", nil, "", cfg.AccessToken)
	if err != nil {
		return matrixWhoAmIResponse{}, fmt.Errorf("matrix access token check failed: %w", err)
	}
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return matrixWhoAmIResponse{}, fmt.Errorf("matrix access token check failed: %s", matrixHTTPErrorSummary(statusCode, data))
	}
	var resp matrixWhoAmIResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return matrixWhoAmIResponse{}, fmt.Errorf("matrix access token check failed: invalid /account/whoami response: %w", err)
	}
	return resp, nil
}

func matrixHTTPErrorSummary(statusCode int, data []byte) string {
	var resp matrixErrorResponse
	if err := json.Unmarshal(data, &resp); err == nil {
		message := strings.TrimSpace(resp.Error)
		errCode := strings.TrimSpace(resp.ErrCode)
		switch {
		case message != "" && errCode != "":
			return fmt.Sprintf("%s (%s, HTTP %d)", message, errCode, statusCode)
		case message != "":
			return fmt.Sprintf("%s (HTTP %d)", message, statusCode)
		case errCode != "":
			return fmt.Sprintf("%s (HTTP %d)", errCode, statusCode)
		}
	}
	message := strings.TrimSpace(string(data))
	if message == "" {
		return fmt.Sprintf("HTTP %d", statusCode)
	}
	return fmt.Sprintf("%s (HTTP %d)", textutil.TruncateRunes(message, 300), statusCode)
}

func (a *MatrixAdapter) Send(ctx context.Context, cfg channel.ChannelConfig, msg channel.PreparedOutboundMessage) error {
	if msg.Message.Message.IsEmpty() {
		return errors.New("message is required")
	}
	parsed, err := parseConfig(cfg.Credentials)
	if err != nil {
		return err
	}
	roomID, err := a.resolveRoomTarget(ctx, parsed, msg.Target)
	if err != nil {
		return err
	}
	text := matrixMessageBody(msg.Message.Message)
	if text != "" {
		textMsg := msg.Message.Message
		textMsg.Attachments = nil
		if _, err := a.sendTextEvent(ctx, parsed, roomID, buildMatrixMessageContent(textMsg, false, "")); err != nil {
			return err
		}
	}
	for i, att := range msg.Message.Attachments {
		mediaMsg := channel.Message{}
		if text == "" && i == 0 {
			mediaMsg.Reply = msg.Message.Message.Reply
		}
		if err := a.sendMediaAttachment(ctx, parsed, roomID, mediaMsg, att); err != nil {
			return err
		}
	}
	return nil
}

func (a *MatrixAdapter) OpenStream(_ context.Context, cfg channel.ChannelConfig, target string, opts channel.StreamOptions) (channel.PreparedOutboundStream, error) {
	if err := validateTarget(target); err != nil {
		return nil, err
	}
	parsed, err := parseConfig(cfg.Credentials)
	if err != nil {
		return nil, err
	}
	reply := opts.Reply
	if reply == nil && strings.TrimSpace(opts.SourceMessageID) != "" {
		reply = &channel.ReplyRef{Target: normalizeTarget(target), MessageID: strings.TrimSpace(opts.SourceMessageID)}
	}
	return &matrixOutboundStream{
		adapter: a,
		cfg:     parsed,
		target:  normalizeTarget(target),
		reply:   reply,
	}, nil
}

func (a *MatrixAdapter) Update(ctx context.Context, cfg channel.ChannelConfig, target string, messageID string, msg channel.PreparedMessage) error {
	parsed, err := parseConfig(cfg.Credentials)
	if err != nil {
		return err
	}
	roomID, err := a.resolveRoomTarget(ctx, parsed, target)
	if err != nil {
		return err
	}
	_, err = a.sendTextEvent(ctx, parsed, roomID, buildMatrixMessageContent(msg.Message, true, strings.TrimSpace(messageID)))
	return err
}

func (*MatrixAdapter) Unsend(context.Context, channel.ChannelConfig, string, string) error {
	return errors.New("matrix unsend not supported")
}

func (a *MatrixAdapter) runSyncLoop(ctx context.Context, cfg channel.ChannelConfig, parsed Config, handler channel.InboundHandler) {
	backoffs := []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 20 * time.Second}
	attempt := 0
	since := matrixSinceTokenFromRouting(cfg.Routing)
	persistedSince := since
	if strings.TrimSpace(since) == "" {
		bootstrapSince, err := a.bootstrapSinceToken(ctx, cfg, parsed)
		if err != nil {
			if a.logger != nil {
				a.logger.Warn("matrix sync bootstrap failed", slog.String("config_id", cfg.ID), slog.Any("error", err))
			}
		} else if bootstrapSince != "" {
			since = bootstrapSince
			persistedSince = bootstrapSince
		}
	}
	for ctx.Err() == nil {
		nextSince, healthy, err := a.syncOnce(ctx, cfg, parsed, since, handler)
		if strings.TrimSpace(nextSince) != "" {
			since = nextSince
		}
		if err == nil && strings.TrimSpace(since) != "" && since != persistedSince {
			if saveErr := a.persistSinceToken(ctx, cfg.ID, since); saveErr != nil {
				if a.logger != nil {
					a.logger.Warn("matrix sync cursor persist failed", slog.String("config_id", cfg.ID), slog.Bool("healthy", healthy), slog.Any("error", saveErr))
				}
			} else {
				persistedSince = since
			}
		}
		if err == nil || ctx.Err() != nil {
			attempt = 0
			continue
		}
		if a.logger != nil {
			a.logger.Warn("matrix sync reconnect", slog.String("config_id", cfg.ID), slog.Any("error", err))
		}
		delay, nextAttempt := nextReconnectDelay(backoffs, attempt, healthy)
		attempt = nextAttempt
		if !sleepContext(ctx, delay) {
			return
		}
	}
}

func (a *MatrixAdapter) bootstrapSinceToken(ctx context.Context, cfg channel.ChannelConfig, parsed Config) (string, error) {
	var resp matrixSyncResponse
	if err := a.doJSON(ctx, parsed, http.MethodGet, "/_matrix/client/v3/sync?timeout=0", nil, &resp); err != nil {
		return "", err
	}
	if _, err := a.handleInvites(ctx, cfg, parsed, resp); err != nil {
		return "", err
	}
	a.rememberSyncResponseRoomTypes(cfg.ID, parsed, resp)
	a.rememberSyncResponseEvents(cfg.ID, resp)
	since := strings.TrimSpace(resp.NextBatch)
	if since == "" {
		return "", nil
	}
	if err := a.persistSinceToken(ctx, cfg.ID, since); err != nil {
		return "", err
	}
	if a.logger != nil {
		a.logger.Info("matrix sync cursor bootstrapped", slog.String("config_id", cfg.ID))
	}
	return since, nil
}

func (a *MatrixAdapter) rememberSyncResponseEvents(configID string, resp matrixSyncResponse) {
	configID = strings.TrimSpace(configID)
	if configID == "" {
		return
	}
	for _, joined := range resp.Rooms.Join {
		for _, evt := range joined.Timeline.Events {
			a.seenEvent(configID, evt.EventID)
		}
	}
}

func (a *MatrixAdapter) persistSinceToken(ctx context.Context, configID string, since string) error {
	if a == nil || a.saveSince == nil {
		return nil
	}
	configID = strings.TrimSpace(configID)
	since = strings.TrimSpace(since)
	if configID == "" || since == "" {
		return nil
	}
	return a.saveSince(ctx, configID, since)
}

func (a *MatrixAdapter) syncOnce(ctx context.Context, cfg channel.ChannelConfig, parsed Config, since string, handler channel.InboundHandler) (string, bool, error) {
	query := url.Values{}
	query.Set("timeout", strconv.Itoa(parsed.SyncTimeoutSeconds*1000))
	if strings.TrimSpace(since) != "" {
		query.Set("since", since)
	}
	var resp matrixSyncResponse
	if err := a.doJSON(ctx, parsed, http.MethodGet, "/_matrix/client/v3/sync?"+query.Encode(), nil, &resp); err != nil {
		return since, false, err
	}
	a.rememberSyncResponseRoomTypes(cfg.ID, parsed, resp)
	healthy := false
	joinedInvite, err := a.handleInvites(ctx, cfg, parsed, resp)
	if err != nil {
		return resp.NextBatch, healthy, err
	}
	healthy = healthy || joinedInvite
	for roomID, joined := range resp.Rooms.Join {
		for _, evt := range joined.Timeline.Events {
			evt.RoomID = roomID
			delivered, err := a.handleEvent(ctx, cfg, parsed, evt, handler)
			if err != nil {
				return resp.NextBatch, healthy, err
			}
			healthy = healthy || delivered
		}
	}
	return resp.NextBatch, healthy, nil
}

func (a *MatrixAdapter) handleInvites(ctx context.Context, cfg channel.ChannelConfig, parsed Config, resp matrixSyncResponse) (bool, error) {
	joinedAny := false
	for roomID := range resp.Rooms.Invite {
		roomID = strings.TrimSpace(roomID)
		if roomID == "" {
			continue
		}
		if !parsed.AutoJoinInvites {
			if a.logger != nil {
				a.logger.Info("matrix invite skipped",
					slog.String("config_id", cfg.ID),
					slog.String("room_id", roomID),
					slog.String("reason", "auto_join_disabled"),
				)
			}
			continue
		}
		if err := a.joinRoom(ctx, parsed, roomID); err != nil {
			return joinedAny, err
		}
		joinedAny = true
		if a.logger != nil {
			a.logger.Info("matrix room auto-joined",
				slog.String("config_id", cfg.ID),
				slog.String("room_id", roomID),
			)
		}
	}
	return joinedAny, nil
}

func (a *MatrixAdapter) handleEvent(ctx context.Context, cfg channel.ChannelConfig, parsed Config, evt matrixEvent, handler channel.InboundHandler) (bool, error) {
	if evt.Type != "m.room.message" {
		return false, nil
	}
	if strings.TrimSpace(evt.Sender) == "" || strings.EqualFold(strings.TrimSpace(evt.Sender), parsed.UserID) {
		return false, nil
	}
	if a.seenEvent(cfg.ID, evt.EventID) {
		return false, nil
	}
	if isMatrixEditEvent(evt.Content) {
		return false, nil
	}
	body, attachments := extractMatrixInboundContent(evt.Content)
	if body == "" && len(attachments) == 0 {
		return false, nil
	}
	isMentioned := isMatrixBotMentioned(parsed.UserID, evt.Content)
	replyTo := readReplyToEventID(evt.Content)
	if replyTo != "" {
		body = stripMatrixReplyFallback(body)
	}
	rawText := body
	isReplyToBot := false
	var replySender, replyPreview string
	var replyAttachments []channel.Attachment
	replyAttachmentsKnown := false
	if replyTo != "" {
		repliedEvent, err := a.fetchRoomEvent(ctx, parsed, evt.RoomID, replyTo)
		if err != nil {
			if a.logger != nil {
				a.logger.Warn("failed to fetch matrix replied event",
					slog.String("config_id", cfg.ID),
					slog.String("room_id", evt.RoomID),
					slog.String("reply_to", replyTo),
					slog.Any("error", err),
				)
			}
		} else {
			replyAttachmentsKnown = true
			replyAttachments = matrixQuotedAttachments(repliedEvent)
			isReplyToBot = strings.EqualFold(strings.TrimSpace(repliedEvent.Sender), parsed.UserID)
			replySender = matrixDisplayName(repliedEvent)
			preview, _ := extractMatrixInboundContent(repliedEvent.Content)
			preview = strings.TrimSpace(preview)
			if preview == "" {
				preview = strings.TrimSpace(channel.ReadString(repliedEvent.Content, "body"))
			}
			if len([]rune(preview)) > 200 {
				preview = string([]rune(preview)[:200]) + "..."
			}
			replyPreview = preview
		}
	}
	conversationType := a.resolveConversationType(ctx, cfg.ID, parsed, evt.RoomID)
	msg := channel.InboundMessage{
		Channel:     Type,
		BotID:       cfg.BotID,
		ReplyTarget: evt.RoomID,
		Message: channel.Message{
			ID:          strings.TrimSpace(evt.EventID),
			Format:      channel.MessageFormatPlain,
			Text:        body,
			Attachments: attachments,
		},
		Sender: channel.Identity{
			SubjectID:   strings.TrimSpace(evt.Sender),
			DisplayName: matrixDisplayName(evt),
			Attributes: map[string]string{
				"user_id": strings.TrimSpace(evt.Sender),
				"room_id": strings.TrimSpace(evt.RoomID),
			},
		},
		Conversation: channel.Conversation{
			ID:   strings.TrimSpace(evt.RoomID),
			Type: conversationType,
			Metadata: map[string]any{
				"room_id": strings.TrimSpace(evt.RoomID),
			},
		},
		ReceivedAt: matrixEventTime(evt.OriginServerTS),
		Source:     "matrix",
		Metadata: map[string]any{
			"room_id":         strings.TrimSpace(evt.RoomID),
			"event_id":        strings.TrimSpace(evt.EventID),
			"sender":          strings.TrimSpace(evt.Sender),
			"msgtype":         channel.ReadString(evt.Content, "msgtype"),
			"raw_text":        rawText,
			"attachments":     len(attachments),
			"is_mentioned":    isMentioned,
			"is_reply_to_bot": isReplyToBot,
			"bot_alias":       strings.TrimSpace(parsed.UserID),
			"bot_aliases":     matrixBotMentionAliases(parsed.UserID),
		},
	}
	if replyTo != "" {
		msg.Message.Reply = &channel.ReplyRef{
			Target:           evt.RoomID,
			MessageID:        replyTo,
			Sender:           replySender,
			Preview:          replyPreview,
			Attachments:      replyAttachments,
			AttachmentsKnown: replyAttachmentsKnown,
		}
	}
	if a.logger != nil {
		a.logger.Info("inbound received",
			slog.String("config_id", cfg.ID),
			slog.String("room_id", evt.RoomID),
			slog.String("sender", evt.Sender),
			slog.Bool("is_mentioned", isMentioned),
			slog.String("text", common.SummarizeText(body)),
		)
	}
	return true, handler(ctx, cfg, msg)
}

func (a *MatrixAdapter) fetchRoomEvent(ctx context.Context, cfg Config, roomID, eventID string) (matrixEvent, error) {
	path := fmt.Sprintf("/_matrix/client/v3/rooms/%s/event/%s", url.PathEscape(strings.TrimSpace(roomID)), url.PathEscape(strings.TrimSpace(eventID)))
	var evt matrixEvent
	if err := a.doJSON(ctx, cfg, http.MethodGet, path, nil, &evt); err != nil {
		return matrixEvent{}, err
	}
	evt.RoomID = strings.TrimSpace(roomID)
	return evt, nil
}

func (a *MatrixAdapter) resolveConversationType(ctx context.Context, configID string, cfg Config, roomID string) string {
	if conversationType, ok := a.cachedRoomConversationType(configID, roomID); ok {
		return conversationType
	}
	isDirect, err := a.isDirectRoom(ctx, cfg, roomID)
	if err != nil {
		if a.logger != nil {
			a.logger.Warn("failed to resolve matrix room type",
				slog.String("config_id", configID),
				slog.String("room_id", strings.TrimSpace(roomID)),
				slog.Any("error", err),
			)
		}
		return "group"
	}
	conversationType := "group"
	if isDirect {
		conversationType = "direct"
	}
	a.rememberRoomConversationType(configID, roomID, conversationType)
	return conversationType
}

func (a *MatrixAdapter) isDirectRoom(ctx context.Context, cfg Config, roomID string) (bool, error) {
	path := fmt.Sprintf("/_matrix/client/v3/rooms/%s/joined_members", url.PathEscape(strings.TrimSpace(roomID)))
	var resp matrixJoinedMembersResponse
	if err := a.doJSON(ctx, cfg, http.MethodGet, path, nil, &resp); err != nil {
		return false, err
	}
	return len(resp.Joined) == 2, nil
}

func (a *MatrixAdapter) rememberSyncResponseRoomTypes(configID string, cfg Config, resp matrixSyncResponse) {
	a.rememberSyncDirectRooms(cfg, resp)
	configID = strings.TrimSpace(configID)
	if configID == "" {
		return
	}
	directRooms := extractMatrixDirectRoomIDs(resp)
	for roomID, joined := range resp.Rooms.Join {
		roomID = strings.TrimSpace(roomID)
		if roomID == "" {
			continue
		}
		if _, ok := directRooms[roomID]; ok {
			a.rememberRoomConversationType(configID, roomID, "direct")
			continue
		}
		if conversationType := matrixConversationTypeFromSummary(joined.Summary); conversationType != "" {
			a.rememberRoomConversationType(configID, roomID, conversationType)
		}
	}
}

func extractMatrixDirectRooms(resp matrixSyncResponse) map[string]string {
	directRooms := make(map[string]string)
	for _, evt := range resp.AccountData.Events {
		if strings.TrimSpace(evt.Type) != "m.direct" {
			continue
		}
		for userID, rawRoomIDs := range evt.Content {
			userID = strings.TrimSpace(userID)
			if userID == "" {
				continue
			}
			for _, roomID := range matrixStringList(rawRoomIDs) {
				roomID = strings.TrimSpace(roomID)
				if roomID == "" {
					continue
				}
				directRooms[userID] = roomID
				break
			}
		}
	}
	return directRooms
}

func (a *MatrixAdapter) rememberSyncDirectRooms(cfg Config, resp matrixSyncResponse) {
	for userID, roomID := range extractMatrixDirectRooms(resp) {
		a.rememberDirectRoomForConfig(cfg, userID, roomID)
	}
}

func extractMatrixDirectRoomIDs(resp matrixSyncResponse) map[string]struct{} {
	directRooms := make(map[string]struct{})
	for _, evt := range resp.AccountData.Events {
		if strings.TrimSpace(evt.Type) != "m.direct" {
			continue
		}
		for _, rawRoomIDs := range evt.Content {
			for _, roomID := range matrixStringList(rawRoomIDs) {
				roomID = strings.TrimSpace(roomID)
				if roomID == "" {
					continue
				}
				directRooms[roomID] = struct{}{}
			}
		}
	}
	return directRooms
}

func matrixConversationTypeFromSummary(summary matrixRoomSummary) string {
	totalMembers := summary.JoinedMemberCount + summary.InvitedMemberCount
	switch {
	case totalMembers == 2:
		return "direct"
	case totalMembers > 2:
		return "group"
	default:
		return ""
	}
}

func matrixStringList(raw any) []string {
	switch value := raw.(type) {
	case []string:
		result := make([]string, 0, len(value))
		for _, item := range value {
			trimmed := strings.TrimSpace(item)
			if trimmed != "" {
				result = append(result, trimmed)
			}
		}
		return result
	case []any:
		result := make([]string, 0, len(value))
		for _, item := range value {
			text, ok := item.(string)
			if !ok {
				continue
			}
			trimmed := strings.TrimSpace(text)
			if trimmed != "" {
				result = append(result, trimmed)
			}
		}
		return result
	default:
		return nil
	}
}

func (a *MatrixAdapter) cachedRoomConversationType(configID, roomID string) (string, bool) {
	a.roomTypeMu.Lock()
	defer a.roomTypeMu.Unlock()
	rooms, ok := a.roomTypes[strings.TrimSpace(configID)]
	if !ok {
		return "", false
	}
	conversationType, ok := rooms[strings.TrimSpace(roomID)]
	if !ok || strings.TrimSpace(conversationType) == "" {
		return "", false
	}
	return conversationType, true
}

func (a *MatrixAdapter) rememberRoomConversationType(configID, roomID, conversationType string) {
	configID = strings.TrimSpace(configID)
	roomID = strings.TrimSpace(roomID)
	conversationType = strings.TrimSpace(conversationType)
	if configID == "" || roomID == "" || conversationType == "" {
		return
	}
	a.roomTypeMu.Lock()
	defer a.roomTypeMu.Unlock()
	rooms, ok := a.roomTypes[configID]
	if !ok {
		rooms = make(map[string]string)
		a.roomTypes[configID] = rooms
	}
	rooms[roomID] = conversationType
}

func buildMatrixMessageContent(msg channel.Message, edit bool, originalEventID string) map[string]any {
	formatted := formatMatrixMessage(msg)
	body := formatted.Body
	content := map[string]any{
		"msgtype": "m.notice",
		"body":    body,
	}
	if formatted.HasHTML {
		content["format"] = matrixHTMLFormat
		content["formatted_body"] = formatted.FormattedBody
	}
	if msg.Reply != nil && strings.TrimSpace(msg.Reply.MessageID) != "" && !edit {
		content["m.relates_to"] = map[string]any{
			"m.in_reply_to": map[string]any{
				"event_id": strings.TrimSpace(msg.Reply.MessageID),
			},
		}
	}
	if edit && strings.TrimSpace(originalEventID) != "" {
		newContent := map[string]any{
			"msgtype": "m.notice",
			"body":    body,
		}
		if formatted.HasHTML {
			newContent["format"] = matrixHTMLFormat
			newContent["formatted_body"] = formatted.FormattedBody
		}
		content["m.new_content"] = newContent
		content["m.relates_to"] = map[string]any{
			"rel_type": "m.replace",
			"event_id": strings.TrimSpace(originalEventID),
		}
		content["body"] = "* " + body
	}
	return content
}

func buildMatrixMediaContent(msg channel.Message, att channel.Attachment, contentURI string) map[string]any {
	body := matrixAttachmentBody(att)
	content := map[string]any{
		"msgtype": matrixAttachmentMsgType(att.Type),
		"body":    body,
		"url":     strings.TrimSpace(contentURI),
	}
	if filename := strings.TrimSpace(att.Name); filename != "" {
		content["filename"] = filename
	}
	info := matrixAttachmentInfo(att)
	if len(info) > 0 {
		content["info"] = info
	}
	if msg.Reply != nil && strings.TrimSpace(msg.Reply.MessageID) != "" {
		content["m.relates_to"] = map[string]any{
			"m.in_reply_to": map[string]any{
				"event_id": strings.TrimSpace(msg.Reply.MessageID),
			},
		}
	}
	return content
}

func isMatrixEditEvent(content map[string]any) bool {
	if _, ok := content["m.new_content"]; ok {
		return true
	}
	relatesTo, ok := content["m.relates_to"].(map[string]any)
	if !ok {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(channel.ReadString(relatesTo, "rel_type")), "m.replace")
}

func readReplyToEventID(content map[string]any) string {
	relatesTo, ok := content["m.relates_to"].(map[string]any)
	if !ok {
		return ""
	}
	inReplyTo, ok := relatesTo["m.in_reply_to"].(map[string]any)
	if !ok {
		return ""
	}
	return strings.TrimSpace(channel.ReadString(inReplyTo, "event_id"))
}

func extractMatrixInboundContent(content map[string]any) (string, []channel.Attachment) {
	msgType := strings.TrimSpace(channel.ReadString(content, "msgtype"))
	if !isMatrixAttachmentMsgType(msgType) {
		return strings.TrimSpace(channel.ReadString(content, "body")), nil
	}
	att, ok := matrixAttachmentFromContent(content, msgType)
	if !ok {
		return strings.TrimSpace(channel.ReadString(content, "body")), nil
	}
	return strings.TrimSpace(att.Caption), []channel.Attachment{att}
}

func matrixAttachmentFromContent(content map[string]any, msgType string) (channel.Attachment, bool) {
	contentURI := strings.TrimSpace(channel.ReadString(content, "url"))
	if contentURI == "" {
		return channel.Attachment{}, false
	}
	info, _ := content["info"].(map[string]any)
	body := strings.TrimSpace(channel.ReadString(content, "body"))
	name := strings.TrimSpace(channel.ReadString(content, "filename"))
	caption := ""
	if name == "" {
		name = body
	} else if body != "" && !strings.EqualFold(body, name) {
		caption = body
	}
	att := channel.Attachment{
		Type:           matrixAttachmentType(msgType),
		PlatformKey:    contentURI,
		SourcePlatform: Type.String(),
		Name:           name,
		Caption:        caption,
		Mime:           strings.TrimSpace(channel.ReadString(info, "mimetype")),
		Size:           matrixMapInt64(info, "size"),
		Width:          matrixMapInt(info, "w"),
		Height:         matrixMapInt(info, "h"),
		DurationMs:     matrixMapInt64(info, "duration"),
	}
	return channel.NormalizeInboundChannelAttachment(att), true
}

func isMatrixAttachmentMsgType(msgType string) bool {
	switch strings.TrimSpace(msgType) {
	case "m.image", "m.file", "m.video", "m.audio":
		return true
	default:
		return false
	}
}

func matrixAttachmentType(msgType string) channel.AttachmentType {
	switch strings.TrimSpace(msgType) {
	case "m.image":
		return channel.AttachmentImage
	case "m.video":
		return channel.AttachmentVideo
	case "m.audio":
		return channel.AttachmentAudio
	default:
		return channel.AttachmentFile
	}
}

func matrixQuotedAttachments(replyTo matrixEvent) []channel.Attachment {
	_, attachments := extractMatrixInboundContent(replyTo.Content)
	if len(attachments) == 0 {
		return nil
	}
	return attachments
}

func stripMatrixReplyFallback(body string) string {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(trimmed, "\r\n", "\n"), "\n")
	idx := 0
	sawQuote := false
	for idx < len(lines) {
		line := lines[idx]
		if strings.HasPrefix(line, ">") {
			sawQuote = true
			idx++
			continue
		}
		if sawQuote && strings.TrimSpace(line) == "" {
			idx++
			continue
		}
		break
	}
	if !sawQuote {
		return trimmed
	}
	return strings.TrimSpace(strings.Join(lines[idx:], "\n"))
}

func matrixSinceTokenFromRouting(routing map[string]any) string {
	if len(routing) == 0 {
		return ""
	}
	state, ok := routing[matrixRoutingStateKey]
	if !ok || state == nil {
		return strings.TrimSpace(channel.ReadString(routing, "matrix_since_token", "since_token"))
	}
	switch value := state.(type) {
	case map[string]any:
		return strings.TrimSpace(channel.ReadString(value, "since_token", "sinceToken"))
	case map[string]string:
		return strings.TrimSpace(value["since_token"])
	default:
		return ""
	}
}

func isMatrixBotMentioned(botUserID string, content map[string]any) bool {
	botUserID = strings.TrimSpace(botUserID)
	if botUserID == "" {
		return false
	}
	if mentions, ok := content["m.mentions"].(map[string]any); ok {
		if userIDs, ok := mentions["user_ids"].([]any); ok {
			for _, item := range userIDs {
				if strings.EqualFold(strings.TrimSpace(fmt.Sprint(item)), botUserID) {
					return true
				}
			}
		}
	}
	formatted := strings.TrimSpace(channel.ReadString(content, "formatted_body", "formattedBody"))
	if formatted != "" {
		matches := matrixMentionHrefPattern.FindAllStringSubmatch(formatted, -1)
		for _, match := range matches {
			if len(match) > 1 && strings.EqualFold(strings.TrimSpace(match[1]), botUserID) {
				return true
			}
		}
	}
	body := strings.TrimSpace(channel.ReadString(content, "body"))
	if body == "" {
		return false
	}
	for _, candidate := range matrixBotMentionAliases(botUserID) {
		if matrixHasExactMentionToken(body, candidate) {
			return true
		}
	}
	return false
}

func matrixBotMentionAliases(botUserID string) []string {
	botUserID = strings.TrimSpace(botUserID)
	if botUserID == "" {
		return nil
	}
	aliases := []string{botUserID}
	if idx := strings.Index(botUserID, ":"); idx > 0 {
		aliases = append(aliases, botUserID[:idx])
	}
	return aliases
}

func matrixHasExactMentionToken(body, candidate string) bool {
	body = strings.TrimSpace(body)
	candidate = strings.TrimSpace(candidate)
	if body == "" || candidate == "" {
		return false
	}
	lowerBody := strings.ToLower(body)
	lowerCandidate := strings.ToLower(candidate)
	searchFrom := 0
	for searchFrom < len(lowerBody) {
		idx := strings.Index(lowerBody[searchFrom:], lowerCandidate)
		if idx < 0 {
			return false
		}
		start := searchFrom + idx
		end := start + len(lowerCandidate)
		if matrixMentionBoundaryBefore(body, start) && matrixMentionBoundaryAfter(body, end) {
			return true
		}
		searchFrom = start + len(lowerCandidate)
	}
	return false
}

func matrixMentionBoundaryBefore(body string, idx int) bool {
	if idx <= 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(body[:idx])
	return matrixMentionBoundaryRune(r, true)
}

func matrixMentionBoundaryAfter(body string, idx int) bool {
	if idx >= len(body) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(body[idx:])
	return matrixMentionBoundaryRune(r, false)
}

func matrixMentionBoundaryRune(r rune, before bool) bool {
	if unicode.IsSpace(r) {
		return true
	}
	switch r {
	case '(', '[', '{', '<', '>', ',', ';', '.', '!', '?', '\'', '"', '`':
		return true
	case ')', ']', '}':
		return !before
	default:
		return false
	}
}

func matrixAttachmentMsgType(attType channel.AttachmentType) string {
	switch attType {
	case channel.AttachmentImage, channel.AttachmentGIF:
		return "m.image"
	case channel.AttachmentVideo:
		return "m.video"
	case channel.AttachmentAudio, channel.AttachmentVoice:
		return "m.audio"
	default:
		return "m.file"
	}
}

func matrixAttachmentBody(att channel.Attachment) string {
	if caption := strings.TrimSpace(att.Caption); caption != "" {
		return caption
	}
	if name := strings.TrimSpace(att.Name); name != "" {
		return name
	}
	switch att.Type {
	case channel.AttachmentImage, channel.AttachmentGIF:
		return "image"
	case channel.AttachmentVideo:
		return "video"
	case channel.AttachmentAudio, channel.AttachmentVoice:
		return "audio"
	default:
		return "file"
	}
}

func matrixAttachmentInfo(att channel.Attachment) map[string]any {
	info := map[string]any{}
	if mime := strings.TrimSpace(att.Mime); mime != "" {
		info["mimetype"] = mime
	}
	if att.Size > 0 {
		info["size"] = att.Size
	}
	if att.Width > 0 {
		info["w"] = att.Width
	}
	if att.Height > 0 {
		info["h"] = att.Height
	}
	if att.DurationMs > 0 {
		info["duration"] = att.DurationMs
	}
	return info
}

func matrixMapInt64(raw map[string]any, key string) int64 {
	if raw == nil {
		return 0
	}
	value, ok := raw[key]
	if !ok {
		return 0
	}
	switch v := value.(type) {
	case int:
		return int64(v)
	case int32:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case json.Number:
		parsed, err := v.Int64()
		if err == nil {
			return parsed
		}
	}
	return 0
}

func matrixMapInt(raw map[string]any, key string) int {
	return int(matrixMapInt64(raw, key))
}

func (a *MatrixAdapter) sendTextEvent(ctx context.Context, cfg Config, roomID string, content map[string]any) (string, error) {
	txnID := a.nextTxnID()
	path := fmt.Sprintf("/_matrix/client/v3/rooms/%s/send/m.room.message/%s", url.PathEscape(roomID), url.PathEscape(txnID))
	var resp matrixSendResponse
	if err := a.doJSON(ctx, cfg, http.MethodPut, path, content, &resp); err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.EventID), nil
}

func (a *MatrixAdapter) sendMediaAttachment(ctx context.Context, cfg Config, roomID string, msg channel.Message, att channel.PreparedAttachment) error {
	contentURI, resolved, err := a.resolveMatrixContentURI(ctx, cfg, att)
	if err != nil {
		return err
	}
	_, err = a.sendTextEvent(ctx, cfg, roomID, buildMatrixMediaContent(msg, resolved, contentURI))
	return err
}

func (a *MatrixAdapter) resolveMatrixContentURI(ctx context.Context, cfg Config, att channel.PreparedAttachment) (string, channel.Attachment, error) {
	if ref := strings.TrimSpace(att.NativeRef); att.Kind == channel.PreparedAttachmentNativeRef && isMatrixContentURI(ref) {
		resolved := att.Logical
		if resolved.SourcePlatform == "" {
			resolved.SourcePlatform = Type.String()
		}
		return ref, resolved, nil
	}
	payload, resolved, err := prepareMatrixUpload(ctx, att)
	if err != nil {
		return "", channel.Attachment{}, err
	}
	contentURI, err := a.uploadMatrixMedia(ctx, cfg, payload.data, payload.mime, payload.name)
	if err != nil {
		return "", channel.Attachment{}, err
	}
	resolved.PlatformKey = contentURI
	resolved.SourcePlatform = Type.String()
	if resolved.Size <= 0 {
		resolved.Size = int64(len(payload.data))
	}
	return contentURI, resolved, nil
}

type matrixUploadPayload struct {
	data []byte
	mime string
	name string
}

func prepareMatrixUpload(ctx context.Context, att channel.PreparedAttachment) (matrixUploadPayload, channel.Attachment, error) {
	if att.Kind != channel.PreparedAttachmentUpload {
		return matrixUploadPayload{}, channel.Attachment{}, fmt.Errorf("matrix attachment requires upload source, got %s", att.Kind)
	}
	if att.Open == nil {
		return matrixUploadPayload{}, channel.Attachment{}, errors.New("matrix attachment upload is not openable")
	}
	reader, err := att.Open(ctx)
	if err != nil {
		return matrixUploadPayload{}, channel.Attachment{}, err
	}
	defer func() { _ = reader.Close() }()
	data, err := media.ReadAllWithLimit(reader, media.MaxAssetBytes)
	if err != nil {
		return matrixUploadPayload{}, channel.Attachment{}, err
	}
	resolved := att.Logical
	if strings.TrimSpace(resolved.Mime) == "" {
		resolved.Mime = strings.TrimSpace(att.Mime)
	}
	if resolved.Size <= 0 {
		resolved.Size = att.Size
		if resolved.Size <= 0 {
			resolved.Size = int64(len(data))
		}
	}
	name := deriveMatrixUploadName(resolved, strings.TrimSpace(att.Mime), att.Name)
	return matrixUploadPayload{data: data, mime: strings.TrimSpace(att.Mime), name: name}, resolved, nil
}

func deriveMatrixUploadName(att channel.Attachment, mime, fallbackName string) string {
	if name := strings.TrimSpace(att.Name); name != "" {
		return name
	}
	if name := strings.TrimSpace(fallbackName); name != "" {
		return name
	}
	return matrixAttachmentBody(channel.Attachment{Type: att.Type, Mime: mime, Caption: att.Caption})
}

func (a *MatrixAdapter) uploadMatrixMedia(ctx context.Context, cfg Config, data []byte, mime, filename string) (string, error) {
	query := url.Values{}
	if strings.TrimSpace(filename) != "" {
		query.Set("filename", strings.TrimSpace(filename))
	}
	path := "/_matrix/media/v3/upload"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	body := bytes.NewReader(data)
	payload, _, err := a.doRequest(ctx, cfg, http.MethodPost, path, body, firstNonEmpty(strings.TrimSpace(mime), "application/octet-stream"))
	if err != nil {
		return "", err
	}
	var resp matrixUploadResponse
	if err := json.Unmarshal(payload, &resp); err != nil {
		return "", err
	}
	contentURI := strings.TrimSpace(resp.ContentURI)
	if contentURI == "" {
		return "", errors.New("matrix upload returned empty content_uri")
	}
	return contentURI, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func isMatrixContentURI(ref string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(ref)), "mxc://")
}

func parseMatrixContentURI(ref string) (string, string, bool) {
	trimmed := strings.TrimSpace(ref)
	if !isMatrixContentURI(trimmed) {
		return "", "", false
	}
	withoutScheme := strings.TrimPrefix(trimmed, "mxc://")
	server, mediaID, ok := strings.Cut(withoutScheme, "/")
	if !ok || strings.TrimSpace(server) == "" || strings.TrimSpace(mediaID) == "" {
		return "", "", false
	}
	return strings.TrimSpace(server), strings.TrimSpace(mediaID), true
}

func (a *MatrixAdapter) resolveRoomTarget(ctx context.Context, cfg Config, target string) (string, error) {
	target = normalizeTarget(target)
	if err := validateTarget(target); err != nil {
		return "", err
	}
	if strings.HasPrefix(target, "@") {
		return a.ensureDirectRoom(ctx, cfg, target)
	}
	if strings.HasPrefix(target, "#") {
		return a.resolveRoomAlias(ctx, cfg, target)
	}
	return target, nil
}

func (a *MatrixAdapter) resolveRoomAlias(ctx context.Context, cfg Config, roomAlias string) (string, error) {
	path := fmt.Sprintf("/_matrix/client/v3/directory/room/%s", url.PathEscape(strings.TrimSpace(roomAlias)))
	var resp matrixRoomAliasResponse
	if err := a.doJSON(ctx, cfg, http.MethodGet, path, nil, &resp); err != nil {
		return "", err
	}
	if strings.TrimSpace(resp.RoomID) == "" {
		return "", fmt.Errorf("matrix room alias lookup returned empty room_id: %s", roomAlias)
	}
	return strings.TrimSpace(resp.RoomID), nil
}

func (a *MatrixAdapter) ensureDirectRoom(ctx context.Context, cfg Config, userID string) (string, error) {
	userID = strings.TrimSpace(userID)
	if roomID, ok := a.cachedDirectRoom(cfg, userID); ok {
		return roomID, nil
	}
	if roomID, err := a.findExistingDirectRoom(ctx, cfg, userID); err == nil {
		if roomID != "" {
			a.rememberDirectRoomForConfig(cfg, userID, roomID)
			return roomID, nil
		}
	} else if a.logger != nil {
		a.logger.Warn("matrix direct room lookup failed",
			slog.String("user_id", userID),
			slog.Any("error", err),
		)
	}
	req := matrixCreateRoomRequest{
		Invite:   []string{userID},
		IsDirect: true,
		Preset:   "trusted_private_chat",
	}
	var resp matrixCreateRoomResponse
	if err := a.doJSON(ctx, cfg, http.MethodPost, "/_matrix/client/v3/createRoom", req, &resp); err != nil {
		return "", err
	}
	if strings.TrimSpace(resp.RoomID) == "" {
		return "", errors.New("matrix createRoom returned empty room_id")
	}
	roomID := strings.TrimSpace(resp.RoomID)
	a.rememberDirectRoomForConfig(cfg, userID, roomID)
	return roomID, nil
}

func (a *MatrixAdapter) findExistingDirectRoom(ctx context.Context, cfg Config, userID string) (string, error) {
	var resp matrixJoinedRoomsResponse
	if err := a.doJSON(ctx, cfg, http.MethodGet, "/_matrix/client/v3/joined_rooms", nil, &resp); err != nil {
		return "", err
	}
	for _, roomID := range resp.JoinedRooms {
		matched, err := a.isDirectRoomForUser(ctx, cfg, roomID, userID)
		if err != nil {
			if a.logger != nil {
				a.logger.Warn("matrix direct room candidate lookup failed",
					slog.String("room_id", strings.TrimSpace(roomID)),
					slog.String("user_id", strings.TrimSpace(userID)),
					slog.Any("error", err),
				)
			}
			continue
		}
		if matched {
			return strings.TrimSpace(roomID), nil
		}
	}
	return "", nil
}

func (a *MatrixAdapter) isDirectRoomForUser(ctx context.Context, cfg Config, roomID string, userID string) (bool, error) {
	path := fmt.Sprintf("/_matrix/client/v3/rooms/%s/joined_members", url.PathEscape(strings.TrimSpace(roomID)))
	var resp matrixJoinedMembersResponse
	if err := a.doJSON(ctx, cfg, http.MethodGet, path, nil, &resp); err != nil {
		return false, err
	}
	if len(resp.Joined) != 2 {
		return false, nil
	}
	if _, ok := resp.Joined[strings.TrimSpace(userID)]; !ok {
		return false, nil
	}
	if _, ok := resp.Joined[strings.TrimSpace(cfg.UserID)]; !ok {
		return false, nil
	}
	return true, nil
}

func directRoomCacheKey(cfg Config) string {
	return strings.TrimSpace(cfg.HomeserverURL) + "|" + strings.TrimSpace(cfg.UserID)
}

func (a *MatrixAdapter) cachedDirectRoom(cfg Config, userID string) (string, bool) {
	if a == nil {
		return "", false
	}
	cacheKey := directRoomCacheKey(cfg)
	userID = strings.TrimSpace(userID)
	if cacheKey == "" || userID == "" {
		return "", false
	}
	a.directRoomMu.Lock()
	defer a.directRoomMu.Unlock()
	rooms, ok := a.directRooms[cacheKey]
	if !ok {
		return "", false
	}
	roomID, ok := rooms[userID]
	if !ok || strings.TrimSpace(roomID) == "" {
		return "", false
	}
	return roomID, true
}

func (a *MatrixAdapter) rememberDirectRoomForConfig(cfg Config, userID, roomID string) {
	a.rememberDirectRoom(directRoomCacheKey(cfg), userID, roomID)
}

func (a *MatrixAdapter) rememberDirectRoom(cacheKey, userID, roomID string) {
	if a == nil {
		return
	}
	cacheKey = strings.TrimSpace(cacheKey)
	userID = strings.TrimSpace(userID)
	roomID = strings.TrimSpace(roomID)
	if cacheKey == "" || userID == "" || roomID == "" {
		return
	}
	a.directRoomMu.Lock()
	defer a.directRoomMu.Unlock()
	rooms, ok := a.directRooms[cacheKey]
	if !ok {
		rooms = make(map[string]string)
		a.directRooms[cacheKey] = rooms
	}
	rooms[userID] = roomID
}

func (a *MatrixAdapter) joinRoom(ctx context.Context, cfg Config, roomID string) error {
	path := fmt.Sprintf("/_matrix/client/v3/join/%s", url.PathEscape(strings.TrimSpace(roomID)))
	return a.doJSON(ctx, cfg, http.MethodPost, path, nil, nil)
}

func (a *MatrixAdapter) ResolveAttachment(ctx context.Context, cfg channel.ChannelConfig, attachment channel.Attachment) (channel.AttachmentPayload, error) {
	contentURI := strings.TrimSpace(attachment.PlatformKey)
	if contentURI == "" {
		contentURI = strings.TrimSpace(attachment.URL)
	}
	if contentURI == "" {
		return channel.AttachmentPayload{}, errors.New("matrix attachment requires platform_key or url")
	}
	if !isMatrixContentURI(contentURI) {
		return channel.AttachmentPayload{}, errors.New("matrix attachment reference must be mxc://")
	}
	parsed, err := parseConfig(cfg.Credentials)
	if err != nil {
		return channel.AttachmentPayload{}, err
	}
	serverName, mediaID, ok := parseMatrixContentURI(contentURI)
	if !ok {
		return channel.AttachmentPayload{}, errors.New("invalid matrix content uri")
	}
	body, header, contentLength, err := a.downloadMatrixMedia(ctx, parsed, serverName, mediaID, strings.TrimSpace(attachment.Name))
	if err != nil {
		return channel.AttachmentPayload{}, err
	}
	mime := strings.TrimSpace(attachment.Mime)
	if mime == "" {
		mime = attachmentpkg.NormalizeMime(header.Get("Content-Type"))
	}
	size := attachment.Size
	if size <= 0 && contentLength > 0 {
		size = contentLength
	}
	return channel.AttachmentPayload{
		Reader: body,
		Mime:   mime,
		Name:   strings.TrimSpace(attachment.Name),
		Size:   size,
	}, nil
}

func (a *MatrixAdapter) downloadMatrixMedia(ctx context.Context, cfg Config, serverName, mediaID, fileName string) (io.ReadCloser, http.Header, int64, error) {
	paths := make([]string, 0, 3)
	serverName = url.PathEscape(strings.TrimSpace(serverName))
	mediaID = url.PathEscape(strings.TrimSpace(mediaID))
	trimmedFileName := strings.TrimSpace(fileName)
	if trimmedFileName != "" {
		paths = append(paths, fmt.Sprintf("/_matrix/client/v1/media/download/%s/%s/%s", serverName, mediaID, url.PathEscape(trimmedFileName)))
	}
	paths = append(paths,
		fmt.Sprintf("/_matrix/client/v1/media/download/%s/%s", serverName, mediaID),
		fmt.Sprintf("/_matrix/media/v3/download/%s/%s", serverName, mediaID),
	)

	var lastErr error
	for _, path := range paths {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.HomeserverURL+path, nil)
		if err != nil {
			return nil, nil, 0, err
		}
		request.Header.Set("Authorization", "Bearer "+cfg.AccessToken)
		resp, err := a.httpClient.Do(request) //nolint:gosec // G704: URL is derived from operator-configured Matrix homeserver
		if err != nil {
			lastErr = fmt.Errorf("download matrix attachment: %w", err)
			continue
		}
		if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
			return resp.Body, resp.Header.Clone(), resp.ContentLength, nil
		}
		data, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		message := strings.TrimSpace(string(data))
		if message == "" {
			message = resp.Status
		}
		lastErr = fmt.Errorf("download matrix attachment failed: %s", textutil.TruncateRunes(message, 300))
		if resp.StatusCode != http.StatusNotFound {
			return nil, nil, 0, lastErr
		}
	}
	if lastErr == nil {
		lastErr = errors.New("download matrix attachment failed")
	}
	return nil, nil, 0, lastErr
}

func (a *MatrixAdapter) doJSON(ctx context.Context, cfg Config, method, path string, reqBody any, respBody any) error {
	var body io.Reader
	contentType := ""
	if reqBody != nil {
		payload, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(payload)
		contentType = "application/json"
	}
	data, _, err := a.doRequest(ctx, cfg, method, path, body, contentType)
	if err != nil {
		return err
	}
	if respBody == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, respBody)
}

func (a *MatrixAdapter) doRequest(ctx context.Context, cfg Config, method, path string, body io.Reader, contentType string) ([]byte, http.Header, error) {
	data, header, statusCode, err := a.performRequest(ctx, method, cfg.HomeserverURL+path, body, contentType, cfg.AccessToken)
	if err != nil {
		return nil, nil, err
	}
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return nil, header, fmt.Errorf("matrix %s %s failed: %s", method, path, matrixHTTPErrorSummary(statusCode, data))
	}
	return data, header, nil
}

func (a *MatrixAdapter) performRequest(ctx context.Context, method string, requestURL string, body io.Reader, contentType string, accessToken string) ([]byte, http.Header, int, error) {
	request, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return nil, nil, 0, err
	}
	if strings.TrimSpace(accessToken) != "" {
		request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	}
	if strings.TrimSpace(contentType) != "" {
		request.Header.Set("Content-Type", strings.TrimSpace(contentType))
	}
	resp, err := a.httpClient.Do(request) //nolint:gosec // G704: URL is derived from operator-configured Matrix homeserver
	if err != nil {
		return nil, nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.Header.Clone(), resp.StatusCode, err
	}
	return data, resp.Header.Clone(), resp.StatusCode, nil
}

func (a *MatrixAdapter) nextTxnID() string {
	a.txnMu.Lock()
	defer a.txnMu.Unlock()
	a.txnID++
	rnd, err := cryptorand.Int(cryptorand.Reader, big.NewInt(10000))
	if err != nil {
		return fmt.Sprintf("memoh-%d-%d", time.Now().UnixMilli(), a.txnID)
	}
	return fmt.Sprintf("memoh-%d-%d-%04d", time.Now().UnixMilli(), a.txnID, rnd.Int64())
}

func (a *MatrixAdapter) seenEvent(configID, eventID string) bool {
	configID = strings.TrimSpace(configID)
	eventID = strings.TrimSpace(eventID)
	if configID == "" || eventID == "" {
		return false
	}
	now := time.Now()
	a.seenMu.Lock()
	defer a.seenMu.Unlock()
	byConfig := a.seen[configID]
	if byConfig == nil {
		byConfig = make(map[string]time.Time)
		a.seen[configID] = byConfig
	}
	for id, seenAt := range byConfig {
		if now.Sub(seenAt) > 10*time.Minute {
			delete(byConfig, id)
		}
	}
	if _, ok := byConfig[eventID]; ok {
		return true
	}
	byConfig[eventID] = now
	return false
}

func matrixDisplayName(evt matrixEvent) string {
	unsignedSender, ok := evt.Unsigned["m.relations"].(map[string]any)
	if ok {
		_ = unsignedSender
	}
	if displayName := strings.TrimSpace(channel.ReadString(evt.Unsigned, "displayname", "sender_display_name")); displayName != "" {
		return displayName
	}
	return strings.TrimSpace(evt.Sender)
}

func matrixEventTime(ts int64) time.Time {
	if ts <= 0 {
		return time.Now().UTC()
	}
	return time.UnixMilli(ts).UTC()
}

func sleepContext(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return ctx.Err() == nil
	}
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
	if healthySession {
		attempt = 0
	}
	if len(backoffs) == 0 {
		return time.Second, attempt + 1
	}
	if attempt < 0 {
		attempt = 0
	}
	if attempt >= len(backoffs) {
		attempt = len(backoffs) - 1
	}
	delay := backoffs[attempt]
	if attempt < len(backoffs)-1 {
		attempt++
	}
	return delay, attempt
}
