package compaction

import (
	"fmt"
	"strings"
)

const systemPrompt = `You are a conversation summarizer. Given a conversation history, produce a concise summary that preserves:
- Key facts, decisions, and agreements
- User preferences and requests
- Important context needed for continuing the conversation
- Names, dates, numbers, and specific details
- Tool usage outcomes and their results

If <prior_context> is provided, it contains summaries of earlier conversation segments. Use them ONLY to understand the conversation flow and maintain continuity. Do NOT include, repeat, or rephrase any content from <prior_context> in your output.

For tool results, only include key outcomes; ignore intermediate steps or errors.

Output ONLY the summary of the new conversation segment. No preamble, no headers.`

type messageEntry struct {
	Role    string
	Content string
}

func buildUserPrompt(priorSummaries []string, messages []messageEntry) string {
	var sb strings.Builder
	if len(priorSummaries) > 0 {
		sb.WriteString("<prior_context>\n")
		sb.WriteString("The following are summaries of earlier parts of this conversation. They are provided ONLY as reference context to help you understand the conversation flow. Do NOT include or repeat any of this content in your output summary.\n\n")
		sb.WriteString(strings.Join(priorSummaries, "\n---\n"))
		sb.WriteString("\n</prior_context>\n\n")
		sb.WriteString("Now summarize the following conversation segment:\n")
	} else {
		sb.WriteString("Summarize the following conversation:\n")
	}
	for _, m := range messages {
		fmt.Fprintf(&sb, "%s: %s\n", m.Role, m.Content)
	}
	return sb.String()
}

// priorSeparatorTokens covers the "\n---\n" joiner buildUserPrompt places
// between prior summaries.
const priorSeparatorTokens = 2

// capPriorSummaries bounds the reference context fed to the summarizer.
// Summaries accumulate one per pass, so an unbounded <prior_context> would
// eventually dominate — or overflow — the compaction model's own window.
// The newest summaries carry the most continuity, so the budget keeps them
// and drops from the oldest; order stays chronological. The cap is hard:
// separators are charged, a non-positive budget drops everything, and when
// the forced newest summary alone exceeds the budget its text is truncated
// (marker included) so callers can rely on the returned total.
// priorContextTokens is the prompt cost of the kept prior summaries — bodies
// plus the per-summary separator — and must stay the single accounting both
// capPriorSummaries and the shared-budget rebalance use.
func priorContextTokens(summaries []string) int {
	total := 0
	for _, summary := range summaries {
		total += estimateBytesAsTokens(summary) + priorSeparatorTokens
	}
	return total
}

// capEntriesToBudget truncates entry contents so their total prompt cost fits
// maxTokens. Entries are never dropped — ids and entries are emitted together
// by buildEntriesAndIDs, so dropping one would mark a row the summarizer never
// saw. This only engages when a single markable group exceeds the whole
// budget: the alternative is a prompt the compaction model rejects on every
// pass, which stalls the session behind the failure cooldown forever.
//
// The bound is therefore best-effort, not absolute: every entry keeps at
// least its floor, so when one unsplittable group holds enough entries that
// the floors alone exceed maxTokens, the capped total still does. Callers
// that need the real cost recheck with entriesPromptCost.
func capEntriesToBudget(entries []messageEntry, maxTokens int) []messageEntry {
	if maxTokens <= 0 {
		maxTokens = 1
	}
	markerTokens := estimateBytesAsTokens(truncationMarker)
	overheadOf := func(entry messageEntry) int { return estimateBytesAsTokens(entry.Role) + 1 }
	costOf := func(entry messageEntry) int { return estimateBytesAsTokens(entry.Content) + overheadOf(entry) }
	// An entry's floor is its cheapest faithful representation: the original
	// text when it is already smaller than a truncation marker. Reserving
	// every later entry's floor up front means spending on one entry can never
	// push the tail past the cap, and short entries are never replaced by a
	// marker that costs more than they do.
	floorOf := func(entry messageEntry) int {
		if cost := costOf(entry); cost < overheadOf(entry)+markerTokens {
			return cost
		}
		return overheadOf(entry) + markerTokens
	}
	tailFloor := make([]int, len(entries)+1)
	for i := len(entries) - 1; i >= 0; i-- {
		tailFloor[i] = tailFloor[i+1] + floorOf(entries[i])
	}
	remaining := maxTokens
	capped := make([]messageEntry, len(entries))
	for i, entry := range entries {
		overhead := overheadOf(entry)
		avail := remaining - tailFloor[i+1]
		cost := costOf(entry)
		if cost > avail {
			truncated := entry
			budgetBytes := (avail - overhead) * 4
			if budgetBytes > len(truncationMarker) {
				truncated.Content = truncateBytes(entry.Content, budgetBytes-len(truncationMarker))
			} else {
				truncated.Content = truncationMarker
			}
			if truncatedCost := costOf(truncated); truncatedCost < cost {
				entry, cost = truncated, truncatedCost
			}
		}
		remaining -= cost
		capped[i] = entry
	}
	return capped
}

// entriesPromptCost is the estimated prompt cost of the rendered entries,
// using the same per-entry accounting as capEntriesToBudget.
func entriesPromptCost(entries []messageEntry) int {
	total := 0
	for _, entry := range entries {
		total += estimateBytesAsTokens(entry.Content) + estimateBytesAsTokens(entry.Role) + 1
	}
	return total
}

func capPriorSummaries(summaries []string, maxTokens int) []string {
	if len(summaries) == 0 {
		return summaries
	}
	if maxTokens <= 0 {
		return nil
	}
	accumulated := 0
	start := len(summaries)
	for i := len(summaries) - 1; i >= 0; i-- {
		cost := estimateBytesAsTokens(summaries[i]) + priorSeparatorTokens
		if start < len(summaries) && accumulated+cost > maxTokens {
			break
		}
		accumulated += cost
		start = i
	}
	kept := summaries[start:]
	if len(kept) == 1 && estimateBytesAsTokens(kept[0])+priorSeparatorTokens > maxTokens {
		budgetBytes := (maxTokens-priorSeparatorTokens)*4 - len(truncationMarker)
		if budgetBytes <= 0 {
			return nil
		}
		kept = []string{truncateBytes(kept[0], budgetBytes)}
	}
	return kept
}
