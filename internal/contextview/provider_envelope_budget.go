package contextview

import (
	"fmt"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

func providerToolDefsCost(definitions []contextfrag.ToolDefAccounting) int {
	total := 0
	for _, definition := range definitions {
		total += contextfrag.ProviderToolDefTokens(definition)
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
	return contextfrag.ProviderEnvelopeTokens(payload.System, payload.Messages, nil) + providerToolDefsCost(toolDefs)
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
