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
// by name, because the Anthropic wire changed shape between model generations and
// the era has to be inferred from the advertised tiers. Callers pass their own
// client-type string; this constant is what it is compared against.
const ClientTypeAnthropicMessages = "anthropic-messages"

// Client types whose wire policy maps "max" onto "xhigh". Codex uses its catalog
// levels directly and is deliberately absent.
const (
	clientTypeOpenAICompletions = "openai-completions"
	clientTypeOpenAIResponses   = "openai-responses"
)

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
//	clientType: the provider's client type, for wire policy
//
// Returns nil when the model has no thinking concept at all.
func ResolveConfig(mode string, advertised []string, options Options, stored, requested, clientType string) *Config {
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

	levels := effectiveEfforts(advertised, clientType)
	offEffort := offEffortFor(levels)
	req := strings.TrimSpace(requested)
	adaptive := isAdaptiveWire(mode, levels, clientType)
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
	case req == EffortAdaptive:
		// Legacy "adaptive" override on a toggle model: treat as on (toggle has no
		// adaptive concept; send a normal effort).
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

// isAdaptiveWire reports whether a call should use the Anthropic adaptive wire.
//
// A declared adaptive mode says so directly. Beyond that, Anthropic 4.6+ uses the
// effort/adaptive wire (no budget_tokens), and cloud variants
// (bedrock/vertex/azure/openrouter) are missing supports_adaptive_thinking in the
// LiteLLM registry while still advertising the 4.6+ effort tiers — so promote them
// here. This keeps them off the legacy budget path, where budget_tokens is
// rejected with 400 on 4.7+.
func isAdaptiveWire(mode string, levels []string, clientType string) bool {
	if mode == ModeAdaptive {
		return true
	}
	return clientType == ClientTypeAnthropicMessages && anthropicEffortEra(levels)
}

// anthropicEffortEra reports whether an Anthropic model uses the 4.6+
// effort/adaptive thinking mechanism rather than the legacy
// thinking{type:"enabled", budget_tokens:N} path. Pre-4.6 Claude advertises only
// the implicit low/medium/high base; 4.6+ adds at least one of minimal/xhigh/max.
// Detecting any of those catches the cloud-provider variants that the registry
// leaves without supports_adaptive_thinking.
//
// The disable token is deliberately not a signal. It declares that a model can be
// turned off, which every Claude generation can do, so reading it as "4.6+" would
// put a legacy model on the adaptive wire that it rejects.
func anthropicEffortEra(levels []string) bool {
	for _, e := range levels {
		switch e {
		case EffortMinimal, EffortXHigh, EffortMax:
			return true
		}
	}
	return false
}

// pickEffort resolves the effort to send when thinking is active: the per-message
// override (if a concrete tier) wins, then the stored default, then medium. Values
// outside the effective model+wire effort list are ignored so stale settings or
// command/API overrides cannot send a known-invalid wire value.
func pickEffort(requested, stored string, levels []string) string {
	if e := strings.TrimSpace(requested); e != "" && e != EffortAdaptive && e != EffortDisable {
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

// effectiveEfforts intersects the model's advertised effort levels with the
// selected client's wire policy, and supplies the common base when a model
// advertises nothing. Generic OpenAI-format clients drop "max" (they take xhigh as
// the ceiling); Codex uses its catalog levels directly.
func effectiveEfforts(advertised []string, clientType string) []string {
	levels := advertised
	if len(levels) == 0 {
		levels = []string{EffortLow, EffortMedium, EffortHigh}
	}
	out := make([]string, 0, len(levels))
	for _, e := range levels {
		if normalizesMax(clientType) && e == EffortMax {
			continue
		}
		if !hasEffort(out, e) {
			out = append(out, e)
		}
	}
	return out
}

// normalizesMax reports whether the client's wire policy maps "max" to "xhigh".
func normalizesMax(clientType string) bool {
	switch clientType {
	case clientTypeOpenAICompletions, clientTypeOpenAIResponses:
		return true
	default:
		return false
	}
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
