package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	connectsdk "github.com/felinics/connect-it/sdk/go"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/connectors"
	"github.com/felinics/memoh/internal/httpx"
	"github.com/felinics/memoh/internal/packages"
	supermarketclient "github.com/felinics/memoh/internal/supermarket"
	"github.com/felinics/memoh/internal/workspacedeps"
)

var packageConnectorTypePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// packageService is the slice of *packages.Service the routes use.
type packageService interface {
	List(ctx context.Context, botID, targetID string, refresh bool) (packages.ListResult, error)
	Get(ctx context.Context, botID, installationID string) (packages.Item, error)
	Install(ctx context.Context, botID string, req packages.InstallRequest, sink packages.EventSink) (packages.OperationResult, error)
	Resume(ctx context.Context, botID, installationID string, sink packages.EventSink) (packages.OperationResult, error)
	UpdateSelection(ctx context.Context, botID string, req packages.UpdateRequest, sink packages.EventSink) (packages.OperationResult, error)
	Remove(ctx context.Context, botID, installationID string, opts packages.RemoveOptions, sink packages.EventSink) (packages.OperationResult, error)
	RemovalPreview(ctx context.Context, botID, installationID string) (packages.RemovalPreview, error)
	CheckUpdates(ctx context.Context, botID, targetID string) (packages.ListResult, error)
	BeginConnectorOAuth(ctx context.Context, botID, installationID, connectorType, authMethod string) (connectsdk.OAuthAuthorization, error)
	CreateConnectorCredential(ctx context.Context, botID, installationID, connectorType, authMethod string, fields map[string]string) (connectors.Connector, error)
}

// PackagesHandler serves the Packages a bot has installed from the
// Supermarket and runs their lifecycle operations.
type PackagesHandler struct {
	service        packageService
	botService     *bots.Service
	accountService *accounts.Service
	logger         *slog.Logger
}

func NewPackagesHandler(log *slog.Logger, service *packages.Service, botService *bots.Service, accountService *accounts.Service) *PackagesHandler {
	var svc packageService
	if service != nil {
		svc = service
	}
	return &PackagesHandler{
		service: svc, botService: botService, accountService: accountService,
		logger: log.With(slog.String("handler", "packages")),
	}
}

func (h *PackagesHandler) Register(e *echo.Echo) {
	g := e.Group("/bots/:bot_id/packages")
	g.GET("", h.List)
	g.POST("", h.Install)
	g.POST("/check-updates", h.CheckUpdates)
	g.POST("/update", h.UpdateSelection)
	g.GET("/:installation_id", h.Get)
	g.GET("/:installation_id/removal-preview", h.RemovalPreview)
	g.DELETE("/:installation_id", h.Remove)
	g.POST("/:installation_id/resume", h.Resume)
	g.POST("/:installation_id/connectors/:connector_type/oauth", h.BeginConnectorOAuth)
	g.POST("/:installation_id/connectors/:connector_type/api-key", h.CreateConnectorCredential)
}

// --- DTOs ---

// PackageSkillItem is one Skill a Package materializes.
type PackageSkillItem struct {
	SkillID     string                       `json:"skill_id"`
	InstallID   string                       `json:"install_id"`
	Name        string                       `json:"name"`
	Description string                       `json:"description,omitempty"`
	Icon        *supermarketclient.SkillIcon `json:"icon,omitempty"`
}

// PackageDependencyItem is one workspace dependency a Package references,
// with the reconciled dependency state when the catalog knows it.
type PackageDependencyItem struct {
	ID string `json:"id"`
	// Shared is set when another installed Package references the same
	// dependency on this workspace target.
	Shared     bool                     `json:"shared"`
	Dependency *WorkspaceDependencyItem `json:"dependency,omitempty"`
}

// PackageConnectorItem is one Connect-It connector a Package references.
type PackageConnectorItem struct {
	Type         string `json:"type"`
	Required     bool   `json:"required"`
	ConnectionID string `json:"connection_id,omitempty"`
	// Status is linked once a connection is bound, otherwise needs_auth.
	Status    string                `json:"status" enums:"linked,needs_auth"`
	Connector *connectors.Connector `json:"connector,omitempty"`
}

