package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/agent/application"
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
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
	e.GET("/bots/:bot_id/sessions/:session_id/context-lifecycle/:run_id", h.GetSessionContextLifecycleTurn)
}

type SessionInfoResponse struct {
	MessageCount int64        `json:"message_count"`
	ContextUsage ContextUsage `json:"context_usage"`
	CacheStats   CacheStats   `json:"cache_stats"`
	Skills       []string     `json:"skills"`
}

type ContextUsage struct {
	UsedTokens    int64                          `json:"used_tokens"`
	ContextWindow *int64                         `json:"context_window,omitempty"`
	Breakdown     []contextfrag.KindBreakdown    `json:"breakdown,omitempty"`
	ToolDefs      []ToolDefBucket                `json:"tool_defs,omitempty"`
	BudgetPlan    *contextfrag.ContextBudgetPlan `json:"budget_plan,omitempty"`
	Compaction    *CompactionInfo                `json:"compaction,omitempty"`
}

// CompactionInfo reports where compaction fires for this session. It is
// omitted for runtimes Memoh never compacts, and its marks are omitted until a
// turn has persisted a budget plan, so the UI never draws a guessed level.
type CompactionInfo struct {
	Enabled    bool  `json:"enabled"`
	AutoTokens int64 `json:"auto_tokens,omitempty"`
	HardTokens int64 `json:"hard_tokens,omitempty"`
}

type ToolDefBucket struct {
	Provider      string `json:"provider"`
	Tools         int    `json:"tools"`
	TokenEstimate int    `json:"token_estimate"`
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

	botSettings, hasSettings := h.loadBotSettings(ctx, bot.ID)
	contextWindow := h.resolveContextWindow(c, botSettings)

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

	var breakdown []contextfrag.KindBreakdown
	var toolDefs []ToolDefBucket
	var budgetPlan *contextfrag.ContextBudgetPlan
	if load, err := loadContextLifecycleTurns(ctx, h.queries, pgSessionID, 1); err != nil {
		h.logger.Warn("load latest context snapshot failed", slog.Any("error", err))
	} else {
		breakdown, toolDefs, budgetPlan = latestContextComposition(load.Turns)
	}

	var compactionInfo *CompactionInfo
	if hasSettings && runtimeType == session.RuntimeModel {
		info := contextCompactionInfo(botSettings.CompactionEnabled, botSettings.CompactionThreshold, budgetPlan)
		compactionInfo = &info
	}

	resp := SessionInfoResponse{
		MessageCount: messageCount,
		ContextUsage: ContextUsage{
			UsedTokens:    usedTokens,
			ContextWindow: contextWindow,
			Breakdown:     breakdown,
			ToolDefs:      toolDefs,
			BudgetPlan:    budgetPlan,
			Compaction:    compactionInfo,
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

// loadBotSettings resolves bot settings once per request: both the context
// window fallback and the compaction marks read from the same snapshot.
func (h *SessionInfoHandler) loadBotSettings(ctx context.Context, botID string) (settings.Settings, bool) {
	if h.settingsService == nil {
		return settings.Settings{}, false
	}
	botSettings, err := h.settingsService.GetBot(ctx, botID)
	if err != nil {
		h.logger.Warn("load bot settings failed", slog.Any("error", err))
		return settings.Settings{}, false
	}
	return botSettings, true
}

// contextCompactionInfo mirrors the turn-time compaction levels for display.
// Only the persisted plan window is the budget the turn actually ran against;
// the turn path caps the raw model window, so guessing from it would diverge.
func contextCompactionInfo(enabled bool, threshold int, plan *contextfrag.ContextBudgetPlan) CompactionInfo {
	info := CompactionInfo{Enabled: enabled}
	if plan == nil || plan.Window <= 0 {
		return info
	}
	autoTokens, hardTokens := application.CompactionMarks(threshold, plan.Window)
	info.AutoTokens = int64(autoTokens)
	info.HardTokens = int64(hardTokens)
	return info
}

func (h *SessionInfoHandler) resolveContextWindow(c echo.Context, botSettings settings.Settings) *int64 {
	modelIDStr := strings.TrimSpace(c.QueryParam("model_id"))

	if modelIDStr == "" {
		modelIDStr = botSettings.ChatModelID
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
