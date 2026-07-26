package memory

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	memcatalog "github.com/memohai/memoh/domains/memory/catalog"
	"github.com/memohai/memoh/internal/apperror"
)

type MemoryProvidersHandler struct {
	service *memcatalog.Service
	logger  *slog.Logger
}

func NewMemoryProvidersHandler(log *slog.Logger, service *memcatalog.Service) *MemoryProvidersHandler {
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
// @Failure 400 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /memory-providers [post].
func (h *MemoryProvidersHandler) Create(c echo.Context) error {
	var req memcatalog.ProviderCreateRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Invalid("bind memory provider", err)
	}
	if strings.TrimSpace(req.Name) == "" {
		return apperror.Required("name")
	}
	if strings.TrimSpace(string(req.Provider)) == "" {
		return apperror.Required("provider")
	}
	resp, err := h.service.Create(c.Request().Context(), req)
	if err != nil {
		return apperror.Internal("create memory provider", err)
	}
	return c.JSON(http.StatusCreated, resp)
}

// List godoc
// @Summary List memory providers
// @Description List configured memory providers
// @Tags memory-providers
// @Produce json
// @Success 200 {array} adapters.ProviderGetResponse
// @Failure 500 {object} apperror.Problem
// @Router /memory-providers [get].
func (h *MemoryProvidersHandler) List(c echo.Context) error {
	items, err := h.service.List(c.Request().Context())
	if err != nil {
		return apperror.Internal("list memory providers", err)
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
// @Failure 400 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Router /memory-providers/{id} [get].
func (h *MemoryProvidersHandler) Get(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return apperror.Required("id")
	}
	resp, err := h.service.Get(c.Request().Context(), id)
	if err != nil {
		return apperror.NotFound("get memory provider", err)
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
// @Failure 400 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /memory-providers/{id}/status [get].
func (h *MemoryProvidersHandler) Status(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return apperror.Required("id")
	}
	resp, err := h.service.Status(c.Request().Context(), id)
	if err != nil {
		return apperror.NotFound("get memory provider status", err)
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
// @Failure 400 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /memory-providers/{id} [put].
func (h *MemoryProvidersHandler) Update(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return apperror.Required("id")
	}
	var req memcatalog.ProviderUpdateRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Invalid("bind memory provider", err)
	}
	resp, err := h.service.Update(c.Request().Context(), id, req)
	if err != nil {
		return apperror.Internal("update memory provider", err)
	}
	return c.JSON(http.StatusOK, resp)
}

// Delete godoc
// @Summary Delete a memory provider
// @Description Delete memory provider by ID
// @Tags memory-providers
// @Param id path string true "Provider ID"
// @Success 204 "No Content"
// @Failure 400 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /memory-providers/{id} [delete].
func (h *MemoryProvidersHandler) Delete(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return apperror.Required("id")
	}
	if err := h.service.Delete(c.Request().Context(), id); err != nil {
		return apperror.Internal("delete memory provider", err)
	}
	return c.NoContent(http.StatusNoContent)
}
