package handlers

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/workspacedeps"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

type WorkspaceDependencyPrepareRequest struct {
	Action             string `json:"action" enums:"install,update,reinstall"`
	Version            string `json:"version,omitempty"`
	DefinitionRevision string `json:"definition_revision"`
}

// WorkspaceDependencyPreparedResponse is the target shown before confirmation.
type WorkspaceDependencyPreparedResponse struct {
	DependencyID       string `json:"dependency_id"`
	Action             string `json:"action"`
	Version            string `json:"version"`
	DefinitionRevision string `json:"definition_revision"`
	SourceURL          string `json:"source_url"`
	RegistryID         string `json:"registry_id"`
	ManifestDigest     string `json:"manifest_digest"`
}

type WorkspaceDependencyRepairRequest struct {
	Version            string `json:"version,omitempty"`
	DefinitionRevision string `json:"definition_revision"`
}

// WorkspaceDependencyDesired exposes the confirmed target and recovery progress.
// Private execution paths and authorization actor details remain server-side.
type WorkspaceDependencyDesired struct {
	Version                string     `json:"version"`
	Revision               string     `json:"revision"`
	DefinitionRevision     string     `json:"definition_revision"`
	SourceURL              string     `json:"source_url"`
	RegistryID             string     `json:"registry_id"`
	ManifestDigest         string     `json:"manifest_digest"`
	AutoRepairAuthorizedAt time.Time  `json:"auto_repair_authorized_at"`
	RepairStatus           string     `json:"repair_status" enums:"ready,queued,installing,backoff,manual_required"`
	RepairOperationID      string     `json:"repair_operation_id,omitempty"`
	RepairAttempts         int        `json:"repair_attempts"`
	RepairNextAttemptAt    *time.Time `json:"repair_next_attempt_at,omitempty"`
	RepairLastErrorCode    string     `json:"repair_last_error_code,omitempty"`
}

type workspaceDependencyRepairService interface {
	PrepareRepairAuthorization(context.Context, string, string, string, string) (workspacedeps.RepairAuthorization, error)
	AuthorizeRepair(context.Context, string, string, string, string, string) (workspacedeps.DesiredInstallation, error)
	RetryRepair(context.Context, string, string) (workspacedeps.DesiredInstallation, error)
}

// PrepareWorkspaceDependency godoc
// @Summary Prepare an exact workspace dependency installation
// @Description Requires Manage. Freezes a recipe revision and exact version before confirmation. An unspecified version runs only the frozen recipe's upstream version check; it never installs a dependency.
// @Tags containerd
// @Accept json
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param dep_id path string true "Dependency ID"
// @Param payload body WorkspaceDependencyPrepareRequest true "Target to prepare"
// @Success 200 {object} WorkspaceDependencyPreparedResponse
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/dependencies/{dep_id}/prepare [post].
func (h *ContainerdHandler) PrepareWorkspaceDependency(c echo.Context) error {
	botID, svc, err := h.workspaceDependencyRequest(c)
	if err != nil {
		return err
	}
	depID, err := workspaceDependencyParam(c)
	if err != nil {
		return err
	}
	var req WorkspaceDependencyPrepareRequest
	if err := bindWorkspaceManagementRequest(c, &req); err != nil {
		return apperror.Wrap(apperror.CodeWorkspaceDependencyRequestInvalid, err, nil)
	}
	req.DefinitionRevision = strings.TrimSpace(req.DefinitionRevision)
	if !catalog.ValidRevision(req.DefinitionRevision) {
		return apperror.New(apperror.CodeWorkspaceDependencyRequestInvalid, nil)
	}
	preparer, ok := svc.(interface {
		PrepareInstall(context.Context, string, string, catalog.Action, string) (workspacedeps.PreparedInstall, error)
	})
	if !ok {
		return apperror.New(apperror.CodeWorkspaceDependencyActionUnsupported, nil)
	}
	prepared, err := preparer.PrepareInstall(workspacedeps.WithDefinitionRevision(c.Request().Context(), req.DefinitionRevision), botID, depID, catalog.Action(req.Action), req.Version)
	if err != nil {
		return workspaceDependencyError(err)
	}
	return c.JSON(http.StatusOK, WorkspaceDependencyPreparedResponse{
		DependencyID: prepared.DependencyID, Action: string(prepared.Action), Version: prepared.Version,
		DefinitionRevision: prepared.DefinitionRevision, SourceURL: prepared.SourceURL,
		RegistryID: prepared.RegistryID, ManifestDigest: prepared.ManifestDigest,
	})
}

