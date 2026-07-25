package qq

import (
	"log/slog"

	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/domains/channel/identity"
	"github.com/memohai/memoh/domains/channel/route"
)

func ProvideQQAdapter(log *slog.Logger, opener assetOpener, identityService *identity.Service, routeService *route.DBService) gateway.Adapter {
	adapter := NewQQAdapter(log)
	adapter.SetAssetOpener(opener)
	adapter.SetChannelIdentityResolver(identityService)
	adapter.SetRouteResolver(routeService)
	return adapter
}
