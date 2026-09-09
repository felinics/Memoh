package handlers

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/searchproviders"
)

type SearchProvidersHandler struct {
	service *searchproviders.Service
	logger  *slog.Logger
}

func NewSearchProvidersHandler(log *slog.Logger, service *searchproviders.Service) *SearchProvidersHandler {
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
// @Success 200 {array} searchproviders.ProviderMeta
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
// @Param request body searchproviders.CreateRequest true "Search provider configuration"
// @Success 201 {object} searchproviders.GetResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /search-providers [post].
func (h *SearchProvidersHandler) Create(c echo.Context) error {
	var req searchproviders.CreateRequest
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
		if apperror.CodeOf(err) != "" {
			return err
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
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
// @Success 200 {array} searchproviders.GetResponse
// @Failure 500 {object} ErrorResponse
// @Router /search-providers [get].
func (h *SearchProvidersHandler) List(c echo.Context) error {
	items, err := h.service.List(c.Request().Context(), c.QueryParam("provider"))
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
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
// @Success 200 {object} searchproviders.GetResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /search-providers/{id} [get].
func (h *SearchProvidersHandler) Get(c echo.Context) error {
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

// Update godoc
// @Summary Update a search provider
// @Description Update search provider by ID
// @Tags search-providers
// @Accept json
// @Produce json
// @Param id path string true "Provider ID"
// @Param request body searchproviders.UpdateRequest true "Updated configuration"
// @Success 200 {object} searchproviders.GetResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /search-providers/{id} [put].
func (h *SearchProvidersHandler) Update(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}
	var req searchproviders.UpdateRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	resp, err := h.service.Update(c.Request().Context(), id, req)
	if err != nil {
		if apperror.CodeOf(err) != "" {
			return err
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
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
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /search-providers/{id} [delete].
func (h *SearchProvidersHandler) Delete(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}
	if err := h.service.Delete(c.Request().Context(), id); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.NoContent(http.StatusNoContent)
}
