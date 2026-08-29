package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/agent/background"
	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	chatview "github.com/felinics/memoh/internal/agent/view"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	messageevent "github.com/felinics/memoh/internal/chat/event"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/media"
)

// MessageHandler handles bot-scoped messaging endpoints.
type MessageHandler struct {
	messageService messagepkg.Service
	sessionService *session.Service
	runtimeResets  messageRuntimeResetService
	messageEvents  messageevent.Subscriber
	mediaService   *media.Service
	botService     *bots.Service
	accountService *accounts.Service
	toolApproval   *toolapproval.Service
	userInput      *userinput.Service
	bgManager      *background.Manager
	logger         *slog.Logger
}

// runtimeResetService is intentionally a narrow handler-owned port. Clearing the
// canonical timeline must also discard any process-local ACP conversation;
// otherwise the next turn would silently repopulate history from the old
// native session even though the UI was cleared.
type messageRuntimeResetService interface {
	BeginSessionHistoryReset(ctx context.Context, botID, sessionID string) (resetCtx context.Context, release func(), err error)
	BeginBotHistoryReset(ctx context.Context, botID string) (resetCtx context.Context, release func(), err error)
}

// UIMessageListResponse is the normalized, authoritative session history read by Web.
type UIMessageListResponse struct {
	Items []chatview.UITurn `json:"items" validate:"required"`
}

// UILocateMessageResponse is a normalized history window around one external message.
type UILocateMessageResponse struct {
	Items                   []chatview.UITurn `json:"items" validate:"required"`
	TargetID                string            `json:"target_id" validate:"required" format:"uuid"`
	TargetExternalMessageID string            `json:"target_external_message_id" validate:"required"`
}

