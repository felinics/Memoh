package model

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	modeldomain "github.com/memohai/memoh/domains/model"
	audiopkg "github.com/memohai/memoh/domains/model/audio"
	modelcatalog "github.com/memohai/memoh/domains/model/catalog"
	"github.com/memohai/memoh/internal/apperror"
)

type AudioHandler struct {
	service       *audiopkg.Service
	modelsService *modelcatalog.Service
	logger        *slog.Logger
}

func NewAudioHandler(log *slog.Logger, service *audiopkg.Service, modelsService *modelcatalog.Service) *AudioHandler {
	return &AudioHandler{
		service:       service,
		modelsService: modelsService,
		logger:        log.With(slog.String("handler", "audio")),
	}
}

func (h *AudioHandler) Register(e *echo.Echo) {
	pg := e.Group("/speech-providers")
	pg.GET("", h.ListProviders)
	pg.GET("/:id", h.GetProvider)
	pg.GET("/meta", h.ListSpeechMeta)
	pg.GET("/:id/models", h.ListModelsByProvider)
	pg.POST("/:id/import-models", h.ImportModels)

	tpg := e.Group("/transcription-providers")
	tpg.GET("", h.ListTranscriptionProviders)
	tpg.GET("/meta", h.ListTranscriptionMeta)
	tpg.GET("/:id", h.GetProvider)
	tpg.GET("/:id/models", h.ListTranscriptionModelsByProvider)
	tpg.POST("/:id/import-models", h.ImportTranscriptionModels)

	mg := e.Group("/speech-models")
	mg.GET("", h.ListModels)
	mg.GET("/:id", h.GetModel)
	mg.PUT("/:id", h.UpdateModel)
	mg.GET("/:id/capabilities", h.GetModelCapabilities)
	mg.POST("/:id/test", h.TestModel)

	tg := e.Group("/transcription-models")
	tg.GET("", h.ListTranscriptionModels)
	tg.GET("/:id", h.GetTranscriptionModel)
	tg.PUT("/:id", h.UpdateTranscriptionModel)
	tg.GET("/:id/capabilities", h.GetTranscriptionModelCapabilities)
	tg.POST("/:id/test", h.TestTranscriptionModel)
}

// ListMeta godoc
// @Summary List speech provider metadata
// @Description List available speech provider types with their models and capabilities
// @Tags speech-providers
// @Success 200 {array} audiopkg.ProviderMetaResponse
// @Router /speech-providers/meta [get].
func (h *AudioHandler) ListSpeechMeta(c echo.Context) error {
	return c.JSON(http.StatusOK, h.service.ListSpeechMeta(c.Request().Context()))
}

// ListTranscriptionMeta godoc
// @Summary List transcription provider metadata
// @Description List available transcription provider types with their models and capabilities
// @Tags transcription-providers
// @Success 200 {array} audiopkg.ProviderMetaResponse
// @Router /transcription-providers/meta [get].
func (h *AudioHandler) ListTranscriptionMeta(c echo.Context) error {
	return c.JSON(http.StatusOK, h.service.ListTranscriptionMeta(c.Request().Context()))
}

// ListProviders godoc
// @Summary List speech providers
// @Description List providers that support speech (filtered view of unified providers table)
// @Tags speech-providers
// @Produce json
// @Success 200 {array} audiopkg.SpeechProviderResponse
// @Failure 500 {object} apperror.Problem
// @Router /speech-providers [get].
func (h *AudioHandler) ListProviders(c echo.Context) error {
	items, err := h.service.ListSpeechProviders(c.Request().Context())
	if err != nil {
		return apperror.Internal("list speech providers", err)
	}
	return c.JSON(http.StatusOK, items)
}

// ListTranscriptionProviders godoc
// @Summary List transcription providers
// @Description List providers that support transcription (filtered view of unified providers table)
// @Tags transcription-providers
// @Produce json
// @Success 200 {array} audiopkg.SpeechProviderResponse
// @Failure 500 {object} apperror.Problem
// @Router /transcription-providers [get].
func (h *AudioHandler) ListTranscriptionProviders(c echo.Context) error {
	items, err := h.service.ListTranscriptionProviders(c.Request().Context())
	if err != nil {
		return apperror.Internal("list transcription providers", err)
	}
	return c.JSON(http.StatusOK, items)
}

