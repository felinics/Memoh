package healthcheck

import (
	"context"

	"github.com/memohai/memoh/domains/api/bot"
)

// RuntimeCheckerAdapter bridges Checker to bot.RuntimeChecker.
type RuntimeCheckerAdapter struct {
	checker Checker
}

// NewRuntimeCheckerAdapter creates a runtime checker bridge.
func NewRuntimeCheckerAdapter(checker Checker) *RuntimeCheckerAdapter {
	return &RuntimeCheckerAdapter{checker: checker}
}

// ListChecks evaluates checks and maps healthcheck results to bots check shape.
func (a *RuntimeCheckerAdapter) ListChecks(ctx context.Context, botID string) []bot.BotCheck {
	if a == nil || a.checker == nil {
		return []bot.BotCheck{}
	}
	items := a.checker.ListChecks(ctx, botID)
	result := make([]bot.BotCheck, 0, len(items))
	for _, item := range items {
		result = append(result, bot.BotCheck{
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
