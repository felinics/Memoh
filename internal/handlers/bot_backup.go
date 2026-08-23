package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/internal/accounts"
	"github.com/memohai/memoh/internal/apperror"
	"github.com/memohai/memoh/internal/auth"
	"github.com/memohai/memoh/internal/botbackup"
	"github.com/memohai/memoh/internal/botbackup/secure"
	"github.com/memohai/memoh/internal/bots"
	"github.com/memohai/memoh/internal/runtimefence"
)

type BotBackupHandler struct {
	service        *botbackup.Service
	botService     *bots.Service
	accountService *accounts.Service
}

func NewBotBackupHandler(service *botbackup.Service, botService *bots.Service, accountService *accounts.Service) *BotBackupHandler {
	return &BotBackupHandler{service: service, botService: botService, accountService: accountService}
}

func (h *BotBackupHandler) Register(e *echo.Echo) {
	e.POST("/bots/:bot_id/backup/export", h.Export)
	e.GET("/bots/:bot_id/backup/summary", h.Summary)
	e.POST("/bots/backup/import/preview", h.PreviewImport)
	e.POST("/bots/backup/import", h.Import)
}

// Summary godoc
// @Summary Summarize what a bot would export
// @Tags bots
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} botbackup.SummaryResult
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/backup/summary [get].
func (h *BotBackupHandler) Summary(c echo.Context) error {
	if h.service == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "bot backup service not configured")
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	userID, err := auth.UserIDFromContext(c)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, err.Error())
	}
	if _, err := AuthorizeBotAccess(c.Request().Context(), h.botService, h.accountService, userID, botID); err != nil {
		return err
	}
	res, err := h.service.Summary(c.Request().Context(), botID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return c.JSON(http.StatusOK, res)
}

// Export godoc
// @Summary Export a full bot backup
// @Tags bots
// @Accept json
// @Produce application/zip
// @Param bot_id path string true "Bot ID"
// @Param payload body botbackup.ExportRequest true "Export options"
// @Success 200 {file} file
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/backup/export [post].
func (h *BotBackupHandler) Export(c echo.Context) error {
	if h.service == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "bot backup service not configured")
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	userID, err := auth.UserIDFromContext(c)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, err.Error())
	}
	bot, err := AuthorizeBotAccess(c.Request().Context(), h.botService, h.accountService, userID, botID)
	if err != nil {
		return err
	}
	var req botbackup.ExportRequest
	if c.Request().Body != nil {
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
	}

	// Build the bundle into a temp file first. The whole export can fail (e.g.
	// the workspace stream errors) and that must surface as a proper HTTP error
	// rather than a truncated body after a misleading "200 OK".
	tmp, err := os.CreateTemp("", "memoh-backup-*.zip")
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to allocate temp file")
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()

	if err := h.service.Export(c.Request().Context(), botID, botbackup.ExportOptions{Sections: req.Sections}, tmp); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "export failed: "+err.Error())
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	filename := fmt.Sprintf("bot-%s-backup-%s.memoh.zip", safeFilename(bot.DisplayName, bot.ID), time.Now().UTC().Format("20060102T150405Z"))
	c.Response().Header().Set(echo.HeaderContentType, "application/zip")
	c.Response().Header().Set(echo.HeaderContentDisposition, `attachment; filename="`+filename+`"`)

	// No passphrase: stream the plaintext bundle with a known length.
	if req.Passphrase == "" {
		if info, statErr := tmp.Stat(); statErr == nil {
			c.Response().Header().Set(echo.HeaderContentLength, strconv.FormatInt(info.Size(), 10))
		}
		c.Response().WriteHeader(http.StatusOK)
		_, err = io.Copy(c.Response(), tmp)
		return err
	}
	// Passphrase set: wrap the bundle in an encrypted, length-unknown stream.
	c.Response().WriteHeader(http.StatusOK)
	return secure.Encrypt(c.Response(), tmp, req.Passphrase)
}

