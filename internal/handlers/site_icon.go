package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"golang.org/x/net/html"
	"golang.org/x/sync/singleflight"
)

// Icons are a per-site property, so discovery is keyed by origin: a chat full of
// links to one site costs one outbound fetch, and the page path the user shared
// is never sent to that site. Results, including "no icon", are cached so
// rendering history or switching themes does not fan out new requests. A site
// that answered without a usable page is retried after an hour; timeouts, 5xx
// and rate limits are likely transient, so they are retried sooner rather than
// hiding the icon for the full hour.
const (
	siteIconCacheTTL          = 24 * time.Hour
	siteIconFailureCacheTTL   = time.Hour
	siteIconTransientCacheTTL = 5 * time.Minute
	siteIconCacheMaxEntries   = 1024
)

// SiteIconHandler resolves public site icon metadata; it never forwards account credentials.
type (
	SiteIconHandler struct {
		client  *http.Client
		fetch   func(ctx context.Context, origin string) (SiteIconResponse, time.Duration)
		now     func() time.Time
		flights singleflight.Group
		mu      sync.Mutex
		cache   map[string]siteIconEntry
	}
	// SiteIconResponse holds the icon for each color scheme. An empty value
	// means no icon was found and callers keep their fallback icon.
	SiteIconResponse struct {
		Light string `json:"light"`
		Dark  string `json:"dark"`
	}
	siteIconEntry struct {
		icons   SiteIconResponse
		expires time.Time
	}
)

var siteIconThemes = []string{"light", "dark"}

// Fetches leave this server process directly: the transport resolves and dials
// validated IPs itself and ignores HTTP(S)_PROXY. A hosted deployment whose
// egress requires an explicit proxy, or cannot reach the linked sites, gets
// empty results and the fallback icon rather than an error, so check the
// server's egress when deploying there. Any signed-in user can make the server
// fetch a public site root from that egress IP; per-origin caching bounds
// repeats but there is no per-user rate limit.
func NewSiteIconHandler() *SiteIconHandler {
	transport := &http.Transport{DialContext: dialIconHost, TLSHandshakeTimeout: 5 * time.Second, MaxIdleConns: 16, IdleConnTimeout: 30 * time.Second}
	h := &SiteIconHandler{
		client: &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= 5 || !validIconURL(r.URL) {
				return errors.New("unsupported redirect")
			}
			return nil
		}},
		now:   time.Now,
		cache: map[string]siteIconEntry{},
	}
	h.fetch = h.discover
	return h
}
func (h *SiteIconHandler) Register(e *echo.Echo) { e.GET("/site-icon", h.Get) }

// Get resolves a public site's favicons.
//
// @Summary Resolve a public site's favicons for light and dark color schemes
// @Tags site-icon
// @Param url query string true "Public page or site URL; only its origin is fetched"
// @Success 200 {object} SiteIconResponse
// @Router /site-icon [get]
//
// Discovery failures return empty URLs so callers can retain their fallback icon.
func (h *SiteIconHandler) Get(c echo.Context) error {
	origin, ok := siteIconOrigin(c.QueryParam("url"))
	if !ok {
		return c.JSON(http.StatusOK, SiteIconResponse{})
	}
	if entry, ok := h.cached(origin); ok {
		return h.siteIconJSON(c, entry)
	}
	// Concurrent requests for one origin share a fetch. It is detached from the
	// first caller's request so that caller leaving does not fail the others;
	// the client timeout still bounds it.
	result, _, _ := h.flights.Do(origin, func() (any, error) {
		if entry, ok := h.cached(origin); ok {
			return entry, nil
		}
		icons, ttl := h.fetch(context.WithoutCancel(c.Request().Context()), origin)
		return h.store(origin, icons, ttl), nil
	})
	return h.siteIconJSON(c, result.(siteIconEntry))
}

// siteIconOrigin reduces a link to the site root that is actually fetched.
func siteIconOrigin(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || !validIconURL(u) {
		return "", false
	}
	return (&url.URL{Scheme: u.Scheme, Host: strings.ToLower(u.Host), Path: "/"}).String(), true
}

// The browser may keep the response only as long as the server entry lives,
// capped at an hour, so a short-lived transient miss is not pinned in the
// browser cache after the server would already retry.
func (h *SiteIconHandler) siteIconJSON(c echo.Context, entry siteIconEntry) error {
	maxAge := min(entry.expires.Sub(h.now()), time.Hour)
	c.Response().Header().Set("Cache-Control", fmt.Sprintf("private, max-age=%d", max(int(maxAge.Seconds()), 0)))
	return c.JSON(http.StatusOK, entry.icons)
}

// discover returns the icons and how long the result may be cached.
func (h *SiteIconHandler) discover(ctx context.Context, origin string) (SiteIconResponse, time.Duration) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin, nil)
	if err != nil {
		return SiteIconResponse{}, siteIconFailureCacheTTL
	}
	req.Header.Set("Accept", "text/html")
	// The transport validates and pins public IPs on every connection, including redirects.
	response, err := h.client.Do(req) //nolint:gosec // dialIconHost prevents SSRF to private networks.
	if err != nil {
		return SiteIconResponse{}, siteIconTransientCacheTTL
	}
	defer func() { _ = response.Body.Close() }()
	switch {
	case response.StatusCode == http.StatusOK:
		return discoverIcons(io.LimitReader(response.Body, 1<<20), response.Request.URL), siteIconCacheTTL
	case response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError:
		return SiteIconResponse{}, siteIconTransientCacheTTL
	default:
		return SiteIconResponse{}, siteIconFailureCacheTTL
	}
}

