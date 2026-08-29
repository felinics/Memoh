package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/config"
	ctr "github.com/felinics/memoh/internal/container"
	displaypkg "github.com/felinics/memoh/internal/display"
	"github.com/felinics/memoh/internal/httpx"
	"github.com/felinics/memoh/internal/mcp"
	"github.com/felinics/memoh/internal/policy"
	"github.com/felinics/memoh/internal/workspace"
)

type ContainerdHandler struct {
	manager          containerWorkspace
	cfg              config.WorkspaceConfig
	containerBackend string
	logger           *slog.Logger
	toolGateway      *mcp.ToolGatewayService
	toolContexts     *mcp.ToolSessionContextStore
	acpRuntimes      acpRuntimeContextResolver
	mcpStdioMu       sync.Mutex
	mcpStdioSess     map[string]*mcpStdioSession
	botService       *bots.Service
	accountService   *accounts.Service
	policyService    *policy.Service
	displayService   *displaypkg.Service
	browserSessions  *browserSessionStore
}

type ContainerGPURequest struct {
	Devices []string `json:"devices,omitempty"`
}

type CreateContainerRequest struct {
	Snapshotter string               `json:"snapshotter,omitempty"`
	RestoreData bool                 `json:"restore_data,omitempty"`
	Image       string               `json:"image,omitempty"`
	GPU         *ContainerGPURequest `json:"gpu,omitempty"`
}

type CreateContainerResponse struct {
	ContainerID      string   `json:"container_id"`
	WorkspaceBackend string   `json:"workspace_backend"`
	RuntimeBackend   string   `json:"runtime_backend,omitempty"`
	ContainerPath    string   `json:"container_path"`
	Image            string   `json:"image"`
	Snapshotter      string   `json:"snapshotter"`
	CDIDevices       []string `json:"cdi_devices,omitempty"`
	Started          bool     `json:"started"`
	DataRestored     bool     `json:"data_restored"`
	HasPreservedData bool     `json:"has_preserved_data"`
}

// codesync(container-create-stream): keep these SSE payloads in sync with
// packages/sdk/src/container-stream.ts.
type createContainerPullingEvent struct {
	Type  string `json:"type"`
	Image string `json:"image"`
}

type createContainerPullProgressEvent struct {
	Type   string            `json:"type"`
	Layers []ctr.LayerStatus `json:"layers"`
}

type createContainerPullStatusEvent struct {
	Type    string `json:"type"`
	Image   string `json:"image"`
	Message string `json:"message,omitempty"`
}

type createContainerCreatingEvent struct {
	Type string `json:"type"`
}

type createContainerCompleteEvent struct {
	Type      string                  `json:"type"`
	Container CreateContainerResponse `json:"container"`
}

type createContainerRestoringEvent struct {
	Type string `json:"type"`
}

type createContainerErrorEvent struct {
	Type      string            `json:"type"`
	Code      string            `json:"code"`
	I18nKey   string            `json:"i18n_key,omitempty"`
	Args      map[string]string `json:"args"`
	Detail    string            `json:"detail,omitempty"`
	Message   string            `json:"message"`
	RequestID string            `json:"request_id,omitempty"`
}

func newWorkspaceSetupAppError(setupErr error, requestID string) (createContainerErrorEvent, bool) {
	var code apperror.Code
	switch {
	case errors.Is(setupErr, workspace.ErrWorkspaceImageIncompatible):
		code = apperror.CodeWorkspaceImageIncompatible
	case errors.Is(setupErr, workspace.ErrWorkspaceTemplateBootstrapFailed):
		code = apperror.CodeWorkspaceTemplateBootstrapFailed
	default:
		return createContainerErrorEvent{}, false
	}
	public, ok := apperror.PublicFrom(apperror.Wrap(code, setupErr, nil), requestID)
	if !ok {
		return createContainerErrorEvent{}, false
	}
	return createContainerErrorEvent{
		Type:      "error",
		Code:      string(public.Code),
		Args:      public.Args,
		Detail:    public.Detail,
		Message:   public.Detail,
		RequestID: public.RequestID,
	}, true
}

