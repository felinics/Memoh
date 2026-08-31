package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/acl"
	acpclient "github.com/felinics/memoh/internal/agent/runtime/acp/client"
	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/agentcredential"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/auth"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/route"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/httpx"
	"github.com/felinics/memoh/internal/identity"
	runtimeRpc "github.com/felinics/memoh/internal/rpc/runtime"
	"github.com/felinics/memoh/internal/runtimefence"
	"github.com/felinics/memoh/internal/workspace"
)

type botCreateWorkspace interface {
	SetupBotContainerWithProgress(ctx context.Context, botID string, progress workspace.ContainerSetupProgress) error
}

type runtimeResetService interface {
	BeginBotHistoryReset(ctx context.Context, botID string) (resetCtx context.Context, release func(), err error)
}

type createBotStreamBotEvent struct {
	Type string   `json:"type"`
	Bot  bots.Bot `json:"bot"`
}

// UsersHandler manages user/account CRUD and bot operations via REST API.
type UsersHandler struct {
	service        *accounts.Service
	botService     *bots.Service
	routeService   route.Service
	channelStore   *channel.Store
	channelRuntime channel.Runtime
	registry       *channel.Registry
	workspaceSetup botCreateWorkspace
	runtimeResets  runtimeResetService
	credentials    *agentcredential.Service
	logger         *slog.Logger
}

// NewUsersHandler creates a UsersHandler with channel identity support.
func NewUsersHandler(log *slog.Logger, service *accounts.Service, botService *bots.Service, routeService route.Service, channelStore *channel.Store, channelRuntime channel.Runtime, registry *channel.Registry, workspaceSetup botCreateWorkspace) *UsersHandler {
	if log == nil {
		log = slog.Default()
	}
	return &UsersHandler{
		service:        service,
		botService:     botService,
		routeService:   routeService,
		channelStore:   channelStore,
		channelRuntime: channelRuntime,
		registry:       registry,
		workspaceSetup: workspaceSetup,
		logger:         log.With(slog.String("handler", "users")),
	}
}

func (h *UsersHandler) SetRuntimeResetService(closer runtimeResetService) {
	h.runtimeResets = closer
}

func (h *UsersHandler) SetCredentialService(service *agentcredential.Service) {
	h.credentials = service
}

func (h *UsersHandler) Register(e *echo.Echo) {
	userGroup := e.Group("/users")
	userGroup.GET("/me", h.GetMe)
	userGroup.PUT("/me", h.UpdateMe)
	userGroup.PUT("/me/password", h.UpdateMyPassword)
	userGroup.GET("", h.ListUsers)
	userGroup.GET("/:id", h.GetUser)
	userGroup.PUT("/:id", h.UpdateUser)
	userGroup.POST("", h.CreateUser)
	userGroup.DELETE("/:id", h.RemoveMember)

	botGroup := e.Group("/bots")
	botGroup.POST("", h.CreateBot)
	botGroup.GET("", h.ListBots)
	botGroup.GET("/name-availability", h.CheckBotName)
	botGroup.GET("/:id", h.GetBot)
	botGroup.GET("/:id/checks", h.ListBotChecks)
	botGroup.PUT("/:id", h.UpdateBot)
	botGroup.PUT("/:id/owner", h.TransferBotOwner)
	botGroup.DELETE("/:id", h.DeleteBot)
	botGroup.GET("/:id/channel/:platform", h.GetBotChannelConfig)
	botGroup.PUT("/:id/channel/:platform", h.UpsertBotChannelConfig)
	botGroup.PATCH("/:id/channel/:platform/status", h.UpdateBotChannelStatus)
	botGroup.POST("/:id/channel/:platform/webhook-endpoint", h.SetBotChannelWebhookEndpoint)
	botGroup.DELETE("/:id/channel/:platform", h.DeleteBotChannelConfig)
	botGroup.POST("/:id/channel/:platform/send", h.SendBotMessage)
	botGroup.POST("/:id/channel/:platform/send_chat", h.SendBotMessageSession)
}

// GetMe godoc
// @Summary Get current user
// @Description Get current user profile
// @Tags users
// @Success 200 {object} accounts.Account
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /users/me [get].
func (h *UsersHandler) GetMe(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	resp, err := h.service.Get(c.Request().Context(), channelIdentityID)
	if err != nil {
		if accountNotFound(err) {
			return echo.NewHTTPError(http.StatusUnauthorized, "current user not found, please login again")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, resp)
}

// UpdateMe godoc
// @Summary Update current user profile
// @Description Update current user profile and preferences
// @Tags users
// @Param payload body accounts.UpdateProfileRequest true "Profile payload"
// @Success 200 {object} accounts.Account
// @Failure 400 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /users/me [put].
func (h *UsersHandler) UpdateMe(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	var req accounts.UpdateProfileRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Wrap(apperror.CodeProfileRequestInvalid, err, nil)
	}
	resp, err := h.service.UpdateProfile(c.Request().Context(), channelIdentityID, req)
	if err != nil {
		if errors.Is(err, accounts.ErrInvalidTitleModel) {
			return apperror.Wrap(apperror.CodeProfileTitleModelInvalid, err, nil)
		}
		return apperror.Wrap(apperror.CodeProfileUpdateFailed, err, nil)
	}
	return c.JSON(http.StatusOK, resp)
}

