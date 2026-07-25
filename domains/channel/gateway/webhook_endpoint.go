package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const maxWebhookEndpointLength = 500

var (
	ErrWebhookEndpointUnsupported = errors.New("channel webhook endpoint update is not supported")
	ErrInvalidWebhookEndpoint     = errors.New("invalid webhook endpoint")
)

type SetWebhookEndpointRequest struct {
	Endpoint string `json:"endpoint"`
}

type SetWebhookEndpointResponse struct {
	Endpoint string `json:"endpoint"`
}

func (s *Store) SetWebhookEndpoint(ctx context.Context, botID string, channelType ChannelType, req SetWebhookEndpointRequest) (SetWebhookEndpointResponse, error) {
	if s == nil || s.queries == nil {
		return SetWebhookEndpointResponse{}, errors.New("channel queries not configured")
	}
	setter, ok := s.registry.GetWebhookEndpointSetter(channelType)
	if !ok {
		return SetWebhookEndpointResponse{}, fmt.Errorf("%w: %s", ErrWebhookEndpointUnsupported, channelType)
	}
	cfg, err := s.ResolveEffectiveConfig(ctx, botID, channelType)
	if err != nil {
		return SetWebhookEndpointResponse{}, err
	}
	endpoint, err := normalizeWebhookEndpoint(req.Endpoint, channelType, cfg.ID)
	if err != nil {
		return SetWebhookEndpointResponse{}, err
	}
	if err := setter.SetWebhookEndpoint(ctx, cfg.Credentials, endpoint); err != nil {
		return SetWebhookEndpointResponse{}, err
	}
	return SetWebhookEndpointResponse{Endpoint: endpoint}, nil
}

func normalizeWebhookEndpoint(raw string, channelType ChannelType, configID string) (string, error) {
	endpoint := strings.TrimSpace(raw)
	if endpoint == "" {
		return "", fmt.Errorf("%w: endpoint is required", ErrInvalidWebhookEndpoint)
	}
	if len(endpoint) > maxWebhookEndpointLength {
		return "", fmt.Errorf("%w: endpoint must be at most %d characters", ErrInvalidWebhookEndpoint, maxWebhookEndpointLength)
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidWebhookEndpoint, err)
	}
	if u.Scheme != "https" || strings.TrimSpace(u.Host) == "" {
		return "", fmt.Errorf("%w: endpoint must be a public HTTPS URL", ErrInvalidWebhookEndpoint)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%w: endpoint must not include userinfo, query, or fragment", ErrInvalidWebhookEndpoint)
	}
	if u.Port() != "" {
		return "", fmt.Errorf("%w: endpoint must not include a port", ErrInvalidWebhookEndpoint)
	}
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(u.Hostname()), "."))
	if !IsPublicHost(host) {
		return "", fmt.Errorf("%w: endpoint host must be public", ErrInvalidWebhookEndpoint)
	}
	if err := validateWebhookEndpointPath(u.EscapedPath(), channelType, configID); err != nil {
		return "", err
	}
	u.Host = host
	u.RawPath = ""
	return u.String(), nil
}

func validateWebhookEndpointPath(escapedPath string, channelType ChannelType, configID string) error {
	segments := strings.Split(strings.Trim(escapedPath, "/"), "/")
	if len(segments) != 4 {
		return fmt.Errorf("%w: endpoint path must match /channels/%s/webhook/%s", ErrInvalidWebhookEndpoint, channelType, configID)
	}
	decoded := make([]string, 0, len(segments))
	for _, segment := range segments {
		value, err := url.PathUnescape(segment)
		if err != nil {
			return fmt.Errorf("%w: invalid escaped path", ErrInvalidWebhookEndpoint)
		}
		decoded = append(decoded, value)
	}
	if decoded[0] != "channels" || decoded[1] != channelType.String() || decoded[2] != "webhook" || decoded[3] != configID {
		return fmt.Errorf("%w: endpoint path must match /channels/%s/webhook/%s", ErrInvalidWebhookEndpoint, channelType, configID)
	}
	return nil
}
