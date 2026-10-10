package handlers

import (
	"io"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/errs"
	supermarketclient "github.com/felinics/memoh/internal/supermarket"
)

// SupermarketHandler proxies the read-only Supermarket catalog. Installing
// Apps into bots is the AppsHandler's job.
type SupermarketHandler struct {
	upstream *supermarketclient.Client
	logger   *slog.Logger
}

func NewSupermarketHandler(log *slog.Logger, cfg config.Config) *SupermarketHandler {
	return &SupermarketHandler{
		upstream: supermarketclient.NewClient(cfg.Supermarket.GetBaseURL(), nil),
		logger:   log.With(slog.String("handler", "supermarket")),
	}
}

func (h *SupermarketHandler) Register(e *echo.Echo) {
	g := e.Group("/supermarket")
	g.GET("/skills", h.ListSkills)
	g.GET("/apps", h.ListApps)
	g.GET("/categories", h.ListCategories)
	g.GET("/registries", h.ListRegistries)
	g.GET("/registries/:registry_id/apps", h.ListRegistryApps)
	g.GET("/registries/:registry_id/apps/:app_id", h.GetRegistryApp)
	g.GET("/registries/:registry_id/apps/:app_id/releases/:revision", h.GetRegistryAppRelease)
	g.GET("/registries/:registry_id/apps/:app_id/skills/:skill_id", h.GetRegistrySkill)
	g.GET("/artifacts/icon/:digest", h.GetRegistrySkillIcon)
}

// proxy forwards a GET request to the supermarket and streams the JSON response back.
func (h *SupermarketHandler) proxy(c echo.Context, upstreamPath string) error {
	requestPath := upstreamPath
	if qs := c.QueryString(); qs != "" {
		requestPath += "?" + qs
	}
	resp, err := h.upstream.Get(c.Request().Context(), requestPath, "application/json")
	if err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, "supermarket unreachable").
			WithInternal(errs.WrapDependency(err, "proxy supermarket request", slog.String("path", upstreamPath)))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return upstreamStatusError(resp.StatusCode, upstreamPath)
	}

	c.Response().Header().Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	c.Response().WriteHeader(resp.StatusCode)
	_, _ = io.Copy(c.Response(), resp.Body)
	return nil
}

// upstreamStatusError answers a Supermarket response that is not a success
// without forwarding its body. A missing resource is unknown to the client;
// any other status is Supermarket's failure.
func upstreamStatusError(status int, upstreamPath string) error {
	attrs := []slog.Attr{slog.Int("upstream_status", status), slog.String("path", upstreamPath)}
	if status == http.StatusNotFound {
		return apperror.Wrap(apperror.CodeSupermarketNotFound, errs.New("supermarket resource not found", attrs...), nil)
	}
	return echo.NewHTTPError(http.StatusBadGateway, "supermarket request failed").
		WithInternal(errs.NewDependency("supermarket answered with an error status", attrs...))
}

// --- Supermarket upstream types (for swagger) ---

type SupermarketAuthor = supermarketclient.Author
