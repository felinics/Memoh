package bot

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	pluginspkg "github.com/memohai/memoh/domains/agent/extension/plugins"
	skillset "github.com/memohai/memoh/domains/agent/extension/skills"
	"github.com/memohai/memoh/domains/api/bot"
	httpx "github.com/memohai/memoh/domains/api/http"
	"github.com/memohai/memoh/domains/iam/account"
	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
	"github.com/memohai/memoh/internal/apperror"
	"github.com/memohai/memoh/internal/config"
)

type SupermarketHandler struct {
	baseURL        string
	httpClient     *http.Client
	pluginService  pluginInstaller
	containers     bridge.Provider
	botService     *bot.Service
	accountService *account.Service
	logger         *slog.Logger
}

type pluginInstaller interface {
	Install(ctx context.Context, botID string, req pluginspkg.InstallRequest) (pluginspkg.Installation, error)
}

type pluginBundleWriter interface {
	DeleteFile(ctx context.Context, path string, recursive bool) error
	Mkdir(ctx context.Context, path string) error
	WriteFile(ctx context.Context, path string, content []byte) error
}

type pluginInstallScriptExecutor interface {
	ExecWithEnv(ctx context.Context, command, workDir string, timeout int32, env []string) (*bridge.ExecResult, error)
}

type pluginAssetInstallResult struct {
	OK           bool   `json:"ok"`
	FilesWritten int    `json:"files_written"`
	Error        string `json:"error,omitempty"`
}

type pluginBundleInstallResult struct {
	Skills  pluginAssetInstallResult
	Hooks   pluginAssetInstallResult
	Scripts pluginAssetInstallResult
}

type pluginInstallScriptsResult struct {
	OK          bool                         `json:"ok"`
	CommandsRun int                          `json:"commands_run"`
	Results     []pluginInstallCommandResult `json:"results,omitempty"`
	Error       string                       `json:"error,omitempty"`
}

