package compaction

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	contextlimit "github.com/felinics/memoh/internal/agent/context/limit"
)

// forkRead is what a pass sends its summarizer of the request before the
// newest messages the event keeps, ahead of the instruction: the request's
// messages as the run sends them, except that a call group, which a pass reads
// whole or not at all, larger than a fork can read whole (forkGroupCap) is cut
// to it (cutGroup). A fold therefore reads, and stands for, any group the
// request holds, however large. Messages that render no source, runtime
// context and a system prompt the prompt cache moved into the messages, reach
// the fork whole: every pass reads them. A group no cut brings within the cap
// fails the pass as an input overflow.
func forkRead(ev Event) ([]sdk.Message, error) {
	messages := slices.Clip(ev.Request.Messages[:ev.cut])
	capTokens := forkGroupCap(ev)
	var read []sdk.Message
	for start := 0; start < len(messages); {
		end := start + 1
		for !foldBoundary(messages, end) {
			end++
		}
		var at []int
		var group []sdk.Message
		for i := start; i < end; i++ {
			if len(ev.Sources[i].Sources) > 0 {
				at, group = append(at, i), append(group, messages[i])
			}
		}
		if forkTokens(sdk.Request{Messages: group}) > capTokens {
			cut, ok := cutGroup(group, capTokens)
			if !ok {
				return nil, failed(ReasonInputOverflow, fmt.Errorf("%w: the call group at message %d does not fit a fork in %d tokens however it is cut", errCompactionInputOverflow, start, capTokens))
			}
			if read == nil {
				read = slices.Clone(messages)
			}
			for j, i := range at {
				read[i] = cut[j]
			}
		}
		start = end
	}
	if read == nil {
		return messages, nil
	}
	return read, nil
}

// forkGroupCap is the most one call group takes in a fork request: what the
// window has beside the system prompt, the tools and the messages that render
// no source, the longest summary a pass reads before it, the instruction and
// the least output a fork asks for (leastOutput). A pass can therefore read
// any group after a summary.
func forkGroupCap(ev Event) int {
	var fixed []sdk.Message
	for i, message := range ev.Request.Messages[:ev.cut] {
		if len(ev.Sources[i].Sources) == 0 {
			fixed = append(fixed, message)
		}
	}
	envelope := forkTokens(sdk.Request{System: ev.Request.System, Messages: fixed, Tools: ev.Request.Tools})
	return max(ev.Window-envelope-summaryBudget(ev)-forkInstructionReserve-leastOutput(ev), minForkOutputTokens)
}

// cutGroup renders a call group within capTokens for a fork: the text of its
// messages, the arguments of the calls they make and the results they carry
// keep their head and tail, and the media they carry are named instead of
// sent. Every such part of the group shares one byte budget (partShare), so a
// message of many parts takes more of it than a message of few. Roles, calls
// and results stay, so the fork pairs them the way the request does. The cut
// is measured the way the fork is priced, so content that grows when it is
// encoded keeps less of itself until the group fits, or until no part can be
// cut shorter, when it reports false.
func cutGroup(group []sdk.Message, capTokens int) ([]sdk.Message, bool) {
	var sizes []int
	for _, message := range group {
		for _, part := range message.Content {
			if size, ok := partBytes(part); ok {
				sizes = append(sizes, size)
			}
		}
	}
	budget := int(contextfrag.ProviderBudgetBytesForTokens(capTokens))
	for shortest := math.MaxInt; ; {
		// A quarter of the budget is left for the parts' envelopes and the
		// markers the cut adds.
		limit := contextlimit.NormalizedLimit(contextlimit.ToolOutputLimit{MaxBytes: partShare(sizes, budget*3/4)})
		if limit.MaxBytes >= shortest {
			// No part can be cut shorter than the cut that did not fit.
			return nil, false
		}
		read := make([]sdk.Message, len(group))
		for i, message := range group {
			read[i] = cutParts(message, limit)
		}
		tokens := forkTokens(sdk.Request{Messages: read})
		if tokens <= capTokens {
			return read, true
		}
		shortest, budget = limit.MaxBytes, budget*capTokens/tokens*9/10
	}
}

