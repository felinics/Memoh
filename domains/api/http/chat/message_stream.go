package chat

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	messageevent "github.com/memohai/memoh/domains/agent/chat/event"
	messagepkg "github.com/memohai/memoh/domains/agent/chat/message"
	session "github.com/memohai/memoh/domains/agent/chat/thread"
	"github.com/memohai/memoh/domains/api/bot"
	httpx "github.com/memohai/memoh/domains/api/http/httpx"
)

// sessionMessageBacklogSize is the server-fixed number of backlog messages
// pushed when a client subscribes to a session message stream. Bounding the
// backlog server-side prevents the catch-up explosion that a client-supplied
// cursor allowed: a stale `since=` could replay the entire bot history.
const sessionMessageBacklogSize = 50

// sessionMessageStreamBuffer sizes the per-subscriber channel for both SSE
// streams. The per-session stream is the high-rate path (assistant token
// bursts); the activity stream's traffic is far lower but reuses the same
// constant so a single tuning knob covers both.
const sessionMessageStreamBuffer = 128

// sseHeartbeatInterval is the keep-alive cadence — tuned to land under a
// 30s proxy idle cut.
const sseHeartbeatInterval = 20 * time.Second

// StreamSessionMessageEvents godoc
// @Summary Stream message events for one session
// @Description SSE stream that pushes a server-fixed backlog of the last 50
// @Description messages, then streams future message_created and
// @Description session_title_updated events scoped to this session only.
// @Tags messages
// @Produce text/event-stream
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Success 200 {string} string "SSE stream"
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/sessions/{session_id}/messages/events [get].
func (h *MessageHandler) StreamSessionMessageEvents(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	sessionID := strings.TrimSpace(c.Param("session_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if sessionID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "session id is required")
	}
	if h.messageService == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "message service not configured")
	}
	if h.messageEvents == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "message events not configured")
	}

	bot, _, _, err := h.authorizeMessageSession(c, channelIdentityID, botID, sessionID)
	if err != nil {
		return err
	}
	botID = bot.ID

	// Subscribe BEFORE the backlog read so any message persisted during the
	// DB call lands in the live channel. We then dedup against the backlog
	// IDs so the client never sees a message twice across the seam.
	//
	// Authorization is checked at connection time only. The SSE reconnect
	// cycle (~30s on typical proxies) re-runs the ACL, so revocations
	// propagate within one reconnect window.
	sub, cancel := h.messageEvents.Subscribe(botID, sessionMessageStreamBuffer)
	defer cancel()

	backlog, err := h.messageService.ListLatestBySession(c.Request().Context(), sessionID, sessionMessageBacklogSize)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	writer, flusher, err := beginSSEResponse(c)
	if err != nil {
		return err
	}

	reverseMessages(backlog)
	h.fillAssetMimeFromStorage(c.Request().Context(), botID, backlog)
	backlogIDs := make(map[string]struct{}, len(backlog))
	for _, message := range backlog {
		backlogIDs[message.ID] = struct{}{}
		if err := writeMessageCreated(writer, flusher, botID, message); err != nil {
			return nil
		}
	}

	heartbeat := time.NewTicker(sseHeartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-c.Request().Context().Done():
			return nil
		case <-heartbeat.C:
			if err := httpx.WriteSSEJSON(writer, flusher, map[string]any{"type": "ping"}); err != nil {
				return nil
			}
		case event, ok := <-sub.Events:
			if !ok {
				return nil
			}
			// Emit a `dropped` frame BEFORE the next normal event when the
			// hub's per-subscription buffer overflowed since the previous
			// read. The client treats this as "your view is stale; refresh
			// via REST" — see the dropped-event docs on Subscription.
			if dropped := sub.DroppedSinceLastRead(); dropped > 0 {
				if err := httpx.WriteSSEJSON(writer, flusher, map[string]any{
					"type":  "dropped",
					"count": dropped,
				}); err != nil {
					return nil
				}
			}
			if strings.TrimSpace(event.BotID) != botID || len(event.Data) == 0 {
				continue
			}
			switch event.Type {
			case messageevent.EventTypeMessageCreated:
				var message messagepkg.Message
				if err := json.Unmarshal(event.Data, &message); err != nil {
					h.logger.Warn("decode message_created event failed",
						slog.String("session_id", sessionID),
						slog.Any("error", err),
					)
					continue
				}
				if message.SessionID != sessionID {
					continue
				}
				// Skip messages already delivered as part of the backlog —
				// the Subscribe-before-backlog ordering keeps the seam
				// race-free at the cost of a small dedup set.
				if _, dup := backlogIDs[message.ID]; dup {
					continue
				}
				h.fillAssetMimeFromStorage(c.Request().Context(), botID, []messagepkg.Message{message})
				if err := writeMessageCreated(writer, flusher, botID, message); err != nil {
					return nil
				}
			case messageevent.EventTypeSessionTitleUpdated:
				var payload map[string]string
				if err := json.Unmarshal(event.Data, &payload); err != nil {
					h.logger.Warn("decode session_title_updated event failed",
						slog.String("session_id", sessionID),
						slog.Any("error", err),
					)
					continue
				}
				if payload["session_id"] != sessionID {
					continue
				}
				if err := httpx.WriteSSEJSON(writer, flusher, map[string]any{
					"type":       string(messageevent.EventTypeSessionTitleUpdated),
					"bot_id":     botID,
					"session_id": sessionID,
					"title":      payload["title"],
				}); err != nil {
					return nil
				}
			case messageevent.EventTypeBackgroundTask:
				// Forward only to the owning session. The old bot-wide
				// stream carried these for every active session; if we
				// drop them here the chat UI loses live background-task
				// updates for the focused session.
				var payload map[string]any
				if err := json.Unmarshal(event.Data, &payload); err != nil {
					h.logger.Warn("decode forwarded event failed",
						slog.String("event_type", string(event.Type)),
						slog.String("session_id", sessionID),
						slog.Any("error", err),
					)
					continue
				}
				if payloadSessionID(payload) != sessionID {
					continue
				}
				payload["type"] = string(event.Type)
				payload["bot_id"] = botID
				if err := httpx.WriteSSEJSON(writer, flusher, payload); err != nil {
					return nil
				}
			}
		}
	}
}

