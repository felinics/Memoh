// Package common provides shared utilities for channel adapters.
package common

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/channel"
)

// HTTPProxyConfig holds adapter-scoped HTTP proxy settings.
// Explicit adapter config takes precedence over environment variables.
type HTTPProxyConfig struct {
	URL string
}

// ParseHTTPProxyConfig reads proxy settings from a channel config map.
// It accepts both camelCase and snake_case keys.
func ParseHTTPProxyConfig(raw map[string]any) (HTTPProxyConfig, error) {
	cfg := HTTPProxyConfig{
		URL: strings.TrimSpace(channel.ReadString(raw, "httpProxyUrl", "http_proxy_url")),
	}
	if cfg.URL == "" {
		return cfg, nil
	}
	parsed, err := parseProxyURL(cfg.URL)
	if err != nil {
		return HTTPProxyConfig{}, err
	}
	cfg.URL = parsed.String()
	return cfg, nil
}

// NormalizeHTTPProxyConfig writes canonical proxy fields into the provided map.
func NormalizeHTTPProxyConfig(out map[string]any, cfg HTTPProxyConfig) {
	if out == nil {
		return
	}
	if cfg.URL != "" {
		out["httpProxyUrl"] = cfg.URL
	}
}

// CacheKey returns a stable cache key segment for proxy-aware client caches.
func (c HTTPProxyConfig) CacheKey() string {
	return c.URL
}

// BuildProxyURL returns the proxy URL with optional explicit credentials applied.
func (c HTTPProxyConfig) BuildProxyURL() (*url.URL, error) {
	if strings.TrimSpace(c.URL) == "" {
		return nil, nil
	}
	return parseProxyURL(c.URL)
}

// NewHTTPClient constructs an HTTP client using explicit adapter proxy settings
// when present, otherwise falling back to ProxyFromEnvironment.
func NewHTTPClient(timeout time.Duration, proxyCfg HTTPProxyConfig) (*http.Client, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if strings.TrimSpace(proxyCfg.URL) != "" {
		proxyURL, err := proxyCfg.BuildProxyURL()
		if err != nil {
			return nil, err
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	} else {
		transport.Proxy = http.ProxyFromEnvironment
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}, nil
}

func parseProxyURL(raw string) (*url.URL, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, nil
	}
	if !strings.Contains(value, "://") {
		value = "http://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return nil, fmt.Errorf("invalid http proxy url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("unsupported http proxy scheme: %s", parsed.Scheme)
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return nil, errors.New("http proxy host is required")
	}
	return parsed, nil
}
