package contextview

import (
	"context"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

const currentUserCollectorName = "current_user"

type CurrentUserConfig struct {
	Query string
}

type CurrentUserCollector struct{}

func (*CurrentUserCollector) Name() string {
	return currentUserCollectorName
}

func (*CurrentUserCollector) Collect(_ context.Context, req CollectRequest) ([]contextfrag.ContextFrag, error) {
	cfg, err := currentUserConfig(req.Config)
	if err != nil {
		return nil, err
	}
	if cfg.Query == "" {
		return nil, nil
	}
	msg := sdk.UserMessage(cfg.Query)
	return []contextfrag.ContextFrag{contextfrag.MessageFrag(contextfrag.MessageFragInput{
		ID:         "current_user.message",
		Message:    msg,
		Kind:       contextfrag.KindCurrentUserMessage,
		Slot:       contextfrag.SlotCurrentUser,
		Priority:   90,
		CacheClass: contextfrag.CacheNever,
		Trust:      contextfrag.TrustUser,
		Scope:      req.Scope,
		Source:     contextfrag.SourceRunConfig,
		Collector:  currentUserCollectorName,
		Budget:     contextfrag.BudgetPolicy{Overflow: contextfrag.OverflowKeep},
	})}, nil
}

func currentUserConfig(config any) (CurrentUserConfig, error) {
	return collectorConfig[CurrentUserConfig](config, "current_user config must be CurrentUserConfig")
}
