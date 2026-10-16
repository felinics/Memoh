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
	// ProtectedSources is the current input admission may not drop: the whole
	// unconsumed batch when it fits, otherwise its newest fitting suffix.
	// ProtectedTokens is its cost. OmittedSources is older current input the
	// selection left out; recovery compacts it or the run records it.
	ProtectedSources []turn.ContextMessageSource
	ProtectedTokens  int
	OmittedSources   []turn.ContextMessageSource
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
	protectedCost := admission.recordProtection(messages, entries, available, decision)
	admission.RecoveryBudgetTokens = turn.EstimateTokensFromBytes(max(0, available-protectedCost))
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
	decision := turn.AdmitContextEntries(entries, budgetTokens)
	admission.RecoveryBudgetTokens = max(0, budgetTokens-admission.recordProtection(messages, entries, budgetTokens, decision))
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

// recordProtection records which current input the decision protects and
// which it omits, and returns the protected cost in the entries' unit.
func (a *discussAdmission) recordProtection(messages []turn.DiscussMessage, entries []turn.AdmissionEntry, budget int, decision turn.AdmissionDecision) int {
	cost := 0
	protected := turn.ProtectedAdmissionEntries(entries, budget)
	for i, current := range turn.CurrentAdmissionEntries(entries) {
		switch {
		case protected[i]:
			cost += entries[i].Cost
			a.ProtectedTokens += discussMessageTokens(messages[i])
			if source := messages[i].Source; source != nil {
				a.ProtectedSources = append(a.ProtectedSources, *source)
			}
		case current && !decision.ProtectedOverflow && !decision.Selected[i]:
			if source := messages[i].Source; source != nil {
				a.OmittedSources = append(a.OmittedSources, *source)
			}
		}
	}
	return cost
}

// recordOmittedCurrentInput records the older unconsumed input a run proceeds
// without, so its absence from the provider context is never silent.
func recordOmittedCurrentInput(ledger *contextfrag.MutationLedger, groups ...[]turn.ContextMessageSource) {
	var ids []string
	for _, sources := range groups {
		for _, source := range sources {
			if source.ID != "" {
				ids = append(ids, source.ID)
			}
		}
	}
	if len(ids) > 0 {
		ledger.Record(contextfrag.MutationCurrentInputOmitted, "sources="+strings.Join(ids, ","))
	}
}
