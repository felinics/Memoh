package modelretry

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	sdk "github.com/felinics/twilight/sdk"
)

// Overflow is feedback about one rejected input: what the provider counted
// for the input and the input it would have accepted. Zero counts mean the
// provider identified context overflow without reporting input counts.
type Overflow struct {
	InputTokens int
	LimitTokens int
}

var overflowCounts = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^prompt is too long:\s*([0-9]+) tokens > ([0-9]+) maximum`),
	regexp.MustCompile(`(?i)^the input token count \(([0-9]+)\) exceeds the maximum number of tokens allowed \(([0-9]+)\)`),
}

// The OpenAI-compatible form names the window input and completion share.
// Its count is the input only where it says so: messages that resulted in a
// count, or a request split into messages and completion, whose input could
// take the window less that completion. A bare requested total mixes both.
var (
	contextLengthWindow   = regexp.MustCompile(`(?i)maximum context length is ([0-9]+) tokens`)
	contextLengthMessages = regexp.MustCompile(`(?i)messages resulted in ([0-9]+) tokens`)
	contextLengthSplit    = regexp.MustCompile(`(?i)requested [0-9]+ tokens \(([0-9]+) in the messages, ([0-9]+) in the completion\)`)
)

// ContextOverflow recognizes a provider's explicit token-window rejection,
// including an APIError delivered inside a stream. HTTP 400/413 alone, byte
// limits, output limits and transient errors do not authorize compaction.
func ContextOverflow(err error) (Overflow, bool) {
	var api *sdk.APIError
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &api) {
		return Overflow{}, false
	}
	if api.StatusCode != 0 && api.StatusCode != http.StatusBadRequest && api.StatusCode != http.StatusRequestEntityTooLarge && api.StatusCode != http.StatusUnprocessableEntity {
		return Overflow{}, false
	}
	message := strings.TrimSpace(api.Message)
	for _, pattern := range overflowCounts {
		if match := pattern.FindStringSubmatch(message); match != nil {
			input, _ := strconv.Atoi(match[1])
			limit, _ := strconv.Atoi(match[2])
			return Overflow{InputTokens: input, LimitTokens: limit}, true
		}
	}
	if match := contextLengthWindow.FindStringSubmatch(message); match != nil {
		window, _ := strconv.Atoi(match[1])
		if split := contextLengthSplit.FindStringSubmatch(message); split != nil {
			input, _ := strconv.Atoi(split[1])
			completion, _ := strconv.Atoi(split[2])
			return Overflow{InputTokens: input, LimitTokens: window - completion}, true
		}
		if counted := contextLengthMessages.FindStringSubmatch(message); counted != nil {
			input, _ := strconv.Atoi(counted[1])
			return Overflow{InputTokens: input, LimitTokens: window}, true
		}
		return Overflow{}, true
	}
	for _, code := range []string{api.Code, api.Type} {
		switch strings.ToLower(code) {
		case "context_length_exceeded", "context_window_exceeded", "prompt_too_long", "input_tokens_exceeded":
			return Overflow{}, true
		}
	}
	// Anthropic also reports this without counts. Keep the compatibility
	// form specific to its invalid-request envelope, not arbitrary error text.
	return Overflow{}, api.Type == "invalid_request_error" && strings.EqualFold(message, "prompt is too long")
}

// InputBudget is the estimate a request rejected at estimate rejected must
// shrink to. With the provider's counts it scales the request by how far the
// provider found it over, keeping 5% for tokenizer uncertainty; without them
// it takes off a quarter. The bound always falls; callers bound the number of
// attempts.
func (o Overflow) InputBudget(rejected int) int {
	budget := rejected / 4 * 3
	if o.InputTokens > o.LimitTokens && o.LimitTokens > 0 {
		budget = int(float64(rejected) * float64(o.LimitTokens) / float64(o.InputTokens) * 0.95)
	}
	return max(1, budget)
}
