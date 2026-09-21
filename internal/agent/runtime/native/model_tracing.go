package native

// Span names for model calls. The spans themselves are opened by
// providerCallObserver, which wraps the provider for both entry points.
//
// Nothing about the conversation is recorded on them: not the prompt, not the
// reply, not the tool definitions sent with it. A span carrying those would
// publish the user's messages to whoever can read traces, which is the same
// rule docs/logging.md applies to log records.
//
// One span per call to the provider, not one per turn. A turn is a loop —
// model, tools, model again — and the SDK runs the whole loop inside a single
// call to StreamText, so a span opened around that call covers every round at
// once and can only be ended on one of them. Measured that way a ten-round
// turn shows one model span and nine unexplained gaps, which is the question
// the trace was supposed to answer.
//
// The names say which entry point the call came through, because the two
// behave differently under the same load: a stream can be abandoned partway
// and retried, a generate cannot.
const (
	// spanModelStream covers one streaming call, from the request to the end
	// of that call's parts. How long the provider took to say anything at all
	// is on it as agent.model.first_part_ms rather than as a span, because the
	// two numbers describe the same call and nesting them would double every
	// model row in a waterfall.
	spanModelStream = "agent.model.stream"
	// spanModelGenerate covers one whole non-streaming call.
	spanModelGenerate = "agent.model.generate"
)
