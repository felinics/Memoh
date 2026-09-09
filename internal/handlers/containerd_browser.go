package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

const (
	browserSessionIdleTTL = 30 * time.Minute
	browserProxySeparator = ".browser."
)

type browserSessionCreateRequest struct {
	Port int    `json:"port"`
	Path string `json:"path,omitempty"`
}

type browserSessionCreateResponse struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

type browserSessionKeepAliveResponse struct {
	ID        string    `json:"id"`
	ExpiresAt time.Time `json:"expires_at"`
}

type browserProxySession struct {
	ID        string
	BotID     string
	Port      int
	CreatedAt time.Time
	ExpiresAt time.Time
}

type browserSessionStore struct {
	mu       sync.Mutex
	ttl      time.Duration
	sessions map[string]browserProxySession
}

func newBrowserSessionStore(ttl time.Duration) *browserSessionStore {
	if ttl <= 0 {
		ttl = browserSessionIdleTTL
	}
	return &browserSessionStore{
		ttl:      ttl,
		sessions: make(map[string]browserProxySession),
	}
}

func (s *browserSessionStore) create(botID string, port int, now time.Time) (browserProxySession, error) {
	if s == nil {
		return browserProxySession{}, errors.New("browser session store is not configured")
	}
	id, err := newBrowserSessionID()
	if err != nil {
		return browserProxySession{}, err
	}
	session := browserProxySession{
		ID:        id,
		BotID:     strings.TrimSpace(botID),
		Port:      port,
		CreatedAt: now,
		ExpiresAt: now.Add(s.ttl),
	}
	s.mu.Lock()
	s.pruneLocked(now)
	s.sessions[id] = session
	s.mu.Unlock()
	return session, nil
}

func (s *browserSessionStore) touch(id string, now time.Time) (browserProxySession, bool) {
	if s == nil {
		return browserProxySession{}, false
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return browserProxySession{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	session, ok := s.sessions[id]
	if !ok {
		return browserProxySession{}, false
	}
	session.ExpiresAt = now.Add(s.ttl)
	s.sessions[id] = session
	return session, true
}

func (s *browserSessionStore) touchForBot(id, botID string, now time.Time) (browserProxySession, bool) {
	if s == nil {
		return browserProxySession{}, false
	}
	id = strings.TrimSpace(id)
	botID = strings.TrimSpace(botID)
	if id == "" || botID == "" {
		return browserProxySession{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	session, ok := s.sessions[id]
	if !ok || session.BotID != botID {
		return browserProxySession{}, false
	}
	session.ExpiresAt = now.Add(s.ttl)
	s.sessions[id] = session
	return session, true
}

func (s *browserSessionStore) deleteForBot(id, botID string) bool {
	if s == nil {
		return false
	}
	id = strings.TrimSpace(id)
	botID = strings.TrimSpace(botID)
	if id == "" || botID == "" {
		return false
	}
	s.mu.Lock()
	session, ok := s.sessions[id]
	if ok && session.BotID == botID {
		delete(s.sessions, id)
	}
	s.mu.Unlock()
	return ok && session.BotID == botID
}

func (s *browserSessionStore) pruneLocked(now time.Time) {
	for id, session := range s.sessions {
		if !session.ExpiresAt.After(now) {
			delete(s.sessions, id)
		}
	}
}

func newBrowserSessionID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

// CreateBrowserSession godoc
// @Summary Create browser proxy session for bot workspace
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param payload body browserSessionCreateRequest true "Browser session request"
// @Success 200 {object} browserSessionCreateResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/container/browser/sessions [post].
func (h *ContainerdHandler) CreateBrowserSession(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	if h.manager == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "manager not configured")
	}

	var req browserSessionCreateRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid browser session payload")
	}
	if err := validateBrowserPort(req.Port); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	ctx := c.Request().Context()
	if _, err := h.manager.NativeMCPClient(ctx, botID); err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, "workspace is not reachable: "+err.Error())
	}

	session, err := h.browserSessions.create(botID, req.Port, time.Now())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "create browser session failed")
	}
	return c.JSON(http.StatusOK, browserSessionCreateResponse{
		ID:        session.ID,
		URL:       buildBrowserProxyURL(c.Request(), session.ID, req.Path),
		ExpiresAt: session.ExpiresAt,
	})
}

// KeepAliveBrowserSession godoc
// @Summary Keep browser proxy session alive
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Browser session ID"
// @Success 200 {object} browserSessionKeepAliveResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /bots/{bot_id}/container/browser/sessions/{session_id}/keepalive [post].
func (h *ContainerdHandler) KeepAliveBrowserSession(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	session, ok := h.browserSessions.touchForBot(c.Param("session_id"), botID, time.Now())
	if !ok {
		return echo.NewHTTPError(http.StatusNotFound, "browser session expired")
	}
	return c.JSON(http.StatusOK, browserSessionKeepAliveResponse{
		ID:        session.ID,
		ExpiresAt: session.ExpiresAt,
	})
}

