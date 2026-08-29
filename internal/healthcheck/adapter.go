package healthcheck

import (
	"context"

	"github.com/felinics/memoh/internal/bots"
)

// RuntimeCheckerAdapter bridges Checker to bots.RuntimeChecker.
type RuntimeCheckerAdapter struct {
	checker Checker
}

// NewRuntimeCheckerAdapter creates a runtime checker bridge.
func NewRuntimeCheckerAdapter(checker Checker) *RuntimeCheckerAdapter {
	return &RuntimeCheckerAdapter{checker: checker}
}

// ListChecks evaluates checks and maps healthcheck results to bots check shape.
func (a *RuntimeCheckerAdapter) ListChecks(ctx context.Context, botID string) []bots.BotCheck {
	if a == nil || a.checker == nil {
		return []bots.BotCheck{}
	}
	items := a.checker.ListChecks(ctx, botID)
	result := make([]bots.BotCheck, 0, len(items))
	for _, item := range items {
		result = append(result, bots.BotCheck{
			ID:       item.ID,
			Type:     item.Type,
			TitleKey: item.TitleKey,
			Subtitle: item.Subtitle,
			Status:   item.Status,
			Summary:  item.Summary,
			Detail:   item.Detail,
			Metadata: item.Metadata,
		})
	}
	return result
}
