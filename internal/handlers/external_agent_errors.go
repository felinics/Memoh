package handlers

import (
	"context"
	"errors"

	"github.com/labstack/echo/v4"

	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/bots"
)

// isBotOrSessionNotFound reports whether err is the shared authorization
// answer for a bot or session that does not exist. ACP and context lifecycle
// routes answer their own not-found code for it.
func isBotOrSessionNotFound(err error) bool {
	switch apperror.CodeOf(err) {
	case apperror.CodeBotNotFound, apperror.CodeSessionNotFound:
		return true
	}
	return false
}

func isHTTPStatus(err error, status int) bool {
	var httpErr *echo.HTTPError
	return errors.As(err, &httpErr) && httpErr.Code == status
}

// acpAgentSetupError reports why the bot cannot run the ACP agent: the agent
// is unknown, not enabled, or missing a field the managed setup requires.
// botAgentID names the instance whose setup applies; agents may be nil where
// instances are not wired, which leaves the bot's legacy profile slot.
func acpAgentSetupError(ctx context.Context, agents *botagents.Service, bot bots.Bot, botAgentID, agentID string) error {
	profile, ok := acpprofile.Lookup(agentID)
	if !ok {
		return apperror.New(apperror.CodeACPAgentNotFound, nil)
	}
	setup := acpprofile.ParseAgentSetup(bot.Metadata, agentID)
	if agents != nil {
		resolved, err := agents.ResolveACPSetup(ctx, bot.ID, botAgentID, agentID, bot.Metadata)
		if err != nil {
			if publicErr := botAgentHTTPError(err); publicErr != nil {
				return publicErr
			}
			return err
		}
		setup = resolved
	}
	if !setup.Enabled {
		return apperror.New(apperror.CodeACPAgentNotEnabled, nil)
	}
	if _, missing := acpprofile.MissingRequiredManagedFieldForPreflight(profile, setup); missing {
		return apperror.New(apperror.CodeACPAgentNotConfigured, nil)
	}
	return nil
}
