package handlers

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	acpfeedback "github.com/felinics/memoh/internal/agent/decision/feedback"
	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
)

func acpFeedbackHTTPError(err error) error {
	feedback := acpFeedbackError(err)
	if feedback == nil {
		return nil
	}
	return echo.NewHTTPError(feedback.HTTPStatus, feedback)
}

func acpFeedbackError(err error) *acpfeedback.Error {
	var feedback *acpfeedback.Error
	if errors.As(err, &feedback) {
		return feedback
	}
	var httpErr *echo.HTTPError
	if errors.As(err, &httpErr) {
		if feedback, ok := httpErr.Message.(*acpfeedback.Error); ok {
			return feedback
		}
	}
	return nil
}

func isHTTPStatus(err error, status int) bool {
	var httpErr *echo.HTTPError
	return errors.As(err, &httpErr) && httpErr.Code == status
}

func acpRuntimeOwnerMissingFeedback() *acpfeedback.Error {
	return acpfeedback.New(
		acpfeedback.CodeRuntimeOwnerMissing,
		"missing_runtime_owner",
		http.StatusConflict,
		"chat.acp.runtimeOwnerMissing",
		"ACP runtime owner is missing; recreate or reauthorize the ACP session",
		nil,
	)
}

func acpNoWorkspaceExecFeedback(reason, message string) *acpfeedback.Error {
	return acpfeedback.New(
		acpfeedback.CodeNoWorkspaceExec,
		reason,
		http.StatusForbidden,
		"chat.acp.noWorkspaceExec",
		message,
		nil,
	)
}

func acpAgentSetupHTTPError(metadata map[string]any, agentID string) error {
	profile, ok := acpprofile.Lookup(agentID)
	if !ok {
		feedback := acpfeedback.New(
			acpfeedback.CodeAgentNotFound,
			"unknown_agent",
			http.StatusBadRequest,
			"chat.acp.agentNotFound",
			"Unknown ACP agent",
			map[string]string{"agent_id": agentID},
		)
		return echo.NewHTTPError(feedback.HTTPStatus, feedback)
	}
	setup := acpprofile.ParseAgentSetup(metadata, agentID)
	if !setup.Enabled {
		feedback := acpfeedback.New(
			acpfeedback.CodeAgentNotEnabled,
			"agent_not_enabled",
			http.StatusForbidden,
			"chat.acp.agentNotEnabled",
			"ACP agent is not enabled for this bot",
			map[string]string{"agent_id": agentID},
		)
		return echo.NewHTTPError(feedback.HTTPStatus, feedback)
	}
	if field, missing := acpprofile.MissingRequiredManagedFieldForPreflight(profile, setup); missing {
		feedback := acpfeedback.New(
			acpfeedback.CodeAgentNotConfigured,
			"missing_managed_field",
			http.StatusBadRequest,
			"chat.acp.agentNotConfigured",
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

func acpAgentNotConfiguredFeedback(message string) *acpfeedback.Error {
	return acpfeedback.New(
		acpfeedback.CodeAgentNotConfigured,
		"agent_not_configured",
		http.StatusBadRequest,
		"chat.acp.agentNotConfigured",
		message,
		nil,
	)
}

func acpAgentNotFoundFeedback(message string) *acpfeedback.Error {
	return acpfeedback.New(
		acpfeedback.CodeAgentNotFound,
		"unknown_agent",
		http.StatusBadRequest,
		"chat.acp.agentNotFound",
		message,
		nil,
	)
}

func acpAgentNotEnabledFeedback(message string) *acpfeedback.Error {
	return acpfeedback.New(
		acpfeedback.CodeAgentNotEnabled,
		"agent_not_enabled",
		http.StatusForbidden,
		"chat.acp.agentNotEnabled",
		message,
		nil,
	)
}

func acpProjectModeInvalidFeedback(message string) *acpfeedback.Error {
	return acpfeedback.New(
		acpfeedback.CodeProjectModeInvalid,
		"invalid_project_mode",
		http.StatusBadRequest,
		"chat.acp.projectModeInvalid",
		message,
		nil,
	)
}
