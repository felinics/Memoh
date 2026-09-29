package handlers

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/apps"
	"github.com/felinics/memoh/internal/workspacedeps"
)

type WorkspaceDependencyPlanRequest struct {
	Roots []workspacedeps.PlanRoot `json:"roots"`
}

// PrepareWorkspaceDependencyPlan godoc
// @Summary Prepare an immutable workspace dependency plan
// @Tags containerd
// @Accept json
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param payload body WorkspaceDependencyPlanRequest true "Requested operations"
// @Success 200 {object} workspacedeps.Plan
// @Failure 400 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/dependencies/plan [post].
func (h *ContainerdHandler) PrepareWorkspaceDependencyPlan(c echo.Context) error {
	bot, svc, err := h.workspaceDependencyRequest(c)
	if err != nil {
		return err
	}
	var req WorkspaceDependencyPlanRequest
	if err := bindWorkspaceManagementRequest(c, &req); err != nil || len(req.Roots) == 0 {
		return apperror.New(apperror.CodeWorkspaceDependencyRequestInvalid, nil)
	}
	planner, ok := svc.(interface {
		PreparePlan(context.Context, string, []workspacedeps.PlanRoot) (workspacedeps.Plan, error)
	})
	if !ok {
		return workspaceDependencyError(workspacedeps.ErrPlanChanged)
	}
	plan, err := planner.PreparePlan(c.Request().Context(), bot, req.Roots)
	if err != nil {
		return workspaceDependencyError(err)
	}
	return c.JSON(http.StatusOK, plan)
}

// Prepare godoc
// @Summary Preview the complete dependency plan for an App operation
// @Tags apps
// @Accept json
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param payload body apps.PrepareRequest true "App operation"
// @Success 200 {object} apps.PreparedOperation
// @Failure 400 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/apps/prepare [post].
func (h *AppsHandler) Prepare(c echo.Context) error {
	bot, err := h.authorize(c)
	if err != nil {
		return err
	}
	var req apps.PrepareRequest
	if err := bindWorkspaceManagementRequest(c, &req); err != nil {
		return apperror.New(apperror.CodeAppRequestInvalid, nil)
	}
	planner, ok := h.service.(interface {
		Prepare(context.Context, string, apps.PrepareRequest) (apps.PreparedOperation, error)
	})
	if !ok {
		return workspaceDependencyError(workspacedeps.ErrPlanChanged)
	}
	plan, err := planner.Prepare(c.Request().Context(), bot, req)
	if err != nil {
		return h.httpError(err)
	}
	return c.JSON(http.StatusOK, plan)
}
