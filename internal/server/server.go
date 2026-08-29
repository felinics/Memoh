package server

import (
	"context"
	"encoding/hex"
	"log/slog"
	"net/http"
	neturl "net/url"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"github.com/felinics/memoh/internal/auth"
	"github.com/felinics/memoh/internal/channel/publicmedia"
	"github.com/felinics/memoh/internal/httpx"
	"github.com/felinics/memoh/internal/mcp"
)

type Server struct {
	echo   *echo.Echo
	addr   string
	logger *slog.Logger
}

type Handler interface {
	Register(e *echo.Echo)
}

func NewServer(log *slog.Logger, addr string, jwtSecret string,
	handlers ...Handler,
) *Server {
	return newServer(log, addr, jwtSecret, nil, handlers...)
}

func NewServerWithSessionValidator(log *slog.Logger, addr string, jwtSecret string,
	validateSession auth.UserSessionValidator, handlers ...Handler,
) *Server {
	return newServer(log, addr, jwtSecret, validateSession, handlers...)
}

func newServer(log *slog.Logger, addr string, jwtSecret string,
	validateSession auth.UserSessionValidator, handlers ...Handler,
) *Server {
	if addr == "" {
		addr = ":8080"
	}

	e := echo.New()
	e.HideBanner = true
	e.HTTPErrorHandler = newHTTPErrorHandler(log, e.DefaultHTTPErrorHandler)
	e.Use(middleware.RequestID())
	e.Use(middleware.Recover())
	e.Use(middleware.BodyLimitWithConfig(middleware.BodyLimitConfig{
		Limit: "1M",
		Skipper: func(c echo.Context) bool {
			return !shouldLimitRequestBody(c.Request())
		},
	}))
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins:  []string{"*"},
		AllowMethods:  []string{echo.GET, echo.HEAD, echo.POST, echo.PUT, echo.PATCH, echo.DELETE, echo.OPTIONS},
		AllowHeaders:  []string{echo.HeaderOrigin, echo.HeaderContentType, echo.HeaderAccept, echo.HeaderAuthorization, echo.HeaderXRequestID},
		ExposeHeaders: []string{echo.HeaderXRequestID},
	}))
	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		HandleError: true,
		LogStatus:   true,
		LogURI:      true,
		LogMethod:   true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			log.Info("request",
				slog.String("method", v.Method),
				slog.String("uri", safeRequestLogURI(c.Request().URL, v.URI)),
				slog.Int("status", v.Status),
				slog.Duration("latency", v.Latency),
				slog.String("remote_ip", c.RealIP()),
				slog.String("request_id", httpx.RequestID(c)),
			)
			return nil
		},
	}))
	e.Use(auth.JWTMiddleware(jwtSecret, func(c echo.Context) bool {
		return shouldSkipJWTRequest(c.Request())
	}, validateSession))

	for _, h := range handlers {
		if h != nil {
			h.Register(e)
		}
	}

	return &Server{
		echo:   e,
		addr:   addr,
		logger: log.With(slog.String("component", "server")),
	}
}

func (s *Server) Start() error {
	return s.echo.Start(s.addr)
}

func (s *Server) Stop(ctx context.Context) error {
	return s.echo.Shutdown(ctx)
}

// shouldSkipJWTRequest admits the ACP runtime credential only on the exact
// stateless MCP tools route. The handler still authenticates both values
// against a live runtime before serving a request; this check merely lets that
// independent credential reach the handler instead of being rejected as a
// missing user JWT.
func shouldSkipJWTRequest(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}
	if shouldSkipJWT(req.URL.Path) {
		return true
	}
	return isRuntimeToolsCredentialRequest(req)
}

func isRuntimeToolsCredentialRequest(req *http.Request) bool {
	return req != nil && req.URL != nil && req.Method == http.MethodPost &&
		isExactBotToolsPath(req.URL.Path) &&
		strings.TrimSpace(req.Header.Get(mcp.ToolHeaderRuntimeID)) != "" &&
		strings.TrimSpace(req.Header.Get(mcp.ToolHeaderRuntimeToken)) != ""
}

func isExactBotToolsPath(requestPath string) bool {
	parts := strings.Split(requestPath, "/")
	return len(parts) == 4 && parts[0] == "" && parts[1] == "bots" &&
		strings.TrimSpace(parts[2]) != "" && parts[3] == "tools"
}

func shouldSkipJWT(path string) bool {
	if path == "/" || path == "/ping" || path == "/health" || path == "/api/swagger.json" || path == "/auth/login" || path == "/runtimes/connect" {
		return true
	}
	if strings.HasPrefix(path, "/assets/") {
		return true
	}
	if isPublicSupermarketSkillIconPath(path) {
		return true
	}
	if strings.HasPrefix(path, "/api/docs") {
		return true
	}
	if isPublicChannelWebhookPath(path) {
		return true
	}
	if isPublicChannelMediaPath(path) {
		return true
	}
	if strings.HasPrefix(path, "/email/mailgun/webhook/") {
		return true
	}
	if strings.HasPrefix(path, "/email/oauth/callback") || strings.HasPrefix(path, "/api/email/oauth/callback") {
		return true
	}
	if strings.HasPrefix(path, "/oauth/mcp/callback") || strings.HasPrefix(path, "/api/oauth/mcp/callback") {
		return true
	}
	if strings.HasPrefix(path, "/providers/oauth/callback") {
		return true
	}
	if strings.HasPrefix(path, "/auth/callback") {
		return true
	}
	return false
}

func isPublicSupermarketSkillIconPath(path string) bool {
	digest, found := strings.CutPrefix(path, "/supermarket/artifacts/icon/")
	if !found || len(digest) != 64 || strings.ToLower(digest) != digest {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

func shouldLimitPublicRequestBody(path string) bool {
	return isPublicChannelWebhookPath(path)
}

func shouldLimitRequestBody(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}
	return shouldLimitPublicRequestBody(req.URL.Path) || isRuntimeToolsCredentialRequest(req)
}

func isPublicChannelWebhookPath(path string) bool {
	if !strings.HasPrefix(path, "/channels/") {
		return false
	}
	trimmed := strings.Trim(strings.TrimPrefix(path, "/channels/"), "/")
	parts := strings.Split(trimmed, "/")
	return len(parts) >= 3 && strings.TrimSpace(parts[0]) != "" && parts[1] == "webhook" && strings.TrimSpace(parts[2]) != ""
}

func isPublicChannelMediaPath(path string) bool {
	return publicmedia.IsPath(path)
}

func safeRequestLogURI(u *neturl.URL, fallback string) string {
	if u == nil {
		return fallback
	}
	escapedPath := u.EscapedPath()
	if isPublicChannelMediaPath(escapedPath) {
		return escapedPath
	}
	if fallback != "" {
		return fallback
	}
	return u.RequestURI()
}
