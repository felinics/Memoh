package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/agent/application"
	"github.com/felinics/memoh/internal/agent/decision/approval"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
)

type RuntimeModeRequest struct {
	ModeID string `json:"mode_id"`
	// Omitted means permission; plan changes the independent planning mode.
	ModeKind string `json:"mode_kind,omitempty" enums:"permission,plan"`
}
type RuntimeCommandRequest struct {
	Command string `json:"command"`
}
type RuntimeCommandResponse struct {
	Text string `json:"text"`
}

func (h *SessionHandler) runtimeControlRequest(c echo.Context) (application.RuntimeControlRequest, error) {
	actor, err := RequireChannelIdentityID(c)
	if err != nil {
		return application.RuntimeControlRequest{}, err
	}
	request := application.RuntimeControlRequest{
		BotID:    strings.TrimSpace(c.Param("bot_id")),
		ThreadID: strings.TrimSpace(c.Param("session_id")),
		ActorID:  actor,
		Language: c.Request().Header.Get("Accept-Language"),
	}
	if _, _, _, err := h.authorizeSession(c, actor, request.BotID, request.ThreadID); err != nil {
		return request, err
	}
	if h.runtimeControls == nil {
		return request, external.ErrControlUnsupported
	}
	return request, nil
}

// GetRuntimeControls godoc
// @Summary Get session runtime controls
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Success 200 {object} external.Controls
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/runtime-controls [get].
func (h *SessionHandler) GetRuntimeControls(c echo.Context) error {
	request, err := h.runtimeControlRequest(c)
	if err != nil {
		return runtimeControlError(err)
	}
	controls, err := h.runtimeControls.RuntimeControls(c.Request().Context(), request)
	if err != nil {
		return runtimeControlError(err)
	}
	return c.JSON(http.StatusOK, controls)
}

// SetRuntimeMode godoc
// @Summary Set session runtime permission or planning mode
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Param payload body RuntimeModeRequest true "Permission mode"
// @Success 200 {object} external.ModeState
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/runtime-controls/mode [patch].
func (h *SessionHandler) SetRuntimeMode(c echo.Context) error {
	request, err := h.runtimeControlRequest(c)
	if err != nil {
		return runtimeControlError(err)
	}
	var body RuntimeModeRequest
	if err := c.Bind(&body); err != nil || strings.TrimSpace(body.ModeID) == "" {
		return apperror.New(apperror.CodeRuntimeControlRequestInvalid, nil)
	}
	request.ModeID = body.ModeID
	request.ModeKind = body.ModeKind
	result, err := h.runtimeControls.SetRuntimeMode(c.Request().Context(), request)
	if err != nil {
		return runtimeControlError(err)
	}
	return c.JSON(http.StatusOK, result)
}

// ExecuteRuntimeCommand godoc
// @Summary Execute a read or operation runtime command
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Param payload body RuntimeCommandRequest true "Runtime command"
// @Success 200 {object} RuntimeCommandResponse
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/runtime-controls/commands [post].
func (h *SessionHandler) ExecuteRuntimeCommand(c echo.Context) error {
	request, err := h.runtimeControlRequest(c)
	if err != nil {
		return runtimeControlError(err)
	}
	var body RuntimeCommandRequest
	if err := c.Bind(&body); err != nil || strings.TrimSpace(body.Command) == "" {
		return apperror.New(apperror.CodeRuntimeControlRequestInvalid, nil)
	}
	request.Command = body.Command
	result, err := h.runtimeControls.ExecuteRuntimeCommand(c.Request().Context(), request)
	if err != nil {
		return runtimeControlError(err)
	}
	return c.JSON(http.StatusOK, RuntimeCommandResponse{Text: result})
}

func runtimeControlError(err error) error {
	if apperror.CodeOf(err) != "" {
		return err
	}
	if feedback := externalAgentFeedbackHTTPError(err); feedback != nil {
		return feedback
	}
	var httpErr *echo.HTTPError
	if errors.As(err, &httpErr) && httpErr.Code < 500 {
		return err
	}
	code := apperror.CodeRuntimeControlFailed
	switch {
	case errors.Is(err, approval.ErrForbidden):
		code = apperror.CodeRuntimeControlForbidden
	case errors.Is(err, sessionruntime.ErrSessionBusy):
		code = apperror.CodeSessionBusy
	case errors.Is(err, external.ErrControlUnsupported):
		code = apperror.CodeRuntimeControlUnsupported
	case errors.Is(err, external.ErrCommandUnavailable):
		code = apperror.CodeRuntimeControlCommandUnavailable
	case errors.Is(err, external.ErrModeUnavailable):
		code = apperror.CodeRuntimeControlModeUnavailable
	case errors.Is(err, external.ErrThreadUnavailable):
		code = apperror.CodeRuntimeControlThreadUnavailable
	case errors.Is(err, external.ErrAuthRequired):
		code = apperror.CodeExternalRuntimeAuthRequired
	}
	return apperror.Wrap(code, err, nil)
}

type RuntimeGoalRequest struct {
	Action string `json:"action" enums:"pause,clear"`
}

// ControlRuntimeGoal godoc
// @Summary Pause or clear a runtime-owned goal
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Param payload body RuntimeGoalRequest true "Goal action"
// @Success 204
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/runtime-controls/goal [post].
func (h *SessionHandler) ControlRuntimeGoal(c echo.Context) error {
	request, err := h.runtimeControlRequest(c)
	if err != nil {
		return runtimeControlError(err)
	}
	var body RuntimeGoalRequest
	if err := c.Bind(&body); err != nil {
		return apperror.New(apperror.CodeRuntimeControlRequestInvalid, nil)
	}
	service, ok := h.runtimeControls.(turn.RuntimeGoalService)
	if !ok {
		return runtimeControlError(external.ErrControlUnsupported)
	}
	if err := service.ControlRuntimeGoal(c.Request().Context(), request, body.Action); err != nil {
		return runtimeControlError(err)
	}
	return c.NoContent(http.StatusNoContent)
}

// RuntimeGoalResponse keeps an absent goal distinct from a failed query.
type RuntimeGoalResponse struct {
	Goal *external.Goal `json:"goal"`
}

// GetRuntimeGoal godoc
// @Summary Get the runtime-owned goal
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Success 200 {object} RuntimeGoalResponse
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/runtime-controls/goal [get].
func (h *SessionHandler) GetRuntimeGoal(c echo.Context) error {
	request, err := h.runtimeControlRequest(c)
	if err != nil {
		return runtimeControlError(err)
	}
	service, ok := h.runtimeControls.(turn.RuntimeGoalService)
	if !ok {
		return runtimeControlError(external.ErrControlUnsupported)
	}
	goal, err := service.RuntimeGoal(c.Request().Context(), request)
	if err != nil {
		return runtimeControlError(err)
	}
	return c.JSON(http.StatusOK, RuntimeGoalResponse{Goal: goal})
}
