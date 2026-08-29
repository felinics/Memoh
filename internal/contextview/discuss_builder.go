package contextview

import (
	"context"
	"slices"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

// DiscussSDKContextBuilder assembles typed source fragments for a discuss
// turn without reverse-parsing a system prompt that is already structured.
type DiscussSDKContextBuilder struct{}

func (*DiscussSDKContextBuilder) CollectDiscussSourceFrags(
	ctx context.Context,
	scope contextfrag.Scope,
	system string,
	input DiscussContextInput,
) ([]contextfrag.ContextFrag, error) {
	systemFrags := input.SystemFrags
	if len(systemFrags) == 0 {
		collected, err := (&SystemPromptCollector{}).Collect(ctx, CollectRequest{
			Scope:  scope,
			Intent: contextfrag.IntentRunConfigPreProvider,
			Config: SystemPromptConfig{System: system, SplitWorkspace: true},
		})
		if err != nil {
			return nil, err
		}
		systemFrags = collected
	}
	discussFrags, err := (&DiscussContextCollector{}).Collect(ctx, CollectRequest{
		Scope:  scope,
		Intent: contextfrag.IntentRunConfigPreProvider,
		Config: DiscussContextConfig{
			ComposedMessages: input.ComposedMessages,
			InlineImages:     input.InlineImages,
		},
	})
	if err != nil {
		return nil, err
	}
	return slices.Concat(systemFrags, discussFrags), nil
}
