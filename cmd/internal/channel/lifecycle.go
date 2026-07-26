package channel

import (
	"go.uber.org/fx"

	"github.com/memohai/memoh/domains/channel/inbound"
)

func registerDiscussLifecycle(lifecycle fx.Lifecycle, driver inbound.DiscussDriver) {
	lifecycle.Append(fx.Hook{
		OnStop: driver.Shutdown,
	})
}