// GetProvider godoc
// @Summary Get speech provider
// @Description Get a speech provider with masked config values
// @Tags speech-providers
// @Produce json
// @Param id path string true "Provider ID (UUID)"
// @Success 200 {object} audiopkg.SpeechProviderResponse
// @Failure 400 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Router /speech-providers/{id} [get].
// @Router /transcription-providers/{id} [get].
func (h *AudioHandler) GetProvider(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return apperror.Required("id")
	}
	item, err := h.service.GetSpeechProvider(c.Request().Context(), id)
	if err != nil {
		return apperror.NotFound("get speech provider", err)
	}
	return c.JSON(http.StatusOK, item)
}

// ListModelsByProvider godoc
// @Summary List speech models by provider
// @Description List models of type 'speech' for a specific speech provider
// @Tags speech-providers
// @Produce json
// @Param id path string true "Provider ID (UUID)"
// @Success 200 {array} audiopkg.SpeechModelResponse
// @Failure 400 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /speech-providers/{id}/models [get].
func (h *AudioHandler) ListModelsByProvider(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return apperror.Required("id")
	}
	items, err := h.service.ListSpeechModelsByProvider(c.Request().Context(), id)
	if err != nil {
		return apperror.Internal("list speech models by provider", err)
	}
	return c.JSON(http.StatusOK, items)
}

// ImportModels godoc
// @Summary Import speech models from provider
// @Description Fetch models using the configured speech provider and import them into the unified models table
// @Tags speech-providers
// @Accept json
// @Produce json
// @Param id path string true "Provider ID (UUID)"
// @Success 200 {object} audiopkg.ImportModelsResponse
// @Failure 400 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /speech-providers/{id}/import-models [post].
func (h *AudioHandler) ImportModels(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return apperror.Required("id")
	}

	remoteModels, err := h.service.FetchRemoteModels(c.Request().Context(), id)
	if err != nil {
		return apperror.Internal("fetch remote speech models", err)
	}

	resp := audiopkg.ImportModelsResponse{
		Models: make([]string, 0, len(remoteModels)),
	}

	for _, model := range remoteModels {
		name := strings.TrimSpace(model.Name)
		if name == "" {
			name = model.ID
		}

		// Speech imports stay enabled by default: the audio response DTO and
		// provider-detail page don't yet surface a per-model toggle, so an
		// "imported but disabled" model would have no UI path back to on.
		_, err := h.modelsService.Create(c.Request().Context(), modelcatalog.AddRequest{
			ModelID:    model.ID,
			Name:       name,
			ProviderID: id,
			Type:       modeldomain.ModelTypeSpeech,
			Config:     modeldomain.ModelConfig{},
		})
		if err != nil {
			if errors.Is(err, modelcatalog.ErrModelIDAlreadyExists) {
				resp.Skipped++
				continue
			}
			h.logger.Warn("failed to import speech model", slog.String("model_id", model.ID), slog.Any("error", err))
			continue
		}
		resp.Created++
		resp.Models = append(resp.Models, model.ID)
	}

	return c.JSON(http.StatusOK, resp)
}

// ListTranscriptionModelsByProvider godoc
// @Summary List transcription models by provider
// @Description List models of type 'transcription' for a specific transcription provider
// @Tags transcription-providers
// @Produce json
// @Param id path string true "Provider ID (UUID)"
// @Success 200 {array} audiopkg.TranscriptionModelResponse
// @Failure 400 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /transcription-providers/{id}/models [get].
func (h *AudioHandler) ListTranscriptionModelsByProvider(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return apperror.Required("id")
	}
	items, err := h.service.ListTranscriptionModelsByProvider(c.Request().Context(), id)
	if err != nil {
		return apperror.Internal("list transcription models by provider", err)
	}
	return c.JSON(http.StatusOK, items)
}

// ImportTranscriptionModels godoc
// @Summary Import transcription models from provider
// @Description Fetch models using the configured transcription provider and import them into the unified models table
// @Tags transcription-providers
// @Accept json
// @Produce json
// @Param id path string true "Provider ID (UUID)"
// @Success 200 {object} audiopkg.ImportModelsResponse
// @Failure 400 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /transcription-providers/{id}/import-models [post].
func (h *AudioHandler) ImportTranscriptionModels(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return apperror.Required("id")
	}

	remoteModels, err := h.service.FetchRemoteTranscriptionModels(c.Request().Context(), id)
	if err != nil {
		return apperror.Internal("fetch remote transcription models", err)
	}

	resp := audiopkg.ImportModelsResponse{
		Models: make([]string, 0, len(remoteModels)),
	}

	for _, model := range remoteModels {
		name := strings.TrimSpace(model.Name)
		if name == "" {
			name = model.ID
		}

		// Transcription imports stay enabled by default: see ImportSpeechModels
		// for the rationale.
		_, err := h.modelsService.Create(c.Request().Context(), modelcatalog.AddRequest{
			ModelID:    model.ID,
			Name:       name,
			ProviderID: id,
			Type:       modeldomain.ModelTypeTranscription,
			Config:     modeldomain.ModelConfig{},
		})
		if err != nil {
			if errors.Is(err, modelcatalog.ErrModelIDAlreadyExists) {
				resp.Skipped++
				continue
			}
			h.logger.Warn("failed to import transcription model", slog.String("model_id", model.ID), slog.Any("error", err))
			continue
		}
		resp.Created++
		resp.Models = append(resp.Models, model.ID)
	}

	return c.JSON(http.StatusOK, resp)
}

