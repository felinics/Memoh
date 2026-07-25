package agent

import (
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/domains/api/bot"
)

// AuthorizeACPRuntimeSessionAccess checks runtime-owner and workspace-exec permission.
func AuthorizeACPRuntimeSessionAccess(actorUserID string, perms []string, runtimeOwnerAccountID string) error {
	actorUserID = strings.TrimSpace(actorUserID)
	runtimeOwnerAccountID = strings.TrimSpace(runtimeOwnerAccountID)
	if runtimeOwnerAccountID == "" {
		feedback := AcpRuntimeOwnerMissingFeedback()
		return echo.NewHTTPError(feedback.HTTPStatus, feedback)
	}
	if actorUserID == "" || actorUserID != runtimeOwnerAccountID {
		feedback := AcpNoWorkspaceExecFeedback("runtime_owner_mismatch", "This ACP runtime belongs to another user.")
		return echo.NewHTTPError(feedback.HTTPStatus, feedback)
	}
	if !bot.HasPermission(perms, bot.PermissionWorkspaceExec) {
		feedback := AcpNoWorkspaceExecFeedback("missing_workspace_exec", "You do not have permission to run workspace commands for this bot.")
		return echo.NewHTTPError(feedback.HTTPStatus, feedback)
	}
	return nil
}
