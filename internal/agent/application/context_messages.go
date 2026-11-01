package application

import (
	"slices"
	"strings"

	sdk "github.com/felinics/twilight/sdk"
)

// contextMessage pairs a request message with its provider form. Converting
// a stored message decodes and re-encodes its whole JSON, so a request
// converts each message once: passes over it read the provider form, and a
// pass that rewrites a message converts only the rewrite.
type contextMessage struct {
	model ModelMessage
	sent  sdk.Message
}

func newContextMessage(model ModelMessage) contextMessage {
	return contextMessage{model: model, sent: modelMessageToSDKMessage(model)}
}

func newContextMessages(models []ModelMessage) []contextMessage {
	out := make([]contextMessage, 0, len(models))
	for _, model := range models {
		out = append(out, newContextMessage(model))
	}
	return out
}

func contextModelMessages(messages []contextMessage) []ModelMessage {
	out := make([]ModelMessage, 0, len(messages))
	for _, message := range messages {
		out = append(out, message.model)
	}
	return out
}

func contextSentMessages(messages []contextMessage) []sdk.Message {
	out := make([]sdk.Message, 0, len(messages))
	for _, message := range messages {
		out = append(out, message.sent)
	}
	return out
}

// normalizedContext is a request's messages after sanitizing and tool-call
// closure repair. Sanitizing keeps or drops each message on its own, so the
// first n inputs sanitize to a prefix of the sanitized messages; what
// normalizing such a prefix alone yields is answered from those conversions.
type normalizedContext struct {
	messages  []contextMessage
	sanitized []contextMessage
	inputs    []int
}

func normalizeContext(messages []ModelMessage) normalizedContext {
	var normalized normalizedContext
	for i, message := range messages {
		message, ok := sanitizeMessage(message)
		if !ok {
			continue
		}
		normalized.sanitized = append(normalized.sanitized, newContextMessage(message))
		normalized.inputs = append(normalized.inputs, i)
	}
	normalized.messages = repairContextToolCalls(normalized.sanitized, syntheticToolClosureError)
	return normalized
}

func (c normalizedContext) models() []ModelMessage {
	return contextModelMessages(c.messages)
}

func (c normalizedContext) sent() []sdk.Message {
	return contextSentMessages(c.messages)
}

// prefixLength is how many messages normalizing the first n inputs alone
// yields.
func (c normalizedContext) prefixLength(n int) int {
	if n <= 0 {
		return 0
	}
	kept, _ := slices.BinarySearch(c.inputs, n)
	return len(repairContextToolCalls(c.sanitized[:kept:kept], syntheticToolClosureError))
}

// index is where input i lands: the last message normalizing inputs up to
// and including i yields, when i is a user message normalizing keeps.
func (c normalizedContext) index(i *int) *int {
	if i == nil {
		return nil
	}
	kept, found := slices.BinarySearch(c.inputs, *i)
	if !found || !strings.EqualFold(strings.TrimSpace(c.sanitized[kept].model.Role), "user") {
		return nil
	}
	prefix := repairContextToolCalls(c.sanitized[:kept+1:kept+1], syntheticToolClosureError)
	if len(prefix) == 0 || !strings.EqualFold(strings.TrimSpace(prefix[len(prefix)-1].model.Role), "user") {
		return nil
	}
	return intPointer(len(prefix) - 1)
}
