package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/workspace/bridge"
	"github.com/felinics/memoh/internal/workspace/cdpsession"
)

// The CDP proxy serves the revocable sessions issued by the
// browser_remote_session tool: /bots/{bot}/container/cdp/{session}/... .
// The session id in the path is the credential (CDP clients cannot send a
// bearer token), the session is bound to one page target, and revoking it
// closes every websocket the proxy opened for it.

var cdpProxyUpgrader = websocket.Upgrader{
	ReadBufferSize:  64 * 1024,
	WriteBufferSize: 64 * 1024,
	// CDP clients (Playwright, chrome-remote-interface, curl-driven tools)
	// send no Origin or an arbitrary one; the session id already gates access.
	CheckOrigin: func(*http.Request) bool { return true },
}

// SetCDPSessions wires the store shared with the browser_remote_session tool.
func (h *ContainerdHandler) SetCDPSessions(store *cdpsession.Store) {
	h.cdpSessions = store
}

func (h *ContainerdHandler) registerCDPProxy(group *echo.Group) {
	group.GET("/cdp/:session_id/json/version", h.CDPProxyVersion)
	group.GET("/cdp/:session_id/json/list", h.CDPProxyList)
	group.GET("/cdp/:session_id/json", h.CDPProxyList)
	group.GET("/cdp/:session_id/devtools/page/:target_id", h.CDPProxyWebSocket)
}

// cdpProxySession resolves and touches the session named by the request.
func (h *ContainerdHandler) cdpProxySession(c echo.Context) (cdpsession.Session, *bridge.Client, error) {
	botID := strings.TrimSpace(c.Param("bot_id"))
	if h.cdpSessions == nil {
		return cdpsession.Session{}, nil, echo.NewHTTPError(http.StatusNotFound, "cdp sessions are not configured")
	}
	session, ok := h.cdpSessions.Get(c.Param("session_id"), botID)
	if !ok {
		return cdpsession.Session{}, nil, echo.NewHTTPError(http.StatusNotFound, "cdp session not found, expired, or revoked")
	}
	if h.manager == nil {
		return cdpsession.Session{}, nil, echo.NewHTTPError(http.StatusBadGateway, "manager not configured")
	}
	client, err := h.manager.NativeMCPClient(c.Request().Context(), botID)
	if err != nil {
		return cdpsession.Session{}, nil, echo.NewHTTPError(http.StatusBadGateway, "workspace is not reachable: "+err.Error())
	}
	return session, client, nil
}

// cdpProxyGet fetches a /json endpoint of the workspace browser.
func cdpProxyGet(ctx context.Context, client *bridge.Client, port int, path string) ([]byte, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return client.DialContext(ctx, network, address)
		},
		DisableKeepAlives: true,
	}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req) //nolint:gosec // tunnelled to the workspace loopback CDP endpoint of the session's browser.
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("browser answered HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// cdpProxyWSBase builds the websocket origin and path prefix a client must
// use to come back through this proxy, from the request it just made.
func cdpProxyWSBase(req *http.Request, trimSuffix string) string {
	scheme := "ws"
	if proto := firstHeaderValue(req.Header.Get(echo.HeaderXForwardedProto)); proto == "https" || (proto == "" && req.TLS != nil) {
		scheme = "wss"
	}
	host := firstHeaderValue(req.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = req.Host
	}
	path := strings.TrimSuffix(req.URL.Path, trimSuffix)
	if prefix := strings.TrimSpace(req.Header.Get("X-Forwarded-Prefix")); prefix != "" && !strings.HasPrefix(path, prefix) {
		path = strings.TrimSuffix(prefix, "/") + path
	}
	return scheme + "://" + host + path
}

// CDPProxyVersion godoc
// @Summary CDP /json/version of a revocable browser session
// @Description Browser metadata for CDP clients. The browser-level websocket is not exposed: a session is scoped to one page target, see /json/list.
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "CDP session ID"
// @Success 200 {object} map[string]any
// @Failure 404 {object} ErrorResponse
// @Failure 502 {object} ErrorResponse
// @Router /bots/{bot_id}/container/cdp/{session_id}/json/version [get].
func (h *ContainerdHandler) CDPProxyVersion(c echo.Context) error {
	session, client, err := h.cdpProxySession(c)
	if err != nil {
		return err
	}
	body, err := cdpProxyGet(c.Request().Context(), client, session.Port, "/json/version")
	if err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, "browser is not reachable: "+err.Error())
	}
	var version map[string]any
	if err := json.Unmarshal(body, &version); err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, "browser returned an invalid /json/version")
	}
	delete(version, "webSocketDebuggerUrl")
	version["Memoh-Session-Scope"] = "page target " + session.TabID + " only; the browser-level websocket is not exposed, use the target listed by /json/list"
	return c.JSON(http.StatusOK, version)
}

