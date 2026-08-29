// Package acpprofile adapts the ACP runtime profile registry to the stable
// turn contract consumed by Channel.
package acpprofile

import (
	runtimeprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/chat/thread"
)

// Catalog exposes the channel-safe subset of the ACP runtime profile registry.
type Catalog struct{}

var (
	_ turn.ACPProfileResolver  = (*Catalog)(nil)
	_ thread.ACPSetupValidator = (*Catalog)(nil)
)

func NewCatalog() *Catalog {
	return &Catalog{}
}

func (*Catalog) ResolveACPProfile(agentID string) turn.ACPAgentProfile {
	normalized := runtimeprofile.NormalizeAgentID(agentID)
	profile, ok := runtimeprofile.Lookup(normalized)
	if !ok {
		return turn.ACPAgentProfile{ID: normalized}
	}
	return turn.ACPAgentProfile{
		ID:          profile.ID,
		DisplayName: profile.DisplayName,
		Known:       true,
	}
}

func (*Catalog) ResolveACPSetupPreflight(agentID string, metadata map[string]any) turn.ACPSetupPreflight {
	profile, ok := runtimeprofile.Lookup(agentID)
	if !ok {
		return turn.ACPSetupPreflight{}
	}
	setup := runtimeprofile.ParseAgentSetup(metadata, profile.ID)
	result := turn.ACPSetupPreflight{Enabled: setup.Enabled}
	if field, missing := runtimeprofile.MissingRequiredManagedFieldForPreflight(profile, setup); missing {
		result.MissingManagedField = &turn.ACPManagedField{
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
