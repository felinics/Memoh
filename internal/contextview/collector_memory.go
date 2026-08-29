package contextview

import (
	"context"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

const memoryContextCollectorName = "memory_context"

type MemoryContextConfig struct {
	Text    string
	Message *sdk.Message
	Index   int
}

type MemoryContextCollector struct{}

func (*MemoryContextCollector) Name() string {
	return memoryContextCollectorName
}

func (*MemoryContextCollector) Collect(_ context.Context, req CollectRequest) ([]contextfrag.ContextFrag, error) {
	cfg, err := collectorConfig[MemoryContextConfig](req.Config, "memory_context config must be MemoryContextConfig")
	if err != nil {
		return nil, err
	}
	var msg sdk.Message
	if cfg.Message != nil {
		msg = *cfg.Message
	} else {
		if cfg.Text == "" {
			return nil, nil
		}
		msg = sdk.UserMessage(cfg.Text)
	}
	return []contextfrag.ContextFrag{contextfrag.MessageFrag(contextfrag.MessageFragInput{
		ID: "memory.recall", Message: msg, Kind: contextfrag.KindMemoryRecall, Slot: contextfrag.SlotHistory,
		Priority: 60, CacheClass: contextfrag.CacheNever, Trust: contextfrag.TrustWorkspace,
		Scope: req.Scope, Source: "memory_context", Collector: memoryContextCollectorName, Index: cfg.Index,
	})}, nil
}
