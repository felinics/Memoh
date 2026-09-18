package application

import "github.com/felinics/memoh/internal/agent/runtime/native"

// ResolveRunConfigResult holds a fully resolved run configuration for one
// agent request, together with the selected model and session runtime.
// Produced by application.Service.ResolveRunConfig and consumed by the turn
// runtime adapters.
type ResolveRunConfigResult struct {
	RunConfig              native.RunConfig
	ModelID                string // database UUID of the selected model
	RuntimeType            string
	ContextBudgetMaxTokens int
	// DiscussProbeModelID is the bot's configured probe model, carried from the
	// settings read the base builder already performed. Empty means the bot has
	// no override; the gate then falls back to the owner's title model.
	DiscussProbeModelID string
}