// partBytes is what a part a cut keeps the head and tail of takes.
func partBytes(part sdk.MessagePart) (int, bool) {
	switch p := part.(type) {
	case sdk.TextPart:
		return len(p.Text), true
	case sdk.ToolResultPart:
		return len(p.Result.String()), true
	case sdk.ToolCallPart:
		return len(p.Input.String()), true
	}
	return 0, false
}

// partShare is the most a part of the given sizes may take of budget: the
// parts within an equal share of what the smaller ones leave stay whole, and
// the larger ones take that share each.
func partShare(sizes []int, budget int) int {
	sorted := slices.Sorted(slices.Values(sizes))
	left := budget
	for i, size := range sorted {
		if share := left / (len(sorted) - i); size > share {
			return share
		}
		left -= size
	}
	return budget
}

// cutParts keeps each part of message within limit: its text, the arguments
// of its calls and its results keep their head and tail, its reasoning is
// left out, and the media of a message other than a tool's are named.
func cutParts(message sdk.Message, limit contextlimit.ToolOutputLimit) sdk.Message {
	cut := message
	cut.Content = make([]sdk.MessagePart, 0, len(message.Content))
	for _, part := range message.Content {
		switch p := part.(type) {
		case sdk.TextPart:
			p.Text = contextlimit.LimitString(p.Text, "message", limit)
			cut.Content = append(cut.Content, p)
		case sdk.ToolResultPart:
			p.Result = cutOutput(p.Result, limit)
			cut.Content = append(cut.Content, p)
		case sdk.ToolCallPart:
			p.Input = cutArguments(p.Input, limit)
			cut.Content = append(cut.Content, p)
		case sdk.ReasoningPart:
			// A summary stands for what was said and done, not how it was
			// reasoned.
		case sdk.ImagePart, sdk.FilePart:
			if message.Role != sdk.MessageRoleTool {
				cut.Content = append(cut.Content, sdk.TextPart{Text: "[media]"})
			}
		default:
			cut.Content = append(cut.Content, part)
		}
	}
	return cut
}

// cutOutput keeps a result within limit as the fork encodes it: a text keeps
// its head and tail, a document the head and tail of its strings.
func cutOutput(output sdk.ToolOutput, limit contextlimit.ToolOutputLimit) sdk.ToolOutput {
	var value any = output.Text
	if output.IsJSON() && json.Unmarshal(output.JSON, &value) != nil {
		value = string(output.JSON)
	}
	limited := contextlimit.LimitToolOutput(value, "tool result", limit)
	if text, ok := limited.(string); ok {
		return sdk.TextOutput(text)
	}
	if document, err := sdk.JSONOutput(limited); err == nil {
		return document
	}
	return sdk.TextOutput(contextlimit.MarshalString(limited))
}

// cutArguments keeps a call's arguments within limit as the fork encodes
// them: the head and tail of their strings, still one JSON object, so the
// fork sends the call the way providers take one.
func cutArguments(arguments sdk.ToolArguments, limit contextlimit.ToolOutputLimit) sdk.ToolArguments {
	if !contextlimit.EncodedExceeds(arguments.Object(), limit) {
		return arguments
	}
	var value any
	if arguments.Unmarshal(&value) != nil {
		value = arguments.String()
	}
	limited := contextlimit.LimitToolOutput(value, "tool call arguments", limit)
	if _, ok := limited.(map[string]any); !ok {
		limited = map[string]any{"truncated_arguments": limited}
	}
	encoded, err := json.Marshal(limited)
	if err != nil {
		return arguments
	}
	return sdk.ParseToolArguments(string(encoded))
}