// ListModels godoc
// @Summary List all speech models
// @Description List all models of type 'speech' (filtered view of unified models table)
// @Tags speech-models
// @Produce json
// @Success 200 {array} audiopkg.SpeechModelResponse
// @Failure 500 {object} apperror.Problem
// @Router /speech-models [get].
func (h *AudioHandler) ListModels(c echo.Context) error {
	items, err := h.service.ListSpeechModels(c.Request().Context())
	if err != nil {
		return apperror.Internal("list speech models", err)
	}
	return c.JSON(http.StatusOK, items)
}

// ListTranscriptionModels godoc
// @Summary List all transcription models
// @Description List all models of type 'transcription' (filtered view of unified models table)
// @Tags transcription-models
// @Produce json
// @Success 200 {array} audiopkg.TranscriptionModelResponse
// @Failure 500 {object} apperror.Problem
// @Router /transcription-models [get].
func (h *AudioHandler) ListTranscriptionModels(c echo.Context) error {
	items, err := h.service.ListTranscriptionModels(c.Request().Context())
	if err != nil {
		return apperror.Internal("list transcription models", err)
	}
	return c.JSON(http.StatusOK, items)
}

// GetModel godoc
// @Summary Get a speech model
// @Tags speech-models
// @Produce json
// @Param id path string true "Model ID"
// @Success 200 {object} audiopkg.SpeechModelResponse
// @Failure 404 {object} apperror.Problem
// @Router /speech-models/{id} [get].
func (h *AudioHandler) GetModel(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return apperror.Required("id")
	}
	resp, err := h.service.GetSpeechModel(c.Request().Context(), id)
	if err != nil {
		return apperror.NotFound("get speech model", err)
	}
	return c.JSON(http.StatusOK, resp)
}

// UpdateModel godoc
// @Summary Update a speech model
// @Tags speech-models
// @Accept json
// @Produce json
// @Param id path string true "Model ID"
// @Param request body audiopkg.UpdateSpeechModelRequest true "Model update payload"
// @Success 200 {object} audiopkg.SpeechModelResponse
// @Failure 400 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /speech-models/{id} [put].
func (h *AudioHandler) UpdateModel(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return apperror.Required("id")
	}
	var req audiopkg.UpdateSpeechModelRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Invalid("bind speech model", err)
	}
	resp, err := h.service.UpdateSpeechModel(c.Request().Context(), id, req)
	if err != nil {
		return apperror.Internal("update speech model", err)
	}
	return c.JSON(http.StatusOK, resp)
}

// GetTranscriptionModel godoc
// @Summary Get a transcription model
// @Tags transcription-models
// @Produce json
// @Param id path string true "Model ID"
// @Success 200 {object} audiopkg.TranscriptionModelResponse
// @Failure 404 {object} apperror.Problem
// @Router /transcription-models/{id} [get].
func (h *AudioHandler) GetTranscriptionModel(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return apperror.Required("id")
	}
	resp, err := h.service.GetTranscriptionModel(c.Request().Context(), id)
	if err != nil {
		return apperror.NotFound("get transcription model", err)
	}
	return c.JSON(http.StatusOK, resp)
}

// UpdateTranscriptionModel godoc
// @Summary Update a transcription model
// @Tags transcription-models
// @Accept json
// @Produce json
// @Param id path string true "Model ID"
// @Param request body audiopkg.UpdateSpeechModelRequest true "Model update payload"
// @Success 200 {object} audiopkg.TranscriptionModelResponse
// @Failure 400 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /transcription-models/{id} [put].
func (h *AudioHandler) UpdateTranscriptionModel(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return apperror.Required("id")
	}
	var req audiopkg.UpdateSpeechModelRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Invalid("bind transcription model", err)
	}
	resp, err := h.service.UpdateTranscriptionModel(c.Request().Context(), id, req)
	if err != nil {
		return apperror.Internal("update transcription model", err)
	}
	return c.JSON(http.StatusOK, resp)
}

