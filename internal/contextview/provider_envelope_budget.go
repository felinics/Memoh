package contextview

import (
	"fmt"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
)

func providerToolDefsCost(definitions []contextfrag.ToolDefAccounting) int {
	total := 0
	for _, definition := range definitions {
		total += max(
			definition.TokenEstimate,
			contextfrag.ProviderBudgetTokensFromBytes(definition.Bytes),
		)
	}
	return total
}

func providerRenderedEnvelopeTokens(
	payload *SDKRenderedPayload,
	toolDefs []contextfrag.ToolDefAccounting,
) int {
	if payload == nil {
		return 0
	}
	total := contextfrag.ProviderBudgetTokensFromBytes(len(payload.System))
	for _, message := range payload.Messages {
		frag := contextfrag.MessageFrag(contextfrag.MessageFragInput{Message: message})
		total += contextfrag.ResolveProviderBudgetFragTokens(frag)
	}
	return total + providerToolDefsCost(toolDefs)
}

func validateProviderRenderedEnvelope(
	payload *SDKRenderedPayload,
	toolDefs []contextfrag.ToolDefAccounting,
	plan *contextfrag.ContextBudgetPlan,
) error {
	if plan == nil {
		return nil
	}
	inputTokens := providerRenderedEnvelopeTokens(payload, toolDefs)
	if inputTokens+plan.OutputReserve <= plan.Window {
		return nil
	}
	return fmt.Errorf(
		"%w: rendered_input=%d output_reserve=%d window=%d",
		contextfrag.ErrBudgetUnsatisfied,
		inputTokens,
		plan.OutputReserve,
		plan.Window,
	)
}
