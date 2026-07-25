package agent

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	acpprofile "github.com/memohai/memoh/domains/agent/acp/profile"
	acpfeedback "github.com/memohai/memoh/domains/agent/decision/feedback"
)

func AcpFeedbackHTTPError(err error) error {
	feedback := AcpFeedbackError(err)
	if feedback == nil {
		return nil
	}
	return echo.NewHTTPError(feedback.HTTPStatus, feedback)
}

func AcpFeedbackError(err error) *acpfeedback.Error {
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

func IsHTTPStatus(err error, status int) bool {
	var httpErr *echo.HTTPError
	return errors.As(err, &httpErr) && httpErr.Code == status
}

func AcpRuntimeOwnerMissingFeedback() *acpfeedback.Error {
	return acpfeedback.New(
		acpfeedback.CodeRuntimeOwnerMissing,
		"missing_runtime_owner",
		http.StatusConflict,
		"chat.acp.runtimeOwnerMissing",
		"ACP runtime owner is missing; recreate or reauthorize the ACP session",
		nil,
	)
}

func AcpNoWorkspaceExecFeedback(reason, message string) *acpfeedback.Error {
	return acpfeedback.New(
		acpfeedback.CodeNoWorkspaceExec,
		reason,
		http.StatusForbidden,
		"chat.acp.noWorkspaceExec",
		message,
		nil,
	)
}

func acpRuntimeStartFailedFeedback(_ string) *acpfeedback.Error {
	message := "ACP runtime failed to start. Check the agent configuration and workspace runtime, then retry."
	return acpfeedback.New(
		acpfeedback.CodeRuntimeStartFailed,
		"runtime_start_failed",
		http.StatusInternalServerError,
		"chat.acp.runtimeStartFailed",
		message,
		nil,
	)
}

func AcpAgentSetupHTTPError(metadata map[string]any, agentID string) error {
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

func AcpAgentNotConfiguredFeedback(message string) *acpfeedback.Error {
	return acpfeedback.New(
		acpfeedback.CodeAgentNotConfigured,
		"agent_not_configured",
		http.StatusBadRequest,
		"chat.acp.agentNotConfigured",
		message,
		nil,
	)
}

func AcpAgentNotFoundFeedback(message string) *acpfeedback.Error {
	return acpfeedback.New(
		acpfeedback.CodeAgentNotFound,
		"unknown_agent",
		http.StatusBadRequest,
		"chat.acp.agentNotFound",
		message,
		nil,
	)
}

func AcpAgentNotEnabledFeedback(message string) *acpfeedback.Error {
	return acpfeedback.New(
		acpfeedback.CodeAgentNotEnabled,
		"agent_not_enabled",
		http.StatusForbidden,
		"chat.acp.agentNotEnabled",
		message,
		nil,
	)
}

func AcpProjectModeInvalidFeedback(message string) *acpfeedback.Error {
	return acpfeedback.New(
		acpfeedback.CodeProjectModeInvalid,
		"invalid_project_mode",
		http.StatusBadRequest,
		"chat.acp.projectModeInvalid",
		message,
		nil,
	)
}
