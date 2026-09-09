package handlers

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	audiopkg "github.com/felinics/memoh/internal/audio"
	"github.com/felinics/memoh/internal/settings"
)

// BotAudioHandler handles per-bot speech synthesis requests from the agent tool.
type BotAudioHandler struct {
	audioService    *audiopkg.Service
	settingsService *settings.Service
	tempStore       *audiopkg.TempStore
	logger          *slog.Logger
}

func NewBotAudioHandler(log *slog.Logger, audioService *audiopkg.Service, settingsService *settings.Service, tempStore *audiopkg.TempStore) *BotAudioHandler {
	return &BotAudioHandler{
		audioService:    audioService,
		settingsService: settingsService,
		tempStore:       tempStore,
		logger:          log.With(slog.String("handler", "bot_audio")),
	}
}

func (h *BotAudioHandler) Register(e *echo.Echo) {
	e.POST("/bots/:bot_id/tts/synthesize", h.Synthesize)
}

type synthesizeRequest struct {
	Text string `json:"text"`
}

type synthesizeResponse struct {
	TempID      string `json:"temp_id"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

// Synthesize godoc
// @Summary Synthesize speech for a bot
// @Description Stream-synthesize text using the bot's configured TTS model, write to temp file
// @Tags bots
// @Accept json
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param request body synthesizeRequest true "Text to synthesize"
// @Success 200 {object} synthesizeResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/tts/synthesize [post].
func (h *BotAudioHandler) Synthesize(c echo.Context) error {
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot_id is required")
	}

	var req synthesizeRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "text is required")
	}
	const maxTextLen = 500
	if len([]rune(text)) > maxTextLen {
		return echo.NewHTTPError(http.StatusBadRequest, "text too long, max 500 characters")
	}

	botSettings, err := h.settingsService.GetBot(c.Request().Context(), botID)
	if err != nil {
		h.logger.Error("failed to load bot settings", slog.String("bot_id", botID), slog.Any("error", err))
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to load bot settings")
	}
	if botSettings.TtsModelID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot has no TTS model configured")
	}

	tempID, f, err := h.tempStore.Create()
	if err != nil {
		h.logger.Error("failed to create temp file", slog.Any("error", err))
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create temp file")
	}

	contentType, streamErr := h.audioService.StreamToFile(c.Request().Context(), botSettings.TtsModelID, text, f)
	closeErr := f.Close()
	if streamErr != nil {
		h.logger.Error("speech synthesis failed", slog.String("bot_id", botID), slog.String("model_id", botSettings.TtsModelID), slog.Any("error", streamErr))
		h.tempStore.Delete(tempID)
		return echo.NewHTTPError(http.StatusInternalServerError, streamErr.Error())
	}
	if closeErr != nil {
		h.logger.Error("failed to finalize audio file", slog.String("bot_id", botID), slog.Any("error", closeErr))
		h.tempStore.Delete(tempID)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to finalize audio file")
	}

	size, _ := h.tempStore.FileSize(tempID)

	return c.JSON(http.StatusOK, synthesizeResponse{
		TempID:      tempID,
		ContentType: contentType,
		Size:        size,
	})
}
