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
	"github.com/felinics/memoh/internal/apps"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/connectors"
	"github.com/felinics/memoh/internal/httpx"
	supermarketclient "github.com/felinics/memoh/internal/supermarket"
	"github.com/felinics/memoh/internal/workspacedeps"
)

var appConnectorTypePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// appService is the slice of *apps.Service the routes use.
type appService interface {
	List(ctx context.Context, botID, targetID string, refresh bool) (apps.ListResult, error)
	Get(ctx context.Context, botID, installationID string) (apps.Item, error)
	Install(ctx context.Context, botID string, req apps.InstallRequest, sink apps.EventSink) (apps.OperationResult, error)
	Resume(ctx context.Context, botID, installationID string, sink apps.EventSink) (apps.OperationResult, error)
	UpdateSelection(ctx context.Context, botID string, req apps.UpdateRequest, sink apps.EventSink) (apps.OperationResult, error)
	Remove(ctx context.Context, botID, installationID string, opts apps.RemoveOptions, sink apps.EventSink) (apps.OperationResult, error)
	RemovalPreview(ctx context.Context, botID, installationID string) (apps.RemovalPreview, error)
	CheckUpdates(ctx context.Context, botID, targetID string) (apps.ListResult, error)
	BeginConnectorOAuth(ctx context.Context, botID, installationID, connectorType, authMethod string) (connectsdk.OAuthAuthorization, error)
	CreateConnectorCredential(ctx context.Context, botID, installationID, connectorType, authMethod string, fields map[string]string) (connectors.Connector, error)
}

// AppsHandler serves the Apps a bot has installed from the
// Supermarket and runs their lifecycle operations.
type AppsHandler struct {
	service        appService
	botService     *bots.Service
	accountService *accounts.Service
	logger         *slog.Logger
}

func NewAppsHandler(log *slog.Logger, service *apps.Service, botService *bots.Service, accountService *accounts.Service) *AppsHandler {
	var svc appService
	if service != nil {
		svc = service
	}
	return &AppsHandler{
		service: svc, botService: botService, accountService: accountService,
		logger: log.With(slog.String("handler", "apps")),
	}
}

