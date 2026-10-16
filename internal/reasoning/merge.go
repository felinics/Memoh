package reasoning

// capabilityEfforts are the tokens in an effort list that describe a capability of
// our own rather than a tier on the wire.
//
// disable declares that a model can be turned off, and enabled that it has a
// switch with no intensity scale. Providers spell those states their own way —
// OpenAI's "none", a chat-completions compat toggle, a zero thinking budget — and
// no capability source ever reports our spelling back. A source that answers about
// tiers therefore cannot revoke one by omitting it: silence about a token the
// source has no vocabulary for is not a statement about it.
//
// They lead the list because "off" is the weakest thing a user can ask for.
var capabilityEfforts = []string{EffortDisable, EffortEnabled}

// MergeTiers combines a model's declared effort list with what a capability source
// found for it.
//
// discovered is nil when the source has no opinion about this model: it does not
// know the model, or it knows it but publishes nothing about which tiers it
// accepts. Silence is not a denial, so nil leaves the declaration untouched — this
// is what removes the need for a hand-maintained list of tokens to rescue. A
// non-nil discovered list is the source's answer about tiers and replaces the
// declared ones, including when it is shorter: "this model does not accept
// minimal" is a fact, not an omission.
//
// The exception is documented by capabilityEfforts: a token the source cannot
// express survives the replacement, because a source answering about wire tiers
// was never able to speak about it in the first place. Without that, a discovered
// list would silently drop Off from every model whose off travels outside the
// effort field.
func MergeTiers(declared, discovered []string) []string {
	if discovered == nil {
		return declared
	}
	out := make([]string, 0, len(discovered)+len(capabilityEfforts))
	for _, token := range capabilityEfforts {
		if hasEffort(declared, token) && !hasEffort(discovered, token) {
			out = append(out, token)
		}
	}
	return append(out, discovered...)
}
