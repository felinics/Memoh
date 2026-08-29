package models

import (
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/version"
)

const (
	DefaultProviderRequestTimeout      = 2 * time.Minute
	DefaultProviderProbeTimeout        = 60 * time.Second
	DefaultProviderTLSHandshakeTimeout = 30 * time.Second

	providerUserAgentProduct = "Memoh"
)

var defaultProviderTransport = newDefaultProviderTransport()

// NewProviderHTTPClient returns an HTTP client for model/provider traffic.
// When timeout is zero or negative, the caller is expected to enforce limits
// via context deadlines, which keeps streaming responses unbounded by the
// client's global timeout while still using the relaxed TLS handshake window.
func NewProviderHTTPClient(timeout time.Duration) *http.Client {
	client := &http.Client{Transport: &providerUserAgentRoundTripper{
		base:      defaultProviderTransport,
		userAgent: DefaultProviderUserAgent(),
	}}
	if timeout > 0 {
		client.Timeout = timeout
	}
	return client
}

// DefaultProviderUserAgent is the project-level User-Agent for outbound
// model/provider traffic.
func DefaultProviderUserAgent() string {
	versionToken := sanitizeUserAgentToken(version.Version)
	if versionToken == "" {
		versionToken = "dev"
	}
	buildToken := sanitizeUserAgentCommentValue(version.ShortCommitHash())
	if buildToken == "" {
		buildToken = "unknown"
	}
	return providerUserAgentProduct + "/" + versionToken +
		" (" + providerUserAgentPlatform() + ") memoh-server (Memoh; " + buildToken + ")"
}

type providerUserAgentRoundTripper struct {
	base      http.RoundTripper
	userAgent string
}

func (rt *providerUserAgentRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") != "" {
		return rt.roundTrip(req)
	}

	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	if clone.Header == nil {
		clone.Header = make(http.Header)
	}
	clone.Header.Set("User-Agent", rt.userAgent)
	return rt.roundTrip(clone)
}

func (rt *providerUserAgentRoundTripper) roundTrip(req *http.Request) (*http.Response, error) {
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

func newDefaultProviderTransport() *http.Transport {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Transport{TLSHandshakeTimeout: DefaultProviderTLSHandshakeTimeout}
	}

	transport := base.Clone()
	if transport.TLSHandshakeTimeout < DefaultProviderTLSHandshakeTimeout {
		transport.TLSHandshakeTimeout = DefaultProviderTLSHandshakeTimeout
	}
	return transport
}

func sanitizeUserAgentToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		if isUserAgentTokenChar(r) {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('-')
	}
	return strings.Trim(b.String(), "-")
}

func providerUserAgentPlatform() string {
	osName := sanitizeUserAgentCommentValue(providerUserAgentOSName())
	arch := sanitizeUserAgentCommentValue(runtime.GOARCH)
	if arch == "" {
		arch = "unknown"
	}
	return osName + " unknown; " + arch
}

func providerUserAgentOSName() string {
	switch runtime.GOOS {
	case "darwin":
		return "Mac OS"
	case "linux":
		return "Linux"
	case "windows":
		return "Windows"
	default:
		return runtime.GOOS
	}
}

func sanitizeUserAgentCommentValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	var b strings.Builder
	b.Grow(len(value))
	lastSpace := false
	for _, r := range value {
		if r <= 31 || r == 127 || r == '(' || r == ')' || r == '\\' {
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
			continue
		}
		b.WriteRune(r)
		lastSpace = false
	}
	return strings.TrimSpace(b.String())
}

func isUserAgentTokenChar(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
		return true
	case r >= 'A' && r <= 'Z':
		return true
	case r >= '0' && r <= '9':
		return true
	}

	switch r {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	default:
		return false
	}
}
