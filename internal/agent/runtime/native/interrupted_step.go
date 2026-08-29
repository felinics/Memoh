package native

import (
	"strings"

	sdk "github.com/felinics/twilight/sdk"
)

// interruptedStepCapture retains only the current model call's text and
// reasoning. Tool activity stays on Twilight's complete-step path. stepIndex
// distinguishes an unfinished frontier from a step already committed while its
// finish event was still buffered for this consumer.
type interruptedStepCapture struct {
	text                 strings.Builder
	textProviderMetadata map[string]any
	reasoningBlocks      reasoningBlockCapture
	toolActivity         bool
	finished             bool
	stepIndex            int
}

// reasoningBlockCapture folds streamed reasoning into ordered blocks, keeping
// each block's own dialect and opaque token. Providers verify the block sequence
// on replay, so merging blocks or dropping their tokens makes the checkpoint
// unreplayable.
type reasoningBlockCapture struct {
	parts []sdk.ReasoningPart
	byID  map[string]int
}

func (c *reasoningBlockCapture) reset() {
	c.parts = nil
	c.byID = nil
}

// at returns the block a stream part belongs to, creating it when the provider
// announces a new ID. A part with no ID joins the most recent block, which is
// what providers that omit block IDs imply.
func (c *reasoningBlockCapture) at(id string) int {
	if id != "" {
		if idx, ok := c.byID[id]; ok {
			return idx
		}
	} else if len(c.parts) > 0 {
		return len(c.parts) - 1
	}
	c.parts = append(c.parts, sdk.ReasoningPart{ID: id})
	idx := len(c.parts) - 1
	if id != "" {
		if c.byID == nil {
			c.byID = make(map[string]int)
		}
		c.byID[id] = idx
	}
	return idx
}

// observe applies one stream part. Text concatenates; the dialect, model, and
// opaque token replace when present because the later value is authoritative.
func (c *reasoningBlockCapture) observe(
	id, text string,
	format sdk.ReasoningFormat,
	model string,
	meta map[string]any,
) {
	idx := c.at(id)
	c.parts[idx].Text += text
	if format != sdk.ReasoningFormatUnknown {
		c.parts[idx].Format = format
	}
	if model != "" {
		c.parts[idx].Model = model
	}
	if len(meta) == 0 {
		return
	}
	if c.parts[idx].ProviderMetadata == nil {
		c.parts[idx].ProviderMetadata = make(map[string]any, len(meta))
	}
	for key, value := range meta {
		c.parts[idx].ProviderMetadata[key] = value
	}
}

func (c *reasoningBlockCapture) messageParts() []sdk.MessagePart {
	parts := make([]sdk.MessagePart, 0, len(c.parts))
	for i := range c.parts {
		parts = append(parts, c.parts[i])
	}
	return parts
}

func (c *reasoningBlockCapture) text() string {
	return sdk.ReasoningText(c.parts)
}

// empty reports whether any replayable reasoning was captured. IDs, dialects,
// and model names merely describe a block; text or provider metadata is the
// payload. A redacted thinking block has no text but is meaningful because its
// encrypted payload lives in metadata.
func (c *reasoningBlockCapture) empty() bool {
	for i := range c.parts {
		if strings.TrimSpace(c.parts[i].Text) != "" || len(c.parts[i].ProviderMetadata) > 0 {
			return false
		}
	}
	return true
}

func (c *interruptedStepCapture) resetContent() {
	c.text.Reset()
	c.textProviderMetadata = nil
	c.reasoningBlocks.reset()
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
	case *sdk.TextStartPart:
		c.reopenIfFinished()
	case *sdk.TextDeltaPart:
		c.reopenIfFinished()
		c.text.WriteString(p.Text)
	case *sdk.TextEndPart:
		if p.ProviderMetadata != nil {
			c.textProviderMetadata = p.ProviderMetadata
		}
	case *sdk.ReasoningStartPart:
		c.reopenIfFinished()
		c.reasoningBlocks.observe(p.ID, "", p.Format, p.Model, p.ProviderMetadata)
	case *sdk.ReasoningDeltaPart:
		c.reopenIfFinished()
		c.reasoningBlocks.observe(p.ID, p.Text, p.Format, p.Model, p.ProviderMetadata)
	case *sdk.ReasoningEndPart:
		c.reasoningBlocks.observe(p.ID, "", p.Format, p.Model, p.ProviderMetadata)
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
	text := c.text.String()
	if c.toolActivity || c.stepIndex != nextDurableStep ||
		(strings.TrimSpace(text) == "" && c.reasoningBlocks.empty()) {
		return nil
	}
	// Reasoning leads the message, one part per block: providers enforce
	// thinking-first ordering and reject a modified block sequence.
	parts := c.reasoningBlocks.messageParts()
	if text != "" {
		parts = append(parts, sdk.TextPart{
			Text:             text,
			ProviderMetadata: c.textProviderMetadata,
		})
	}
	return &sdk.StepResult{
		Text:           text,
		Reasoning:      c.reasoningBlocks.text(),
		ReasoningParts: c.reasoningBlocks.parts,
		Messages:       []sdk.Message{{Role: sdk.MessageRoleAssistant, Content: parts}},
	}
}
