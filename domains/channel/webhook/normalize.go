package webhook

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/memohai/memoh/domains/channel/gateway"
)

// NormalizeConfiguredPublicBase validates an operator-provided public base URL.
// Configured public bases must be public HTTPS origins without path prefixes,
// ports, userinfo, query, or fragment.
func NormalizeConfiguredPublicBase(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", errors.New("public base url is empty")
	}
	u, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("parse public base url: %w", err)
	}
	if u.Scheme != "https" || strings.TrimSpace(u.Host) == "" {
		return "", errors.New("public base url must be HTTPS")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("public base url must not include userinfo, query, or fragment")
	}
	if path := strings.TrimSpace(u.EscapedPath()); path != "" && path != "/" {
		return "", errors.New("public base url must not include a path")
	}
	if u.Port() != "" {
		return "", errors.New("public base url must not include a port")
	}
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(u.Hostname()), "."))
	if !gateway.IsPublicHost(host) {
		return "", errors.New("public base url host must be public")
	}
	return "https://" + host, nil
}
