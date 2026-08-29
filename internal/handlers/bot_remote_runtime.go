package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	acpagent "github.com/felinics/memoh/internal/agent/runtime/acp"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/settings"
	"github.com/felinics/memoh/internal/userruntime"
	"github.com/felinics/memoh/internal/workspace"
)

type botRemoteRuntimeService interface {
	Mount(ctx context.Context, botID, runtimeID string) (workspace.WorkspaceTarget, error)
	GetMount(ctx context.Context, botID, targetID string) (workspace.WorkspaceTarget, error)
	SetPrimary(ctx context.Context, botID, targetID string) error
	UpdateToolApprovalConfig(ctx context.Context, botID, targetID string, config settings.ToolApprovalConfig) error
	DeleteMount(ctx context.Context, botID, targetID string) error
}

type workspaceTargetManager interface {
	ListWorkspaceTargets(ctx context.Context, botID string) ([]workspace.WorkspaceTarget, error)
}

type workspaceTargetSettings interface {
	GetBot(ctx context.Context, botID string) (settings.Settings, error)
	UpsertBot(ctx context.Context, botID string, req settings.UpsertRequest) (settings.Settings, error)
}

type workspaceTargetRuntimeCloser interface {
	CloseBotWorkspaceTargetRuntimes(botID, targetID string) error
}

type BotRemoteRuntimeHandler struct {
	log        *slog.Logger
	service    botRemoteRuntimeService
	workspaces workspaceTargetManager
	settings   workspaceTargetSettings
	runtimes   workspaceTargetRuntimeCloser
	bots       *bots.Service
	accounts   *accounts.Service
}

func NewBotRemoteRuntimeHandler(
	log *slog.Logger,
	service *workspace.RemoteWorkspaceService,
	manager *workspace.Manager,
	settingsService *settings.Service,
	botService *bots.Service,
	accountService *accounts.Service,
	acpPool *acpagent.SessionPool,
) *BotRemoteRuntimeHandler {
	if log == nil {
		log = slog.Default()
	}
	return &BotRemoteRuntimeHandler{
		log:        log.With(slog.String("handler", "workspace_targets")),
		service:    service,
		workspaces: manager,
		settings:   settingsService,
		runtimes:   acpPool,
		bots:       botService,
		accounts:   accountService,
	}
}

func (h *BotRemoteRuntimeHandler) Register(e *echo.Echo) {
	g := e.Group("/bots/:bot_id/workspace-targets")
	g.GET("", h.List)
	g.PUT("/remotes/:runtime_id", h.Mount)
	g.DELETE("/:target_id", h.Delete)
	g.PUT("/primary", h.SetPrimary)
	g.PUT("/:target_id/tool-approval", h.UpdateToolApproval)
}

// List godoc
// @Summary List a Bot's workspace targets
// @Tags workspace-targets
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} workspace.WorkspaceTargetsResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/workspace-targets [get].
func (h *BotRemoteRuntimeHandler) List(c echo.Context) error {
	botID, err := h.requirePermission(c, bots.PermissionWorkspaceRead)
	if err != nil {
		return err
	}
	targets, err := h.workspaces.ListWorkspaceTargets(c.Request().Context(), botID)
	if err != nil {
		return workspaceTargetHTTPError(h.log, err)
	}
	return c.JSON(http.StatusOK, workspace.WorkspaceTargetsResponse{Targets: targets})
}

// Mount godoc
// @Summary Add or update a Remote Runtime workspace target
// @Tags workspace-targets
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param runtime_id path string true "Runtime ID"
// @Success 200 {object} workspace.WorkspaceTarget
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /bots/{bot_id}/workspace-targets/remotes/{runtime_id} [put].
func (h *BotRemoteRuntimeHandler) Mount(c echo.Context) error {
	botID, err := h.requirePermission(c, bots.PermissionManage)
	if err != nil {
		return err
	}
	target, err := h.service.Mount(c.Request().Context(), botID, c.Param("runtime_id"))
	if err != nil {
		return workspaceTargetHTTPError(h.log, err)
	}
	return c.JSON(http.StatusOK, target)
}

// Delete godoc
// @Summary Delete a Remote Runtime workspace target
// @Description Remote files and the Native workspace are not deleted.
// @Tags workspace-targets
// @Param bot_id path string true "Bot ID"
// @Param target_id path string true "Workspace target ID"
// @Success 204 "No Content"
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} apperror.Problem
// @Router /bots/{bot_id}/workspace-targets/{target_id} [delete].
func (h *BotRemoteRuntimeHandler) Delete(c echo.Context) error {
	botID, err := h.requirePermission(c, bots.PermissionManage)
	if err != nil {
		return err
	}
	targetID := strings.TrimSpace(c.Param("target_id"))
	if targetID == workspace.WorkspaceTargetNative {
		return echo.NewHTTPError(http.StatusBadRequest, "native workspace target cannot be deleted")
	}
	if err := h.service.DeleteMount(c.Request().Context(), botID, targetID); err != nil {
		return workspaceTargetHTTPError(h.log, err)
	}
	h.closeDeletedTargetRuntimes(botID, targetID)
	return c.NoContent(http.StatusNoContent)
}

func (h *BotRemoteRuntimeHandler) closeDeletedTargetRuntimes(botID, targetID string) {
	if h == nil || h.runtimes == nil {
		return
	}
	if err := h.runtimes.CloseBotWorkspaceTargetRuntimes(botID, targetID); err != nil {
		h.log.Warn("failed to close ACP runtimes for deleted workspace target",
			slog.Any("error", err),
			slog.String("bot_id", botID),
			slog.String("workspace_target_id", targetID))
	}
}

