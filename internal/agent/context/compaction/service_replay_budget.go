package compaction

import (
	"encoding/json"
	"github.com/felinics/memoh/internal/agent/turn"
	"strings"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	sdk "github.com/felinics/twilight/sdk"
	"github.com/jackc/pgx/v5/pgtype"
)

func summaryProviderReplayTokens(summary string) int {
	return contextfrag.ResolveProviderBudgetFragTokens(contextfrag.MessageFrag(contextfrag.MessageFragInput{Message: sdk.UserMessage("<summary>\n" + strings.TrimSpace(summary) + "\n</summary>")}))
}

func retainedReplayTokens(messages []CompactionCandidate, compacted []pgtype.UUID, frontier []Artifact, fusing bool, sources []turn.ContextMessageSource) int {
	selected := make(map[pgtype.UUID]bool, len(compacted))
	for _, id := range compacted {
		selected[id] = true
	}
	tokens := 0
	for _, message := range messages {
		if selected[message.ID] || isProtectedSource(message, sources) {
			continue
		}
		encoded, err := json.Marshal(message.Record.ModelMessage)
		var sdkMessage sdk.Message
		if err == nil {
			err = json.Unmarshal(encoded, &sdkMessage)
		}
		if err != nil {
			tokens += contextfrag.ProviderBudgetTokensFromBytes(len(message.RawContent))
			continue
		}
		tokens += contextfrag.ResolveProviderBudgetFragTokens(contextfrag.MessageFrag(contextfrag.MessageFragInput{Message: sdkMessage}))
	}
	if !fusing {
		for _, artifact := range frontier {
			tokens += summaryProviderReplayTokens(artifact.Summary)
		}
	}
	return tokens
}

func summaryOutputLimit(replayBudget, modelLimit int) int {
	low, high := 0, min(modelLimit, replayBudget)
	for low < high {
		mid := (low + high + 1) / 2
		if summaryProviderReplayTokens(strings.Repeat("x", mid*contextfrag.EstimateBytesPerToken)) <= replayBudget {
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
