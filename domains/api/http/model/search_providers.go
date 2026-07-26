package model

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/domains/model/search"
	"github.com/memohai/memoh/internal/apperror"
)

type SearchProvidersHandler struct {
	service *search.Service
	logger  *slog.Logger
}

func NewSearchProvidersHandler(log *slog.Logger, service *search.Service) *SearchProvidersHandler {
	return &SearchProvidersHandler{
		service: service,
		logger:  log.With(slog.String("handler", "search_providers")),
	}
}

func (h *SearchProvidersHandler) Register(e *echo.Echo) {
	group := e.Group("/search-providers")
	group.GET("/meta", h.ListMeta)
	group.POST("", h.Create)
	group.GET("", h.List)
	group.GET("/:id", h.Get)
	group.PUT("/:id", h.Update)
	group.DELETE("/:id", h.Delete)
}

// ListMeta godoc
// @Summary List search provider metadata
// @Description List available search provider types and config schemas
// @Tags search-providers
// @Success 200 {array} search.ProviderMeta
// @Router /search-providers/meta [get].
func (h *SearchProvidersHandler) ListMeta(c echo.Context) error {
	return c.JSON(http.StatusOK, h.service.ListMeta(c.Request().Context()))
}

// Create godoc
// @Summary Create a search provider
// @Description Create a search provider configuration
// @Tags search-providers
// @Accept json
// @Produce json
// @Param request body search.CreateRequest true "Search provider configuration"
// @Success 201 {object} search.GetResponse
// @Failure 400 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /search-providers [post].
func (h *SearchProvidersHandler) Create(c echo.Context) error {
	var req search.CreateRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Invalid("bind search provider", err)
	}
	if strings.TrimSpace(req.Name) == "" {
		return apperror.Required("name")
	}
	if strings.TrimSpace(string(req.Provider)) == "" {
		return apperror.Required("provider")
	}
	resp, err := h.service.Create(c.Request().Context(), req)
	if err != nil {
		return searchProviderError("create search provider", err)
	}
	return c.JSON(http.StatusCreated, resp)
}

// List godoc
// @Summary List search providers
// @Description List configured search providers
// @Tags search-providers
// @Accept json
// @Produce json
// @Param provider query string false "Provider filter (brave)"
// @Success 200 {array} search.GetResponse
// @Failure 500 {object} apperror.Problem
// @Router /search-providers [get].
func (h *SearchProvidersHandler) List(c echo.Context) error {
	items, err := h.service.List(c.Request().Context(), c.QueryParam("provider"))
	if err != nil {
		return apperror.Internal("list search providers", err)
	}
	return c.JSON(http.StatusOK, items)
}

// Get godoc
// @Summary Get a search provider
// @Description Get search provider by ID
// @Tags search-providers
// @Accept json
// @Produce json
// @Param id path string true "Provider ID"
// @Success 200 {object} search.GetResponse
// @Failure 400 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Router /search-providers/{id} [get].
func (h *SearchProvidersHandler) Get(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return apperror.Required("id")
	}
	resp, err := h.service.Get(c.Request().Context(), id)
	if err != nil {
		return apperror.NotFound("get search provider", err)
	}
	return c.JSON(http.StatusOK, resp)
}

// Update godoc
// @Summary Update a search provider
// @Description Update search provider by ID
// @Tags search-providers
// @Accept json
// @Produce json
// @Param id path string true "Provider ID"
// @Param request body search.UpdateRequest true "Updated configuration"
// @Success 200 {object} search.GetResponse
// @Failure 400 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /search-providers/{id} [put].
func (h *SearchProvidersHandler) Update(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return apperror.Required("id")
	}
	var req search.UpdateRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Invalid("bind search provider", err)
	}
	resp, err := h.service.Update(c.Request().Context(), id, req)
	if err != nil {
		return searchProviderError("update search provider", err)
	}
	return c.JSON(http.StatusOK, resp)
}

// Delete godoc
// @Summary Delete a search provider
// @Description Delete search provider by ID
// @Tags search-providers
// @Accept json
// @Produce json
// @Param id path string true "Provider ID"
// @Success 204 "No Content"
// @Failure 400 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /search-providers/{id} [delete].
func (h *SearchProvidersHandler) Delete(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return apperror.Required("id")
	}
	if err := h.service.Delete(c.Request().Context(), id); err != nil {
		return apperror.Internal("delete search provider", err)
	}
	return c.NoContent(http.StatusNoContent)
}

// searchProviderError names the two service failures the client can act on and
// lets everything else fall through as an internal error.
func searchProviderError(op string, err error) error {
	switch {
	case errors.Is(err, search.ErrProviderTypeConflict):
		return apperror.Conflict(op, err).WithCode(apperror.CodeSearchProviderTypeConflict, nil)
	case errors.Is(err, search.ErrProviderNameTaken):
		return apperror.Conflict(op, err).WithCode(apperror.CodeProviderNameTaken, nil)
	default:
		return apperror.Internal(op, err)
	}
}
