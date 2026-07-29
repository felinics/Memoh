package handlers

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/internal/bots"
	pluginspkg "github.com/memohai/memoh/internal/plugins"
	skillset "github.com/memohai/memoh/internal/skills"
	"github.com/memohai/memoh/internal/workspace"
	"github.com/memohai/memoh/internal/workspacecontext"
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
	State       string         `json:"state,omitempty"`
	ShadowedBy  string         `json:"shadowed_by,omitempty"`
}

type SkillsResponse struct {
	Skills []SkillItem `json:"skills"`
}

type SafeSkillsResponse struct {
	Skills []skillset.SafeCatalogItem `json:"skills"`
}

type SkillsUpsertRequest struct {
	Skills []string `json:"skills"`
}

type SkillsDeleteRequest struct {
	Names []string `json:"names"`
}

type SkillsActionRequest struct {
	Action     string `json:"action"`
	TargetPath string `json:"target_path"`
}

type skillsOpResponse struct {
	OK bool `json:"ok"`
}

type PluginInstallationLister interface {
	List(ctx context.Context, botID string) ([]pluginspkg.Installation, error)
}

func (h *ContainerdHandler) SetPluginService(service PluginInstallationLister) {
	h.pluginService = service
}

// ListSkills godoc
// @Summary List skills from the bot workspace
// @Tags containerd
// @Param bot_id path string true "Bot ID"
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

	skills, err := h.listSkillsFromContainer(c.Request().Context(), botID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
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
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
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

	ctx := c.Request().Context()
	client, err := h.getGRPCClient(ctx, botID)
	if err != nil {
		return workspaceUnavailableError(err)
	}

	for _, raw := range req.Skills {
		parsed := skillset.ParseFile(raw, "")
		dirPath, dirErr := skillset.ManagedSkillDirForName(parsed.Name)
		if dirErr != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "skill must have a valid name in YAML frontmatter")
		}
		// A pooled client can pass getGRPCClient and still fail on first use
		// when the workspace just stopped; classify per-op errors so the dial
		// diagnostics never reach the response body.
		if err := client.Mkdir(ctx, dirPath); err != nil {
			return fsHTTPError(fmt.Errorf("mkdir skill dir: %w", err))
		}
		filePath := path.Join(dirPath, "SKILL.md")
		if err := client.WriteFile(ctx, filePath, []byte(raw)); err != nil {
			return fsHTTPError(fmt.Errorf("write skill file: %w", err))
		}
	}
	if err := h.refreshWorkspaceContext(ctx, botID, workspacecontext.ReasonSkillsChanged); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.JSON(http.StatusOK, skillsOpResponse{OK: true})
}

// DeleteSkills godoc
// @Summary Delete Memoh-managed skills
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param payload body SkillsDeleteRequest true "Delete skills payload"
// @Success 200 {object} skillsOpResponse
// @Failure 400 {object} ErrorResponse
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
	if len(req.Names) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "names is required")
	}

	ctx := c.Request().Context()
	client, err := h.getGRPCClient(ctx, botID)
	if err != nil {
		return workspaceUnavailableError(err)
	}

	for _, name := range req.Names {
		skillName := strings.TrimSpace(name)
		managedDir, dirErr := skillset.ManagedSkillDirForName(skillName)
		if dirErr != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid skill name")
		}
		if _, statErr := client.Stat(ctx, managedDir); statErr != nil {
			return fsHTTPError(statErr)
		}
		if err := client.DeleteFile(ctx, managedDir, true); err != nil {
			return fsHTTPError(err)
		}
	}
	if err := h.refreshWorkspaceContext(ctx, botID, workspacecontext.ReasonSkillsChanged); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.JSON(http.StatusOK, skillsOpResponse{OK: true})
}

// ApplySkillAction godoc
// @Summary Apply an action to a discovered or managed skill source
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param payload body SkillsActionRequest true "Skill action payload"
// @Success 200 {object} skillsOpResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
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

	ctx := c.Request().Context()
	client, err := h.getGRPCClient(ctx, botID)
	if err != nil {
		return workspaceUnavailableError(err)
	}
	roots, pluginRoots, err := h.skillDiscoveryRoots(ctx, botID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	if err := skillset.ApplyActionWithPluginRoots(ctx, client, roots, pluginRoots, skillset.ActionRequest{
		Action:     req.Action,
		TargetPath: req.TargetPath,
	}); err != nil {
		return fsHTTPError(err)
	}
	if err := h.refreshWorkspaceContext(ctx, botID, workspacecontext.ReasonSkillsChanged); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.JSON(http.StatusOK, skillsOpResponse{OK: true})
}

// LoadSkills loads the effective skills from the container for the given bot.
func (h *ContainerdHandler) LoadSkills(ctx context.Context, botID string) ([]SkillItem, error) {
	if h.workspaceContext != nil {
		items, err := h.workspaceContext.Skills(ctx, botID, true)
		if err != nil {
			return nil, err
		}
		return skillItemsFromEntries(items), nil
	}
	client, err := h.getGRPCClient(ctx, botID)
	if err != nil {
		return nil, err
	}
	roots, pluginRoots, err := h.skillDiscoveryRoots(ctx, botID)
	if err != nil {
		return nil, err
	}
	items, err := skillset.LoadEffectiveWithPluginRoots(ctx, client, roots, pluginRoots)
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
	if h.workspaceContext != nil {
		return h.workspaceContext.Skills(ctx, botID, false)
	}
	client, err := h.getGRPCClient(ctx, botID)
	if err != nil {
		return nil, err
	}
	roots, pluginRoots, err := h.skillDiscoveryRoots(ctx, botID)
	if err != nil {
		return nil, err
	}
	items, err := skillset.ListWithPluginRoots(ctx, client, roots, pluginRoots)
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (h *ContainerdHandler) skillDiscoveryRoots(ctx context.Context, botID string) ([]string, []string, error) {
	var roots []string
	if h.botService != nil {
		bot, err := h.botService.Get(ctx, botID)
		if err == nil {
			roots = workspace.SkillDiscoveryRootsFromMetadata(bot.Metadata)
			pluginRoots, err := h.pluginSkillRoots(ctx, botID)
			return roots, pluginRoots, err
		}
	}
	if h.manager == nil {
		return nil, nil, nil
	}
	var err error
	roots, err = h.manager.ResolveWorkspaceSkillDiscoveryRoots(ctx, botID)
	if err != nil {
		return nil, nil, err
	}
	pluginRoots, err := h.pluginSkillRoots(ctx, botID)
	return roots, pluginRoots, err
}

func (h *ContainerdHandler) pluginSkillRoots(ctx context.Context, botID string) ([]string, error) {
	if h.pluginService == nil {
		return nil, nil
	}
	installations, err := h.pluginService.List(ctx, botID)
	if err != nil {
		return nil, err
	}
	roots := make([]string, 0, len(installations))
	seen := make(map[string]struct{}, len(installations))
	for _, installation := range installations {
		if !installation.Enabled || installation.Status == pluginspkg.StatusUninstalled {
			continue
		}
		root, err := skillset.PluginSkillsDirForID(installation.PluginID)
		if err != nil {
			continue
		}
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
	}
	return roots, nil
}

func skillItemsFromEntries(entries []skillset.Entry) []SkillItem {
	items := make([]SkillItem, len(entries))
	for i, entry := range entries {
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
			State:       entry.State,
			ShadowedBy:  entry.ShadowedBy,
		}
	}
	return items
}
