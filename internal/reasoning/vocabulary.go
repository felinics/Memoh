// Package reasoning owns every decision about extended thinking: what a user may
// select for a model, and what a call actually sends upstream. Both answers come
// from this package and share the same internals, so "what the picker offers" and
// "what the wire receives" cannot drift apart — the drift that produced a web
// picker showing Off on models that could not be turned off, and hiding it on
// models that could.
//
// It is a leaf package on purpose. Everything it needs arrives as plain strings
// and slices, so any caller can use it: turn orchestration, the subagent spawn
// path, slash commands, HTTP handlers. Nothing here imports another Memoh package.
package reasoning

import (
	"slices"
	"strings"
)

// Active effort tiers, weakest to strongest. These are the values that turn
// reasoning on; "off" is EffortDisable and deliberately not among them.
const (
	EffortMinimal = "minimal"
	EffortLow     = "low"
	EffortMedium  = "medium"
	EffortHigh    = "high"
	EffortXHigh   = "xhigh"
	EffortMax     = "max"
)

// EffortDisable is the single representation of "no reasoning". It is both what a
// user picks and what a model advertises: a model listing it in reasoning_efforts
// can be turned off, whatever wire shape that takes on its provider. Since bots
// dropped the separate reasoning_enabled flag, it is also the only stored form of
// "off".
const EffortDisable = "disable"

// EffortNone is OpenAI's wire spelling of "no reasoning" (gpt-5.1 introduced it
// and dropped minimal; gpt-5.0 has minimal and no none). It is never declared by a
// model nor stored in settings — provider adaptors translate EffortDisable into
// it, exactly as the Anthropic path translates the same intent into
// thinking{type:"disabled"}. Giving "off" one name on our side and letting each
// provider spell it its own way is what keeps a single state from acquiring two
// selectable tokens.
const EffortNone = "none"

// EffortAdaptive is a legacy per-message override value. Adaptive is a thinking
// mode resolved from the model, not a tier a caller selects, but settings written
// before that distinction existed still carry it, so ResolveConfig accepts it as
// "on, at the default tier".
const EffortAdaptive = "adaptive"

// orderedEfforts lists the active tiers weakest to strongest. Order is what
// NearestToMedium walks, so it must stay monotonic. EffortDisable is absent on
// purpose: it is not a tier, and including it would let the nearest-tier fallback
// resolve an *active* reasoning config to "off".
var orderedEfforts = []string{
	EffortMinimal,
	EffortLow,
	EffortMedium,
	EffortHigh,
	EffortXHigh,
	EffortMax,
}

// declarableEfforts is the vocabulary a model may advertise: the active tiers plus
// EffortDisable, which declares that the model can be turned off. EffortNone is
// absent because it is a provider wire value, not something a model declares.
var declarableEfforts = map[string]struct{}{
	EffortDisable: {},
	EffortMinimal: {},
	EffortLow:     {},
	EffortMedium:  {},
	EffortHigh:    {},
	EffortXHigh:   {},
	EffortMax:     {},
}

// IsDisabled reports whether an effort value means "no reasoning". EffortNone is
// accepted as the legacy spelling: it was declarable and storable before "off" was
// unified onto EffortDisable.
func IsDisabled(effort string) bool {
	switch strings.TrimSpace(effort) {
	case EffortDisable, EffortNone:
		return true
	}
	return false
}

// IsDeclarable reports whether effort can be stored in a model's advertised
// effort list.
func IsDeclarable(effort string) bool {
	_, ok := declarableEfforts[effort]
	return ok
}

// NormalizeAdvertised rewrites the legacy spelling of "off" to the token a model
// declares today, and drops duplicates. It runs on both boundaries of a stored
// model config — before a write is validated and after a row is read back — so
// nothing downstream has to know that "none" was ever declarable.
//
// Without it the vocabulary change would only apply to freshly written configs:
// rows persisted earlier, and provider registries that have not been regenerated,
// would keep advertising "none", and every consumer that now looks for the disable
// token would read those models as "cannot be turned off" — silently dropping Off
// from the picker and misreading which thinking mechanism the model wants.
func NormalizeAdvertised(efforts []string) []string {
	if len(efforts) == 0 {
		return efforts
	}
	out := make([]string, 0, len(efforts))
	for _, effort := range efforts {
		if strings.TrimSpace(effort) == EffortNone {
			effort = EffortDisable
		}
		if !slices.Contains(out, effort) {
			out = append(out, effort)
		}
	}
	return out
}

// NearestToMedium picks the tier closest to medium from levels, breaking ties
// toward the weaker tier. It is the fallback when a model does not advertise
// medium: [minimal low] -> low, [high max] -> high, [low high] -> low. Ignores
// values outside the known tier list (including "disable"), and returns "" when
// levels has no usable tier.
func NearestToMedium(levels []string) string {
	mediumIdx := slices.Index(orderedEfforts, EffortMedium)

	best, bestIdx, bestDistance := "", 0, 0
	for _, level := range levels {
		idx := slices.Index(orderedEfforts, level)
		if idx < 0 {
			continue
		}
		distance := idx - mediumIdx
		if distance < 0 {
			distance = -distance
		}
		// Ties break toward the weaker tier (smaller index) rather than toward
		// whichever came first, because levels arrives in registry order and is
		// not guaranteed to be sorted.
		if best == "" || distance < bestDistance || (distance == bestDistance && idx < bestIdx) {
			best, bestIdx, bestDistance = level, idx, distance
		}
	}
	return best
}

// hasEffort reports whether levels contains effort.
func hasEffort(levels []string, effort string) bool {
	return slices.Contains(levels, effort)
}

// OrderedEfforts returns the active tiers weakest to strongest. Callers that need
// a rendering order with "off" at the front prepend EffortDisable themselves — it
// is not a tier and stays out of this list.
func OrderedEfforts() []string {
	return slices.Clone(orderedEfforts)
}
