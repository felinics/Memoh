package agent

import (
	"strings"

	"github.com/memohai/memoh/domains/api/bot"
)

// AuthorizeACPRuntimeSessionAccess checks runtime-owner and workspace-exec permission.
func AuthorizeACPRuntimeSessionAccess(actorUserID string, perms []string, runtimeOwnerAccountID string) error {
	actorUserID = strings.TrimSpace(actorUserID)
	runtimeOwnerAccountID = strings.TrimSpace(runtimeOwnerAccountID)
	if runtimeOwnerAccountID == "" {
		return AcpFeedbackHTTPError(AcpRuntimeOwnerMissingFeedback())
	}
	if actorUserID == "" || actorUserID != runtimeOwnerAccountID {
		return AcpFeedbackHTTPError(AcpNoWorkspaceExecFeedback("runtime_owner_mismatch", "This ACP runtime belongs to another user."))
	}
	if !bot.HasPermission(perms, bot.PermissionWorkspaceExec) {
		return AcpFeedbackHTTPError(AcpNoWorkspaceExecFeedback("missing_workspace_exec", "You do not have permission to run workspace commands for this bot."))
	}
	return nil
}
