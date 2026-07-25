// Package acpprofile adapts the ACP runtime profile registry to the stable
// turn contract consumed by Channel.
package profile

import (
	agentdomain "github.com/memohai/memoh/domains/agent"
	runtimeprofile "github.com/memohai/memoh/domains/agent/acp/profile"
	"github.com/memohai/memoh/domains/agent/chat/thread"
)

// Catalog exposes the channel-safe subset of the ACP runtime profile registry.
type Catalog struct{}

var (
	_ agentdomain.ACPProfileResolver = (*Catalog)(nil)
	_ thread.ACPSetupValidator       = (*Catalog)(nil)
)

func NewCatalog() *Catalog {
	return &Catalog{}
}

func (*Catalog) ResolveACPProfile(agentID string) agentdomain.ACPAgentProfile {
	normalized := runtimeprofile.NormalizeAgentID(agentID)
	profile, ok := runtimeprofile.Lookup(normalized)
	if !ok {
		return agentdomain.ACPAgentProfile{ID: normalized}
	}
	return agentdomain.ACPAgentProfile{
		ID:          profile.ID,
		DisplayName: profile.DisplayName,
		Known:       true,
	}
}

func (*Catalog) ResolveACPSetupPreflight(agentID string, metadata map[string]any) agentdomain.ACPSetupPreflight {
	profile, ok := runtimeprofile.Lookup(agentID)
	if !ok {
		return agentdomain.ACPSetupPreflight{}
	}
	setup := runtimeprofile.ParseAgentSetup(metadata, profile.ID)
	result := agentdomain.ACPSetupPreflight{Enabled: setup.Enabled}
	if field, missing := runtimeprofile.MissingRequiredManagedFieldForPreflight(profile, setup); missing {
		result.MissingManagedField = &agentdomain.ACPManagedField{
			ID:    field.ID,
			Label: field.Label,
		}
	}
	return result
}

func (*Catalog) ValidateACPSetup(agentID string, metadata map[string]any) thread.ACPSetupValidation {
	profile, ok := runtimeprofile.Lookup(agentID)
	if !ok {
		return thread.ACPSetupValidation{}
	}
	setup := runtimeprofile.ParseAgentSetup(metadata, profile.ID)
	result := thread.ACPSetupValidation{
		Known:   true,
		Enabled: setup.Enabled,
	}
	if field, missing := runtimeprofile.MissingRequiredManagedFieldForPreflight(profile, setup); missing {
		result.MissingManagedFieldID = field.ID
	}
	return result
}