// PackageItem is one Package on a workspace target.
type PackageItem struct {
	// InstallationID is empty for a discovered Package: a dependency the
	// workspace carries that no installed Package references, shown through
	// its canonical Package.
	InstallationID string `json:"installation_id,omitempty"`
	RegistryID     string `json:"registry_id"`
	PackageID      string `json:"package_id"`
	Revision       string `json:"revision,omitempty"`
	Version        string `json:"version,omitempty"`
	Name           string `json:"name"`
	Description    string `json:"description,omitempty"`
	// Status is discovered for Packages without an installation record.
	Status string `json:"status" enums:"installed,partial,installing,updating,removing,failed,discovered"`
	Reason string `json:"reason,omitempty" enums:"user,required"`
	// AvailableRevision and AvailableVersion name the registry's newer
	// release after a check found one.
	AvailableRevision string                                          `json:"available_revision,omitempty"`
	AvailableVersion  string                                          `json:"available_version,omitempty"`
	LastCheckedAt     *time.Time                                      `json:"last_checked_at,omitempty"`
	LastError         string                                          `json:"last_error,omitempty"`
	Icon              *supermarketclient.SkillIcon                    `json:"icon,omitempty"`
	Category          string                                          `json:"category,omitempty"`
	CategoryName      string                                          `json:"category_name,omitempty"`
	Author            *supermarketclient.Author                       `json:"author,omitempty"`
	Homepage          string                                          `json:"homepage,omitempty"`
	Repository        string                                          `json:"repository,omitempty"`
	License           string                                          `json:"license,omitempty"`
	Tags              []string                                        `json:"tags"`
	Translations      map[string]supermarketclient.PackageTranslation `json:"translations,omitempty"`
	Skills            []PackageSkillItem                              `json:"skills"`
	Dependencies      []PackageDependencyItem                         `json:"dependencies"`
	Connectors        []PackageConnectorItem                          `json:"connectors"`
	InstalledAt       *time.Time                                      `json:"installed_at,omitempty"`
	UpdatedAt         *time.Time                                      `json:"updated_at,omitempty"`
}

// PackageListResponse is the Package view of one workspace target.
type PackageListResponse struct {
	WorkspaceTargetID      string        `json:"workspace_target_id"`
	WorkspaceState         string        `json:"workspace_state,omitempty" enums:"running,not_running,missing,remote_offline"`
	DependencyCatalogStale bool          `json:"dependency_catalog_stale"`
	Items                  []PackageItem `json:"items"`
}

// PackageInstallRequest names one immutable Package release to install.
type PackageInstallRequest struct {
	RegistryID        string `json:"registry_id" validate:"required"`
	PackageID         string `json:"package_id" validate:"required"`
	Revision          string `json:"revision" validate:"required"`
	WorkspaceTargetID string `json:"workspace_target_id,omitempty"`
}

// PackageUpdateRequest selects what to update for one Package on a workspace
// target: its dependencies, its release, or both.
type PackageUpdateRequest struct {
	RegistryID        string `json:"registry_id" validate:"required"`
	PackageID         string `json:"package_id" validate:"required"`
	WorkspaceTargetID string `json:"workspace_target_id,omitempty"`
	// Release moves the installation to the registry's current release.
	Release bool `json:"release"`
	// Dependencies are updated to their latest version.
	Dependencies []string `json:"dependencies,omitempty"`
}

// PackageRemovalPreviewDependency says what removing a Package does to one
// dependency reference.
type PackageRemovalPreviewDependency struct {
	ID     string `json:"id"`
	Action string `json:"action" enums:"remove,keep"`
	Reason string `json:"reason,omitempty" enums:"shared,image,absent"`
}

// PackageRemovalPreviewConnector says what removing a Package does to one
// connector reference.
type PackageRemovalPreviewConnector struct {
	Type         string `json:"type"`
	ConnectionID string `json:"connection_id,omitempty"`
	Action       string `json:"action" enums:"disconnect,keep,none"`
	Reason       string `json:"reason,omitempty" enums:"shared"`
}

// PackageRemovalPreviewPackage is an auto-installed Package that would lose
// its last reference.
type PackageRemovalPreviewPackage struct {
	InstallationID string `json:"installation_id"`
	RegistryID     string `json:"registry_id"`
	PackageID      string `json:"package_id"`
	Version        string `json:"version,omitempty"`
}

