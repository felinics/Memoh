package model

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/domains/api/identity/auth"
	modeldomain "github.com/memohai/memoh/domains/model"
	modelcatalog "github.com/memohai/memoh/domains/model/catalog"
	"github.com/memohai/memoh/internal/apperror"
	"github.com/memohai/memoh/internal/oauth"
)

type ModelsHandler struct {
	service *modelcatalog.Service
	logger  *slog.Logger
}

func NewModelsHandler(log *slog.Logger, service *modelcatalog.Service) *ModelsHandler {
	return &ModelsHandler{
		service: service,
		logger:  log.With(slog.String("handler", "models")),
	}
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
// @Param payload body modelcatalog.AddRequest true "Model configuration"
// @Success 201 {object} modelcatalog.AddResponse
// @Failure 400 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /models [post].
func (h *ModelsHandler) Create(c echo.Context) error {
	var req modelcatalog.AddRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Invalid("bind model", err)
	}

	resp, err := h.service.Create(c.Request().Context(), req)
	if err != nil {
		if errors.Is(err, modelcatalog.ErrModelIDAlreadyExists) {
			return apperror.Conflict("create model", err)
		}
		return apperror.Internal("create model", err)
	}
	return c.JSON(http.StatusCreated, resp)
}

// List godoc
// @Summary List all models
// @Description Get a list of all configured models, optionally filtered by type or provider client type
// @Tags models
// @Param type query string false "Model type (chat, embedding)"
// @Param client_type query string false "Provider client type (openai-responses, openai-completions, anthropic-messages, google-generative-ai)"
// @Success 200 {array} modelcatalog.GetResponse
// @Failure 400 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /models [get].
func (h *ModelsHandler) List(c echo.Context) error {
	modelType := c.QueryParam("type")
	clientType := c.QueryParam("client_type")

	var resp []modelcatalog.GetResponse
	var err error

	switch {
	case modelType != "":
		resp, err = h.service.ListEnabledByType(c.Request().Context(), modeldomain.ModelType(modelType))
	case clientType != "":
		ct := modeldomain.ClientType(clientType)
		if !modeldomain.IsLLMClientType(ct) {
			return apperror.Field("client_type", apperror.FieldInvalid)
		}
		resp, err = h.service.ListEnabledByProviderClientType(c.Request().Context(), ct)
	default:
		resp, err = h.service.ListEnabled(c.Request().Context())
	}

	if err != nil {
		return apperror.Internal("list models", err)
	}
	return c.JSON(http.StatusOK, resp)
}

// GetByID godoc
// @Summary Get model by internal ID
// @Description Get a model configuration by its internal UUID
// @Tags models
// @Param id path string true "Model internal ID (UUID)"
// @Success 200 {object} modelcatalog.GetResponse
// @Failure 400 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /models/{id} [get].
func (h *ModelsHandler) GetByID(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return apperror.Required("id")
	}

	resp, err := h.service.GetByID(c.Request().Context(), id)
	if err != nil {
		return apperror.NotFound("get model", err)
	}
	return c.JSON(http.StatusOK, resp)
}

// GetByModelID godoc
// @Summary Get model by model ID
// @Description Get a model configuration by its model_id field (e.g., gpt-4)
// @Tags models
// @Param modelId path string true "Model ID (e.g., gpt-4)"
// @Success 200 {object} modelcatalog.GetResponse
// @Failure 400 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /models/model/{modelId} [get].
func (h *ModelsHandler) GetByModelID(c echo.Context) error {
	modelID := c.Param("modelId")
	if modelID == "" {
		return apperror.Required("model_id")
	}
	if decoded, err := url.PathUnescape(modelID); err == nil {
		modelID = decoded
	} else {
		return apperror.Field("model_id", apperror.FieldInvalid)
	}

	resp, err := h.service.GetByModelID(c.Request().Context(), modelID)
	if err != nil {
		if errors.Is(err, modelcatalog.ErrModelIDAmbiguous) {
			return apperror.Conflict("get model by model id", err)
		}
		if errors.Is(err, modelcatalog.ErrModelNotFound) {
			return apperror.NotFound("get model by model id", err)
		}
		return apperror.NotFound("get model by model id", err)
	}
	return c.JSON(http.StatusOK, resp)
}

// UpdateByID godoc
// @Summary Update model by internal ID
// @Description Update a model configuration by its internal UUID
// @Tags models
// @Param id path string true "Model internal ID (UUID)"
// @Param payload body modelcatalog.UpdateRequest true "Updated model configuration"
// @Success 200 {object} modelcatalog.GetResponse
// @Failure 400 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /models/{id} [put].
func (h *ModelsHandler) UpdateByID(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return apperror.Required("id")
	}

	var req modelcatalog.UpdateRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Invalid("bind model", err)
	}

	resp, err := h.service.UpdateByID(c.Request().Context(), id, req)
	if err != nil {
		if errors.Is(err, modelcatalog.ErrModelIDAlreadyExists) {
			return apperror.Conflict("update model", err)
		}
		return apperror.Internal("update model", err)
	}
	return c.JSON(http.StatusOK, resp)
}

