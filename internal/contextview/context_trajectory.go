package contextview

import (
	"context"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/context/trajectory"
	agentpkg "github.com/felinics/memoh/internal/agent/runtime/native"
)

func recordProviderContextTrajectory(ctx context.Context, cfg agentpkg.RunConfig, stage string, frags []contextfrag.ContextFrag) {
	if cfg.ContextLifecycle.TrajectoryRecorder() == nil {
		return
	}
	blocks := make([]trajectory.Block, 0, len(frags)+1)
	for _, frag := range frags {
		blocks = append(blocks, trajectory.JSONBlock(string(frag.Kind), frag.ID, frag))
	}
	if stage != "context_collected" {
		blocks = append(blocks, trajectory.JSONBlock("selection", "selection", cfg.ContextManifest))
	}
	cfg.RecordTrajectory(ctx, stage, nil, nil, blocks...)
}
