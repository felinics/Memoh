package handlers

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/skillpackages"
	supermarketclient "github.com/felinics/memoh/internal/supermarket"
	"github.com/felinics/memoh/internal/workspace"
)

type SupermarketHandler struct {
	upstream       *supermarketclient.Client
	installer      *supermarketclient.Installer
	packages       *skillpackages.Service
	workspaces     *workspace.Manager
	botService     *bots.Service
	accountService *accounts.Service
	logger         *slog.Logger
}

func NewSupermarketHandler(
	log *slog.Logger,
	cfg config.Config,
	packageService *skillpackages.Service,
	workspaces *workspace.Manager,
	botService *bots.Service,
	accountService *accounts.Service,
) *SupermarketHandler {
	upstream := supermarketclient.NewClient(cfg.Supermarket.GetBaseURL(), nil)
	return &SupermarketHandler{
		upstream:       upstream,
		installer:      supermarketclient.NewInstaller(upstream, packageService, workspaces, log),
		packages:       packageService,
		workspaces:     workspaces,
		botService:     botService,
		accountService: accountService,
		logger:         log.With(slog.String("handler", "supermarket")),
	}
}

func (h *SupermarketHandler) Register(e *echo.Echo) {
	g := e.Group("/supermarket")
	g.GET("/skills", h.ListSkills)
	g.GET("/packages", h.ListPackages)
	g.GET("/registries", h.ListRegistries)
	g.GET("/registries/:registry_id/categories", h.ListRegistryCategories)
	g.GET("/registries/:registry_id/packages", h.ListRegistryPackages)
	g.GET("/registries/:registry_id/packages/:package_id", h.GetRegistryPackage)
	g.GET("/registries/:registry_id/packages/:package_id/releases/:revision", h.GetRegistryPackageRelease)
	g.GET("/registries/:registry_id/packages/:package_id/skills/:skill_id", h.GetRegistrySkill)
	g.GET("/artifacts/icon/:digest", h.GetRegistrySkillIcon)

	ig := e.Group("/bots/:bot_id/supermarket")
	ig.POST("/install-package", h.InstallPackage)
	ig.GET("/packages", h.ListInstalledPackages)
	ig.DELETE("/packages/:installation_id", h.UninstallPackage)
}

func (h *SupermarketHandler) requireBotAccess(c echo.Context) (string, error) {
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return "", err
	}
	botID := c.Param("bot_id")
	if _, err := AuthorizeBotAccess(c.Request().Context(), h.botService, h.accountService, channelIdentityID, botID); err != nil {
		return "", err
	}
	return botID, nil
}

// proxy forwards a GET request to the supermarket and streams the JSON response back.
func (h *SupermarketHandler) proxy(c echo.Context, upstreamPath string) error {
	requestPath := upstreamPath
	if qs := c.QueryString(); qs != "" {
		requestPath += "?" + qs
	}
	resp, err := h.upstream.Get(c.Request().Context(), requestPath, "application/json")
	if err != nil {
		h.logger.Error("supermarket proxy failed", slog.String("path", requestPath), slog.Any("error", err))
		return echo.NewHTTPError(http.StatusBadGateway, "supermarket unreachable")
	}
	defer func() { _ = resp.Body.Close() }()

	c.Response().Header().Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	c.Response().WriteHeader(resp.StatusCode)
	_, _ = io.Copy(c.Response(), resp.Body)
	return nil
}

// --- Install endpoints ---

// InstallPackageRequest installs one immutable Package revision.
type InstallPackageRequest struct {
	RegistryID        string `json:"registry_id" validate:"required"`
	PackageID         string `json:"package_id" validate:"required"`
	Revision          string `json:"revision" validate:"required"`
	WorkspaceTargetID string `json:"workspace_target_id,omitempty"`
}

// InstallPackage godoc
// @Summary Install an immutable Skill Package release to a bot workspace
// @Tags supermarket
// @Param bot_id path string true "Bot ID"
// @Param payload body InstallPackageRequest true "Install Package request"
// @Success 200 {object} InstallRegistryPackageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Failure 502 {object} apperror.Problem
// @Router /bots/{bot_id}/supermarket/install-package [post].
func (h *SupermarketHandler) InstallPackage(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}

	var req InstallPackageRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if h.installer == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "supermarket installer is not configured")
	}
	result, err := h.installer.InstallPackage(c.Request().Context(), botID, supermarketclient.InstallPackageRequest{
		RegistryID: req.RegistryID, PackageID: req.PackageID, Revision: req.Revision,
		WorkspaceTargetID: req.WorkspaceTargetID,
	})
	if err != nil {
		return h.installerHTTPError(err)
	}
	return c.JSON(http.StatusOK, result)
}

// ListInstalledPackages godoc
// @Summary List Skill Packages installed for a bot
// @Tags supermarket
// @Param bot_id path string true "Bot ID"
// @Param workspace_target_id query string false "Workspace target ID"
// @Success 200 {array} skillpackages.Installation
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/supermarket/packages [get].
func (h *SupermarketHandler) ListInstalledPackages(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	if h.packages == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Skill Package service is not configured")
	}
	targetID := strings.TrimSpace(c.QueryParam("workspace_target_id"))
	if h.workspaces != nil {
		target, resolveErr := h.workspaces.ResolveWorkspaceTarget(c.Request().Context(), botID, targetID)
		err = resolveErr
		if err != nil {
			return workspaceTargetHTTPError(h.logger, err)
		}
		targetID = target.TargetID
	} else if targetID == "" {
		targetID = workspace.WorkspaceTargetNative
	}
	items, err := h.packages.ListForTarget(c.Request().Context(), botID, targetID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, items)
}

// UninstallPackage godoc
// @Summary Uninstall a Skill Package from a bot
// @Tags supermarket
// @Param bot_id path string true "Bot ID"
// @Param installation_id path string true "Package installation ID"
// @Success 200 {object} supermarket.UninstallPackageResponse
// @Failure 404 {object} ErrorResponse
// @Router /bots/{bot_id}/supermarket/packages/{installation_id} [delete].
func (h *SupermarketHandler) UninstallPackage(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	if h.installer == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "supermarket installer is not configured")
	}
	result, err := h.installer.UninstallPackage(c.Request().Context(), botID, strings.TrimSpace(c.Param("installation_id")))
	if err != nil {
		return h.installerHTTPError(err)
	}
	return c.JSON(http.StatusOK, result)
}

func (h *SupermarketHandler) installerHTTPError(err error) error {
	var targetErr *supermarketclient.WorkspaceTargetError
	if errors.As(err, &targetErr) {
		return workspaceTargetHTTPError(h.logger, targetErr.Err)
	}
	var statusErr *supermarketclient.StatusError
	if errors.As(err, &statusErr) {
		return echo.NewHTTPError(statusErr.Status, statusErr.Error())
	}
	return err
}

// --- Supermarket upstream types (for swagger) ---

type SupermarketAuthor = supermarketclient.Author
