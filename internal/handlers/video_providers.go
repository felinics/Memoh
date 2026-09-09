package handlers

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/models"
	videopkg "github.com/felinics/memoh/internal/video"
)

type VideoHandler struct {
	service       *videopkg.Service
	modelsService *models.Service
	logger        *slog.Logger
}

func NewVideoHandler(log *slog.Logger, service *videopkg.Service, modelsService *models.Service) *VideoHandler {
	return &VideoHandler{
		service:       service,
		modelsService: modelsService,
		logger:        log.With(slog.String("handler", "video")),
	}
}

func (h *VideoHandler) Register(e *echo.Echo) {
	pg := e.Group("/video-providers")
	pg.GET("", h.ListProviders)
	pg.GET("/meta", h.ListMeta)
	pg.GET("/:id", h.GetProvider)
	pg.GET("/:id/models", h.ListModelsByProvider)
	pg.POST("/:id/import-models", h.ImportModels)

	mg := e.Group("/video-models")
	mg.GET("", h.ListModels)
	mg.GET("/:id", h.GetModel)
	mg.PUT("/:id", h.UpdateModel)
}

// ListMeta godoc
// @Summary List video provider metadata
// @Description List available video provider types with their models and capabilities
// @Tags video-providers
// @Success 200 {array} videopkg.ProviderMetaResponse
// @Router /video-providers/meta [get].
func (h *VideoHandler) ListMeta(c echo.Context) error {
	return c.JSON(http.StatusOK, h.service.ListMeta(c.Request().Context()))
}

// ListProviders godoc
// @Summary List video providers
// @Description List providers that support video generation
// @Tags video-providers
// @Produce json
// @Success 200 {array} videopkg.ProviderResponse
// @Failure 500 {object} ErrorResponse
// @Router /video-providers [get].
func (h *VideoHandler) ListProviders(c echo.Context) error {
	items, err := h.service.ListProviders(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, items)
}

// GetProvider godoc
// @Summary Get video provider
// @Tags video-providers
// @Produce json
// @Param id path string true "Provider ID (UUID)"
// @Success 200 {object} videopkg.ProviderResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /video-providers/{id} [get].
func (h *VideoHandler) GetProvider(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}
	item, err := h.service.GetProvider(c.Request().Context(), id)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	}
	return c.JSON(http.StatusOK, item)
}

// ListModelsByProvider godoc
// @Summary List video models by provider
// @Tags video-providers
// @Produce json
// @Param id path string true "Provider ID (UUID)"
// @Success 200 {array} videopkg.ModelResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /video-providers/{id}/models [get].
func (h *VideoHandler) ListModelsByProvider(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}
	items, err := h.service.ListModelsByProvider(c.Request().Context(), id)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, items)
}

// ImportModels godoc
// @Summary Import video models from provider
// @Tags video-providers
// @Accept json
// @Produce json
// @Param id path string true "Provider ID (UUID)"
// @Success 200 {object} videopkg.ImportModelsResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /video-providers/{id}/import-models [post].
func (h *VideoHandler) ImportModels(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}
	remoteModels, err := h.service.FetchRemoteModels(c.Request().Context(), id)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("fetch remote video models: %v", err))
	}

	resp := videopkg.ImportModelsResponse{Models: make([]string, 0, len(remoteModels))}
	for _, model := range remoteModels {
		name := strings.TrimSpace(model.Name)
		if name == "" {
			name = model.ID
		}
		_, err := h.modelsService.Create(c.Request().Context(), models.AddRequest{
			ModelID:    model.ID,
			Name:       name,
			ProviderID: id,
			Type:       models.ModelTypeVideo,
			Config:     models.ModelConfig{},
		})
		if err != nil {
			if errors.Is(err, models.ErrModelIDAlreadyExists) {
				resp.Skipped++
				continue
			}
			h.logger.Warn("failed to import video model", slog.String("model_id", model.ID), slog.Any("error", err))
			continue
		}
		resp.Created++
		resp.Models = append(resp.Models, model.ID)
	}
	return c.JSON(http.StatusOK, resp)
}

// ListModels godoc
// @Summary List all video models
// @Tags video-models
// @Produce json
// @Success 200 {array} videopkg.ModelResponse
// @Failure 500 {object} ErrorResponse
// @Router /video-models [get].
func (h *VideoHandler) ListModels(c echo.Context) error {
	items, err := h.service.ListModels(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, items)
}

// GetModel godoc
// @Summary Get a video model
// @Tags video-models
// @Produce json
// @Param id path string true "Model ID (UUID)"
// @Success 200 {object} videopkg.ModelResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /video-models/{id} [get].
func (h *VideoHandler) GetModel(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}
	resp, err := h.service.GetModel(c.Request().Context(), id)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	}
	return c.JSON(http.StatusOK, resp)
}

// UpdateModel godoc
// @Summary Update a video model
// @Tags video-models
// @Accept json
// @Produce json
// @Param id path string true "Model ID (UUID)"
// @Param request body videopkg.UpdateModelRequest true "Model update payload"
// @Success 200 {object} videopkg.ModelResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /video-models/{id} [put].
func (h *VideoHandler) UpdateModel(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}
	var req videopkg.UpdateModelRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}
	resp, err := h.service.UpdateModel(c.Request().Context(), id, req)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, resp)
}
