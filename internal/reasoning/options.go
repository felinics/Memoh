package reasoning

import "strings"

// Options is what a caller may select for a model: the answer every surface needs
// before it can render a picker, a slash-command choice list, or an API response.
//
// Efforts holds active tiers only — the disable token never appears in it. Whether
// off is reachable is CanDisable, a field of its own, so no consumer has to
// remember to filter a sentinel out of a tier list. That sentinel was how a
// capability boolean travelled through the effort list before this package
// existed, and forgetting to filter it is what let an *active* config resolve to
// "off" in two separate places.
type Options struct {
	// Supported is false when the model has no thinking concept; the other fields
	// are then empty and no control should be rendered at all.
	Supported bool `json:"supported"`
	// CanDisable reports whether picking "off" actually reaches the model.
	CanDisable bool `json:"can_disable"`
	// Efforts are the selectable active tiers, weakest to strongest as advertised.
	Efforts []string `json:"efforts,omitempty"`
	// DefaultEffort is the tier to use when nothing is stored, and the tier to fall
	// back to when a stored value is no longer offered by the model.
	DefaultEffort string `json:"default_effort,omitempty"`
	// EffortsWithoutOff are tiers that cannot be combined with off on this model.
	// Opus 5 accepts an explicit disable only at effort high or below, so a client
	// that lets a user hold both must know which tiers conflict.
	EffortsWithoutOff []string `json:"efforts_without_off,omitempty"`
}

// Client types whose wire expresses "off" by omitting the field rather than by
// sending a value. Anthropic's Messages API has no off token: thinking is off when
// the field is absent, which is exactly what the adaptor sends for a disabled
// toggle model. No catalog advertises the disable token for Claude either — the
// capability registry never marks an Anthropic entry as supporting the off effort
// — so gating on the declaration alone would hide a control that works.
func expressesOffByOmission(clientType string) bool {
	return clientType == ClientTypeAnthropicMessages
}

// OptionsFor reports what a caller may select for a model.
//
// It shares effectiveEfforts and pickEffort with ResolveConfig on purpose: the
// options a user is offered and the value a call actually sends are then two
// readings of one computation, and cannot disagree. Every past bug in this area
// was a disagreement between those two answers.
//
// offSupport is the model's declared response to an explicit disable (see the
// OffSupport constants). Empty falls back to the mode-based rule.
func OptionsFor(mode string, advertised []string, clientType, offSupport string) Options {
	if !Supported(mode) {
		return Options{}
	}
	if mode == ModeAlways {
		// The model reasons and takes no direction: no tiers to pick, no switch to
		// flip. Reporting it as supported-but-uncontrollable is what lets a client
		// say so, rather than rendering a control that cannot reach the model.
		return Options{Supported: true}
	}

	levels := effectiveEfforts(advertised, clientType)
	tiers := make([]string, 0, len(levels))
	for _, e := range levels {
		if e == EffortDisable {
			continue
		}
		tiers = append(tiers, e)
	}

	var conflicting []string
	if offSupport == OffSupportLowEffortOnly {
		for _, e := range tiers {
			if effortExceedsHigh(e) {
				conflicting = append(conflicting, e)
			}
		}
	}

	// A model advertising only the off token has no tier to fall back to. Reporting
	// one it does not offer would invite a caller to send it, so the default stays
	// empty and ReconcileStored keeps such a model on off.
	defaultEffort := ""
	if len(tiers) > 0 {
		defaultEffort = pickEffort("", "", levels)
	}

	return Options{
		Supported:         true,
		CanDisable:        canDisable(mode, levels, clientType, offSupport),
		Efforts:           tiers,
		DefaultEffort:     defaultEffort,
		EffortsWithoutOff: conflicting,
	}
}

// canDisable reports whether picking "off" reaches the model.
//
// A declared off-support answer wins, because it is the only thing that can be
// right: Anthropic's per-model table splits models that share a thinking mode and
// an identical tier list. Opus 4.6-4.8 accept an explicit disable, Fable 5 rejects
// it with a 400, and nothing about the mode or the tiers distinguishes them.
//
// Without a declaration, the fallbacks are an advertised disable token, then the
// omission rule — limited to toggle models, where thinking only happens when asked
// for. Under adaptive thinking the model may think by default, so omitting the
// field can leave that default in charge rather than turning thinking off. That
// fallback is deliberately conservative: it under-offers on adaptive models that
// would accept a disable, which costs a control, rather than over-offering on a
// model that rejects one, which costs a failed request.
func canDisable(mode string, levels []string, clientType, offSupport string) bool {
	if mode == ModeAlways {
		return false
	}
	switch offSupport {
	case OffSupportAccepted, OffSupportLowEffortOnly:
		return true
	case OffSupportRejected:
		return false
	}
	if hasEffort(levels, EffortDisable) {
		return true
	}
	return mode == ModeToggle && expressesOffByOmission(clientType)
}

// ReconcileStored returns the effort a caller should hold after the model changed.
// A stored value the model still offers survives; "off" survives when off is still
// reachable; anything else lands on the model's default tier. It returns "" when
// the model has no thinking concept, meaning the caller should clear the value.
//
// This is the one policy the frontend used to implement twice — once in the bot
// settings page and once in the chat composer, with the two disagreeing about
// whether Claude could be turned off.
func ReconcileStored(stored string, opts Options) string {
	if !opts.Supported {
		return ""
	}
	if !opts.CanDisable && len(opts.Efforts) == 0 {
		// An always-on model exposes no selection to reconcile against. Keep the
		// preference dormant so switching back restores it, while canonicalizing
		// the legacy spelling of off at the storage boundary.
		if IsDisabled(stored) {
			return EffortDisable
		}
		return strings.TrimSpace(stored)
	}
	if IsDisabled(stored) {
		if opts.CanDisable {
			return EffortDisable
		}
		return opts.DefaultEffort
	}
	if hasEffort(opts.Efforts, stored) {
		return stored
	}
	return opts.DefaultEffort
}
