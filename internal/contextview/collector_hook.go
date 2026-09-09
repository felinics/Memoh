package contextview

import (
	"context"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

const hookContextCollectorName = "hook_context"

const maxHookContextChars = 8 * 1024

type HookContextConfig struct {
	// Text is the combined resolver-hook AppendContext text materialized by
	// the application. The collector owns its role, placement, and cap.
	Text string
}

type HookContextCollector struct{}

func (*HookContextCollector) Name() string {
	return hookContextCollectorName
}

func (*HookContextCollector) Collect(_ context.Context, req CollectRequest) ([]contextfrag.ContextFrag, error) {
	cfg, err := hookContextConfig(req.Config)
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(cfg.Text)
	if text == "" {
		return nil, nil
	}
	msg := sdk.UserMessage(text)
	return []contextfrag.ContextFrag{contextfrag.MessageFrag(contextfrag.MessageFragInput{
		ID:         "hook_context.message",
		Message:    msg,
		Kind:       contextfrag.KindHookContext,
		Slot:       contextfrag.SlotAfterHistoryBeforeCurrent,
		Priority:   contextfrag.PriorityForMessage(msg),
		CacheClass: contextfrag.CacheNever,
		Trust:      contextfrag.TrustWorkspace,
		Scope:      req.Scope,
		Source:     hookContextCollectorName,
		SourceID:   hookContextCollectorName,
		Collector:  hookContextCollectorName,
		Budget: contextfrag.BudgetPolicy{
			MaxChars: maxHookContextChars,
			Overflow: contextfrag.OverflowDrop,
		},
	})}, nil
}

func hookContextConfig(config any) (HookContextConfig, error) {
	return collectorConfig[HookContextConfig](config, "hook_context config must be HookContextConfig")
}