func (h *AppsHandler) Register(e *echo.Echo) {
	g := e.Group("/bots/:bot_id/apps")
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

// AppSkillItem is one Skill an App materializes.
type AppSkillItem struct {
	SkillID     string                       `json:"skill_id"`
	InstallID   string                       `json:"install_id"`
	Name        string                       `json:"name"`
	Description string                       `json:"description,omitempty"`
	Icon        *supermarketclient.SkillIcon `json:"icon,omitempty"`
}

// AppDependencyItem is one workspace dependency an App references,
// with the reconciled dependency state when the catalog knows it.
type AppDependencyItem struct {
	ID string `json:"id"`
	// Shared is set when another installed App references the same
	// dependency on this workspace target.
	Shared     bool                     `json:"shared"`
	Dependency *WorkspaceDependencyItem `json:"dependency,omitempty"`
}

// AppConnectorItem is one Connect-It connector an App references.
type AppConnectorItem struct {
	Type         string `json:"type"`
	Required     bool   `json:"required"`
	ConnectionID string `json:"connection_id,omitempty"`
	// Status is linked once a connection is bound, otherwise needs_auth.
	Status    string                `json:"status" enums:"linked,needs_auth"`
	Connector *connectors.Connector `json:"connector,omitempty"`
}

// AppItem is one App on a workspace target.
type AppItem struct {
	// InstallationID is empty for a discovered App: a dependency the
	// workspace carries that no installed App references, shown through
	// its canonical App.
	InstallationID string `json:"installation_id,omitempty"`
	RegistryID     string `json:"registry_id"`
	AppID          string `json:"app_id"`
	Revision       string `json:"revision,omitempty"`
	Version        string `json:"version,omitempty"`
	Name           string `json:"name"`
	Description    string `json:"description,omitempty"`
	// Status is discovered for Apps without an installation record.
	Status string `json:"status" enums:"installed,partial,installing,updating,removing,failed,discovered"`
	Reason string `json:"reason,omitempty" enums:"user,required"`
	// AvailableRevision and AvailableVersion name the registry's newer
	// release after a check found one.
	AvailableRevision string                                      `json:"available_revision,omitempty"`
	AvailableVersion  string                                      `json:"available_version,omitempty"`
	LastCheckedAt     *time.Time                                  `json:"last_checked_at,omitempty"`
	LastError         string                                      `json:"last_error,omitempty"`
	Icon              *supermarketclient.SkillIcon                `json:"icon,omitempty"`
	Category          string                                      `json:"category,omitempty"`
	CategoryName      string                                      `json:"category_name,omitempty"`
	Author            *supermarketclient.Author                   `json:"author,omitempty"`
	Homepage          string                                      `json:"homepage,omitempty"`
	Repository        string                                      `json:"repository,omitempty"`
	License           string                                      `json:"license,omitempty"`
	Tags              []string                                    `json:"tags"`
	Translations      map[string]supermarketclient.AppTranslation `json:"translations,omitempty"`
	Skills            []AppSkillItem                              `json:"skills"`
	Dependencies      []AppDependencyItem                         `json:"dependencies"`
	Connectors        []AppConnectorItem                          `json:"connectors"`
	InstalledAt       *time.Time                                  `json:"installed_at,omitempty"`
	UpdatedAt         *time.Time                                  `json:"updated_at,omitempty"`
}

// AppListResponse is the App view of one workspace target.
type AppListResponse struct {
	WorkspaceTargetID      string    `json:"workspace_target_id"`
	WorkspaceState         string    `json:"workspace_state,omitempty" enums:"running,not_running,missing,remote_offline"`
	DependencyCatalogStale bool      `json:"dependency_catalog_stale"`
	Items                  []AppItem `json:"items"`
}

// AppInstallRequest names one immutable App release to install.
type AppInstallRequest struct {
	RegistryID        string `json:"registry_id" validate:"required"`
	AppID             string `json:"app_id" validate:"required"`
	Revision          string `json:"revision" validate:"required"`
	WorkspaceTargetID string `json:"workspace_target_id,omitempty"`
}

// AppUpdateRequest selects what to update for one App on a workspace
// target: its dependencies, its release, or both.
type AppUpdateRequest struct {
	RegistryID        string `json:"registry_id" validate:"required"`
	AppID             string `json:"app_id" validate:"required"`
	WorkspaceTargetID string `json:"workspace_target_id,omitempty"`
	// Release moves the installation to the registry's current release.
	Release bool `json:"release"`
	// Dependencies are updated to their latest version.
	Dependencies []string `json:"dependencies,omitempty"`
}

// AppRemovalPreviewDependency says what removing an App does to one
// dependency reference.
type AppRemovalPreviewDependency struct {
	ID     string `json:"id"`
	Action string `json:"action" enums:"remove,keep"`
	Reason string `json:"reason,omitempty" enums:"shared,image,absent"`
}

// AppRemovalPreviewConnector says what removing an App does to one
// connector reference.
type AppRemovalPreviewConnector struct {
	Type         string `json:"type"`
	ConnectionID string `json:"connection_id,omitempty"`
	Action       string `json:"action" enums:"disconnect,keep,none"`
	Reason       string `json:"reason,omitempty" enums:"shared"`
}

// AppRemovalPreviewApp is an auto-installed App that would lose
// its last reference.
type AppRemovalPreviewApp struct {
	InstallationID string `json:"installation_id"`
	RegistryID     string `json:"registry_id"`
	AppID          string `json:"app_id"`
	Version        string `json:"version,omitempty"`
}

// AppRemovalPreviewResponse is the plan of an App removal.
type AppRemovalPreviewResponse struct {
	InstallationID string                        `json:"installation_id"`
	Dependencies   []AppRemovalPreviewDependency `json:"dependencies"`
	Connectors     []AppRemovalPreviewConnector  `json:"connectors"`
	RequiredApps   []AppRemovalPreviewApp        `json:"required_apps"`
}

// AppConnectorOAuthRequest starts OAuth for a referenced connector.
type AppConnectorOAuthRequest struct {
	AuthMethod string `json:"auth_method" validate:"required"`
}

// AppConnectorCredentialRequest connects a referenced API-key connector.
type AppConnectorCredentialRequest struct {
	AuthMethod string            `json:"auth_method" validate:"required"`
	Fields     map[string]string `json:"fields"`
}

// AppStreamEvent documents the SSE frames of install, resume, update and
// remove. Type selects which fields are present: started and done carry the
// app id and status; step and step_done carry kind and id; log carries
// stream and data; error carries the Problem fields.
//
// codesync(app-stream): keep in sync with
// apps/web/src/composables/api/useAppStream.ts.
type AppStreamEvent struct {
	Type      string            `json:"type" enums:"started,step,log,step_done,done,error"`
	Kind      string            `json:"kind,omitempty" enums:"app,dependency,skills,connector"`
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
// @Summary List the Apps installed for a bot
// @Description Every App installed on the workspace target with its Skills, dependency references and connector references, plus the canonical Apps of dependencies the workspace carries that no App references.
// @Tags apps
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param workspace_target_id query string false "Workspace target ID (defaults to the bot's current target)"
// @Param refresh query bool false "Refresh workspace discovery"
// @Success 200 {object} AppListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/apps [get].
func (h *AppsHandler) List(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	result, err := h.service.List(c.Request().Context(), botID, strings.TrimSpace(c.QueryParam("workspace_target_id")), c.QueryParam("refresh") == "true")
	if err != nil {
		return h.httpError(err)
	}
	return c.JSON(http.StatusOK, appListResponse(result))
}

// Get godoc
// @Summary Get one installed App
// @Tags apps
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param installation_id path string true "App installation ID"
// @Success 200 {object} AppItem
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/apps/{installation_id} [get].
func (h *AppsHandler) Get(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	installationID, err := appInstallationParam(c)
	if err != nil {
		return err
	}
	item, err := h.service.Get(c.Request().Context(), botID, installationID)
	if err != nil {
		return h.httpError(err)
	}
	return c.JSON(http.StatusOK, appItem(item, ""))
}

// CheckUpdates godoc
// @Summary Check installed Apps for newer releases
// @Description Compares every installed App with the registry's current release, runs the dependency update checks, and returns the refreshed list.
// @Tags apps
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param workspace_target_id query string false "Workspace target ID (defaults to the bot's current target)"
// @Success 200 {object} AppListResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} apperror.Problem
// @Failure 502 {object} apperror.Problem
// @Router /bots/{bot_id}/apps/check-updates [post].
func (h *AppsHandler) CheckUpdates(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	result, err := h.service.CheckUpdates(c.Request().Context(), botID, strings.TrimSpace(c.QueryParam("workspace_target_id")))
	if err != nil {
		return h.httpError(err)
	}
	return c.JSON(http.StatusOK, appListResponse(result))
}

// RemovalPreview godoc
// @Summary Preview what removing an App would do
// @Tags apps
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param installation_id path string true "App installation ID"
// @Success 200 {object} AppRemovalPreviewResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/apps/{installation_id}/removal-preview [get].
func (h *AppsHandler) RemovalPreview(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	installationID, err := appInstallationParam(c)
	if err != nil {
		return err
	}
	preview, err := h.service.RemovalPreview(c.Request().Context(), botID, installationID)
	if err != nil {
		return h.httpError(err)
	}
	resp := AppRemovalPreviewResponse{
		InstallationID: preview.Installation.ID,
		Dependencies:   make([]AppRemovalPreviewDependency, 0, len(preview.Dependencies)),
		Connectors:     make([]AppRemovalPreviewConnector, 0, len(preview.Connectors)),
		RequiredApps:   make([]AppRemovalPreviewApp, 0, len(preview.RequiredApps)),
	}
	for _, dep := range preview.Dependencies {
		resp.Dependencies = append(resp.Dependencies, AppRemovalPreviewDependency{ID: dep.ID, Action: dep.Action, Reason: dep.Reason})
	}
	for _, conn := range preview.Connectors {
		resp.Connectors = append(resp.Connectors, AppRemovalPreviewConnector{Type: conn.Type, ConnectionID: conn.ConnectionID, Action: conn.Action, Reason: conn.Reason})
	}
	for _, pkg := range preview.RequiredApps {
		resp.RequiredApps = append(resp.RequiredApps, AppRemovalPreviewApp{InstallationID: pkg.ID, RegistryID: pkg.RegistryID, AppID: pkg.AppID, Version: pkg.Version})
	}
	return c.JSON(http.StatusOK, resp)
}

// Install godoc
// @Summary Install an App release into a bot workspace
// @Description Installs missing dependencies, publishes the Skills and links connectors, streaming progress. A dependency failure or an unauthorized required connector leaves the installation partial. Events: started, step, log, step_done, done, error.
// @Tags apps
// @Accept json
// @Produce text/event-stream
// @Param bot_id path string true "Bot ID"
// @Param payload body AppInstallRequest true "App release to install"
// @Success 200 {object} AppStreamEvent "SSE stream of operation events"
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Failure 502 {object} apperror.Problem
// @Router /bots/{bot_id}/apps [post].
func (h *AppsHandler) Install(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	var req AppInstallRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Wrap(apperror.CodeAppRequestInvalid, err, nil)
	}
	if strings.TrimSpace(req.RegistryID) == "" || strings.TrimSpace(req.AppID) == "" || !supermarketclient.IsCanonicalSHA256(strings.TrimSpace(req.Revision)) {
		return apperror.New(apperror.CodeAppRequestInvalid, nil)
	}
	return h.stream(c, "install", func(ctx context.Context, sink apps.EventSink) (apps.OperationResult, error) {
		return h.service.Install(ctx, botID, apps.InstallRequest{
			RegistryID: req.RegistryID, AppID: req.AppID, Revision: req.Revision,
			WorkspaceTargetID: req.WorkspaceTargetID,
		}, sink)
	})
}

// Resume godoc
// @Summary Continue a partial App installation
// @Description Installs dependencies that are still missing, reconciles the Skills and links connectors that were authorized since. Events: started, step, log, step_done, done, error.
// @Tags apps
// @Produce text/event-stream
// @Param bot_id path string true "Bot ID"
// @Param installation_id path string true "App installation ID"
// @Success 200 {object} AppStreamEvent "SSE stream of operation events"
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Router /bots/{bot_id}/apps/{installation_id}/resume [post].
func (h *AppsHandler) Resume(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	installationID, err := appInstallationParam(c)
	if err != nil {
		return err
	}
	return h.stream(c, "resume", func(ctx context.Context, sink apps.EventSink) (apps.OperationResult, error) {
		return h.service.Resume(ctx, botID, installationID, sink)
	})
}

// UpdateSelection godoc
// @Summary Update parts of an App on a bot workspace
// @Description Updates the selected dependencies to their latest version and, when release is set, moves the installation to the registry's current release, streaming progress. A discovered App may update its own dependency. Events: started, step, log, step_done, done, error.
// @Tags apps
// @Accept json
// @Produce text/event-stream
// @Param bot_id path string true "Bot ID"
// @Param payload body AppUpdateRequest true "What to update"
// @Success 200 {object} AppStreamEvent "SSE stream of operation events"
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Failure 502 {object} apperror.Problem
// @Router /bots/{bot_id}/apps/update [post].
func (h *AppsHandler) UpdateSelection(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	var req AppUpdateRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Wrap(apperror.CodeAppRequestInvalid, err, nil)
	}
	if strings.TrimSpace(req.RegistryID) == "" || strings.TrimSpace(req.AppID) == "" || (!req.Release && len(req.Dependencies) == 0) {
		return apperror.New(apperror.CodeAppRequestInvalid, nil)
	}
	return h.stream(c, "update", func(ctx context.Context, sink apps.EventSink) (apps.OperationResult, error) {
		return h.service.UpdateSelection(ctx, botID, apps.UpdateRequest{
			RegistryID: req.RegistryID, AppID: req.AppID, WorkspaceTargetID: req.WorkspaceTargetID,
			Release: req.Release, Dependencies: req.Dependencies,
		}, sink)
	})
}

