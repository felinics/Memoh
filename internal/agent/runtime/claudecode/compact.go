package claudecode

import (
	"context"
	"errors"

	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/external"
)

var _ external.Compactor = (*Driver)(nil)

func (d *Driver) Compact(ctx context.Context, input external.PromptInput) (external.CompactionResult, error) {
	if metadataString(input.RuntimeMetadata, metadataSessionIDKey) == "" {
		return external.CompactionResult{}, external.ErrThreadUnavailable
	}
	// Keep the model last observed in this session when the Agent default differs.
	input.ModelID = firstNonEmpty(input.ModelID, metadataString(input.RuntimeMetadata, "claude_model"))
	input.Command = "compact"
	input.CanRequestUserInput = false
	input.Steering = nil
	input.Sink = external.EventSinkFunc(func(event.StreamEvent) {})
	result, err := d.Prompt(ctx, input)
	if err != nil {
		return external.CompactionResult{}, err
	}
	if ctx.Err() != nil {
		return external.CompactionResult{}, ctx.Err()
	}
	if !result.TurnCompleted {
		return external.CompactionResult{}, errors.New("claude compaction did not complete")
	}
	return external.CompactionResult{RuntimeMetadata: result.RuntimeMetadata}, nil
}
