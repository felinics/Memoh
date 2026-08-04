package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/internal/accounts"
	"github.com/memohai/memoh/internal/bots"
	"github.com/memohai/memoh/internal/db"
	"github.com/memohai/memoh/internal/project"
	"github.com/memohai/memoh/internal/workspace"
)

// botProjectService is the slice of *project.Service the handler needs.
type botProjectService interface {
	Create(ctx context.Context, botID, userID string, req project.CreateRequest) (project.Project, error)
	List(ctx context.Context, botID string, includeArchived bool) ([]project.Project, error)
	Rename(ctx context.Context, botID, projectID, name string) (project.Project, error)
	Archive(ctx context.Context, botID, projectID string) error
}

// ProjectHandler manages a bot's project directories.
type ProjectHandler struct {
	log      *slog.Logger
	service  botProjectService
	bots     *bots.Service
	accounts *accounts.Service
}

func NewProjectHandler(
	log *slog.Logger,
	service *project.Service,
	botService *bots.Service,
	accountService *accounts.Service,
) *ProjectHandler {
	if log == nil {
		log = slog.Default()
	}
	return &ProjectHandler{
		log:      log.With(slog.String("handler", "projects")),
		service:  service,
		bots:     botService,
		accounts: accountService,
	}
}

func (h *ProjectHandler) Register(e *echo.Echo) {
	g := e.Group("/bots/:bot_id/projects")
	g.POST("", h.Create)
	g.GET("", h.List)
	g.PATCH("/:project_id", h.Rename)
	g.DELETE("/:project_id", h.Archive)
}

// Create godoc
// @Summary Create a Bot project
// @Description Registers a named project directory on a workspace target. The directory must already exist on that target.
// @Tags projects
// @Accept json
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param request body project.CreateRequest true "Project"
// @Success 201 {object} project.Project
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Router /bots/{bot_id}/projects [post].
func (h *ProjectHandler) Create(c echo.Context) error {
	botID, identityID, err := h.requirePermission(c, bots.PermissionManage)
	if err != nil {
		return err
	}
	var req project.CreateRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	created, err := h.service.Create(c.Request().Context(), botID, identityID, req)
	if err != nil {
		return projectHTTPError(h.log, err)
	}
	return c.JSON(http.StatusCreated, created)
}

// List godoc
// @Summary List a Bot's projects
// @Tags projects
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param include_archived query bool false "Include archived projects"
// @Success 200 {object} project.ProjectsResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/projects [get].
func (h *ProjectHandler) List(c echo.Context) error {
	botID, _, err := h.requirePermission(c, bots.PermissionWorkspaceRead)
	if err != nil {
		return err
	}
	includeArchived := strings.EqualFold(c.QueryParam("include_archived"), "true")
	projects, err := h.service.List(c.Request().Context(), botID, includeArchived)
	if err != nil {
		return projectHTTPError(h.log, err)
	}
	return c.JSON(http.StatusOK, project.ProjectsResponse{Projects: projects})
}

// Rename godoc
// @Summary Rename a Bot project
// @Description Only the name can change. The target and path are immutable: they are baked into the working directory of every session bound to this project.
// @Tags projects
// @Accept json
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param project_id path string true "Project ID"
// @Param request body project.UpdateRequest true "New name"
// @Success 200 {object} project.Project
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /bots/{bot_id}/projects/{project_id} [patch].
func (h *ProjectHandler) Rename(c echo.Context) error {
	botID, _, err := h.requirePermission(c, bots.PermissionManage)
	if err != nil {
		return err
	}
	projectID := strings.TrimSpace(c.Param("project_id"))
	if projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project_id is required")
	}
	var req project.UpdateRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	renamed, err := h.service.Rename(c.Request().Context(), botID, projectID, req.Name)
	if err != nil {
		return projectHTTPError(h.log, err)
	}
	return c.JSON(http.StatusOK, renamed)
}

// Archive godoc
// @Summary Archive a Bot project
// @Description Archiving refuses new session bindings but keeps existing sessions working: their directory never changes underneath them.
// @Tags projects
// @Param bot_id path string true "Bot ID"
// @Param project_id path string true "Project ID"
// @Success 204 "No Content"
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /bots/{bot_id}/projects/{project_id} [delete].
func (h *ProjectHandler) Archive(c echo.Context) error {
	botID, _, err := h.requirePermission(c, bots.PermissionManage)
	if err != nil {
		return err
	}
	projectID := strings.TrimSpace(c.Param("project_id"))
	if projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project_id is required")
	}
	if err := h.service.Archive(c.Request().Context(), botID, projectID); err != nil {
		return projectHTTPError(h.log, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// requirePermission authorizes the caller on the bot and returns the bot's
// canonical UUID (the path parameter may be a name slug) plus the caller's
// channel identity id.
func (h *ProjectHandler) requirePermission(c echo.Context, permission string) (string, string, error) {
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

func projectHTTPError(log *slog.Logger, err error) error {
	switch {
	case errors.Is(err, project.ErrNameRequired),
		errors.Is(err, project.ErrPathRequired),
		errors.Is(err, project.ErrInvalidPath),
		errors.Is(err, project.ErrPathNotFound),
		errors.Is(err, project.ErrPathNotDirectory):
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	case errors.Is(err, project.ErrProjectNotFound),
		errors.Is(err, workspace.ErrWorkspaceTargetNotFound),
		errors.Is(err, db.ErrNotFound):
		return echo.NewHTTPError(http.StatusNotFound, "project not found")
	case errors.Is(err, project.ErrDuplicatePath),
		errors.Is(err, project.ErrProjectArchived),
		errors.Is(err, workspace.ErrRemoteRuntimeOffline),
		errors.Is(err, workspace.ErrRemoteRuntimeRevoked),
		errors.Is(err, workspace.ErrRemoteRuntimeOwnerMismatch),
		errors.Is(err, workspace.ErrRemoteRuntimeClientUpdateNeeded),
		errors.Is(err, workspace.ErrRemoteRuntimeNotUsable),
		errors.Is(err, workspace.ErrRemoteWorkspaceNotBound):
		return echo.NewHTTPError(http.StatusConflict, err.Error())
	default:
		if log != nil {
			log.Error("project request failed", slog.Any("error", err))
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "internal server error")
	}
}
