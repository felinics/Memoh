package handlers

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"golang.org/x/time/rate"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/httpx"
	"github.com/felinics/memoh/internal/workspace/bridge"
	"github.com/felinics/memoh/internal/workspacedeps"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

var workspaceDependencyIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// workspaceDependencyService is the slice of *workspacedeps.Service the
// dependency routes use.
type workspaceDependencyService interface {
	Refresh(ctx context.Context, botID, targetID string) (workspacedeps.ListResult, error)
	Icon(ctx context.Context, digest string) ([]byte, error)
	Catalog(ctx context.Context, refresh bool) (workspacedeps.CatalogView, error)
	List(ctx context.Context, botID, targetID string) (workspacedeps.ListResult, error)
	Preflight(ctx context.Context, botID, targetID string, depIDs []string) (workspacedeps.PreflightResult, error)
	Install(ctx context.Context, botID, targetID, depID, version string, sink workspacedeps.LogSink) (workspacedeps.OperationResult, error)
	Update(ctx context.Context, botID, targetID, depID, version string, sink workspacedeps.LogSink) (workspacedeps.OperationResult, error)
	Reinstall(ctx context.Context, botID, targetID, depID, version string, sink workspacedeps.LogSink) (workspacedeps.OperationResult, error)
	Remove(ctx context.Context, botID, targetID, depID string, sink workspacedeps.LogSink) (workspacedeps.OperationResult, error)
	Rollback(ctx context.Context, botID, targetID, depID string) (workspacedeps.OperationResult, error)
	CheckUpdates(ctx context.Context, botID, targetID string) (workspacedeps.ListResult, error)
	ScriptPreviewDetails(ctx context.Context, botID, targetID, depID string, action catalog.Action) (workspacedeps.ScriptPreview, error)
}

// SetWorkspaceDependencyService installs the dependency service behind
// /bots/:bot_id/dependencies. Without it the routes answer 503.
func (h *ContainerdHandler) SetWorkspaceDependencyService(svc workspaceDependencyService) {
	h.workspaceDeps = svc
}

// workspaceDependencyHeartbeatInterval keeps the SSE connection alive through
// proxies while an install downloads for minutes without printing anything.
// Heartbeats are SSE comment lines, which every parser drops before the
// frontend's event type guard sees them.
const workspaceDependencyHeartbeatInterval = 15 * time.Second

// platformReasonUnsupported is the platform_reason code for dependencies the
// probed platform cannot run.
const platformReasonUnsupported = "unsupported_platform"

// WorkspaceDependencyPlatform is the probed platform of the workspace target.
type WorkspaceDependencyPlatform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
	Libc string `json:"libc,omitempty"`
}