// Remove godoc
// @Summary Remove an App from a bot workspace
// @Description Removes the Skills, the dependencies no other App references and the connections no other App references, streaming progress. Events: started, step, log, step_done, done, error.
// @Tags apps
// @Produce text/event-stream
// @Param bot_id path string true "Bot ID"
// @Param installation_id path string true "App installation ID"
// @Param remove_unreferenced_required query bool false "Also remove auto-installed Apps that lose their last reference"
// @Success 200 {object} AppStreamEvent "SSE stream of operation events"
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Router /bots/{bot_id}/apps/{installation_id} [delete].
func (h *AppsHandler) Remove(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	installationID, err := appInstallationParam(c)
	if err != nil {
		return err
	}
	opts := apps.RemoveOptions{RemoveUnreferencedRequired: c.QueryParam("remove_unreferenced_required") == "true"}
	return h.stream(c, "remove", func(ctx context.Context, sink apps.EventSink) (apps.OperationResult, error) {
		return h.service.Remove(ctx, botID, installationID, opts, sink)
	})
}

// BeginConnectorOAuth godoc
// @Summary Authorize a connector an App references
// @Description Starts OAuth for the connector type and links the resulting Connect-It connection to the App installation.
// @Tags apps
// @Accept json
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param installation_id path string true "App installation ID"
// @Param connector_type path string true "Connector type"
// @Param payload body AppConnectorOAuthRequest true "OAuth request"
// @Success 201 {object} connectsdk.OAuthAuthorization
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Failure 502 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/apps/{installation_id}/connectors/{connector_type}/oauth [post].
func (h *AppsHandler) BeginConnectorOAuth(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	installationID, connectorType, err := appConnectorParams(c)
	if err != nil {
		return err
	}
	var req AppConnectorOAuthRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Wrap(apperror.CodeAppRequestInvalid, err, nil)
	}
	result, err := h.service.BeginConnectorOAuth(c.Request().Context(), botID, installationID, connectorType, req.AuthMethod)
	if err != nil {
		return h.httpError(err)
	}
	return c.JSON(http.StatusCreated, result)
}