// DeleteBrowserSession godoc
// @Summary Delete browser proxy session
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Browser session ID"
// @Success 204
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Router /bots/{bot_id}/container/browser/sessions/{session_id} [delete].
func (h *ContainerdHandler) DeleteBrowserSession(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	h.browserSessions.deleteForBot(c.Param("session_id"), botID)
	return c.NoContent(http.StatusNoContent)
}

func (h *ContainerdHandler) handleBrowserProxyPre(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if _, ok := browserSessionIDFromHost(c.Request().Host); ok {
			return h.HandleBrowserProxy(c)
		}
		return next(c)
	}
}

func (h *ContainerdHandler) HandleBrowserProxy(c echo.Context) error {
	sessionID, ok := browserSessionIDFromHost(c.Request().Host)
	if !ok {
		return echo.NewHTTPError(http.StatusNotFound, "browser session not found")
	}
	session, ok := h.browserSessions.touch(sessionID, time.Now())
	if !ok {
		return echo.NewHTTPError(http.StatusNotFound, "browser session expired")
	}
	if h.manager == nil {
		return echo.NewHTTPError(http.StatusBadGateway, "manager not configured")
	}
	client, err := h.manager.NativeMCPClient(c.Request().Context(), session.BotID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, "workspace is not reachable: "+err.Error())
	}

	proxy := newBrowserReverseProxy(client, session.Port)
	proxy.ServeHTTP(c.Response(), c.Request()) //nolint:gosec // Target host is fixed to the workspace loopback and only the validated port varies.
	return nil
}

func newBrowserReverseProxy(client *bridge.Client, port int) *httputil.ReverseProxy {
	targetAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	target := &url.URL{Scheme: "http", Host: targetAddr}
	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(req *http.Request) {
		director(req)
		req.Host = targetAddr
	}
	proxy.Transport = &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			if client == nil {
				return nil, errors.New("workspace bridge client is required")
			}
			if network != "tcp" {
				return nil, fmt.Errorf("unsupported browser proxy network %q", network)
			}
			return client.DialContext(ctx, network, targetAddr)
		},
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		cleanBrowserProxyResponseHeaders(resp.Header)
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		http.Error(w, "browser proxy failed: "+err.Error(), http.StatusBadGateway)
	}
	return proxy
}

func validateBrowserPort(port int) error {
	if port < 1 || port > 65535 {
		return errors.New("browser port must be between 1 and 65535")
	}
	return nil
}

func buildBrowserProxyURL(req *http.Request, sessionID, path string) string {
	scheme := firstHeaderValue(req.Header.Get(echo.HeaderXForwardedProto))
	if scheme == "" {
		if req.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	hostHeader := firstHeaderValue(req.Header.Get("X-Forwarded-Host"))
	if hostHeader == "" {
		hostHeader = req.Host
	}
	host, port := splitHostPortLossy(hostHeader)
	baseHost := browserProxyBaseHost(host)
	proxyHost := strings.TrimSpace(sessionID) + browserProxySeparator + baseHost
	if port != "" {
		proxyHost = net.JoinHostPort(proxyHost, port)
	}
	return scheme + "://" + proxyHost + normalizeBrowserProxyPath(path)
}

func browserSessionIDFromHost(value string) (string, bool) {
	host, _ := splitHostPortLossy(value)
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	idx := strings.Index(host, browserProxySeparator)
	if idx <= 0 {
		return "", false
	}
	id := host[:idx]
	if !isBrowserSessionID(id) {
		return "", false
	}
	return id, true
}

func isBrowserSessionID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, ch := range value {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

func browserProxyBaseHost(host string) string {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" {
		return "localhost"
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return "localhost"
	}
	return host
}

func splitHostPortLossy(value string) (string, string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ""
	}
	if strings.Contains(value, "://") {
		if parsed, err := url.Parse(value); err == nil {
			value = parsed.Host
		}
	}
	if host, port, err := net.SplitHostPort(value); err == nil {
		return strings.Trim(host, "[]"), port
	}
	if strings.HasPrefix(value, "[") {
		if end := strings.Index(value, "]"); end > 0 {
			return value[1:end], ""
		}
	}
	return strings.Trim(value, "[]"), ""
}

func normalizeBrowserProxyPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "/"
	}
	if strings.ContainsAny(path, "\r\n\t") {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}

func cleanBrowserProxyResponseHeaders(header http.Header) {
	header.Del("X-Frame-Options")
	cleanCSPHeader(header, "Content-Security-Policy")
	cleanCSPHeader(header, "Content-Security-Policy-Report-Only")
}

func cleanCSPHeader(header http.Header, name string) {
	values := header.Values(name)
	if len(values) == 0 {
		return
	}
	header.Del(name)
	for _, value := range values {
		cleaned := removeCSPFrameAncestors(value)
		if strings.TrimSpace(cleaned) != "" {
			header.Add(name, cleaned)
		}
	}
}

func removeCSPFrameAncestors(value string) string {
	parts := strings.Split(value, ";")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) > 0 && strings.EqualFold(fields[0], "frame-ancestors") {
			continue
		}
		out = append(out, trimmed)
	}
	return strings.Join(out, "; ")
}
