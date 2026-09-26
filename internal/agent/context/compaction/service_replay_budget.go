package compaction

import (
	"encoding/json"
	"strings"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	sdk "github.com/felinics/twilight/sdk"
	"github.com/jackc/pgx/v5/pgtype"
)

func summaryProviderReplayTokens(summary string) int {
	return contextfrag.ResolveProviderBudgetFragTokens(contextfrag.MessageFrag(contextfrag.MessageFragInput{Message: sdk.UserMessage("<summary>\n" + strings.TrimSpace(summary) + "\n</summary>")}))
}

func retainedReplayTokens(messages []CompactionCandidate, compacted []pgtype.UUID, frontier []Artifact, fusing bool) int {
	selected := make(map[pgtype.UUID]bool, len(compacted))
	for _, id := range compacted {
		selected[id] = true
	}
	tokens := 0
	for _, message := range messages {
		if selected[message.ID] {
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