// WorkspaceDependencyItem is one catalog dependency reconciled with its
// installation record and the workspace.
type WorkspaceDependencyItem struct {
	LastErrorCode      string                                    `json:"last_error_code,omitempty"`
	RegistryID         string                                    `json:"registry_id,omitempty"`
	DefinitionRevision string                                    `json:"definition_revision,omitempty"`
	IconURL            string                                    `json:"icon_url,omitempty"`
	Translations       map[string]WorkspaceDependencyTranslation `json:"translations,omitempty"`
	Retired            bool                                      `json:"retired,omitempty"`
	ID                 string                                    `json:"id"`
	Name               string                                    `json:"name"`
	Description        string                                    `json:"description,omitempty"`
	// Category is agent, runtime, or tool.
	Category string `json:"category" enums:"agent,runtime,tool"`
	// Source is image for dependencies shipped with the workspace image and
	// managed for dependencies installed by catalog scripts.
	Source string `json:"source" enums:"image,managed"`
	Icon   string `json:"icon,omitempty"`
	// Provides lists the commands the dependency makes available.
	Provides []string `json:"provides"`
	// PlatformSupported is false when the probed workspace platform is not
	// listed by the catalog manifest; PlatformReason then says why.
	PlatformSupported bool   `json:"platform_supported"`
	PlatformReason    string `json:"platform_reason,omitempty" enums:"unsupported_platform"`
	// Status is omitted when the dependency has no record and was not found
	// in the workspace.
	Status string `json:"status,omitempty" enums:"installed,installing,updating,removing,missing,failed"`
	// InstalledVersion is the version of the copy in effect: the one the
	// runtime launches and the one first on PATH (managed, then image, then
	// PATH).
	InstalledVersion string `json:"installed_version,omitempty"`
	// ImageVersion is the version of the workspace's toolkit copy, omitted
	// when no toolkit copy remains. Native removal clears it as well.
	ImageVersion string `json:"image_version,omitempty"`
	// Overlay is set when the copy in effect is a managed one installed over
	// an image copy.
	Overlay bool `json:"overlay,omitempty"`
	// LatestVersion is the last upstream check result, omitted until a check
	// ran.
	LatestVersion string `json:"latest_version,omitempty"`
	// UpdateAvailable is set for installed dependencies whose last upstream
	// check reported a version other than the one in effect.
	UpdateAvailable bool       `json:"update_available,omitempty"`
	LastCheckedAt   *time.Time `json:"last_checked_at,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
	// PreviousVersion is the version rollback would switch back to.
	PreviousVersion string `json:"previous_version,omitempty"`
	// InstallPath is the dependency home when a managed copy is in effect or
	// can be installed, and the discovered command path when the image copy
	// is in effect.
	InstallPath string `json:"install_path,omitempty"`
	// Actions lists what may be requested right now.
	Actions []string `json:"actions" enums:"install,update,reinstall,remove,rollback,check_update"`
}

type WorkspaceDependencyTranslation struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

func dependencyTranslations(dep catalog.Dependency) map[string]WorkspaceDependencyTranslation {
	result := make(map[string]WorkspaceDependencyTranslation, len(dep.Translations))
	for language, text := range dep.Translations {
		result[language] = WorkspaceDependencyTranslation{Name: text.Name, Description: text.Description}
	}
	return result
}

// WorkspaceDependencyListResponse is the reconciled dependency view of one
// workspace target.
type WorkspaceDependencyListResponse struct {
	CatalogStale     bool                         `json:"catalog_stale"`
	CatalogFetchedAt *time.Time                   `json:"catalog_fetched_at,omitempty"`
	WorkspaceState   string                       `json:"workspace_state" enums:"running,not_running,missing,remote_offline"`
	Platform         *WorkspaceDependencyPlatform `json:"platform,omitempty"`
	Items            []WorkspaceDependencyItem    `json:"items"`
	// DiscoveryError is set when the workspace is running but could not be
	// inspected (the discovery command was killed or timed out). Items then
	// reflect the installation records alone, without workspace facts or
	// actions; a refresh retries discovery.
	DiscoveryError string `json:"discovery_error,omitempty"`
}

// WorkspaceDependencyPreflightRequest names the dependencies an agent needs.
type WorkspaceDependencyPreflightRequest struct {
	DependencyIDs []string `json:"dependency_ids"`
	// WorkspaceTargetID overrides the query parameter of the same name.
	WorkspaceTargetID string `json:"workspace_target_id,omitempty"`
}

// WorkspaceDependencyPreflightItem is the verdict for one dependency.
type WorkspaceDependencyPreflightItem struct {
	DependencyID     string `json:"dependency_id"`
	Name             string `json:"name"`
	InstalledVersion string `json:"installed_version,omitempty"`
	State            string `json:"state" enums:"satisfied,missing,platform_unsupported,unknown_dependency"`
}

// WorkspaceDependencyInstallRequest is the optional body of install, update,
// and reinstall.
type WorkspaceDependencyInstallRequest struct {
	// SessionID optionally routes operation progress to its originating conversation.
	SessionID          string `json:"session_id,omitempty"`
	DefinitionRevision string `json:"definition_revision,omitempty"`
	// Version to install. Empty (or no body) installs the latest version the
	// catalog script resolves, or the manifest pin when the dependency has
	// one. The version recorded afterwards is the one the script reports.
	Version string `json:"version,omitempty"`
}

// WorkspaceDependencyPreflightResponse reports whether the requested
// dependencies are ready. Items is empty unless the workspace is running.
type WorkspaceDependencyPreflightResponse struct {
	WorkspaceState string                             `json:"workspace_state" enums:"running,not_running,missing,remote_offline"`
	Items          []WorkspaceDependencyPreflightItem `json:"items"`
}

// WorkspaceDependencyOperationResponse is the receipt of a synchronous
// operation such as rollback.
type WorkspaceDependencyOperationResponse struct {
	DefinitionRevision string            `json:"definition_revision,omitempty"`
	DependencyID       string            `json:"dependency_id"`
	Action             string            `json:"action"`
	Version            string            `json:"version,omitempty"`
	Entrypoints        map[string]string `json:"entrypoints,omitempty"`
	Status             string            `json:"status,omitempty"`
}

// WorkspaceDependencyScriptEnv is one environment variable the script sees.
type WorkspaceDependencyScriptEnv struct {
	Key string `json:"key"`
	// Value is empty when Secret is set.
	Value  string `json:"value"`
	Secret bool   `json:"secret"`
}

// WorkspaceDependencyScriptResponse is the exact script an action would run.
type WorkspaceDependencyScriptResponse struct {
	DefinitionRevision string                         `json:"definition_revision,omitempty"`
	DependencyID       string                         `json:"dependency_id"`
	Action             string                         `json:"action" enums:"install,update,remove,reinstall,rollback"`
	Digest             string                         `json:"digest"`
	Exec               string                         `json:"exec"`
	TimeoutSeconds     int                            `json:"timeout_seconds"`
	Env                []WorkspaceDependencyScriptEnv `json:"env"`
	Script             string                         `json:"script"`
}

// WorkspaceDependencyStreamEvent documents the SSE frames of install, update,
// reinstall, and remove. Type selects which fields are present: started
// carries dependency_id and the requested version (absent for latest); log
// carries stream and data; done carries the installed version and
// entrypoints; error carries the Problem fields.
//
// codesync(workspace-dependency-stream): keep in sync with
// apps/web/src/composables/api/useWorkspaceDependencyStream.ts.
type WorkspaceDependencyStreamEvent struct {
	DefinitionRevision string            `json:"definition_revision,omitempty"`
	Type               string            `json:"type" enums:"started,log,done,error"`
	DependencyID       string            `json:"dependency_id,omitempty"`
	Version            string            `json:"version,omitempty"`
	Stream             string            `json:"stream,omitempty" enums:"stdout,stderr"`
	Data               string            `json:"data,omitempty"`
	Entrypoints        map[string]string `json:"entrypoints,omitempty"`
	Code               string            `json:"code,omitempty"`
	Args               map[string]string `json:"args,omitempty"`
	Detail             string            `json:"detail,omitempty"`
	Message            string            `json:"message,omitempty"`
	RequestID          string            `json:"request_id,omitempty"`
}

// The frames actually written. They are separate from the documentation
// struct so a log line that is empty still carries its data field.
type workspaceDependencyStartedEvent struct {
	DefinitionRevision string `json:"definition_revision,omitempty"`
	Type               string `json:"type"`
	DependencyID       string `json:"dependency_id"`
	Version            string `json:"version,omitempty"`
}

type workspaceDependencyLogEvent struct {
	Type   string `json:"type"`
	Stream string `json:"stream"`
	Data   string `json:"data"`
}

type workspaceDependencyDoneEvent struct {
	DefinitionRevision string            `json:"definition_revision,omitempty"`
	Type               string            `json:"type"`
	Version            string            `json:"version,omitempty"`
	Entrypoints        map[string]string `json:"entrypoints,omitempty"`
}

type workspaceDependencyErrorEvent struct {
	Type      string            `json:"type"`
	Code      string            `json:"code"`
	Args      map[string]string `json:"args"`
	Detail    string            `json:"detail,omitempty"`
	Message   string            `json:"message"`
	RequestID string            `json:"request_id,omitempty"`
}

// ListWorkspaceDependencies godoc
// @Summary List workspace dependencies
// @Description Every catalog dependency (image-provided runtimes, managed agent CLIs and tools) reconciled with its installation record and, when the workspace is running, with what is actually installed.
// @Tags containerd
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param workspace_target_id query string false "Workspace target ID (defaults to the bot's current target)"
// @Success 200 {object} WorkspaceDependencyListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Param refresh query bool false "Refresh definitions and workspace discovery"
// @Router /bots/{bot_id}/dependencies [get].
func (h *ContainerdHandler) ListWorkspaceDependencies(c echo.Context) error {
	botID, svc, err := h.workspaceDependencyRequest(c)
	if err != nil {
		return err
	}
	ctx, targetID, err := h.workspaceDependencyTarget(c, botID, "")
	if err != nil {
		return err
	}
	var result workspacedeps.ListResult
	if c.QueryParam("refresh") == "true" {
		result, err = svc.Refresh(ctx, botID, targetID)
	} else {
		result, err = svc.List(ctx, botID, targetID)
	}
	if err != nil {
		return workspaceDependencyError(err)
	}
	return c.JSON(http.StatusOK, workspaceDependencyListResponse(result))
}

// CheckWorkspaceDependencyUpdates godoc
// @Summary Check workspace dependencies for updates
// @Description Re-discovers the workspace and runs the upstream update check of every installed tool dependency, then returns the refreshed list.
// @Tags containerd
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param workspace_target_id query string false "Workspace target ID (defaults to the bot's current target)"
// @Success 200 {object} WorkspaceDependencyListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/dependencies/check-updates [post].
func (h *ContainerdHandler) CheckWorkspaceDependencyUpdates(c echo.Context) error {
	botID, svc, err := h.workspaceDependencyRequest(c)
	if err != nil {
		return err
	}
	ctx, targetID, err := h.workspaceDependencyTarget(c, botID, "")
	if err != nil {
		return err
	}
	result, err := svc.CheckUpdates(ctx, botID, targetID)
	if err != nil {
		return workspaceDependencyError(err)
	}
	return c.JSON(http.StatusOK, workspaceDependencyListResponse(result))
}

// PreflightWorkspaceDependencies godoc
// @Summary Check whether dependencies are ready
// @Description Reports for each requested dependency whether a copy is installed, whatever its version. Never starts the workspace: when it is not running, items is empty and workspace_state says why.
// @Tags containerd
// @Accept json
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param workspace_target_id query string false "Workspace target ID (defaults to the bot's current target)"
// @Param payload body WorkspaceDependencyPreflightRequest true "Dependencies to check"
// @Success 200 {object} WorkspaceDependencyPreflightResponse
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/dependencies/preflight [post].
func (h *ContainerdHandler) PreflightWorkspaceDependencies(c echo.Context) error {
	botID, svc, err := h.workspaceDependencyRequest(c)
	if err != nil {
		return err
	}
	var req WorkspaceDependencyPreflightRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Wrap(apperror.CodeWorkspaceDependencyRequestInvalid, err, nil)
	}
	depIDs := make([]string, 0, len(req.DependencyIDs))
	for _, id := range req.DependencyIDs {
		if id = strings.TrimSpace(id); id != "" {
			depIDs = append(depIDs, id)
		}
	}
	if len(depIDs) == 0 {
		return apperror.Wrap(apperror.CodeWorkspaceDependencyRequestInvalid, errors.New("dependency_ids is required"), nil)
	}
	ctx, targetID, err := h.workspaceDependencyTarget(c, botID, req.WorkspaceTargetID)
	if err != nil {
		return err
	}
	result, err := svc.Preflight(ctx, botID, targetID, depIDs)
	if err != nil {
		return workspaceDependencyError(err)
	}
	items := make([]WorkspaceDependencyPreflightItem, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, WorkspaceDependencyPreflightItem{
			DependencyID:     item.DependencyID,
			Name:             item.Name,
			InstalledVersion: item.InstalledVersion,
			State:            preflightState(item),
		})
	}
	return c.JSON(http.StatusOK, WorkspaceDependencyPreflightResponse{
		WorkspaceState: string(result.Workspace),
		Items:          items,
	})
}

// InstallWorkspaceDependency godoc
// @Summary Install or reinstall a referenced workspace dependency
// @Description Runs the catalog install script for a dependency an App references and streams its output: a retry after a failed App step, or a managed overlay laid over the copy the workspace image ships. New dependencies reach a bot by installing the App that references them. Events: started, log, done, error.
// @Tags containerd
// @Accept json
// @Produce text/event-stream
// @Param bot_id path string true "Bot ID"
// @Param dep_id path string true "Dependency ID"
// @Param workspace_target_id query string false "Workspace target ID (defaults to the bot's current target)"
// @Param payload body WorkspaceDependencyInstallRequest false "Version to install (optional)"
// @Success 200 {object} WorkspaceDependencyStreamEvent "SSE stream of operation events"
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Failure 422 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/dependencies/{dep_id}/install [post].
func (h *ContainerdHandler) InstallWorkspaceDependency(c echo.Context) error {
	return h.streamWorkspaceDependencyOperation(c, catalog.ActionInstall, workspaceDependencyService.Install)
}

// UpdateWorkspaceDependency godoc
// @Summary Update a workspace dependency
// @Description Runs the catalog update script (or the install script when the manifest has none) and streams its output. The optional body names the version to update to; without one the script picks the latest version (or the manifest pin). The previous version is kept for rollback.
// @Tags containerd
// @Accept json
// @Produce text/event-stream
// @Param bot_id path string true "Bot ID"
// @Param dep_id path string true "Dependency ID"
// @Param workspace_target_id query string false "Workspace target ID (defaults to the bot's current target)"
// @Param payload body WorkspaceDependencyInstallRequest false "Version to update to (optional)"
// @Success 200 {object} WorkspaceDependencyStreamEvent "SSE stream of operation events"
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Failure 422 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/dependencies/{dep_id}/update [post].
func (h *ContainerdHandler) UpdateWorkspaceDependency(c echo.Context) error {
	return h.streamWorkspaceDependencyOperation(c, catalog.ActionUpdate, workspaceDependencyService.Update)
}

// ReinstallWorkspaceDependency godoc
// @Summary Reinstall a workspace dependency
// @Description Runs the catalog reinstall script, or remove followed by install, and streams the output. The optional body names the version to install; without one the script picks the latest version (or the manifest pin).
// @Tags containerd
// @Accept json
// @Produce text/event-stream
// @Param bot_id path string true "Bot ID"
// @Param dep_id path string true "Dependency ID"
// @Param workspace_target_id query string false "Workspace target ID (defaults to the bot's current target)"
// @Param payload body WorkspaceDependencyInstallRequest false "Version to install (optional)"
// @Success 200 {object} WorkspaceDependencyStreamEvent "SSE stream of operation events"
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Failure 422 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/dependencies/{dep_id}/reinstall [post].
func (h *ContainerdHandler) ReinstallWorkspaceDependency(c echo.Context) error {
	return h.streamWorkspaceDependencyOperation(c, catalog.ActionReinstall, workspaceDependencyService.Reinstall)
}

// RollbackWorkspaceDependency godoc
// @Summary Roll a workspace dependency back to its previous version
// @Description Switches the dependency back to the previous version kept in the workspace. A pure data operation: nothing is downloaded and no log is streamed.
// @Tags containerd
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param dep_id path string true "Dependency ID"
// @Param workspace_target_id query string false "Workspace target ID (defaults to the bot's current target)"
// @Success 200 {object} WorkspaceDependencyOperationResponse
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Failure 422 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/dependencies/{dep_id}/rollback [post].
func (h *ContainerdHandler) RollbackWorkspaceDependency(c echo.Context) error {
	botID, svc, err := h.workspaceDependencyRequest(c)
	if err != nil {
		return err
	}
	depID := strings.TrimSpace(c.Param("dep_id"))
	if !workspaceDependencyIDPattern.MatchString(depID) {
		return apperror.New(apperror.CodeWorkspaceDependencyRequestInvalid, nil)
	}
	ctx, targetID, err := h.workspaceDependencyTarget(c, botID, "")
	if err != nil {
		return err
	}
	result, err := svc.Rollback(ctx, botID, targetID, depID)
	if err != nil {
		return workspaceDependencyError(err)
	}
	return c.JSON(http.StatusOK, WorkspaceDependencyOperationResponse{
		DependencyID:       result.DependencyID,
		DefinitionRevision: result.DefinitionRevision,
		Action:             string(result.Action),
		Version:            result.Version,
		Entrypoints:        result.Entrypoints,
		Status:             string(result.Installation.Status),
	})
}

// GetWorkspaceDependencyScript godoc
// @Summary Show the script a dependency action would run
// @Description The exact stdin text the workspace shell receives, prelude included, with the command, time budget, and environment the runner uses. Scripts never touch the workspace disk, so this is the only way to inspect them.
// @Tags containerd
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param dep_id path string true "Dependency ID"
// @Param action query string false "Action" Enums(install, update, remove, reinstall, rollback) default(install)
// @Param workspace_target_id query string false "Workspace target ID (defaults to the bot's current target)"
// @Success 200 {object} WorkspaceDependencyScriptResponse
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Failure 422 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Param definition_revision query string false "Keep a previously prepared definition revision"
// @Router /bots/{bot_id}/dependencies/{dep_id}/script [get].
func (h *ContainerdHandler) GetWorkspaceDependencyScript(c echo.Context) error {
	botID, svc, err := h.workspaceDependencyRequest(c)
	if err != nil {
		return err
	}
	depID, err := workspaceDependencyParam(c)
	if err != nil {
		return err
	}
	action, err := workspaceDependencyScriptAction(c.QueryParam("action"))
	if err != nil {
		return err
	}
	ctx, targetID, err := h.workspaceDependencyTarget(c, botID, "")
	if err != nil {
		return err
	}
	revision := strings.TrimSpace(c.QueryParam("definition_revision"))
	if revision != "" && !catalog.ValidRevision(revision) {
		return apperror.New(apperror.CodeWorkspaceDependencyRequestInvalid, nil)
	}
	ctx = workspacedeps.WithDefinitionRevision(ctx, revision)
	preview, err := svc.ScriptPreviewDetails(ctx, botID, targetID, depID, action)
	if err != nil {
		return workspaceDependencyError(err)
	}
	env := make([]WorkspaceDependencyScriptEnv, 0, len(preview.Env))
	for _, entry := range preview.Env {
		env = append(env, WorkspaceDependencyScriptEnv{Key: entry.Key, Value: entry.Value, Secret: entry.Secret})
	}
	return c.JSON(http.StatusOK, WorkspaceDependencyScriptResponse{
		DependencyID:       preview.DependencyID,
		DefinitionRevision: preview.Revision,
		Action:             string(preview.Action),
		Digest:             preview.Digest,
		Exec:               preview.Exec,
		TimeoutSeconds:     preview.TimeoutSeconds,
		Env:                env,
		Script:             preview.Script,
	})
}

// workspaceDependencyOperation is the shape shared by the four streamed
// service methods. version is the requested version for install-like
// actions and ignored by remove.
type workspaceDependencyOperation func(svc workspaceDependencyService, ctx context.Context, botID, targetID, depID, version string, sink workspacedeps.LogSink) (workspacedeps.OperationResult, error)

// streamWorkspaceDependencyOperation runs one mutating action as an SSE
// stream: started, then one log frame per output line, then done or error.
// Request validation happens before the stream opens so unknown dependencies,
// unsupported actions, and malformed bodies are ordinary Problem responses;
// everything the service reports afterwards becomes an error frame carrying
// the same code. Closing the request disconnects observation; the admitted
// operation finishes under its own manifest budget and Server lifecycle.
func (h *ContainerdHandler) streamWorkspaceDependencyOperation(c echo.Context, action catalog.Action, run workspaceDependencyOperation) error {
	botID, svc, err := h.workspaceDependencyRequest(c)
	if err != nil {
		return err
	}
	depID, err := workspaceDependencyParam(c)
	if err != nil {
		return err
	}
	request, err := workspaceDependencyOperationRequest(c, action)
	if err != nil {
		return err
	}
	ctx, targetID, err := h.workspaceDependencyTarget(c, botID, "")
	if err != nil {
		return err
	}
	if request.DefinitionRevision != "" {
		ctx = workspacedeps.WithDefinitionRevision(ctx, request.DefinitionRevision)
	}
	preview, err := svc.ScriptPreviewDetails(ctx, botID, targetID, depID, action)
	if err != nil {
		return workspaceDependencyError(err)
	}
	ctx = workspacedeps.WithDefinitionRevision(ctx, preview.Revision)
	if validator, ok := svc.(interface {
		ValidateOperationSession(context.Context, string, string) error
	}); ok {
		if err := validator.ValidateOperationSession(ctx, botID, request.SessionID); err != nil {
			return workspaceDependencyError(err)
		}
	} else if request.SessionID != "" {
		return apperror.New(apperror.CodeWorkspaceDependencyRequestInvalid, nil)
	}
	// A browser disconnect must not cancel a download already admitted by Manage
	// authorization and pinned to the prepared definition revision.
	ctx = context.WithoutCancel(ctx)
	version := request.Version
	writer, flusher, err := beginSSEResponse(c)
	if err != nil {
		return err
	}
	stream := newWorkspaceDependencyStream(writer, flusher, workspaceDependencyHeartbeatInterval)
	defer stream.close()

	stream.send(workspaceDependencyStartedEvent{Type: "started", DependencyID: depID, Version: version, DefinitionRevision: preview.Revision})
	sink := workspacedeps.LogFunc(func(name, line string) {
		stream.send(workspaceDependencyLogEvent{Type: "log", Stream: name, Data: line})
	})
	var result workspacedeps.OperationResult
	if authorized, ok := svc.(interface {
		RunAuthorizedOperation(context.Context, string, string, string, string, string, func(context.Context, workspacedeps.LogSink) (workspacedeps.OperationResult, error), workspacedeps.LogSink) (workspacedeps.OperationResult, error)
	}); ok {
		result, err = authorized.RunAuthorizedOperation(ctx, botID, targetID, depID, request.SessionID, string(action)+" "+depID, func(opCtx context.Context, opSink workspacedeps.LogSink) (workspacedeps.OperationResult, error) {
			return run(svc, opCtx, botID, targetID, depID, version, opSink)
		}, sink)
	} else {
		result, err = run(svc, ctx, botID, targetID, depID, version, sink)
	}
	if err != nil {
		requestID := httpx.RequestID(c)
		attrs := []any{
			slog.String("bot_id", botID),
			slog.String("workspace_target_id", targetID),
			slog.String("dependency_id", depID),
			slog.String("action", string(action)),
			slog.String("request_id", requestID),
			slog.Any("error", err),
		}
		switch {
		case errors.Is(err, workspacedeps.ErrBusy):
			// Operations never queue: a second request for the
			// same dependency is refused by design, not failed.
			h.logger.Info("workspace dependency operation refused: another operation is in progress", attrs...)
		default:
			h.logger.Warn("workspace dependency operation failed", attrs...)
		}
		stream.send(newWorkspaceDependencyErrorEvent(err, requestID))
		return nil
	}
	stream.send(workspaceDependencyDoneEvent{Type: "done", Version: result.Version, Entrypoints: result.Entrypoints, DefinitionRevision: result.DefinitionRevision})
	return nil
}

// workspaceDependencyStream owns a bounded observation queue. A slow client
// can lose intermediate log lines, but can never apply backpressure to the
// bridge's stdout pipe or prevent the script from finishing.
type workspaceDependencyStream struct {
	writer  io.Writer
	flusher http.Flusher
	events  chan any
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
}

const (
	workspaceDependencyLogBuffer    = 256
	workspaceDependencyWriteTimeout = 5 * time.Second
)

func newWorkspaceDependencyStream(writer io.Writer, flusher http.Flusher, heartbeat time.Duration) *workspaceDependencyStream {
	ticks := time.NewTicker(heartbeat)
	return startWorkspaceDependencyStream(writer, flusher, ticks.C, ticks.Stop)
}

func startWorkspaceDependencyStream(writer io.Writer, flusher http.Flusher, ticks <-chan time.Time, stopTicks func()) *workspaceDependencyStream {
	s := &workspaceDependencyStream{writer: writer, flusher: flusher, events: make(chan any, workspaceDependencyLogBuffer), stop: make(chan struct{}), done: make(chan struct{})}
	go s.writeLoop(ticks, stopTicks)
	return s
}

func (s *workspaceDependencyStream) send(payload any) {
	select {
	case <-s.done:
		return
	default:
	}
	select {
	case s.events <- payload:
	default:
		// Keep the latest progress and terminal event by evicting an old log.
		select {
		case <-s.events:
		default:
		}
		select {
		case s.events <- payload:
		default:
		}
	}
}

func (s *workspaceDependencyStream) writeLoop(ticks <-chan time.Time, stopTicks func()) {
	defer close(s.done)
	defer stopTicks()
	if writer, ok := s.writer.(http.ResponseWriter); ok {
		defer func() { _ = http.NewResponseController(writer).SetWriteDeadline(time.Time{}) }()
	}
	write := func(payload any) error {
		if writer, ok := s.writer.(http.ResponseWriter); ok {
			_ = http.NewResponseController(writer).SetWriteDeadline(time.Now().Add(workspaceDependencyWriteTimeout))
		}
		if payload == nil {
			return writeSSEComment(s.writer, s.flusher, "ping")
		}
		return writeSSEJSON(s.writer, s.flusher, payload)
	}
	for {
		select {
		case payload := <-s.events:
			if write(payload) != nil {
				return
			}
		case <-ticks:
			if write(nil) != nil {
				return
			}
		case <-s.stop:
			for {
				select {
				case payload := <-s.events:
					if write(payload) != nil {
						return
					}
				default:
					return
				}
			}
		}
	}
}

func (s *workspaceDependencyStream) close() {
	s.once.Do(func() { close(s.stop) })
	<-s.done
}

// writeSSEComment writes a comment frame (": text"). Comments are part of the
// SSE grammar and carry no event.
func writeSSEComment(writer io.Writer, flusher http.Flusher, text string) error {
	safe := strings.NewReplacer("\r", "", "\n", "").Replace(text)
	if _, err := io.WriteString(writer, ": "+safe+"\n\n"); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

// workspaceDependencyRequest authorizes the manage permission on the bot and
// resolves the service.
func (h *ContainerdHandler) workspaceDependencyRequest(c echo.Context) (string, workspaceDependencyService, error) {
	botID, err := h.requireBotAccessWithPermission(c, bots.PermissionManage)
	if err != nil {
		return "", nil, err
	}
	if h.workspaceDeps == nil {
		return "", nil, echo.NewHTTPError(http.StatusServiceUnavailable, "workspace dependency service not configured")
	}
	return botID, h.workspaceDeps, nil
}

// workspaceDependencyTarget pins the workspace target for the request: the
// explicit override, then the workspace_target_id query parameter, then the
// bot's current target.
func (h *ContainerdHandler) workspaceDependencyTarget(c echo.Context, botID, override string) (context.Context, string, error) {
	ctx := c.Request().Context()
	targetID := strings.TrimSpace(override)
	if targetID == "" {
		targetID = strings.TrimSpace(c.QueryParam("workspace_target_id"))
	}
	if targetID != "" {
		ctx = bridge.WithWorkspaceTarget(ctx, targetID)
	}
	ctx, targetID, err := h.pinCurrentWorkspaceTarget(ctx, botID)
	if err != nil {
		return nil, "", workspaceTargetHTTPError(h.logger, err)
	}
	return ctx, targetID, nil
}

// workspaceDependencyRequestedVersion reads the optional install request
// body. Remove takes none; a request without a body means latest.
func workspaceDependencyOperationRequest(c echo.Context, action catalog.Action) (WorkspaceDependencyInstallRequest, error) {
	var req WorkspaceDependencyInstallRequest
	if c.Request().ContentLength != 0 {
		if err := c.Bind(&req); err != nil {
			return req, apperror.Wrap(apperror.CodeWorkspaceDependencyRequestInvalid, err, nil)
		}
	}
	req.Version = strings.TrimSpace(req.Version)
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.DefinitionRevision = strings.TrimSpace(req.DefinitionRevision)
	// A normal confirmation need not open the script viewer. The handler
	// prepares and pins the current definition before it admits the operation.
	if req.DefinitionRevision != "" && !catalog.ValidRevision(req.DefinitionRevision) {
		return req, apperror.New(apperror.CodeWorkspaceDependencyRequestInvalid, nil)
	}
	if action == catalog.ActionRemove {
		req.Version = ""
	}
	if !workspacedeps.ValidRequestedVersion(req.Version) {
		return req, apperror.New(apperror.CodeWorkspaceDependencyRequestInvalid, nil)
	}
	return req, nil
}

// workspaceDependencyParam resolves the dep_id path parameter against the
// catalog.
func workspaceDependencyParam(c echo.Context) (string, error) {
	id := strings.TrimSpace(c.Param("dep_id"))
	if len(id) > 80 || !workspaceDependencyIDPattern.MatchString(id) {
		return "", apperror.New(apperror.CodeWorkspaceDependencyRequestInvalid, nil)
	}
	return id, nil
}

// workspaceDependencyScriptAction parses the script endpoint's action query;
// install is the default.
func workspaceDependencyScriptAction(raw string) (catalog.Action, error) {
	switch action := catalog.Action(strings.TrimSpace(raw)); action {
	case "", catalog.ActionInstall:
		return catalog.ActionInstall, nil
	case catalog.ActionUpdate, catalog.ActionRemove, catalog.ActionReinstall, workspacedeps.ActionRollback:
		return action, nil
	default:
		return "", apperror.Wrap(apperror.CodeWorkspaceDependencyRequestInvalid, errors.New("unsupported script action "+string(action)), nil)
	}
}

// workspaceDependencyError maps service sentinels to stable public codes.
// Anything unrecognized is an operation failure whose cause is logged at the
// transport boundary, never sent to the client.
func workspaceDependencyError(err error) error {
	switch {
	case err == nil:
		return nil
	case apperror.CodeOf(err) != "":
		return err
	case errors.Is(err, workspacedeps.ErrInvalidVersion):
		return apperror.Wrap(apperror.CodeWorkspaceDependencyRequestInvalid, err, nil)
	case errors.Is(err, workspacedeps.ErrCatalogUnavailable):
		return apperror.Wrap(apperror.CodeWorkspaceDependencyCatalogUnavailable, err, nil)
	case errors.Is(err, workspacedeps.ErrDefinitionInvalid):
		return apperror.Wrap(apperror.CodeWorkspaceDependencyDefinitionInvalid, err, nil)
	case errors.Is(err, workspacedeps.ErrDefinitionUnavailable):
		return apperror.Wrap(apperror.CodeWorkspaceDependencyDefinitionUnavailable, err, nil)
	case errors.Is(err, workspacedeps.ErrDependencyNotFound):
		return apperror.Wrap(apperror.CodeWorkspaceDependencyNotFound, err, nil)
	case errors.Is(err, workspacedeps.ErrActionUnsupported):
		return apperror.Wrap(apperror.CodeWorkspaceDependencyActionUnsupported, err, nil)
	case errors.Is(err, workspacedeps.ErrPlatformUnsupported):
		return apperror.Wrap(apperror.CodeWorkspaceDependencyPlatformUnsupported, err, nil)
	case errors.Is(err, workspacedeps.ErrBusy):
		return apperror.Wrap(apperror.CodeWorkspaceDependencyBusy, err, nil)
	case errors.Is(err, workspacedeps.ErrWorkspaceNotRunning):
		return apperror.Wrap(apperror.CodeWorkspaceDependencyWorkspaceNotRunning, err, nil)
	case errors.Is(err, workspacedeps.ErrWorkspaceMissing):
		return apperror.Wrap(apperror.CodeWorkspaceDependencyWorkspaceMissing, err, nil)
	case errors.Is(err, workspacedeps.ErrRemoteOffline):
		return apperror.Wrap(apperror.CodeWorkspaceDependencyRemoteOffline, err, nil)
	case errors.Is(err, workspacedeps.ErrRollbackUnavailable):
		return apperror.Wrap(apperror.CodeWorkspaceDependencyRollbackUnavailable, err, nil)
	case errors.Is(err, bridge.ErrUnavailable):
		return apperror.Wrap(apperror.CodeWorkspaceUnreachable, err, nil)
	default:
		return apperror.Wrap(apperror.CodeWorkspaceDependencyOperationFailed, err, nil)
	}
}

// newWorkspaceDependencyErrorEvent projects only stable public Problem fields.
func newWorkspaceDependencyErrorEvent(err error, requestID string) workspaceDependencyErrorEvent {
	if errors.Is(err, workspacedeps.ErrOperationUncertain) {
		return workspaceDependencyErrorEvent{Type: "error", Code: "workspace_dependency_operation_unknown", Args: map[string]string{}, Detail: "The operation result is not yet confirmed. Refresh dependencies to check its status.", Message: "The operation result is not yet confirmed.", RequestID: requestID}
	}
	mapped := workspaceDependencyError(err)
	public, ok := apperror.PublicFrom(mapped, requestID)
	if !ok {
		return workspaceDependencyErrorEvent{
			Type:      "error",
			Code:      string(apperror.CodeWorkspaceDependencyOperationFailed),
			Args:      map[string]string{},
			Message:   "The dependency operation failed.",
			RequestID: requestID,
		}
	}
	message := public.Detail
	return workspaceDependencyErrorEvent{
		Type:      "error",
		Code:      string(public.Code),
		Args:      public.Args,
		Detail:    public.Detail,
		Message:   message,
		RequestID: public.RequestID,
	}
}

func workspaceDependencyListResponse(result workspacedeps.ListResult) WorkspaceDependencyListResponse {
	resp := WorkspaceDependencyListResponse{
		WorkspaceState: string(result.Workspace),
		Items:          make([]WorkspaceDependencyItem, 0, len(result.Entries)),
		DiscoveryError: "",
		CatalogStale:   result.CatalogStale,
	}
	if result.DiscoveryError != "" {
		resp.DiscoveryError = string(apperror.CodeWorkspaceDependencyDiscoveryFailed)
	}
	if !result.CatalogFetchedAt.IsZero() {
		resp.CatalogFetchedAt = &result.CatalogFetchedAt
	}
	if result.Platform.OS != "" {
		resp.Platform = &WorkspaceDependencyPlatform{
			OS:   result.Platform.OS,
			Arch: result.Platform.Arch,
			Libc: result.Platform.Libc,
		}
	}
	for _, entry := range result.Entries {
		resp.Items = append(resp.Items, workspaceDependencyItem(entry, result.DataRoot))
	}
	return resp
}

func dependencyIconURL(dep catalog.Dependency) string {
	if !catalog.ValidRevision(dep.IconDigest) {
		return ""
	}
	return "/workspace-dependencies/icons/" + dep.IconDigest
}

// A global bucket bounds unauthenticated database work without retaining an
// unbounded map of attacker-controlled IP addresses.
var dependencyIconLimiter = rate.NewLimiter(50, 100)

// GetWorkspaceDependencyIcon godoc
// @Summary Read a cached verified dependency icon
// @Tags containerd
// @Produce image/svg+xml
// @Param digest path string true "SHA-256 digest"
// @Success 200 {file} binary
// @Failure 400 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Router /workspace-dependencies/icons/{digest} [get].
func (h *ContainerdHandler) GetWorkspaceDependencyIcon(c echo.Context) error {
	if !dependencyIconLimiter.Allow() {
		c.Response().Header().Set("Retry-After", "1")
		return echo.NewHTTPError(http.StatusTooManyRequests, "dependency icon request limit reached")
	}
	digest := c.Param("digest")
	if !catalog.ValidRevision(digest) {
		return apperror.New(apperror.CodeWorkspaceDependencyRequestInvalid, nil)
	}
	if h.workspaceDeps == nil {
		return apperror.New(apperror.CodeWorkspaceDependencyCatalogUnavailable, nil)
	}
	content, err := h.workspaceDeps.Icon(c.Request().Context(), digest)
	if err != nil {
		return workspaceDependencyError(err)
	}
	etag := "\"" + digest + "\""
	copySkillIconHeaders(c.Response().Header(), http.Header{"Cache-Control": []string{"public, max-age=31536000, immutable"}, "Etag": []string{etag}, "X-Content-Sha256": []string{digest}})
	if c.Request().Header.Get("If-None-Match") == etag {
		return c.NoContent(http.StatusNotModified)
	}
	return c.Blob(http.StatusOK, "image/svg+xml", content)
}

func workspaceDependencyItem(entry workspacedeps.Entry, dataRoot string) WorkspaceDependencyItem {
	dep := entry.Dependency
	item := WorkspaceDependencyItem{
		ID:         dep.ID,
		RegistryID: dep.RegistryID, DefinitionRevision: dep.Revision, IconURL: dependencyIconURL(dep), Translations: dependencyTranslations(dep), Retired: dep.Retired,
		Name:              dep.Name,
		Description:       dep.Description,
		Category:          string(dep.Category),
		Source:            string(dep.Source),
		Icon:              dep.Icon,
		Provides:          append([]string{}, dep.Provides...),
		PlatformSupported: entry.PlatformSupported,
		Status:            string(entry.Status),
		InstalledVersion:  entry.InstalledVersion,
		ImageVersion:      entry.ImageVersion,
		Overlay:           entry.Overlay,
		LatestVersion:     entry.LatestVersion,
		UpdateAvailable:   entry.UpdateAvailable,
		Actions:           make([]string, 0, len(entry.Actions)),
	}
	if !entry.PlatformSupported {
		item.PlatformReason = platformReasonUnsupported
	}
	if rec := entry.Installation; rec != nil {
		item.LastCheckedAt = rec.LastCheckedAt
		if rec.LastError != "" {
			item.LastErrorCode = string(apperror.CodeWorkspaceDependencyOperationFailed)
			item.LastError = workspacedeps.SafeErrorDetail(rec.LastError)
		}
	}
	if state := entry.Observed.State; state != nil {
		item.PreviousVersion = strings.TrimSpace(state.PreviousVersion)
	}
	switch {
	case entry.Observed.Present && entry.Observed.Source != workspacedeps.SourceManaged:
		item.InstallPath = entry.Observed.Command
	case dataRoot != "" && workspacedeps.ActionSupported(dep, catalog.ActionInstall):
		item.InstallPath = workspacedeps.Home(dataRoot, dep.ID)
	}
	for _, action := range entry.Actions {
		item.Actions = append(item.Actions, string(action))
	}
	return item
}

// preflightState folds Satisfied and Reason into the single state the UI
// switches on.
func preflightState(item workspacedeps.PreflightItem) string {
	if item.Satisfied {
		return "satisfied"
	}
	if item.Reason == "" {
		return workspacedeps.PreflightReasonMissing
	}
	return item.Reason
}