// GetModelCapabilities godoc
// @Summary Get speech model capabilities
// @Tags speech-models
// @Produce json
// @Param id path string true "Model ID"
// @Success 200 {object} audiopkg.ModelCapabilities
// @Failure 404 {object} apperror.Problem
// @Router /speech-models/{id}/capabilities [get].
func (h *AudioHandler) GetModelCapabilities(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return apperror.Required("id")
	}
	caps, err := h.service.GetModelCapabilities(c.Request().Context(), id)
	if err != nil {
		return apperror.NotFound("get speech model capabilities", err)
	}
	return c.JSON(http.StatusOK, caps)
}

// GetTranscriptionModelCapabilities godoc
// @Summary Get transcription model capabilities
// @Tags transcription-models
// @Produce json
// @Param id path string true "Model ID"
// @Success 200 {object} audiopkg.ModelCapabilities
// @Failure 404 {object} apperror.Problem
// @Router /transcription-models/{id}/capabilities [get].
func (h *AudioHandler) GetTranscriptionModelCapabilities(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return apperror.Required("id")
	}
	caps, err := h.service.GetTranscriptionModelCapabilities(c.Request().Context(), id)
	if err != nil {
		return apperror.NotFound("get transcription model capabilities", err)
	}
	return c.JSON(http.StatusOK, caps)
}

// TestModel godoc
// @Summary Test speech model synthesis
// @Description Synthesize text using a specific model's config and return audio
// @Tags speech-models
// @Accept json
// @Produce application/octet-stream
// @Param id path string true "Model ID"
// @Param request body audiopkg.TestSynthesizeRequest true "Text to synthesize"
// @Success 200 {file} binary "Audio data"
// @Failure 400 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /speech-models/{id}/test [post].
func (h *AudioHandler) TestModel(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return apperror.Required("id")
	}
	var req audiopkg.TestSynthesizeRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Invalid("bind speech test", err)
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return apperror.Required("text")
	}
	const maxTestTextLen = 500
	if len([]rune(text)) > maxTestTextLen {
		return apperror.Field("text", apperror.FieldTooLong)
	}
	audio, contentType, err := h.service.Synthesize(c.Request().Context(), id, text, req.Config)
	if err != nil {
		return apperror.Internal("test speech model", err)
	}
	return c.Blob(http.StatusOK, contentType, audio)
}

// TestTranscriptionModel godoc
// @Summary Test transcription model recognition
// @Description Transcribe uploaded audio using a specific model's config and return structured text output
// @Tags transcription-models
// @Accept mpfd
// @Produce json
// @Param id path string true "Model ID"
// @Param file formData file true "Audio file"
// @Param config formData string false "Optional JSON config"
// @Success 200 {object} audiopkg.TestTranscriptionResponse
// @Failure 400 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /transcription-models/{id}/test [post].
func (h *AudioHandler) TestTranscriptionModel(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return apperror.Required("id")
	}
	file, err := c.FormFile("file")
	if err != nil {
		return apperror.Required("file")
	}
	src, err := file.Open()
	if err != nil {
		return apperror.Invalid("open transcription upload", err)
	}
	defer func(src multipart.File) {
		err := src.Close()
		if err != nil {
			h.logger.Warn("failed to close uploaded file", slog.Any("error", err))
		}
	}(src)
	audio, err := io.ReadAll(src)
	if err != nil {
		return apperror.Invalid("read transcription upload", err)
	}
	var cfg map[string]any
	if raw := strings.TrimSpace(c.FormValue("config")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return apperror.Field("config", apperror.FieldInvalid)
		}
	}
	result, err := h.service.Transcribe(c.Request().Context(), id, audio, file.Filename, file.Header.Get("Content-Type"), cfg)
	if err != nil {
		return apperror.Internal("test transcription model", err)
	}
	resp := audiopkg.TestTranscriptionResponse{
		Text:            result.Text,
		Language:        result.Language,
		DurationSeconds: result.DurationSeconds,
		Metadata:        result.ProviderMetadata,
	}
	if len(result.Words) > 0 {
		resp.Words = make([]audiopkg.TranscriptionWord, 0, len(result.Words))
		for _, word := range result.Words {
			resp.Words = append(resp.Words, audiopkg.TranscriptionWord{
				Text:      word.Text,
				Start:     word.Start,
				End:       word.End,
				SpeakerID: word.SpeakerID,
			})
		}
	}
	return c.JSON(http.StatusOK, resp)
}
