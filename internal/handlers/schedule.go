package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/httpx"
	"github.com/felinics/memoh/internal/schedule"
	"github.com/felinics/memoh/internal/workdir"
)

type ScheduleHandler struct {
	service        *schedule.Service
	botService     *bots.Service
	accountService *accounts.Service
	logger         *slog.Logger
}

func NewScheduleHandler(log *slog.Logger, service *schedule.Service, botService *bots.Service, accountService *accounts.Service) *ScheduleHandler {
	return &ScheduleHandler{
		service:        service,
		botService:     botService,
		accountService: accountService,
		logger:         log.With(slog.String("handler", "schedule")),
	}
}

func (h *ScheduleHandler) Register(e *echo.Echo) {
	group := e.Group("/bots/:bot_id/schedule")
	group.POST("", h.Create)
	group.GET("", h.List)
	group.GET("/logs", h.ListLogs)
	group.DELETE("/logs", h.DeleteLogs)
	group.GET("/:id", h.Get)
	group.GET("/:id/logs", h.ListLogsBySchedule)
	group.PUT("/:id", h.Update)
	group.DELETE("/:id", h.Delete)
}

// Create godoc
// @Summary Create schedule
// @Description Create a schedule for current user
// @Tags schedule
// @Param bot_id path string true "Bot ID"
// @Param payload body schedule.CreateRequest true "Schedule payload"
// @Success 201 {object} schedule.Schedule
// @Failure 400 {object} server.Problem
// @Failure 500 {object} server.Problem
// @Router /bots/{bot_id}/schedule [post].
func (h *ScheduleHandler) Create(c echo.Context) error {
	userID, err := h.requireUserID(c)
	if err != nil {
		return err
	}
	botID, err := httpx.RequiredParam(c, "bot_id")
	if err != nil {
		return err
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), userID, botID); err != nil {
		return err
	}
	var req schedule.CreateRequest
	if err := c.Bind(&req); err != nil {
		return err
	}
	resp, err := h.service.Create(c.Request().Context(), botID, req)
	if err != nil {
		return scheduleServiceError(err)
	}
	return c.JSON(http.StatusCreated, resp)
}

// List godoc
// @Summary List schedules
// @Description List schedules for current user
// @Tags schedule
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} schedule.ListResponse
// @Failure 400 {object} server.Problem
// @Failure 500 {object} server.Problem
// @Router /bots/{bot_id}/schedule [get].
func (h *ScheduleHandler) List(c echo.Context) error {
	userID, err := h.requireUserID(c)
	if err != nil {
		return err
	}
	botID, err := httpx.RequiredParam(c, "bot_id")
	if err != nil {
		return err
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), userID, botID); err != nil {
		return err
	}
	items, err := h.service.List(c.Request().Context(), botID)
	if err != nil {
		return errs.Wrap(err, "schedule request")
	}
	return c.JSON(http.StatusOK, schedule.ListResponse{Items: items})
}

// Get godoc
// @Summary Get schedule
// @Description Get a schedule by ID
// @Tags schedule
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Schedule ID"
// @Success 200 {object} schedule.Schedule
// @Failure 400 {object} server.Problem
// @Failure 404 {object} server.Problem
// @Failure 500 {object} server.Problem
// @Router /bots/{bot_id}/schedule/{id} [get].
func (h *ScheduleHandler) Get(c echo.Context) error {
	userID, err := h.requireUserID(c)
	if err != nil {
		return err
	}
	botID, err := httpx.RequiredParam(c, "bot_id")
	if err != nil {
		return err
	}
	id, err := httpx.RequiredParam(c, "id")
	if err != nil {
		return err
	}
	item, err := h.service.Get(c.Request().Context(), id)
	if err != nil {
		return scheduleLookupError(err)
	}
	if item.BotID != botID {
		return apperror.New(apperror.CodeHTTPForbidden, nil)
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), userID, botID); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, item)
}

