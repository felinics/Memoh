package chat

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	messageevent "github.com/memohai/memoh/domains/agent/chat/event"
	messagepkg "github.com/memohai/memoh/domains/agent/chat/message"
	session "github.com/memohai/memoh/domains/agent/chat/thread"
	toolapproval "github.com/memohai/memoh/domains/agent/decision/approval"
	userinput "github.com/memohai/memoh/domains/agent/decision/input"
	"github.com/memohai/memoh/domains/agent/engine/background"
	chatview "github.com/memohai/memoh/domains/agent/view"
	"github.com/memohai/memoh/domains/api/bot"
	httpx "github.com/memohai/memoh/domains/api/http/httpx"
	"github.com/memohai/memoh/domains/iam/account"
	"github.com/memohai/memoh/domains/media"
)

// messageMediaService is the narrow asset surface MessageHandler needs.
type messageMediaService interface {
	Resolve(ctx context.Context, botID, contentHash string) (media.Asset, error)
	Open(ctx context.Context, botID, contentHash string) (io.ReadCloser, media.Asset, error)
}

// MessageHandler handles bot-scoped messaging endpoints.
type MessageHandler struct {
	messageService messagepkg.Service
	sessionService *session.Service
	messageEvents  messageevent.Subscriber
	mediaService   messageMediaService
	botService     *bot.Service
	accountService *account.Service
	toolApproval   *toolapproval.Service
	userInput      *userinput.Service
	bgManager      *background.Manager
	logger         *slog.Logger
}

// NewMessageHandler creates a MessageHandler.
func NewMessageHandler(log *slog.Logger, messageService messagepkg.Service, sessionService *session.Service, botService *bot.Service, accountService *account.Service, eventSubscribers ...messageevent.Subscriber) *MessageHandler {
	var messageEvents messageevent.Subscriber
	if len(eventSubscribers) > 0 {
		messageEvents = eventSubscribers[0]
	}
	return &MessageHandler{
		messageService: messageService,
		sessionService: sessionService,
		messageEvents:  messageEvents,
		botService:     botService,
		accountService: accountService,
		logger:         log.With(slog.String("handler", "conversation")),
	}
}

// SetMediaService sets the optional media service for asset serving.
func (h *MessageHandler) SetMediaService(svc messageMediaService) {
	h.mediaService = svc
}

func (h *MessageHandler) SetToolApprovalService(svc *toolapproval.Service) {
	h.toolApproval = svc
}

func (h *MessageHandler) SetUserInputService(svc *userinput.Service) {
	h.userInput = svc
}

func (h *MessageHandler) SetBackgroundManager(mgr *background.Manager) {
	h.bgManager = mgr
}

// Register registers all conversation routes.
func (h *MessageHandler) Register(e *echo.Echo) {
	botGroup := e.Group("/bots/:bot_id")
	botGroup.GET("/messages", h.ListMessages)
	botGroup.GET("/messages/locate", h.LocateMessage)
	botGroup.DELETE("/messages", h.DeleteMessages)
	botGroup.GET("/media/:content_hash", h.ServeMedia)

	// SSE streams. Per-session messages are subscribed explicitly by the
	// client; bot-wide activity carries only lightweight session metadata
	// (no message bodies) for sidebar live-sort.
	botGroup.GET("/sessions/:session_id/messages/events", h.StreamSessionMessageEvents)
	botGroup.GET("/sessions/events", h.StreamSessionsActivityEvents)
}

// --- Messages ---

