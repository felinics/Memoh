package application

import (
	"strings"

	"github.com/memohai/memoh/internal/agent/turn"
	"github.com/memohai/memoh/internal/tokenest"
)

// discussAdmission reports the agent-side admission decision made on a
// composed discuss context before any SDK materialization (CM-ADM-001).
type discussAdmission struct {
	EstimatedTokens int
	SelectedTokens  int
	BudgetTokens    int
	DroppedMessages int
	// ProtectedOverflow is set when artifact summaries plus the newest
	// message alone exceed the budget; the turn must fail closed with a
	// stable error instead of calling the provider (CM-ADM-002).
	ProtectedOverflow bool
}

func discussMessageTokens(m turn.DiscussMessage) int {
	if len(m.RawContent) > 0 {
		return tokenest.FromBytes(len(m.RawContent))
	}
	return tokenest.FromBytes(len(m.Content))
}

// admitDiscussMessages trims a composed discuss context to the token budget
// before SDK conversion. Artifact-summary messages and the newest message are
// protected; older raw messages are dropped as a contiguous prefix, never
// leaving an orphaned tool response at the window start. The input slice is
// not modified; the returned slice shares its backing payloads.
func admitDiscussMessages(messages []turn.DiscussMessage, budgetTokens int) ([]turn.DiscussMessage, discussAdmission) {
	admission := discussAdmission{BudgetTokens: budgetTokens}
	if len(messages) == 0 {
		return messages, admission
	}
	costs := make([]int, len(messages))
	total := 0
	for i := range messages {
		costs[i] = discussMessageTokens(messages[i])
		total += costs[i]
	}
	admission.EstimatedTokens = total
	if budgetTokens <= 0 || total <= budgetTokens {
		admission.SelectedTokens = total
		return messages, admission
	}

	selected := make([]bool, len(messages))
	used := 0
	for i := range messages {
		if messages[i].CompactionArtifactID != "" {
			selected[i] = true
			used += costs[i]
		}
	}
	newest := len(messages) - 1
	for newest >= 0 && selected[newest] {
		newest--
	}
	if newest >= 0 {
		used += costs[newest]
		selected[newest] = true
	}
	if used > budgetTokens {
		admission.SelectedTokens = used
		admission.ProtectedOverflow = true
		return nil, admission
	}
	for i := newest - 1; i >= 0; i-- {
		if selected[i] {
			continue
		}
		if used+costs[i] > budgetTokens {
			break
		}
		used += costs[i]
		selected[i] = true
	}
	// The raw window must not open on a tool response whose call was dropped.
	for i := range messages {
		if !selected[i] || messages[i].CompactionArtifactID != "" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(messages[i].Role), "tool") {
			selected[i] = false
			used -= costs[i]
			continue
		}
		break
	}

	kept := make([]turn.DiscussMessage, 0, len(messages))
	for i := range messages {
		if selected[i] {
			kept = append(kept, messages[i])
		}
	}
	admission.SelectedTokens = used
	admission.DroppedMessages = len(messages) - len(kept)
	return kept, admission
}