// Update godoc
// @Summary Update schedule
// @Description Update a schedule by ID
// @Tags schedule
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Schedule ID"
// @Param payload body schedule.UpdateRequest true "Schedule payload"
// @Success 200 {object} schedule.Schedule
// @Failure 400 {object} server.Problem
// @Failure 500 {object} server.Problem
// @Router /bots/{bot_id}/schedule/{id} [put].
func (h *ScheduleHandler) Update(c echo.Context) error {
	userID, err := h.requireUserID(c)
	if err != nil {
		return err
	}
	botID, err := httpx.RequiredParam(c, "bot_id")
	if err != nil {
		return err
	}
	id, err := httpx.RequiredParam(c, "id")
	if err != nil {
		return err
	}
	var req schedule.UpdateRequest
	if err := c.Bind(&req); err != nil {
		return err
	}
	item, err := h.service.Get(c.Request().Context(), id)
	if err != nil {
		return scheduleLookupError(err)
	}
	if item.BotID != botID {
		return apperror.New(apperror.CodeHTTPForbidden, nil)
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), userID, botID); err != nil {
		return err
	}
	resp, err := h.service.Update(c.Request().Context(), id, req)
	if err != nil {
		return scheduleServiceError(err)
	}
	return c.JSON(http.StatusOK, resp)
}

// Delete godoc
// @Summary Delete schedule
// @Description Delete a schedule by ID
// @Tags schedule
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Schedule ID"
// @Success 204 "No Content"
// @Failure 400 {object} server.Problem
// @Failure 500 {object} server.Problem
// @Router /bots/{bot_id}/schedule/{id} [delete].
func (h *ScheduleHandler) Delete(c echo.Context) error {
	userID, err := h.requireUserID(c)
	if err != nil {
		return err
	}
	botID, err := httpx.RequiredParam(c, "bot_id")
	if err != nil {
		return err
	}
	id, err := httpx.RequiredParam(c, "id")
	if err != nil {
		return err
	}
	item, err := h.service.Get(c.Request().Context(), id)
	if err != nil {
		return scheduleLookupError(err)
	}
	if item.BotID != botID {
		return apperror.New(apperror.CodeHTTPForbidden, nil)
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), userID, botID); err != nil {
		return err
	}
	if err := h.service.Delete(c.Request().Context(), id); err != nil {
		return errs.Wrap(err, "schedule request")
	}
	return c.NoContent(http.StatusNoContent)
}

// ListLogs godoc
// @Summary List schedule logs
// @Description List schedule execution logs for a bot
// @Tags schedule
// @Param bot_id path string true "Bot ID"
// @Param limit query int false "Limit" default(50)
// @Param offset query int false "Offset" default(0)
// @Success 200 {object} schedule.ListLogsResponse
// @Failure 400 {object} server.Problem
// @Failure 500 {object} server.Problem
// @Router /bots/{bot_id}/schedule/logs [get].
func (h *ScheduleHandler) ListLogs(c echo.Context) error {
	userID, err := h.requireUserID(c)
	if err != nil {
		return err
	}
	botID, err := httpx.RequiredParam(c, "bot_id")
	if err != nil {
		return err
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), userID, botID); err != nil {
		return err
	}

	limit, offset := parseOffsetLimit(c)
	items, total, err := h.service.ListLogs(c.Request().Context(), botID, limit, offset)
	if err != nil {
		return errs.Wrap(err, "schedule request")
	}
	return c.JSON(http.StatusOK, schedule.ListLogsResponse{Items: items, TotalCount: total})
}

