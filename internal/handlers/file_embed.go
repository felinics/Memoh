package handlers

import (
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/embedded"
)

type EmbeddedWebHandler struct {
	log   *slog.Logger
	webFS fs.FS
}

var embeddedStaticRoutes = map[string]struct {
	assetPath   string
	contentType string
}{
	"/logo.png": {assetPath: "logo.png", contentType: "image/png"},
}

func NewEmbeddedWebHandler(log *slog.Logger) (*EmbeddedWebHandler, error) {
	webFS, err := embedded.WebFS()
	if err != nil {
		return nil, err
	}
	return &EmbeddedWebHandler{log: log, webFS: webFS}, nil
}

func (h *EmbeddedWebHandler) Register(e *echo.Echo) {
	e.GET("/assets/*", h.serveAsset)
	for route, meta := range embeddedStaticRoutes {
		e.GET(route, h.serveStatic(meta.assetPath, meta.contentType))
	}
	e.GET("/", h.serveIndex)
	e.GET("/*", func(c echo.Context) error {
		reqPath := c.Request().URL.Path
		if isBackendPath(reqPath) || strings.Contains(path.Base(reqPath), ".") {
			return echo.ErrNotFound
		}
		return h.serveIndex(c)
	})
}

func (h *EmbeddedWebHandler) serveIndex(c echo.Context) error {
	return h.serveKnownGzip(c, "index.html", "text/html; charset=utf-8")
}

func (h *EmbeddedWebHandler) serveStatic(targetPath, contentType string) echo.HandlerFunc {
	return func(c echo.Context) error {
		return h.serveKnownGzip(c, targetPath, contentType)
	}
}

func (h *EmbeddedWebHandler) serveAsset(c echo.Context) error {
	assetPath := strings.TrimPrefix(c.Param("*"), "/")
	if assetPath == "" {
		return echo.ErrNotFound
	}

	fullPath := path.Join("assets", assetPath)
	contentType := mime.TypeByExtension(filepath.Ext(assetPath))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	gzipPath := fullPath + ".gz"
	content, err := fs.ReadFile(h.webFS, gzipPath)
	if err != nil {
		return echo.ErrNotFound
	}
	header := c.Response().Header()
	header.Set(echo.HeaderContentEncoding, "gzip")
	header.Set(echo.HeaderVary, "Accept-Encoding")
	return c.Blob(http.StatusOK, contentType, content)
}

func (h *EmbeddedWebHandler) serveKnownGzip(c echo.Context, targetPath, contentType string) error {
	gzipPath := targetPath + ".gz"
	content, err := fs.ReadFile(h.webFS, gzipPath)
	if err != nil {
		if targetPath == "index.html" {
			h.log.Error("read embedded index.html.gz failed", slog.Any("error", err))
		}
		return echo.ErrNotFound
	}
	header := c.Response().Header()
	header.Set(echo.HeaderContentEncoding, "gzip")
	header.Set(echo.HeaderVary, "Accept-Encoding")
	return c.Blob(http.StatusOK, contentType, content)
}

func isBackendPath(p string) bool {
	return p == "/ping" ||
		p == "/health" ||
		strings.HasPrefix(p, "/api") ||
		strings.HasPrefix(p, "/auth") ||
		strings.HasPrefix(p, "/channels") ||
		strings.HasPrefix(p, "/containers") ||
		strings.HasPrefix(p, "/runtimes/")
}