// ListMessages godoc
// @Summary List session history messages
// @Description List messages for one session with optional pagination
// @Tags messages
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param session_id query string true "Session ID"
// @Param limit query int false "Limit"
// @Param before query string false "Before"
// @Param before_message_id query string false "Message ID cursor before which to page"
// @Param format query string false "Response format: ui returns normalized chat UI turns"
// @Success 200 {object} map[string][]messagepkg.Message
// @Success 200 {object} map[string][]chatview.UITurn "when format=ui"
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/messages [get].
func (h *MessageHandler) ListMessages(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	sessionID := strings.TrimSpace(c.QueryParam("session_id"))
	if sessionID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "session_id is required")
	}
	if h.messageService == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "message service not configured")
	}

	limit := int32(30)
	if s := strings.TrimSpace(c.QueryParam("limit")); s != "" {
		if n, err := strconv.ParseInt(s, 10, 32); err == nil && n > 0 && n <= 100 {
			limit = int32(n)
		}
	}

	beforeMessageID := strings.TrimSpace(c.QueryParam("before_message_id"))
	if beforeMessageID != "" {
		if _, err := uuid.Parse(beforeMessageID); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid before_message_id")
		}
	}
	before, hasBefore := parseBeforeParam(c.QueryParam("before"))
	format := strings.ToLower(strings.TrimSpace(c.QueryParam("format")))

	bot, _, sess, err := h.authorizeMessageSession(c, channelIdentityID, botID, sessionID)
	if err != nil {
		return err
	}
	botID = bot.ID

	var messages []messagepkg.Message
	switch {
	case beforeMessageID != "":
		messages, err = h.messageService.ListBeforeMessageBySession(c.Request().Context(), sessionID, beforeMessageID, limit)
	case hasBefore:
		// ListBeforeBySession already returns oldest-first (its converter
		// reverses the DESC DB rows). Do NOT reverse again: the before page
		// and the turn-head extension both depend on monotonic ASC input.
		messages, err = h.messageService.ListBeforeBySession(c.Request().Context(), sessionID, before, limit)
	case format == "ui":
		messages, err = h.listLatestUIPageBySession(c.Request().Context(), sessionID, limit)
	default:
		messages, err = h.messageService.ListLatestBySession(c.Request().Context(), sessionID, limit)
		if err == nil {
			reverseMessages(messages)
		}
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	// format=ui converts each page independently, so a page that begins mid
	// assistant turn (its earlier rows on the previous page) would render one
	// reply as several turns/action bars. Extend the head back to a real turn
	// boundary so a turn is never split across pages.
	if format == "ui" && sessionID != "" && len(messages) > 0 {
		messages = h.extendToUITurnHead(c.Request().Context(), sessionID, messages, limit)
	}
	h.fillAssetMimeFromStorage(c.Request().Context(), botID, messages)
	if format == "ui" {
		items := chatview.ConvertMessagesToUITurns(messages)
		h.decorateUITurns(c.Request().Context(), botID, sessionID, sess, items)
		return c.JSON(http.StatusOK, map[string]any{
			"items": items,
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"items": messages})
}

type latestUIMessageLister interface {
	ListLatestUIBySession(ctx context.Context, sessionID string, limit int32) ([]messagepkg.Message, error)
}

func (h *MessageHandler) listLatestUIBySession(ctx context.Context, sessionID string, limit int32) ([]messagepkg.Message, error) {
	if svc, ok := h.messageService.(latestUIMessageLister); ok {
		return svc.ListLatestUIBySession(ctx, sessionID, limit)
	}
	return h.messageService.ListLatestBySession(ctx, sessionID, limit)
}

func (h *MessageHandler) listLatestUIPageBySession(ctx context.Context, sessionID string, limit int32) ([]messagepkg.Message, error) {
	const lookbackRows = int32(50)
	fetchLimit := limit
	if fetchLimit > 0 {
		fetchLimit += lookbackRows
	}
	messages, err := h.listLatestUIBySession(ctx, sessionID, fetchLimit)
	if err != nil {
		return nil, err
	}
	reverseMessages(messages)
	if limit <= 0 || len(messages) <= int(limit) {
		return messages, nil
	}

	start := len(messages) - int(limit)
	for start > 0 && !chatview.IsUITurnBoundary(messages[start]) {
		start--
	}
	return messages[start:], nil
}

// LocateMessage godoc
// @Summary Locate a bot history message
// @Description Locate a session message by external message ID and return nearby UI turns
// @Tags messages
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param session_id query string true "Session ID"
// @Param external_message_id query string true "External message ID"
// @Param before query int false "Messages before target"
// @Param after query int false "Messages after target"
// @Success 200 {object} map[string]any
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/messages/locate [get].
func (h *MessageHandler) LocateMessage(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if h.messageService == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "message service not configured")
	}

	sessionID := strings.TrimSpace(c.QueryParam("session_id"))
	if sessionID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "session_id is required")
	}
	bot, _, sess, err := h.authorizeMessageSession(c, channelIdentityID, botID, sessionID)
	if err != nil {
		return err
	}
	botID = bot.ID
	externalMessageID := strings.TrimSpace(c.QueryParam("external_message_id"))
	if externalMessageID == "" {
		externalMessageID = strings.TrimSpace(c.QueryParam("message_id"))
	}
	if externalMessageID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "external_message_id is required")
	}

	before := parseBoundedInt32(c.QueryParam("before"), 30, 0, 100)
	after := parseBoundedInt32(c.QueryParam("after"), 30, 0, 100)
	located, err := h.messageService.LocateByExternalIDBySession(c.Request().Context(), sessionID, externalMessageID, before, after)
	if err != nil {
		if errors.Is(err, messagepkg.ErrNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "message not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	h.fillAssetMimeFromStorage(c.Request().Context(), botID, located.Messages)
	items := chatview.ConvertMessagesToUITurns(located.Messages)
	h.decorateUITurns(c.Request().Context(), botID, sessionID, sess, items)
	return c.JSON(http.StatusOK, map[string]any{
		"items":                      items,
		"target_id":                  located.TargetID,
		"target_external_message_id": externalMessageID,
	})
}

func parseBoundedInt32(raw string, fallback int32, minValue int32, maxValue int32) int32 {
	value := fallback
	if s := strings.TrimSpace(raw); s != "" {
		if n, err := strconv.ParseInt(s, 10, 32); err == nil {
			value = int32(n)
		}
	}
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func (h *MessageHandler) backgroundTaskSnapshots(botID, sessionID string) []chatview.UIBackgroundTask {
	if h.bgManager == nil {
		return nil
	}
	snapshots := h.bgManager.ListSnapshotsForSession(botID, sessionID)
	tasks := make([]chatview.UIBackgroundTask, 0, len(snapshots))
	for _, snapshot := range snapshots {
		status := string(snapshot.Status)
		stalled := snapshot.Stalled
		if stalled {
			status = "stalled"
		}
		label := snapshot.Command
		if label == "" {
			label = snapshot.Description
		}
		tasks = append(tasks, chatview.UIBackgroundTask{
			TaskID:         snapshot.TaskID,
			Status:         status,
			Command:        label,
			AgentID:        snapshot.AgentID,
			AgentSessionID: snapshot.AgentSessionID,
			OutputFile:     snapshot.OutputFile,
			ExitCode:       snapshot.ExitCode,
			Duration:       snapshot.Duration.Round(time.Millisecond).String(),
			OutputTail:     firstNonEmptyString(snapshot.OutputTail, snapshot.AgentReport, snapshot.AgentError),
			Stalled:        stalled,
		})
	}
	return tasks
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func (h *MessageHandler) toolApprovalCanApproveFn(sess session.Thread) func(toolapproval.Request) bool {
	defaultFn := func(req toolapproval.Request) bool {
		return toolapproval.CanApprove(req.Status)
	}
	if h == nil || h.toolApproval == nil || !session.IsACPRuntime(sess) {
		return defaultFn
	}
	return h.toolApproval.CanRespond
}

func (h *MessageHandler) decorateUITurns(ctx context.Context, botID, sessionID string, sess session.Thread, items []chatview.UITurn) {
	if len(items) == 0 {
		return
	}
	if h.bgManager != nil {
		chatview.ApplyBackgroundTaskSnapshots(items, h.backgroundTaskSnapshots(botID, sessionID))
	}
	toolCallIDs := toolCallIDsFromUITurns(items)
	if len(toolCallIDs) == 0 {
		return
	}
	var wg sync.WaitGroup
	var approvals []toolapproval.Request
	var requests []userinput.Request
	if h.toolApproval != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rows, err := h.toolApproval.ListBySessionToolCalls(ctx, botID, sessionID, toolCallIDs); err == nil {
				approvals = rows
			}
		}()
	}
	if h.userInput != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rows, err := h.userInput.ListBySessionToolCalls(ctx, botID, sessionID, toolCallIDs); err == nil {
				requests = rows
			}
		}()
	}
	wg.Wait()
	if len(approvals) > 0 {
		mergeToolApprovals(items, approvals, h.toolApprovalCanApproveFn(sess))
	}
	if len(requests) > 0 {
		mergeUserInputs(items, requests, h.userInput.CanRespond)
	}
}

