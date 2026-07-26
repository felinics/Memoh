package chat

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	session "github.com/memohai/memoh/domains/agent/chat/thread"
	usagepersistence "github.com/memohai/memoh/domains/agent/chat/usage/persistence"
	"github.com/memohai/memoh/domains/api/bot"
	"github.com/memohai/memoh/domains/api/bot/setting"
	httpx "github.com/memohai/memoh/domains/api/http"
	"github.com/memohai/memoh/domains/iam/account"
	modelcatalog "github.com/memohai/memoh/domains/model/catalog"
	"github.com/memohai/memoh/internal/apperror"
)

type SessionInfoHandler struct {
	reader          usagepersistence.Reader
	botService      *bot.Service
	accountService  *account.Service
	modelsService   *modelcatalog.Service
	settingsService *setting.Service
	logger          *slog.Logger
}

func NewSessionInfoHandler(log *slog.Logger, reader usagepersistence.Reader, botService *bot.Service, accountService *account.Service, modelsService *modelcatalog.Service, settingsService *setting.Service) *SessionInfoHandler {
	return &SessionInfoHandler{
		reader:          reader,
		botService:      botService,
		accountService:  accountService,
		modelsService:   modelsService,
		settingsService: settingsService,
		logger:          log.With(slog.String("handler", "session_info")),
	}
}

func (h *SessionInfoHandler) Register(e *echo.Echo) {
	e.GET("/bots/:bot_id/sessions/:session_id/status", h.GetSessionInfo)
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
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/status [get].
func (h *SessionInfoHandler) GetSessionInfo(c echo.Context) error {
	userID, err := httpx.RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return apperror.Required("bot_id")
	}
	sessionID := strings.TrimSpace(c.Param("session_id"))
	if sessionID == "" {
		return apperror.Required("session_id")
	}

	if _, err := uuid.Parse(sessionID); err != nil {
		return apperror.Field("session_id", apperror.FieldInvalid)
	}

	ctx := c.Request().Context()
	sessionRow, err := h.reader.GetSession(ctx, sessionID)
	if err != nil {
		return apperror.NotFound("get session info", nil)
	}
	sessionMode, runtimeType := normalizedSessionDescriptor(session.Thread{
		Type:        sessionRow.Type,
		SessionMode: sessionRow.SessionMode,
		RuntimeType: sessionRow.RuntimeType,
	})
	bot, err := httpx.AuthorizeBotAccessWithPermission(ctx, h.botService, h.accountService, userID, botID, requiredReadPermissionForSessionRuntime(sessionMode, runtimeType))
	if err != nil {
		return err
	}
	if sessionRow.BotID != bot.ID {
		return apperror.NotFound("get session info", nil)
	}
	perms, err := h.resolveCurrentUserPermissions(c, userID, bot.ID)
	if err != nil {
		return err
	}
	sess := session.Thread{
		ID:              sessionRow.ID,
		BotID:           sessionRow.BotID,
		Type:            sessionRow.Type,
		SessionMode:     sessionMode,
		RuntimeType:     runtimeType,
		CreatedByUserID: sessionRow.CreatedByUserID,
	}
	if !canAccessSession(sess, userID, perms) {
		return apperror.NotFound("get session info", nil)
	}

	messageCount, err := h.reader.CountMessagesBySession(ctx, sessionID)
	if err != nil {
		h.logger.Error("count messages failed", slog.Any("error", err))
		return apperror.Internal("count session messages", err)
	}

	var usedTokens int64
	latestUsage, err := h.reader.GetLatestAssistantUsage(ctx, sessionID)
	if err != nil && !errors.Is(err, usagepersistence.ErrNotFound) {
		h.logger.Error("get latest usage failed", slog.Any("error", err))
		return apperror.Internal("get latest usage", err)
	}
	if err == nil {
		usedTokens = latestUsage
	}

	contextWindow := h.resolveContextWindow(c, bot.ID)

	cacheRow, err := h.reader.GetSessionCacheStats(ctx, sessionID)
	if err != nil {
		h.logger.Error("get cache stats failed", slog.Any("error", err))
		return apperror.Internal("get cache stats", err)
	}

	var cacheHitRate float64
	if cacheRow.TotalInputTokens > 0 {
		cacheHitRate = float64(cacheRow.CacheReadTokens) / float64(cacheRow.TotalInputTokens) * 100
	}

	skills, err := h.reader.GetSessionUsedSkills(ctx, sessionID)
	if err != nil {
		h.logger.Error("get used skills failed", slog.Any("error", err))
		return apperror.Internal("get used skills", err)
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
		return nil, apperror.Internal("resolve user permissions", nil)
	}
	isAdmin, err := h.accountService.IsAdmin(c.Request().Context(), channelIdentityID)
	if err != nil {
		return nil, apperror.Internal("resolve user permissions", err)
	}
	perms, err := h.botService.ResolveUserPermissions(c.Request().Context(), botID, channelIdentityID, isAdmin)
	if err != nil {
		return nil, apperror.Internal("resolve user permissions", err)
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
