package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/internal/apperror"
	"github.com/memohai/memoh/internal/mcp"
)

const (
	maxStickerCatalogBytes = 8 << 20
	maxStickerPreviewBytes = 20 << 20
	maxTelegramStickerSets = 8
)

const telegramStickerSetHeader = "X-Telegram-Sticker-Set"

var telegramStickerSetNamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

var telegramStickerHTTPClient = &http.Client{
	Timeout: 5 * time.Minute,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// TelegramStickerCatalogEntry is one first-party Telegram sticker management item.
type TelegramStickerCatalogEntry struct {
	ID          string `json:"id"`
	Emoji       string `json:"emoji,omitempty"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status"`
	Attempts    int    `json:"attempts,omitempty"`
}

// TelegramStickerSetCatalog is one configured Sticker Set in the first-party list.
type TelegramStickerSetCatalog struct {
	Name         string                        `json:"name"`
	TotalCount   int                           `json:"total_count"`
	ReadyCount   int                           `json:"ready_count"`
	FailedCount  int                           `json:"failed_count"`
	PendingCount int                           `json:"pending_count"`
	Stickers     []TelegramStickerCatalogEntry `json:"stickers"`
}

// TelegramStickerCatalog is the visual-recognition state of the configured Sticker Set.
type TelegramStickerCatalog struct {
	Name                      string                        `json:"name"`
	TotalCount                int                           `json:"total_count"`
	ReadyCount                int                           `json:"ready_count"`
	FailedCount               int                           `json:"failed_count"`
	PendingCount              int                           `json:"pending_count"`
	Stickers                  []TelegramStickerCatalogEntry `json:"stickers"`
	Sets                      []TelegramStickerSetCatalog   `json:"sets"`
	RecognitionModelID        string                        `json:"recognition_model_id,omitempty"`
	RecognitionModelInherited bool                          `json:"recognition_model_inherited"`
	PromptVersion             string                        `json:"prompt_version,omitempty"`
}

// UpdateTelegramStickerSetsRequest replaces the ordered-independent Sticker Set list.
type UpdateTelegramStickerSetsRequest struct {
	Names []string `json:"names"`
}

type telegramStickerRecognitionResult struct {
	Model         string `json:"model"`
	PromptVersion string `json:"prompt_version"`
	Description   string `json:"description"`
	Status        string `json:"status,omitempty"`
	Attempts      int    `json:"attempts,omitempty"`
}

// ListTelegramStickers godoc
// @Summary List Telegram stickers
// @Description List the configured Telegram Sticker Set with visual descriptions and recognition status
// @Tags telegram-stickers
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} TelegramStickerCatalog
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/telegram/stickers [get].
func (h *MCPHandler) ListTelegramStickers(c echo.Context) error {
	botID, channelIdentityID, err := h.authorizeTelegramStickerRequestIdentity(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	model, promptVersion, inherited, err := h.telegramStickerVisionProfile(ctx, botID)
	if err != nil {
		return h.stickerRecognitionError(err)
	}
	conn, endpoint, err := h.telegramStickerEndpoint(ctx, botID, "/api/catalog")
	if err != nil {
		return err
	}
	if err := h.configureTelegramStickerVisionProfile(ctx, conn, model, promptVersion); err != nil {
		return h.stickerServiceError("configure recognition profile", err)
	}
	resp, err := h.doTelegramStickerRequest(ctx, conn, http.MethodGet, endpoint)
	if err != nil {
		return h.stickerServiceError("list", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return h.stickerServiceError("list", fmt.Errorf("upstream status %d", resp.StatusCode))
	}
	var result TelegramStickerCatalog
	if err := decodeBoundedJSON(resp.Body, maxStickerCatalogBytes, &result); err != nil {
		return h.stickerServiceError("decode catalog", err)
	}
	if result.Stickers == nil {
		result.Stickers = []TelegramStickerCatalogEntry{}
	}
	normalizeTelegramStickerCatalog(&result)
	result.RecognitionModelID = model
	result.RecognitionModelInherited = inherited
	result.PromptVersion = promptVersion
	h.enqueueTelegramStickerRecognition(result, botID, channelIdentityID, model, promptVersion)
	return c.JSON(http.StatusOK, result)
}

// UpdateTelegramStickerSets godoc
// @Summary Update Telegram Sticker Sets
// @Description Replace the configured Sticker Set list. Sets are merged into one stable model-visible catalog.
// @Tags telegram-stickers
// @Param bot_id path string true "Bot ID"
// @Param payload body UpdateTelegramStickerSetsRequest true "Sticker Set names"
// @Success 200 {object} TelegramStickerCatalog
// @Failure 400 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/telegram/stickers/sets [put].
func (h *MCPHandler) UpdateTelegramStickerSets(c echo.Context) error {
	botID, channelIdentityID, err := h.authorizeTelegramStickerRequestIdentity(c)
	if err != nil {
		return err
	}
	var input UpdateTelegramStickerSetsRequest
	if err := c.Bind(&input); err != nil {
		return apperror.New(apperror.CodeStickerSetsInvalid, nil)
	}
	names, err := normalizeTelegramStickerSetNames(input.Names)
	if err != nil {
		return apperror.New(apperror.CodeStickerSetsInvalid, nil)
	}
	ctx := c.Request().Context()
	conn, endpoint, err := h.telegramStickerEndpoint(ctx, botID, "/api/catalog")
	if err != nil {
		return err
	}
	candidate, headers := telegramStickerConnectionWithSets(conn, names)
	rawURL, _ := candidate.Config["url"].(string)
	model, promptVersion, inherited, profileErr := h.telegramStickerVisionProfile(ctx, botID)
	if profileErr != nil {
		return h.stickerRecognitionError(profileErr)
	}
	if err := h.configureTelegramStickerVisionProfile(ctx, candidate, model, promptVersion); err != nil {
		return h.stickerServiceError("configure Sticker Sets", err)
	}

	// Validate and load every Set before replacing the persisted connection.
	// Normal reads then remain on the service's permanent metadata cache.
	resp, err := h.doTelegramStickerRequest(ctx, candidate, http.MethodGet, endpoint)
	if err != nil {
		return h.stickerServiceError("validate Sticker Sets", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return h.stickerServiceError("validate Sticker Sets", fmt.Errorf("upstream status %d", resp.StatusCode))
	}
	var result TelegramStickerCatalog
	if err := decodeBoundedJSON(resp.Body, maxStickerCatalogBytes, &result); err != nil {
		return h.stickerServiceError("decode Sticker Sets", err)
	}

	active := conn.Active
	updated, err := h.service.Update(ctx, botID, conn.ID, mcp.UpsertRequest{
		Name: conn.Name, URL: rawURL, Headers: headers,
		Active: &active, AuthType: conn.AuthType,
	})
	if err != nil {
		return h.stickerServiceError("save Sticker Sets", err)
	}
	if h.fedGateway != nil {
		tools, probeErr := h.fedGateway.ListHTTPConnectionTools(ctx, updated)
		if probeErr != nil {
			_ = h.service.UpdateProbeResult(ctx, botID, updated.ID, "error", []mcp.ToolDescriptor{}, probeErr.Error())
			return h.stickerServiceError("refresh Sticker tool schema", probeErr)
		}
		_ = h.service.UpdateProbeResult(ctx, botID, updated.ID, "connected", tools, "")
	}
	result.RecognitionModelID = model
	result.RecognitionModelInherited = inherited
	result.PromptVersion = promptVersion
	normalizeTelegramStickerCatalog(&result)
	h.enqueueTelegramStickerRecognition(result, botID, channelIdentityID, model, promptVersion)
	return c.JSON(http.StatusOK, result)
}

func normalizeTelegramStickerSetNames(raw []string) ([]string, error) {
	if len(raw) == 0 || len(raw) > maxTelegramStickerSets {
		return nil, errors.New("invalid Sticker Set count")
	}
	seen := make(map[string]struct{}, len(raw))
	names := make([]string, 0, len(raw))
	for _, value := range raw {
		name := strings.TrimSpace(value)
		if !telegramStickerSetNamePattern.MatchString(name) {
			return nil, errors.New("invalid Sticker Set name")
		}
		identity := strings.ToLower(name)
		if _, exists := seen[identity]; exists {
			continue
		}
		seen[identity] = struct{}{}
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil, errors.New("at least one Sticker Set is required")
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	return names, nil
}

func telegramStickerConnectionWithSets(conn mcp.Connection, names []string) (mcp.Connection, map[string]string) {
	candidate := conn
	candidate.Config = make(map[string]any, len(conn.Config))
	for key, value := range conn.Config {
		candidate.Config[key] = value
	}
	headers := make(map[string]string)
	if raw, ok := conn.Config["headers"].(map[string]any); ok {
		for key, value := range raw {
			if text, ok := value.(string); ok && !strings.EqualFold(key, telegramStickerSetHeader) {
				headers[key] = text
			}
		}
	}
	if raw, ok := conn.Config["headers"].(map[string]string); ok {
		for key, value := range raw {
			if !strings.EqualFold(key, telegramStickerSetHeader) {
				headers[key] = value
			}
		}
	}
	headers[telegramStickerSetHeader] = strings.Join(names, ",")
	stored := make(map[string]any, len(headers))
	for key, value := range headers {
		stored[key] = value
	}
	candidate.Config["headers"] = stored
	return candidate, headers
}

func normalizeTelegramStickerCatalog(result *TelegramStickerCatalog) {
	if result == nil {
		return
	}
	if result.Stickers == nil {
		result.Stickers = []TelegramStickerCatalogEntry{}
	}
	if len(result.Sets) == 0 && strings.TrimSpace(result.Name) != "" {
		result.Sets = []TelegramStickerSetCatalog{{
			Name: result.Name, TotalCount: result.TotalCount, ReadyCount: result.ReadyCount,
			FailedCount: result.FailedCount, PendingCount: result.PendingCount, Stickers: result.Stickers,
		}}
	}
	for index := range result.Sets {
		if result.Sets[index].Stickers == nil {
			result.Sets[index].Stickers = []TelegramStickerCatalogEntry{}
		}
	}
}

// RefreshTelegramStickerSet godoc
// @Summary Refresh Telegram Sticker Set metadata
// @Description Explicitly replace the permanent Sticker Set metadata cache from Telegram
// @Tags telegram-stickers
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} TelegramStickerCatalog
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/telegram/stickers/refresh [post].
func (h *MCPHandler) RefreshTelegramStickerSet(c echo.Context) error {
	botID, channelIdentityID, err := h.authorizeTelegramStickerRequestIdentity(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	model, promptVersion, inherited, err := h.telegramStickerVisionProfile(ctx, botID)
	if err != nil {
		return h.stickerRecognitionError(err)
	}
	conn, endpoint, err := h.telegramStickerEndpoint(
		ctx,
		botID,
		"/api/catalog/refresh",
	)
	if err != nil {
		return err
	}
	if err := h.configureTelegramStickerVisionProfile(ctx, conn, model, promptVersion); err != nil {
		return h.stickerServiceError("configure recognition profile", err)
	}
	resp, err := h.doTelegramStickerRequest(ctx, conn, http.MethodPost, endpoint)
	if err != nil {
		return h.stickerServiceError("refresh", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return h.stickerServiceError("refresh", fmt.Errorf("upstream status %d", resp.StatusCode))
	}
	var result TelegramStickerCatalog
	if err := decodeBoundedJSON(resp.Body, maxStickerCatalogBytes, &result); err != nil {
		return h.stickerServiceError("decode refreshed catalog", err)
	}
	if result.Stickers == nil {
		result.Stickers = []TelegramStickerCatalogEntry{}
	}
	normalizeTelegramStickerCatalog(&result)
	result.RecognitionModelID = model
	result.RecognitionModelInherited = inherited
	result.PromptVersion = promptVersion
	h.enqueueTelegramStickerRecognition(result, botID, channelIdentityID, model, promptVersion)
	return c.JSON(http.StatusOK, result)
}

// PreviewTelegramSticker godoc
// @Summary Preview a Telegram sticker
// @Description Return a static preview for one configured Telegram sticker
// @Tags telegram-stickers
// @Param bot_id path string true "Bot ID"
// @Param sticker_id path string true "Sticker ID"
// @Produce image/webp
// @Success 200 {file} binary
// @Failure 404 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/telegram/stickers/{sticker_id}/preview [get].
func (h *MCPHandler) PreviewTelegramSticker(c echo.Context) error {
	botID, err := h.authorizeTelegramStickerRequest(c)
	if err != nil {
		return err
	}
	stickerID := strings.TrimSpace(c.Param("sticker_id"))
	if stickerID == "" {
		return apperror.New(apperror.CodeStickerNotFound, nil)
	}
	conn, endpoint, err := h.telegramStickerEndpoint(
		c.Request().Context(),
		botID,
		"/api/stickers/"+url.PathEscape(stickerID)+"/preview",
	)
	if err != nil {
		return err
	}
	resp, err := h.doTelegramStickerRequest(c.Request().Context(), conn, http.MethodGet, endpoint)
	if err != nil {
		return h.stickerServiceError("preview", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return apperror.New(apperror.CodeStickerNotFound, nil)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return h.stickerServiceError("preview", fmt.Errorf("upstream status %d", resp.StatusCode))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxStickerPreviewBytes+1))
	if err != nil {
		return h.stickerServiceError("read preview", err)
	}
	if len(data) > maxStickerPreviewBytes {
		return h.stickerServiceError("read preview", errors.New("preview exceeds size limit"))
	}
	contentType := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if contentType == "" || contentType == "application/octet-stream" {
		contentType = http.DetectContentType(data)
	}
	c.Response().Header().Set("Cache-Control", "private, max-age=3600")
	c.Response().Header().Set("X-Content-Type-Options", "nosniff")
	return c.Blob(http.StatusOK, contentType, data)
}

// RetryTelegramStickerRecognition godoc
// @Summary Retry Telegram sticker recognition
// @Description Clear a failed visual-description cache entry and recognize the sticker again
// @Tags telegram-stickers
// @Param bot_id path string true "Bot ID"
// @Param sticker_id path string true "Sticker ID"
// @Success 200 {object} TelegramStickerCatalogEntry
// @Failure 404 {object} apperror.Problem
// @Failure 502 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/telegram/stickers/{sticker_id}/retry [post].
func (h *MCPHandler) RetryTelegramStickerRecognition(c echo.Context) error {
	botID, channelIdentityID, err := h.authorizeTelegramStickerRequestIdentity(c)
	if err != nil {
		return err
	}
	stickerID := strings.TrimSpace(c.Param("sticker_id"))
	if stickerID == "" {
		return apperror.New(apperror.CodeStickerNotFound, nil)
	}
	ctx := c.Request().Context()
	model, promptVersion, _, err := h.telegramStickerVisionProfile(ctx, botID)
	if err != nil {
		return h.stickerRecognitionError(err)
	}
	result, err := h.recognizeTelegramSticker(ctx, telegramStickerRecognitionTask{
		BotID: botID, ChannelIdentityID: channelIdentityID, StickerID: stickerID,
		Model: model, PromptVersion: promptVersion,
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, result)
}

func (h *MCPHandler) recognizeTelegramSticker(
	ctx context.Context,
	task telegramStickerRecognitionTask,
) (TelegramStickerCatalogEntry, error) {
	conn, previewEndpoint, err := h.telegramStickerEndpoint(
		ctx, task.BotID, "/api/stickers/"+url.PathEscape(task.StickerID)+"/preview",
	)
	if err != nil {
		return TelegramStickerCatalogEntry{}, err
	}
	failed := func(cause error, model, promptVersion string) (TelegramStickerCatalogEntry, error) {
		if storeErr := h.storeTelegramStickerRecognitionFailure(
			ctx, conn, task.StickerID, model, promptVersion,
		); storeErr != nil && h.logger != nil {
			h.logger.Warn("Telegram sticker recognition failure status could not be stored",
				slog.String("bot_id", task.BotID),
				slog.String("sticker_id", task.StickerID),
				slog.Any("error", storeErr),
			)
		}
		return TelegramStickerCatalogEntry{}, h.stickerRecognitionError(cause)
	}

	preview, err := h.doTelegramStickerRequest(ctx, conn, http.MethodGet, previewEndpoint)
	if err != nil {
		return failed(err, task.Model, task.PromptVersion)
	}
	defer func() { _ = preview.Body.Close() }()
	if preview.StatusCode == http.StatusNotFound {
		return TelegramStickerCatalogEntry{}, apperror.New(apperror.CodeStickerNotFound, nil)
	}
	if preview.StatusCode < http.StatusOK || preview.StatusCode >= http.StatusMultipleChoices {
		return failed(fmt.Errorf("preview status %d", preview.StatusCode), task.Model, task.PromptVersion)
	}
	data, err := io.ReadAll(io.LimitReader(preview.Body, maxStickerPreviewBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxStickerPreviewBytes {
		return failed(errors.New("invalid Sticker preview"), task.Model, task.PromptVersion)
	}
	mediaType := strings.TrimSpace(strings.Split(preview.Header.Get("Content-Type"), ";")[0])
	if mediaType == "" || mediaType == "application/octet-stream" {
		mediaType = http.DetectContentType(data)
	}
	if h.stickerVision == nil {
		return failed(errors.New("sticker vision recognizer is unavailable"), task.Model, task.PromptVersion)
	}
	description, model, promptVersion, err := h.stickerVision.RecognizeTelegramSticker(
		ctx, task.BotID, task.ChannelIdentityID, mediaType, data,
	)
	if err != nil {
		if strings.TrimSpace(model) == "" {
			model = task.Model
		}
		if strings.TrimSpace(promptVersion) == "" {
			promptVersion = task.PromptVersion
		}
		return failed(err, model, promptVersion)
	}
	payload, err := json.Marshal(telegramStickerRecognitionResult{
		Model: model, PromptVersion: promptVersion, Description: description,
		Status: "ready", Attempts: 1,
	})
	if err != nil {
		return failed(err, model, promptVersion)
	}
	recognitionEndpoint, err := telegramStickerAPIEndpoint(
		conn, "/api/stickers/"+url.PathEscape(task.StickerID)+"/recognition",
	)
	if err != nil {
		return failed(err, model, promptVersion)
	}
	resp, err := h.doTelegramStickerJSONRequest(ctx, conn, http.MethodPost, recognitionEndpoint, payload)
	if err != nil {
		return failed(err, model, promptVersion)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return TelegramStickerCatalogEntry{}, apperror.New(apperror.CodeStickerNotFound, nil)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return failed(fmt.Errorf("store status %d", resp.StatusCode), model, promptVersion)
	}
	var result TelegramStickerCatalogEntry
	if err := decodeBoundedJSON(resp.Body, maxStickerCatalogBytes, &result); err != nil {
		return failed(err, model, promptVersion)
	}
	return result, nil
}

func (h *MCPHandler) authorizeTelegramStickerRequest(c echo.Context) (string, error) {
	botID, _, err := h.authorizeTelegramStickerRequestIdentity(c)
	return botID, err
}

func (h *MCPHandler) authorizeTelegramStickerRequestIdentity(c echo.Context) (string, string, error) {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return "", "", err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return "", "", echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), channelIdentityID, botID); err != nil {
		return "", "", err
	}
	return botID, channelIdentityID, nil
}

func (h *MCPHandler) telegramStickerVisionProfile(ctx context.Context, botID string) (string, string, bool, error) {
	if h.stickerVision == nil {
		return "", "", true, nil
	}
	return h.stickerVision.TelegramStickerVisionConfig(ctx, botID)
}

func (h *MCPHandler) configureTelegramStickerVisionProfile(
	ctx context.Context,
	conn mcp.Connection,
	model, promptVersion string,
) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	endpoint, err := telegramStickerAPIEndpoint(conn, "/api/profile")
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{
		"model": model, "prompt_version": strings.TrimSpace(promptVersion),
	})
	if err != nil {
		return err
	}
	resp, err := h.doTelegramStickerJSONRequest(ctx, conn, http.MethodPost, endpoint, payload)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("profile status %d", resp.StatusCode)
	}
	return nil
}

func (h *MCPHandler) storeTelegramStickerRecognitionFailure(
	ctx context.Context,
	conn mcp.Connection,
	stickerID, model, promptVersion string,
) error {
	model = strings.TrimSpace(model)
	promptVersion = strings.TrimSpace(promptVersion)
	if model == "" || promptVersion == "" {
		return nil
	}
	endpoint, err := telegramStickerAPIEndpoint(
		conn, "/api/stickers/"+url.PathEscape(stickerID)+"/recognition",
	)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(telegramStickerRecognitionResult{
		Model: model, PromptVersion: promptVersion, Status: "failed", Attempts: 1,
	})
	if err != nil {
		return err
	}
	resp, err := h.doTelegramStickerJSONRequest(ctx, conn, http.MethodPost, endpoint, payload)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("store failure status %d", resp.StatusCode)
	}
	return nil
}

func (h *MCPHandler) enqueueTelegramStickerRecognition(
	catalog TelegramStickerCatalog,
	botID, channelIdentityID, model, promptVersion string,
) {
	if h == nil || h.stickerRecognition == nil {
		return
	}
	tasks := pendingTelegramStickerRecognitionTasks(
		catalog, botID, channelIdentityID, model, promptVersion,
	)
	if count := h.stickerRecognition.Enqueue(tasks); count > 0 && h.logger != nil {
		h.logger.Info("Telegram sticker background recognition queued",
			slog.String("bot_id", botID),
			slog.Int("sticker_count", count),
		)
	}
}

func (h *MCPHandler) refreshTelegramStickerToolSchema(ctx context.Context, botID string) error {
	if h == nil || h.fedGateway == nil || h.service == nil {
		return nil
	}
	conn, _, err := h.telegramStickerEndpoint(ctx, botID, "/api/catalog")
	if err != nil {
		return err
	}
	tools, err := h.fedGateway.ListHTTPConnectionTools(ctx, conn)
	if err != nil {
		_ = h.service.UpdateProbeResult(ctx, botID, conn.ID, "error", []mcp.ToolDescriptor{}, err.Error())
		return err
	}
	return h.service.UpdateProbeResult(ctx, botID, conn.ID, "connected", tools, "")
}

func telegramStickerAPIEndpoint(conn mcp.Connection, apiPath string) (string, error) {
	rawURL, _ := conn.Config["url"].(string)
	return deriveStickerAPIEndpoint(rawURL, apiPath)
}

func (h *MCPHandler) telegramStickerEndpoint(ctx context.Context, botID, apiPath string) (mcp.Connection, string, error) {
	connections, err := h.service.ListActiveByBot(ctx, botID)
	if err != nil {
		return mcp.Connection{}, "", h.stickerServiceError("list connections", err)
	}
	for _, conn := range connections {
		if conn.Type != "http" || !isTelegramStickerConnection(conn) {
			continue
		}
		rawURL, _ := conn.Config["url"].(string)
		endpoint, endpointErr := deriveStickerAPIEndpoint(rawURL, apiPath)
		if endpointErr != nil {
			continue
		}
		return conn, endpoint, nil
	}
	return mcp.Connection{}, "", apperror.New(apperror.CodeStickerServiceUnavailable, nil)
}

func isTelegramStickerConnection(conn mcp.Connection) bool {
	for _, tool := range conn.ToolsCache {
		switch strings.TrimSpace(tool.Name) {
		case "send_telegram_sticker", "search_telegram_stickers":
			return true
		}
	}
	return strings.EqualFold(strings.TrimSpace(conn.Name), "sticker")
}

func deriveStickerAPIEndpoint(rawURL, apiPath string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return "", errors.New("invalid sticker MCP URL")
	}
	cleanPath := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(cleanPath, "/mcp") && cleanPath != "mcp" {
		return "", errors.New("sticker MCP URL must end with /mcp")
	}
	cleanPath = strings.TrimSuffix(cleanPath, "/mcp")
	parsed.Path = path.Join("/", cleanPath, strings.TrimLeft(apiPath, "/"))
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func (h *MCPHandler) doTelegramStickerRequest(ctx context.Context, conn mcp.Connection, method, endpoint string) (*http.Response, error) {
	return h.doTelegramStickerRequestBody(ctx, conn, method, endpoint, nil, "")
}

func (h *MCPHandler) doTelegramStickerJSONRequest(ctx context.Context, conn mcp.Connection, method, endpoint string, payload []byte) (*http.Response, error) {
	return h.doTelegramStickerRequestBody(ctx, conn, method, endpoint, strings.NewReader(string(payload)), "application/json")
}

func (*MCPHandler) doTelegramStickerRequestBody(ctx context.Context, conn mcp.Connection, method, endpoint string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	if rawHeaders, ok := conn.Config["headers"].(map[string]any); ok {
		for key, value := range rawHeaders {
			if text, ok := value.(string); ok && strings.TrimSpace(key) != "" {
				req.Header.Set(key, text)
			}
		}
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return telegramStickerHTTPClient.Do(req)
}

func decodeBoundedJSON(reader io.Reader, limit int64, output any) error {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > limit {
		return errors.New("response exceeds size limit")
	}
	return json.Unmarshal(data, output)
}

func (h *MCPHandler) stickerServiceError(operation string, cause error) error {
	if h.logger != nil {
		h.logger.Warn("Telegram sticker service request failed",
			slog.String("operation", operation), slog.Any("error", cause))
	}
	return apperror.Wrap(apperror.CodeStickerServiceUnavailable, cause, nil)
}

func (h *MCPHandler) stickerRecognitionError(cause error) error {
	if h.logger != nil {
		h.logger.Warn("Telegram sticker recognition failed", slog.Any("error", cause))
	}
	return apperror.Wrap(apperror.CodeStickerRecognitionFailed, cause, nil)
}
