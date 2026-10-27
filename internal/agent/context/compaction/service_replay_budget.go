package compaction

import (
	"strings"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/jackc/pgx/v5/pgtype"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/messageconv"
)

// SummaryMessage is the message a summary renders as in a request.
func SummaryMessage(summary string) sdk.Message {
	return sdk.UserMessage("<summary>\n" + strings.TrimSpace(summary) + "\n</summary>")
}

// summaryReplayTokens prices a summary as the request it lands in carries
// it, wrapper and message envelope included.
func summaryReplayTokens(summary string) int {
	return contextfrag.ProviderEnvelopeTokens("", []sdk.Message{SummaryMessage(summary)}, nil)
}

// keptReplayTokens is the history a compaction leaves raw inside the
// caller's budget: every row from the first one it must keep on, other than
// anchors and what the caller budgeted as current input.
func keptReplayTokens(messages []CompactionCandidate, sources []turn.ContextMessageSource) int {
	current := make([]turn.ContextMessageSource, 0, len(sources))
	for _, source := range sources {
		if source.Current {
			current = append(current, source)
		}
	}
	stop := min(firstPolicyStart(messages, CompactPolicyPreserveRecent), firstPolicyStart(messages, CompactPolicyMustKeep))
	tokens := 0
	for _, message := range messages[stop:] {
		if !message.HasPolicy(CompactPolicyAnchor) && !isProtectedSource(message, current) {
			tokens += messageReplayTokens(message)
		}
	}
	return tokens
}

// currentRequestEntries renders what the work continues from, so the
// summarizer knows the goal its summary serves: the protected rows, or the
// request itself before it is stored. It stays raw beside the summary and is
// never part of what the summary covers.
func currentRequestEntries(messages []CompactionCandidate, sources []turn.ContextMessageSource, request string) []messageEntry {
	var entries []messageEntry
	for _, message := range messages {
		if !isProtectedSource(message, sources) {
			continue
		}
		if content := renderCandidateEntry(message.Record); strings.TrimSpace(content) != "" {
			entries = append(entries, messageEntry{Role: message.Record.ModelMessage.Role, Content: content})
		}
	}
	if len(entries) == 0 && strings.TrimSpace(request) != "" {
		entries = append(entries, messageEntry{Role: "user", Content: request})
	}
	return entries
}

func withoutCompacted(messages []CompactionCandidate, compacted []pgtype.UUID) []CompactionCandidate {
	selected := make(map[pgtype.UUID]bool, len(compacted))
	for _, id := range compacted {
		selected[id] = true
	}
	rest := make([]CompactionCandidate, 0, len(messages))
	for _, message := range messages {
		if !selected[message.ID] {
			rest = append(rest, message)
		}
	}
	return rest
}

func compactedReplayTokens(messages []CompactionCandidate, compacted []pgtype.UUID) int {
	selected := make(map[pgtype.UUID]bool, len(compacted))
	for _, id := range compacted {
		selected[id] = true
	}
	tokens := 0
	for _, message := range messages {
		if selected[message.ID] {
			tokens += messageReplayTokens(message)
		}
	}
	return tokens
}

func messageReplayTokens(message CompactionCandidate) int {
	sdkMessage := messageconv.ModelMessageToSDKMessage(message.Record.ModelMessage)
	if len(sdkMessage.Content) == 0 {
		return contextfrag.ProviderBudgetTokensFromBytes(len(message.RawContent))
	}
	return contextfrag.ResolveProviderBudgetFragTokens(contextfrag.MessageFrag(contextfrag.MessageFragInput{Message: sdkMessage}))
}

// summaryOutputLimit is the longest summary, in tokens, the prompt may ask
// for: one that long, with a token to spare for rounding, still fits
// replayBudget as the request carries it.
func summaryOutputLimit(replayBudget, modelLimit int) int {
	low, high := 0, min(modelLimit, replayBudget)
	for low < high {
		mid := (low + high + 1) / 2
		if summaryReplayTokens(strings.Repeat("x", (mid+1)*contextfrag.EstimateBytesPerToken)) <= replayBudget {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return low
}

func isProtectedSource(candidate CompactionCandidate, sources []turn.ContextMessageSource) bool {
	for _, source := range sources {
		if source.ID == "" {
			continue
		}
		if source.Kind == "external" && source.ID == candidate.Record.ExternalMessageID {
			return true
		}
		if source.Kind == "history" && source.ID == candidate.ID.String() {
			return true
		}
	}
	return false
}

func protectCurrentSources(messages []CompactionCandidate, sources []turn.ContextMessageSource) {
	for i := range messages {
		if isProtectedSource(messages[i], sources) {
			for j := i; j < len(messages); j++ {
				messages[j].Policies = appendPolicy(messages[j].Policies, CompactPolicyPreserveRecent)
				messages[j].Policies = appendPolicy(messages[j].Policies, CompactPolicyMustKeep)
			}
			propagateMustKeepAcrossToolExchanges(messages)
			return
		}
	}
}