// UpdateMyPassword godoc
// @Summary Update current user password
// @Description Update current user password with current password check
// @Tags users
// @Param payload body accounts.UpdatePasswordRequest true "Password payload"
// @Success 204 "No Content"
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /users/me/password [put].
func (h *UsersHandler) UpdateMyPassword(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	var req accounts.UpdatePasswordRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := h.service.UpdatePassword(c.Request().Context(), channelIdentityID, req.CurrentPassword, req.NewPassword); err != nil {
		if errors.Is(err, accounts.ErrInvalidPassword) {
			return echo.NewHTTPError(http.StatusBadRequest, "current password mismatch")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.NoContent(http.StatusNoContent)
}

// ListUsers godoc
// @Summary List users (admin only)
// @Description List users
// @Tags users
// @Success 200 {object} accounts.ListAccountsResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /users [get].
func (h *UsersHandler) ListUsers(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	isAdmin, err := h.service.IsAdmin(c.Request().Context(), channelIdentityID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if !isAdmin {
		return echo.NewHTTPError(http.StatusForbidden, "admin role required")
	}
	if strings.TrimSpace(c.QueryParam("user_type")) != "" || strings.TrimSpace(c.QueryParam("owner_id")) != "" {
		return echo.NewHTTPError(http.StatusBadRequest, "user_type and owner_id are not supported")
	}
	items, err := h.service.ListAccounts(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, accounts.ListAccountsResponse{Items: items})
}

// GetUser godoc
// @Summary Get user by ID
// @Description Get user details (self or admin only)
// @Tags users
// @Param id path string true "User ID"
// @Success 200 {object} accounts.Account
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /users/{id} [get].
func (h *UsersHandler) GetUser(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	targetID := strings.TrimSpace(c.Param("id"))
	if targetID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "user id is required")
	}
	if targetID != channelIdentityID {
		isAdmin, err := h.service.IsAdmin(c.Request().Context(), channelIdentityID)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		if !isAdmin {
			return echo.NewHTTPError(http.StatusForbidden, "user access denied")
		}
	}
	user, err := h.service.Get(c.Request().Context(), targetID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, "user not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, user)
}

// UpdateUser godoc
// @Summary Update user (admin only)
// @Description Update the user's role or membership status in the current workspace
// @Tags users
// @Param id path string true "User ID"
// @Param payload body accounts.UpdateAccountRequest true "User update payload"
// @Success 200 {object} accounts.Account
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /users/{id} [put].
func (h *UsersHandler) UpdateUser(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	isAdmin, err := h.service.IsAdmin(c.Request().Context(), channelIdentityID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if !isAdmin {
		return echo.NewHTTPError(http.StatusForbidden, "admin role required")
	}
	targetID := strings.TrimSpace(c.Param("id"))
	if targetID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "user id is required")
	}
	_, err = h.service.Get(c.Request().Context(), targetID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, "user not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	var req accounts.UpdateAccountRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	resp, err := h.service.UpdateAdmin(c.Request().Context(), targetID, req)
	if err != nil {
		if errors.Is(err, db.ErrLastActiveAdmin) {
			return echo.NewHTTPError(http.StatusConflict, err.Error())
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, resp)
}

// CreateUser godoc
// @Summary Create human user (admin only)
// @Description Create a new human user account
// @Tags users
// @Param payload body accounts.CreateAccountRequest true "User payload"
// @Success 201 {object} accounts.Account
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /users [post].
func (h *UsersHandler) CreateUser(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	isAdmin, err := h.service.IsAdmin(c.Request().Context(), channelIdentityID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if !isAdmin {
		return echo.NewHTTPError(http.StatusForbidden, "admin role required")
	}
	var req accounts.CreateAccountRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	//nolint:staticcheck // Keep backward-compatible behavior: CreateHuman creates backing user when owner id is empty.
	resp, err := h.service.CreateHuman(c.Request().Context(), "", req)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusCreated, resp)
}

// RemoveMember godoc
// @Summary Deactivate member (admin only)
// @Description Deactivate the member in the current workspace without changing global credentials
// @Tags users
// @Param id path string true "User ID"
// @Success 204 "No Content"
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /users/{id} [delete].
func (h *UsersHandler) RemoveMember(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	isAdmin, err := h.service.IsAdmin(c.Request().Context(), channelIdentityID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if !isAdmin {
		return echo.NewHTTPError(http.StatusForbidden, "admin role required")
	}
	targetID := strings.TrimSpace(c.Param("id"))
	if targetID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "user id is required")
	}
	if targetID == channelIdentityID {
		return echo.NewHTTPError(http.StatusBadRequest, "cannot remove current member")
	}
	if _, err := h.service.Get(c.Request().Context(), targetID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, db.ErrNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "member not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if err := h.service.RemoveMember(c.Request().Context(), targetID); err != nil {
		if errors.Is(err, db.ErrLastActiveAdmin) {
			return echo.NewHTTPError(http.StatusConflict, err.Error())
		}
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, db.ErrNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "member not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.NoContent(http.StatusNoContent)
}

// CreateBot godoc
// @Summary Create bot user
// @Description Create a bot user owned by current user (or admin-specified owner)
// @Tags bots
// @Param payload body bots.CreateBotRequest true "Bot payload"
// @Success 201 {object} bots.Bot
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 409 {object} apperror.Problem
// @Failure 500 {object} ErrorResponse
// @Router /bots [post].
func (h *UsersHandler) CreateBot(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	var req bots.CreateBotRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	ownerID := channelIdentityID
	ownerFromToken := true
	if raw := strings.TrimSpace(c.QueryParam("owner_id")); raw != "" {
		isAdmin, err := h.service.IsAdmin(c.Request().Context(), channelIdentityID)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		if !isAdmin {
			return echo.NewHTTPError(http.StatusForbidden, "admin role required for owner override")
		}
		if err := identity.ValidateChannelIdentityID(raw); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		ownerID = raw
		ownerFromToken = false
	}
	if ownerFromToken {
		if _, err := h.service.Get(c.Request().Context(), ownerID); err != nil {
			if accountNotFound(err) {
				return echo.NewHTTPError(http.StatusUnauthorized, "owner user not found, please login again")
			} else {
				return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
			}
		}
	}
	if req.Metadata != nil {
		if err := validateACPManagedConfig(req.Metadata); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid ACP metadata: "+err.Error())
		}
	}
	if acceptsEventStream(c) {
		return h.createBotStream(c, ownerID, ownerFromToken, req)
	}
	resp, err := h.botService.Create(c.Request().Context(), ownerID, req)
	if err != nil {
		return createBotHTTPError(err, ownerFromToken)
	}
	// Mirror UpdateBot: when a bot is created with ACP metadata (e.g. the
	// onboarding flow creates the bot directly with an api_key agent), write the
	// managed workspace config now so the first session has its credentials.
	// This requires a ready workspace (the bridge must be reachable), which is
	// only guaranteed when WaitForReady ran the lifecycle synchronously. For
	// async creation the workspace isn't ready yet, so skip here and let the
	// config be written on a later settings update.
	//
	return c.JSON(http.StatusCreated, scrubBotForResponse(resp))
}

func acceptsEventStream(c echo.Context) bool {
	return strings.Contains(strings.ToLower(c.Request().Header.Get(echo.HeaderAccept)), "text/event-stream")
}

func accountNotFound(err error) bool {
	return errors.Is(err, pgx.ErrNoRows) || errors.Is(err, db.ErrNotFound)
}

func createBotHTTPError(err error, ownerFromToken bool) error {
	if errors.Is(err, workspace.ErrWorkspaceImageIncompatible) {
		return apperror.Wrap(apperror.CodeWorkspaceImageIncompatible, err, nil)
	}
	if errors.Is(err, workspace.ErrWorkspaceTemplateBootstrapFailed) {
		return apperror.Wrap(apperror.CodeWorkspaceTemplateBootstrapFailed, err, nil)
	}
	if errors.Is(err, bots.ErrOwnerUserNotFound) {
		if ownerFromToken {
			return echo.NewHTTPError(http.StatusUnauthorized, "owner user not found, please login again")
		}
		return echo.NewHTTPError(http.StatusBadRequest, "owner user not found")
	}
	if errors.Is(err, acl.ErrUnknownPreset) {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if errors.Is(err, bots.ErrBotNameTaken) {
		return apperror.New(apperror.CodeBotNameTaken, map[string]string{"field": "name"})
	}
	if errors.Is(err, bots.ErrBotNameInvalid) || errors.Is(err, bots.ErrBotNameReserved) {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
}

func (h *UsersHandler) createBotStream(c echo.Context, ownerID string, ownerFromToken bool, req bots.CreateBotRequest) error {
	flusher, ok := c.Response().Writer.(http.Flusher)
	if !ok {
		return echo.NewHTTPError(http.StatusInternalServerError, "streaming not supported")
	}
	if h.workspaceSetup == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "workspace lifecycle not configured")
	}

	req.WaitForReady = false
	req.SkipLifecycle = true
	bot, err := h.botService.Create(c.Request().Context(), ownerID, req)
	if err != nil {
		return createBotHTTPError(err, ownerFromToken)
	}

	setSSEHeaders(c)
	c.Response().WriteHeader(http.StatusOK)
	writer := c.Response().Writer

	var mu sync.Mutex
	var writeErr error
	send := func(payload any) bool {
		mu.Lock()
		defer mu.Unlock()
		if writeErr != nil {
			return false
		}
		if err := writeSSEJSON(writer, flusher, payload); err != nil {
			writeErr = err
			return false
		}
		return true
	}
	sendError := func(code, i18nKey, message string) {
		_ = send(createContainerErrorEvent{
			Type:      "error",
			Code:      code,
			I18nKey:   i18nKey,
			Args:      map[string]string{},
			Message:   message,
			RequestID: httpx.RequestID(c),
		})
	}

	send(createBotStreamBotEvent{Type: "bot_created", Bot: scrubBotForResponse(bot)})

	lifecycleCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Request().Context()), 5*time.Minute)
	defer cancel()

	if err := h.workspaceSetup.SetupBotContainerWithProgress(lifecycleCtx, bot.ID, func(event workspace.ContainerSetupEvent) {
		switch event.Type {
		case "pulling":
			send(createContainerPullingEvent{Type: "pulling", Image: event.Image})
		case "pull_progress":
			send(createContainerPullProgressEvent{Type: "pull_progress", Layers: event.Layers})
		case "pull_skipped", "pull_delegated":
			send(createContainerPullStatusEvent{Type: event.Type, Image: event.Image, Message: event.Message})
		case "creating":
			send(createContainerCreatingEvent{Type: "creating"})
		case "restoring":
			send(createContainerRestoringEvent{Type: "restoring"})
		case "complete":
			send(createContainerCompleteEvent{
				Type: "complete",
				Container: CreateContainerResponse{
					ContainerID:      event.ContainerID,
					WorkspaceBackend: event.WorkspaceBackend,
					RuntimeBackend:   event.RuntimeBackend,
					ContainerPath:    event.ContainerPath,
					Image:            event.Image,
					CDIDevices:       event.CDIDevices,
					Started:          event.Started,
					DataRestored:     event.DataRestored,
					HasPreservedData: event.HasPreservedData,
				},
			})
		}
	}); err != nil {
		h.logger.Error("bot container setup failed",
			slog.String("bot_id", bot.ID),
			slog.Any("error", err),
		)
		if recordErr := h.botService.RecordContainerSetupFailure(lifecycleCtx, bot.ID, "setup", err); recordErr != nil {
			h.logger.Warn("record bot container setup failure failed",
				slog.String("bot_id", bot.ID),
				slog.Any("error", recordErr),
			)
		}
		if _, readyErr := h.botService.MarkReady(lifecycleCtx, bot.ID); readyErr != nil {
			h.logger.Error("failed to update bot status to ready after stream create failure",
				slog.String("bot_id", bot.ID),
				slog.Any("error", readyErr),
			)
			sendError("workspace_setup_failed", "bots.create.failedSubtitle", "workspace setup failed; ready status update failed")
			return nil
		}
		if event, ok := newWorkspaceSetupAppError(err, httpx.RequestID(c)); ok {
			_ = send(event)
			return nil
		}
		sendError("workspace_setup_failed", "bots.create.failedSubtitle", "workspace setup failed")
		return nil
	}

	if clearErr := h.botService.ClearContainerSetupFailure(lifecycleCtx, bot.ID); clearErr != nil {
		h.logger.Warn("clear bot container setup failure failed",
			slog.String("bot_id", bot.ID),
			slog.Any("error", clearErr),
		)
	}
	readyBot, err := h.botService.MarkReady(lifecycleCtx, bot.ID)
	if err != nil {
		h.logger.Error("failed to update bot status to ready after stream create",
			slog.String("bot_id", bot.ID),
			slog.Any("error", err),
		)
		sendError("bot_ready_update_failed", "bots.create.failedSubtitle", "ready status update failed: "+err.Error())
		return nil
	}
	send(createBotStreamBotEvent{Type: "ready", Bot: scrubBotForResponse(readyBot)})
	return nil
}