// PackageRemovalPreviewResponse is the plan of a Package removal.
type PackageRemovalPreviewResponse struct {
	InstallationID   string                            `json:"installation_id"`
	Dependencies     []PackageRemovalPreviewDependency `json:"dependencies"`
	Connectors       []PackageRemovalPreviewConnector  `json:"connectors"`
	RequiredPackages []PackageRemovalPreviewPackage    `json:"required_packages"`
}

// PackageConnectorOAuthRequest starts OAuth for a referenced connector.
type PackageConnectorOAuthRequest struct {
	AuthMethod string `json:"auth_method" validate:"required"`
}

// PackageConnectorCredentialRequest connects a referenced API-key connector.
type PackageConnectorCredentialRequest struct {
	AuthMethod string            `json:"auth_method" validate:"required"`
	Fields     map[string]string `json:"fields"`
}

// PackageStreamEvent documents the SSE frames of install, resume, update and
// remove. Type selects which fields are present: started and done carry the
// package id and status; step and step_done carry kind and id; log carries
// stream and data; error carries the Problem fields.
//
// codesync(package-stream): keep in sync with
// apps/web/src/composables/api/usePackageStream.ts.
type PackageStreamEvent struct {
	Type      string            `json:"type" enums:"started,step,log,step_done,done,error"`
	Kind      string            `json:"kind,omitempty" enums:"package,dependency,skills,connector"`
	ID        string            `json:"id,omitempty"`
	Stream    string            `json:"stream,omitempty" enums:"stdout,stderr"`
	Data      string            `json:"data,omitempty"`
	Status    string            `json:"status,omitempty"`
	Version   string            `json:"version,omitempty"`
	Message   string            `json:"message,omitempty"`
	Code      string            `json:"code,omitempty"`
	Args      map[string]string `json:"args,omitempty"`
	Detail    string            `json:"detail,omitempty"`
	RequestID string            `json:"request_id,omitempty"`
}

// --- Handlers ---

// List godoc
// @Summary List the Packages installed for a bot
// @Description Every Package installed on the workspace target with its Skills, dependency references and connector references, plus the canonical Packages of dependencies the workspace carries that no Package references.
// @Tags packages
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param workspace_target_id query string false "Workspace target ID (defaults to the bot's current target)"
// @Param refresh query bool false "Refresh workspace discovery"
// @Success 200 {object} PackageListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/packages [get].
func (h *PackagesHandler) List(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	result, err := h.service.List(c.Request().Context(), botID, strings.TrimSpace(c.QueryParam("workspace_target_id")), c.QueryParam("refresh") == "true")
	if err != nil {
		return h.httpError(err)
	}
	return c.JSON(http.StatusOK, packageListResponse(result))
}

// Get godoc
// @Summary Get one installed Package
// @Tags packages
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param installation_id path string true "Package installation ID"
// @Success 200 {object} PackageItem
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/packages/{installation_id} [get].
func (h *PackagesHandler) Get(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	installationID, err := packageInstallationParam(c)
	if err != nil {
		return err
	}
	item, err := h.service.Get(c.Request().Context(), botID, installationID)
	if err != nil {
		return h.httpError(err)
	}
	return c.JSON(http.StatusOK, packageItem(item, ""))
}

// CheckUpdates godoc
// @Summary Check installed Packages for newer releases
// @Description Compares every installed Package with the registry's current release, runs the dependency update checks, and returns the refreshed list.
// @Tags packages
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param workspace_target_id query string false "Workspace target ID (defaults to the bot's current target)"
// @Success 200 {object} PackageListResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} apperror.Problem
// @Failure 502 {object} apperror.Problem
// @Router /bots/{bot_id}/packages/check-updates [post].
func (h *PackagesHandler) CheckUpdates(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	result, err := h.service.CheckUpdates(c.Request().Context(), botID, strings.TrimSpace(c.QueryParam("workspace_target_id")))
	if err != nil {
		return h.httpError(err)
	}
	return c.JSON(http.StatusOK, packageListResponse(result))
}

