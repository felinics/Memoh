package bot

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/domains/api/bot/setting"
	audiopkg "github.com/memohai/memoh/domains/model/audio"
	"github.com/memohai/memoh/internal/apperror"
)

// BotAudioHandler handles per-bot speech synthesis requests from the agent tool.
type BotAudioHandler struct {
	audioService    *audiopkg.Service
	settingsService *setting.Service
	tempStore       *audiopkg.TempStore
	logger          *slog.Logger
}

func NewBotAudioHandler(log *slog.Logger, audioService *audiopkg.Service, settingsService *setting.Service, tempStore *audiopkg.TempStore) *BotAudioHandler {
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
// @Failure 400 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/tts/synthesize [post].
func (h *BotAudioHandler) Synthesize(c echo.Context) error {
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return apperror.Required("bot_id")
	}

	var req synthesizeRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Invalid("bind tts payload", err)
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return apperror.Required("text")
	}
	const maxTextLen = 500
	if len([]rune(text)) > maxTextLen {
		return apperror.Field("text", apperror.FieldTooLong)
	}

	botSettings, err := h.settingsService.GetBot(c.Request().Context(), botID)
	if err != nil {
		h.logger.Error("failed to load bot settings", slog.String("bot_id", botID), slog.Any("error", err))
		return apperror.Internal("load bot settings", err)
	}
	if botSettings.TtsModelID == "" {
		return apperror.Invalid("synthesize speech", nil)
	}

	tempID, f, err := h.tempStore.Create()
	if err != nil {
		h.logger.Error("failed to create temp file", slog.Any("error", err))
		return apperror.Internal("create temp file", err)
	}

	contentType, streamErr := h.audioService.StreamToFile(c.Request().Context(), botSettings.TtsModelID, text, f)
	closeErr := f.Close()
	if streamErr != nil {
		h.logger.Error("speech synthesis failed", slog.String("bot_id", botID), slog.String("model_id", botSettings.TtsModelID), slog.Any("error", streamErr))
		h.tempStore.Delete(tempID)
		return apperror.Internal("stream speech", streamErr)
	}
	if closeErr != nil {
		h.logger.Error("failed to finalize audio file", slog.String("bot_id", botID), slog.Any("error", closeErr))
		h.tempStore.Delete(tempID)
		return apperror.Internal("finalize audio file", closeErr)
	}

	size, _ := h.tempStore.FileSize(tempID)

	return c.JSON(http.StatusOK, synthesizeResponse{
		TempID:      tempID,
		ContentType: contentType,
		Size:        size,
	})
}
