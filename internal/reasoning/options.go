package reasoning

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
	Supported bool
	// CanDisable reports whether picking "off" actually reaches the model.
	CanDisable bool
	// Efforts are the selectable active tiers, weakest to strongest as advertised.
	Efforts []string
	// DefaultEffort is the tier to use when nothing is stored, and the tier to fall
	// back to when a stored value is no longer offered by the model.
	DefaultEffort string
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
func OptionsFor(mode string, advertised []string, clientType string) Options {
	if !Supported(mode) {
		return Options{}
	}

	levels := effectiveEfforts(advertised, clientType)
	tiers := make([]string, 0, len(levels))
	for _, e := range levels {
		if e == EffortDisable {
			continue
		}
		tiers = append(tiers, e)
	}

	return Options{
		Supported:     true,
		CanDisable:    canDisable(mode, levels, clientType),
		Efforts:       tiers,
		DefaultEffort: pickEffort("", "", levels),
	}
}

// canDisable reports whether picking "off" reaches the model: either the model
// advertises the token, or its client expresses off by omitting the field.
//
// The omission rule is limited to toggle models, where thinking only happens when
// asked for. Under adaptive thinking the model decides on its own, so omitting the
// field leaves that default in charge rather than turning thinking off.
func canDisable(mode string, levels []string, clientType string) bool {
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