// PrepareWorkspaceDependencyRepair godoc
// @Summary Prepare authorization to restore a workspace dependency
// @Description Requires Manage. Returns the exact version and frozen recipe to confirm before reinstalling and enabling automatic recovery.
// @Tags containerd
// @Accept json
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param dep_id path string true "Dependency ID"
// @Param payload body WorkspaceDependencyRepairRequest true "Target to prepare"
// @Success 200 {object} WorkspaceDependencyPreparedResponse
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/dependencies/{dep_id}/repair/prepare [post].
func (h *ContainerdHandler) PrepareWorkspaceDependencyRepair(c echo.Context) error {
	botID, depID, svc, req, err := h.workspaceDependencyRepairRequest(c)
	if err != nil {
		return err
	}
	prepared, err := svc.PrepareRepairAuthorization(c.Request().Context(), botID, depID, req.Version, req.DefinitionRevision)
	if err != nil {
		return workspaceDependencyError(err)
	}
	return c.JSON(http.StatusOK, WorkspaceDependencyPreparedResponse{
		DependencyID: depID, Action: string(catalog.ActionReinstall), Version: prepared.Version,
		DefinitionRevision: prepared.DefinitionRevision, SourceURL: prepared.SourceURL,
		RegistryID: prepared.RegistryID, ManifestDigest: prepared.ManifestDigest,
	})
}

// AuthorizeWorkspaceDependencyRepair godoc
// @Summary Reinstall a confirmed dependency and authorize recovery
// @Description Requires Manage and an exact version with a frozen recipe revision. Successful installation authorizes restoring that same target after payload loss. Disconnecting observation does not cancel the admitted installation.
// @Tags containerd
// @Accept json
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param dep_id path string true "Dependency ID"
// @Param payload body WorkspaceDependencyRepairRequest true "Confirmed target"
// @Success 200 {object} WorkspaceDependencyDesired
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/dependencies/{dep_id}/repair/authorize [post].
func (h *ContainerdHandler) AuthorizeWorkspaceDependencyRepair(c echo.Context) error {
	botID, depID, svc, req, err := h.workspaceDependencyRepairRequest(c)
	if err != nil {
		return err
	}
	if !workspacedeps.ExactVersion(req.Version) {
		return apperror.New(apperror.CodeWorkspaceDependencyRequestInvalid, nil)
	}
	actor, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	desired, err := svc.AuthorizeRepair(context.WithoutCancel(c.Request().Context()), botID, depID, req.Version, req.DefinitionRevision, actor)
	if err != nil {
		return workspaceDependencyError(err)
	}
	return c.JSON(http.StatusOK, workspaceDependencyDesired(desired))
}

// RetryWorkspaceDependencyRepair godoc
// @Summary Retry recovery of the current authorized dependency target
// @Description Requires Manage. Requests recovery of the existing frozen target without changing its version or authorization.
// @Tags containerd
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param dep_id path string true "Dependency ID"
// @Success 200 {object} WorkspaceDependencyDesired
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/dependencies/{dep_id}/repair/retry [post].
func (h *ContainerdHandler) RetryWorkspaceDependencyRepair(c echo.Context) error {
	botID, base, err := h.workspaceDependencyRequest(c)
	if err != nil {
		return err
	}
	depID, err := workspaceDependencyParam(c)
	if err != nil {
		return err
	}
	svc, ok := base.(workspaceDependencyRepairService)
	if !ok {
		return apperror.New(apperror.CodeWorkspaceDependencyActionUnsupported, nil)
	}
	desired, err := svc.RetryRepair(c.Request().Context(), botID, depID)
	if err != nil {
		return workspaceDependencyError(err)
	}
	return c.JSON(http.StatusOK, workspaceDependencyDesired(desired))
}

func (h *ContainerdHandler) workspaceDependencyRepairRequest(c echo.Context) (string, string, workspaceDependencyRepairService, WorkspaceDependencyRepairRequest, error) {
	var req WorkspaceDependencyRepairRequest
	botID, base, err := h.workspaceDependencyRequest(c)
	if err != nil {
		return "", "", nil, req, err
	}
	depID, err := workspaceDependencyParam(c)
	if err != nil {
		return "", "", nil, req, err
	}
	if err := bindWorkspaceManagementRequest(c, &req); err != nil {
		return "", "", nil, req, apperror.Wrap(apperror.CodeWorkspaceDependencyRequestInvalid, err, nil)
	}
	req.Version = strings.TrimSpace(req.Version)
	req.DefinitionRevision = strings.TrimSpace(req.DefinitionRevision)
	if !catalog.ValidRevision(req.DefinitionRevision) || !workspacedeps.ValidRequestedVersion(req.Version) {
		return "", "", nil, req, apperror.New(apperror.CodeWorkspaceDependencyRequestInvalid, nil)
	}
	svc, ok := base.(workspaceDependencyRepairService)
	if !ok {
		return "", "", nil, req, apperror.New(apperror.CodeWorkspaceDependencyActionUnsupported, nil)
	}
	return botID, depID, svc, req, nil
}

func workspaceDependencyDesired(desired workspacedeps.DesiredInstallation) WorkspaceDependencyDesired {
	return WorkspaceDependencyDesired{
		Version: desired.Version, Revision: desired.Revision, DefinitionRevision: desired.DefinitionRevision,
		SourceURL: desired.SourceURL, RegistryID: desired.RegistryID, ManifestDigest: desired.ManifestDigest,
		AutoRepairAuthorizedAt: desired.AutoRepairAuthorizedAt, RepairStatus: string(desired.RepairStatus),
		RepairOperationID: desired.RepairOperationID, RepairAttempts: desired.RepairAttempts,
		RepairNextAttemptAt: desired.RepairNextAttemptAt, RepairLastErrorCode: desired.RepairLastErrorCode,
	}
}
