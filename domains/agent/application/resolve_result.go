package application

import "github.com/memohai/memoh/domains/agent/engine"

// ResolveRunConfigResult holds a fully resolved run configuration for one
// agent request, together with the selected model and session runtime.
// Produced by application.Service.ResolveRunConfig and consumed by the turn
// runtime adapters.
type ResolveRunConfigResult struct {
	RunConfig   engine.RunConfig
	ModelID     string // database UUID of the selected model
	RuntimeType string
}
