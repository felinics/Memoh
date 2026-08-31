package handlers

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	agentfeedback "github.com/felinics/memoh/internal/agent/decision/feedback"
	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
)

func acpFeedbackHTTPError(err error) error {
	feedback := acpFeedbackError(err)
	if feedback == nil {
		return nil
	}
	return echo.NewHTTPError(feedback.HTTPStatus, feedback)
}

func acpFeedbackError(err error) *agentfeedback.Error {
	var feedback *agentfeedback.Error
	if errors.As(err, &feedback) {
		return feedback
	}
	var httpErr *echo.HTTPError
	if errors.As(err, &httpErr) {
		if feedback, ok := httpErr.Message.(*agentfeedback.Error); ok {
			return feedback
		}
	}
	return nil
}

func isHTTPStatus(err error, status int) bool {
	var httpErr *echo.HTTPError
	return errors.As(err, &httpErr) && httpErr.Code == status
}

func externalAgentRuntimeOwnerMissingFeedback() *agentfeedback.Error {
	return agentfeedback.New(
		agentfeedback.CodeRuntimeOwnerMissing,
		"missing_runtime_owner",
		http.StatusConflict,
		"chat.externalAgent.runtimeOwnerMissing",
		"External Agent runtime owner is missing; start a new External Agent session",
		nil,
	)
}

func externalAgentNoWorkspaceExecFeedback(reason, message string) *agentfeedback.Error {
	return agentfeedback.New(
		agentfeedback.CodeNoWorkspaceExec,
		reason,
		http.StatusForbidden,
		"chat.externalAgent.noWorkspaceExec",
		message,
		nil,
	)
}

func acpAgentSetupHTTPError(metadata map[string]any, agentID string) error {
	profile, ok := acpprofile.Lookup(agentID)
	if !ok {
		feedback := agentfeedback.New(
			agentfeedback.CodeAgentNotFound,
			"unknown_agent",
			http.StatusBadRequest,
			"chat.externalAgent.agentNotFound",
			"Unknown ACP agent",
			map[string]string{"agent_id": agentID},
		)
		return echo.NewHTTPError(feedback.HTTPStatus, feedback)
	}
	setup := acpprofile.ParseAgentSetup(metadata, agentID)
	if !setup.Enabled {
		feedback := agentfeedback.New(
			agentfeedback.CodeAgentNotEnabled,
			"agent_not_enabled",
			http.StatusForbidden,
			"chat.externalAgent.agentNotEnabled",
			"ACP agent is not enabled for this bot",
			map[string]string{"agent_id": agentID},
		)
		return echo.NewHTTPError(feedback.HTTPStatus, feedback)
	}
	if field, missing := acpprofile.MissingRequiredManagedFieldForPreflight(profile, setup); missing {
		feedback := agentfeedback.New(
			agentfeedback.CodeAgentNotConfigured,
			"missing_managed_field",
			http.StatusBadRequest,
			"chat.externalAgent.agentNotConfigured",
			"ACP agent is missing required configuration",
			map[string]string{
				"agent_id": agentID,
				"field":    field.ID,
			},
		)
		return echo.NewHTTPError(feedback.HTTPStatus, feedback)
	}
	return nil
}

func acpAgentNotConfiguredFeedback(message string) *agentfeedback.Error {
	return agentfeedback.New(
		agentfeedback.CodeAgentNotConfigured,
		"agent_not_configured",
		http.StatusBadRequest,
		"chat.externalAgent.agentNotConfigured",
		message,
		nil,
	)
}

func acpAgentNotFoundFeedback(message string) *agentfeedback.Error {
	return agentfeedback.New(
		agentfeedback.CodeAgentNotFound,
		"unknown_agent",
		http.StatusBadRequest,
		"chat.externalAgent.agentNotFound",
		message,
		nil,
	)
}

func acpAgentNotEnabledFeedback(message string) *agentfeedback.Error {
	return agentfeedback.New(
		agentfeedback.CodeAgentNotEnabled,
		"agent_not_enabled",
		http.StatusForbidden,
		"chat.externalAgent.agentNotEnabled",
		message,
		nil,
	)
}

func acpProjectModeInvalidFeedback(message string) *agentfeedback.Error {
	return agentfeedback.New(
		agentfeedback.CodeProjectModeInvalid,
		"invalid_project_mode",
		http.StatusBadRequest,
		"chat.externalAgent.projectModeInvalid",
		message,
		nil,
	)
}
