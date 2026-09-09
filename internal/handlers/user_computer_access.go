package handlers

import (
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/workspace"
)

// UserComputerAccessHandler serves the account-level Computer ACL view: which
// of the user's bots may use which of their Remote Runtimes. Writes still go
// through the bot-scoped workspace-targets endpoints (mount/unmount); this
// read model exists so the account page and its access dialog can render
// without one request per bot.
type UserComputerAccessHandler struct {
	log     *slog.Logger
	service *workspace.RemoteWorkspaceService
}

func NewUserComputerAccessHandler(log *slog.Logger, service *workspace.RemoteWorkspaceService) *UserComputerAccessHandler {
	if log == nil {
		log = slog.Default()
	}
	return &UserComputerAccessHandler{
		log:     log.With(slog.String("handler", "user_computer_access")),
		service: service,
	}
}

func (h *UserComputerAccessHandler) Register(e *echo.Echo) {
	e.GET("/users/me/computer-access", h.List)
}

// List godoc
// @Summary List the caller's bot-to-Computer access grants
// @Description Every live Remote Runtime mount held by the caller's bots, across all of their runtimes.
// @Tags user-runtimes
// @Produce json
// @Success 200 {object} workspace.WorkspaceTargetGrantsResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /users/me/computer-access [get].
func (h *UserComputerAccessHandler) List(c echo.Context) error {
	userID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	grants, err := h.service.ListAccountGrants(c.Request().Context(), userID)
	if err != nil {
		return workspaceTargetHTTPError(h.log, err)
	}
	return c.JSON(http.StatusOK, workspace.WorkspaceTargetGrantsResponse{Grants: grants})
}
