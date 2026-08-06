package native

import (
	"strings"

	sdk "github.com/memohai/twilight-ai/sdk"
)

// interruptedStepCapture retains only the current model call's text and
// reasoning. Tool activity stays on Twilight's complete-step path. stepIndex
// distinguishes an unfinished frontier from a step already committed while its
// finish event was still buffered for this consumer.
type interruptedStepCapture struct {
	text, reasoning strings.Builder
	reasoningMeta   map[string]any
	toolActivity    bool
	finished        bool
	stepIndex       int
}

func (c *interruptedStepCapture) resetContent() {
	c.text.Reset()
	c.reasoning.Reset()
	c.reasoningMeta = nil
	c.toolActivity = false
	c.finished = false
}

// rebase restarts capture at an absolute step index. A mid-stream retry
// abandons the failed attempt's partial output — the model regenerates it from
// the last committed boundary — so that content must not survive as a
// checkpoint.
func (c *interruptedStepCapture) rebase(stepIndex int) {
	c.resetContent()
	c.stepIndex = stepIndex
}

// advance opens the capture window for whichever step is starting. Only a step
// that already finished moves the index: the first step of a stream starts at
// the index rebase left behind.
func (c *interruptedStepCapture) advance() {
	if c.finished {
		c.stepIndex++
	}
	c.resetContent()
}

func (c *interruptedStepCapture) reopenIfFinished() {
	if c.finished {
		c.advance()
	}
}

func (c *interruptedStepCapture) observe(part sdk.StreamPart) {
	switch p := part.(type) {
	case *sdk.StartStepPart:
		c.advance()
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

// snapshot returns retained output only at the uncommitted frontier. A finished
// step is still eligible when its complete commit lost the abort race; a
// successful complete commit advances nextDurableStep and rejects it here.
func (c *interruptedStepCapture) snapshot(nextDurableStep int) *sdk.StepResult {
	text, reasoning := c.text.String(), c.reasoning.String()
	if c.toolActivity || c.stepIndex != nextDurableStep ||
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
