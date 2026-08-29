package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/bots"
	messageevent "github.com/felinics/memoh/internal/chat/event"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	session "github.com/felinics/memoh/internal/chat/thread"
)

// sessionMessageStreamBuffer sizes the per-subscriber channel for the activity
// stream. Its traffic is low — one frame per session touch, never a message
// body — so the buffer only has to absorb a burst of concurrent sessions.
const sessionMessageStreamBuffer = 128

// sseHeartbeatInterval is the keep-alive cadence — tuned to land under a
// 30s proxy idle cut.
const sseHeartbeatInterval = 20 * time.Second

// StreamSessionsActivityEvents godoc
// @Summary Stream bot-wide sessions activity
// @Description Lightweight SSE for sidebar live-sort. Carries only session
// @Description identifiers and minimal metadata (touched timestamps, titles).
// @Description Never includes message bodies. Filters out internal session
// @Description types such as schedule and subagent.
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
			if err := writeSSEJSON(writer, flusher, map[string]any{"type": "ping"}); err != nil {
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
				if err := writeSSEJSON(writer, flusher, map[string]any{
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
				if err := writeSSEJSON(writer, flusher, map[string]any{
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
				if err := writeSSEJSON(writer, flusher, map[string]any{
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
				if err := writeSSEJSON(writer, flusher, out); err != nil {
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
	if bots.HasPermission(perms, bots.PermissionManage) {
		return true
	}
	if sess.BotID != botID {
		return false
	}
	return canAccessSession(sess, channelIdentityID, perms)
}

// setSSEHeaders applies the response headers shared by every SSE endpoint.
// X-Accel-Buffering disables per-response proxy buffering in nginx (and
// compatible reverse proxies) so events reach the browser as soon as the
// handler flushes them, without requiring proxy-side location tuning.
func setSSEHeaders(c echo.Context) {
	c.Response().Header().Set(echo.HeaderContentType, "text/event-stream")
	c.Response().Header().Set(echo.HeaderCacheControl, "no-cache")
	c.Response().Header().Set(echo.HeaderConnection, "keep-alive")
	c.Response().Header().Set("X-Accel-Buffering", "no")
}

func beginSSEResponse(c echo.Context) (io.Writer, http.Flusher, error) {
	setSSEHeaders(c)
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
		c.logger.Warn("activity stream: load session failed",
			slog.String("session_id", sessionID),
			slog.Any("error", err),
		)
		c.misses[sessionID] = struct{}{}
		return session.Thread{}, false
	}
	c.rows[sessionID] = sess
	return sess, true
}