// CheckBotName godoc
// @Summary Check bot name availability
// @Description Validate a candidate bot name and report whether it is available
// @Tags bots
// @Param name query string true "Candidate bot name"
// @Param exclude_bot_id query string false "Bot ID to exclude from the conflict check (used when renaming)"
// @Success 200 {object} bots.NameAvailability
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/name-availability [get].
func (h *UsersHandler) CheckBotName(c echo.Context) error {
	if _, err := h.requireChannelIdentityID(c); err != nil {
		return err
	}
	name := strings.TrimSpace(c.QueryParam("name"))
	if name == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "name is required")
	}
	excludeBotID := strings.TrimSpace(c.QueryParam("exclude_bot_id"))
	result, err := h.botService.CheckNameAvailability(c.Request().Context(), name, excludeBotID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, result)
}

// ListBots godoc
// @Summary List bots
// @Description List bots accessible to current user (admin can specify owner_id)
// @Tags bots
// @Param owner_id query string false "Owner user ID (admin only)"
// @Success 200 {object} bots.ListBotsResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots [get].
func (h *UsersHandler) ListBots(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	ownerID := strings.TrimSpace(c.QueryParam("owner_id"))
	if ownerID != "" {
		isAdmin, err := h.service.IsAdmin(c.Request().Context(), channelIdentityID)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		if !isAdmin {
			return echo.NewHTTPError(http.StatusForbidden, "admin role required for owner filter")
		}
		items, err := h.botService.ListByOwner(c.Request().Context(), ownerID)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		if err := h.attachCurrentUserPermissionsList(c.Request().Context(), channelIdentityID, items); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		return c.JSON(http.StatusOK, bots.ListBotsResponse{Items: scrubBotsForResponse(items)})
	}
	items, err := h.botService.ListAccessible(c.Request().Context(), channelIdentityID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if err := h.attachCurrentUserPermissionsList(c.Request().Context(), channelIdentityID, items); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, bots.ListBotsResponse{Items: scrubBotsForResponse(items)})
}

