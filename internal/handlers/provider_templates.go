package handlers

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/providertemplates"
)

type ProviderTemplatesHandler struct {
	service *providertemplates.Service
}

func NewProviderTemplatesHandler(service *providertemplates.Service) *ProviderTemplatesHandler {
	return &ProviderTemplatesHandler{service: service}
}

func (h *ProviderTemplatesHandler) Register(e *echo.Echo) {
	group := e.Group("/provider-templates")
	group.GET("", h.List)
	group.GET("/:id", h.Get)
}

// List godoc
// @Summary List provider templates
// @Description List active global provider templates and whether the current tenant has configured each template
// @Tags provider-templates
// @Produce json
// @Param domain query string false "Template domain (llm, speech, transcription, video)"
// @Success 200 {array} providertemplates.GetResponse
// @Failure 400 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /provider-templates [get].
func (h *ProviderTemplatesHandler) List(c echo.Context) error {
	items, err := h.service.List(c.Request().Context(), c.QueryParam("domain"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, items)
}

// Get godoc
// @Summary Get a provider template
// @Description Get an active global provider template and its model catalog
// @Tags provider-templates
// @Produce json
// @Param id path string true "Provider template ID"
// @Success 200 {object} providertemplates.GetResponse
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /provider-templates/{id} [get].
func (h *ProviderTemplatesHandler) Get(c echo.Context) error {
	item, err := h.service.Get(c.Request().Context(), strings.TrimSpace(c.Param("id")), "")
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, item)
}
