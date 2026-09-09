package qq

import (
	"log/slog"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/identities"
	"github.com/felinics/memoh/internal/channel/route"
	"github.com/felinics/memoh/internal/media"
)

func ProvideQQAdapter(log *slog.Logger, mediaService *media.Service, identityService *identities.Service, routeService *route.DBService) channel.Adapter {
	adapter := NewQQAdapter(log)
	adapter.SetAssetOpener(mediaService)
	adapter.SetChannelIdentityResolver(identityService)
	adapter.SetRouteResolver(routeService)
	return adapter
}
