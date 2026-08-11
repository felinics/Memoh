package capabilities

import "fmt"

// Merge combines what each source is authoritative about, rather than ranking the
// sources against each other. Every field has exactly one owner:
//
//	models.dev   the wire shape — dialect, budget bounds, tier list, thinking mode.
//	             It is the only catalog that models a provider's control as a
//	             typed option rather than a spread of booleans.
//	OpenRouter   off-ability and the default state. It is a gateway, so it knows
//	             which models refuse to stop thinking and which think unless told
//	             otherwise; both facts fall out of its own routing.
//
// Where their knowledge overlaps — whether a model can be turned off — a
// disagreement is reported rather than resolved. Picking a winner would hide the
// one thing a second source is for: telling us that something is wrong.
//
// Neither source can answer whether a model *accepts* an explicit disable, which
// splits Anthropic's catalog three ways and only its own documentation states.
// That stays hand-declared, and a declaration always wins here.
func Merge(base, off Capabilities) (Capabilities, []string) {
	out := base
	var conflicts []string

	if off.ReasoningDefaultOn != nil {
		out.ReasoningDefaultOn = off.ReasoningDefaultOn
	}

	switch {
	case off.ReasoningOffSupport == "":
		// No opinion; keep whatever the base said.
	case out.ReasoningOffSupport == "":
		out.ReasoningOffSupport = off.ReasoningOffSupport
	case out.ReasoningOffSupport != off.ReasoningOffSupport:
		conflicts = append(conflicts, fmt.Sprintf(
			"off-ability: models.dev implies %q, OpenRouter reports %q",
			out.ReasoningOffSupport, off.ReasoningOffSupport))
		// The gateway wins on this one field: it has to make the request work,
		// while models.dev is describing what the vendor documents. The conflict is
		// still surfaced so a human can decide whether the catalog is stale.
		out.ReasoningOffSupport = off.ReasoningOffSupport
	}

	return out, conflicts
}