func toolCallIDsFromUITurns(turns []chatview.UITurn) []string {
	var seen map[string]struct{}
	var ids []string
	for _, turn := range turns {
		for _, msg := range turn.Messages {
			if msg.Type != chatview.UIMessageTool {
				continue
			}
			id := strings.TrimSpace(msg.ToolCallID)
			if id == "" {
				continue
			}
			if seen == nil {
				seen = make(map[string]struct{}, 4)
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	return ids
}

func mergeToolApprovals(turns []chatview.UITurn, approvals []toolapproval.Request, canApproveFn func(toolapproval.Request) bool) {
	if len(turns) == 0 || len(approvals) == 0 {
		return
	}
	if canApproveFn == nil {
		canApproveFn = func(req toolapproval.Request) bool {
			return toolapproval.CanApprove(req.Status)
		}
	}
	byCallID := make(map[string]toolapproval.Request, len(approvals))
	for _, approval := range approvals {
		if callID := strings.TrimSpace(approval.ToolCallID); callID != "" {
			byCallID[callID] = approval
		}
	}
	for turnIdx := range turns {
		if turns[turnIdx].Role != "assistant" {
			continue
		}
		for msgIdx := range turns[turnIdx].Messages {
			//nolint:gosec // G602 false positive: msgIdx comes from range over this slice
			msg := &turns[turnIdx].Messages[msgIdx]
			if msg.Type != chatview.UIMessageTool {
				continue
			}
			approval, ok := byCallID[strings.TrimSpace(msg.ToolCallID)]
			if !ok {
				continue
			}
			running := false
			msg.Running = &running
			msg.Approval = &chatview.UIToolApproval{
				ApprovalID:     approval.ID,
				ShortID:        approval.ShortID,
				Status:         approval.Status,
				DecisionReason: approval.DecisionReason,
				CanApprove:     canApproveFn(approval),
			}
		}
	}
}

func mergeUserInputs(turns []chatview.UITurn, requests []userinput.Request, canRespondFn func(userinput.Request) bool) {
	if len(turns) == 0 || len(requests) == 0 {
		return
	}
	byCallID := make(map[string]userinput.Request, len(requests))
	for _, req := range requests {
		if callID := strings.TrimSpace(req.ToolCallID); callID != "" {
			byCallID[callID] = req
		}
	}
	for turnIdx := range turns {
		if turns[turnIdx].Role != "assistant" {
			continue
		}
		for msgIdx := range turns[turnIdx].Messages {
			msg := &turns[turnIdx].Messages[msgIdx]
			if msg.Type != chatview.UIMessageTool {
				continue
			}
			req, ok := byCallID[strings.TrimSpace(msg.ToolCallID)]
			if !ok {
				continue
			}
			running := false
			msg.Running = &running
			canRespond := req.Status == userinput.StatusPending
			if canRespondFn != nil {
				canRespond = canRespondFn(req)
			}
			msg.UserInput = &chatview.UIUserInput{
				UserInputID: req.ID,
				ShortID:     req.ShortID,
				Status:      req.Status,
				Questions:   req.UIPayload.Questions,
				CanRespond:  canRespond,
			}
		}
	}
}

// fillAssetMimeFromStorage fills mime, storage_key, size_bytes from storage (soft link: DB only has content_hash).
func (h *MessageHandler) fillAssetMimeFromStorage(ctx context.Context, botID string, messages []messagepkg.Message) {
	if h.mediaService == nil {
		return
	}
	for i := range messages {
		for j := range messages[i].Assets {
			a := &messages[i].Assets[j] //nolint:gosec // G602: j is bounded by range loop
			if strings.TrimSpace(a.ContentHash) == "" {
				continue
			}
			asset, err := h.mediaService.Resolve(ctx, botID, a.ContentHash)
			if err != nil {
				continue
			}
			a.Mime = asset.Mime
			a.StorageKey = asset.StorageKey
			a.SizeBytes = asset.SizeBytes
		}
	}
}

func parseBeforeParam(s string) (time.Time, bool) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, trimmed); err == nil {
		return t.UTC(), true
	}
	if t, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return t.UTC(), true
	}
	if epochMillis, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		return time.UnixMilli(epochMillis).UTC(), true
	}
	return time.Time{}, false
}