// GetBot godoc
// @Summary Get bot details
// @Description Get a bot by ID (owner/admin only)
// @Tags bots
// @Param id path string true "Bot ID"
// @Success 200 {object} bots.Bot
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{id} [get].
func (h *UsersHandler) GetBot(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	bot, err := AuthorizeBotAccessWithPermission(c.Request().Context(), h.botService, h.service, channelIdentityID, botID, bots.PermissionChat)
	if err != nil {
		return err
	}
	if err := h.attachCurrentUserPermissions(c.Request().Context(), channelIdentityID, &bot); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, scrubBotForResponse(bot))
}

// ListBotChecks godoc
// @Summary List bot runtime checks
// @Description Evaluate bot attached resource checks in runtime
// @Tags bots
// @Param id path string true "Bot ID"
// @Success 200 {object} bots.ListChecksResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{id}/checks [get].
func (h *UsersHandler) ListBotChecks(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	// Health checks are read-only status; members with chat access may view them.
	// Detailed diagnostics can contain runtime paths, registry output, or host
	// network details, so only manage-level users receive detail/metadata fields.
	bot, err := AuthorizeBotAccessWithPermission(c.Request().Context(), h.botService, h.service, channelIdentityID, botID, bots.PermissionChat)
	if err != nil {
		return err
	}
	items, err := h.botService.ListChecks(c.Request().Context(), botID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, "bot not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	isAdmin, err := h.service.IsAdmin(c.Request().Context(), channelIdentityID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	perms, err := h.botService.ResolveUserPermissions(c.Request().Context(), bot.ID, channelIdentityID, isAdmin)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	includeDetails := bots.HasPermission(perms, bots.PermissionManage)
	return c.JSON(http.StatusOK, bots.ListChecksResponse{Items: scrubBotChecksForResponse(items, includeDetails)})
}

// UpdateBot godoc
// @Summary Update bot details
// @Description Update bot profile (owner/admin only)
// @Tags bots
// @Param id path string true "Bot ID"
// @Param payload body bots.UpdateBotRequest true "Bot update payload"
// @Success 200 {object} bots.Bot
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} apperror.Problem
// @Failure 500 {object} ErrorResponse
// @Router /bots/{id} [put].
func (h *UsersHandler) UpdateBot(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	bot, err := h.authorizeBotAccess(c.Request().Context(), channelIdentityID, botID)
	if err != nil {
		return err
	}
	var req bots.UpdateBotRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	shouldResetRuntimes := false
	if req.Metadata != nil {
		managedCredentialStore := h.credentials != nil && h.credentials.Configured()
		if managedCredentialStore {
			req.Metadata, _ = acpprofile.ScrubMetadataForExport(req.Metadata)
		}
		if err := h.botService.ValidateUpdate(c.Request().Context(), bot.ID, req); err != nil {
			return updateBotHTTPError(err)
		}
		pending := bot
		if managedCredentialStore {
			pending.Metadata = req.Metadata
		} else {
			pending.Metadata = acpprofile.MergeSensitiveFieldsForUpdate(bot.Metadata, req.Metadata)
		}
		shouldResetRuntimes = acpRuntimeMetadataChanged(bot.Metadata, pending.Metadata)
	}
	if shouldResetRuntimes && h.runtimeResets == nil {
		return apperror.Wrap(
			apperror.CodeSessionHistoryInconsistent,
			errors.New("runtime reset is not configured"),
			nil,
		)
	}
	if shouldResetRuntimes {
		resetCtx, releaseRuntimeReset, resetErr := h.runtimeResets.BeginBotHistoryReset(c.Request().Context(), bot.ID)
		if resetErr != nil {
			return apperror.Wrap(apperror.CodeSessionHistoryInconsistent, resetErr, nil)
		}
		defer releaseRuntimeReset()
		c.SetRequest(c.Request().WithContext(resetCtx))
	}
	var resp bots.Bot
	if h.credentials != nil && h.credentials.Configured() && req.Metadata != nil {
		resp, err = h.botService.UpdateReplacingMetadata(c.Request().Context(), bot.ID, req)
	} else {
		resp, err = h.botService.Update(c.Request().Context(), bot.ID, req)
	}
	if err != nil {
		if leaseErr := runtimefence.ResetLeaseFailure(c.Request().Context(), err); leaseErr != nil {
			return apperror.Wrap(apperror.CodeSessionHistoryInconsistent, leaseErr, nil)
		}
		return updateBotHTTPError(err)
	}
	return c.JSON(http.StatusOK, scrubBotForResponse(resp))
}

