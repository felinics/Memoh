package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/chat/tabmark"
	session "github.com/felinics/memoh/internal/chat/thread"
)

// GUITabMarksHandler exposes the deliverable / handoff marks an agent placed
// on workspace browser tabs so the conversation UI can list them, tell
// whether the tab still exists, and bring it to the front of the workspace
// display.
type GUITabMarksHandler struct {
	logger         *slog.Logger
	marks          *tabmark.Service
	sessions       *session.Service
	botService     *bots.Service
	accountService *accounts.Service
	manager        containerWorkspace
}

// NewGUITabMarksHandler wires the handler.
func NewGUITabMarksHandler(log *slog.Logger, marks *tabmark.Service, sessions *session.Service, botService *bots.Service, accountService *accounts.Service, manager containerWorkspace) *GUITabMarksHandler {
	return &GUITabMarksHandler{
		logger:         log.With(slog.String("handler", "gui_tab_marks")),
		marks:          marks,
		sessions:       sessions,
		botService:     botService,
		accountService: accountService,
		manager:        manager,
	}
}

// Register mounts the routes.
func (h *GUITabMarksHandler) Register(e *echo.Echo) {
	e.GET("/bots/:bot_id/sessions/:session_id/gui-marks", h.List)
	e.POST("/bots/:bot_id/sessions/:session_id/gui-marks/open", h.Open)
	e.POST("/bots/:bot_id/sessions/:session_id/gui-marks/dismiss", h.Dismiss)
}

// GUITabMarkStatus says whether a marked tab can still be opened.
type GUITabMarkStatus string

const (
	// GUITabMarkActive means the tab exists in the workspace browser.
	GUITabMarkActive GUITabMarkStatus = "active"
	// GUITabMarkClosed means the tab is gone (closed, browser restarted, or
	// workspace rebuilt).
	GUITabMarkClosed GUITabMarkStatus = "closed"
	// GUITabMarkUnknown means the browser could not be asked right now.
	GUITabMarkUnknown GUITabMarkStatus = "unknown"
)

