// Package platformreg assembles external platform adapters into a Channel Registry.
// Split Server composition must not import this package.
package platformreg

import (
	"log/slog"
	"time"

	userinput "github.com/memohai/memoh/domains/agent/decision/input"
	"github.com/memohai/memoh/domains/api/http/chat/local"
	"github.com/memohai/memoh/domains/channel/gateway"
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
	"github.com/memohai/memoh/domains/channel/publicmedia"
	"github.com/memohai/memoh/domains/channel/webhook"
	"github.com/memohai/memoh/domains/media/asset"
	"github.com/memohai/memoh/internal/config"
)

// Deps are the explicit inputs required to register platform adapters.
type Deps struct {
	Log           *slog.Logger
	Config        config.Config
	Hub           *local.RouteHub
	MediaService  *asset.Service
	TunnelManager webhook.Manager
	UserInput     *userinput.Service
}

// NewRegistry registers all external platform adapters plus the local Web adapter.
func NewRegistry(deps Deps) *gateway.Registry {
	log := deps.Log
	cfg := deps.Config
	hub := deps.Hub
	mediaService := deps.MediaService
	tunnelManager := deps.TunnelManager
	userInput := deps.UserInput
	registry := gateway.NewRegistry()

	tgAdapter := telegram.NewTelegramAdapter(log)
	tgAdapter.SetAssetOpener(mediaService)
	tgAdapter.SetUserInputService(userInput)
	registry.MustRegister(tgAdapter)

	discordAdapter := discord.NewDiscordAdapter(log)
	discordAdapter.SetAssetOpener(mediaService)
	registry.MustRegister(discordAdapter)

	qqAdapter := qq.NewQQAdapter(log)
	qqAdapter.SetAssetOpener(mediaService)
	registry.MustRegister(qqAdapter)

	matrixAdapter := matrix.NewMatrixAdapter(log)
	matrixAdapter.SetAssetOpener(mediaService)
	registry.MustRegister(matrixAdapter)

	feishuAdapter := feishu.NewFeishuAdapter(log)
	feishuAdapter.SetAssetOpener(mediaService)
	registry.MustRegister(feishuAdapter)

	slackAdapter := slackadapter.NewSlackAdapter(log)
	slackAdapter.SetAssetOpener(mediaService)
	registry.MustRegister(slackAdapter)

	registry.MustRegister(wecom.NewWeComAdapter(log))
	registry.MustRegister(dingtalk.NewDingTalkAdapter(log))
	registry.MustRegister(wechatoa.NewWeChatOAAdapter(log))

	lineAdapter := line.NewAdapter(log)
	lineAdapter.SetPublicBaseURLProvider(newPublicMediaBaseProvider(cfg, tunnelManager))
	registry.MustRegister(lineAdapter)

	weixinAdapter := weixin.NewWeixinAdapter(log)
	weixinAdapter.SetAssetOpener(mediaService)
	registry.MustRegister(weixinAdapter)
	registry.MustRegister(local.NewWebAdapter(hub))
	registry.MustRegister(misskey.NewMisskeyAdapter(log))
	return registry
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