// CreateConnectorCredential godoc
// @Summary Connect an API-key connector an App references
// @Description Sends the credential fields to Connect-It and links the resulting connection to the App installation.
// @Tags apps
// @Accept json
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param installation_id path string true "App installation ID"
// @Param connector_type path string true "Connector type"
// @Param payload body AppConnectorCredentialRequest true "Credential request"
// @Success 201 {object} connectors.Connector
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Failure 502 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/apps/{installation_id}/connectors/{connector_type}/api-key [post].
func (h *AppsHandler) CreateConnectorCredential(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	installationID, connectorType, err := appConnectorParams(c)
	if err != nil {
		return err
	}
	var req AppConnectorCredentialRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Wrap(apperror.CodeAppRequestInvalid, err, nil)
	}
	result, err := h.service.CreateConnectorCredential(c.Request().Context(), botID, installationID, connectorType, req.AuthMethod, req.Fields)
	if err != nil {
		return h.httpError(err)
	}
	return c.JSON(http.StatusCreated, result)
}

// --- helpers ---

func (h *AppsHandler) authorize(c echo.Context) (string, error) {
	if h.service == nil {
		return "", echo.NewHTTPError(http.StatusServiceUnavailable, "app service not configured")
	}
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return "", err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return "", apperror.New(apperror.CodeAppRequestInvalid, nil)
	}
	bot, err := AuthorizeBotAccess(c.Request().Context(), h.botService, h.accountService, channelIdentityID, botID)
	if err != nil {
		return "", err
	}
	return bot.ID, nil
}

