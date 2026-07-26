// Package catalog assembles the built-in external platform adapters.
// Split Server composition imports the parent adapter package, never this
// concrete catalog.
package catalog

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	userinput "github.com/memohai/memoh/domains/agent/decision/input"
	"github.com/memohai/memoh/domains/api/http/chat/local"
	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/domains/channel/identity"
	"github.com/memohai/memoh/domains/channel/internal/adapter/dingtalk"
	"github.com/memohai/memoh/domains/channel/internal/adapter/discord"
	"github.com/memohai/memoh/domains/channel/internal/adapter/feishu"
	"github.com/memohai/memoh/domains/channel/internal/adapter/line"
	"github.com/memohai/memoh/domains/channel/internal/adapter/matrix"
	"github.com/memohai/memoh/domains/channel/internal/adapter/misskey"
	"github.com/memohai/memoh/domains/channel/internal/adapter/qq"
	slackadapter "github.com/memohai/memoh/domains/channel/internal/adapter/slack"
	"github.com/memohai/memoh/domains/channel/internal/adapter/telegram"
	"github.com/memohai/memoh/domains/channel/internal/adapter/wechatoa"
	"github.com/memohai/memoh/domains/channel/internal/adapter/wecom"
	"github.com/memohai/memoh/domains/channel/internal/adapter/weixin"
	publicmedia "github.com/memohai/memoh/domains/channel/internal/http/media"
	"github.com/memohai/memoh/domains/channel/route"
	"github.com/memohai/memoh/domains/channel/webhook"
	"github.com/memohai/memoh/domains/media/asset"
	"github.com/memohai/memoh/internal/config"
)

// Deps are the explicit inputs required to construct and wire platform
// adapters. Adapter-specific persistence hooks stay inside this catalog.
type Deps struct {
	Log           *slog.Logger
	Config        config.Config
	Hub           *local.RouteHub
	MediaService  *asset.Service
	TunnelManager webhook.Manager
	UserInput     *userinput.Service
	Identities    *identity.Service
	Routes        *route.DBService
}

// NewRegistry registers all external platform adapters plus the local Web
// adapter.
func NewRegistry(deps Deps) *gateway.Registry {
	registry := gateway.NewRegistry()

	tgAdapter := telegram.NewTelegramAdapter(deps.Log)
	tgAdapter.SetAssetOpener(deps.MediaService)
	tgAdapter.SetUserInputService(deps.UserInput)
	registry.MustRegister(tgAdapter)

	discordAdapter := discord.NewDiscordAdapter(deps.Log)
	discordAdapter.SetAssetOpener(deps.MediaService)
	registry.MustRegister(discordAdapter)

	qqAdapter := qq.NewQQAdapter(deps.Log)
	qqAdapter.SetAssetOpener(deps.MediaService)
	qqAdapter.SetChannelIdentityResolver(deps.Identities)
	qqAdapter.SetRouteResolver(deps.Routes)
	registry.MustRegister(qqAdapter)

	matrixAdapter := matrix.NewMatrixAdapter(deps.Log)
	matrixAdapter.SetAssetOpener(deps.MediaService)
	registry.MustRegister(matrixAdapter)

	feishuAdapter := feishu.NewFeishuAdapter(deps.Log)
	feishuAdapter.SetAssetOpener(deps.MediaService)
	registry.MustRegister(feishuAdapter)

	slackAdapter := slackadapter.NewSlackAdapter(deps.Log)
	slackAdapter.SetAssetOpener(deps.MediaService)
	registry.MustRegister(slackAdapter)

	registry.MustRegister(wecom.NewWeComAdapter(deps.Log))
	registry.MustRegister(dingtalk.NewDingTalkAdapter(deps.Log))
	registry.MustRegister(wechatoa.NewWeChatOAAdapter(deps.Log))

	lineAdapter := line.NewAdapter(deps.Log)
	lineAdapter.SetPublicBaseURLProvider(newPublicMediaBaseProvider(deps.Config, deps.TunnelManager))
	registry.MustRegister(lineAdapter)

	weixinAdapter := weixin.NewWeixinAdapter(deps.Log)
	weixinAdapter.SetAssetOpener(deps.MediaService)
	registry.MustRegister(weixinAdapter)
	registry.MustRegister(local.NewWebAdapter(deps.Hub))
	registry.MustRegister(misskey.NewMisskeyAdapter(deps.Log))
	return registry
}

// WirePersistence attaches adapter-specific persistence after the registry and
// channel store have both been constructed. Keeping this edge out of
// NewRegistry avoids the Registry -> Store -> Registry constructor cycle.
func WirePersistence(registry *gateway.Registry, store *gateway.Store) error {
	if registry == nil {
		return errors.New("wire platform adapter persistence: registry is nil")
	}
	if store == nil {
		return errors.New("wire platform adapter persistence: channel store is nil")
	}
	adapter, ok := registry.Get(matrix.Type)
	if !ok {
		return errors.New("wire platform adapter persistence: matrix adapter is not registered")
	}
	matrixAdapter, ok := adapter.(*matrix.MatrixAdapter)
	if !ok {
		return fmt.Errorf("wire platform adapter persistence: matrix adapter has type %T", adapter)
	}
	matrixAdapter.SetSyncStateSaver(store.SaveMatrixSyncSinceToken)
	return nil
}

// NewWeixinQRServerHandler constructs the Weixin QR management HTTP handler.
func NewWeixinQRServerHandler(log *slog.Logger, lifecycle *gateway.Lifecycle) *weixin.QRHandler {
	return weixin.NewQRServerHandler(log, lifecycle)
}

type publicMediaBaseProvider struct {
	tunnel webhook.Manager
	signer *publicmedia.Signer
}

func newPublicMediaBaseProvider(cfg config.Config, tunnel webhook.Manager) *publicMediaBaseProvider {
	return &publicMediaBaseProvider{
		tunnel: tunnel,
		signer: publicmedia.NewSigner(cfg.Auth.JWTSecret, publicmedia.SignedURLTTL),
	}
}

func (p *publicMediaBaseProvider) PublicBaseURL() string {
	if p == nil {
		return ""
	}
	if p.tunnel != nil {
		return p.tunnel.PublicBaseURL()
	}
	return ""
}

func (p *publicMediaBaseProvider) SignPublicMediaPath(path string) (string, bool) {
	if p == nil || p.signer == nil {
		return "", false
	}
	return p.signer.SignPath(path, time.Now().UTC())
}
