package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/hooks"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

type HooksHandler struct {
	logger         *slog.Logger
	botService     *bots.Service
	accountService *accounts.Service
	service        *hooks.Service
	agent          *native.Agent
	bridgeProvider bridge.Provider
}

type HookEventInfo struct {
	Name             string `json:"name"`
	RuntimeSupported bool   `json:"runtime_supported"`
}

type HooksEventsResponse struct {
	ConfigPath string          `json:"config_path"`
	Events     []HookEventInfo `json:"events"`
	Actions    []string        `json:"actions"`
	Decisions  []string        `json:"decisions"`
}

type HookTestRequest struct {
	Event     string             `json:"event"`
	SessionID string             `json:"session_id,omitempty"`
	ChatID    string             `json:"chat_id,omitempty"`
	Tool      *hooks.ToolPayload `json:"tool,omitempty"`
	Approval  map[string]any     `json:"approval,omitempty"`
	Turn      map[string]any     `json:"turn,omitempty"`
	Memory    map[string]any     `json:"memory,omitempty"`
	Channel   map[string]any     `json:"channel,omitempty"`
	Extra     map[string]any     `json:"extra,omitempty"`
	Error     string             `json:"error,omitempty"`
}

type HookTestResponse struct {
	ConfigExists bool         `json:"config_exists"`
	Result       hooks.Result `json:"result"`
}

func NewHooksHandler(log *slog.Logger, botService *bots.Service, accountService *accounts.Service, service *hooks.Service, agent *native.Agent, provider bridge.Provider) *HooksHandler {
	if log == nil {
		log = slog.Default()
	}
	return &HooksHandler{
		logger:         log.With(slog.String("handler", "hooks")),
		botService:     botService,
		accountService: accountService,
		service:        service,
		agent:          agent,
		bridgeProvider: provider,
	}
}

func (h *HooksHandler) Register(e *echo.Echo) {
	group := e.Group("/bots/:bot_id/hooks")
	group.GET("/events", h.Events)
	group.POST("/test", h.Test)
}

// Events godoc
// @Summary List supported bot hook events
// @Tags hooks
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} HooksEventsResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/hooks/events [get].
func (h *HooksHandler) Events(c echo.Context) error {
	userID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot_id is required")
	}
	if _, err := AuthorizeBotAccessWithPermission(c.Request().Context(), h.botService, h.accountService, userID, botID, bots.PermissionChat); err != nil {
		return err
	}
	events := hooks.EventCatalog()
	items := make([]HookEventInfo, 0, len(events))
	for _, event := range events {
		items = append(items, HookEventInfo{Name: event, RuntimeSupported: hooks.RuntimeSupported(event)})
	}
	return c.JSON(http.StatusOK, HooksEventsResponse{
		ConfigPath: hooks.DefaultConfigPath,
		Events:     items,
		Actions:    []string{hooks.ActionCommand, hooks.ActionTool},
		Decisions:  []string{hooks.DecisionAllow, hooks.DecisionDeny, hooks.DecisionAskApproval, hooks.DecisionAppendContext},
	})
}

// Test godoc
// @Summary Run bot hooks for a synthetic event
// @Tags hooks
// @Param bot_id path string true "Bot ID"
// @Param payload body HookTestRequest true "Hook test payload"
// @Success 200 {object} HookTestResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/hooks/test [post].
func (h *HooksHandler) Test(c echo.Context) error {
	userID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot_id is required")
	}
	if _, err := AuthorizeBotAccessWithPermission(c.Request().Context(), h.botService, h.accountService, userID, botID, bots.PermissionWorkspaceExec); err != nil {
		return err
	}
	var input HookTestRequest
	if err := c.Bind(&input); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	eventName := strings.TrimSpace(input.Event)
	if eventName == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "event is required")
	}
	if h.service == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "hooks service is not configured")
	}
	ctx := c.Request().Context()
	cfg, exists, err := h.service.LoadEffective(ctx, botID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	req := hooks.Request{
		Version:   1,
		Event:     eventName,
		BotID:     botID,
		SessionID: strings.TrimSpace(input.SessionID),
		ChatID:    strings.TrimSpace(input.ChatID),
		Workspace: h.workspaceInfo(ctx, botID),
		Tool:      input.Tool,
		Approval:  input.Approval,
		Turn:      input.Turn,
		Memory:    input.Memory,
		Channel:   input.Channel,
		Extra:     input.Extra,
		Error:     strings.TrimSpace(input.Error),
	}
	result, err := h.service.RunConfig(ctx, cfg, req, hookTestToolRunner{agent: h.agent, cfg: native.RunConfig{
		SupportsImageInput: true,
		SupportsToolCall:   true,
		Identity: native.SessionContext{
			BotID:     botID,
			ChatID:    req.ChatID,
			SessionID: req.SessionID,
		},
	}})
	if err != nil {
		if errors.Is(err, hooks.ErrDenied) {
			return c.JSON(http.StatusOK, HookTestResponse{ConfigExists: exists, Result: result})
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, HookTestResponse{ConfigExists: exists, Result: result})
}

func (h *HooksHandler) workspaceInfo(ctx context.Context, botID string) hooks.WorkspaceInfo {
	info := hooks.WorkspaceInfo{
		CWD:     hooks.DefaultWorkDir,
		Runtime: bridge.WorkspaceBackendContainer,
	}
	provider, ok := h.bridgeProvider.(bridge.WorkspaceInfoProvider)
	if !ok {
		return info
	}
	workspaceInfo, err := provider.WorkspaceInfo(ctx, botID)
	if err != nil {
		return info
	}
	if strings.TrimSpace(workspaceInfo.DefaultWorkDir) != "" {
		info.CWD = workspaceInfo.DefaultWorkDir
	}
	if strings.TrimSpace(workspaceInfo.Backend) != "" {
		info.Runtime = workspaceInfo.Backend
	}
	return info
}

type hookTestToolRunner struct {
	agent *native.Agent
	cfg   native.RunConfig
}

func (r hookTestToolRunner) RunHookTool(ctx context.Context, toolName string, input map[string]any) (any, error) {
	if r.agent == nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, "agent is not configured")
	}
	part, err := r.agent.ExecuteTool(ctx, r.cfg, sdk.ToolCall{
		ToolName:   strings.TrimSpace(toolName),
		ToolCallID: "hook-test:" + strings.TrimSpace(toolName),
		Input:      input,
	})
	if err != nil {
		return nil, err
	}
	return part.Result, nil
}