type GetContainerResponse struct {
	ContainerID      string    `json:"container_id"`
	WorkspaceBackend string    `json:"workspace_backend"`
	RuntimeBackend   string    `json:"runtime_backend,omitempty"`
	Image            string    `json:"image"`
	Status           string    `json:"status"`
	Namespace        string    `json:"namespace"`
	ContainerPath    string    `json:"container_path"`
	CDIDevices       []string  `json:"cdi_devices,omitempty"`
	TaskRunning      bool      `json:"task_running"`
	HasPreservedData bool      `json:"has_preserved_data"`
	Legacy           bool      `json:"legacy"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type ContainerMetricsStatusResponse struct {
	Exists      bool `json:"exists"`
	TaskRunning bool `json:"task_running"`
}

type ContainerCPUMetricsResponse struct {
	UsagePercent      float64 `json:"usage_percent"`
	UsageNanoseconds  uint64  `json:"usage_nanoseconds"`
	UsageNanocores    uint64  `json:"usage_nanocores"`
	UserNanoseconds   uint64  `json:"user_nanoseconds"`
	KernelNanoseconds uint64  `json:"kernel_nanoseconds"`
}

type ContainerMemoryMetricsResponse struct {
	UsageBytes   uint64  `json:"usage_bytes"`
	LimitBytes   uint64  `json:"limit_bytes"`
	UsagePercent float64 `json:"usage_percent"`
}

type ContainerStorageMetricsResponse struct {
	Path      string `json:"path"`
	UsedBytes uint64 `json:"used_bytes"`
}

type ContainerMetricsPayloadResponse struct {
	CPU     *ContainerCPUMetricsResponse     `json:"cpu,omitempty"`
	Memory  *ContainerMemoryMetricsResponse  `json:"memory,omitempty"`
	Storage *ContainerStorageMetricsResponse `json:"storage,omitempty"`
}

type GetContainerMetricsResponse struct {
	Supported         bool                               `json:"supported"`
	Backend           string                             `json:"backend"`
	UnsupportedReason string                             `json:"unsupported_reason,omitempty"`
	Status            ContainerMetricsStatusResponse     `json:"status"`
	Metrics           ContainerMetricsPayloadResponse    `json:"metrics"`
	ResourceLimits    GetContainerResourceLimitsResponse `json:"resource_limits"`
	SampledAt         *time.Time                         `json:"sampled_at,omitempty"`
}

type ContainerResourceLimitValuesResponse struct {
	CPUMillicores int64 `json:"cpu_millicores"`
	MemoryBytes   int64 `json:"memory_bytes"`
	StorageBytes  int64 `json:"storage_bytes"`
}

type ContainerResourceLimitCapabilityResponse struct {
	HardLimitSupported bool `json:"hard_limit_supported"`
	SoftLimitSupported bool `json:"soft_limit_supported"`
}

type ContainerResourceLimitCapabilitiesResponse struct {
	CPU     ContainerResourceLimitCapabilityResponse `json:"cpu"`
	Memory  ContainerResourceLimitCapabilityResponse `json:"memory"`
	Storage ContainerResourceLimitCapabilityResponse `json:"storage"`
}

type ContainerResourceLimitObservedResponse struct {
	CPUUsagePercent      float64 `json:"cpu_usage_percent"`
	MemoryUsageBytes     uint64  `json:"memory_usage_bytes"`
	MemoryLimitBytes     uint64  `json:"memory_limit_bytes"`
	StorageUsedBytes     uint64  `json:"storage_used_bytes"`
	StorageOverSoftLimit bool    `json:"storage_over_soft_limit"`
}

type GetContainerResourceLimitsResponse struct {
	Desired          ContainerResourceLimitValuesResponse       `json:"desired"`
	Applied          ContainerResourceLimitValuesResponse       `json:"applied"`
	Capabilities     ContainerResourceLimitCapabilitiesResponse `json:"capabilities"`
	Observed         ContainerResourceLimitObservedResponse     `json:"observed"`
	Status           string                                     `json:"status"`
	RequiresRecreate bool                                       `json:"requires_recreate"`
	Backend          string                                     `json:"backend"`
	WorkspaceBackend string                                     `json:"workspace_backend"`
	RuntimeBackend   string                                     `json:"runtime_backend,omitempty"`
}

type UpdateContainerResourceLimitsRequest struct {
	CPUMillicores int64 `json:"cpu_millicores"`
	MemoryBytes   int64 `json:"memory_bytes"`
	StorageBytes  int64 `json:"storage_bytes"`
}

type UpdateContainerMetricsRequest struct {
	ResourceLimits *UpdateContainerResourceLimitsRequest `json:"resource_limits"`
}

type RollbackRequest struct {
	Version int `json:"version"`
}

type CreateSnapshotRequest struct {
	SnapshotName string `json:"snapshot_name"`
}

type CreateSnapshotResponse struct {
	ContainerID         string `json:"container_id"`
	SnapshotName        string `json:"snapshot_name"`
	RuntimeSnapshotName string `json:"runtime_snapshot_name"`
	DisplayName         string `json:"display_name"`
	Snapshotter         string `json:"snapshotter"`
	Version             int    `json:"version"`
	Source              string `json:"source"`
}

type SnapshotInfo struct {
	Snapshotter string            `json:"snapshotter"`
	Name        string            `json:"name"`
	DisplayName string            `json:"display_name,omitempty"`
	RuntimeName string            `json:"runtime_snapshot_name"`
	Parent      string            `json:"parent,omitempty"`
	Kind        string            `json:"kind"`
	CreatedAt   time.Time         `json:"created_at,omitempty"`
	UpdatedAt   time.Time         `json:"updated_at,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Source      string            `json:"source"`
	Managed     bool              `json:"managed"`
	Version     *int              `json:"version,omitempty"`
}

type ListSnapshotsResponse struct {
	Snapshotter string         `json:"snapshotter"`
	Snapshots   []SnapshotInfo `json:"snapshots"`
}

func NewContainerdHandler(log *slog.Logger, manager containerWorkspace, cfg config.WorkspaceConfig, containerBackend string, botService *bots.Service, accountService *accounts.Service, policyService *policy.Service) *ContainerdHandler {
	h := &ContainerdHandler{
		manager:          manager,
		cfg:              cfg,
		containerBackend: containerBackend,
		logger:           log.With(slog.String("handler", "containerd")),
		mcpStdioSess:     make(map[string]*mcpStdioSession),
		botService:       botService,
		accountService:   accountService,
		policyService:    policyService,
		browserSessions:  newBrowserSessionStore(browserSessionIdleTTL),
	}
	h.displayService = displaypkg.NewService(h.logger, manager)
	return h
}

