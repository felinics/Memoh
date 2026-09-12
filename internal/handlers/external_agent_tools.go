package handlers

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/labstack/echo/v4"
)

func buildExternalAgentToolsURL(c echo.Context, botID string) string {
	if c == nil {
		return ""
	}
	return buildExternalAgentToolsURLFromRequest(c.Request(), botID)
}

func buildExternalAgentToolsURLFromRequest(req *http.Request, botID string) string {
	// Keep the legacy environment keys for existing deployments; this URL serves all external agents.
	if raw := strings.TrimSpace(os.Getenv("MEMOH_ACP_MCP_HTTP_URL")); raw != "" {
		if strings.Contains(raw, "{bot_id}") {
			return strings.ReplaceAll(raw, "{bot_id}", url.PathEscape(strings.TrimSpace(botID)))
		}
		return raw
	}
	base := strings.TrimSpace(os.Getenv("MEMOH_ACP_MCP_HTTP_BASE_URL"))
	if base == "" {
		base = localRequestBaseURL(req)
	}
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return ""
	}
	return base + "/bots/" + url.PathEscape(strings.TrimSpace(botID)) + "/tools"
}

func localRequestBaseURL(req *http.Request) string {
	if req == nil {
		return ""
	}
	proto := "http"
	if req.TLS != nil {
		proto = "https"
	}
	host := strings.TrimSpace(req.Host)
	if host == "" {
		return ""
	}
	if !isLoopbackRequestHost(host) {
		return ""
	}
	return proto + "://" + host
}

func isLoopbackRequestHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" || strings.Contains(host, "/") {
		return false
	}
	name := host
	if splitHost, _, err := net.SplitHostPort(host); err == nil {
		name = splitHost
	}
	name = strings.Trim(strings.TrimSpace(name), "[]")
	if strings.EqualFold(name, "localhost") {
		return true
	}
	ip := net.ParseIP(name)
	return ip != nil && ip.IsLoopback()
}
