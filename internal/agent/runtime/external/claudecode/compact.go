package claudecode

import (
	"context"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/errs"
)

var _ external.Compactor = (*Driver)(nil)

func (d *Driver) Compact(ctx context.Context, input external.PromptInput) (external.CompactionResult, error) {
	input, err := external.CompactionTurn(input, metadataSessionIDKey)
	if err != nil {
		return external.CompactionResult{}, err
	}
	// Keep the model last observed in this session when the Agent default differs.
	input.ModelID = external.FirstNonEmpty(input.ModelID, external.MetadataString(input.RuntimeMetadata, "claude_model"))
	result, err := d.Prompt(ctx, input)
	if err != nil {
		return external.CompactionResult{}, err
	}
	if ctx.Err() != nil {
		return external.CompactionResult{}, ctx.Err()
	}
	if !result.TurnCompleted {
		return external.CompactionResult{}, errs.NewDependency("claude compaction did not complete")
	}
	return external.CompactionResult{RuntimeMetadata: result.RuntimeMetadata}, nil
}