// CDPProxyList godoc
// @Summary CDP /json/list of a revocable browser session
// @Description Lists the single page target the session is bound to, with its websocket URL rewritten to this proxy. Other tabs of the browser are never listed.
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "CDP session ID"
// @Success 200 {array} map[string]any
// @Failure 404 {object} ErrorResponse
// @Failure 502 {object} ErrorResponse
// @Router /bots/{bot_id}/container/cdp/{session_id}/json/list [get].
func (h *ContainerdHandler) CDPProxyList(c echo.Context) error {
	session, client, err := h.cdpProxySession(c)
	if err != nil {
		return err
	}
	body, err := cdpProxyGet(c.Request().Context(), client, session.Port, "/json/list")
	if err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, "browser is not reachable: "+err.Error())
	}
	var targets []map[string]any
	if err := json.Unmarshal(body, &targets); err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, "browser returned an invalid /json/list")
	}
	suffix := "/json/list"
	if strings.HasSuffix(c.Request().URL.Path, "/json") {
		suffix = "/json"
	}
	base := cdpProxyWSBase(c.Request(), suffix)
	out := make([]map[string]any, 0, 1)
	for _, target := range targets {
		id, _ := target["id"].(string)
		if id != session.TabID {
			continue
		}
		target["webSocketDebuggerUrl"] = base + "/devtools/page/" + url.PathEscape(id)
		delete(target, "devtoolsFrontendUrl")
		out = append(out, target)
	}
	return c.JSON(http.StatusOK, out)
}

// CDPProxyWebSocket godoc
// @Summary CDP websocket of a revocable browser session
// @Description Attaches to the session's page target inside the workspace. Revoking the session closes this connection.
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "CDP session ID"
// @Param target_id path string true "Page target ID (must be the session's tab)"
// @Success 101
// @Failure 404 {object} ErrorResponse
// @Failure 502 {object} ErrorResponse
// @Router /bots/{bot_id}/container/cdp/{session_id}/devtools/page/{target_id} [get].
func (h *ContainerdHandler) CDPProxyWebSocket(c echo.Context) error {
	session, client, err := h.cdpProxySession(c)
	if err != nil {
		return err
	}
	targetID := strings.TrimSpace(c.Param("target_id"))
	if targetID != session.TabID {
		return echo.NewHTTPError(http.StatusNotFound, "this session is bound to another page target")
	}
	ctx := c.Request().Context()
	dialer := websocket.Dialer{
		NetDialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return client.DialContext(ctx, network, address)
		},
		HandshakeTimeout: 10 * time.Second,
	}
	upstreamURL := "ws://127.0.0.1:" + strconv.Itoa(session.Port) + "/devtools/page/" + url.PathEscape(targetID)
	upstream, _, err := dialer.DialContext(ctx, upstreamURL, nil) //nolint:bodyclose // gorilla websocket owns the response body.
	if err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, "page target is not reachable (closed?): "+err.Error())
	}
	conn, err := cdpProxyUpgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		_ = upstream.Close()
		return err
	}
	release, attached := h.cdpSessions.Attach(session.ID, session.BotID, conn)
	if !attached {
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseGoingAway, "session revoked"), time.Now().Add(time.Second))
		_ = conn.Close()
		_ = upstream.Close()
		return nil
	}
	defer release()
	h.logger.Info("cdp proxy attached", slog.String("bot_id", session.BotID), slog.String("session_id", session.ID), slog.String("tab_id", targetID))
	pumpCDPWebSockets(conn, upstream)
	h.logger.Info("cdp proxy detached", slog.String("bot_id", session.BotID), slog.String("session_id", session.ID))
	return nil
}

// pumpCDPWebSockets relays frames both ways until either side closes, then
// closes the other so a revoked client connection also ends the upstream.
func pumpCDPWebSockets(client, upstream *websocket.Conn) {
	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			_ = client.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
			_ = client.Close()
			_ = upstream.Close()
		})
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer closeBoth()
		copyWebSocketFrames(upstream, client)
	}()
	go func() {
		defer wg.Done()
		defer closeBoth()
		copyWebSocketFrames(client, upstream)
	}()
	wg.Wait()
}

func copyWebSocketFrames(dst, src *websocket.Conn) {
	for {
		messageType, data, err := src.ReadMessage()
		if err != nil {
			return
		}
		if err := dst.WriteMessage(messageType, data); err != nil {
			return
		}
	}
}
