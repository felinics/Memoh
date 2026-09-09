package reasoning

import (
	"regexp"
	"strconv"
	"strings"
)

// ThinkingMode describes how a model's extended-thinking control behaves. It is
// the capability-discovery output that the UI and wire layer key off of.
//
//   - toggle:        user can turn thinking on/off (most reasoning/hybrid models,
//     incl. OpenAI). "off" wire behavior is provider-specific (see adaptor).
//   - adaptive:      user can turn thinking on/off; when on, the provider uses
//     adaptive thinking (Claude 4.6+/4.7/4.8).
//   - always:        the model reasons and exposes no control at all — no tiers,
//     no off switch (deepseek-reasoner, MiniMax M2.x). Distinct from toggle: a
//     toggle model with an empty tier list can still be turned off, while this
//     one cannot be influenced in any way.
//   - only_adaptive: legacy alias for adaptive retained for branch-local imports.
//   - none:          model has no thinking concept.
//
// An empty value means "unknown" and is treated as a transitional state that falls
// back to the legacy reasoning compatibility flag (see ResolveMode).
const (
	ModeAdaptive     = "adaptive"
	ModeToggle       = "toggle"
	ModeAlways       = "always"
	ModeOnlyAdaptive = "only_adaptive"
	ModeNone         = "none"
)

var validModes = map[string]struct{}{
	ModeAdaptive:     {},
	ModeToggle:       {},
	ModeAlways:       {},
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
//
// When the mode is empty — a model imported before the thinking-mode schema
// existed, or synced from a gateway that carries no capability metadata — the
// bridge infers one. A model id that reads as Claude 4.6+ (or as a Claude whose
// version cannot be parsed at all) resolves to adaptive: those generations
// reject the legacy thinking wire with a 400, and unknown Claude ids skew new
// because new models keep shipping while old ones retire. Every other id falls
// back to the old rule — the legacy "reasoning" compatibility flag means
// toggle, its absence means none — since non-Claude models on this bridge are
// typically compatibility gateways built around the older, more widely
// implemented wire shape.
func ResolveMode(declared string, hasReasoningCompat bool, modelID string) string {
	switch declared {
	case ModeToggle, ModeAdaptive, ModeAlways, ModeNone:
		return declared
	case ModeOnlyAdaptive:
		return ModeAdaptive
	default:
		if !hasReasoningCompat {
			return ModeNone
		}
		if claudeReadsAdaptive(modelID) {
			return ModeAdaptive
		}
		return ModeToggle
	}
}

// claudeVersionPattern reads a Claude version out of a model id, tolerating the
// id shapes gateways produce: family-first (claude-opus-5), version-first
// (claude-3-7-sonnet), and prefixed (anthropic/claude-sonnet-5,
// us.anthropic.claude-opus-4-8-v1:0). It is deliberately unanchored so those
// prefixes match, and the minor is capped at two digits so a release date in
// the id (claude-opus-4-20250514) is not read as a version.
var claudeVersionPattern = regexp.MustCompile(`claude-(?:[a-z]+-)?(\d+)(?:[.-](\d{1,2}))?(?:[.:@-]|$)`)

// claudeReadsAdaptive reports whether a model id names a Claude generation that
// uses adaptive thinking. Claude 4.6 and later do; so does any id that is
// recognizably Claude but carries no parseable version, on the grounds that an
// unknown Claude is more likely new than old — the wrong wire is a hard 400 on
// new models and merely inert on old ones, so when guessing, guess new.
// Non-Claude ids report false and keep their existing behavior.
func claudeReadsAdaptive(modelID string) bool {
	id := strings.ToLower(strings.TrimSpace(modelID))
	if !strings.Contains(id, "claude") {
		return false
	}
	match := claudeVersionPattern.FindStringSubmatch(id)
	if match == nil {
		return true
	}
	major, err := strconv.Atoi(match[1])
	if err != nil {
		return true
	}
	minor := 0
	if match[2] != "" {
		if m, err := strconv.Atoi(match[2]); err == nil {
			minor = m
		}
	}
	return major >= 5 || (major == 4 && minor >= 6)
}

// Supported reports whether a resolved mode allows any thinking at all.
func Supported(mode string) bool {
	return mode != ModeNone && mode != ""
}
