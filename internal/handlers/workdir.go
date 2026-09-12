package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/workdir"
	"github.com/felinics/memoh/internal/workspace"
)

// botWorkdirService is the slice of *workdir.Service the handler needs.
type botWorkdirService interface {
	GitBranch(ctx context.Context, botID, workdirID string) (workdir.GitBranchResponse, error)
	SwitchGitBranch(ctx context.Context, botID, workdirID, branch string) (workdir.GitBranchResponse, error)
	Create(ctx context.Context, botID, userID string, req workdir.CreateRequest) (workdir.Workdir, error)
	List(ctx context.Context, botID string, includeArchived bool) ([]workdir.Workdir, error)
	Rename(ctx context.Context, botID, workdirID, name string) (workdir.Workdir, error)
	Archive(ctx context.Context, botID, workdirID string) error
}

// WorkdirHandler manages a bot's working directories.
type WorkdirHandler struct {
	log      *slog.Logger
	service  botWorkdirService
	bots     *bots.Service
	accounts *accounts.Service
}

func NewWorkdirHandler(
	log *slog.Logger,
	service *workdir.Service,
	botService *bots.Service,
	accountService *accounts.Service,
) *WorkdirHandler {
	if log == nil {
		log = slog.Default()
	}
	return &WorkdirHandler{
		log:      log.With(slog.String("handler", "workdirs")),
		service:  service,
		bots:     botService,
		accounts: accountService,
	}
}

func (h *WorkdirHandler) Register(e *echo.Echo) {
	g := e.Group("/bots/:bot_id/workdirs")
	g.POST("", h.Create)
	g.GET("", h.List)
	g.GET("/:workdir_id/git-branch", h.GitBranch)
	g.POST("/:workdir_id/git-branch", h.SwitchGitBranch)
	g.PATCH("/:workdir_id", h.Rename)
	g.DELETE("/:workdir_id", h.Archive)
}

// Create godoc
// @Summary Create a Bot workdir
// @Description Registers a named working directory on a workspace target. The directory must already exist on that target.
// @Tags workdirs
// @Accept json
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param request body workdir.CreateRequest true "Workdir"
// @Success 201 {object} workdir.Workdir
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Router /bots/{bot_id}/workdirs [post].
func (h *WorkdirHandler) Create(c echo.Context) error {
	botID, identityID, err := h.requirePermission(c, bots.PermissionManage)
	if err != nil {
		return err
	}
	var req workdir.CreateRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	created, err := h.service.Create(c.Request().Context(), botID, identityID, req)
	if err != nil {
		return workdirHTTPError(h.log, err)
	}
	return c.JSON(http.StatusCreated, created)
}

// List godoc
// @Summary List a Bot's workdirs
// @Tags workdirs
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param include_archived query bool false "Include archived workdirs"
// @Success 200 {object} workdir.WorkdirsResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/workdirs [get].
func (h *WorkdirHandler) List(c echo.Context) error {
	botID, _, err := h.requirePermission(c, bots.PermissionWorkspaceRead)
	if err != nil {
		return err
	}
	includeArchived := strings.EqualFold(c.QueryParam("include_archived"), "true")
	workdirs, err := h.service.List(c.Request().Context(), botID, includeArchived)
	if err != nil {
		return workdirHTTPError(h.log, err)
	}
	return c.JSON(http.StatusOK, workdir.WorkdirsResponse{Workdirs: workdirs})
}

// Rename godoc
// @Summary Rename a Bot workdir
// @Description Only the name can change. The target and path are immutable: they are baked into the working directory of every session bound to this workdir.
// @Tags workdirs
// @Accept json
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param workdir_id path string true "Workdir ID"
// @Param request body workdir.UpdateRequest true "New name"
// @Success 200 {object} workdir.Workdir
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /bots/{bot_id}/workdirs/{workdir_id} [patch].
func (h *WorkdirHandler) Rename(c echo.Context) error {
	botID, _, err := h.requirePermission(c, bots.PermissionManage)
	if err != nil {
		return err
	}
	workdirID := strings.TrimSpace(c.Param("workdir_id"))
	if workdirID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "workdir_id is required")
	}
	var req workdir.UpdateRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	renamed, err := h.service.Rename(c.Request().Context(), botID, workdirID, req.Name)
	if err != nil {
		return workdirHTTPError(h.log, err)
	}
	return c.JSON(http.StatusOK, renamed)
}