// PreviewImport godoc
// @Summary Preview a bot backup import
// @Tags bots
// @Accept multipart/form-data
// @Produce json
// @Param file formData file true "Bot backup zip"
// @Param mode formData string false "Import mode"
// @Param target_bot_id formData string false "Target bot ID for overwrite mode"
// @Param sections formData string false "JSON object mapping section to strategy (skip|merge|replace), e.g. {\"settings\":\"replace\"}; omit to import all"
// @Param passphrase formData string false "Passphrase to decrypt an encrypted backup"
// @Success 200 {object} botbackup.PreviewResult
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/backup/import/preview [post].
func (h *BotBackupHandler) PreviewImport(c echo.Context) error {
	if h.service == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "bot backup service not configured")
	}
	if _, err := auth.UserIDFromContext(c); err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, err.Error())
	}
	raw, err := readUploadedBackup(c)
	if err != nil {
		return err
	}
	preview, err := h.service.Preview(c.Request().Context(), raw, importOptionsFromForm(c), c.FormValue("passphrase"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return c.JSON(http.StatusOK, preview)
}

// Import godoc
// @Summary Import a bot backup
// @Tags bots
// @Accept multipart/form-data
// @Produce json
// @Param file formData file true "Bot backup zip"
// @Param mode formData string false "Import mode"
// @Param target_bot_id formData string false "Target bot ID for overwrite mode"
// @Param sections formData string false "JSON object mapping section to strategy (skip|merge|replace), e.g. {\"settings\":\"replace\"}; omit to import all"
// @Param passphrase formData string false "Passphrase to decrypt an encrypted backup"
// @Success 200 {object} botbackup.ImportResult
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/backup/import [post].
func (h *BotBackupHandler) Import(c echo.Context) error {
	if h.service == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "bot backup service not configured")
	}
	userID, err := auth.UserIDFromContext(c)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, err.Error())
	}
	opts := importOptionsFromForm(c)
	if opts.Mode == botbackup.ImportModeOverwrite {
		if _, err := AuthorizeBotAccess(c.Request().Context(), h.botService, h.accountService, userID, opts.TargetBotID); err != nil {
			return err
		}
	}
	raw, err := readUploadedBackup(c)
	if err != nil {
		return err
	}
	result, err := h.service.Import(c.Request().Context(), userID, raw, opts, c.FormValue("passphrase"))
	if err != nil {
		if errors.Is(err, runtimefence.ErrResetLeaseLost) ||
			errors.Is(err, runtimefence.ErrTransactionsUnsupported) ||
			errors.Is(err, botbackup.ErrHistoryResetUnavailable) {
			return apperror.Wrap(apperror.CodeSessionHistoryInconsistent, err, nil)
		}
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return c.JSON(http.StatusOK, result)
}

func readUploadedBackup(c echo.Context) ([]byte, error) {
	file, err := c.FormFile("file")
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "file is required")
	}
	src, err := file.Open()
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "failed to open uploaded file")
	}
	defer func() { _ = src.Close() }()
	return io.ReadAll(src)
}

func importOptionsFromForm(c echo.Context) botbackup.ImportOptions {
	opts := botbackup.ImportOptions{
		Mode:        botbackup.ImportMode(strings.TrimSpace(c.FormValue("mode"))),
		TargetBotID: strings.TrimSpace(c.FormValue("target_bot_id")),
	}
	// "sections" is a JSON object mapping section -> strategy (skip|merge|replace),
	// e.g. {"settings":"replace","channels":"merge"}. When the field is absent,
	// every section is imported with the default strategy.
	if params, err := c.FormParams(); err == nil && params.Has("sections") {
		opts.Sections = parseSectionStrategies(c.FormValue("sections"))
	}
	return opts
}

func parseSectionStrategies(raw string) map[botbackup.Section]botbackup.ImportStrategy {
	out := map[botbackup.Section]botbackup.ImportStrategy{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return out
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return out
	}
	for k, v := range m {
		out[botbackup.Section(strings.TrimSpace(k))] = botbackup.ImportStrategy(strings.TrimSpace(v))
	}
	return out
}

func safeFilename(name, fallback string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = fallback
	}
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return fallback
	}
	return out
}
