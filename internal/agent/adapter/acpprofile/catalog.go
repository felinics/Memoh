// Package acpprofile adapts the ACP runtime profile registry to the stable
// turn contract consumed by Channel.
package acpprofile

import (
	"context"

	runtimeprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/chat/thread"
)

// SetupResolver resolves the setup of the Agent instance an ACP thread runs
// as. *botagents.Service satisfies it.
type SetupResolver interface {
	ResolveACPSetup(ctx context.Context, botID, botAgentID, provider string, botMetadata map[string]any) (runtimeprofile.AgentSetup, error)
}

// Catalog exposes the channel-safe subset of the ACP runtime profile registry.
type Catalog struct {
	setups SetupResolver
}

var (
	_ turn.ACPProfileResolver  = (*Catalog)(nil)
	_ thread.ACPSetupValidator = (*Catalog)(nil)
)

func NewCatalog(setups SetupResolver) *Catalog {
	return &Catalog{setups: setups}
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

func (c *Catalog) ResolveACPSetupPreflight(ctx context.Context, botID, botAgentID, agentID string, metadata map[string]any) (turn.ACPSetupPreflight, error) {
	profile, ok := runtimeprofile.Lookup(agentID)
	if !ok {
		return turn.ACPSetupPreflight{}, nil
	}
	setup, err := c.setup(ctx, botID, botAgentID, profile.ID, metadata)
	if err != nil {
		return turn.ACPSetupPreflight{}, err
	}
	result := turn.ACPSetupPreflight{Enabled: setup.Enabled}
	if field, missing := runtimeprofile.MissingRequiredManagedFieldForPreflight(profile, setup); missing {
		result.MissingManagedField = &turn.ACPManagedField{
			ID:    field.ID,
			Label: field.Label,
		}
	}
	return result, nil
}

// KnownACPAgent reports whether agentID is a registered ACP profile. The
// built-in external agents left the ACP pool for their direct runtimes
// (migration 0144), so new ACP sessions for them are refused as unknown.
func (*Catalog) KnownACPAgent(agentID string) bool {
	_, ok := runtimeprofile.Lookup(agentID)
	return ok
}

func (c *Catalog) ValidateACPSetup(ctx context.Context, botID, botAgentID, agentID string, metadata map[string]any) (thread.ACPSetupValidation, error) {
	profile, ok := runtimeprofile.Lookup(agentID)
	if !ok {
		return thread.ACPSetupValidation{}, nil
	}
	setup, err := c.setup(ctx, botID, botAgentID, profile.ID, metadata)
	if err != nil {
		return thread.ACPSetupValidation{}, err
	}
	result := thread.ACPSetupValidation{Enabled: setup.Enabled}
	if field, missing := runtimeprofile.MissingRequiredManagedFieldForPreflight(profile, setup); missing {
		result.MissingManagedFieldID = field.ID
	}
	return result, nil
}

func (c *Catalog) setup(ctx context.Context, botID, botAgentID, provider string, metadata map[string]any) (runtimeprofile.AgentSetup, error) {
	if c == nil || c.setups == nil {
		return runtimeprofile.ParseAgentSetup(metadata, provider), nil
	}
	return c.setups.ResolveACPSetup(ctx, botID, botAgentID, provider, metadata)
}