func validateACPManagedConfig(metadata map[string]any) error {
	for _, item := range acpprofile.List() {
		profile, ok := acpprofile.Lookup(item.ID)
		if !ok {
			continue
		}
		setup := acpprofile.ParseAgentSetup(metadata, profile.ID)
		if !setup.Enabled {
			continue
		}
		mode := acpclient.SetupMode(setup.Mode)
		if !acpProfileSupportsSetupMode(profile, mode) {
			return fmt.Errorf("%s does not support setup mode %q", profile.DisplayName, mode)
		}
		if mode == acpclient.SetupModeSelf {
			continue
		}
		if err := acpclient.ValidateManagedACPConfig(profile, setup, mode); err != nil {
			return err
		}
	}
	return nil
}

func acpProfileSupportsSetupMode(profile acpprofile.Profile, mode acpclient.SetupMode) bool {
	if len(profile.SetupModes) == 0 {
		return true
	}
	for _, supported := range profile.SetupModes {
		if strings.EqualFold(strings.TrimSpace(supported), string(mode)) {
			return true
		}
	}
	return false
}

func acpRuntimeMetadataChanged(existing, pending map[string]any) bool {
	return !reflect.DeepEqual(
		metadataSubtree(existing, acpprofile.MetadataKeyACP),
		metadataSubtree(pending, acpprofile.MetadataKeyACP),
	)
}