// StreamSessionsActivityEvents godoc
// @Summary Stream bot-wide sessions activity
// @Description Lightweight SSE for sidebar live-sort. Carries only session
// @Description identifiers and minimal metadata (touched timestamps, titles).
// @Description Never includes message bodies. Filters out internal session
// @Description types such as heartbeat, schedule, subagent.
// @Tags messages
// @Produce text/event-stream
// @Param bot_id path string true "Bot ID"
// @Success 200 {string} string "SSE stream"
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/sessions/events [get].
func (h *MessageHandler) StreamSessionsActivityEvents(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	bot, perms, err := h.authorizeBotMessageAccess(c, channelIdentityID, botID)
	if err != nil {
		return err
	}
	botID = bot.ID
	if h.messageEvents == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "message events not configured")
	}
	if h.sessionService == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "session service not configured")
	}

	writer, flusher, err := beginSSEResponse(c)
	if err != nil {
		return err
	}

	// Authorization is checked at connection time only. The SSE reconnect
	// cycle (~30s on typical proxies) re-runs the ACL, so revocations
	// propagate within one reconnect window.
	sub, cancel := h.messageEvents.Subscribe(botID, sessionMessageStreamBuffer)
	defer cancel()

	cache := newSessionCache(h.logger, h.sessionService)

	heartbeat := time.NewTicker(sseHeartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-c.Request().Context().Done():
			return nil
		case <-heartbeat.C:
			if err := httpx.WriteSSEJSON(writer, flusher, map[string]any{"type": "ping"}); err != nil {
				return nil
			}
		case event, ok := <-sub.Events:
			if !ok {
				return nil
			}
			// Emit a `dropped` frame BEFORE the next normal event when the
			// hub's per-subscription buffer overflowed since the previous
			// read. The client treats this as "your view is stale; refresh
			// via REST" — see the dropped-event docs on Subscription.
			if dropped := sub.DroppedSinceLastRead(); dropped > 0 {
				if err := httpx.WriteSSEJSON(writer, flusher, map[string]any{
					"type":  "dropped",
					"count": dropped,
				}); err != nil {
					return nil
				}
			}
			if strings.TrimSpace(event.BotID) != botID || len(event.Data) == 0 {
				continue
			}

			switch event.Type {
			case messageevent.EventTypeMessageCreated:
				var message messagepkg.Message
				if err := json.Unmarshal(event.Data, &message); err != nil {
					h.logger.Warn("activity stream: decode message_created event failed",
						slog.String("bot_id", botID),
						slog.Any("error", err),
					)
					continue
				}
				if !canDeliverSessionActivity(c.Request().Context(), channelIdentityID, botID, perms, cache, message.SessionID) {
					continue
				}
				if err := httpx.WriteSSEJSON(writer, flusher, map[string]any{
					"type":       "session_touched",
					"session_id": message.SessionID,
					"updated_at": message.CreatedAt,
				}); err != nil {
					return nil
				}
			case messageevent.EventTypeSessionTitleUpdated:
				var payload map[string]string
				if err := json.Unmarshal(event.Data, &payload); err != nil {
					h.logger.Warn("activity stream: decode session_title_updated event failed",
						slog.String("bot_id", botID),
						slog.Any("error", err),
					)
					continue
				}
				sessionID := strings.TrimSpace(payload["session_id"])
				if !canDeliverSessionActivity(c.Request().Context(), channelIdentityID, botID, perms, cache, sessionID) {
					continue
				}
				// Match the frontend's bot-activity wire name. The per-session
				// stream emits `session_title_updated`; the bot-wide activity
				// stream (consumed by the sidebar) expects `session_title_changed`.
				if err := httpx.WriteSSEJSON(writer, flusher, map[string]any{
					"type":       "session_title_changed",
					"session_id": sessionID,
					"title":      payload["title"],
				}); err != nil {
					return nil
				}
			case messageevent.EventTypeSessionCreated:
				var payload map[string]any
				if err := json.Unmarshal(event.Data, &payload); err != nil {
					h.logger.Warn("activity stream: decode session_created event failed",
						slog.String("bot_id", botID),
						slog.Any("error", err),
					)
					continue
				}
				sessionID, _ := payload["session_id"].(string)
				sessionID = strings.TrimSpace(sessionID)
				if sessionID == "" {
					continue
				}
				typ, _ := payload["type"].(string)
				if !session.IsUserFacingType(typ) {
					continue
				}
				if !canDeliverSessionActivity(c.Request().Context(), channelIdentityID, botID, perms, cache, sessionID) {
					continue
				}
				out := map[string]any{
					"type":       "session_created",
					"session_id": sessionID,
				}
				if title, ok := payload["title"].(string); ok && title != "" {
					out["title"] = title
				}
				if createdAt, ok := payload["created_at"].(string); ok && createdAt != "" {
					out["created_at"] = createdAt
				}
				if err := httpx.WriteSSEJSON(writer, flusher, out); err != nil {
					return nil
				}
			}
		}
	}
}

