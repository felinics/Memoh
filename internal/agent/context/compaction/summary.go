package compaction

import (
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/turn"
)

// SummaryMessage is the message a summary renders as in a request.
func SummaryMessage(summary string) sdk.Message {
	return sdk.UserMessage(turn.SummaryText(summary))
}

// summaryReplayTokens prices a summary as the request it lands in carries
// it, wrapper and message envelope included.
func summaryReplayTokens(summary string) int {
	return contextfrag.ProviderEnvelopeTokens("", []sdk.Message{SummaryMessage(summary)}, nil)
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
