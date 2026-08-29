package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/auth"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/oauthctx"
	"github.com/felinics/memoh/internal/providers"
)

type ModelsHandler struct {
	service   *models.Service
	providers *providers.Service
	logger    *slog.Logger
}

func NewModelsHandler(log *slog.Logger, service *models.Service, providerSvc *providers.Service) *ModelsHandler {
	return &ModelsHandler{
		service:   service,
		providers: providerSvc,
		logger:    log.With(slog.String("handler", "models")),
	}
}

// withReasoning fills each model's resolved reasoning options. It is computed
// here rather than stored because it depends on the provider's client type,
// which lives one join away from the model row — and because it is a projection
// of the catalog, not a fact about it.
//
// A provider that cannot be read yields options resolved with an empty client
// type: the model's own tiers still come through, only the wire policy is
// missing. That is strictly better than omitting the field and sending the
// frontend back to deriving it.
func (h *ModelsHandler) withReasoning(ctx context.Context, list []models.GetResponse) []models.GetResponse {
	clientTypes := make(map[string]string, 4)
	for i := range list {
		if list[i].Type != models.ModelTypeChat {
			continue
		}
		providerID := list[i].ProviderID
		clientType, cached := clientTypes[providerID]
		if !cached {
			if h.providers != nil {
				if p, err := h.providers.Get(ctx, providerID); err == nil {
					clientType = p.ClientType
				}
			}
			clientTypes[providerID] = clientType
		}
		withReasoningForClientType(list[i:i+1], clientType)
	}
	return list
}

// withReasoningForClientType projects the model's catalog declarations into the
// capability object returned by every model-listing surface. Keep this helper
// independent of a handler so /models and /providers/:id/models cannot drift.
func withReasoningForClientType(list []models.GetResponse, clientType string) []models.GetResponse {
	for i := range list {
		if list[i].Type != models.ModelTypeChat {
			continue
		}
		opts := list[i].ReasoningOptions(clientType)
		list[i].Reasoning = &opts
	}
	return list
}

// withReasoningOne is withReasoning for a single model.
func (h *ModelsHandler) withReasoningOne(ctx context.Context, m models.GetResponse) models.GetResponse {
	return h.withReasoning(ctx, []models.GetResponse{m})[0]
}

func (h *ModelsHandler) Register(e *echo.Echo) {
	group := e.Group("/models")
	group.POST("", h.Create)
	group.GET("", h.List)
	group.GET("/:id", h.GetByID)
	group.GET("/model/:modelId", h.GetByModelID)
	group.PUT("/:id", h.UpdateByID)
	group.PUT("/model/:modelId", h.UpdateByModelID)
	group.DELETE("/:id", h.DeleteByID)
	group.DELETE("/model/:modelId", h.DeleteByModelID)
	group.GET("/count", h.Count)
	group.POST("/:id/test", h.Test)
}

// Create godoc
// @Summary Create a new model
// @Description Create a new model configuration
// @Tags models
// @Param payload body models.AddRequest true "Model configuration"
// @Success 201 {object} models.AddResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /models [post].
func (h *ModelsHandler) Create(c echo.Context) error {
	var req models.AddRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	resp, err := h.service.Create(c.Request().Context(), req)
	if err != nil {
		if errors.Is(err, models.ErrModelIDAlreadyExists) {
			return echo.NewHTTPError(http.StatusConflict, "model_id already exists under the selected provider")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusCreated, resp)
}

// List godoc
// @Summary List all models
// @Description Get a list of all configured models, optionally filtered by type or provider client type
// @Tags models
// @Param type query string false "Model type (chat, embedding)"
// @Param client_type query string false "Provider client type (openai-responses, openai-completions, anthropic-messages, google-generative-ai)"
// @Success 200 {array} models.GetResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /models [get].
func (h *ModelsHandler) List(c echo.Context) error {
	modelType := c.QueryParam("type")
	clientType := c.QueryParam("client_type")

	var resp []models.GetResponse
	var err error

	switch {
	case modelType != "":
		resp, err = h.service.ListEnabledByType(c.Request().Context(), models.ModelType(modelType))
	case clientType != "":
		ct := models.ClientType(clientType)
		if !models.IsLLMClientType(ct) {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid client type for LLM models endpoint")
		}
		resp, err = h.service.ListEnabledByProviderClientType(c.Request().Context(), ct)
	default:
		resp, err = h.service.ListEnabled(c.Request().Context())
	}

	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, h.withReasoning(c.Request().Context(), resp))
}

// GetByID godoc
// @Summary Get model by internal ID
// @Description Get a model configuration by its internal UUID
// @Tags models
// @Param id path string true "Model internal ID (UUID)"
// @Success 200 {object} models.GetResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /models/{id} [get].
func (h *ModelsHandler) GetByID(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}

	resp, err := h.service.GetByID(c.Request().Context(), id)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	}
	return c.JSON(http.StatusOK, h.withReasoningOne(c.Request().Context(), resp))
}