func (h *ContainerdHandler) Register(e *echo.Echo) {
	e.Pre(h.handleBrowserProxyPre)

	e.GET("/bots/:bot_id/skills/catalog", h.ListSafeSkills)

	group := e.Group("/bots/:bot_id/container")
	group.POST("", h.CreateContainer)
	group.GET("", h.GetContainer)
	group.GET("/metrics", h.GetContainerMetrics)
	group.PUT("/metrics", h.UpdateContainerMetrics)
	group.DELETE("", h.DeleteContainer)
	group.POST("/start", h.StartContainer)
	group.POST("/stop", h.StopContainer)
	group.POST("/snapshots", h.CreateSnapshot)
	group.GET("/snapshots", h.ListSnapshots)
	group.POST("/snapshots/rollback", h.RollbackSnapshot)
	group.POST("/data/restore", h.RestorePreservedData)
	group.GET("/skills", h.ListSkills)
	group.POST("/skills", h.UpsertSkills)
	group.DELETE("/skills", h.DeleteSkills)
	group.POST("/skills/actions", h.ApplySkillAction)
	// Terminal routes
	group.GET("/terminal", h.GetTerminalInfo)
	group.GET("/terminal/ws", h.HandleTerminalWS)
	// Browser routes
	group.POST("/browser/sessions", h.CreateBrowserSession)
	group.POST("/browser/sessions/:session_id/keepalive", h.KeepAliveBrowserSession)
	group.DELETE("/browser/sessions/:session_id", h.DeleteBrowserSession)
	// Display routes
	group.GET("/display", h.GetDisplayInfo)
	group.POST("/display/prepare", h.PrepareDisplay)
	group.GET("/display/sessions", h.ListDisplaySessions)
	group.DELETE("/display/sessions/:session_id", h.CloseDisplaySession)
	group.POST("/display/webrtc/offer", h.HandleDisplayWebRTCOffer)
	// File manager routes
	group.GET("/fs", h.FSStat)
	group.GET("/fs/list", h.FSList)
	group.GET("/fs/read", h.FSRead)
	group.GET("/fs/download", h.FSDownload)
	group.POST("/fs/archive", h.FSArchive)
	group.POST("/fs/write", h.FSWrite)
	group.POST("/fs/upload", h.FSUpload)
	group.POST("/fs/mkdir", h.FSMkdir)
	group.POST("/fs/delete", h.FSDelete)
	group.POST("/fs/rename", h.FSRename)
	group.POST("/fs/extract", h.FSExtract)
	root := e.Group("/bots/:bot_id")
	root.POST("/mcp-stdio", h.CreateMCPStdio)
	root.POST("/mcp-stdio/:connection_id", h.HandleMCPStdio)
	root.POST("/tools", h.HandleMCPTools)
}

// CreateContainer godoc
// @Summary Create and start workspace for bot
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param payload body CreateContainerRequest true "Create workspace payload"
// @Success 200 {object} CreateContainerResponse "SSE stream of workspace creation events"
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/container [post].
func (h *ContainerdHandler) CreateContainer(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}

	var req CreateContainerRequest
	if err := c.Bind(&req); err != nil {
		return newI18nHTTPError(http.StatusBadRequest, "workspace_create_request_invalid", "bots.container.createFailed", err.Error())
	}
	// Image override lets administrators specify a custom base image.
	// NOTE(saas): if this becomes a multi-tenant SaaS, image override must be
	// validated against an allowlist to prevent SSRF and resource abuse.
	ctx := c.Request().Context()
	imageOverride := strings.TrimSpace(req.Image)
	image, err := h.manager.ResolveWorkspaceImage(ctx, botID)
	if err != nil {
		h.logger.Error("resolve workspace image failed",
			slog.String("bot_id", botID), slog.Any("error", err))
		return nil
	}
	gpu, err := h.manager.ResolveWorkspaceGPU(ctx, botID)
	if err != nil {
		h.logger.Error("resolve workspace gpu failed",
			slog.String("bot_id", botID), slog.Any("error", err))
		return nil
	}
	if imageOverride != "" {
		image = config.NormalizeImageRef(imageOverride)
	}
	if req.GPU != nil {
		gpu = workspace.WorkspaceGPUConfig{Devices: req.GPU.Devices}
	}

	snapshotter := strings.TrimSpace(req.Snapshotter)
	if snapshotter == "" {
		snapshotter = h.cfg.Snapshotter
	}

	flusher, ok := c.Response().Writer.(http.Flusher)
	if !ok {
		return echo.NewHTTPError(http.StatusInternalServerError, "streaming not supported")
	}

	setSSEHeaders(c)
	c.Response().WriteHeader(http.StatusOK)
	writer := c.Response().Writer

	var mu sync.Mutex
	send := func(payload any) {
		mu.Lock()
		defer mu.Unlock()
		data, err := json.Marshal(payload)
		if err != nil {
			return
		}
		_ = writeSSEData(writer, flusher, string(data))
	}

	sendError := func(code, i18nKey, message string) {
		send(createContainerErrorEvent{
			Type:      "error",
			Code:      code,
			I18nKey:   i18nKey,
			Args:      map[string]string{},
			Message:   message,
			RequestID: httpx.RequestID(c),
		})
	}

	workspaceBackend := "container"
	// Phase 1: Pull image with progress
	send(createContainerPullingEvent{Type: "pulling", Image: image})

	var pullDone atomic.Bool
	prepareResult, pullErr := h.manager.PrepareImageForCreate(ctx, image, &ctr.PullImageOptions{
		Unpack:        true,
		StorageDriver: snapshotter,
		OnProgress: func(p ctr.PullProgress) {
			if pullDone.Load() {
				return
			}
			send(createContainerPullProgressEvent{Type: "pull_progress", Layers: p.Layers})
		},
	})
	pullDone.Store(true)
	if pullErr != nil {
		h.logger.Error("image preparation failed",
			slog.String("image", image), slog.Any("error", pullErr))
		h.recordContainerSetupFailure(ctx, botID, "image_prepare", pullErr)
		sendError("workspace_image_prepare_failed", "bots.container.createFailed", "image preparation failed: "+pullErr.Error())
		return nil
	}
	if strings.TrimSpace(prepareResult.ImageRef) != "" {
		image = prepareResult.ImageRef
	}
	switch prepareResult.Mode {
	case workspace.ImagePrepareSkipped:
		send(createContainerPullStatusEvent{Type: "pull_skipped", Image: image, Message: prepareResult.Message})
	case workspace.ImagePrepareDelegated:
		send(createContainerPullStatusEvent{Type: "pull_delegated", Image: image, Message: prepareResult.Message})
	}

	// Phase 2: Create container (image is local, should be fast)
	send(createContainerCreatingEvent{Type: "creating"})

	// Notify the client before starting if data migration will happen,
	// since restoring a large /data volume can take a while.
	willRestoreData := h.manager.HasPreservedData(botID)
	if willRestoreData {
		send(createContainerRestoringEvent{Type: "restoring"})
	}

	if err := h.manager.StartWithResolvedConfig(ctx, botID, image, gpu); err != nil {
		h.logger.Error("container start failed",
			slog.String("bot_id", botID), slog.Any("error", err))
		h.recordContainerSetupFailure(ctx, botID, "start", err)
		sendError("workspace_start_failed", "bots.container.createFailed", "workspace failed to start")
		return nil
	}
	if err := h.manager.WaitForWorkspaceReady(ctx, botID); err != nil {
		h.logger.Error("container bridge not ready",
			slog.String("bot_id", botID), slog.Any("error", err))
		sendError("workspace_not_ready", "bots.container.createFailed", "workspace runtime is not ready")
		return nil
	}
	if err := h.manager.InitializeNativeWorkspace(ctx, botID); err != nil {
		h.logger.Error("workspace initialization failed",
			slog.String("bot_id", botID),
			slog.String("request_id", httpx.RequestID(c)),
			slog.Any("error", err),
		)
		h.recordContainerSetupFailure(ctx, botID, "initialize", err)
		if event, ok := newWorkspaceSetupAppError(err, httpx.RequestID(c)); ok {
			send(event)
		} else {
			sendError("workspace_setup_failed", "bots.container.createFailed", "workspace initialization failed")
		}
		return nil
	}
	if err := h.manager.RememberWorkspaceImage(ctx, botID, image); err != nil {
		h.logger.Warn("remember workspace image failed",
			slog.String("bot_id", botID), slog.String("image", image), slog.Any("error", err))
	}
	if req.GPU != nil {
		if err := h.manager.RememberWorkspaceGPU(ctx, botID, gpu); err != nil {
			h.logger.Warn("remember workspace gpu failed",
				slog.String("bot_id", botID), slog.Any("error", err))
		}
	}

	containerID, err := h.manager.ContainerID(ctx, botID)
	if err != nil {
		h.logger.Error("container ID resolution failed after start",
			slog.String("bot_id", botID), slog.Any("error", err))
		sendError("workspace_runtime_id_failed", "bots.container.createFailed", "workspace runtime ID could not be resolved")
		return nil
	}

	dataRestored := willRestoreData && !h.manager.HasPreservedData(botID)
	if req.RestoreData && h.manager.HasPreservedData(botID) {
		if err := h.manager.RestorePreservedData(ctx, botID); err != nil {
			h.logger.Error("restore preserved data failed",
				slog.String("bot_id", botID), slog.Any("error", err))
			sendError("workspace_restore_failed", "bots.container.createFailed", "restore preserved data failed: "+err.Error())
			return nil
		}
		dataRestored = true
	}

	h.manager.RecordContainerRunning(ctx, botID, containerID, image)
	h.clearContainerSetupFailure(ctx, botID)

	status, statusErr := h.manager.GetContainerInfo(ctx, botID)
	if statusErr != nil {
		h.logger.Warn("load container status after start failed",
			slog.String("bot_id", botID), slog.Any("error", statusErr))
	}
	cdiDevices := gpu.Devices
	containerPath := ""
	responseBackend := workspaceBackend
	runtimeBackend := ""
	if status != nil {
		cdiDevices = status.CDIDevices
		containerPath = status.ContainerPath
		responseBackend = status.WorkspaceBackend
		runtimeBackend = status.RuntimeBackend
	}

	// Phase 3: Complete
	send(createContainerCompleteEvent{
		Type: "complete",
		Container: CreateContainerResponse{
			ContainerID:      containerID,
			WorkspaceBackend: responseBackend,
			RuntimeBackend:   runtimeBackend,
			ContainerPath:    containerPath,
			Image:            image,
			Snapshotter:      snapshotter,
			CDIDevices:       cdiDevices,
			Started:          true,
			DataRestored:     dataRestored,
			HasPreservedData: h.manager.HasPreservedData(botID),
		},
	})

	return nil
}

