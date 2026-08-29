package client

import (
	"context"
	"errors"
	"fmt"
	"strings"

	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

type ManagedACPConfigRequest struct {
	Profile  acpprofile.Profile
	Setup    acpprofile.AgentSetup
	Mode     SetupMode
	Resolved ResolvedSessionContext
}

type ManagedACPConfigClientGetter func() (*bridge.Client, error)

func ValidateManagedACPConfig(profile acpprofile.Profile, setup acpprofile.AgentSetup, mode SetupMode) error {
	mode = normalizeSetupMode(mode)
	if mode == SetupModeSelf {
		return nil
	}
	values := setup.Managed
	switch profile.ID {
	case acpprofile.AgentCodexID:
		if mode == SetupModeOAuth {
			return nil
		}
		if strings.TrimSpace(values["api_key"]) == "" {
			return fmt.Errorf("api_key required for %s api_key setup", profile.DisplayName)
		}
		return nil
	case acpprofile.AgentClaudeCodeID:
		if mode == SetupModeOAuth {
			if strings.TrimSpace(values["oauth_token"]) == "" {
				return fmt.Errorf("oauth_token required for %s oauth setup", profile.DisplayName)
			}
			return nil
		}
		if strings.TrimSpace(values["api_key"]) == "" {
			return fmt.Errorf("api_key required for %s api_key setup", profile.DisplayName)
		}
		return nil
	case acpprofile.AgentHermesID:
		return ValidateHermesManagedConfig(values)
	}
	for _, field := range profile.ManagedFields {
		if !field.Required {
			continue
		}
		if strings.TrimSpace(values[field.ID]) == "" {
			return fmt.Errorf("%s required for %s %s setup", field.ID, profile.DisplayName, mode)
		}
	}
	return nil
}

func WriteManagedACPConfig(ctx context.Context, req ManagedACPConfigRequest, getClient ManagedACPConfigClientGetter) error {
	mode := normalizeSetupMode(req.Mode)
	if mode == SetupModeSelf {
		return nil
	}

	switch req.Profile.ID {
	case acpprofile.AgentCodexID:
		client, err := requireManagedACPClient(getClient)
		if err != nil {
			return err
		}
		cfg := CodexManagedConfig{
			Mode:    mode,
			Managed: req.Setup.Managed,
		}
		if mode == SetupModeOAuth {
			return WriteCodexManagedConfigFile(ctx, client, cfg)
		}
		return WriteCodexManagedConfigWithAuth(ctx, client, cfg)
	case acpprofile.AgentHermesID:
		client, err := requireManagedACPClient(getClient)
		if err != nil {
			return err
		}
		return WriteHermesManagedConfig(ctx, client, HermesManagedConfig{
			Managed: req.Setup.Managed,
			Home:    req.Resolved.HermesHome,
		})
	default:
		return nil
	}
}

func requireManagedACPClient(getClient ManagedACPConfigClientGetter) (*bridge.Client, error) {
	if getClient == nil {
		return nil, errors.New("workspace bridge client getter is required")
	}
	return getClient()
}