// GetByModelID godoc
// @Summary Get model by model ID
// @Description Get a model configuration by its model_id field (e.g., gpt-4)
// @Tags models
// @Param modelId path string true "Model ID (e.g., gpt-4)"
// @Success 200 {object} models.GetResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /models/model/{modelId} [get].
func (h *ModelsHandler) GetByModelID(c echo.Context) error {
	modelID := c.Param("modelId")
	if modelID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "modelId is required")
	}
	if decoded, err := url.PathUnescape(modelID); err == nil {
		modelID = decoded
	} else {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid modelId")
	}

	resp, err := h.service.GetByModelID(c.Request().Context(), modelID)
	if err != nil {
		if errors.Is(err, models.ErrModelIDAmbiguous) {
			return echo.NewHTTPError(http.StatusConflict, "model_id is duplicated across providers; use /models/{id} instead")
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, err.Error())
		}
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	}
	return c.JSON(http.StatusOK, h.withReasoningOne(c.Request().Context(), resp))
}

// UpdateByID godoc
// @Summary Update model by internal ID
// @Description Update a model configuration by its internal UUID
// @Tags models
// @Param id path string true "Model internal ID (UUID)"
// @Param payload body models.UpdateRequest true "Updated model configuration"
// @Success 200 {object} models.GetResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /models/{id} [put].
func (h *ModelsHandler) UpdateByID(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}

	var req models.UpdateRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	resp, err := h.service.UpdateByID(c.Request().Context(), id, req)
	if err != nil {
		if errors.Is(err, models.ErrModelIDAlreadyExists) {
			return echo.NewHTTPError(http.StatusConflict, "model_id already exists under the selected provider")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, h.withReasoningOne(c.Request().Context(), resp))
}

// UpdateByModelID godoc
// @Summary Update model by model ID
// @Description Update a model configuration by its model_id field (e.g., gpt-4)
// @Tags models
// @Param modelId path string true "Model ID (e.g., gpt-4)"
// @Param payload body models.UpdateRequest true "Updated model configuration"
// @Success 200 {object} models.GetResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /models/model/{modelId} [put].
func (h *ModelsHandler) UpdateByModelID(c echo.Context) error {
	modelID := c.Param("modelId")
	if modelID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "modelId is required")
	}
	if decoded, err := url.PathUnescape(modelID); err == nil {
		modelID = decoded
	} else {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid modelId")
	}

	var req models.UpdateRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	resp, err := h.service.UpdateByModelID(c.Request().Context(), modelID, req)
	if err != nil {
		if errors.Is(err, models.ErrModelIDAlreadyExists) {
			return echo.NewHTTPError(http.StatusConflict, "model_id already exists under the selected provider")
		}
		if errors.Is(err, models.ErrModelIDAmbiguous) {
			return echo.NewHTTPError(http.StatusConflict, "model_id is duplicated across providers; use /models/{id} instead")
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, err.Error())
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, h.withReasoningOne(c.Request().Context(), resp))
}

// DeleteByID godoc
// @Summary Delete model by internal ID
// @Description Delete a model configuration by its internal UUID
// @Tags models
// @Param id path string true "Model internal ID (UUID)"
// @Success 204 "No Content"
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /models/{id} [delete].
func (h *ModelsHandler) DeleteByID(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}

	if err := h.service.DeleteByID(c.Request().Context(), id); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.NoContent(http.StatusNoContent)
}

// DeleteByModelID godoc
// @Summary Delete model by model ID
// @Description Delete a model configuration by its model_id field (e.g., gpt-4)
// @Tags models
// @Param modelId path string true "Model ID (e.g., gpt-4)"
// @Success 204 "No Content"
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /models/model/{modelId} [delete].
func (h *ModelsHandler) DeleteByModelID(c echo.Context) error {
	modelID := c.Param("modelId")
	if modelID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "modelId is required")
	}
	if decoded, err := url.PathUnescape(modelID); err == nil {
		modelID = decoded
	} else {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid modelId")
	}

	if err := h.service.DeleteByModelID(c.Request().Context(), modelID); err != nil {
		if errors.Is(err, models.ErrModelIDAmbiguous) {
			return echo.NewHTTPError(http.StatusConflict, "model_id is duplicated across providers; use /models/{id} instead")
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, err.Error())
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.NoContent(http.StatusNoContent)
}

// Test godoc
// @Summary Test model connectivity
// @Description Probe a model's provider endpoint using the model's real model_id and client_type to verify configuration
// @Tags models
// @Accept json
// @Produce json
// @Param id path string true "Model internal ID (UUID)"
// @Success 200 {object} models.TestResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /models/{id}/test [post].
func (h *ModelsHandler) Test(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}

	ctx := c.Request().Context()
	if userID, err := auth.UserIDFromContext(c); err == nil {
		ctx = oauthctx.WithUserID(ctx, userID)
	}

	resp, err := h.service.Test(ctx, id)
	if err != nil {
		if strings.Contains(err.Error(), "invalid") {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	}

	return c.JSON(http.StatusOK, resp)
}

// Count godoc
// @Summary Get model count
// @Description Get the total count of models, optionally filtered by type
// @Tags models
// @Param type query string false "Model type (chat, embedding)"
// @Success 200 {object} models.CountResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /models/count [get].
func (h *ModelsHandler) Count(c echo.Context) error {
	modelType := c.QueryParam("type")

	var count int64
	var err error

	if modelType != "" {
		count, err = h.service.CountByType(c.Request().Context(), models.ModelType(modelType))
	} else {
		count, err = h.service.Count(c.Request().Context())
	}

	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, models.CountResponse{Count: count})
}