// UpdateByModelID godoc
// @Summary Update model by model ID
// @Description Update a model configuration by its model_id field (e.g., gpt-4)
// @Tags models
// @Param modelId path string true "Model ID (e.g., gpt-4)"
// @Param payload body modelcatalog.UpdateRequest true "Updated model configuration"
// @Success 200 {object} modelcatalog.GetResponse
// @Failure 400 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /models/model/{modelId} [put].
func (h *ModelsHandler) UpdateByModelID(c echo.Context) error {
	modelID := c.Param("modelId")
	if modelID == "" {
		return apperror.Required("model_id")
	}
	if decoded, err := url.PathUnescape(modelID); err == nil {
		modelID = decoded
	} else {
		return apperror.Field("model_id", apperror.FieldInvalid)
	}

	var req modelcatalog.UpdateRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Invalid("bind model", err)
	}

	resp, err := h.service.UpdateByModelID(c.Request().Context(), modelID, req)
	if err != nil {
		if errors.Is(err, modelcatalog.ErrModelIDAlreadyExists) {
			return apperror.Conflict("update model by model id", err)
		}
		if errors.Is(err, modelcatalog.ErrModelIDAmbiguous) {
			return apperror.Conflict("update model by model id", err)
		}
		if errors.Is(err, modelcatalog.ErrModelNotFound) {
			return apperror.NotFound("update model by model id", err)
		}
		return apperror.Internal("update model by model id", err)
	}
	return c.JSON(http.StatusOK, resp)
}

// DeleteByID godoc
// @Summary Delete model by internal ID
// @Description Delete a model configuration by its internal UUID
// @Tags models
// @Param id path string true "Model internal ID (UUID)"
// @Success 204 "No Content"
// @Failure 400 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /models/{id} [delete].
func (h *ModelsHandler) DeleteByID(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return apperror.Required("id")
	}

	if err := h.service.DeleteByID(c.Request().Context(), id); err != nil {
		return apperror.Internal("delete model", err)
	}
	return c.NoContent(http.StatusNoContent)
}

// DeleteByModelID godoc
// @Summary Delete model by model ID
// @Description Delete a model configuration by its model_id field (e.g., gpt-4)
// @Tags models
// @Param modelId path string true "Model ID (e.g., gpt-4)"
// @Success 204 "No Content"
// @Failure 400 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /models/model/{modelId} [delete].
func (h *ModelsHandler) DeleteByModelID(c echo.Context) error {
	modelID := c.Param("modelId")
	if modelID == "" {
		return apperror.Required("model_id")
	}
	if decoded, err := url.PathUnescape(modelID); err == nil {
		modelID = decoded
	} else {
		return apperror.Field("model_id", apperror.FieldInvalid)
	}

	if err := h.service.DeleteByModelID(c.Request().Context(), modelID); err != nil {
		if errors.Is(err, modelcatalog.ErrModelIDAmbiguous) {
			return apperror.Conflict("delete model by model id", err)
		}
		if errors.Is(err, modelcatalog.ErrModelNotFound) {
			return apperror.NotFound("delete model by model id", err)
		}
		return apperror.Internal("delete model by model id", err)
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
// @Success 200 {object} modelcatalog.TestResponse
// @Failure 400 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /models/{id}/test [post].
func (h *ModelsHandler) Test(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return apperror.Required("id")
	}

	ctx := c.Request().Context()
	if userID, err := auth.UserIDFromContext(c); err == nil {
		ctx = oauth.WithUserID(ctx, userID)
	}

	resp, err := h.service.Test(ctx, id)
	if err != nil {
		if strings.Contains(err.Error(), "invalid") {
			return apperror.Invalid("test model", err)
		}
		return apperror.NotFound("test model", err)
	}

	return c.JSON(http.StatusOK, resp)
}

// Count godoc
// @Summary Get model count
// @Description Get the total count of models, optionally filtered by type
// @Tags models
// @Param type query string false "Model type (chat, embedding)"
// @Success 200 {object} modelcatalog.CountResponse
// @Failure 400 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /models/count [get].
func (h *ModelsHandler) Count(c echo.Context) error {
	modelType := c.QueryParam("type")

	var count int64
	var err error

	if modelType != "" {
		count, err = h.service.CountByType(c.Request().Context(), modeldomain.ModelType(modelType))
	} else {
		count, err = h.service.Count(c.Request().Context())
	}

	if err != nil {
		return apperror.Internal("count models", err)
	}
	return c.JSON(http.StatusOK, modelcatalog.CountResponse{Count: count})
}
