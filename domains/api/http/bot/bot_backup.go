package bot

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/domains/api/bot"
	botbackup "github.com/memohai/memoh/domains/api/bot/backup"
	httpx "github.com/memohai/memoh/domains/api/http"
	"github.com/memohai/memoh/domains/api/identity/auth"
	"github.com/memohai/memoh/domains/api/internal/bot/backup/secure"
	"github.com/memohai/memoh/domains/iam/account"
	"github.com/memohai/memoh/internal/apperror"
)

type BotBackupHandler struct {
	service        *botbackup.Service
	botService     *bot.Service
	accountService *account.Service
}

func NewBotBackupHandler(service *botbackup.Service, botService *bot.Service, accountService *account.Service) *BotBackupHandler {
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
// @Failure 403 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/backup/summary [get].
func (h *BotBackupHandler) Summary(c echo.Context) error {
	if h.service == nil {
		return apperror.Internal("backup service", nil)
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	userID, err := auth.UserIDFromContext(c)
	if err != nil {
		return apperror.Unauthenticated("resolve user", err)
	}
	if _, err := httpx.AuthorizeBotAccess(c.Request().Context(), h.botService, h.accountService, userID, botID); err != nil {
		return err
	}
	res, err := h.service.Summary(c.Request().Context(), botID)
	if err != nil {
		return apperror.Invalid("summarize bot backup", err)
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
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/backup/export [post].
func (h *BotBackupHandler) Export(c echo.Context) error {
	if h.service == nil {
		return apperror.Internal("backup service", nil)
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	userID, err := auth.UserIDFromContext(c)
	if err != nil {
		return apperror.Unauthenticated("resolve user", err)
	}
	bot, err := httpx.AuthorizeBotAccess(c.Request().Context(), h.botService, h.accountService, userID, botID)
	if err != nil {
		return err
	}
	var req botbackup.ExportRequest
	if c.Request().Body != nil {
		if err := c.Bind(&req); err != nil {
			return apperror.Invalid("bind export request", err)
		}
	}

	// Build the bundle into a temp file first. The whole export can fail (e.g.
	// the workspace stream errors) and that must surface as a proper HTTP error
	// rather than a truncated body after a misleading "200 OK".
	tmp, err := os.CreateTemp("", "memoh-backup-*.zip")
	if err != nil {
		return apperror.Internal("create temp file", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()

	if err := h.service.Export(c.Request().Context(), botID, botbackup.ExportOptions{Sections: req.Sections}, tmp); err != nil {
		return apperror.Internal("export bot backup", err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return apperror.Internal("seek backup file", err)
	}

	// A bundle is arbitrarily large and the client may be on a slow link, so the
	// download must not race the server WriteTimeout.
	if err := httpx.ClearWriteDeadline(c); err != nil {
		return err
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
// @Failure 400 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/backup/import/preview [post].
func (h *BotBackupHandler) PreviewImport(c echo.Context) error {
	if h.service == nil {
		return apperror.Internal("backup service", nil)
	}
	if _, err := auth.UserIDFromContext(c); err != nil {
		return apperror.Unauthenticated("resolve user", err)
	}
	raw, err := readUploadedBackup(c)
	if err != nil {
		return err
	}
	preview, err := h.service.Preview(c.Request().Context(), raw, importOptionsFromForm(c), c.FormValue("passphrase"))
	if err != nil {
		return apperror.Invalid("preview bot backup", err)
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
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/backup/import [post].
func (h *BotBackupHandler) Import(c echo.Context) error {
	if h.service == nil {
		return apperror.Internal("backup service", nil)
	}
	userID, err := auth.UserIDFromContext(c)
	if err != nil {
		return apperror.Unauthenticated("resolve user", err)
	}
	opts := importOptionsFromForm(c)
	if opts.Mode == botbackup.ImportModeOverwrite {
		if _, err := httpx.AuthorizeBotAccess(c.Request().Context(), h.botService, h.accountService, userID, opts.TargetBotID); err != nil {
			return err
		}
	}
	raw, err := readUploadedBackup(c)
	if err != nil {
		return err
	}
	result, err := h.service.Import(c.Request().Context(), userID, raw, opts, c.FormValue("passphrase"))
	if err != nil {
		return apperror.Invalid("import bot backup", err)
	}
	return c.JSON(http.StatusOK, result)
}

func readUploadedBackup(c echo.Context) ([]byte, error) {
	// Same reasoning as the export path, in the read direction.
	if err := httpx.ClearReadDeadline(c); err != nil {
		return nil, err
	}
	file, err := c.FormFile("file")
	if err != nil {
		return nil, apperror.Required("file")
	}
	src, err := file.Open()
	if err != nil {
		return nil, apperror.Invalid("open backup file", err)
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