func metadataSubtree(metadata map[string]any, key string) map[string]any {
	subtree, ok := metadata[key].(map[string]any)
	if !ok {
		return nil
	}
	return subtree
}

func updateBotHTTPError(err error) error {
	if errors.Is(err, bots.ErrBotNameTaken) {
		return apperror.New(apperror.CodeBotNameTaken, map[string]string{"field": "name"})
	}
	if errors.Is(err, bots.ErrBotNameInvalid) || errors.Is(err, bots.ErrBotNameReserved) {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
}

// TransferBotOwner godoc
// @Summary Transfer bot owner (admin only)
// @Description Transfer bot ownership to another human user
// @Tags bots
// @Param id path string true "Bot ID"
// @Param payload body bots.TransferBotRequest true "Transfer payload"
// @Success 200 {object} bots.Bot
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{id}/owner [put].
func (h *UsersHandler) TransferBotOwner(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	isAdmin, err := h.service.IsAdmin(c.Request().Context(), channelIdentityID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if !isAdmin {
		return echo.NewHTTPError(http.StatusForbidden, "admin role required")
	}
	botID := strings.TrimSpace(c.Param("id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	var req bots.TransferBotRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	resp, err := h.botService.TransferOwner(c.Request().Context(), botID, req.OwnerUserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, "bot not found")
		}
		if errors.Is(err, bots.ErrOwnerUserNotFound) {
			return echo.NewHTTPError(http.StatusBadRequest, "owner user not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, scrubBotForResponse(resp))
}

// DeleteBot godoc
// @Summary Delete bot
// @Description Delete a bot user (owner/admin only)
// @Tags bots
// @Param id path string true "Bot ID"
// @Success 202 {object} map[string]string
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{id} [delete].
func (h *UsersHandler) DeleteBot(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), channelIdentityID, botID); err != nil {
		return err
	}
	var releaseRuntimeReset func()
	if h.runtimeResets != nil {
		var resetCtx context.Context
		resetCtx, releaseRuntimeReset, err = h.runtimeResets.BeginBotHistoryReset(c.Request().Context(), botID)
		if err != nil {
			return apperror.Wrap(apperror.CodeSessionHistoryInconsistent, err, nil)
		}
		defer releaseRuntimeReset()
		c.SetRequest(c.Request().WithContext(resetCtx))
	}
	if err := h.botService.Delete(c.Request().Context(), botID); err != nil {
		if leaseErr := runtimefence.ResetLeaseFailure(c.Request().Context(), err); leaseErr != nil {
			return apperror.Wrap(apperror.CodeSessionHistoryInconsistent, leaseErr, nil)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, "bot not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusAccepted, map[string]string{
		"id":     botID,
		"status": bots.BotStatusDeleting,
	})
}

// GetBotChannelConfig godoc
// @Summary Get bot channel config
// @Description Get bot channel configuration
// @Tags bots
// @Param id path string true "Bot ID"
// @Param platform path string true "Channel platform"
// @Success 200 {object} channel.ChannelConfig
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{id}/channel/{platform} [get].
func (h *UsersHandler) GetBotChannelConfig(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), channelIdentityID, botID); err != nil {
		return err
	}
	channelType, err := h.registry.ParseChannelType(c.Param("platform"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if h.channelStore == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "channel store not configured")
	}
	resp, err := h.channelStore.ResolveEffectiveConfig(c.Request().Context(), botID, channelType)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return echo.NewHTTPError(http.StatusNotFound, err.Error())
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, resp)
}