func (h *ContainerdHandler) recordContainerSetupFailure(ctx context.Context, botID, phase string, err error) {
	if h.botService == nil {
		return
	}
	if recordErr := h.botService.RecordContainerSetupFailure(ctx, botID, phase, err); recordErr != nil {
		h.logger.Warn("record bot container setup failure failed",
			slog.String("bot_id", botID),
			slog.Any("error", recordErr),
		)
	}
}

func (h *ContainerdHandler) clearContainerSetupFailure(ctx context.Context, botID string) {
	if h.botService == nil {
		return
	}
	if err := h.botService.ClearContainerSetupFailure(ctx, botID); err != nil {
		h.logger.Warn("clear bot container setup failure failed",
			slog.String("bot_id", botID),
			slog.Any("error", err),
		)
	}
}

// GetContainer godoc
// @Summary Get workspace info for bot
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} GetContainerResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/container [get].
func (h *ContainerdHandler) GetContainer(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	status, err := h.manager.GetContainerInfo(c.Request().Context(), botID)
	if err != nil {
		if errors.Is(err, workspace.ErrContainerNotFound) {
			return newI18nHTTPError(http.StatusNotFound, "workspace_not_found", "bots.container.loadFailed", "workspace not found for bot")
		}
		return newI18nHTTPError(http.StatusInternalServerError, "workspace_load_failed", "bots.container.loadFailed", err.Error())
	}
	return c.JSON(http.StatusOK, GetContainerResponse{
		ContainerID:      status.ContainerID,
		WorkspaceBackend: status.WorkspaceBackend,
		RuntimeBackend:   status.RuntimeBackend,
		Image:            status.Image,
		Status:           status.Status,
		Namespace:        status.Namespace,
		ContainerPath:    status.ContainerPath,
		CDIDevices:       status.CDIDevices,
		TaskRunning:      status.TaskRunning,
		HasPreservedData: status.HasPreservedData,
		Legacy:           status.Legacy,
		CreatedAt:        status.CreatedAt,
		UpdatedAt:        status.UpdatedAt,
	})
}

