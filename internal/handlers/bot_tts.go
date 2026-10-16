package handlers

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	audiopkg "github.com/felinics/memoh/internal/audio"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/httpx"
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
// @Failure 400 {object} server.Problem
// @Failure 500 {object} server.Problem
// @Router /bots/{bot_id}/tts/synthesize [post].
func (h *BotAudioHandler) Synthesize(c echo.Context) error {
	botID, err := httpx.RequiredParam(c, "bot_id")
	if err != nil {
		return err
	}

	var req synthesizeRequest
	if err := c.Bind(&req); err != nil {
		return err
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return apperror.FieldRequired("text")
	}
	const maxTextLen = 500
	if len([]rune(text)) > maxTextLen {
		return apperror.New(apperror.CodeTTSTextTooLong, map[string]string{"max": strconv.Itoa(maxTextLen)})
	}

	botSettings, err := h.settingsService.GetBot(c.Request().Context(), botID)
	if err != nil {
		return errs.Wrap(err, "load bot settings", slog.String("bot_id", botID))
	}
	if botSettings.TtsModelID == "" {
		return apperror.New(apperror.CodeTTSModelNotConfigured, nil)
	}

	tempID, f, err := h.tempStore.Create()
	if err != nil {
		return errs.Wrap(err, "create temp file")
	}

	contentType, streamErr := h.audioService.StreamToFile(c.Request().Context(), botSettings.TtsModelID, text, f)
	closeErr := f.Close()
	if streamErr != nil {
		h.tempStore.Delete(tempID)
		return errs.Wrap(streamErr, "synthesize speech", slog.String("bot_id", botID), slog.String("model_id", botSettings.TtsModelID))
	}
	if closeErr != nil {
		h.tempStore.Delete(tempID)
		return errs.Wrap(closeErr, "finalize audio file", slog.String("bot_id", botID))
	}

	size, _ := h.tempStore.FileSize(tempID)

	return c.JSON(http.StatusOK, synthesizeResponse{
		TempID:      tempID,
		ContentType: contentType,
		Size:        size,
	})
}