// NewMessageHandler creates a MessageHandler.
func NewMessageHandler(log *slog.Logger, messageService messagepkg.Service, sessionService *session.Service, botService *bots.Service, accountService *accounts.Service, eventSubscribers ...messageevent.Subscriber) *MessageHandler {
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
func (h *MessageHandler) SetMediaService(svc *media.Service) {
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

func (h *MessageHandler) SetRuntimeResetService(resets messageRuntimeResetService) {
	h.runtimeResets = resets
}

// Register registers all conversation routes.
func (h *MessageHandler) Register(e *echo.Echo) {
	botGroup := e.Group("/bots/:bot_id")
	botGroup.GET("/messages", h.ListMessages)
	botGroup.GET("/messages/locate", h.LocateMessage)
	botGroup.DELETE("/messages", h.DeleteMessages)
	botGroup.GET("/media/:content_hash", h.ServeMedia)

	// Bot-wide activity SSE, carrying only lightweight session metadata (no
	// message bodies) for sidebar live-sort. A session's own contents are read
	// through the session runtime over the chat WebSocket, which is what lets
	// every subscriber of a session agree on what it has seen.
	botGroup.GET("/sessions/events", h.StreamSessionsActivityEvents)
}

// --- Messages ---

func writeSSEData(writer io.Writer, flusher http.Flusher, payload string) error {
	// SSE frames are line-oriented; fold CR/LF to avoid frame injection.
	safePayload := strings.NewReplacer("\r", "\\r", "\n", "\\n").Replace(payload)
	if _, err := io.WriteString(writer, "data: "); err != nil {
		return err
	}
	if _, err := io.WriteString(writer, safePayload); err != nil { //nolint:gosec // G705: SSE body is plain text and CR/LF are escaped above
		return err
	}
	if _, err := io.WriteString(writer, "\n\n"); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func writeSSEJSON(writer io.Writer, flusher http.Flusher, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return writeSSEData(writer, flusher, string(data))
}

// ListMessages godoc
// @Summary List session history messages
// @Description List messages for one session with optional pagination
// @Tags messages
// @Produce json
// @Param bot_id path string true "Bot ID" format(uuid)
// @Param session_id query string true "Session ID" format(uuid)
// @Param limit query int false "Limit" default(30) minimum(1) maximum(100)
// @Param before query string false "Before"
// @Param before_message_id query string false "Message ID cursor before which to page" format(uuid)
// @Success 200 {object} UIMessageListResponse
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
	default:
		messages, err = h.listLatestUIPageBySession(c.Request().Context(), sessionID, limit)
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	// Each page is converted independently, so a page that begins mid assistant
	// turn (its earlier rows on the previous page) would render one reply as
	// several turns/action bars. Extend the head back to a real turn boundary
	// so a turn is never split across pages.
	if len(messages) > 0 {
		messages = h.extendToUITurnHead(c.Request().Context(), sessionID, messages, limit)
	}
	items := chatview.ConvertMessagesToUITurns(messages)
	h.decorateUITurns(c.Request().Context(), botID, sessionID, sess, items)
	return c.JSON(http.StatusOK, UIMessageListResponse{Items: items})
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
	turnID := strings.TrimSpace(messages[start].TurnID)
	for start > 0 && turnID != "" && strings.TrimSpace(messages[start-1].TurnID) == turnID {
		start--
	}
	return messages[start:], nil
}

// LocateMessage godoc
// @Summary Locate a bot history message
// @Description Locate a session message by external message ID and return nearby UI turns
// @Tags messages
// @Produce json
// @Param bot_id path string true "Bot ID" format(uuid)
// @Param session_id query string true "Session ID" format(uuid)
// @Param external_message_id query string true "External message ID"
// @Param before query int false "Messages before target" default(30) minimum(0) maximum(100)
// @Param after query int false "Messages after target" default(30) minimum(0) maximum(100)
// @Success 200 {object} UILocateMessageResponse
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
		if errors.Is(err, pgx.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, "message not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	items := chatview.ConvertMessagesToUITurns(located.Messages)
	h.decorateUITurns(c.Request().Context(), botID, sessionID, sess, items)
	return c.JSON(http.StatusOK, UILocateMessageResponse{
		Items:                   items,
		TargetID:                located.TargetID,
		TargetExternalMessageID: externalMessageID,
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
				ApprovalID:       approval.ID,
				ShortID:          approval.ShortID,
				Status:           approval.Status,
				DecisionReason:   approval.DecisionReason,
				CanApprove:       canApproveFn(approval),
				Options:          uiToolApprovalOptions(approval.Options),
				SelectedOptionID: approval.SelectedOptionID,
			}
		}
	}
}

func uiToolApprovalOptions(options []toolapproval.PermissionOption) []chatview.UIToolApprovalOption {
	if len(options) == 0 {
		return nil
	}
	converted := make([]chatview.UIToolApprovalOption, 0, len(options))
	for _, option := range options {
		converted = append(converted, chatview.UIToolApprovalOption{
			ID:   option.ID,
			Name: option.Name,
			Kind: option.Kind,
		})
	}
	return converted
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
				Answers:     userinput.AnswersFromResult(req.Result),
				CanRespond:  canRespond,
			}
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

// extendToUITurnHead prepends older rows with the same authoritative turn_id.
// A turn is the unit of an action bar, so a fixed-size page must not start in
// the middle of one. The extension budget keeps one pathologically long turn
// from turning a small UI page into a multi-thousand-row response.
func (h *MessageHandler) extendToUITurnHead(ctx context.Context, sessionID string, messages []messagepkg.Message, limit int32) []messagepkg.Message {
	const batch = int32(50)
	maxRows := uiTurnHeadExtensionLimit(len(messages), limit)
	if len(messages) == 0 {
		return messages
	}
	headTurnID := strings.TrimSpace(messages[0].TurnID)
	if headTurnID == "" {
		return messages
	}
	// messages is oldest-first (ASC). The cursor is the oldest row on the
	// current page. Only the matching suffix of an older batch belongs to the
	// same turn; rows before it belong to earlier turns and are not pulled in.
	for len(messages) < maxRows {
		older, err := h.messageService.ListBeforeMessageBySession(ctx, sessionID, messages[0].ID, batch)
		if err != nil || len(older) == 0 {
			break
		}
		fetched := len(older)
		sameTurnStart := len(older)
		for sameTurnStart > 0 && strings.TrimSpace(older[sameTurnStart-1].TurnID) == headTurnID {
			sameTurnStart--
		}
		older = older[sameTurnStart:]
		if len(older) == 0 {
			break
		}
		if overflow := len(messages) + len(older) - maxRows; overflow > 0 {
			older = older[overflow:]
		}
		if len(older) == 0 {
			break
		}
		messages = append(older, messages...)
		if sameTurnStart > 0 || fetched < int(batch) {
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
	ctx := c.Request().Context()
	if h.runtimeResets == nil {
		return apperror.Wrap(
			apperror.CodeSessionHistoryInconsistent,
			errors.New("runtime reset is not configured"),
			nil,
		)
	}
	if sessionID != "" {
		if h.sessionService == nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "session service not configured")
		}
		sess, getErr := h.sessionService.Get(ctx, sessionID)
		if getErr != nil || sess.BotID != botID {
			return echo.NewHTTPError(http.StatusNotFound, "session not found")
		}
		ctx, release, resetErr := h.runtimeResets.BeginSessionHistoryReset(ctx, botID, sessionID)
		if resetErr != nil {
			return apperror.Wrap(apperror.CodeSessionHistoryInconsistent, resetErr, nil)
		}
		defer release()
		if err := h.messageService.DeleteBySession(ctx, sessionID); err != nil {
			return apperror.Wrap(apperror.CodeSessionHistoryInconsistent, err, nil)
		}
	} else {
		ctx, release, resetErr := h.runtimeResets.BeginBotHistoryReset(ctx, botID)
		if resetErr != nil {
			return apperror.Wrap(apperror.CodeSessionHistoryInconsistent, resetErr, nil)
		}
		defer release()
		if err := h.messageService.DeleteByBot(ctx, botID); err != nil {
			return apperror.Wrap(apperror.CodeSessionHistoryInconsistent, err, nil)
		}
	}
	return c.NoContent(http.StatusNoContent)
}

// --- helpers ---

func (*MessageHandler) requireChannelIdentityID(c echo.Context) (string, error) {
	return RequireChannelIdentityID(c)
}

func (h *MessageHandler) authorizeBotAccess(ctx context.Context, channelIdentityID, botID string) (bots.Bot, error) {
	return AuthorizeBotAccessWithPermission(ctx, h.botService, h.accountService, channelIdentityID, botID, bots.PermissionChat)
}

func (h *MessageHandler) authorizeBotManage(ctx context.Context, channelIdentityID, botID string) (bots.Bot, error) {
	return AuthorizeBotAccessWithPermission(ctx, h.botService, h.accountService, channelIdentityID, botID, bots.PermissionManage)
}

func (h *MessageHandler) authorizeBotMessageAccess(c echo.Context, channelIdentityID, botID string) (bots.Bot, []string, error) {
	if h.botService == nil || h.accountService == nil {
		return bots.Bot{}, nil, echo.NewHTTPError(http.StatusInternalServerError, "bot services not configured")
	}
	ctx := c.Request().Context()
	isAdmin, err := h.accountService.IsAdmin(ctx, channelIdentityID)
	if err != nil {
		return bots.Bot{}, nil, echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	bot, err := h.botService.GetForAccess(ctx, botID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return bots.Bot{}, nil, echo.NewHTTPError(http.StatusNotFound, "bot not found")
		}
		return bots.Bot{}, nil, echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	perms, err := h.botService.ResolveUserPermissionsForBot(ctx, bot, channelIdentityID, isAdmin)
	if err != nil {
		if errors.Is(err, bots.ErrBotNotFound) {
			return bots.Bot{}, nil, echo.NewHTTPError(http.StatusNotFound, "bot not found")
		}
		return bots.Bot{}, nil, echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if !bots.HasPermission(perms, bots.PermissionChat) && !bots.HasPermission(perms, bots.PermissionWorkspaceExec) {
		return bots.Bot{}, nil, echo.NewHTTPError(http.StatusForbidden, "bot access denied")
	}
	return bot, perms, nil
}

func (h *MessageHandler) authorizeMessageSession(c echo.Context, channelIdentityID, botID, sessionID string) (bots.Bot, []string, session.Thread, error) {
	if h.sessionService == nil {
		return bots.Bot{}, nil, session.Thread{}, echo.NewHTTPError(http.StatusInternalServerError, "session service not configured")
	}
	bot, perms, err := h.authorizeBotMessageAccess(c, channelIdentityID, botID)
	if err != nil {
		return bots.Bot{}, nil, session.Thread{}, err
	}
	sess, err := h.sessionService.Get(c.Request().Context(), sessionID)
	if err != nil || sess.BotID != bot.ID {
		return bots.Bot{}, nil, session.Thread{}, echo.NewHTTPError(http.StatusNotFound, "session not found")
	}
	if !canAccessSession(sess, channelIdentityID, perms) {
		return bots.Bot{}, nil, session.Thread{}, echo.NewHTTPError(http.StatusNotFound, "session not found")
	}
	return bot, perms, sess, nil
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
	c.Response().Header().Set("Content-Type", contentType)
	c.Response().Header().Set("Cache-Control", "private, max-age=86400")
	c.Response().WriteHeader(http.StatusOK)
	if _, err := io.Copy(c.Response().Writer, reader); err != nil {
		h.logger.Warn("serve media stream failed", slog.Any("error", err))
	}
	return nil
}