func reverseMessages(m []messagepkg.Message) {
	for i, j := 0, len(m)-1; i < j; i, j = i+1, j-1 {
		m[i], m[j] = m[j], m[i]
	}
}

// StreamMessageEvents was removed in favor of two narrower streams: a
// per-session messages SSE (see message_stream.go) and a bot-wide lightweight
// sessions activity SSE. Resolves a catch-up explosion where a stale client
// `since=` cursor could force a multi-megabyte replay of bot history.
// extendToUITurnHead prepends older session messages (oldest-first) until the
// slice starts on a real UI turn boundary — a visible user message or a
// background-task system turn. A turn is the unit of an action bar, so when a
// fixed-size page lands in the middle of an assistant turn we pull the turn's
// earlier rows back in. The extension budget keeps one pathologically long turn
// from turning a small UI page into a multi-thousand-row response.
func (h *MessageHandler) extendToUITurnHead(ctx context.Context, sessionID string, messages []messagepkg.Message, limit int32) []messagepkg.Message {
	const batch = int32(50)
	maxRows := uiTurnHeadExtensionLimit(len(messages), limit)
	// messages is oldest-first (ASC). The cursor is the oldest row on the
	// current page, and we pull rows before that row in visible turn order.
	// ListBeforeMessageBySession already returns oldest-first, so prepend each
	// batch directly; the combined slice stays monotonic and the turn converter
	// (which scans in order) keeps one reply in a single turn.
	for len(messages) > 0 && len(messages) < maxRows && !chatview.IsUITurnBoundary(messages[0]) {
		older, err := h.messageService.ListBeforeMessageBySession(ctx, sessionID, messages[0].ID, batch)
		if err != nil || len(older) == 0 {
			break
		}
		fetched := len(older)
		if overflow := len(messages) + len(older) - maxRows; overflow > 0 {
			older = older[overflow:]
		}
		if len(older) == 0 {
			break
		}
		messages = append(older, messages...)
		if fetched < int(batch) {
			break // reached the start of the session
		}
	}
	return messages
}