// GetContainerMetrics godoc
// @Summary Get current workspace metrics for bot
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} GetContainerMetricsResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/container/metrics [get].
func (h *ContainerdHandler) GetContainerMetrics(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}

	response, err := h.buildContainerMetricsResponse(c.Request().Context(), botID, nil)
	if err != nil {
		return newI18nHTTPError(http.StatusInternalServerError, "workspace_metrics_load_failed", "bots.container.metricsLoadFailed", err.Error())
	}
	return c.JSON(http.StatusOK, response)
}

// UpdateContainerMetrics godoc
// @Summary Update workspace metrics settings for bot
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param payload body UpdateContainerMetricsRequest true "Metrics settings payload"
// @Success 200 {object} GetContainerMetricsResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/container/metrics [put].
func (h *ContainerdHandler) UpdateContainerMetrics(c echo.Context) error {
	botID, err := h.requireBotAccessWithPermission(c, bots.PermissionManage)
	if err != nil {
		return err
	}

	var req UpdateContainerMetricsRequest
	if err := c.Bind(&req); err != nil {
		return newI18nHTTPError(http.StatusBadRequest, "workspace_resource_limits_invalid", "bots.container.resourceLimits.saveFailed", err.Error())
	}
	if req.ResourceLimits == nil {
		return newI18nHTTPError(http.StatusBadRequest, "workspace_resource_limits_required", "bots.container.resourceLimits.saveFailed", "resource_limits is required")
	}
	limitsReq := req.ResourceLimits
	if limitsReq.CPUMillicores < 0 || limitsReq.MemoryBytes < 0 || limitsReq.StorageBytes < 0 {
		return newI18nHTTPError(http.StatusBadRequest, "workspace_resource_limits_invalid", "bots.container.resourceLimits.saveFailed", "resource limits must be non-negative")
	}
	limits, err := h.manager.SetResourceLimits(c.Request().Context(), botID, ctr.ResourceLimits{
		CPUMillicores: limitsReq.CPUMillicores,
		MemoryBytes:   limitsReq.MemoryBytes,
		StorageBytes:  limitsReq.StorageBytes,
	})
	if err != nil {
		return newI18nHTTPError(http.StatusInternalServerError, "workspace_resource_limits_save_failed", "bots.container.resourceLimits.saveFailed", err.Error())
	}
	response, err := h.buildContainerMetricsResponse(c.Request().Context(), botID, limits)
	if err != nil {
		return newI18nHTTPError(http.StatusInternalServerError, "workspace_metrics_load_failed", "bots.container.metricsLoadFailed", err.Error())
	}
	return c.JSON(http.StatusOK, response)
}

func (h *ContainerdHandler) buildContainerMetricsResponse(
	ctx context.Context,
	botID string,
	resourceLimits *workspace.ResourceLimitsResult,
) (GetContainerMetricsResponse, error) {
	metrics, err := h.manager.GetContainerMetrics(ctx, botID)
	if err != nil {
		return GetContainerMetricsResponse{}, err
	}
	if resourceLimits == nil {
		resourceLimits, err = h.manager.GetResourceLimits(ctx, botID)
		if err != nil {
			return GetContainerMetricsResponse{}, err
		}
	}

	response := GetContainerMetricsResponse{
		Supported:         metrics.Supported,
		Backend:           h.containerBackend,
		UnsupportedReason: metrics.UnsupportedReason,
		Status: ContainerMetricsStatusResponse{
			Exists:      metrics.Status.Exists,
			TaskRunning: metrics.Status.TaskRunning,
		},
		Metrics: ContainerMetricsPayloadResponse{
			CPU:     toContainerCPUMetricsResponse(metrics.CPU),
			Memory:  toContainerMemoryMetricsResponse(metrics.Memory),
			Storage: toContainerStorageMetricsResponse(metrics.Storage),
		},
		ResourceLimits: h.toContainerResourceLimitsResponse(resourceLimits),
	}
	if !metrics.SampledAt.IsZero() {
		sampledAt := metrics.SampledAt
		response.SampledAt = &sampledAt
	}
	return response, nil
}

// DeleteContainer godoc
// @Summary Delete workspace for bot
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param preserve_data query bool false "Export /data before deletion"
// @Success 204
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/container [delete].
func (h *ContainerdHandler) DeleteContainer(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	preserveData := c.QueryParam("preserve_data") == "true"
	if err := h.manager.CleanupBotContainer(c.Request().Context(), botID, preserveData); err != nil {
		return newI18nHTTPError(http.StatusInternalServerError, "workspace_delete_failed", "bots.container.deleteFailed", err.Error())
	}
	return c.NoContent(http.StatusNoContent)
}

// StartContainer godoc
// @Summary Start workspace for bot
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} object
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/container/start [post].
func (h *ContainerdHandler) StartContainer(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	if err := h.manager.EnsureNativeRunning(c.Request().Context(), botID); err != nil {
		if errors.Is(err, workspace.ErrContainerNotFound) {
			return newI18nHTTPError(http.StatusNotFound, "workspace_not_found", "bots.container.startFailed", "workspace not found for bot")
		}
		return newI18nHTTPError(http.StatusInternalServerError, "workspace_start_failed", "bots.container.startFailed", err.Error())
	}
	return c.JSON(http.StatusOK, map[string]bool{"started": true})
}

