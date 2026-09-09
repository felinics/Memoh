package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/userruntime"
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
// @Failure 400 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /users/me/runtimes [post].
func (h *UserRuntimeHandler) Create(c echo.Context) error {
	userID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	var req userruntime.CreateRuntimeRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	resp, err := h.service.CreateRuntime(c.Request().Context(), userID, req)
	if err != nil {
		return runtimeHTTPError(h.log, err)
	}
	return c.JSON(http.StatusCreated, resp)
}

// List godoc
// @Summary List Remote Runtime credentials
// @Tags user-runtimes
// @Produce json
// @Success 200 {array} userruntime.Runtime
// @Failure 500 {object} ErrorResponse
// @Router /users/me/runtimes [get].
func (h *UserRuntimeHandler) List(c echo.Context) error {
	userID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	items, err := h.service.ListRuntimes(c.Request().Context(), userID)
	if err != nil {
		return runtimeHTTPError(h.log, err)
	}
	return c.JSON(http.StatusOK, items)
}

// Delete godoc
// @Summary Revoke a Remote Runtime credential
// @Tags user-runtimes
// @Param id path string true "Runtime ID"
// @Success 204 "No Content"
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /users/me/runtimes/{id} [delete].
func (h *UserRuntimeHandler) Delete(c echo.Context) error {
	userID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	if err := h.service.RevokeRuntime(c.Request().Context(), userID, strings.TrimSpace(c.Param("id"))); err != nil {
		return runtimeHTTPError(h.log, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func runtimeHTTPError(log *slog.Logger, err error) error {
	if errors.Is(err, userruntime.ErrInvalidInput) || errors.Is(err, userruntime.ErrInvalidKey) {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if errors.Is(err, db.ErrNotFound) {
		return echo.NewHTTPError(http.StatusNotFound, "runtime not found")
	}
	if db.IsUniqueViolation(err) {
		return echo.NewHTTPError(http.StatusConflict, "runtime already exists")
	}
	if log != nil {
		log.Error("runtime request failed", slog.Any("error", err))
	}
	return echo.NewHTTPError(http.StatusInternalServerError, "internal server error")
}
