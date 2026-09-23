package handlers

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"golang.org/x/net/html"
)

// SiteIconHandler resolves public page icon metadata; it never forwards account credentials.
type (
	SiteIconHandler  struct{ client *http.Client }
	SiteIconResponse struct {
		URL string `json:"url"`
	}
)

func NewSiteIconHandler() *SiteIconHandler {
	transport := &http.Transport{DialContext: dialIconHost, TLSHandshakeTimeout: 5 * time.Second, MaxIdleConns: 16, IdleConnTimeout: 30 * time.Second}
	return &SiteIconHandler{client: &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !validIconURL(r.URL) {
			return errors.New("unsupported redirect")
		}
		return nil
	}}}
}
func (h *SiteIconHandler) Register(e *echo.Echo) { e.GET("/site-icon", h.Get) }

// Get resolves a public page favicon.
//
// @Summary Resolve a public page favicon for a color scheme
// @Tags site-icon
// @Param url query string true "Public page URL"
// @Param theme query string false "light or dark"
// @Success 200 {object} SiteIconResponse
// @Router /site-icon [get]
//
// Discovery failures return an empty URL so callers can retain their fallback icon.
func (h *SiteIconHandler) Get(c echo.Context) error {
	u, err := url.Parse(c.QueryParam("url"))
	if err != nil || !validIconURL(u) {
		return c.JSON(http.StatusOK, SiteIconResponse{})
	}
	theme := "light"
	if c.QueryParam("theme") == "dark" {
		theme = "dark"
	}
	req, err := http.NewRequestWithContext(c.Request().Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		return c.JSON(http.StatusOK, SiteIconResponse{})
	}
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Sec-CH-Prefers-Color-Scheme", theme)
	// The transport validates and pins public IPs on every connection, including redirects.
	response, err := h.client.Do(req) //nolint:gosec // dialIconHost prevents SSRF to private networks.
	if err != nil {
		return c.JSON(http.StatusOK, SiteIconResponse{})
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return c.JSON(http.StatusOK, SiteIconResponse{})
	}
	icon := discoverIcon(io.LimitReader(response.Body, 1<<20), response.Request.URL, theme)
	c.Response().Header().Set("Cache-Control", "private, max-age=300")
	return c.JSON(http.StatusOK, SiteIconResponse{URL: icon})
}

func validIconURL(u *url.URL) bool {
	if u == nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil {
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

func publicIconIP(ip net.IP) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() &&
		(ip.To4() == nil || ip.To4()[0] != 100 || ip.To4()[1] < 64 || ip.To4()[1] > 127)
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

func discoverIcon(body io.Reader, page *url.URL, theme string) string {
	base := page
	fallback := page.ResolveReference(&url.URL{Path: "/favicon.ico"}).String()
	best, bestScore := fallback, -1
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
		if token.Data != "link" || attrs["href"] == "" {
			continue
		}
		rel := strings.Fields(strings.ToLower(attrs["rel"]))
		isIcon := false
		for _, r := range rel {
			if r == "icon" {
				isIcon = true
			}
		}
		if !isIcon {
			continue
		}
		media := strings.ToLower(strings.ReplaceAll(attrs["media"], " ", ""))
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
		href := attrs["href"]
		// GitHub switches its favicon in JavaScript using data-base-href and
		// matchMedia. Its static HTML has no themed media declaration.
		if page.Hostname() == "github.com" && attrs["type"] == "image/svg+xml" && attrs["data-base-href"] != "" {
			suffix := ".svg"
			if theme == "dark" {
				suffix = "-dark.svg"
			}
			href = attrs["data-base-href"] + suffix
		}
		candidate, err := url.Parse(href)
		if err != nil {
			continue
		}
		candidate = base.ResolveReference(candidate)
		if validIconURL(candidate) && score >= bestScore {
			best = candidate.String()
			bestScore = score
		}
	}
	return best
}