// StopContainer godoc
// @Summary Stop workspace for bot
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} object
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/container/stop [post].
func (h *ContainerdHandler) StopContainer(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	if err := h.manager.StopBot(c.Request().Context(), botID); err != nil {
		if errors.Is(err, workspace.ErrContainerNotFound) {
			return newI18nHTTPError(http.StatusNotFound, "workspace_not_found", "bots.container.stopFailed", "workspace not found for bot")
		}
		return newI18nHTTPError(http.StatusInternalServerError, "workspace_stop_failed", "bots.container.stopFailed", err.Error())
	}
	return c.JSON(http.StatusOK, map[string]bool{"stopped": true})
}

// CreateSnapshot godoc
// @Summary Create workspace snapshot for bot
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param payload body CreateSnapshotRequest true "Create snapshot payload"
// @Success 200 {object} CreateSnapshotResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 501 {object} ErrorResponse "Snapshots currently not supported on this backend"
// @Router /bots/{bot_id}/container/snapshots [post].
func (h *ContainerdHandler) CreateSnapshot(c echo.Context) error {
	if h.containerBackend == "apple" {
		return newI18nHTTPError(http.StatusNotImplemented, "workspace_snapshots_unsupported", "bots.container.snapshotActionFailed", "snapshots are not supported by the Apple runtime backend")
	}
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	if h.manager == nil {
		return newI18nHTTPError(http.StatusInternalServerError, "workspace_snapshot_manager_unavailable", "bots.container.snapshotActionFailed", "snapshot manager not configured")
	}
	var req CreateSnapshotRequest
	if err := c.Bind(&req); err != nil {
		return newI18nHTTPError(http.StatusBadRequest, "workspace_snapshot_request_invalid", "bots.container.snapshotActionFailed", err.Error())
	}
	created, err := h.manager.CreateSnapshot(c.Request().Context(), botID, req.SnapshotName, workspace.SnapshotSourceManual)
	if err != nil {
		if ctr.IsNotFound(err) {
			return newI18nHTTPError(http.StatusNotFound, "workspace_not_found", "bots.container.snapshotActionFailed", "workspace not found")
		}
		return newI18nHTTPError(http.StatusInternalServerError, "workspace_snapshot_create_failed", "bots.container.snapshotActionFailed", err.Error())
	}
	return c.JSON(http.StatusOK, CreateSnapshotResponse{
		ContainerID:         created.ContainerID,
		SnapshotName:        created.SnapshotName,
		RuntimeSnapshotName: created.RuntimeSnapshotName,
		DisplayName:         created.DisplayName,
		Snapshotter:         created.Snapshotter,
		Version:             created.Version,
		Source:              workspace.SnapshotSourceManual,
	})
}

// ListSnapshots godoc
// @Summary List snapshots
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param snapshotter query string false "Snapshotter name"
// @Success 200 {object} ListSnapshotsResponse
// @Failure 501 {object} ErrorResponse "Snapshots currently not supported on this backend"
// @Router /bots/{bot_id}/container/snapshots [get].
func (h *ContainerdHandler) ListSnapshots(c echo.Context) error {
	if h.containerBackend == "apple" {
		return newI18nHTTPError(http.StatusNotImplemented, "workspace_snapshots_unsupported", "bots.container.snapshotLoadFailed", "snapshots are not supported by the Apple runtime backend")
	}
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	if h.manager == nil {
		return newI18nHTTPError(http.StatusInternalServerError, "workspace_snapshot_manager_unavailable", "bots.container.snapshotLoadFailed", "snapshot manager not configured")
	}

	data, err := h.manager.ListBotSnapshotData(c.Request().Context(), botID)
	if err != nil {
		if ctr.IsNotFound(err) {
			return newI18nHTTPError(http.StatusNotFound, "workspace_not_found", "bots.container.snapshotLoadFailed", "workspace not found")
		}
		return newI18nHTTPError(http.StatusInternalServerError, "workspace_snapshots_load_failed", "bots.container.snapshotLoadFailed", err.Error())
	}

	if req := strings.TrimSpace(c.QueryParam("snapshotter")); req != "" && req != data.Snapshotter {
		return newI18nHTTPError(http.StatusBadRequest, "workspace_snapshotter_mismatch", "bots.container.snapshotLoadFailed", "snapshotter does not match the workspace runtime")
	}

	snapshotKey := strings.TrimSpace(data.Info.StorageRef.Key)

	resp, ok := buildSnapshotListResponse(data)
	if !ok {
		h.logger.Warn("container snapshot chain root not found",
			slog.String("container_id", data.ContainerID),
			slog.String("snapshotter", data.Snapshotter),
			slog.String("snapshot_key", snapshotKey),
		)
		return newI18nHTTPError(http.StatusInternalServerError, "workspace_snapshot_chain_not_found", "bots.container.snapshotLoadFailed", "workspace snapshot chain not found")
	}
	return c.JSON(http.StatusOK, resp)
}

// RollbackSnapshot godoc
// @Summary Roll back workspace to a previous snapshot version
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param payload body RollbackRequest true "Rollback payload"
// @Success 200 {object} object
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/container/snapshots/rollback [post].
func (h *ContainerdHandler) RollbackSnapshot(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	if h.manager == nil {
		return newI18nHTTPError(http.StatusInternalServerError, "workspace_manager_unavailable", "bots.container.rollbackFailed", "manager not configured")
	}

	var req RollbackRequest
	if err := c.Bind(&req); err != nil {
		return newI18nHTTPError(http.StatusBadRequest, "workspace_snapshot_rollback_request_invalid", "bots.container.rollbackFailed", "invalid request body")
	}
	if req.Version < 1 {
		return newI18nHTTPError(http.StatusBadRequest, "workspace_snapshot_version_invalid", "bots.container.rollbackFailed", "version must be >= 1")
	}

	if err := h.manager.RollbackVersion(c.Request().Context(), botID, req.Version); err != nil {
		return newI18nHTTPError(http.StatusInternalServerError, "workspace_snapshot_rollback_failed", "bots.container.rollbackFailed", err.Error())
	}
	return c.JSON(http.StatusOK, map[string]any{"rolled_back_to": req.Version})
}