// RemovalPreview godoc
// @Summary Preview what removing a Package would do
// @Tags packages
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param installation_id path string true "Package installation ID"
// @Success 200 {object} PackageRemovalPreviewResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/packages/{installation_id}/removal-preview [get].
func (h *PackagesHandler) RemovalPreview(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	installationID, err := packageInstallationParam(c)
	if err != nil {
		return err
	}
	preview, err := h.service.RemovalPreview(c.Request().Context(), botID, installationID)
	if err != nil {
		return h.httpError(err)
	}
	resp := PackageRemovalPreviewResponse{
		InstallationID:   preview.Installation.ID,
		Dependencies:     make([]PackageRemovalPreviewDependency, 0, len(preview.Dependencies)),
		Connectors:       make([]PackageRemovalPreviewConnector, 0, len(preview.Connectors)),
		RequiredPackages: make([]PackageRemovalPreviewPackage, 0, len(preview.RequiredPackages)),
	}
	for _, dep := range preview.Dependencies {
		resp.Dependencies = append(resp.Dependencies, PackageRemovalPreviewDependency{ID: dep.ID, Action: dep.Action, Reason: dep.Reason})
	}
	for _, conn := range preview.Connectors {
		resp.Connectors = append(resp.Connectors, PackageRemovalPreviewConnector{Type: conn.Type, ConnectionID: conn.ConnectionID, Action: conn.Action, Reason: conn.Reason})
	}
	for _, pkg := range preview.RequiredPackages {
		resp.RequiredPackages = append(resp.RequiredPackages, PackageRemovalPreviewPackage{InstallationID: pkg.ID, RegistryID: pkg.RegistryID, PackageID: pkg.PackageID, Version: pkg.Version})
	}
	return c.JSON(http.StatusOK, resp)
}

// Install godoc
// @Summary Install a Package release into a bot workspace
// @Description Installs missing dependencies, publishes the Skills and links connectors, streaming progress. A dependency failure or an unauthorized required connector leaves the installation partial. Events: started, step, log, step_done, done, error.
// @Tags packages
// @Accept json
// @Produce text/event-stream
// @Param bot_id path string true "Bot ID"
// @Param payload body PackageInstallRequest true "Package release to install"
// @Success 200 {object} PackageStreamEvent "SSE stream of operation events"
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Failure 502 {object} apperror.Problem
// @Router /bots/{bot_id}/packages [post].
func (h *PackagesHandler) Install(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	var req PackageInstallRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Wrap(apperror.CodePackageRequestInvalid, err, nil)
	}
	if strings.TrimSpace(req.RegistryID) == "" || strings.TrimSpace(req.PackageID) == "" || !supermarketclient.IsCanonicalSHA256(strings.TrimSpace(req.Revision)) {
		return apperror.New(apperror.CodePackageRequestInvalid, nil)
	}
	return h.stream(c, "install", func(ctx context.Context, sink packages.EventSink) (packages.OperationResult, error) {
		return h.service.Install(ctx, botID, packages.InstallRequest{
			RegistryID: req.RegistryID, PackageID: req.PackageID, Revision: req.Revision,
			WorkspaceTargetID: req.WorkspaceTargetID,
		}, sink)
	})
}

// Resume godoc
// @Summary Continue a partial Package installation
// @Description Installs dependencies that are still missing, reconciles the Skills and links connectors that were authorized since. Events: started, step, log, step_done, done, error.
// @Tags packages
// @Produce text/event-stream
// @Param bot_id path string true "Bot ID"
// @Param installation_id path string true "Package installation ID"
// @Success 200 {object} PackageStreamEvent "SSE stream of operation events"
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Router /bots/{bot_id}/packages/{installation_id}/resume [post].
func (h *PackagesHandler) Resume(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	installationID, err := packageInstallationParam(c)
	if err != nil {
		return err
	}
	return h.stream(c, "resume", func(ctx context.Context, sink packages.EventSink) (packages.OperationResult, error) {
		return h.service.Resume(ctx, botID, installationID, sink)
	})
}