// UpsertBotChannelConfig godoc
// @Summary Update bot channel config
// @Description Update bot channel configuration
// @Tags bots
// @Param id path string true "Bot ID"
// @Param platform path string true "Channel platform"
// @Param payload body channel.UpsertConfigRequest true "Channel config payload"
// @Success 200 {object} channel.ChannelConfig
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 502 {object} ErrorResponse
// @Failure 503 {object} apperror.Problem
// @Failure 500 {object} ErrorResponse
// @Router /bots/{id}/channel/{platform} [put].
func (h *UsersHandler) UpsertBotChannelConfig(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), channelIdentityID, botID); err != nil {
		return err
	}
	channelType, err := h.registry.ParseChannelType(c.Param("platform"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	var req channel.UpsertConfigRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if req.Credentials == nil {
		req.Credentials = map[string]any{}
	}
	if h.channelRuntime == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "channel lifecycle not configured")
	}
	resp, err := h.channelRuntime.UpsertBotChannelConfig(c.Request().Context(), botID, channelType, req)
	if err != nil {
		if mapped := mapChannelRuntimeError(err); mapped != nil {
			return mapped
		}
		status := http.StatusInternalServerError
		if errors.Is(err, channel.ErrChannelDiscoveryFailed) {
			status = http.StatusBadGateway
		} else if errors.Is(err, channel.ErrEnableChannelFailed) {
			status = http.StatusBadRequest
		}
		return echo.NewHTTPError(status, err.Error())
	}
	return c.JSON(http.StatusOK, resp)
}

// UpdateBotChannelStatus godoc
// @Summary Update bot channel status
// @Description Update bot channel enabled/disabled status
// @Tags bots
// @Param id path string true "Bot ID"
// @Param platform path string true "Channel platform"
// @Param payload body channel.UpdateChannelStatusRequest true "Channel status payload"
// @Success 200 {object} channel.ChannelConfig
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 502 {object} ErrorResponse
// @Failure 503 {object} apperror.Problem
// @Failure 500 {object} ErrorResponse
// @Router /bots/{id}/channel/{platform}/status [patch].
func (h *UsersHandler) UpdateBotChannelStatus(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), channelIdentityID, botID); err != nil {
		return err
	}
	channelType, err := h.registry.ParseChannelType(c.Param("platform"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	var req channel.UpdateChannelStatusRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if h.channelRuntime == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "channel lifecycle not configured")
	}
	resp, err := h.channelRuntime.SetBotChannelStatus(c.Request().Context(), botID, channelType, req.Disabled)
	if err != nil {
		if mapped := mapChannelRuntimeError(err); mapped != nil {
			return mapped
		}
		if errors.Is(err, channel.ErrChannelConfigNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, err.Error())
		}
		status := http.StatusInternalServerError
		if errors.Is(err, channel.ErrChannelDiscoveryFailed) {
			status = http.StatusBadGateway
		} else if errors.Is(err, channel.ErrEnableChannelFailed) {
			status = http.StatusBadRequest
		}
		return echo.NewHTTPError(status, err.Error())
	}
	return c.JSON(http.StatusOK, resp)
}

// SetBotChannelWebhookEndpoint godoc
// @Summary Set bot channel webhook endpoint
// @Description Set the platform-side webhook endpoint for a bot channel.
// @Tags bots
// @Param id path string true "Bot ID"
// @Param platform path string true "Channel platform"
// @Param payload body channel.SetWebhookEndpointRequest true "Webhook endpoint payload"
// @Success 200 {object} channel.SetWebhookEndpointResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 502 {object} ErrorResponse
// @Failure 503 {object} apperror.Problem
// @Router /bots/{id}/channel/{platform}/webhook-endpoint [post].
func (h *UsersHandler) SetBotChannelWebhookEndpoint(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), channelIdentityID, botID); err != nil {
		return err
	}
	channelType, err := h.registry.ParseChannelType(c.Param("platform"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	var req channel.SetWebhookEndpointRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if h.channelRuntime == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "channel runtime not configured")
	}
	resp, err := h.channelRuntime.SetWebhookEndpoint(c.Request().Context(), botID, channelType, req)
	if err != nil {
		if mapped := mapChannelRuntimeError(err); mapped != nil {
			return mapped
		}
		switch {
		case errors.Is(err, channel.ErrChannelConfigNotFound):
			return echo.NewHTTPError(http.StatusNotFound, err.Error())
		case errors.Is(err, channel.ErrInvalidWebhookEndpoint), errors.Is(err, channel.ErrWebhookEndpointUnsupported):
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		default:
			return echo.NewHTTPError(http.StatusBadGateway, err.Error())
		}
	}
	return c.JSON(http.StatusOK, resp)
}

