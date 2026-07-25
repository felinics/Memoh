package tool

import (
	"context"

	"github.com/memohai/memoh/domains/api/setting"
)

// BotSettingsReader is the narrow settings surface used by tool providers.
type BotSettingsReader interface {
	GetBot(context.Context, string) (setting.Settings, error)
}