func appInstallationParam(c echo.Context) (string, error) {
	id := strings.TrimSpace(c.Param("installation_id"))
	if id == "" {
		return "", apperror.New(apperror.CodeAppRequestInvalid, nil)
	}
	return id, nil
}

func appConnectorParams(c echo.Context) (string, string, error) {
	installationID, err := appInstallationParam(c)
	if err != nil {
		return "", "", err
	}
	connectorType := strings.TrimSpace(c.Param("connector_type"))
	if !appConnectorTypePattern.MatchString(connectorType) {
		return "", "", apperror.New(apperror.CodeAppRequestInvalid, nil)
	}
	return installationID, connectorType, nil
}

type appOperation func(ctx context.Context, sink apps.EventSink) (apps.OperationResult, error)

// stream runs one mutating operation as an SSE stream. The request is
// validated before the stream opens; everything the service reports
// afterwards becomes an error frame. A browser disconnect does not cancel
// the admitted operation.
func (h *AppsHandler) stream(c echo.Context, action string, run appOperation) error {
	ctx := context.WithoutCancel(c.Request().Context())
	writer, flusher, err := beginSSEResponse(c)
	if err != nil {
		return err
	}
	stream := newWorkspaceDependencyStream(writer, flusher, workspaceDependencyHeartbeatInterval)
	defer stream.close()
	sink := apps.EventFunc(func(event apps.Event) {
		stream.send(AppStreamEvent{
			Type: event.Type, Kind: event.Kind, ID: event.ID, Stream: event.Stream, Data: event.Data,
			Status: event.Status, Version: event.Version, Message: event.Message,
		})
	})
	if _, err := run(ctx, sink); err != nil {
		requestID := httpx.RequestID(c)
		h.logger.Warn("app operation failed",
			slog.String("action", action), slog.String("request_id", requestID), slog.Any("error", err))
		stream.send(newAppErrorEvent(h.httpError(err), requestID))
	}
	return nil
}

