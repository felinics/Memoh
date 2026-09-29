package client

import (
	"fmt"
	"strings"

	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/errs"
)

func ValidateManagedACPConfig(profile acpprofile.Profile, setup acpprofile.AgentSetup, mode SetupMode) error {
	mode = normalizeSetupMode(mode)
	if mode == SetupModeSelf {
		return nil
	}
	values := setup.Managed
	for _, field := range profile.ManagedFields {
		if !field.Required {
			continue
		}
		if strings.TrimSpace(values[field.ID]) == "" {
			return errs.New(fmt.Sprintf("%s required for %s %s setup", field.ID, profile.DisplayName, mode))
		}
	}
	return nil
}
