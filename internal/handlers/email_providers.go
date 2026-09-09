package handlers

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/auth"
	"github.com/felinics/memoh/internal/email"
)

type EmailProvidersHandler struct {
	service *email.Service
	logger  *slog.Logger
}

func NewEmailProvidersHandler(log *slog.Logger, service *email.Service) *EmailProvidersHandler {
	return &EmailProvidersHandler{
		service: service,
		logger:  log.With(slog.String("handler", "email_providers")),
	}
}

func (h *EmailProvidersHandler) Register(e *echo.Echo) {
	g := e.Group("/email-providers")
	g.GET("/meta", h.ListMeta)
	g.POST("", h.Create)
	g.GET("", h.List)
	g.GET("/:id", h.Get)
	g.PUT("/:id", h.Update)
	g.DELETE("/:id", h.Delete)
}

// ListMeta godoc
// @Summary List email provider metadata
// @Description List available email provider types and config schemas
// @Tags email-providers
// @Success 200 {array} email.ProviderMeta
// @Router /email-providers/meta [get].
func (h *EmailProvidersHandler) ListMeta(c echo.Context) error {
	return c.JSON(http.StatusOK, h.service.ListMeta(c.Request().Context()))
}

// Create godoc
// @Summary Create an email provider
// @Tags email-providers
// @Accept json
// @Produce json
// @Param request body email.CreateProviderRequest true "Email provider configuration"
// @Success 201 {object} email.ProviderResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /email-providers [post].
func (h *EmailProvidersHandler) Create(c echo.Context) error {
	userID, err := auth.UserIDFromContext(c)
	if err != nil {
		return err
	}
	var req email.CreateProviderRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if strings.TrimSpace(req.Name) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "name is required")
	}
	if strings.TrimSpace(string(req.Provider)) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "provider is required")
	}
	resp, err := h.service.CreateProvider(c.Request().Context(), userID, req)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusCreated, resp)
}

// List godoc
// @Summary List email providers
// @Tags email-providers
// @Produce json
// @Param provider query string false "Provider type filter"
// @Success 200 {array} email.ProviderResponse
// @Failure 500 {object} ErrorResponse
// @Router /email-providers [get].
func (h *EmailProvidersHandler) List(c echo.Context) error {
	userID, err := auth.UserIDFromContext(c)
	if err != nil {
		return err
	}
	items, err := h.service.ListProviders(c.Request().Context(), userID, c.QueryParam("provider"))
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, items)
}

// Get godoc
// @Summary Get an email provider
// @Tags email-providers
// @Produce json
// @Param id path string true "Provider ID"
// @Success 200 {object} email.ProviderResponse
// @Failure 404 {object} ErrorResponse
// @Router /email-providers/{id} [get].
func (h *EmailProvidersHandler) Get(c echo.Context) error {
	userID, err := auth.UserIDFromContext(c)
	if err != nil {
		return err
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}
	resp, err := h.service.GetProvider(c.Request().Context(), userID, id)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	}
	return c.JSON(http.StatusOK, resp)
}

// Update godoc
// @Summary Update an email provider
// @Tags email-providers
// @Accept json
// @Produce json
// @Param id path string true "Provider ID"
// @Param request body email.UpdateProviderRequest true "Updated configuration"
// @Success 200 {object} email.ProviderResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /email-providers/{id} [put].
func (h *EmailProvidersHandler) Update(c echo.Context) error {
	userID, err := auth.UserIDFromContext(c)
	if err != nil {
		return err
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}
	var req email.UpdateProviderRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	resp, err := h.service.UpdateProvider(c.Request().Context(), userID, id, req)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, resp)
}

// Delete godoc
// @Summary Delete an email provider
// @Tags email-providers
// @Param id path string true "Provider ID"
// @Success 204 "No Content"
// @Failure 500 {object} ErrorResponse
// @Router /email-providers/{id} [delete].
func (h *EmailProvidersHandler) Delete(c echo.Context) error {
	userID, err := auth.UserIDFromContext(c)
	if err != nil {
		return err
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}
	if _, err := h.service.GetProvider(c.Request().Context(), userID, id); err != nil {
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	}
	if err := h.service.DeleteProvider(c.Request().Context(), userID, id); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.NoContent(http.StatusNoContent)
}
