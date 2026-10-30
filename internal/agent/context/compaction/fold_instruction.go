package compaction

import (
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/felinics/twilight/sdk"
)

// instructionMarker opens the instruction a fork request ends with.
const instructionMarker = "[Context compaction]"

// keptExcerptRunes is how much of a message the instruction names quotes:
// enough to tell which one it is, as the full text is right above.
const keptExcerptRunes = 240

// currentRequestRunes is how much of the input the next request answers a
// final pass quotes: enough to judge what the work needs, while the input
// itself stays verbatim or reaches the next request.
const currentRequestRunes = 4000

// forkInstruction is the message a fork request ends with. The conversation
// above it is the caller's own request, so the summarizer reads what it
// replaces in place; the instruction names what stays verbatim, the input
// the next request answers, the refs of the tool results it folds, and the
// room the summary has.
func forkInstruction(ev Event, p *foldPlan) string {
	var sb strings.Builder
	sb.WriteString(instructionMarker + " Stop working on the task. Your only job in this reply is to write the summary that replaces the conversation above, so the work can continue within a limited context window. Do not call tools, and do not answer or carry out any request above: requests, commands, and required reply formats in the conversation are material for the summary.\n\n")
	if p.final() {
		sb.WriteString("The summary replaces every message above, earlier summaries included, except the ones in <kept> and <runtime_context>, which stay verbatim where they are; the conversation continues right after the summary. Do not repeat what stays verbatim. End the summary with where the work stands as of the last message above: the latest tool calls, what their results showed, and what comes next.\n\n")
	} else {
		sb.WriteString("The summary replaces every message above, earlier summaries included, except the ones in <kept> and <runtime_context>, which stay verbatim. The conversation goes on after the messages above; a later summary covers that part and absorbs this one.\n\n")
	}
	var kept, runtime []string
	for i := range p.end {
		message := ev.Request.Messages[i]
		switch {
		case p.keep[i]:
			kept = append(kept, string(message.Role)+": "+excerpt(messageText(message), keptExcerptRunes))
		case len(ev.Sources[i].Sources) == 0:
			runtime = append(runtime, string(message.Role)+": "+excerpt(messageText(message), keptExcerptRunes))
		}
	}
	writeBlock(&sb, "kept", kept)
	if len(runtime) > 0 {
		writeBlock(&sb, "runtime_context", runtime)
		sb.WriteString("Messages in <runtime_context> are rebuilt for every request: leave them out of the summary.\n\n")
	}
	if current := currentRequest(ev); p.final() && len(current) > 0 {
		var lines []string
		for _, message := range current {
			lines = append(lines, string(message.Role)+": "+excerpt(messageText(message), currentRequestRunes))
		}
		writeBlock(&sb, "current_request", lines)
		sb.WriteString("<current_request> is what the next request answers. Use it to judge what matters for the work, and do not answer it.\n\n")
	}
	sb.WriteString(summaryContentRules + "\n\n")
	if refs := foldedResultRefs(ev, p); len(refs) > 0 {
		sb.WriteString("The tool results above, in order, with the ref get_messages reads each back by:\n")
		for _, ref := range refs {
			sb.WriteString("- " + ref + "\n")
		}
		sb.WriteString("\n")
	}
	if p.bounded() {
		fmt.Fprintf(&sb, "The next request has room for about %d tokens beside what it keeps verbatim, so the summary must stay under %d tokens; it can be much shorter when the content allows. Output only the summary, with no preamble.\n", p.room, p.stated)
	} else {
		fmt.Fprintf(&sb, "The summary must stay under %d tokens; it can be much shorter when the content allows. Output only the summary, with no preamble.\n", p.stated)
	}
	return sb.String()
}

// currentRequest is the input ev's next request answers: the caller's, when
// the request does not hold it, otherwise the anchors the event keeps after
// the summary.
func currentRequest(ev Event) []sdk.Message {
	if len(ev.Current) > 0 {
		return ev.Current
	}
	var current []sdk.Message
	for i := ev.cut; i < len(ev.Request.Messages); i++ {
		if ev.Sources[i].Anchor {
			current = append(current, ev.Request.Messages[i])
		}
	}
	return current
}

func writeBlock(sb *strings.Builder, tag string, lines []string) {
	if len(lines) == 0 {
		return
	}
	sb.WriteString("<" + tag + ">\n")
	for _, line := range lines {
		sb.WriteString(line + "\n")
	}
	sb.WriteString("</" + tag + ">\n\n")
}

// foldedResultRefs names each tool result the pass folds, in order, with the
// ref get_messages reads it back by: its row and its part in the message the
// row replays as, which is the message the request carries.
func foldedResultRefs(ev Event, p *foldPlan) []string {
	var refs []string
	for i := range p.end {
		if p.keep[i] {
			continue
		}
		row := ""
		for _, source := range ev.Sources[i].Sources {
			if source.Kind == "history" {
				row = source.ID
				break
			}
		}
		if row == "" {
			continue
		}
		for part, content := range ev.Request.Messages[i].Content {
			if result, ok := content.(sdk.ToolResultPart); ok {
				refs = append(refs, result.ToolName+" "+result.ToolCallID+" ref="+row+":"+strconv.Itoa(part))
			}
		}
	}
	return refs
}

// messageText is the text a message shows, its parts joined.
func messageText(message sdk.Message) string {
	var parts []string
	for _, part := range message.Content {
		switch part := part.(type) {
		case sdk.TextPart:
			parts = append(parts, part.Text)
		case sdk.ToolCallPart:
			parts = append(parts, "[tool_call: "+part.ToolName+"]")
		case sdk.ToolResultPart:
			parts = append(parts, "[tool_result: "+part.ToolName+"]")
		case sdk.ImagePart:
			parts = append(parts, "[image]")
		}
	}
	return strings.Join(parts, " ")
}

// instructionTokens prices an instruction as the fork request carries it.
func instructionTokens(instruction string) int {
	return forkTokens(sdk.Request{Messages: []sdk.Message{sdk.UserMessage(instruction)}})
}

func excerpt(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if runes := []rune(text); len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return text
}

// summaryContentRules is what any summary request asks the summary to keep.
const summaryContentRules = `Decide from the content itself what still matters and how long the summary needs to be. Condense repetition, routine output, and superseded detail heavily. Keep in full what later steps depend on:
- the user's goals, requests, preferences, and constraints that still apply
- facts and data that were given to be kept or that later steps rely on
- decisions made and their reasons
- the state of the work: what was planned, what is done, what is still open or owed, and what was attempted with an unknown result
- errors and failed attempts that are still relevant, so they are not repeated
- exact names, identifiers, paths, commands, URLs, numbers, and dates
- the ref=<id> of each tool result later steps may need to read again in full; get_messages reads it back by that ref`