// RestorePreservedData godoc
// @Summary Restore previously preserved data into workspace
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} object
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/container/data/restore [post].
func (h *ContainerdHandler) RestorePreservedData(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	if h.manager == nil {
		return newI18nHTTPError(http.StatusInternalServerError, "workspace_manager_unavailable", "bots.container.restoreFailed", "manager not configured")
	}

	if !h.manager.HasPreservedData(botID) {
		return newI18nHTTPError(http.StatusNotFound, "workspace_preserved_data_not_found", "bots.container.restoreFailed", "no preserved data found")
	}

	if err := h.manager.RestorePreservedData(c.Request().Context(), botID); err != nil {
		return newI18nHTTPError(http.StatusInternalServerError, "workspace_restore_failed", "bots.container.restoreFailed", err.Error())
	}
	return c.JSON(http.StatusOK, map[string]bool{"restored": true})
}

func toContainerCPUMetricsResponse(metrics *ctr.CPUMetrics) *ContainerCPUMetricsResponse {
	if metrics == nil {
		return nil
	}
	return &ContainerCPUMetricsResponse{
		UsagePercent:      metrics.UsagePercent,
		UsageNanoseconds:  metrics.UsageNanoseconds,
		UsageNanocores:    metrics.UsageNanocores,
		UserNanoseconds:   metrics.UserNanoseconds,
		KernelNanoseconds: metrics.KernelNanoseconds,
	}
}

func toContainerMemoryMetricsResponse(metrics *ctr.MemoryMetrics) *ContainerMemoryMetricsResponse {
	if metrics == nil {
		return nil
	}
	return &ContainerMemoryMetricsResponse{
		UsageBytes:   metrics.UsageBytes,
		LimitBytes:   metrics.LimitBytes,
		UsagePercent: metrics.UsagePercent,
	}
}

func toContainerStorageMetricsResponse(metrics *workspace.ContainerStorageMetrics) *ContainerStorageMetricsResponse {
	if metrics == nil {
		return nil
	}
	return &ContainerStorageMetricsResponse{
		Path:      metrics.Path,
		UsedBytes: metrics.UsedBytes,
	}
}

func (h *ContainerdHandler) toContainerResourceLimitsResponse(result *workspace.ResourceLimitsResult) GetContainerResourceLimitsResponse {
	if result == nil {
		return GetContainerResourceLimitsResponse{Backend: h.containerBackend}
	}
	return GetContainerResourceLimitsResponse{
		Desired:          toContainerResourceLimitValuesResponse(result.Desired),
		Applied:          toContainerResourceLimitValuesResponse(result.Applied),
		Capabilities:     toContainerResourceLimitCapabilitiesResponse(result.Capabilities),
		Observed:         toContainerResourceLimitObservedResponse(result.Observed),
		Status:           result.Status,
		RequiresRecreate: result.RequiresRecreate,
		Backend:          h.containerBackend,
		WorkspaceBackend: result.WorkspaceBackend,
		RuntimeBackend:   result.RuntimeBackend,
	}
}

func toContainerResourceLimitValuesResponse(limits ctr.ResourceLimits) ContainerResourceLimitValuesResponse {
	return ContainerResourceLimitValuesResponse{
		CPUMillicores: limits.CPUMillicores,
		MemoryBytes:   limits.MemoryBytes,
		StorageBytes:  limits.StorageBytes,
	}
}

func toContainerResourceLimitCapabilityResponse(capability workspace.ResourceLimitCapability) ContainerResourceLimitCapabilityResponse {
	return ContainerResourceLimitCapabilityResponse{
		HardLimitSupported: capability.HardLimitSupported,
		SoftLimitSupported: capability.SoftLimitSupported,
	}
}

func toContainerResourceLimitCapabilitiesResponse(caps workspace.ResourceLimitCapabilities) ContainerResourceLimitCapabilitiesResponse {
	return ContainerResourceLimitCapabilitiesResponse{
		CPU:     toContainerResourceLimitCapabilityResponse(caps.CPU),
		Memory:  toContainerResourceLimitCapabilityResponse(caps.Memory),
		Storage: toContainerResourceLimitCapabilityResponse(caps.Storage),
	}
}

func toContainerResourceLimitObservedResponse(observed workspace.ResourceLimitObserved) ContainerResourceLimitObservedResponse {
	return ContainerResourceLimitObservedResponse{
		CPUUsagePercent:      observed.CPUUsagePercent,
		MemoryUsageBytes:     observed.MemoryUsageBytes,
		MemoryLimitBytes:     observed.MemoryLimitBytes,
		StorageUsedBytes:     observed.StorageUsedBytes,
		StorageOverSoftLimit: observed.StorageOverSoftLimit,
	}
}

