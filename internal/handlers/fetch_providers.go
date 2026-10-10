package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/fetchproviders"
	"github.com/felinics/memoh/internal/httpx"
)

type FetchProvidersHandler struct {
	service *fetchproviders.Service
	logger  *slog.Logger
}

func NewFetchProvidersHandler(log *slog.Logger, service *fetchproviders.Service) *FetchProvidersHandler {
	return &FetchProvidersHandler{
		service: service,
		logger:  log.With(slog.String("handler", "fetch_providers")),
	}
}

func (h *FetchProvidersHandler) Register(e *echo.Echo) {
	group := e.Group("/fetch-providers")
	group.GET("/meta", h.ListMeta)
	group.POST("", h.Create)
	group.GET("", h.List)
	group.GET("/:id", h.Get)
	group.PUT("/:id", h.Update)
	group.DELETE("/:id", h.Delete)
}

// ListMeta godoc
// @Summary List fetch provider metadata
// @Description List available fetch provider types and config schemas
// @Tags fetch-providers
// @Success 200 {array} fetchproviders.ProviderMeta
// @Router /fetch-providers/meta [get].
func (h *FetchProvidersHandler) ListMeta(c echo.Context) error {
	return c.JSON(http.StatusOK, h.service.ListMeta(c.Request().Context()))
}

// Create godoc
// @Summary Create a fetch provider
// @Description Create a fetch provider configuration
// @Tags fetch-providers
// @Accept json
// @Produce json
// @Param request body fetchproviders.CreateRequest true "Fetch provider configuration"
// @Success 201 {object} fetchproviders.GetResponse
// @Failure 400 {object} server.Problem
// @Failure 500 {object} server.Problem
// @Router /fetch-providers [post].
func (h *FetchProvidersHandler) Create(c echo.Context) error {
	var req fetchproviders.CreateRequest
	if err := c.Bind(&req); err != nil {
		return err
	}
	if strings.TrimSpace(req.Name) == "" {
		return apperror.FieldRequired("name")
	}
	if strings.TrimSpace(string(req.Provider)) == "" {
		return apperror.FieldRequired("provider")
	}
	resp, err := h.service.Create(c.Request().Context(), req)
	if err != nil {
		return fetchProviderHTTPError(err)
	}
	return c.JSON(http.StatusCreated, resp)
}

// List godoc
// @Summary List fetch providers
// @Description List configured fetch providers
// @Tags fetch-providers
// @Accept json
// @Produce json
// @Param provider query string false "Provider filter (native)"
// @Success 200 {array} fetchproviders.GetResponse
// @Failure 500 {object} server.Problem
// @Router /fetch-providers [get].
func (h *FetchProvidersHandler) List(c echo.Context) error {
	items, err := h.service.List(c.Request().Context(), c.QueryParam("provider"))
	if err != nil {
		return errs.Wrap(err, "list fetch providers")
	}
	return c.JSON(http.StatusOK, items)
}

// Get godoc
// @Summary Get a fetch provider
// @Description Get fetch provider by ID
// @Tags fetch-providers
// @Accept json
// @Produce json
// @Param id path string true "Provider ID"
// @Success 200 {object} fetchproviders.GetResponse
// @Failure 400 {object} server.Problem
// @Failure 404 {object} server.Problem
// @Router /fetch-providers/{id} [get].
func (h *FetchProvidersHandler) Get(c echo.Context) error {
	id, err := httpx.RequiredParam(c, "id")
	if err != nil {
		return err
	}
	resp, err := h.service.Get(c.Request().Context(), id)
	if err != nil {
		return resourceLookupError(err, "id", apperror.CodeFetchProviderNotFound, "get fetch provider")
	}
	return c.JSON(http.StatusOK, resp)
}

// Update godoc
// @Summary Update a fetch provider
// @Description Update fetch provider by ID
// @Tags fetch-providers
// @Accept json
// @Produce json
// @Param id path string true "Provider ID"
// @Param request body fetchproviders.UpdateRequest true "Updated configuration"
// @Success 200 {object} fetchproviders.GetResponse
// @Failure 400 {object} server.Problem
// @Failure 500 {object} server.Problem
// @Router /fetch-providers/{id} [put].
func (h *FetchProvidersHandler) Update(c echo.Context) error {
	id, err := httpx.RequiredParam(c, "id")
	if err != nil {
		return err
	}
	var req fetchproviders.UpdateRequest
	if err := c.Bind(&req); err != nil {
		return err
	}
	resp, err := h.service.Update(c.Request().Context(), id, req)
	if err != nil {
		return fetchProviderHTTPError(err)
	}
	return c.JSON(http.StatusOK, resp)
}

// Delete godoc
// @Summary Delete a fetch provider
// @Description Delete fetch provider by ID
// @Tags fetch-providers
// @Accept json
// @Produce json
// @Param id path string true "Provider ID"
// @Success 204 "No Content"
// @Failure 400 {object} server.Problem
// @Failure 500 {object} server.Problem
// @Router /fetch-providers/{id} [delete].
func (h *FetchProvidersHandler) Delete(c echo.Context) error {
	id, err := httpx.RequiredParam(c, "id")
	if err != nil {
		return err
	}
	if err := h.service.Delete(c.Request().Context(), id); err != nil {
		return fetchProviderHTTPError(err)
	}
	return c.NoContent(http.StatusNoContent)
}

func fetchProviderHTTPError(err error) error {
	switch {
	case errors.Is(err, fetchproviders.ErrManagedNativeProvider):
		return apperror.Wrap(apperror.CodeFetchProviderNativeManaged, err, nil)
	case errors.Is(err, fetchproviders.ErrInvalidProvider):
		return apperror.FieldInvalid("provider", err)
	}
	return errs.Wrap(err, "handle fetch provider error")
}
