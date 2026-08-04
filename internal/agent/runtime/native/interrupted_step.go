package native

import (
	"strings"

	sdk "github.com/memohai/twilight-ai/sdk"
)

// interruptedStepCapture retains only the current model call's text and
// reasoning. Tool activity and finish-step both disqualify the snapshot: those
// paths keep using Twilight's complete-step barrier.
type interruptedStepCapture struct {
	text, reasoning strings.Builder
	reasoningMeta   map[string]any
	toolActivity    bool
	finished        bool
}

func (c *interruptedStepCapture) reset() {
	c.text.Reset()
	c.reasoning.Reset()
	c.reasoningMeta = nil
	c.toolActivity = false
	c.finished = false
}

func (c *interruptedStepCapture) reopenIfFinished() {
	if c.finished {
		c.reset()
	}
}

func (c *interruptedStepCapture) observe(part sdk.StreamPart) {
	switch p := part.(type) {
	case *sdk.StartStepPart:
		c.reset()
	case *sdk.TextStartPart, *sdk.ReasoningStartPart:
		c.reopenIfFinished()
	case *sdk.TextDeltaPart:
		c.reopenIfFinished()
		c.text.WriteString(p.Text)
	case *sdk.ReasoningDeltaPart:
		c.reopenIfFinished()
		c.reasoning.WriteString(p.Text)
		if p.ProviderMetadata != nil {
			c.reasoningMeta = p.ProviderMetadata
		}
	case *sdk.ReasoningEndPart:
		if p.ProviderMetadata != nil {
			c.reasoningMeta = p.ProviderMetadata
		}
	case *sdk.ToolInputStartPart, *sdk.ToolInputDeltaPart, *sdk.ToolInputEndPart,
		*sdk.StreamToolCallPart, *sdk.StreamToolResultPart, *sdk.StreamToolErrorPart,
		*sdk.ToolOutputDeniedPart, *sdk.ToolApprovalRequestPart, *sdk.ToolProgressPart:
		c.toolActivity = true
	case *sdk.FinishStepPart:
		c.finished = true
	}
}

func (c *interruptedStepCapture) snapshot() *sdk.StepResult {
	text, reasoning := c.text.String(), c.reasoning.String()
	if c.finished || c.toolActivity ||
		(strings.TrimSpace(text) == "" && strings.TrimSpace(reasoning) == "") {
		return nil
	}
	parts := make([]sdk.MessagePart, 0, 2)
	if reasoning != "" {
		parts = append(parts, sdk.ReasoningPart{Text: reasoning, ProviderMetadata: c.reasoningMeta})
	}
	if text != "" {
		parts = append(parts, sdk.TextPart{Text: text})
	}
	return &sdk.StepResult{
		Text: text, Reasoning: reasoning,
		Messages: []sdk.Message{{Role: sdk.MessageRoleAssistant, Content: parts}},
	}
}
