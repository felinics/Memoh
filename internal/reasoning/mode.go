package reasoning

// ThinkingMode describes how a model's extended-thinking control behaves. It is
// the capability-discovery output that the UI and wire layer key off of.
//
//   - toggle:        user can turn thinking on/off (most reasoning/hybrid models,
//     incl. OpenAI). "off" wire behavior is provider-specific (see adaptor).
//   - adaptive:      user can turn thinking on/off; when on, the provider uses
//     adaptive thinking (Claude 4.6+/4.7/4.8).
//   - only_adaptive: legacy alias for adaptive retained for branch-local imports.
//   - none:          model has no thinking concept.
//
// An empty value means "unknown" and is treated as a transitional state that falls
// back to the legacy reasoning compatibility flag (see ResolveMode).
const (
	ModeAdaptive     = "adaptive"
	ModeToggle       = "toggle"
	ModeOnlyAdaptive = "only_adaptive"
	ModeNone         = "none"
)

var validModes = map[string]struct{}{
	ModeAdaptive:     {},
	ModeToggle:       {},
	ModeOnlyAdaptive: {},
	ModeNone:         {},
}

// IsValidMode reports whether mode can be stored in a model config.
func IsValidMode(mode string) bool {
	_, ok := validModes[mode]
	return ok
}

// ResolveMode returns the effective thinking mode, bridging legacy data. The
// declared mode wins when it is known; only_adaptive collapses onto adaptive.
// When the mode is empty — a model imported before the thinking-mode schema
// existed — hasReasoningCompat decides: the old "reasoning" compatibility flag
// means toggle, its absence means none.
func ResolveMode(declared string, hasReasoningCompat bool) string {
	switch declared {
	case ModeToggle, ModeAdaptive, ModeNone:
		return declared
	case ModeOnlyAdaptive:
		return ModeAdaptive
	default:
		if hasReasoningCompat {
			return ModeToggle
		}
		return ModeNone
	}
}

// Supported reports whether a resolved mode allows any thinking at all.
func Supported(mode string) bool {
	return mode != ModeNone && mode != ""
}