// UpdateSelection godoc
// @Summary Update parts of a Package on a bot workspace
// @Description Updates the selected dependencies to their latest version and, when release is set, moves the installation to the registry's current release, streaming progress. A discovered Package may update its own dependency. Events: started, step, log, step_done, done, error.
// @Tags packages
// @Accept json
// @Produce text/event-stream
// @Param bot_id path string true "Bot ID"
// @Param payload body PackageUpdateRequest true "What to update"
// @Success 200 {object} PackageStreamEvent "SSE stream of operation events"
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Failure 502 {object} apperror.Problem
// @Router /bots/{bot_id}/packages/update [post].
func (h *PackagesHandler) UpdateSelection(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	var req PackageUpdateRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Wrap(apperror.CodePackageRequestInvalid, err, nil)
	}
	if strings.TrimSpace(req.RegistryID) == "" || strings.TrimSpace(req.PackageID) == "" || (!req.Release && len(req.Dependencies) == 0) {
		return apperror.New(apperror.CodePackageRequestInvalid, nil)
	}
	return h.stream(c, "update", func(ctx context.Context, sink packages.EventSink) (packages.OperationResult, error) {
		return h.service.UpdateSelection(ctx, botID, packages.UpdateRequest{
			RegistryID: req.RegistryID, PackageID: req.PackageID, WorkspaceTargetID: req.WorkspaceTargetID,
			Release: req.Release, Dependencies: req.Dependencies,
		}, sink)
	})
}

// Remove godoc
// @Summary Remove a Package from a bot workspace
// @Description Removes the Skills, the dependencies no other Package references and the connections no other Package references, streaming progress. Events: started, step, log, step_done, done, error.
// @Tags packages
// @Produce text/event-stream
// @Param bot_id path string true "Bot ID"
// @Param installation_id path string true "Package installation ID"
// @Param remove_unreferenced_required query bool false "Also remove auto-installed Packages that lose their last reference"
// @Success 200 {object} PackageStreamEvent "SSE stream of operation events"
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Router /bots/{bot_id}/packages/{installation_id} [delete].
func (h *PackagesHandler) Remove(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	installationID, err := packageInstallationParam(c)
	if err != nil {
		return err
	}
	opts := packages.RemoveOptions{RemoveUnreferencedRequired: c.QueryParam("remove_unreferenced_required") == "true"}
	return h.stream(c, "remove", func(ctx context.Context, sink packages.EventSink) (packages.OperationResult, error) {
		return h.service.Remove(ctx, botID, installationID, opts, sink)
	})
}

// BeginConnectorOAuth godoc
// @Summary Authorize a connector a Package references
// @Description Starts OAuth for the connector type and links the resulting Connect-It connection to the Package installation.
// @Tags packages
// @Accept json
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param installation_id path string true "Package installation ID"
// @Param connector_type path string true "Connector type"
// @Param payload body PackageConnectorOAuthRequest true "OAuth request"
// @Success 201 {object} connectsdk.OAuthAuthorization
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Failure 502 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/packages/{installation_id}/connectors/{connector_type}/oauth [post].
func (h *PackagesHandler) BeginConnectorOAuth(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	installationID, connectorType, err := packageConnectorParams(c)
	if err != nil {
		return err
	}
	var req PackageConnectorOAuthRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Wrap(apperror.CodePackageRequestInvalid, err, nil)
	}
	result, err := h.service.BeginConnectorOAuth(c.Request().Context(), botID, installationID, connectorType, req.AuthMethod)
	if err != nil {
		return h.httpError(err)
	}
	return c.JSON(http.StatusCreated, result)
}

// CreateConnectorCredential godoc
// @Summary Connect an API-key connector a Package references
// @Description Sends the credential fields to Connect-It and links the resulting connection to the Package installation.
// @Tags packages
// @Accept json
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param installation_id path string true "Package installation ID"
// @Param connector_type path string true "Connector type"
// @Param payload body PackageConnectorCredentialRequest true "Credential request"
// @Success 201 {object} connectors.Connector
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Failure 502 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/packages/{installation_id}/connectors/{connector_type}/api-key [post].
func (h *PackagesHandler) CreateConnectorCredential(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	installationID, connectorType, err := packageConnectorParams(c)
	if err != nil {
		return err
	}
	var req PackageConnectorCredentialRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Wrap(apperror.CodePackageRequestInvalid, err, nil)
	}
	result, err := h.service.CreateConnectorCredential(c.Request().Context(), botID, installationID, connectorType, req.AuthMethod, req.Fields)
	if err != nil {
		return h.httpError(err)
	}
	return c.JSON(http.StatusCreated, result)
}

// --- helpers ---