func uiTurnHeadExtensionLimit(currentRows int, limit int32) int {
	const maxExtendedRows = 200
	pageRows := int(limit)
	if pageRows <= 0 {
		pageRows = currentRows
	}
	if pageRows < currentRows {
		pageRows = currentRows
	}
	maxRows := pageRows * 4
	if minRows := pageRows + 50; maxRows < minRows {
		maxRows = minRows
	}
	if maxRows > maxExtendedRows {
		maxRows = maxExtendedRows
	}
	if maxRows < currentRows {
		return currentRows
	}
	return maxRows
}

// DeleteMessages godoc
// @Summary Delete all bot history messages
// @Description Clear all persisted bot-level history messages
// @Tags messages
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Success 204 "No Content"
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/messages [delete].
func (h *MessageHandler) DeleteMessages(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	bot, err := h.authorizeBotManage(c.Request().Context(), channelIdentityID, botID)
	if err != nil {
		return err
	}
	botID = bot.ID
	if h.messageService == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "message service not configured")
	}
	sessionID := strings.TrimSpace(c.QueryParam("session_id"))
	if sessionID != "" {
		if err := h.messageService.DeleteBySession(c.Request().Context(), sessionID); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
	} else {
		if err := h.messageService.DeleteByBot(c.Request().Context(), botID); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
	}
	return c.NoContent(http.StatusNoContent)
}

// --- helpers ---

func (*MessageHandler) requireChannelIdentityID(c echo.Context) (string, error) {
	return httpx.RequireChannelIdentityID(c)
}

func (h *MessageHandler) authorizeBotAccess(ctx context.Context, channelIdentityID, botID string) (bot.Bot, error) {
	return httpx.AuthorizeBotAccessWithPermission(ctx, h.botService, h.accountService, channelIdentityID, botID, bot.PermissionChat)
}

