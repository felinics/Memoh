package handlers

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	memprovider "github.com/felinics/memoh/internal/memory/adapters"
)

type MemoryProvidersHandler struct {
	service *memprovider.Service
	logger  *slog.Logger
}

func NewMemoryProvidersHandler(log *slog.Logger, service *memprovider.Service) *MemoryProvidersHandler {
	return &MemoryProvidersHandler{
		service: service,
		logger:  log.With(slog.String("handler", "memory_providers")),
	}
}

func (h *MemoryProvidersHandler) Register(e *echo.Echo) {
	group := e.Group("/memory-providers")
	group.GET("/meta", h.ListMeta)
	group.POST("", h.Create)
	group.GET("", h.List)
	group.GET("/:id", h.Get)
	group.GET("/:id/status", h.Status)
	group.PUT("/:id", h.Update)
	group.DELETE("/:id", h.Delete)
}

// ListMeta godoc
// @Summary List memory provider metadata
// @Description List available memory provider types and config schemas
// @Tags memory-providers
// @Success 200 {array} adapters.ProviderMeta
// @Router /memory-providers/meta [get].
func (h *MemoryProvidersHandler) ListMeta(c echo.Context) error {
	return c.JSON(http.StatusOK, h.service.ListMeta(c.Request().Context()))
}

// Create godoc
// @Summary Create a memory provider
// @Description Create a memory provider configuration
// @Tags memory-providers
// @Accept json
// @Produce json
// @Param request body adapters.ProviderCreateRequest true "Memory provider configuration"
// @Success 201 {object} adapters.ProviderGetResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /memory-providers [post].
func (h *MemoryProvidersHandler) Create(c echo.Context) error {
	var req memprovider.ProviderCreateRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if strings.TrimSpace(req.Name) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "name is required")
	}
	if strings.TrimSpace(string(req.Provider)) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "provider is required")
	}
	resp, err := h.service.Create(c.Request().Context(), req)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusCreated, resp)
}

// List godoc
// @Summary List memory providers
// @Description List configured memory providers
// @Tags memory-providers
// @Produce json
// @Success 200 {array} adapters.ProviderGetResponse
// @Failure 500 {object} ErrorResponse
// @Router /memory-providers [get].
func (h *MemoryProvidersHandler) List(c echo.Context) error {
	items, err := h.service.List(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, items)
}

// Get godoc
// @Summary Get a memory provider
// @Description Get memory provider by ID
// @Tags memory-providers
// @Produce json
// @Param id path string true "Provider ID"
// @Success 200 {object} adapters.ProviderGetResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /memory-providers/{id} [get].
func (h *MemoryProvidersHandler) Get(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}
	resp, err := h.service.Get(c.Request().Context(), id)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	}
	return c.JSON(http.StatusOK, resp)
}

// Status godoc
// @Summary Get memory provider status
// @Description Get runtime status data for a memory provider
// @Tags memory-providers
// @Produce json
// @Param id path string true "Provider ID"
// @Success 200 {object} adapters.ProviderStatusResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /memory-providers/{id}/status [get].
func (h *MemoryProvidersHandler) Status(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}
	resp, err := h.service.Status(c.Request().Context(), id)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	}
	return c.JSON(http.StatusOK, resp)
}

// Update godoc
// @Summary Update a memory provider
// @Description Update memory provider by ID
// @Tags memory-providers
// @Accept json
// @Produce json
// @Param id path string true "Provider ID"
// @Param request body adapters.ProviderUpdateRequest true "Updated configuration"
// @Success 200 {object} adapters.ProviderGetResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /memory-providers/{id} [put].
func (h *MemoryProvidersHandler) Update(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}
	var req memprovider.ProviderUpdateRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	resp, err := h.service.Update(c.Request().Context(), id, req)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, resp)
}

// Delete godoc
// @Summary Delete a memory provider
// @Description Delete memory provider by ID
// @Tags memory-providers
// @Param id path string true "Provider ID"
// @Success 204 "No Content"
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /memory-providers/{id} [delete].
func (h *MemoryProvidersHandler) Delete(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}
	if err := h.service.Delete(c.Request().Context(), id); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.NoContent(http.StatusNoContent)
}