// canDeliverSessionActivity returns true when the subscriber may see an
// activity event for sessionID: the session must be a user-facing type AND
// the subscriber must have read access to it. The session row is loaded
// at most once per stream — both the user-facing and access checks read
// from the cached value.
func canDeliverSessionActivity(ctx context.Context, channelIdentityID, botID string, perms []string, cache *sessionCache, sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	sess, ok := cache.get(ctx, sessionID)
	if !ok {
		return false
	}
	if !session.IsUserFacingType(sess.Type) {
		return false
	}
	return canReadMessageSessionFromCache(sess, channelIdentityID, botID, perms)
}

// canReadMessageSessionFromCache mirrors canReadMessageSession but skips the
// session.Get call by accepting an already-loaded session. The authorization
// policy lives in canAccessSession; this is the cached-path entry point.
func canReadMessageSessionFromCache(sess session.Thread, channelIdentityID, botID string, perms []string) bool {
	if bot.HasPermission(perms, bot.PermissionManage) {
		return true
	}
	if sess.BotID != botID {
		return false
	}
	return canAccessSession(sess, channelIdentityID, perms)
}

func writeMessageCreated(writer io.Writer, flusher http.Flusher, botID string, message messagepkg.Message) error {
	return httpx.WriteSSEJSON(writer, flusher, map[string]any{
		"type":    string(messageevent.EventTypeMessageCreated),
		"bot_id":  botID,
		"message": message,
	})
}

