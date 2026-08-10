package contextview

import (
	"context"

	sdk "github.com/memohai/twilight-ai/sdk"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
)

const hookContextCollectorName = "hook_context"

type HookContextConfig struct {
	Text string
}

type HookContextCollector struct{}

func (*HookContextCollector) Name() string {
	return hookContextCollectorName
}

func (*HookContextCollector) Collect(_ context.Context, req CollectRequest) ([]contextfrag.ContextFrag, error) {
	cfg, err := collectorConfig[HookContextConfig](req.Config, "hook_context config must be HookContextConfig")
	if err != nil {
		return nil, err
	}
	if cfg.Text == "" {
		return nil, nil
	}
	return []contextfrag.ContextFrag{{
		ID: "system.hook_context", Kind: contextfrag.KindHookContext, Role: sdk.MessageRoleSystem, Slot: contextfrag.SlotSystem,
		Priority: 80, CacheClass: contextfrag.CacheNever, Trust: contextfrag.TrustSystem, Scope: req.Scope,
		Render:     contextfrag.RenderPolicy{Format: contextfrag.RenderMarkdown},
		Provenance: contextfrag.Provenance{Source: "hook_context", Collector: hookContextCollectorName},
		Parts:      []contextfrag.Part{{Type: contextfrag.PartText, Text: cfg.Text}},
	}}, nil
}