// GUITabMark is one mark with its live status.
type GUITabMark struct {
	Key         string           `json:"key"`
	Kind        string           `json:"kind"`
	BrowserID   string           `json:"browser_id"`
	TabID       string           `json:"tab_id"`
	URL         string           `json:"url,omitempty"`
	Title       string           `json:"title,omitempty"`
	SessionName string           `json:"session_name,omitempty"`
	Note        string           `json:"note,omitempty"`
	MarkedAt    time.Time        `json:"marked_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
	ClosedAt    *time.Time       `json:"closed_at,omitempty"`
	Status      GUITabMarkStatus `json:"status"`
}

// GUITabMarksResponse lists the marks of a session.
type GUITabMarksResponse struct {
	Marks []GUITabMark `json:"marks"`
}

// GUITabMarkKeyRequest names one mark.
type GUITabMarkKeyRequest struct {
	Key string `json:"key"`
}

// GUITabMarkOpenResponse reports the outcome of bringing a marked tab to the front.
type GUITabMarkOpenResponse struct {
	Key       string           `json:"key"`
	Status    GUITabMarkStatus `json:"status"`
	Activated bool             `json:"activated"`
}

func (h *GUITabMarksHandler) authorize(c echo.Context) (bot bots.Bot, sessionID string, err error) {
	userID, err := RequireChannelIdentityID(c)
	if err != nil {
		return bots.Bot{}, "", err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	sessionID = strings.TrimSpace(c.Param("session_id"))
	if botID == "" || sessionID == "" {
		return bots.Bot{}, "", echo.NewHTTPError(http.StatusBadRequest, "bot id and session id are required")
	}
	if h.marks == nil || h.sessions == nil {
		return bots.Bot{}, "", echo.NewHTTPError(http.StatusNotFound, "tab marks are not configured")
	}
	ctx := c.Request().Context()
	bot, err = AuthorizeBotAccessWithPermission(ctx, h.botService, h.accountService, userID, botID, bots.PermissionChat)
	if err != nil {
		return bots.Bot{}, "", err
	}
	thread, err := h.sessions.Get(ctx, sessionID)
	if err != nil || thread.BotID != bot.ID {
		return bots.Bot{}, "", echo.NewHTTPError(http.StatusNotFound, "session not found")
	}
	return bot, sessionID, nil
}

// browserTargets lists the page targets of one workspace browser; a nil
// slice with ok=false means the browser could not be asked.
func (h *GUITabMarksHandler) browserTargets(ctx context.Context, botID, browserID string) (map[string]map[string]any, bool) {
	port, ok := cdpPortFromBrowserID(browserID)
	if !ok || h.manager == nil {
		return nil, false
	}
	client, err := h.manager.NativeMCPClient(ctx, botID)
	if err != nil {
		return nil, false
	}
	body, err := cdpProxyGet(ctx, client, port, "/json/list")
	if err != nil {
		return nil, false
	}
	var targets []map[string]any
	if err := json.Unmarshal(body, &targets); err != nil {
		return nil, false
	}
	out := make(map[string]map[string]any, len(targets))
	for _, target := range targets {
		if id, _ := target["id"].(string); id != "" {
			out[id] = target
		}
	}
	return out, true
}

// cdpPortFromBrowserID parses the chrome-<port> browser ids the GUI tools use.
func cdpPortFromBrowserID(id string) (int, bool) {
	id = strings.TrimSpace(id)
	if !strings.HasPrefix(id, "chrome-") {
		return 0, false
	}
	port, err := strconv.Atoi(strings.TrimPrefix(id, "chrome-"))
	if err != nil || port <= 0 || port > 65535 {
		return 0, false
	}
	return port, true
}

// List godoc
// @Summary List the tab marks of a session
// @Description Deliverable / handoff marks the agent placed on workspace browser tabs, with whether each tab still exists.
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Success 200 {object} GUITabMarksResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /bots/{bot_id}/sessions/{session_id}/gui-marks [get].
func (h *GUITabMarksHandler) List(c echo.Context) error {
	bot, sessionID, err := h.authorize(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	marks, err := h.marks.List(ctx, sessionID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "load tab marks failed")
	}
	targetsByBrowser := map[string]map[string]map[string]any{}
	reachable := map[string]bool{}
	out := make([]GUITabMark, 0, len(marks))
	for _, mark := range marks {
		item := GUITabMark{
			Key: mark.Key, Kind: string(mark.Kind), BrowserID: mark.BrowserID, TabID: mark.TabID,
			URL: mark.URL, Title: mark.Title, SessionName: mark.SessionName, Note: mark.Note,
			MarkedAt: mark.MarkedAt, UpdatedAt: mark.UpdatedAt, ClosedAt: mark.ClosedAt, Status: GUITabMarkActive,
		}
		switch {
		case mark.ClosedAt != nil:
			item.Status = GUITabMarkClosed
		default:
			if _, asked := reachable[mark.BrowserID]; !asked {
				targets, ok := h.browserTargets(ctx, bot.ID, mark.BrowserID)
				targetsByBrowser[mark.BrowserID] = targets
				reachable[mark.BrowserID] = ok
			}
			if !reachable[mark.BrowserID] {
				item.Status = GUITabMarkUnknown
				break
			}
			target, present := targetsByBrowser[mark.BrowserID][mark.TabID]
			if !present {
				item.Status = GUITabMarkClosed
				if closed, changed, err := h.marks.MarkClosed(ctx, sessionID, mark.Key); err == nil && changed {
					item.ClosedAt = closed.ClosedAt
					item.UpdatedAt = closed.UpdatedAt
				}
				break
			}
			if u, _ := target["url"].(string); u != "" {
				item.URL = u
			}
			if t, _ := target["title"].(string); t != "" {
				item.Title = t
			}
		}
		out = append(out, item)
	}
	return c.JSON(http.StatusOK, GUITabMarksResponse{Marks: out})
}

// Open godoc
// @Summary Bring a marked tab to the front
// @Description Activates the marked tab in the workspace browser so it is visible in the display; the client then shows the display.
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Param payload body GUITabMarkKeyRequest true "Mark key"
// @Success 200 {object} GUITabMarkOpenResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 502 {object} ErrorResponse
// @Router /bots/{bot_id}/sessions/{session_id}/gui-marks/open [post].
func (h *GUITabMarksHandler) Open(c echo.Context) error {
	bot, sessionID, err := h.authorize(c)
	if err != nil {
		return err
	}
	var req GUITabMarkKeyRequest
	if err := c.Bind(&req); err != nil || strings.TrimSpace(req.Key) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "mark key is required")
	}
	ctx := c.Request().Context()
	mark, err := h.find(ctx, sessionID, req.Key)
	if err != nil {
		return err
	}
	if mark.ClosedAt != nil {
		return echo.NewHTTPError(http.StatusConflict, "the marked tab is closed")
	}
	port, ok := cdpPortFromBrowserID(mark.BrowserID)
	if !ok || h.manager == nil {
		return echo.NewHTTPError(http.StatusBadGateway, "the browser of this mark is not addressable")
	}
	client, err := h.manager.NativeMCPClient(ctx, bot.ID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, "workspace is not reachable")
	}
	targets, reachable := h.browserTargets(ctx, bot.ID, mark.BrowserID)
	if !reachable {
		return echo.NewHTTPError(http.StatusBadGateway, "the workspace browser is not reachable")
	}
	if _, present := targets[mark.TabID]; !present {
		_, _, _ = h.marks.MarkClosed(ctx, sessionID, mark.Key)
		return echo.NewHTTPError(http.StatusConflict, "the marked tab no longer exists")
	}
	if _, err := cdpProxyGet(ctx, client, port, "/json/activate/"+url.PathEscape(mark.TabID)); err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, "activate tab failed: "+err.Error())
	}
	return c.JSON(http.StatusOK, GUITabMarkOpenResponse{Key: mark.Key, Status: GUITabMarkActive, Activated: true})
}

// Dismiss godoc
// @Summary Remove a tab mark
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Param payload body GUITabMarkKeyRequest true "Mark key"
// @Success 204
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /bots/{bot_id}/sessions/{session_id}/gui-marks/dismiss [post].
func (h *GUITabMarksHandler) Dismiss(c echo.Context) error {
	_, sessionID, err := h.authorize(c)
	if err != nil {
		return err
	}
	var req GUITabMarkKeyRequest
	if err := c.Bind(&req); err != nil || strings.TrimSpace(req.Key) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "mark key is required")
	}
	removed, err := h.marks.Delete(c.Request().Context(), sessionID, strings.TrimSpace(req.Key))
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "remove tab mark failed")
	}
	if !removed {
		return echo.NewHTTPError(http.StatusNotFound, "mark not found")
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *GUITabMarksHandler) find(ctx context.Context, sessionID, key string) (tabmark.Mark, error) {
	marks, err := h.marks.List(ctx, sessionID)
	if err != nil {
		return tabmark.Mark{}, echo.NewHTTPError(http.StatusInternalServerError, "load tab marks failed")
	}
	key = strings.TrimSpace(key)
	for _, mark := range marks {
		if mark.Key == key {
			return mark, nil
		}
	}
	return tabmark.Mark{}, echo.NewHTTPError(http.StatusNotFound, "mark not found")
}