func (h *SiteIconHandler) cached(origin string) (siteIconEntry, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	entry, ok := h.cache[origin]
	if !ok || !h.now().Before(entry.expires) {
		return siteIconEntry{}, false
	}
	return entry, true
}

func (h *SiteIconHandler) store(origin string, icons SiteIconResponse, ttl time.Duration) siteIconEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	if len(h.cache) >= siteIconCacheMaxEntries {
		for key, entry := range h.cache {
			if !now.Before(entry.expires) {
				delete(h.cache, key)
			}
		}
	}
	// Still full of live entries: drop an arbitrary one. Icons are cheap to
	// rediscover, so bounding memory matters more than eviction order.
	for key := range h.cache {
		if len(h.cache) < siteIconCacheMaxEntries {
			break
		}
		delete(h.cache, key)
	}
	entry := siteIconEntry{icons: icons, expires: now.Add(ttl)}
	h.cache[origin] = entry
	return entry
}

// Only default web ports are fetched, so the endpoint cannot be used to probe
// arbitrary services on public hosts.
func validIconURL(u *url.URL) bool {
	if u == nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil {
		return false
	}
	if port := u.Port(); port != "" && port != "80" && port != "443" {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return publicIconIP(ip)
	}
	return true
}

// Special-purpose ranges that pass IsGlobalUnicast but can still reach
// internal networks (CGNAT, NAT64 translation, IETF assignments).
// 198.18.0.0/15 is deliberately allowed: fake-IP proxies (Clash, Surge in TUN
// mode) resolve every host into it, so blocking it disables icons for those
// users. Behind such a proxy the real destination is chosen by the proxy, which
// an IP check here cannot see either way.
var nonPublicIconPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
}

func publicIconIP(ip net.IP) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range nonPublicIconPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

// Dial the validated address itself so DNS rebinding cannot bypass the check.
func dialIconHost(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, a := range addresses {
		if !publicIconIP(a.IP) {
			return nil, errors.New("non-public icon host")
		}
	}
	for _, a := range addresses {
		conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(a.IP.String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, errors.New("icon host unavailable")
}

// discoverIcons picks the best declared icon for each color scheme from one
// pass over the page head, falling back to /favicon.ico.
func discoverIcons(body io.Reader, page *url.URL) SiteIconResponse {
	base := page
	fallback := page.ResolveReference(&url.URL{Path: "/favicon.ico"}).String()
	type choice struct {
		url   string
		score int
	}
	best := map[string]*choice{}
	for _, theme := range siteIconThemes {
		best[theme] = &choice{url: fallback, score: -1}
	}
	tokenizer := html.NewTokenizer(body)
	for {
		kind := tokenizer.Next()
		if kind == html.ErrorToken {
			break
		}
		token := tokenizer.Token()
		if kind == html.EndTagToken && token.Data == "head" {
			break
		}
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken {
			continue
		}
		attrs := map[string]string{}
		for _, a := range token.Attr {
			attrs[a.Key] = a.Val
		}
		if token.Data == "base" {
			if u, err := url.Parse(attrs["href"]); err == nil {
				candidate := page.ResolveReference(u)
				if validIconURL(candidate) {
					base = candidate
				}
			}
			continue
		}
		if token.Data != "link" || attrs["href"] == "" || !hasIconRel(attrs["rel"]) {
			continue
		}
		media := strings.ToLower(strings.ReplaceAll(attrs["media"], " ", ""))
		for _, theme := range siteIconThemes {
			score := 0
			if media != "" && media != "all" {
				if media != "(prefers-color-scheme:"+theme+")" {
					continue
				}
				score += 10
			}
			// SVG can carry its own color-scheme media rules and scales at inline size.
			if strings.Contains(attrs["type"], "svg") {
				score++
			}
			candidate, err := url.Parse(themedIconHref(page, attrs, theme))
			if err != nil {
				continue
			}
			candidate = base.ResolveReference(candidate)
			if validIconURL(candidate) && score >= best[theme].score {
				best[theme] = &choice{url: candidate.String(), score: score}
			}
		}
	}
	return SiteIconResponse{Light: best["light"].url, Dark: best["dark"].url}
}

func hasIconRel(rel string) bool {
	for _, r := range strings.Fields(strings.ToLower(rel)) {
		if r == "icon" {
			return true
		}
	}
	return false
}

// GitHub switches its favicon in JavaScript using data-base-href and
// matchMedia, so its static HTML has no themed media declaration. It is special
// cased because it is among the most linked sites in agent replies; other
// script-switched sites keep their static icon.
func themedIconHref(page *url.URL, attrs map[string]string, theme string) string {
	if page.Hostname() != "github.com" || attrs["type"] != "image/svg+xml" || attrs["data-base-href"] == "" {
		return attrs["href"]
	}
	if theme == "dark" {
		return attrs["data-base-href"] + "-dark.svg"
	}
	return attrs["data-base-href"] + ".svg"
}