// Archive godoc
// @Summary Archive a Bot workdir
// @Description Archiving refuses new session bindings but keeps existing sessions working: their directory never changes underneath them.
// @Tags workdirs
// @Param bot_id path string true "Bot ID"
// @Param workdir_id path string true "Workdir ID"
// @Success 204 "No Content"
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /bots/{bot_id}/workdirs/{workdir_id} [delete].
func (h *WorkdirHandler) Archive(c echo.Context) error {
	botID, _, err := h.requirePermission(c, bots.PermissionManage)
	if err != nil {
		return err
	}
	workdirID := strings.TrimSpace(c.Param("workdir_id"))
	if workdirID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "workdir_id is required")
	}
	if err := h.service.Archive(c.Request().Context(), botID, workdirID); err != nil {
		return workdirHTTPError(h.log, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// requirePermission authorizes the caller on the bot and returns the bot's
// canonical UUID (the path parameter may be a name slug) plus the caller's
// channel identity id.
func (h *WorkdirHandler) requirePermission(c echo.Context, permission string) (string, string, error) {
	identityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return "", "", err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return "", "", echo.NewHTTPError(http.StatusBadRequest, "bot_id is required")
	}
	bot, err := AuthorizeBotAccessWithPermission(c.Request().Context(), h.bots, h.accounts, identityID, botID, permission)
	if err != nil {
		return "", "", err
	}
	return bot.ID, identityID, nil
}

func workdirHTTPError(log *slog.Logger, err error) error {
	switch {
	case errors.Is(err, workdir.ErrNameRequired),
		errors.Is(err, workdir.ErrPathRequired),
		errors.Is(err, workdir.ErrInvalidPath),
		errors.Is(err, workdir.ErrPathNotFound),
		errors.Is(err, workdir.ErrPathNotDirectory):
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	case errors.Is(err, workdir.ErrWorkdirNotFound),
		errors.Is(err, workspace.ErrWorkspaceTargetNotFound),
		errors.Is(err, db.ErrNotFound):
		return echo.NewHTTPError(http.StatusNotFound, "workdir not found")
	case errors.Is(err, workdir.ErrDuplicatePath),
		errors.Is(err, workdir.ErrWorkdirArchived),
		errors.Is(err, workspace.ErrRemoteRuntimeOffline),
		errors.Is(err, workspace.ErrRemoteRuntimeRevoked),
		errors.Is(err, workspace.ErrRemoteRuntimeOwnerMismatch),
		errors.Is(err, workspace.ErrRemoteRuntimeClientUpdateNeeded),
		errors.Is(err, workspace.ErrRemoteRuntimeNotUsable),
		errors.Is(err, workspace.ErrRemoteWorkspaceNotBound):
		return echo.NewHTTPError(http.StatusConflict, err.Error())
	default:
		if log != nil {
			log.Error("workdir request failed", slog.Any("error", err))
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "internal server error")
	}
}

// GitBranch godoc
// @Summary Read a workdir's current Git branch
// @Tags workdirs
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param workdir_id path string true "Workdir ID"
// @Success 200 {object} workdir.GitBranchResponse
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/workdirs/{workdir_id}/git-branch [get].
func (h *WorkdirHandler) GitBranch(c echo.Context) error {
	botID, _, err := h.requirePermission(c, bots.PermissionWorkspaceRead)
	if err != nil {
		return err
	}
	result, err := h.service.GitBranch(c.Request().Context(), botID, strings.TrimSpace(c.Param("workdir_id")))
	if err != nil {
		return apperror.Wrap(apperror.CodeWorkdirGitUnavailable, err, nil)
	}
	return c.JSON(http.StatusOK, result)
}

// SwitchGitBranch godoc
// @Summary Switch a workdir to an existing local Git branch
// @Tags workdirs
// @Accept json
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param workdir_id path string true "Workdir ID"
// @Param request body workdir.SwitchGitBranchRequest true "Local branch"
// @Success 200 {object} workdir.GitBranchResponse
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/workdirs/{workdir_id}/git-branch [post].
func (h *WorkdirHandler) SwitchGitBranch(c echo.Context) error {
	botID, _, err := h.requirePermission(c, bots.PermissionWorkspaceExec)
	if err != nil {
		return err
	}
	var req workdir.SwitchGitBranchRequest
	if err := c.Bind(&req); err != nil || strings.TrimSpace(req.Branch) == "" {
		return apperror.New(apperror.CodeWorkdirGitBranchUnavailable, nil)
	}
	result, err := h.service.SwitchGitBranch(c.Request().Context(), botID, strings.TrimSpace(c.Param("workdir_id")), req.Branch)
	if err != nil {
		code := apperror.CodeWorkdirGitUnavailable
		switch {
		case errors.Is(err, workdir.ErrGitBusy):
			code = apperror.CodeWorkdirGitBusy
		case errors.Is(err, workdir.ErrGitBranchUnavailable):
			code = apperror.CodeWorkdirGitBranchUnavailable
		case errors.Is(err, workdir.ErrGitSwitchFailed):
			code = apperror.CodeWorkdirGitSwitchFailed
		}
		return apperror.Wrap(code, err, nil)
	}
	return c.JSON(http.StatusOK, result)
}
