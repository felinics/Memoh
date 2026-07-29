package application

import "github.com/memohai/memoh/internal/agent/runtime/native"

// ResolveRunConfigResult holds a fully resolved run configuration for one
// agent request, together with the selected model and session runtime.
// Produced by application.Service.ResolveRunConfig and consumed by the turn
// runtime adapters.
type ResolveRunConfigResult struct {
	RunConfig                 native.RunConfig
	ModelID                   string // database UUID of the selected model
	ContextTokenBudget        int    // configured context window of the selected model
	DiscussMessageTokenBudget int    // pre-send cap for composed discuss messages
	RuntimeType               string
}
