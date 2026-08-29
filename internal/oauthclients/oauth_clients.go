package oauthclients

import (
	"log/slog"
	"os"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/felinics/memoh/internal/config"
)

type Client struct {
	DisplayName           string   `toml:"display_name"`
	ClientID              string   `toml:"client_id"`
	ClientSecret          string   `toml:"client_secret"` //nolint:gosec // Server-side OAuth client secret loaded from operator config.
	AuthorizationEndpoint string   `toml:"authorization_endpoint"`
	TokenEndpoint         string   `toml:"token_endpoint"`
	RedirectURI           string   `toml:"redirect_uri"`
	AllowedScopes         []string `toml:"allowed_scopes"`
}

type Resolver interface {
	Get(ref string) (Client, bool)
	HasUsableClient(ref string) bool
}

type Registry struct {
	clients map[string]Client
	logger  *slog.Logger
}

func NewRegistry(log *slog.Logger, cfg config.Config) *Registry {
	if log == nil {
		log = slog.Default()
	}
	registry := &Registry{
		clients: map[string]Client{},
		logger:  log.With(slog.String("registry", "oauth_clients")),
	}
	path := cfg.OAuthClients.Path()
	if strings.TrimSpace(path) == "" {
		return registry
	}
	//nolint:gosec // OAuth client config path is controlled by server configuration.
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			registry.logger.Warn("failed to read OAuth clients config", slog.String("path", path), slog.Any("error", err))
		}
		return registry
	}
	var raw struct {
		Clients map[string]Client `toml:"clients"`
	}
	if _, err := toml.Decode(string(data), &raw); err != nil {
		registry.logger.Warn("failed to parse OAuth clients config", slog.String("path", path), slog.Any("error", err))
		return registry
	}
	for key, client := range raw.Clients {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		client.DisplayName = expand(client.DisplayName)
		client.ClientID = expand(client.ClientID)
		client.ClientSecret = expand(client.ClientSecret)
		client.AuthorizationEndpoint = expand(client.AuthorizationEndpoint)
		client.TokenEndpoint = expand(client.TokenEndpoint)
		client.RedirectURI = expand(client.RedirectURI)
		registry.clients[key] = client
	}
	return registry
}

func (r *Registry) Get(ref string) (Client, bool) {
	if r == nil {
		return Client{}, false
	}
	client, ok := r.clients[strings.TrimSpace(ref)]
	return client, ok
}

func (r *Registry) HasUsableClient(ref string) bool {
	client, ok := r.Get(ref)
	return ok && strings.TrimSpace(client.ClientID) != ""
}

func expand(value string) string {
	return os.ExpandEnv(strings.TrimSpace(value))
}
