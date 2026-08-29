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

// Reasoning wire dialects. A dialect is how a provider spells the thinking
// control on the request, which is not derivable from the tiers a model
// advertises — Gemini 2.5 takes a token budget while 3.x takes a named level, and
// sending both is a 400. It is declared per model rather than sniffed from an id.
const (
	// DialectTier sends a named tier: OpenAI reasoning.effort, Anthropic
	// output_config.effort, Gemini 3.x thinkingLevel.
	DialectTier = "tier"
	// DialectBudget sends a token budget: Anthropic <=4.5 budget_tokens, Gemini
	// 2.5 thinkingBudget.
	DialectBudget = "budget"
)

var validDialects = map[string]struct{}{
	DialectTier:   {},
	DialectBudget: {},
}

// IsValidDialect reports whether a dialect token can be stored. An empty dialect
// is valid and means "the provider's modern default".
func IsValidDialect(dialect string) bool {
	if dialect == "" {
		return true
	}
	_, ok := validDialects[dialect]
	return ok
}

// budgetRatios place each tier proportionally within a model's own budget range.
// Fixed token counts cannot be right across models with different ceilings, and
// the vendors publish no tier-to-token mapping — they describe tiers as relative
// allowances rather than guarantees. Two independent clients (Cline, Cherry
// Studio) converged on this same proportional approach.
var budgetRatios = map[string]float64{
	EffortMinimal: 0.1,
	EffortLow:     0.2,
	EffortMedium:  0.5,
	EffortHigh:    0.8,
	EffortXHigh:   0.95,
	EffortMax:     1,
}

// BudgetRatio returns where a tier sits within a budget range, as a fraction.
// It reports false for values that are not active tiers, including the off token.
func BudgetRatio(effort string) (float64, bool) {
	ratio, ok := budgetRatios[strings.TrimSpace(effort)]
	return ratio, ok
}

// How a model responds to an explicit request to stop thinking. This cannot be
// derived from the thinking mode or the advertised tiers: Anthropic's own
// per-model table splits models that share both. Opus 4.6 through 4.8 default to
// thinking off and accept thinking{type:"disabled"}; Sonnet 5 defaults to on and
// still accepts it; Opus 5 accepts it only at effort high or below; Fable 5 and
// Mythos 5 reject it outright. Guessing from a model id is what this field exists
// to avoid.
const (
	// OffSupportUnset means the catalog has not said. Callers fall back to the
	// mode-based rule, which is right for the toggle generation and conservative
	// for the rest.
	OffSupportUnset = ""
	// OffSupportAccepted means the model accepts an explicit disable at any effort.
	OffSupportAccepted = "accepted"
	// OffSupportLowEffortOnly means the model accepts an explicit disable, but only
	// at effort high or below — pairing it with xhigh or max is a 400 (Opus 5).
	OffSupportLowEffortOnly = "low_effort_only"
	// OffSupportRejected means the model always thinks and rejects an explicit
	// disable (Fable 5, Mythos 5). Offering an off switch would be a dead control
	// that also costs a round trip.
	OffSupportRejected = "rejected"
)

var validOffSupport = map[string]struct{}{
	OffSupportUnset:         {},
	OffSupportAccepted:      {},
	OffSupportLowEffortOnly: {},
	OffSupportRejected:      {},
}

// IsValidOffSupport reports whether an off-support token can be stored.
func IsValidOffSupport(support string) bool {
	_, ok := validOffSupport[support]
	return ok
}

// DefaultOn reports whether omitting the thinking field leaves the model thinking.
//
// This is a different question from whether a model can be turned off, and the two
// were briefly conflated. "Can it be turned off" decides whether a picker offers
// the control; "does omission mean off" decides what the adaptor must send to
// honour that choice. Claude 4.6 answers no to the second (omitting is off) while
// Opus 5 answers yes (omitting keeps thinking, billed and counted against
// max_tokens, while the user believes it is off).
//
// nil means unknown, which callers treat as the conservative "omission is not a
// reliable way to turn thinking off".
func DefaultOn(defaultOn *bool) bool {
	return defaultOn != nil && *defaultOn
}

// effortsAboveHigh are the tiers Opus 5 refuses to combine with an explicit
// disable. They are the tiers stronger than high in the ordering.
func effortExceedsHigh(effort string) bool {
	switch strings.TrimSpace(effort) {
	case EffortXHigh, EffortMax:
		return true
	}
	return false
}
