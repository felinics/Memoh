package reasoning

import "strings"

// Config is the resolved reasoning decision for one call.
//
// Active and Disabled are the two on/off states; both false means the model has
// no thinking concept and the caller sends nothing. Effort is the tier to send
// when Active. Adaptive selects the Anthropic 4.6+ wire. OffEffort is the value an
// OpenAI-format provider needs to express "off", or "" when the model cannot be
// turned off and the field must be omitted entirely.
type Config struct {
	Active    bool
	Disabled  bool
	Adaptive  bool
	Effort    string
	OffEffort string
}

// ClientTypeAnthropicMessages is the one client type this package must recognise
// by name, because the Anthropic Messages wire expresses "off" by omitting the
// thinking field rather than by sending a value. Callers pass their own
// client-type string; this constant is what it is compared against.
const ClientTypeAnthropicMessages = "anthropic-messages"

// ResolveConfig makes the single reasoning decision for a call.
//
// It takes the same Options projection that capability surfaces render. This is
// important for CanDisable: explicit model declarations can override both an
// advertised disable token and a client-type omission fallback, so deriving that
// answer again here would let the picker and provider wire disagree.
//
//	mode:       the model's resolved thinking mode (see ResolveMode)
//	advertised: the model's declared effort list, "" entries and all
//	options:    the model's resolved selectable options
//	stored:     the bot's persisted effort ("" when unset, "disable" for off)
//	requested:  this message's override ("" when absent)
//
// Returns nil when the model has no thinking concept at all.
func ResolveConfig(mode string, advertised []string, options Options, stored, requested string) *Config {
	if !Supported(mode) || !options.Supported {
		return nil
	}
	if mode == ModeAlways || (!options.CanDisable && len(options.Efforts) == 0) {
		// The provider owns the decision completely. Supplying a synthetic medium
		// tier turns an uncontrollable model into a control it never advertised.
		// The second condition also covers legacy Google rows that predate the
		// dialect declaration and therefore cannot safely expose a wire control.
		return nil
	}

	levels := effectiveEfforts(advertised)
	offEffort := offEffortFor(levels)
	req := strings.TrimSpace(requested)
	adaptive := mode == ModeAdaptive
	canTurnOff := options.CanDisable
	active := func(requestedEffort string) *Config {
		return &Config{
			Active:    true,
			Adaptive:  adaptive,
			Effort:    pickEffort(requestedEffort, stored, levels),
			OffEffort: offEffort,
		}
	}

	switch {
	case IsDisabled(req) && canTurnOff:
		return &Config{Disabled: true, OffEffort: offEffort}
	case IsDisabled(req):
		// An unavailable off override is no different from any other unsupported
		// tier: fall back to the stored/default active effort. Returning Disabled
		// here would make the settings surface claim Off while the provider receives
		// no off signal and continues with its own (usually active) default.
		return active("")
	case req != "":
		return active(req)
	case IsDisabled(stored) && canTurnOff:
		// The stored effort is the only on/off source; "disable" is off.
		return &Config{Disabled: true, OffEffort: offEffort}
	default:
		// This also reconciles a stale stored Off after switching to a model that
		// cannot disable reasoning.
		return active("")
	}
}

// pickEffort resolves the effort to send when thinking is active: the per-message
// override (if a concrete tier) wins, then the stored default, then medium. Values
// outside the effective model+wire effort list are ignored so stale settings or
// command/API overrides cannot send a known-invalid wire value.
func pickEffort(requested, stored string, levels []string) string {
	if e := strings.TrimSpace(requested); e != "" && e != EffortDisable {
		if hasEffort(levels, e) {
			return e
		}
	}
	// The stored effort is skipped when it means "off". pickEffort only runs once
	// reasoning is on, and "off" is advertisable, so without this guard a bot
	// parked on off would hand the disable token back as an active tier and send it
	// upstream, where no provider knows the word.
	if e := strings.TrimSpace(stored); e != "" && !IsDisabled(e) && hasEffort(levels, e) {
		return e
	}
	if hasEffort(levels, EffortEnabled) {
		return EffortEnabled
	}
	if hasEffort(levels, EffortMedium) {
		return EffortMedium
	}
	// No medium: land on the tier closest to it rather than on levels[0], which is
	// whatever the registry listed first (usually the weakest tier).
	if nearest := NearestToMedium(levels); nearest != "" {
		return nearest
	}
	return EffortMedium
}

// effectiveEfforts supplies the common base when a model advertises nothing, and
// otherwise returns the model's own tiers with duplicates removed.
//
// The declared ladder is authoritative, and the client type is deliberately not
// consulted. Which tiers an endpoint accepts is a fact about the endpoint, not
// about the shape of the request body: the same OpenAI-shaped wire carries OpenAI
// models whose ceiling is xhigh and models from other vendors whose ceiling is
// max. Deriving the ceiling from the client type made the top tier unreachable for
// the latter, and rewrote a selected max into xhigh on the way out.
func effectiveEfforts(advertised []string) []string {
	levels := advertised
	if len(levels) == 0 {
		levels = []string{EffortLow, EffortMedium, EffortHigh}
	}
	out := make([]string, 0, len(levels))
	for _, e := range levels {
		if !hasEffort(out, e) {
			out = append(out, e)
		}
	}
	return out
}

// offEffortFor translates "off" into the effort value an OpenAI-format provider
// needs, or "" when the model cannot be turned off and the caller must omit
// reasoning_effort entirely. A model that advertises EffortDisable can be switched
// off, and OpenAI spells that state "none".
//
// It deliberately never falls back to a real tier. "minimal" used to serve as a
// second-best "off", but it enables reasoning rather than disabling it, so Off
// would have resolved to the same request as the Minimal tier — one state, two
// selectable options. The two are also mutually exclusive upstream: minimal is
// gpt-5.0's weakest tier and gpt-5.1 replaced it with none, so "advertises minimal
// but not none" describes a model that genuinely cannot be turned off. Omitting
// the field is the honest answer there, and it lets the provider default stand.
func offEffortFor(levels []string) string {
	if hasEffort(levels, EffortDisable) {
		return EffortNone
	}
	return ""
}
