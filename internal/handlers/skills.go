package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	skillset "github.com/felinics/memoh/internal/skills"
	"github.com/felinics/memoh/internal/workspace"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

type SkillItem struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Content     string         `json:"content"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	Raw         string         `json:"raw"`
	SourcePath  string         `json:"source_path,omitempty"`
	SourceRoot  string         `json:"source_root,omitempty"`
	SourceKind  string         `json:"source_kind,omitempty"`
	Managed     bool           `json:"managed,omitempty"`
	Editable    bool           `json:"editable"`
	Deletable   bool           `json:"deletable"`
	State       string         `json:"state,omitempty"`
	ShadowedBy  string         `json:"shadowed_by,omitempty"`
	RegistryID  string         `json:"registry_id,omitempty"`
	PackageID   string         `json:"package_id,omitempty"`
	SkillID     string         `json:"skill_id,omitempty"`
}

type SkillsResponse struct {
	Skills []SkillItem `json:"skills"`
}

type SafeSkillsResponse struct {
	Skills []skillset.SafeCatalogItem `json:"skills"`
}

type SkillsUpsertRequest struct {
	Skills []string `json:"skills"`
	// SourcePath is the existing SKILL.md being edited when saving a single skill.
	// Empty means create (or overwrite by frontmatter name under
	// /data/skills/user/personal/<name>/).
	SourcePath string `json:"source_path,omitempty"`
}

type SkillsDeleteRequest struct {
	// SourcePaths are SKILL.md paths reported in the skill list. Deleting by name
	// cannot address registry skills, which are nested by registry and package.
	SourcePaths []string `json:"source_paths"`
}

type SkillsActionRequest struct {
	Action     string `json:"action"`
	TargetPath string `json:"target_path"`
}

type skillsOpResponse struct {
	OK bool `json:"ok"`
}

// ListSkills godoc
// @Summary List skills from the bot workspace
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param workspace_target_id query string false "Workspace target ID"
// @Success 200 {object} SkillsResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/container/skills [get].
func (h *ContainerdHandler) ListSkills(c echo.Context) error {
	botID, err := h.requireBotAccessWithPermission(c, bots.PermissionManage)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()
	if targetID := strings.TrimSpace(c.QueryParam("workspace_target_id")); targetID != "" {
		ctx = bridge.WithWorkspaceTarget(ctx, targetID)
	}
	skills, err := h.listSkillsFromContainer(ctx, botID)
	if err != nil {
		return workspaceTargetHTTPError(h.logger, err)
	}
	return c.JSON(http.StatusOK, SkillsResponse{Skills: skills})
}

// ListSafeSkills godoc
// @Summary List runtime-safe skills for chat-time skill selection
// @Tags skills
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} SafeSkillsResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/skills/catalog [get].
func (h *ContainerdHandler) ListSafeSkills(c echo.Context) error {
	botID, err := h.requireBotAccessWithPermission(c, bots.PermissionChat)
	if err != nil {
		return err
	}
	catalog, err := h.buildSafeSkillCatalog(c.Request().Context(), botID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, SafeSkillsResponse{Skills: catalog})
}

// UpsertSkills godoc
// @Summary Upload skills into Memoh-managed directory
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param payload body SkillsUpsertRequest true "Skills payload"
// @Success 200 {object} skillsOpResponse
// @Failure 400 {object} ErrorResponse
// @Failure 409 {object} apperror.Problem
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/container/skills [post].
func (h *ContainerdHandler) UpsertSkills(c echo.Context) error {
	botID, err := h.requireBotAccessWithPermission(c, bots.PermissionWorkspaceWrite)
	if err != nil {
		return err
	}

	var req SkillsUpsertRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if len(req.Skills) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "skills is required")
	}
	sourcePath := strings.TrimSpace(req.SourcePath)
	if sourcePath != "" && len(req.Skills) != 1 {
		return echo.NewHTTPError(http.StatusBadRequest, "source_path requires exactly one skill")
	}

	if err := h.upsertSkills(c.Request().Context(), botID, sourcePath, req.Skills); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, skillsOpResponse{OK: true})
}

func (h *ContainerdHandler) upsertSkills(
	ctx context.Context,
	botID, sourcePath string,
	rawSkills []string,
) error {
	client, err := h.getGRPCClient(ctx, botID)
	if err != nil {
		return workspaceUnavailableError(err)
	}
	if _, _, _, ok := skillset.RegistrySkillIDs(sourcePath); ok {
		return echo.NewHTTPError(http.StatusBadRequest, "Registry Package Skills cannot be edited directly")
	}

	for i, raw := range rawSkills {
		editPath := ""
		if i == 0 {
			editPath = sourcePath
		}
		plan, planErr := skillset.PlanUpsert(raw, editPath)
		if planErr != nil {
			if errors.Is(planErr, skillset.ErrBuiltinSkillReadOnly) {
				return apperror.New(apperror.CodeSkillBuiltinReadOnly, nil)
			}
			if errors.Is(planErr, skillset.ErrRegistrySkillReadOnly) {
				return echo.NewHTTPError(http.StatusBadRequest, "Registry Package Skills cannot be edited directly")
			}
			return echo.NewHTTPError(http.StatusBadRequest, "skill must have a valid name in YAML frontmatter")
		}
		dirPath := path.Dir(plan.WritePath)
		if plan.RenameFromDir != "" {
			if _, statErr := client.Stat(ctx, dirPath); statErr == nil {
				return apperror.New(apperror.CodeSkillNameTaken, nil)
			} else if !errors.Is(statErr, bridge.ErrNotFound) {
				return skillSaveHTTPError(fmt.Errorf("inspect renamed skill destination: %w", statErr))
			}
			if err := client.Rename(ctx, plan.RenameFromDir, dirPath); err != nil {
				return skillSaveHTTPError(fmt.Errorf("rename skill dir: %w", err))
			}
			if err := client.WriteFile(ctx, plan.WritePath, []byte(raw)); err != nil {
				rollbackErr := client.Rename(context.WithoutCancel(ctx), dirPath, plan.RenameFromDir)
				if rollbackErr != nil {
					return apperror.Wrap(
						apperror.CodeSkillSaveFailed,
						fmt.Errorf("write renamed skill file: %w; restore original skill directory: %w", err, rollbackErr),
						nil,
					)
				}
				return skillSaveHTTPError(fmt.Errorf("write renamed skill file: %w", err))
			}
			continue
		}
		// A pooled client can pass getGRPCClient and still fail on first use
		// when the workspace just stopped; classify per-op errors so the dial
		// diagnostics never reach the response body.
		if err := client.Mkdir(ctx, dirPath); err != nil {
			return skillSaveHTTPError(fmt.Errorf("mkdir skill dir: %w", err))
		}
		if err := client.WriteFile(ctx, plan.WritePath, []byte(raw)); err != nil {
			return skillSaveHTTPError(fmt.Errorf("write skill file: %w", err))
		}
	}

	return nil
}

func skillSaveHTTPError(err error) error {
	switch {
	case errors.Is(err, bridge.ErrNotFound),
		errors.Is(err, bridge.ErrBadRequest),
		errors.Is(err, bridge.ErrForbidden):
		return fsHTTPError(err)
	case errors.Is(err, bridge.ErrUnavailable):
		return workspaceUnavailableError(err)
	default:
		return apperror.Wrap(apperror.CodeSkillSaveFailed, err, nil)
	}
}

// DeleteSkills godoc
// @Summary Delete Memoh-managed skills
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param payload body SkillsDeleteRequest true "Delete skills payload"
// @Success 200 {object} skillsOpResponse
// @Failure 400 {object} ErrorResponse
// @Failure 409 {object} apperror.Problem
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/container/skills [delete].
func (h *ContainerdHandler) DeleteSkills(c echo.Context) error {
	botID, err := h.requireBotAccessWithPermission(c, bots.PermissionWorkspaceWrite)
	if err != nil {
		return err
	}

	var req SkillsDeleteRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if len(req.SourcePaths) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "source_paths is required")
	}

	if err := h.deleteSkills(c.Request().Context(), botID, req.SourcePaths); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, skillsOpResponse{OK: true})
}

func (h *ContainerdHandler) deleteSkills(ctx context.Context, botID string, sourcePaths []string) error {
	ctx, _, err := h.pinCurrentWorkspaceTarget(ctx, botID)
	if err != nil {
		return workspaceUnavailableError(err)
	}
	client, err := h.getGRPCClient(ctx, botID)
	if err != nil {
		return workspaceUnavailableError(err)
	}

	type deleteTarget struct {
		sourcePath string
		skillDir   string
	}
	targets := make([]deleteTarget, 0, len(sourcePaths))
	for _, sourcePath := range sourcePaths {
		skillDir, dirErr := skillset.DeletableSkillDirForSourcePath(sourcePath)
		if dirErr != nil {
			if errors.Is(dirErr, skillset.ErrBuiltinSkillReadOnly) {
				return apperror.New(apperror.CodeSkillBuiltinReadOnly, nil)
			}
			if errors.Is(dirErr, skillset.ErrRegistrySkillReadOnly) {
				return echo.NewHTTPError(http.StatusBadRequest, "Registry Package Skills cannot be deleted directly")
			}
			return echo.NewHTTPError(http.StatusBadRequest, "only Memoh-managed skills can be deleted")
		}
		target := deleteTarget{sourcePath: path.Clean(strings.TrimSpace(sourcePath)), skillDir: skillDir}
		targets = append(targets, target)
	}

	for _, target := range targets {
		if _, statErr := client.Stat(ctx, target.skillDir); statErr != nil {
			return fsHTTPError(statErr)
		}
		if err := client.DeleteFile(ctx, target.skillDir, true); err != nil {
			return fsHTTPError(err)
		}
		pruneEmptySkillNamespaceDirs(ctx, client, target.skillDir)
	}

	return nil
}

type currentWorkspaceTargetResolver interface {
	CurrentWorkspaceTargetID(ctx context.Context, botID string) (string, error)
}

func (h *ContainerdHandler) currentWorkspaceTargetID(ctx context.Context, botID string) (string, error) {
	if targetID := bridge.WorkspaceTargetFromContext(ctx); targetID != "" {
		return targetID, nil
	}
	resolver, ok := h.manager.(currentWorkspaceTargetResolver)
	if !ok {
		return workspace.WorkspaceTargetNative, nil
	}
	return resolver.CurrentWorkspaceTargetID(ctx, botID)
}

func (h *ContainerdHandler) pinCurrentWorkspaceTarget(
	ctx context.Context,
	botID string,
) (context.Context, string, error) {
	targetID, err := h.currentWorkspaceTargetID(ctx, botID)
	if err != nil {
		return ctx, "", err
	}
	return bridge.WithWorkspaceTarget(ctx, targetID), targetID, nil
}

// pruneEmptySkillNamespaceDirs drops the package and namespace directories left
// behind by the last deleted Skill. Best effort: a concurrent install may refill
// them, and a stale empty directory is harmless to discovery.
func pruneEmptySkillNamespaceDirs(ctx context.Context, client *bridge.Client, skillDir string) {
	for _, dir := range skillset.PrunableSkillNamespaceDirs(skillDir) {
		entries, err := client.ListDirAll(ctx, dir, false)
		if err != nil || len(entries) > 0 {
			return
		}
		if err := client.DeleteFile(ctx, dir, false); err != nil {
			return
		}
	}
}

// ApplySkillAction godoc
// @Summary Apply an action to a discovered or managed skill source
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param payload body SkillsActionRequest true "Skill action payload"
// @Success 200 {object} skillsOpResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} apperror.Problem
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/container/skills/actions [post].
func (h *ContainerdHandler) ApplySkillAction(c echo.Context) error {
	botID, err := h.requireBotAccessWithPermission(c, bots.PermissionWorkspaceWrite)
	if err != nil {
		return err
	}

	var req SkillsActionRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	if err := h.applySkillAction(c.Request().Context(), botID, req); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, skillsOpResponse{OK: true})
}

func (h *ContainerdHandler) applySkillAction(ctx context.Context, botID string, req SkillsActionRequest) error {
	if _, _, _, ok := skillset.RegistrySkillIDs(req.TargetPath); ok {
		return echo.NewHTTPError(http.StatusBadRequest, "Registry Package Skills are read-only")
	}
	ctx, _, err := h.pinCurrentWorkspaceTarget(ctx, botID)
	if err != nil {
		return workspaceUnavailableError(err)
	}
	client, err := h.getGRPCClient(ctx, botID)
	if err != nil {
		return workspaceUnavailableError(err)
	}
	roots, err := h.skillDiscoveryRoots(ctx, botID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	if err := skillset.ApplyAction(ctx, client, roots, skillset.ActionRequest{
		Action:     req.Action,
		TargetPath: req.TargetPath,
	}); err != nil {
		return fsHTTPError(err)
	}

	return nil
}

// LoadSkills loads the effective skills from the container for the given bot.
func (h *ContainerdHandler) LoadSkills(ctx context.Context, botID string) ([]SkillItem, error) {
	ctx, _, err := h.pinCurrentWorkspaceTarget(ctx, botID)
	if err != nil {
		return nil, err
	}
	client, err := h.getGRPCClient(ctx, botID)
	if err != nil {
		return nil, err
	}
	roots, err := h.skillDiscoveryRoots(ctx, botID)
	if err != nil {
		return nil, err
	}
	items, err := skillset.LoadEffective(ctx, client, roots)
	if err != nil {
		return nil, err
	}
	return skillItemsFromEntries(items), nil
}

func (h *ContainerdHandler) ListSafeSkillCatalog(ctx context.Context, botID string) ([]skillset.SafeCatalogItem, error) {
	return h.buildSafeSkillCatalog(ctx, botID)
}

func (h *ContainerdHandler) buildSafeSkillCatalog(ctx context.Context, botID string) ([]skillset.SafeCatalogItem, error) {
	entries, err := h.listSkillEntriesFromContainer(ctx, botID)
	if err != nil {
		return nil, err
	}
	return skillset.BuildSafeCatalog(entries)
}

func (h *ContainerdHandler) ResolveTextRequestedSkills(ctx context.Context, botID string, names []string) ([]skillset.ResolvedSkill, error) {
	entries, err := h.listSkillEntriesFromContainer(ctx, botID)
	if err != nil {
		return nil, err
	}
	return skillset.ResolveTextRequestedSkills(entries, names, skillset.ResolveLimits{})
}

func (h *ContainerdHandler) listSkillsFromContainer(ctx context.Context, botID string) ([]SkillItem, error) {
	items, err := h.listSkillEntriesFromContainer(ctx, botID)
	if err != nil {
		return nil, err
	}
	return skillItemsFromEntries(items), nil
}

func (h *ContainerdHandler) listSkillEntriesFromContainer(ctx context.Context, botID string) ([]skillset.Entry, error) {
	ctx, _, err := h.pinCurrentWorkspaceTarget(ctx, botID)
	if err != nil {
		return nil, err
	}
	client, err := h.getGRPCClient(ctx, botID)
	if err != nil {
		return nil, err
	}
	roots, err := h.skillDiscoveryRoots(ctx, botID)
	if err != nil {
		return nil, err
	}
	items, err := skillset.List(ctx, client, roots)
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (h *ContainerdHandler) skillDiscoveryRoots(
	ctx context.Context,
	botID string,
) ([]string, error) {
	var roots []string
	if h.botService != nil {
		bot, err := h.botService.Get(ctx, botID)
		if err == nil {
			return workspace.SkillDiscoveryRootsFromMetadata(bot.Metadata), nil
		}
	}
	if h.manager == nil {
		return nil, nil
	}
	var err error
	roots, err = h.manager.ResolveWorkspaceSkillDiscoveryRoots(ctx, botID)
	if err != nil {
		return nil, err
	}
	return roots, nil
}

func skillItemsFromEntries(entries []skillset.Entry) []SkillItem {
	items := make([]SkillItem, len(entries))
	for i, entry := range entries {
		_, builtin := skillset.BuiltinSkillName(entry.SourcePath)
		registryID, packageID, skillID, packageMember := skillset.RegistrySkillIDs(entry.SourcePath)
		registryOwned := entry.SourceKind == skillset.SourceKindRegistry || packageMember
		items[i] = SkillItem{
			Name:        entry.Name,
			Description: entry.Description,
			Content:     entry.Content,
			Metadata:    entry.Metadata,
			Raw:         entry.Raw,
			SourcePath:  entry.SourcePath,
			SourceRoot:  entry.SourceRoot,
			SourceKind:  entry.SourceKind,
			Managed:     entry.Managed,
			Editable:    !builtin && !registryOwned,
			Deletable:   entry.Managed && !builtin && !packageMember,
			State:       entry.State,
			ShadowedBy:  entry.ShadowedBy,
			RegistryID:  registryID,
			PackageID:   packageID,
			SkillID:     skillID,
		}
	}
	return items
}