type pluginInstallCommandResult struct {
	Command  string `json:"command"`
	ExitCode int32  `json:"exit_code"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	Error    string `json:"error,omitempty"`
}

const (
	pluginInstallScriptTimeoutSeconds int32 = 10 * 60
	pluginInstallScriptOutputLimit          = 64 * 1024
)

func NewSupermarketHandler(
	log *slog.Logger,
	cfg config.Config,
	pluginService *pluginspkg.Service,
	containers bridge.Provider,
	botService *bot.Service,
	accountService *account.Service,
) *SupermarketHandler {
	return &SupermarketHandler{
		baseURL:        cfg.Supermarket.GetBaseURL(),
		httpClient:     &http.Client{Timeout: 30 * time.Second},
		pluginService:  pluginService,
		containers:     containers,
		botService:     botService,
		accountService: accountService,
		logger:         log.With(slog.String("handler", "supermarket")),
	}
}

func (h *SupermarketHandler) Register(e *echo.Echo) {
	g := e.Group("/supermarket")
	g.GET("/plugins", h.ListPlugins)
	g.GET("/plugins/:id", h.GetPlugin)
	g.GET("/skills", h.ListSkills)
	g.GET("/skills/:id", h.GetSkill)
	g.GET("/tags", h.ListTags)

	ig := e.Group("/bots/:bot_id/supermarket")
	ig.POST("/install-plugin", h.InstallPlugin)
	ig.POST("/install-skill", h.InstallSkill)
}

func (h *SupermarketHandler) requireBotAccess(c echo.Context) (string, error) {
	channelIdentityID, err := httpx.RequireChannelIdentityID(c)
	if err != nil {
		return "", err
	}
	botID := c.Param("bot_id")
	if _, err := httpx.AuthorizeBotAccess(c.Request().Context(), h.botService, h.accountService, channelIdentityID, botID); err != nil {
		return "", err
	}
	return botID, nil
}

// proxy forwards a GET request to the supermarket and streams the JSON response back.
func (h *SupermarketHandler) proxy(c echo.Context, upstreamPath string) error {
	url := h.baseURL + upstreamPath
	if qs := c.QueryString(); qs != "" {
		url += "?" + qs
	}

	req, err := http.NewRequestWithContext(c.Request().Context(), http.MethodGet, url, nil)
	if err != nil {
		return apperror.Internal("proxy supermarket", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := h.httpClient.Do(req) //nolint:gosec // URL constructed from trusted config
	if err != nil {
		h.logger.Error("supermarket proxy failed", slog.String("url", url), slog.Any("error", err))
		return apperror.Unavailable("proxy supermarket", err)
	}
	defer func() { _ = resp.Body.Close() }()

	c.Response().Header().Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	c.Response().WriteHeader(resp.StatusCode)
	_, _ = io.Copy(c.Response(), resp.Body)
	return nil
}

// ListPlugins godoc
// @Summary List plugins from supermarket
// @Tags supermarket
// @Param q query string false "Search query"
// @Param tag query string false "Filter by tag"
// @Param page query int false "Page number"
// @Param limit query int false "Items per page"
// @Success 200 {object} SupermarketPluginListResponse
// @Failure 502 {object} apperror.Problem
// @Router /supermarket/plugins [get].
func (h *SupermarketHandler) ListPlugins(c echo.Context) error {
	return h.proxy(c, "/api/plugins")
}

// GetPlugin godoc
// @Summary Get plugin detail from supermarket
// @Tags supermarket
// @Param id path string true "Plugin ID"
// @Success 200 {object} plugins.Manifest
// @Failure 404 {object} apperror.Problem
// @Failure 502 {object} apperror.Problem
// @Router /supermarket/plugins/{id} [get].
func (h *SupermarketHandler) GetPlugin(c echo.Context) error {
	id := c.Param("id")
	return h.proxy(c, "/api/plugins/"+id)
}

// ListSkills godoc
// @Summary List skills from supermarket
// @Tags supermarket
// @Param q query string false "Search query"
// @Param tag query string false "Filter by tag"
// @Param page query int false "Page number"
// @Param limit query int false "Items per page"
// @Success 200 {object} SupermarketSkillListResponse
// @Failure 502 {object} apperror.Problem
// @Router /supermarket/skills [get].
func (h *SupermarketHandler) ListSkills(c echo.Context) error {
	return h.proxy(c, "/api/skills")
}

// GetSkill godoc
// @Summary Get skill detail from supermarket
// @Tags supermarket
// @Param id path string true "Skill ID"
// @Success 200 {object} SupermarketSkillEntry
// @Failure 404 {object} apperror.Problem
// @Failure 502 {object} apperror.Problem
// @Router /supermarket/skills/{id} [get].
func (h *SupermarketHandler) GetSkill(c echo.Context) error {
	id := c.Param("id")
	return h.proxy(c, "/api/skills/"+id)
}

// ListTags godoc
// @Summary List all tags from supermarket
// @Tags supermarket
// @Success 200 {object} SupermarketTagsResponse
// @Failure 502 {object} apperror.Problem
// @Router /supermarket/tags [get].
func (h *SupermarketHandler) ListTags(c echo.Context) error {
	return h.proxy(c, "/api/tags")
}

// --- Install endpoints ---

// InstallPluginRequest is the request body for installing a plugin from supermarket.
type InstallPluginRequest struct {
	PluginID  string            `json:"plugin_id"`
	Variables map[string]string `json:"variables,omitempty"`
}

// InstallSkillRequest is the request body for installing a skill from supermarket.
type InstallSkillRequest struct {
	SkillID string `json:"skill_id"`
}

// InstallPlugin godoc
// @Summary Install plugin from supermarket to bot
// @Tags supermarket
// @Param bot_id path string true "Bot ID"
// @Param payload body InstallPluginRequest true "Install plugin request"
// @Success 200 {object} plugins.Installation
// @Failure 400 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 502 {object} apperror.Problem
// @Router /bots/{bot_id}/supermarket/install-plugin [post].
func (h *SupermarketHandler) InstallPlugin(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}

	var req InstallPluginRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Invalid("bind install plugin", err)
	}
	if strings.TrimSpace(req.PluginID) == "" {
		return apperror.Required("plugin_id")
	}

	manifest, err := h.fetchPluginEntry(c, req.PluginID)
	if err != nil {
		return err
	}
	manifest = pluginspkg.NormalizeManifest(manifest)
	if manifest.ID == "" {
		return apperror.Required("id")
	}

	ctx := c.Request().Context()
	bundleResult, err := h.installPluginBundle(ctx, botID, req.PluginID, manifest.ID)
	if err != nil {
		return apperror.Unavailable("install plugin bundle", err)
	}
	scriptsResult, err := h.runPluginInstallScripts(ctx, botID, manifest.ID, manifest.Install)
	if err != nil {
		return apperror.Unavailable("run plugin install scripts", err)
	}

	installation, err := h.pluginService.Install(ctx, botID, pluginspkg.InstallRequest{
		Manifest:  manifest,
		Variables: req.Variables,
	})
	if err != nil {
		return apperror.Invalid("install plugin", err)
	}
	installation = withPluginBundleInstallMetadata(installation, bundleResult, nil)
	installation = withPluginInstallScriptsMetadata(installation, scriptsResult, nil)
	return c.JSON(http.StatusOK, installation)
}

// InstallSkill godoc
// @Summary Install skill from supermarket to bot workspace
// @Tags supermarket
// @Param bot_id path string true "Bot ID"
// @Param payload body InstallSkillRequest true "Install skill request"
// @Success 200 {object} map[string]bool
// @Failure 400 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 502 {object} apperror.Problem
// @Router /bots/{bot_id}/supermarket/install-skill [post].
func (h *SupermarketHandler) InstallSkill(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}

	var req InstallSkillRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Invalid("bind install skill", err)
	}
	skillID := strings.TrimSpace(req.SkillID)
	if skillID == "" {
		return apperror.Required("skill_id")
	}
	if strings.Contains(skillID, "..") || strings.Contains(skillID, "/") {
		return apperror.Field("skill_id", apperror.FieldInvalid)
	}

	ctx := c.Request().Context()
	client, err := h.containers.MCPClient(ctx, botID)
	if err != nil {
		return apperror.Internal("reach workspace", err)
	}

	downloadURL := h.baseURL + "/api/skills/" + skillID + "/download"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return apperror.Internal("download skill", err)
	}
	resp, err := h.httpClient.Do(httpReq) //nolint:gosec // URL constructed from trusted config
	if err != nil {
		h.logger.Error("supermarket skill download failed", slog.String("url", downloadURL), slog.Any("error", err))
		return apperror.Unavailable("download skill", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return apperror.NotFound("download skill", nil)
	}
	if resp.StatusCode != http.StatusOK {
		return apperror.Unavailable("download skill", nil)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return apperror.Unavailable("decode skill archive", err)
	}
	defer func() { _ = gz.Close() }()

	skillDir := path.Join(skillset.ManagedDir(), skillID)
	if err := client.Mkdir(ctx, skillDir); err != nil {
		return apperror.Internal("mkdir skill dir", err)
	}

	tr := tar.NewReader(gz)
	filesWritten := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return apperror.Unavailable("read skill archive", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		relativePath := strings.TrimPrefix(hdr.Name, skillID+"/")
		if relativePath == "" || strings.Contains(relativePath, "..") {
			continue
		}

		content, err := io.ReadAll(tr)
		if err != nil {
			return apperror.Unavailable("read skill archive entry", err)
		}

		filePath := path.Join(skillDir, relativePath)
		dir := path.Dir(filePath)
		if dir != skillDir {
			_ = client.Mkdir(ctx, dir)
		}

		if err := client.WriteFile(ctx, filePath, content); err != nil {
			return apperror.Internal("write skill file", err)
		}
		filesWritten++
	}

	if filesWritten == 0 {
		return apperror.Unavailable("install skill", nil)
	}

	return c.JSON(http.StatusOK, map[string]any{"ok": true, "files_written": filesWritten})
}

// --- Supermarket upstream types (for swagger) ---

type SupermarketAuthor struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type SupermarketPluginListResponse struct {
	Total int                   `json:"total"`
	Page  int                   `json:"page"`
	Limit int                   `json:"limit"`
	Data  []pluginspkg.Manifest `json:"data"`
}

type SupermarketSkillMetadata struct {
	Author   SupermarketAuthor `json:"author"`
	Tags     []string          `json:"tags,omitempty"`
	Homepage string            `json:"homepage,omitempty"`
}

type SupermarketSkillEntry struct {
	ID          string                   `json:"id"`
	Name        string                   `json:"name"`
	Description string                   `json:"description"`
	Metadata    SupermarketSkillMetadata `json:"metadata"`
	Content     string                   `json:"content"`
	Files       []string                 `json:"files"`
}

type SupermarketSkillListResponse struct {
	Total int                     `json:"total"`
	Page  int                     `json:"page"`
	Limit int                     `json:"limit"`
	Data  []SupermarketSkillEntry `json:"data"`
}

type SupermarketTagsResponse struct {
	Tags []string `json:"tags"`
}

// --- Internal helpers ---

func (h *SupermarketHandler) fetchPluginEntry(c echo.Context, pluginID string) (pluginspkg.Manifest, error) {
	url := h.baseURL + "/api/plugins/" + pluginID
	req, err := http.NewRequestWithContext(c.Request().Context(), http.MethodGet, url, nil)
	if err != nil {
		return pluginspkg.Manifest{}, apperror.Internal("fetch plugin", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := h.httpClient.Do(req) //nolint:gosec // URL constructed from trusted config
	if err != nil {
		h.logger.Error("supermarket plugin fetch failed", slog.String("url", url), slog.Any("error", err))
		return pluginspkg.Manifest{}, apperror.Unavailable("fetch plugin", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return pluginspkg.Manifest{}, apperror.NotFound("fetch plugin", nil)
	}
	if resp.StatusCode != http.StatusOK {
		return pluginspkg.Manifest{}, apperror.Unavailable("fetch plugin", nil)
	}

	var manifest pluginspkg.Manifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return pluginspkg.Manifest{}, apperror.Unavailable("decode plugin", err)
	}
	return manifest, nil
}

func (h *SupermarketHandler) installPluginBundle(ctx context.Context, botID, downloadPluginID, targetPluginID string) (pluginBundleInstallResult, error) {
	result := newPluginBundleInstallResult()
	if h.containers == nil {
		return pluginBundleInstallResult{}, errors.New("workspace runtime provider is not configured")
	}
	client, err := h.containers.MCPClient(ctx, botID)
	if err != nil {
		return pluginBundleInstallResult{}, fmt.Errorf("workspace is not reachable: %w", err)
	}

	downloadURL := h.baseURL + "/api/plugins/" + url.PathEscape(strings.TrimSpace(downloadPluginID)) + "/download"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return pluginBundleInstallResult{}, err
	}

	resp, err := h.httpClient.Do(httpReq) //nolint:gosec // URL constructed from trusted config
	if err != nil {
		h.logger.WarnContext(ctx, "supermarket plugin bundle download failed", slog.String("url", downloadURL), slog.Any("error", err))
		return pluginBundleInstallResult{}, fmt.Errorf("supermarket unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return result, nil
	}
	if resp.StatusCode != http.StatusOK {
		return pluginBundleInstallResult{}, fmt.Errorf("supermarket returned status %d", resp.StatusCode)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return pluginBundleInstallResult{}, fmt.Errorf("invalid gzip response from supermarket: %w", err)
	}
	defer func() { _ = gz.Close() }()

	result, err = extractPluginBundleArchive(ctx, client, downloadPluginID, targetPluginID, gz)
	if err != nil {
		return pluginBundleInstallResult{}, err
	}
	return result, nil
}

func (h *SupermarketHandler) runPluginInstallScripts(ctx context.Context, botID, pluginID string, commands pluginspkg.InstallCommands) (pluginInstallScriptsResult, error) {
	result := newPluginInstallScriptsResult()
	if len(commands) == 0 {
		return result, nil
	}
	if h.containers == nil {
		return result, errors.New("workspace runtime provider is not configured")
	}
	client, err := h.containers.MCPClient(ctx, botID)
	if err != nil {
		return result, fmt.Errorf("workspace is not reachable: %w", err)
	}
	return runPluginInstallCommands(ctx, client, botID, pluginID, []string(commands))
}

func runPluginInstallCommands(ctx context.Context, executor pluginInstallScriptExecutor, botID, pluginID string, commands []string) (pluginInstallScriptsResult, error) {
	result := newPluginInstallScriptsResult()
	if executor == nil {
		return result, errors.New("plugin install script executor is not configured")
	}
	pluginRoot, err := skillset.PluginDirForID(pluginID)
	if err != nil {
		return result, err
	}
	env := []string{
		"MEMOH_PLUGIN_ID=" + pluginID,
		"MEMOH_PLUGIN_DIR=" + pluginRoot,
		"MEMOH_BOT_ID=" + botID,
	}
	for _, command := range commands {
		command = strings.TrimSpace(command)
		if command == "" {
			continue
		}
		commandResult := pluginInstallCommandResult{Command: command}
		execResult, execErr := executor.ExecWithEnv(ctx, command, pluginRoot, pluginInstallScriptTimeoutSeconds, env)
		result.CommandsRun++
		if execResult != nil {
			commandResult.ExitCode = execResult.ExitCode
			commandResult.Stdout = truncatePluginInstallOutput(execResult.Stdout)
			commandResult.Stderr = truncatePluginInstallOutput(execResult.Stderr)
		}
		if execErr != nil {
			commandResult.Error = execErr.Error()
			result.Results = append(result.Results, commandResult)
			result.OK = false
			result.Error = execErr.Error()
			return result, fmt.Errorf("plugin install command %q failed: %w", command, execErr)
		}
		if execResult != nil && execResult.ExitCode != 0 {
			commandResult.Error = fmt.Sprintf("command exited with code %d", execResult.ExitCode)
			result.Results = append(result.Results, commandResult)
			result.OK = false
			result.Error = commandResult.Error
			return result, fmt.Errorf("plugin install command %q exited with code %d", command, execResult.ExitCode)
		}
		result.Results = append(result.Results, commandResult)
	}
	return result, nil
}

const (
	pluginArchiveKindSkills  = "skills"
	pluginArchiveKindHooks   = "hooks"
	pluginArchiveKindScripts = "scripts"
)

type pluginArchiveEntry struct {
	kind         string
	root         string
	relativePath string
}

func extractPluginBundleArchive(ctx context.Context, client pluginBundleWriter, archivePluginID, targetPluginID string, r io.Reader) (pluginBundleInstallResult, error) {
	result := newPluginBundleInstallResult()
	pluginRoot, err := skillset.PluginDirForID(targetPluginID)
	if err != nil {
		return pluginBundleInstallResult{}, err
	}
	if err := client.DeleteFile(ctx, pluginRoot, true); err != nil {
		return pluginBundleInstallResult{}, fmt.Errorf("clear plugin root: %w", err)
	}
	if err := client.Mkdir(ctx, pluginRoot); err != nil {
		return pluginBundleInstallResult{}, fmt.Errorf("mkdir plugin root: %w", err)
	}

	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return result, fmt.Errorf("invalid tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		entry, ok, err := pluginBundleArchiveEntry(archivePluginID, targetPluginID, hdr.Name)
		if err != nil {
			return result, err
		}
		if !ok {
			continue
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			return result, fmt.Errorf("read tar entry failed: %w", err)
		}

		if err := client.Mkdir(ctx, entry.root); err != nil {
			return result, fmt.Errorf("mkdir %s failed: %w", entry.root, err)
		}
		filePath := path.Clean(path.Join(entry.root, entry.relativePath))
		if filePath == entry.root || !strings.HasPrefix(filePath, entry.root+"/") {
			return result, fmt.Errorf("plugin bundle path escapes root: %s", hdr.Name)
		}
		if dir := path.Dir(filePath); dir != entry.root {
			if err := client.Mkdir(ctx, dir); err != nil {
				return result, fmt.Errorf("mkdir %s failed: %w", dir, err)
			}
		}
		if err := client.WriteFile(ctx, filePath, content); err != nil {
			return result, fmt.Errorf("write file %s failed: %w", entry.relativePath, err)
		}
		switch entry.kind {
		case pluginArchiveKindSkills:
			result.Skills.FilesWritten++
		case pluginArchiveKindHooks:
			result.Hooks.FilesWritten++
		case pluginArchiveKindScripts:
			result.Scripts.FilesWritten++
		}
	}
	return result, nil
}

func pluginBundleArchiveEntry(archivePluginID, targetPluginID, rawName string) (pluginArchiveEntry, bool, error) {
	name := strings.TrimSpace(rawName)
	if name == "" || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") {
		return pluginArchiveEntry{}, false, nil
	}
	pluginPrefix := strings.Trim(path.Clean(archivePluginID), "/")
	if pluginPrefix != "" {
		name = strings.TrimPrefix(name, pluginPrefix+"/")
	}
	if name == "" {
		return pluginArchiveEntry{}, false, nil
	}

	segments := strings.Split(name, "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return pluginArchiveEntry{}, false, nil
		}
	}

	switch segments[0] {
	case "plugin.yaml":
		if len(segments) == 1 {
			return pluginArchiveEntry{}, false, nil
		}
	case "hooks.json":
		if len(segments) != 1 {
			return pluginArchiveEntry{}, false, nil
		}
		root, err := skillset.PluginDirForID(targetPluginID)
		if err != nil {
			return pluginArchiveEntry{}, false, err
		}
		return pluginArchiveEntry{kind: pluginArchiveKindHooks, root: root, relativePath: "hooks.json"}, true, nil
	case "skills":
		if len(segments) < 2 {
			return pluginArchiveEntry{}, false, nil
		}
		root, err := skillset.PluginSkillsDirForID(targetPluginID)
		if err != nil {
			return pluginArchiveEntry{}, false, err
		}
		return pluginArchiveEntry{kind: pluginArchiveKindSkills, root: root, relativePath: strings.Join(segments[1:], "/")}, true, nil
	case "scripts":
		if len(segments) < 2 {
			return pluginArchiveEntry{}, false, nil
		}
		root, err := skillset.PluginScriptsDirForID(targetPluginID)
		if err != nil {
			return pluginArchiveEntry{}, false, err
		}
		return pluginArchiveEntry{kind: pluginArchiveKindScripts, root: root, relativePath: strings.Join(segments[1:], "/")}, true, nil
	}
	return pluginArchiveEntry{}, false, nil
}

func newPluginBundleInstallResult() pluginBundleInstallResult {
	return pluginBundleInstallResult{
		Skills:  pluginAssetInstallResult{OK: true},
		Hooks:   pluginAssetInstallResult{OK: true},
		Scripts: pluginAssetInstallResult{OK: true},
	}
}

func newPluginInstallScriptsResult() pluginInstallScriptsResult {
	return pluginInstallScriptsResult{OK: true}
}

func withPluginBundleInstallMetadata(installation pluginspkg.Installation, result pluginBundleInstallResult, err error) pluginspkg.Installation {
	metadata := maps.Clone(installation.Metadata)
	if metadata == nil {
		metadata = make(map[string]any)
	}
	if err != nil {
		failed := pluginAssetInstallResult{OK: false, Error: err.Error()}
		result = pluginBundleInstallResult{Skills: failed, Hooks: failed, Scripts: failed}
	}
	metadata["skills_install"] = result.Skills
	metadata["hooks_install"] = result.Hooks
	metadata["scripts_install"] = result.Scripts
	installation.Metadata = metadata
	return installation
}

func withPluginInstallScriptsMetadata(installation pluginspkg.Installation, result pluginInstallScriptsResult, err error) pluginspkg.Installation {
	metadata := maps.Clone(installation.Metadata)
	if metadata == nil {
		metadata = make(map[string]any)
	}
	if err != nil {
		result.OK = false
		result.Error = err.Error()
	}
	metadata["install_scripts"] = result
	installation.Metadata = metadata
	return installation
}

func truncatePluginInstallOutput(output string) string {
	if len(output) <= pluginInstallScriptOutputLimit {
		return output
	}
	return output[:pluginInstallScriptOutputLimit]
}