// SetPrimary godoc
// @Summary Set a Bot's Primary workspace target
// @Tags workspace-targets
// @Accept json
// @Param bot_id path string true "Bot ID"
// @Param request body workspace.SetPrimaryWorkspaceTargetRequest true "Primary target"
// @Success 204 "No Content"
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /bots/{bot_id}/workspace-targets/primary [put].
func (h *BotRemoteRuntimeHandler) SetPrimary(c echo.Context) error {
	botID, err := h.requirePermission(c, bots.PermissionManage)
	if err != nil {
		return err
	}
	var req workspace.SetPrimaryWorkspaceTargetRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if strings.TrimSpace(req.TargetID) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "target_id is required")
	}
	if err := h.service.SetPrimary(c.Request().Context(), botID, req.TargetID); err != nil {
		return workspaceTargetHTTPError(h.log, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// UpdateToolApproval godoc
// @Summary Update tool approval for one workspace target
// @Description The read/write/exec fields are mode shortcuts. tool_approval_config preserves and updates advanced bypass/force rules.
// @Tags workspace-targets
// @Accept json
// @Param bot_id path string true "Bot ID"
// @Param target_id path string true "Workspace target ID"
// @Param request body workspace.UpdateWorkspaceTargetToolApprovalRequest true "Target tool approval"
// @Success 204 "No Content"
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /bots/{bot_id}/workspace-targets/{target_id}/tool-approval [put].
func (h *BotRemoteRuntimeHandler) UpdateToolApproval(c echo.Context) error {
	botID, err := h.requirePermission(c, bots.PermissionManage)
	if err != nil {
		return err
	}
	var req workspace.UpdateWorkspaceTargetToolApprovalRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	targetID := strings.TrimSpace(c.Param("target_id"))
	config, err := h.resolveToolApprovalUpdate(c.Request().Context(), botID, targetID, req)
	if err != nil {
		return workspaceTargetHTTPError(h.log, err)
	}
	if targetID == workspace.WorkspaceTargetNative {
		if h.settings == nil {
			return workspaceTargetHTTPError(h.log, errors.New("settings service not configured"))
		}
		_, err = h.settings.UpsertBot(c.Request().Context(), botID, settings.UpsertRequest{ToolApprovalConfig: &config})
	} else {
		err = h.service.UpdateToolApprovalConfig(c.Request().Context(), botID, targetID, config)
	}
	if err != nil {
		return workspaceTargetHTTPError(h.log, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *BotRemoteRuntimeHandler) resolveToolApprovalUpdate(
	ctx context.Context,
	botID, targetID string,
	req workspace.UpdateWorkspaceTargetToolApprovalRequest,
) (settings.ToolApprovalConfig, error) {
	var config settings.ToolApprovalConfig
	if targetID == workspace.WorkspaceTargetNative {
		if h.settings == nil {
			return config, errors.New("settings service not configured")
		}
		current, err := h.settings.GetBot(ctx, botID)
		if err != nil {
			return config, err
		}
		config = current.ToolApprovalConfig
	} else {
		target, err := h.service.GetMount(ctx, botID, targetID)
		if err != nil {
			return config, err
		}
		config = target.ToolApprovalConfig
	}
	if req.ToolApprovalConfig != nil {
		config = settings.NormalizeToolApprovalConfig(*req.ToolApprovalConfig)
	}
	if req.Enabled != nil {
		config.Enabled = *req.Enabled
	}
	hasModes := req.Read != "" || req.Write != "" || req.Exec != ""
	if !hasModes {
		if req.ToolApprovalConfig == nil && req.Enabled == nil {
			return config, workspace.ErrInvalidWorkspaceToolApprovalMode
		}
		return config, nil
	}
	return workspace.ApplyWorkspaceToolApprovalModes(config, workspace.WorkspaceTargetToolApproval{
		Read: req.Read, Write: req.Write, Exec: req.Exec,
	})
}

func (h *BotRemoteRuntimeHandler) requirePermission(c echo.Context, permission string) (string, error) {
	identityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return "", err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return "", echo.NewHTTPError(http.StatusBadRequest, "bot_id is required")
	}
	if _, err := AuthorizeBotAccessWithPermission(c.Request().Context(), h.bots, h.accounts, identityID, botID, permission); err != nil {
		return "", err
	}
	return botID, nil
}

func workspaceTargetHTTPError(log *slog.Logger, err error) error {
	switch {
	case errors.Is(err, workspace.ErrInvalidWorkspaceToolApprovalMode),
		errors.Is(err, userruntime.ErrInvalidInput):
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	case errors.Is(err, workspace.ErrRemoteRuntimeNotUsable),
		errors.Is(err, workspace.ErrWorkspaceTargetNotFound),
		errors.Is(err, db.ErrNotFound):
		return echo.NewHTTPError(http.StatusNotFound, "workspace target not found")
	case errors.Is(err, workspace.ErrWorkspaceTargetInUse):
		return apperror.New(apperror.CodeWorkspaceTargetInUse, nil)
	case errors.Is(err, workspace.ErrRemoteRuntimeRevoked),
		errors.Is(err, workspace.ErrRemoteRuntimeOwnerMismatch),
		errors.Is(err, workspace.ErrRemoteRuntimeClientUpdateNeeded):
		return echo.NewHTTPError(http.StatusConflict, err.Error())
	default:
		if log != nil {
			log.Error("workspace target request failed", slog.Any("error", err))
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "internal server error")
	}
}
