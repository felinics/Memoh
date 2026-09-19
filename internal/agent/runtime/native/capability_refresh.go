package native

import (
	"errors"
	"strings"

	sdk "github.com/felinics/twilight/sdk"
)

var errCapabilitiesChanged = errors.New("agent capabilities changed after committed step")

// The SDK snapshots executable tools when an invocation starts. Continue from
// the committed transcript so discovery, execution and context accounting use
// the same new tools; a PrepareStep-only schema change would leave old handlers.
func capabilityContinuation(cfg RunConfig, messages []sdk.Message, steps int) RunConfig {
	cfg = appendSteerContinuation(cfg, messages, steps)
	cfg.capabilityRefreshCount++
	if cfg.ContextToolUsage != "" {
		cfg.System = strings.Replace(cfg.System, cfg.ContextToolUsage, "", 1)
	}
	cfg.ContextToolUsage = ""
	cfg.ContextToolUsageFrags = nil
	cfg.ContextToolDefs = nil
	return cfg
}
