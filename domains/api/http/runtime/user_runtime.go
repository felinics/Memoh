package runtime

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	httpx "github.com/memohai/memoh/domains/api/http"
	userruntime "github.com/memohai/memoh/domains/runtime/client"
	"github.com/memohai/memoh/internal/apperror"
)

// UserRuntimeHandler only manages the long-lived credential used by the
// reverse-RPC WebSocket. Runtime selection and bot bindings are separate
// product concerns and intentionally do not live here.
type UserRuntimeHandler struct {
	log     *slog.Logger
	service *userruntime.Service
}

func NewUserRuntimeHandler(log *slog.Logger, service *userruntime.Service) *UserRuntimeHandler {
	if log == nil {
		log = slog.Default()
	}
	return &UserRuntimeHandler{log: log.With(slog.String("handler", "user_runtime")), service: service}
}

func (h *UserRuntimeHandler) Register(e *echo.Echo) {
	g := e.Group("/users/me/runtimes")
	g.POST("", h.Create)
	g.GET("", h.List)
	g.DELETE("/:id", h.Delete)
}

// Create godoc
// @Summary Create a Remote Runtime credential
// @Description Register a Remote Runtime and return its reusable API token.
// @Tags user-runtimes
// @Accept json
// @Produce json
// @Param request body userruntime.CreateRuntimeRequest true "Runtime configuration"
// @Success 201 {object} userruntime.Runtime
// @Failure 400 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /users/me/runtimes [post].
func (h *UserRuntimeHandler) Create(c echo.Context) error {
	userID, err := httpx.RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	var req userruntime.CreateRuntimeRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Invalid("bind user runtime", err)
	}
	resp, err := h.service.CreateRuntime(c.Request().Context(), userID, req)
	if err != nil {
		return runtimeError(h.log, "create user runtime", err)
	}
	return c.JSON(http.StatusCreated, resp)
}

// List godoc
// @Summary List Remote Runtime credentials
// @Tags user-runtimes
// @Produce json
// @Success 200 {array} userruntime.Runtime
// @Failure 500 {object} apperror.Problem
// @Router /users/me/runtimes [get].
func (h *UserRuntimeHandler) List(c echo.Context) error {
	userID, err := httpx.RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	items, err := h.service.ListRuntimes(c.Request().Context(), userID)
	if err != nil {
		return runtimeError(h.log, "list user runtimes", err)
	}
	return c.JSON(http.StatusOK, items)
}

// Delete godoc
// @Summary Revoke a Remote Runtime credential
// @Tags user-runtimes
// @Param id path string true "Runtime ID"
// @Success 204 "No Content"
// @Failure 400 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /users/me/runtimes/{id} [delete].
func (h *UserRuntimeHandler) Delete(c echo.Context) error {
	userID, err := httpx.RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	if err := h.service.RevokeRuntime(c.Request().Context(), userID, strings.TrimSpace(c.Param("id"))); err != nil {
		return runtimeError(h.log, "revoke user runtime", err)
	}
	return c.NoContent(http.StatusNoContent)
}

func runtimeError(log *slog.Logger, op string, err error) error {
	switch {
	case errors.Is(err, userruntime.ErrInvalidInput), errors.Is(err, userruntime.ErrInvalidKey):
		return apperror.Invalid(op, err)
	case errors.Is(err, userruntime.ErrRuntimeNotFound):
		return apperror.NotFound(op, err)
	case errors.Is(err, userruntime.ErrRuntimeNameTaken):
		return apperror.Conflict(op, err)
	default:
		if log != nil {
			log.Error("runtime request failed", slog.Any("error", err))
		}
		return apperror.Internal(op, err)
	}
}