func buildSnapshotListResponse(data *workspace.BotSnapshotData) (ListSnapshotsResponse, bool) {
	if data == nil {
		return ListSnapshotsResponse{}, false
	}

	snapshotter := strings.TrimSpace(data.Snapshotter)
	runtimeByName := make(map[string]ctr.SnapshotInfo, len(data.RuntimeSnapshots))
	for _, info := range data.RuntimeSnapshots {
		name := strings.TrimSpace(info.Name)
		if name == "" {
			continue
		}
		runtimeByName[name] = info
	}

	snapshotKey := strings.TrimSpace(data.Info.StorageRef.Key)
	lineage, ok := snapshotLineage(snapshotKey, data.RuntimeSnapshots)
	if !ok {
		if snapshotLineageRootRequired(snapshotter) {
			return ListSnapshotsResponse{}, false
		}
		lineage = nil
	}

	items := make([]SnapshotInfo, 0, len(lineage)+len(data.ManagedMeta))
	seen := make(map[string]struct{}, len(lineage)+len(data.ManagedMeta))
	appendRuntime := func(runtimeInfo ctr.SnapshotInfo, fallbackSource string, meta *workspace.ManagedSnapshotMeta) {
		source := fallbackSource
		managed := false
		var version *int
		displayName := ""
		itemSnapshotter := snapshotter
		if meta != nil {
			if metaSource := strings.TrimSpace(meta.Source); metaSource != "" {
				source = metaSource
			}
			if metaSnapshotter := strings.TrimSpace(meta.Snapshotter); metaSnapshotter != "" {
				itemSnapshotter = metaSnapshotter
			}
			managed = true
			version = meta.Version
			displayName = strings.TrimSpace(meta.DisplayName)
		}
		if strings.EqualFold(runtimeInfo.Kind, "archive") || hasArchiveSnapshotPrefix(runtimeInfo.Name) {
			itemSnapshotter = "archive"
		}

		name := displayName
		if name == "" {
			if version != nil {
				name = fmt.Sprintf("Version %d", *version)
			} else {
				name = runtimeInfo.Name
			}
		}
		items = append(items, SnapshotInfo{
			Snapshotter: itemSnapshotter,
			Name:        name,
			DisplayName: displayName,
			RuntimeName: runtimeInfo.Name,
			Parent:      runtimeInfo.Parent,
			Kind:        runtimeInfo.Kind,
			CreatedAt:   runtimeInfo.Created,
			UpdatedAt:   runtimeInfo.Updated,
			Labels:      runtimeInfo.Labels,
			Source:      source,
			Managed:     managed,
			Version:     version,
		})
		seen[strings.TrimSpace(runtimeInfo.Name)] = struct{}{}
	}

	for _, runtimeInfo := range lineage {
		name := strings.TrimSpace(runtimeInfo.Name)
		if meta, hasMeta := data.ManagedMeta[name]; hasMeta {
			appendRuntime(runtimeInfo, "image_layer", &meta)
			continue
		}
		appendRuntime(runtimeInfo, "image_layer", nil)
	}

	for name, meta := range data.ManagedMeta {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		runtimeInfo, exists := runtimeByName[name]
		if !exists {
			var syntheticOK bool
			runtimeInfo, syntheticOK = managedArchiveRuntimeInfo(name, meta)
			if !syntheticOK {
				continue
			}
		}
		appendRuntime(runtimeInfo, "managed", &meta)
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].Name < items[j].Name
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})

	return ListSnapshotsResponse{
		Snapshotter: snapshotter,
		Snapshots:   items,
	}, true
}

func snapshotLineageRootRequired(snapshotter string) bool {
	switch strings.ToLower(strings.TrimSpace(snapshotter)) {
	case "archive", "docker", "local":
		return false
	default:
		return true
	}
}

func managedArchiveRuntimeInfo(name string, meta workspace.ManagedSnapshotMeta) (ctr.SnapshotInfo, bool) {
	name = strings.TrimSpace(name)
	if name == "" || !managedSnapshotIsArchive(name, meta) {
		return ctr.SnapshotInfo{}, false
	}
	return ctr.SnapshotInfo{
		Name:    name,
		Parent:  strings.TrimSpace(meta.ParentRuntimeSnapshotName),
		Kind:    "archive",
		Created: meta.CreatedAt,
		Updated: meta.CreatedAt,
	}, true
}

func managedSnapshotIsArchive(name string, meta workspace.ManagedSnapshotMeta) bool {
	return strings.EqualFold(strings.TrimSpace(meta.Snapshotter), "archive") || hasArchiveSnapshotPrefix(name)
}

func hasArchiveSnapshotPrefix(name string) bool {
	return strings.HasPrefix(strings.TrimSpace(name), "archive:")
}

func snapshotLineage(root string, all []ctr.SnapshotInfo) ([]ctr.SnapshotInfo, bool) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, false
	}
	index := make(map[string]ctr.SnapshotInfo, len(all))
	for _, info := range all {
		name := strings.TrimSpace(info.Name)
		if name == "" {
			continue
		}
		index[name] = info
	}
	if _, ok := index[root]; !ok {
		return nil, false
	}
	lineage := make([]ctr.SnapshotInfo, 0, len(index))
	visited := make(map[string]struct{}, len(index))
	current := root
	for current != "" {
		if _, seen := visited[current]; seen {
			break
		}
		info, ok := index[current]
		if !ok {
			break
		}
		lineage = append(lineage, info)
		visited[current] = struct{}{}
		current = strings.TrimSpace(info.Parent)
	}
	return lineage, true
}

// ---------- auth helpers ----------

// requireBotAccess extracts bot_id from path, validates user auth, and authorizes bot access.
func (h *ContainerdHandler) requireBotAccess(c echo.Context) (string, error) {
	return h.requireBotAccessWithPermission(c, bots.PermissionManage)
}

func (h *ContainerdHandler) requireBotAccessWithPermission(c echo.Context, permission string) (string, error) {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return "", err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return "", echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := h.authorizeBotAccessWithPermission(c.Request().Context(), channelIdentityID, botID, permission); err != nil {
		return "", err
	}
	return botID, nil
}

func (*ContainerdHandler) requireChannelIdentityID(c echo.Context) (string, error) {
	return RequireChannelIdentityID(c)
}

func (h *ContainerdHandler) authorizeBotAccessWithPermission(ctx context.Context, channelIdentityID, botID, permission string) (bots.Bot, error) {
	return AuthorizeBotAccessWithPermission(ctx, h.botService, h.accountService, channelIdentityID, botID, permission)
}

// requireBotAccessWithGuest is like requireBotAccess but also allows guest access
// via ACL when the caller explicitly opts into guest-compatible access.
func (h *ContainerdHandler) requireBotAccessWithGuest(c echo.Context) (string, error) {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return "", err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return "", echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := AuthorizeBotAccess(c.Request().Context(), h.botService, h.accountService, channelIdentityID, botID); err != nil {
		return "", err
	}
	return botID, nil
}
