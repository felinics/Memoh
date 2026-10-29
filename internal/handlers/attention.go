package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/chat/attention"
	messageevent "github.com/felinics/memoh/internal/chat/event"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/errs"
)

// attentionService is the unread state the handlers read and move.
type attentionService interface {
	SetReadState(ctx context.Context, userID string, changes []attention.ReadStateChange) ([]attention.Attention, error)
	ListUnread(ctx context.Context, userID string) ([]attention.Attention, error)
	Annotate(ctx context.Context, userID string, threads []session.Thread) ([]session.Thread, error)
}

const attentionStreamBuffer = 64

func (h *MessageHandler) SetAttentionService(svc attentionService) {
	h.attention = svc
}

func (h *SessionHandler) SetAttentionService(svc attentionService) {
	h.attention = svc
}

// SetReadStateRequest writes the caller's read state of one or more sessions.
type SetReadStateRequest struct {
	Items []attention.ReadStateChange `json:"items"`
}

// SetReadStateResponse lists the sessions this request left unread.
type SetReadStateResponse struct {
	Items []attention.Attention `json:"items"`
}

// UnreadSessionsResponse lists the caller's unread sessions across bots.
type UnreadSessionsResponse struct {
	Items []attention.Attention `json:"items"`
}

// SetReadState godoc
// @Summary Set the caller's read state of sessions
// @Description Marks each session read (through the attention time the client
// @Description displayed) or unread. Read and unread are forward-only marks; the
// @Description later one wins. Sessions the caller did not create, or that carry no
// @Description unread state, are skipped. Marking unread never notifies.
// @Tags sessions
// @Accept json
// @Produce json
// @Param payload body SetReadStateRequest true "Read state changes"
// @Success 200 {object} SetReadStateResponse
// @Failure 400 {object} server.Problem
// @Router /users/me/read-state [put].
func (h *MessageHandler) SetReadState(c echo.Context) error {
	if h.attention == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "attention service not configured")
	}
	userID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	var req SetReadStateRequest
	if err := c.Bind(&req); err != nil {
		return apperror.FieldInvalid("items", err)
	}
	items, err := h.attention.SetReadState(c.Request().Context(), userID, req.Items)
	if errors.Is(err, attention.ErrThroughRequired) {
		return echo.NewHTTPError(http.StatusBadRequest, "reading a session requires through")
	}
	if err != nil {
		return errs.Wrap(err, "set read state")
	}
	access := h.botAccessCache(c, userID)
	visible := make([]attention.Attention, 0, len(items))
	for _, item := range items {
		if access(item.BotID) {
			visible = append(visible, item)
		}
	}
	return c.JSON(http.StatusOK, SetReadStateResponse{Items: visible})
}

// ListUnreadSessions godoc
// @Summary List the caller's unread sessions
// @Description Sessions the caller created that need their attention, across
// @Description every bot they can still access, newest first. No message bodies.
// @Tags sessions
// @Produce json
// @Success 200 {object} UnreadSessionsResponse
// @Failure 500 {object} server.Problem
// @Router /users/me/unread [get].
func (h *MessageHandler) ListUnreadSessions(c echo.Context) error {
	if h.attention == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "attention service not configured")
	}
	userID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	items, err := h.attention.ListUnread(c.Request().Context(), userID)
	if err != nil {
		return errs.Wrap(err, "list unread sessions")
	}
	access := h.botAccessCache(c, userID)
	visible := make([]attention.Attention, 0, len(items))
	for _, item := range items {
		if access(item.BotID) {
			visible = append(visible, item)
		}
	}
	return c.JSON(http.StatusOK, UnreadSessionsResponse{Items: visible})
}

// StreamAttentionEvents godoc
// @Summary Stream the caller's attention events
// @Description SSE addressed to the caller: session_attention when one of
// @Description their sessions needs them, session_unread when they mark one
// @Description unread, session_read when their read cursor moves. Starts with attention_ready; never carries message bodies.
// @Tags sessions
// @Produce text/event-stream
// @Success 200 {string} string "SSE stream"
// @Failure 500 {object} server.Problem
// @Router /users/me/attention/events [get].
func (h *MessageHandler) StreamAttentionEvents(c echo.Context) error {
	userID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	if h.messageEvents == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "message events not configured")
	}
	writer, flusher, err := beginSSEResponse(c)
	if err != nil {
		return err
	}
	sub, cancel := h.messageEvents.Subscribe(messageevent.UserTopic(userID), attentionStreamBuffer)
	defer cancel()
	if err := writeSSEJSON(writer, flusher, map[string]any{"type": "attention_ready"}); err != nil {
		return nil
	}

	// A healthy stream can live indefinitely. Recheck each attention so a
	// revoked grant cannot keep leaking session titles over an old connection.
	access := func(botID string) bool {
		if strings.TrimSpace(botID) == "" {
			return false
		}
		_, _, err := h.authorizeBotMessageAccess(c, userID, botID)
		return err == nil
	}
	heartbeat := time.NewTicker(sseHeartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case <-c.Request().Context().Done():
			return nil
		case <-heartbeat.C:
			if err := writeSSEJSON(writer, flusher, map[string]any{"type": "ping"}); err != nil {
				return nil
			}
		case event, ok := <-sub.Events:
			if !ok {
				return nil
			}
			if dropped := sub.DroppedSinceLastRead(); dropped > 0 {
				if err := writeSSEJSON(writer, flusher, map[string]any{"type": "dropped", "count": dropped}); err != nil {
					return nil
				}
			}
			frame := attentionFrame(event, access)
			if frame == nil {
				continue
			}
			if err := writeSSEJSON(writer, flusher, frame); err != nil {
				return nil
			}
		}
	}
}

// attentionFrame turns a user-topic event into its wire frame, or nil when the
// event is not for this stream or names a bot the user can no longer access.
func attentionFrame(event messageevent.Event, access func(botID string) bool) map[string]any {
	switch event.Type {
	case messageevent.EventTypeSessionAttention, messageevent.EventTypeSessionUnread:
		var payload attention.Attention
		if json.Unmarshal(event.Data, &payload) != nil || !access(payload.BotID) {
			return nil
		}
		return map[string]any{
			"type": string(event.Type), "bot_id": payload.BotID, "session_id": payload.SessionID,
			"title": payload.Title, "attention_kind": payload.Kind, "attention_at": payload.At,
			"read_through": payload.ReadThrough, "marked": payload.Marked,
		}
	case messageevent.EventTypeSessionRead:
		var payload attention.Read
		if json.Unmarshal(event.Data, &payload) != nil {
			return nil
		}
		return map[string]any{"type": "session_read", "session_id": payload.SessionID, "read_at": payload.ReadAt}
	}
	return nil
}

// botAccessCache answers whether userID may read messages of a bot,
// remembering each answer for the lifetime of the request.
func (h *MessageHandler) botAccessCache(c echo.Context, userID string) func(botID string) bool {
	known := map[string]bool{}
	return func(botID string) bool {
		botID = strings.TrimSpace(botID)
		if botID == "" {
			return false
		}
		if allowed, ok := known[botID]; ok {
			return allowed
		}
		_, _, err := h.authorizeBotMessageAccess(c, userID, botID)
		known[botID] = err == nil
		return known[botID]
	}
}
