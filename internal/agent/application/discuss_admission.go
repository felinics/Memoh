package application

import (
	"strings"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/turn"
)

// discussAdmission reports the agent-side admission decision made on a
// composed discuss context before any SDK materialization (CM-ADM-001).
type discussAdmission struct {
	EstimatedTokens      int
	SelectedTokens       int
	BudgetTokens         int
	DroppedMessages      int
	RecoveryBudgetTokens int
	// ProtectedOverflow is set when artifact summaries plus the newest
	// message alone exceed the budget, or when the newest message is a tool
	// response that cannot open a valid window; the turn must fail closed
	// with a stable error instead of calling the provider (CM-ADM-002).
	ProtectedOverflow bool
}

func discussMessageTokens(m turn.DiscussMessage) int {
	if len(m.RawContent) > 0 {
		return contextfrag.TokensFromBytes(len(m.RawContent))
	}
	return contextfrag.TokensFromBytes(len(m.Content))
}

func admitDiscussAgentMessages(messages []turn.DiscussMessage, budgetTokens int) ([]turn.DiscussMessage, discussAdmission) {
	return admitDiscussAgentContext(messages, budgetTokens, 0, 0)
}

func admitDiscussAgentContext(messages []turn.DiscussMessage, budgetTokens, contextBytes, imageCount int) ([]turn.DiscussMessage, discussAdmission) {
	imageTokens := imageCount * contextfrag.EstimateImageTokens
	fixedBytes := len(discussAgentPromptPrefix) + len(discussAgentPromptSuffix) + contextBytes
	entries := make([]turn.AdmissionEntry, len(messages))
	total := fixedBytes
	for i, message := range messages {
		role := strings.TrimSpace(message.Role)
		if role == "" {
			role = "user"
		}
		cost := 0
		if content := strings.TrimSpace(message.Content); content != "" {
			cost = len(role) + len(content) + len("[]\n\n\n")
		}
		entries[i] = turn.AdmissionEntry{Source: message.Source, Cost: cost, Pinned: message.CompactionArtifactID != "", ToolResponse: strings.EqualFold(role, "tool")}
		total += cost
	}
	admission := discussAdmission{BudgetTokens: budgetTokens, EstimatedTokens: turn.EstimateTokensFromBytes(total) + imageTokens}
	available := int(turn.ContextBudgetBytes(budgetTokens-imageTokens)) - fixedBytes
	if available <= 0 {
		admission.ProtectedOverflow = true
		return nil, admission
	}
	decision := turn.AdmitContextEntries(entries, available)
	admission.RecoveryBudgetTokens = turn.EstimateTokensFromBytes(available)
	currentCost := 0
	for i, current := range turn.CurrentAdmissionEntries(entries) {
		if current {
			currentCost += entries[i].Cost
		}
	}
	admission.RecoveryBudgetTokens = turn.EstimateTokensFromBytes(max(0, available-currentCost))
	admission.SelectedTokens = turn.EstimateTokensFromBytes(fixedBytes+decision.SelectedTokens) + imageTokens
	admission.DroppedMessages = decision.DroppedEntries
	admission.ProtectedOverflow = decision.ProtectedOverflow
	if decision.ProtectedOverflow {
		return nil, admission
	}
	kept := make([]turn.DiscussMessage, 0, len(messages)-decision.DroppedEntries)
	for i, message := range messages {
		if decision.Selected[i] {
			kept = append(kept, message)
		}
	}
	return kept, admission
}

// admitDiscussMessages trims a composed discuss context to the token budget
// before SDK conversion by delegating to the shared turn admission core.
// Artifact-summary messages and the newest message are protected; older raw
// messages are dropped as a contiguous prefix, never leaving an orphaned
// tool response at the window start. The input slice is not modified; the
// returned slice shares its backing payloads.
func admitDiscussMessages(messages []turn.DiscussMessage, budgetTokens int) ([]turn.DiscussMessage, discussAdmission) {
	admission := discussAdmission{BudgetTokens: budgetTokens, RecoveryBudgetTokens: budgetTokens}
	if len(messages) == 0 {
		return messages, admission
	}
	entries := make([]turn.AdmissionEntry, len(messages))
	for i := range messages {
		entries[i] = turn.AdmissionEntry{
			Cost:         discussMessageTokens(messages[i]),
			Source:       messages[i].Source,
			Pinned:       messages[i].CompactionArtifactID != "",
			ToolResponse: strings.EqualFold(strings.TrimSpace(messages[i].Role), "tool"),
		}
	}
	for i, current := range turn.CurrentAdmissionEntries(entries) {
		if current {
			admission.RecoveryBudgetTokens -= entries[i].Cost
		}
	}
	admission.RecoveryBudgetTokens = max(0, admission.RecoveryBudgetTokens)
	decision := turn.AdmitContextEntries(entries, budgetTokens)
	admission.EstimatedTokens = decision.EstimatedTokens
	admission.SelectedTokens = decision.SelectedTokens
	admission.DroppedMessages = decision.DroppedEntries
	admission.ProtectedOverflow = decision.ProtectedOverflow
	if decision.ProtectedOverflow {
		return nil, admission
	}
	if decision.DroppedEntries == 0 {
		return messages, admission
	}
	kept := make([]turn.DiscussMessage, 0, len(messages)-decision.DroppedEntries)
	for i := range messages {
		if decision.Selected[i] {
			kept = append(kept, messages[i])
		}
	}
	return kept, admission
}

func discussCurrentMessageTokens(messages []turn.DiscussMessage) int {
	entries := make([]turn.AdmissionEntry, len(messages))
	for i, message := range messages {
		entries[i] = turn.AdmissionEntry{Source: message.Source, Pinned: message.CompactionArtifactID != "", Cost: discussMessageTokens(message)}
	}
	tokens := 0
	for i, current := range turn.CurrentAdmissionEntries(entries) {
		if current {
			tokens += entries[i].Cost
		}
	}
	return tokens
}