// DeleteBotChannelConfig godoc
// @Summary Delete bot channel config
// @Description Remove bot channel configuration
// @Tags bots
// @Param id path string true "Bot ID"
// @Param platform path string true "Channel platform"
// @Success 204 "No Content"
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} apperror.Problem
// @Router /bots/{id}/channel/{platform} [delete].
func (h *UsersHandler) DeleteBotChannelConfig(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), channelIdentityID, botID); err != nil {
		return err
	}
	channelType, err := h.registry.ParseChannelType(c.Param("platform"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if h.channelRuntime == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "channel lifecycle not configured")
	}
	if err := h.channelRuntime.DeleteBotChannelConfig(c.Request().Context(), botID, channelType); err != nil {
		if mapped := mapChannelRuntimeError(err); mapped != nil {
			return mapped
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.NoContent(http.StatusNoContent)
}

// SendBotMessage godoc
// @Summary Send message via bot channel
// @Description Send a message using bot channel configuration
// @Tags bots
// @Param id path string true "Bot ID"
// @Param platform path string true "Channel platform"
// @Param payload body channel.SendRequest true "Send payload"
// @Success 200 {object} map[string]string
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} apperror.Problem
// @Router /bots/{id}/channel/{platform}/send [post].
func (h *UsersHandler) SendBotMessage(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), channelIdentityID, botID); err != nil {
		return err
	}
	if h.channelRuntime == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "channel manager not configured")
	}
	channelType, err := h.registry.ParseChannelType(c.Param("platform"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	var req channel.SendRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if req.Message.IsEmpty() {
		return echo.NewHTTPError(http.StatusBadRequest, "message is required")
	}
	if err := h.channelRuntime.Send(c.Request().Context(), botID, channelType, req); err != nil {
		if mapped := mapChannelRuntimeError(err); mapped != nil {
			return mapped
		}
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// SendBotMessageSession godoc
// @Summary Send message via bot channel session token
// @Description Send a message using a session-scoped token (reply only)
// @Tags bots
// @Param id path string true "Bot ID"
// @Param platform path string true "Channel platform"
// @Param payload body channel.SendRequest true "Send payload"
// @Success 200 {object} map[string]string
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} apperror.Problem
// @Router /bots/{id}/channel/{platform}/send_chat [post].
func (h *UsersHandler) SendBotMessageSession(c echo.Context) error {
	chatToken, err := auth.ChatTokenFromContext(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if chatToken.BotID != botID {
		return echo.NewHTTPError(http.StatusForbidden, "token bot mismatch")
	}
	if h.channelRuntime == nil || h.routeService == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "services not configured")
	}
	route, err := h.routeService.GetByID(c.Request().Context(), chatToken.RouteID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "route not found")
	}
	if strings.TrimSpace(route.ReplyTarget) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "reply target missing in route")
	}
	channelType, err := h.registry.ParseChannelType(route.Platform)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	var req channel.SendRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if req.Message.IsEmpty() {
		return echo.NewHTTPError(http.StatusBadRequest, "message is required")
	}
	if err := h.channelRuntime.Send(c.Request().Context(), botID, channelType, channel.SendRequest{
		Target:  route.ReplyTarget,
		Message: req.Message,
	}); err != nil {
		if mapped := mapChannelRuntimeError(err); mapped != nil {
			return mapped
		}
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

func mapChannelRuntimeError(err error) error {
	if errors.Is(err, runtimeRpc.ErrUnavailable) {
		return apperror.Wrap(apperror.CodeChannelRuntimeUnavailable, err, nil)
	}
	return nil
}

func (h *UsersHandler) authorizeBotAccess(ctx context.Context, channelIdentityID, botID string) (bots.Bot, error) {
	return AuthorizeBotAccess(ctx, h.botService, h.service, channelIdentityID, botID)
}

// attachCurrentUserPermissions populates the requesting user's effective access
// permissions for a single bot.
func (h *UsersHandler) attachCurrentUserPermissions(ctx context.Context, channelIdentityID string, bot *bots.Bot) error {
	isAdmin, err := h.service.IsAdmin(ctx, channelIdentityID)
	if err != nil {
		return err
	}
	perms, err := h.botService.ResolveUserPermissions(ctx, bot.ID, channelIdentityID, isAdmin)
	if err != nil {
		return err
	}
	bot.CurrentUserPermissions = perms
	return nil
}

// attachCurrentUserPermissionsList populates effective permissions for a list of bots.
func (h *UsersHandler) attachCurrentUserPermissionsList(ctx context.Context, channelIdentityID string, items []bots.Bot) error {
	isAdmin, err := h.service.IsAdmin(ctx, channelIdentityID)
	if err != nil {
		return err
	}
	for i := range items {
		perms, err := h.botService.ResolveUserPermissions(ctx, items[i].ID, channelIdentityID, isAdmin)
		if err != nil {
			return err
		}
		items[i].CurrentUserPermissions = perms
	}
	return nil
}

func (*UsersHandler) requireChannelIdentityID(c echo.Context) (string, error) {
	return RequireChannelIdentityID(c)
}