// setSSEHeaders applies the response headers shared by every SSE endpoint.
// X-Accel-Buffering disables per-response proxy buffering in nginx (and
// compatible reverse proxies) so events reach the browser as soon as the
// handler flushes them, without requiring proxy-side location tuning.

func beginSSEResponse(c echo.Context) (io.Writer, http.Flusher, error) {
	if err := httpx.PrepareSSE(c); err != nil {
		return nil, nil, err
	}
	c.Response().WriteHeader(http.StatusOK)
	flusher, ok := c.Response().Writer.(http.Flusher)
	if !ok {
		return nil, nil, echo.NewHTTPError(http.StatusInternalServerError, "streaming not supported")
	}
	return c.Response().Writer, flusher, nil
}

// sessionCache memoizes the session row for the lifetime of one activity
// stream. Without it every delivered event would issue two DB reads — one
// for the user-facing-type check and one for the access check. Both checks
// now read the same cached value, so the first event for a session pays a
// single Get and subsequent events for that session are DB-free.
//
// Caching the full row is safe because the only mutable bit either check
// reads is `Type`, which IsUserFacingType doesn't filter on for an already
// admitted session, and CreatedByUserID, which is immutable.
//
// The cache is stream-local and consulted only from the single goroutine
// that drives the SSE writer loop, so no synchronization is needed.
type sessionCache struct {
	logger *slog.Logger
	svc    *session.Service
	rows   map[string]session.Thread
	misses map[string]struct{}
}

func newSessionCache(logger *slog.Logger, svc *session.Service) *sessionCache {
	if logger == nil {
		logger = slog.Default()
	}
	return &sessionCache{
		logger: logger,
		svc:    svc,
		rows:   map[string]session.Thread{},
		misses: map[string]struct{}{},
	}
}

// get returns the cached session, loading it on first access. The second
// return value is false when the session cannot be loaded — either because
// the service is not configured or because the DB lookup failed (which is
// logged so an outage doesn't masquerade as normal filtering).
func (c *sessionCache) get(ctx context.Context, sessionID string) (session.Thread, bool) {
	if cached, ok := c.rows[sessionID]; ok {
		return cached, true
	}
	if _, missed := c.misses[sessionID]; missed {
		return session.Thread{}, false
	}
	if c.svc == nil {
		return session.Thread{}, false
	}
	sess, err := c.svc.Get(ctx, sessionID)
	if err != nil {
		c.logger.WarnContext(ctx, "activity stream: load session failed",
			slog.String("session_id", sessionID),
			slog.Any("error", err),
		)
		c.misses[sessionID] = struct{}{}
		return session.Thread{}, false
	}
	c.rows[sessionID] = sess
	return sess, true
}

// payloadSessionID extracts the session id from an event payload. All in-tree
// publishers (BackgroundTask, MessageCreated, …) lift `session_id`
// to the top level of the payload; the producer-side contract is pinned by
// the helper tests in domains/agent/event/payload, so this is a single lookup.
func payloadSessionID(payload map[string]any) string {
	v, _ := payload["session_id"].(string)
	return v
}