// ListLogsBySchedule godoc
// @Summary List schedule logs by schedule
// @Description List execution logs for a specific schedule
// @Tags schedule
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Schedule ID"
// @Param limit query int false "Limit" default(50)
// @Param offset query int false "Offset" default(0)
// @Success 200 {object} schedule.ListLogsResponse
// @Failure 400 {object} server.Problem
// @Failure 500 {object} server.Problem
// @Router /bots/{bot_id}/schedule/{id}/logs [get].
func (h *ScheduleHandler) ListLogsBySchedule(c echo.Context) error {
	userID, err := h.requireUserID(c)
	if err != nil {
		return err
	}
	botID, err := httpx.RequiredParam(c, "bot_id")
	if err != nil {
		return err
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), userID, botID); err != nil {
		return err
	}
	scheduleID, err := httpx.RequiredParam(c, "id")
	if err != nil {
		return err
	}

	limit, offset := parseOffsetLimit(c)
	items, total, err := h.service.ListLogsBySchedule(c.Request().Context(), scheduleID, limit, offset)
	if err != nil {
		return errs.Wrap(err, "schedule request")
	}
	return c.JSON(http.StatusOK, schedule.ListLogsResponse{Items: items, TotalCount: total})
}

// DeleteLogs godoc
// @Summary Delete schedule logs
// @Description Delete all schedule execution logs for a bot
// @Tags schedule
// @Param bot_id path string true "Bot ID"
// @Success 204 "No Content"
// @Failure 400 {object} server.Problem
// @Failure 500 {object} server.Problem
// @Router /bots/{bot_id}/schedule/logs [delete].
func (h *ScheduleHandler) DeleteLogs(c echo.Context) error {
	userID, err := h.requireUserID(c)
	if err != nil {
		return err
	}
	botID, err := httpx.RequiredParam(c, "bot_id")
	if err != nil {
		return err
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), userID, botID); err != nil {
		return err
	}
	if err := h.service.DeleteLogs(c.Request().Context(), botID); err != nil {
		return errs.Wrap(err, "schedule request")
	}
	return c.NoContent(http.StatusNoContent)
}

func (*ScheduleHandler) requireUserID(c echo.Context) (string, error) {
	return RequireChannelIdentityID(c)
}

func (h *ScheduleHandler) authorizeBotAccess(ctx context.Context, userID, botID string) (bots.Bot, error) {
	return AuthorizeBotAccess(ctx, h.botService, h.accountService, userID, botID)
}

// scheduleLookupError answers a failed schedule lookup: an unknown ID is a
// 404, a malformed one a field problem, anything else is internal.
func scheduleLookupError(err error) error {
	if errors.Is(err, schedule.ErrScheduleNotFound) {
		return apperror.Wrap(apperror.CodeScheduleNotFound, err, nil)
	}
	return scheduleServiceError(err)
}

// scheduleServiceError maps schedule domain errors onto HTTP status codes:
// user-correctable validation failures answer 400, everything else stays 500.
func scheduleServiceError(err error) error {
	if errors.Is(err, schedule.ErrExecutionTimeout) {
		return apperror.Wrap(apperror.CodeScheduleExecutionTimeout, err, nil)
	}
	if botAgentErr := botAgentHTTPError(err); botAgentErr != nil {
		return botAgentErr
	}
	var invalid schedule.InvalidRequestError
	if errors.As(err, &invalid) {
		if code, ok := scheduleRuleCodes[invalid.Rule()]; ok {
			return apperror.Wrap(code, err, map[string]string{"field": invalid.Field()})
		}
		if invalid.Required() {
			return apperror.FieldRequired(invalid.Field())
		}
		return apperror.FieldInvalid(invalid.Field(), err)
	}
	if errors.Is(err, workdir.ErrWorkdirNotFound) || errors.Is(err, workdir.ErrWorkdirArchived) {
		return apperror.FieldInvalid("workdir_id", err)
	}
	return errs.Wrap(err, "schedule request")
}

// scheduleRuleCodes maps a broken schedule validation rule to its public code.
var scheduleRuleCodes = map[schedule.Rule]apperror.Code{
	schedule.RuleRunTargetConflict:      apperror.CodeScheduleRunTargetConflict,
	schedule.RuleModelConflict:          apperror.CodeScheduleModelConflict,
	schedule.RuleModelUnusable:          apperror.CodeScheduleModelUnusable,
	schedule.RuleModelRequired:          apperror.CodeScheduleModelRequired,
	schedule.RuleSessionModeUnsupported: apperror.CodeScheduleSessionModeUnsupported,
}