func (h *MessageHandler) authorizeBotManage(ctx context.Context, channelIdentityID, botID string) (bot.Bot, error) {
	return httpx.AuthorizeBotAccessWithPermission(ctx, h.botService, h.accountService, channelIdentityID, botID, bot.PermissionManage)
}

func (h *MessageHandler) authorizeBotMessageAccess(c echo.Context, channelIdentityID, botID string) (bot.Bot, []string, error) {
	if h.botService == nil || h.accountService == nil {
		return bot.Bot{}, nil, echo.NewHTTPError(http.StatusInternalServerError, "bot services not configured")
	}
	ctx := c.Request().Context()
	isAdmin, err := h.accountService.IsAdmin(ctx, channelIdentityID)
	if err != nil {
		return bot.Bot{}, nil, echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	record, err := h.botService.GetForAccess(ctx, botID)
	if err != nil {
		if errors.Is(err, bot.ErrBotNotFound) {
			return bot.Bot{}, nil, echo.NewHTTPError(http.StatusNotFound, "bot not found")
		}
		return bot.Bot{}, nil, echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	perms, err := h.botService.ResolveUserPermissionsForBot(ctx, record, channelIdentityID, isAdmin)
	if err != nil {
		if errors.Is(err, bot.ErrBotNotFound) {
			return bot.Bot{}, nil, echo.NewHTTPError(http.StatusNotFound, "bot not found")
		}
		return bot.Bot{}, nil, echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if !bot.HasPermission(perms, bot.PermissionChat) && !bot.HasPermission(perms, bot.PermissionWorkspaceExec) {
		return bot.Bot{}, nil, echo.NewHTTPError(http.StatusForbidden, "bot access denied")
	}
	return record, perms, nil
}

func (h *MessageHandler) authorizeMessageSession(c echo.Context, channelIdentityID, botID, sessionID string) (bot.Bot, []string, session.Thread, error) {
	if h.sessionService == nil {
		return bot.Bot{}, nil, session.Thread{}, echo.NewHTTPError(http.StatusInternalServerError, "session service not configured")
	}
	record, perms, err := h.authorizeBotMessageAccess(c, channelIdentityID, botID)
	if err != nil {
		return bot.Bot{}, nil, session.Thread{}, err
	}
	sess, err := h.sessionService.Get(c.Request().Context(), sessionID)
	if err != nil || sess.BotID != record.ID {
		return bot.Bot{}, nil, session.Thread{}, echo.NewHTTPError(http.StatusNotFound, "session not found")
	}
	if !canAccessSession(sess, channelIdentityID, perms) {
		return bot.Bot{}, nil, session.Thread{}, echo.NewHTTPError(http.StatusNotFound, "session not found")
	}
	return record, perms, sess, nil
}

// ServeMedia streams a media asset by bot_id + content_hash with read-access authorization.
func (h *MessageHandler) ServeMedia(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	contentHash := strings.TrimSpace(c.Param("content_hash"))
	if contentHash == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "content hash is required")
	}
	bot, err := h.authorizeBotAccess(c.Request().Context(), channelIdentityID, botID)
	if err != nil {
		return err
	}
	botID = bot.ID
	if h.mediaService == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "media service not configured")
	}
	reader, asset, err := h.mediaService.Open(c.Request().Context(), botID, contentHash)
	if err != nil {
		if errors.Is(err, media.ErrAssetNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "asset not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	defer func() { _ = reader.Close() }()
	contentType := asset.Mime
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	// Media assets have no size ceiling, so serving one must not race the server
	// WriteTimeout.
	if err := httpx.ClearWriteDeadline(c); err != nil {
		return err
	}
	c.Response().Header().Set("Content-Type", contentType)
	c.Response().Header().Set("Cache-Control", "private, max-age=86400")
	c.Response().WriteHeader(http.StatusOK)
	if _, err := io.Copy(c.Response().Writer, reader); err != nil {
		h.logger.Warn("serve media stream failed", slog.Any("error", err))
	}
	return nil
}
