package plugins

import (
	"log/slog"

	"github.com/memohai/memoh/internal/config"
	"github.com/memohai/memoh/internal/oauth"
)

type (
	OAuthClient         = oauth.Client
	OAuthClientRegistry = oauth.Registry
)

func NewOAuthClientRegistry(log *slog.Logger, cfg config.Config) *OAuthClientRegistry {
	return oauth.NewRegistry(log, cfg)
}