func (h *PackagesHandler) authorize(c echo.Context) (string, error) {
	if h.service == nil {
		return "", echo.NewHTTPError(http.StatusServiceUnavailable, "package service not configured")
	}
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return "", err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return "", apperror.New(apperror.CodePackageRequestInvalid, nil)
	}
	bot, err := AuthorizeBotAccess(c.Request().Context(), h.botService, h.accountService, channelIdentityID, botID)
	if err != nil {
		return "", err
	}
	return bot.ID, nil
}

func packageInstallationParam(c echo.Context) (string, error) {
	id := strings.TrimSpace(c.Param("installation_id"))
	if id == "" {
		return "", apperror.New(apperror.CodePackageRequestInvalid, nil)
	}
	return id, nil
}

func packageConnectorParams(c echo.Context) (string, string, error) {
	installationID, err := packageInstallationParam(c)
	if err != nil {
		return "", "", err
	}
	connectorType := strings.TrimSpace(c.Param("connector_type"))
	if !packageConnectorTypePattern.MatchString(connectorType) {
		return "", "", apperror.New(apperror.CodePackageRequestInvalid, nil)
	}
	return installationID, connectorType, nil
}

type packageOperation func(ctx context.Context, sink packages.EventSink) (packages.OperationResult, error)

// stream runs one mutating operation as an SSE stream. The request is
// validated before the stream opens; everything the service reports
// afterwards becomes an error frame. A browser disconnect does not cancel
// the admitted operation.
func (h *PackagesHandler) stream(c echo.Context, action string, run packageOperation) error {
	ctx := context.WithoutCancel(c.Request().Context())
	writer, flusher, err := beginSSEResponse(c)
	if err != nil {
		return err
	}
	stream := newWorkspaceDependencyStream(writer, flusher, workspaceDependencyHeartbeatInterval)
	defer stream.close()
	sink := packages.EventFunc(func(event packages.Event) {
		stream.send(PackageStreamEvent{
			Type: event.Type, Kind: event.Kind, ID: event.ID, Stream: event.Stream, Data: event.Data,
			Status: event.Status, Version: event.Version, Message: event.Message,
		})
	})
	if _, err := run(ctx, sink); err != nil {
		requestID := httpx.RequestID(c)
		h.logger.Warn("package operation failed",
			slog.String("action", action), slog.String("request_id", requestID), slog.Any("error", err))
		stream.send(newPackageErrorEvent(h.httpError(err), requestID))
	}
	return nil
}

func newPackageErrorEvent(err error, requestID string) PackageStreamEvent {
	public, ok := apperror.PublicFrom(err, requestID)
	if !ok {
		return PackageStreamEvent{
			Type: "error", Code: string(apperror.CodePackageOperationFailed), Args: map[string]string{},
			Message: "The Package operation failed.", RequestID: requestID,
		}
	}
	return PackageStreamEvent{
		Type: "error", Code: string(public.Code), Args: public.Args, Detail: public.Detail,
		Message: public.Detail, RequestID: public.RequestID,
	}
}

func (h *PackagesHandler) httpError(err error) error {
	var targetErr *supermarketclient.WorkspaceTargetError
	var statusErr *supermarketclient.StatusError
	switch {
	case err == nil:
		return nil
	case apperror.CodeOf(err) != "":
		return err
	case errors.As(err, &targetErr):
		return workspaceTargetHTTPError(h.logger, targetErr.Err)
	case errors.As(err, &statusErr):
		return echo.NewHTTPError(statusErr.Status, statusErr.Error())
	case errors.Is(err, packages.ErrNotInstalled):
		return apperror.Wrap(apperror.CodePackageNotFound, err, nil)
	case errors.Is(err, packages.ErrInvalidRequest), errors.Is(err, packages.ErrConnectorNotReferenced):
		return apperror.Wrap(apperror.CodePackageRequestInvalid, err, nil)
	case errors.Is(err, connectors.ErrInvalidInput), errors.Is(err, connectors.ErrNotConfigured), errors.Is(err, connectors.ErrUpstreamUnavailable):
		return connectorHTTPError(err)
	case errors.Is(err, workspacedeps.ErrWorkspaceNotRunning), errors.Is(err, workspacedeps.ErrWorkspaceMissing),
		errors.Is(err, workspacedeps.ErrRemoteOffline), errors.Is(err, workspacedeps.ErrBusy),
		errors.Is(err, workspacedeps.ErrDependencyNotFound), errors.Is(err, workspacedeps.ErrCatalogUnavailable),
		errors.Is(err, workspacedeps.ErrDefinitionInvalid), errors.Is(err, workspacedeps.ErrDefinitionUnavailable):
		return workspaceDependencyError(err)
	default:
		var apiErr *connectsdk.APIError
		if errors.As(err, &apiErr) {
			return connectorHTTPError(err)
		}
		return apperror.Wrap(apperror.CodePackageOperationFailed, err, nil)
	}
}

