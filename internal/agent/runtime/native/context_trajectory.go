package native

import (
	"context"
	"fmt"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/context/trajectory"
)

func (cfg RunConfig) TrajectoryContext(ctx context.Context) context.Context {
	recorder := cfg.ContextLifecycle.TrajectoryRecorder()
	recorder.Bind(cfg.RunID, cfg.Identity.SessionID)
	return trajectory.WithRecorder(ctx, recorder)
}

func (cfg RunConfig) flushTrajectory(ctx context.Context) {
	flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	_ = cfg.ContextLifecycle.TrajectoryRecorder().Flush(flushCtx)
}

func (cfg RunConfig) RecordTrajectory(ctx context.Context, stage string, stepIndex *int, params *sdk.GenerateParams, extra ...trajectory.Block) int64 {
	recorder := cfg.ContextLifecycle.TrajectoryRecorder()
	if recorder == nil {
		return 0
	}
	if params == nil {
		params = &sdk.GenerateParams{System: cfg.System, Messages: cfg.Messages}
	}
	blocks := make([]trajectory.Block, 0, 3+len(params.Messages)+len(params.Tools)+len(extra))
	blocks = append(blocks, trajectory.Block{Kind: "system", Label: "system", Content: params.System})
	for i, message := range params.Messages {
		blocks = append(blocks, trajectory.JSONBlock(string(message.Role), fmt.Sprintf("messages[%d]", i), message))
	}
	for _, tool := range params.Tools {
		blocks = append(blocks, trajectory.JSONBlock("tool_definition", tool.Name, tool))
	}
	options := *params
	options.Model, options.System, options.Messages, options.Tools = nil, "", nil, nil
	blocks = append(blocks, trajectory.JSONBlock("generation_options", "options", options),
		trajectory.JSONBlock("model", "model", map[string]string{
			"model": modelID(cfg.Model), "provider": providerNameOf(cfg.Model),
			"input_hash": contextfrag.ProviderPayloadHash(params.System, params.Messages, params.Tools),
		}),
	)
	blocks = append(blocks, extra...)
	return recorder.Record(ctx, stage, stepIndex, blocks...)
}

func (p contextBudgetGuardProvider) trajectoryRequest(ctx context.Context, params sdk.GenerateParams) context.Context {
	if p.handoff == nil {
		return ctx
	}
	cfg := p.handoff.cfg
	p.handoff.mu.Lock()
	step := p.handoff.publishedStep
	p.handoff.mu.Unlock()
	sequence := cfg.RecordTrajectory(ctx, "provider_request", &step, &params)
	return trajectory.WithRequest(ctx, sequence)
}
