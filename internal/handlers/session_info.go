package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/bots"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/settings"
)

type SessionInfoHandler struct {
	queries         dbstore.Queries
	botService      *bots.Service
	accountService  *accounts.Service
	modelsService   *models.Service
	settingsService *settings.Service
	logger          *slog.Logger
}

func NewSessionInfoHandler(log *slog.Logger, queries dbstore.Queries, botService *bots.Service, accountService *accounts.Service, modelsService *models.Service, settingsService *settings.Service) *SessionInfoHandler {
	return &SessionInfoHandler{
		queries:         queries,
		botService:      botService,
		accountService:  accountService,
		modelsService:   modelsService,
		settingsService: settingsService,
		logger:          log.With(slog.String("handler", "session_info")),
	}
}

func (h *SessionInfoHandler) Register(e *echo.Echo) {
	e.GET("/bots/:bot_id/sessions/:session_id/status", h.GetSessionInfo)
	e.GET("/bots/:bot_id/sessions/:session_id/context-lifecycle", h.GetSessionContextLifecycle)
}

type SessionInfoResponse struct {
	MessageCount int64        `json:"message_count"`
	ContextUsage ContextUsage `json:"context_usage"`
	CacheStats   CacheStats   `json:"cache_stats"`
	Skills       []string     `json:"skills"`
}

type ContextUsage struct {
	UsedTokens    int64  `json:"used_tokens"`
	ContextWindow *int64 `json:"context_window,omitempty"`
}

type CacheStats struct {
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	TotalInputTokens int64   `json:"total_input_tokens"`
	CacheHitRate     float64 `json:"cache_hit_rate"`
}

// GetSessionInfo godoc
// @Summary Get session info
// @Description Get aggregated info for a chat session including message count, context usage, cache stats, and used skills
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Param model_id query string false "Optional model UUID override for context window"
// @Success 200 {object} SessionInfoResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/sessions/{session_id}/status [get].
func (h *SessionInfoHandler) GetSessionInfo(c echo.Context) error {
	userID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	sessionID := strings.TrimSpace(c.Param("session_id"))
	if sessionID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "session id is required")
	}

	pgSessionID, err := db.ParseUUID(sessionID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid session id")
	}

	ctx := c.Request().Context()
	sessionRow, err := h.queries.GetSessionByID(ctx, pgSessionID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "session not found")
	}
	sessionMode, runtimeType := normalizedSessionDescriptor(session.Thread{
		Type:        sessionRow.Type,
		SessionMode: sessionRow.SessionMode,
		RuntimeType: sessionRow.RuntimeType,
	})
	bot, err := AuthorizeBotAccessWithPermission(ctx, h.botService, h.accountService, userID, botID, requiredReadPermissionForSessionRuntime(sessionMode, runtimeType))
	if err != nil {
		return err
	}
	if sessionRow.BotID.String() != bot.ID {
		return echo.NewHTTPError(http.StatusNotFound, "session not found")
	}
	perms, err := h.resolveCurrentUserPermissions(c, userID, bot.ID)
	if err != nil {
		return err
	}
	sess := session.Thread{
		ID:          sessionRow.ID.String(),
		BotID:       sessionRow.BotID.String(),
		Type:        sessionRow.Type,
		SessionMode: sessionMode,
		RuntimeType: runtimeType,
	}
	if sessionRow.CreatedByUserID.Valid {
		sess.CreatedByUserID = sessionRow.CreatedByUserID.String()
	}
	if !canAccessSession(sess, userID, perms) {
		return echo.NewHTTPError(http.StatusNotFound, "session not found")
	}

	messageCount, err := h.queries.CountMessagesBySession(ctx, pgSessionID)
	if err != nil {
		h.logger.Error("count messages failed", slog.Any("error", err))
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count messages")
	}

	var usedTokens int64
	latestUsage, err := h.queries.GetLatestAssistantUsage(ctx, pgSessionID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		h.logger.Error("get latest usage failed", slog.Any("error", err))
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get latest usage")
	}
	if err == nil {
		usedTokens = latestUsage
	}

	contextWindow := h.resolveContextWindow(c, bot.ID)

	cacheRow, err := h.queries.GetSessionCacheStats(ctx, pgSessionID)
	if err != nil {
		h.logger.Error("get cache stats failed", slog.Any("error", err))
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get cache stats")
	}

	var cacheHitRate float64
	if cacheRow.TotalInputTokens > 0 {
		cacheHitRate = float64(cacheRow.CacheReadTokens) / float64(cacheRow.TotalInputTokens) * 100
	}

	skills, err := h.queries.GetSessionUsedSkills(ctx, pgSessionID)
	if err != nil {
		h.logger.Error("get used skills failed", slog.Any("error", err))
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get used skills")
	}
	if skills == nil {
		skills = []string{}
	}

	resp := SessionInfoResponse{
		MessageCount: messageCount,
		ContextUsage: ContextUsage{
			UsedTokens:    usedTokens,
			ContextWindow: contextWindow,
		},
		CacheStats: CacheStats{
			CacheReadTokens:  cacheRow.CacheReadTokens,
			TotalInputTokens: cacheRow.TotalInputTokens,
			CacheHitRate:     cacheHitRate,
		},
		Skills: skills,
	}
	return c.JSON(http.StatusOK, resp)
}

func (h *SessionInfoHandler) resolveCurrentUserPermissions(c echo.Context, channelIdentityID, botID string) ([]string, error) {
	if h.botService == nil || h.accountService == nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, "bot services not configured")
	}
	isAdmin, err := h.accountService.IsAdmin(c.Request().Context(), channelIdentityID)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	perms, err := h.botService.ResolveUserPermissions(c.Request().Context(), botID, channelIdentityID, isAdmin)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return perms, nil
}

func (h *SessionInfoHandler) resolveContextWindow(c echo.Context, botID string) *int64 {
	modelIDStr := strings.TrimSpace(c.QueryParam("model_id"))

	if modelIDStr == "" && h.settingsService != nil {
		s, err := h.settingsService.GetBot(c.Request().Context(), botID)
		if err == nil && s.ChatModelID != "" {
			modelIDStr = s.ChatModelID
		}
	}

	if modelIDStr == "" || h.modelsService == nil {
		return nil
	}

	m, err := h.modelsService.GetByID(c.Request().Context(), modelIDStr)
	if err != nil {
		return nil
	}
	if m.Config.ContextWindow == nil {
		return nil
	}
	cw := int64(*m.Config.ContextWindow)
	return &cw
}