func newAppErrorEvent(err error, requestID string) AppStreamEvent {
	public, ok := apperror.PublicFrom(err, requestID)
	if !ok {
		return AppStreamEvent{
			Type: "error", Code: string(apperror.CodeAppOperationFailed), Args: map[string]string{},
			Message: "The App operation failed.", RequestID: requestID,
		}
	}
	return AppStreamEvent{
		Type: "error", Code: string(public.Code), Args: public.Args, Detail: public.Detail,
		Message: public.Detail, RequestID: public.RequestID,
	}
}

func (h *AppsHandler) httpError(err error) error {
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
	case errors.Is(err, apps.ErrNotInstalled):
		return apperror.Wrap(apperror.CodeAppNotFound, err, nil)
	case errors.Is(err, apps.ErrInvalidRequest), errors.Is(err, apps.ErrConnectorNotReferenced):
		return apperror.Wrap(apperror.CodeAppRequestInvalid, err, nil)
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
		return apperror.Wrap(apperror.CodeAppOperationFailed, err, nil)
	}
}

func appListResponse(result apps.ListResult) AppListResponse {
	resp := AppListResponse{
		WorkspaceTargetID:      result.WorkspaceTargetID,
		WorkspaceState:         string(result.Workspace),
		DependencyCatalogStale: result.DependencyCatalogStale,
		Items:                  make([]AppItem, 0, len(result.Items)),
	}
	for _, item := range result.Items {
		resp.Items = append(resp.Items, appItem(item, result.DataRoot))
	}
	return resp
}

func appItem(item apps.Item, dataRoot string) AppItem {
	out := AppItem{
		RegistryID: item.RegistryID, AppID: item.AppID,
		Revision: item.Revision, Version: item.Version,
		Name:   item.AppID,
		Tags:   []string{},
		Skills: []AppSkillItem{}, Dependencies: []AppDependencyItem{}, Connectors: []AppConnectorItem{},
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
			out.Skills = append(out.Skills, AppSkillItem{
				SkillID: skill.SkillID, InstallID: skill.InstallID, Name: skill.Name, Description: skill.Description, Icon: skill.Icon,
			})
		}
	}
	for _, dep := range item.Dependencies {
		entry := AppDependencyItem{ID: dep.ID, Shared: dep.Shared}
		if dep.Entry != nil {
			mapped := workspaceDependencyItem(*dep.Entry, dataRoot)
			entry.Dependency = &mapped
			if item.Release == nil {
				out.Name = dep.Entry.Dependency.Name
				out.Description = dep.Entry.Dependency.Description
				out.Category = string(dep.Entry.Dependency.Category)
				if len(dep.Entry.Dependency.Translations) > 0 {
					out.Translations = make(map[string]supermarketclient.AppTranslation, len(dep.Entry.Dependency.Translations))
					for locale, text := range dep.Entry.Dependency.Translations {
						out.Translations[locale] = supermarketclient.AppTranslation{Name: text.Name, Description: text.Description}
					}
				}
			}
		}
		out.Dependencies = append(out.Dependencies, entry)
	}
	for _, conn := range item.Connectors {
		entry := AppConnectorItem{Type: conn.Type, Required: conn.Required, ConnectionID: conn.ConnectionID, Status: "needs_auth", Connector: conn.Connector}
		if conn.ConnectionID != "" {
			entry.Status = "linked"
		}
		out.Connectors = append(out.Connectors, entry)
	}
	return out
}