func packageListResponse(result packages.ListResult) PackageListResponse {
	resp := PackageListResponse{
		WorkspaceTargetID:      result.WorkspaceTargetID,
		WorkspaceState:         string(result.Workspace),
		DependencyCatalogStale: result.DependencyCatalogStale,
		Items:                  make([]PackageItem, 0, len(result.Items)),
	}
	for _, item := range result.Items {
		resp.Items = append(resp.Items, packageItem(item, result.DataRoot))
	}
	return resp
}

func packageItem(item packages.Item, dataRoot string) PackageItem {
	out := PackageItem{
		RegistryID: item.RegistryID, PackageID: item.PackageID,
		Revision: item.Revision, Version: item.Version,
		Name:   item.PackageID,
		Tags:   []string{},
		Skills: []PackageSkillItem{}, Dependencies: []PackageDependencyItem{}, Connectors: []PackageConnectorItem{},
	}
	if inst := item.Installation; inst != nil {
		out.InstallationID = inst.ID
		out.Status = string(inst.Status)
		out.Reason = string(inst.Reason)
		out.AvailableRevision = inst.AvailableRevision
		out.AvailableVersion = inst.AvailableVersion
		out.LastCheckedAt = inst.LastCheckedAt
		out.LastError = inst.LastError
		installedAt, updatedAt := inst.InstalledAt, inst.UpdatedAt
		if !installedAt.IsZero() {
			out.InstalledAt = &installedAt
		}
		if !updatedAt.IsZero() {
			out.UpdatedAt = &updatedAt
		}
	} else {
		out.Status = "discovered"
	}
	if release := item.Release; release != nil {
		out.Name = release.Name
		out.Description = release.Description
		out.Icon = release.Icon
		out.Category = release.Category
		out.CategoryName = release.CategoryName
		out.Author = release.Author
		out.Homepage = release.Homepage
		out.Repository = release.Repository
		out.License = release.License
		if release.Tags != nil {
			out.Tags = release.Tags
		}
		out.Translations = release.Translations
		for _, skill := range release.Skills {
			out.Skills = append(out.Skills, PackageSkillItem{
				SkillID: skill.SkillID, InstallID: skill.InstallID, Name: skill.Name, Description: skill.Description, Icon: skill.Icon,
			})
		}
	}
	for _, dep := range item.Dependencies {
		entry := PackageDependencyItem{ID: dep.ID, Shared: dep.Shared}
		if dep.Entry != nil {
			mapped := workspaceDependencyItem(*dep.Entry, dataRoot)
			entry.Dependency = &mapped
			if item.Release == nil {
				out.Name = dep.Entry.Dependency.Name
				out.Description = dep.Entry.Dependency.Description
				out.Category = string(dep.Entry.Dependency.Category)
				if len(dep.Entry.Dependency.Translations) > 0 {
					out.Translations = make(map[string]supermarketclient.PackageTranslation, len(dep.Entry.Dependency.Translations))
					for locale, text := range dep.Entry.Dependency.Translations {
						out.Translations[locale] = supermarketclient.PackageTranslation{Name: text.Name, Description: text.Description}
					}
				}
			}
		}
		out.Dependencies = append(out.Dependencies, entry)
	}
	for _, conn := range item.Connectors {
		entry := PackageConnectorItem{Type: conn.Type, Required: conn.Required, ConnectionID: conn.ConnectionID, Status: "needs_auth", Connector: conn.Connector}
		if conn.ConnectionID != "" {
			entry.Status = "linked"
		}
		out.Connectors = append(out.Connectors, entry)
	}
	return out
}
