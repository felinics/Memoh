package handlers

import (
	"errors"

	"github.com/labstack/echo/v4"

	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/apperror"
)

func isHTTPStatus(err error, status int) bool {
	var httpErr *echo.HTTPError
	return errors.As(err, &httpErr) && httpErr.Code == status
}

// acpAgentSetupError reports why the bot cannot run the ACP agent: the agent
// is unknown, not enabled, or missing a field the managed setup requires.
func acpAgentSetupError(metadata map[string]any, agentID string) error {
	profile, ok := acpprofile.Lookup(agentID)
	if !ok {
		return apperror.New(apperror.CodeACPAgentNotFound, nil)
	}
	setup := acpprofile.ParseAgentSetup(metadata, agentID)
	if !setup.Enabled {
		return apperror.New(apperror.CodeACPAgentNotEnabled, nil)
	}
	if _, missing := acpprofile.MissingRequiredManagedFieldForPreflight(profile, setup); missing {
		return apperror.New(apperror.CodeACPAgentNotConfigured, nil)
	}
	return nil
}
