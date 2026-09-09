package network

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	ctr "github.com/felinics/memoh/internal/container"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type statusRuntime interface {
	GetTaskInfo(ctx context.Context, containerID string) (ctr.TaskInfo, error)
}

var ErrWorkspaceContainerMissing = errors.New("workspace is not created")

type Service struct {
	queries          dbstore.Queries
	registry         *Registry
	runtime          statusRuntime
	controller       Controller
	runtimeKind      string
	cniBinDir        string
	cniConfDir       string
	networkStateRoot string
	logger           *slog.Logger
}

func NewService(log *slog.Logger, queries dbstore.Queries, registry *Registry, runtime statusRuntime, runtimeKind, cniBinDir, cniConfDir, networkStateRoot string) *Service {
	return &Service{
		queries:          queries,
		registry:         registry,
		runtime:          runtime,
		runtimeKind:      normalizeKind(runtimeKind),
		cniBinDir:        cniBinDir,
		cniConfDir:       cniConfDir,
		networkStateRoot: networkStateRoot,
		logger:           log.With(slog.String("service", "network")),
	}
}

// SetController injects the network controller after construction to break
// the Service ↔ Controller circular dependency (Controller depends on Service
// as its BindingResolver).
func (s *Service) SetController(ctrl Controller) {
	s.controller = ctrl
}

func (s *Service) ListMeta(_ context.Context) []ProviderDescriptor {
	if s.registry == nil {
		return nil
	}
	return s.registry.ListDescriptors()
}

func (s *Service) PrepareBotConfigForWrite(cfg BotOverlayConfig) (BotOverlayConfig, error) {
	normalized, err := s.normalizeBotConfig(
		cfg.Enabled,
		cfg.Provider,
		cfg.Config,
	)
	if err != nil {
		return BotOverlayConfig{}, err
	}
	if !normalized.Enabled || strings.TrimSpace(normalized.Provider) == "" {
		return normalized, nil
	}
	provider, err := s.requireProvider(normalized.Provider)
	if err != nil {
		return BotOverlayConfig{}, err
	}
	if _, err := provider.BuildDriver(normalized); err != nil {
		return BotOverlayConfig{}, err
	}
	return normalized, nil
}

func (s *Service) StatusBot(ctx context.Context, botID string) (BotStatus, error) {
	ws := s.workspaceRuntimeStatus(ctx, botID)
	cfg, err := s.GetBotConfig(ctx, botID)
	if err != nil {
		return BotStatus{}, err
	}
	if !cfg.Enabled {
		return withWorkspace(BotStatus{
			Provider:    cfg.Provider,
			State:       "disabled",
			Title:       "Disabled",
			Description: "Bot network is disabled.",
		}, ws), nil
	}
	if strings.TrimSpace(cfg.Provider) == "" {
		return withWorkspace(BotStatus{
			State:       string(StatusStateNeedsConfig),
			Title:       "Config Required",
			Description: "Select a network provider for this bot.",
		}, ws), nil
	}
	cfg, provider, err := s.getBotProvider(ctx, botID)
	if err != nil {
		return BotStatus{}, err
	}
	providerStatus, err := provider.Status(ctx, cfg)
	if err != nil {
		return BotStatus{}, err
	}
	if providerStatus.State != StatusStateReady {
		return withWorkspace(botStatusFromProviderStatus(cfg.Provider, providerStatus), ws), nil
	}
	if s.overlayRuntimeUnsupported() {
		return withWorkspace(composeBotStatus(cfg, unsupportedOverlayStatus(cfg)), ws), nil
	}
	overlayStatus, err := s.statusOverlayBot(ctx, botID, cfg)
	if err != nil {
		if errors.Is(err, ErrWorkspaceContainerMissing) {
			return withWorkspace(BotStatus{
				Provider:    cfg.Provider,
				State:       "workspace_missing",
				Title:       "Workspace Missing",
				Description: "The bot workspace has not been created yet. Start the bot once before checking overlay status.",
			}, ws), nil
		}
		return BotStatus{}, err
	}
	return withWorkspace(composeBotStatus(cfg, overlayStatus), ws), nil
}

func (s *Service) ListBotNodes(ctx context.Context, botID string) (NodeListResponse, error) {
	cfg, provider, err := s.getBotProvider(ctx, botID)
	if err != nil {
		return NodeListResponse{}, err
	}
	items, err := provider.ListNodes(ctx, botID, cfg)
	if err != nil {
		s.logger.Warn("list network provider nodes failed",
			slog.String("bot_id", botID),
			slog.String("provider", cfg.Provider),
			slog.Any("error", err))
		return NodeListResponse{
			Provider: cfg.Provider,
			Message:  "network provider nodes are unavailable",
		}, nil
	}
	return NodeListResponse{
		Provider: cfg.Provider,
		Items:    items,
	}, nil
}

func (s *Service) ExecuteActionBot(ctx context.Context, botID, actionID string, input map[string]any) (ProviderActionExecution, error) {
	cfg, provider, err := s.getBotProvider(ctx, botID)
	if err != nil {
		return ProviderActionExecution{}, err
	}
	if actionID == "logout" {
		return s.executeLogout(ctx, botID, cfg)
	}
	if actionID != "test_connection" {
		return provider.ExecuteAction(ctx, cfg, actionID, input)
	}
	providerExec, err := provider.ExecuteAction(ctx, cfg, actionID, input)
	if err != nil {
		return providerExec, err
	}
	if providerExec.Status.State != StatusStateReady {
		return providerExec, nil
	}
	if _, ensureErr := s.ensureOverlayBot(ctx, botID, cfg); ensureErr != nil && !errors.Is(ensureErr, ErrWorkspaceContainerMissing) {
		return providerExec, ensureErr
	}
	botStatus, statusErr := s.StatusBot(ctx, botID)
	if statusErr != nil {
		return providerExec, statusErr
	}
	providerExec.Status = ProviderStatus{
		State:       StatusState(botStatus.State),
		Title:       botStatus.Title,
		Description: firstNonEmptyString(botStatus.Description, botStatus.Message),
		Details:     cloneMap(botStatus.Details),
	}
	return providerExec, nil
}

// executeLogout stops and removes the overlay sidecar and then wipes the
// provider's state directory so that the next EnsureAttached triggers a fresh
// authentication flow.
func (s *Service) executeLogout(ctx context.Context, botID string, cfg BotOverlayConfig) (ProviderActionExecution, error) {
	if err := s.detachOverlayBot(ctx, botID, cfg); err != nil && !errors.Is(err, ErrWorkspaceContainerMissing) {
		return ProviderActionExecution{}, err
	}
	if s.networkStateRoot != "" && strings.TrimSpace(cfg.Provider) != "" {
		stateDir := filepath.Join(s.networkStateRoot, "network", botID, cfg.Provider)
		if err := os.RemoveAll(stateDir); err != nil {
			s.logger.Warn("failed to wipe network state directory on logout",
				slog.String("bot_id", botID),
				slog.String("provider", cfg.Provider),
				slog.Any("error", err))
		}
	}
	return ProviderActionExecution{
		ActionID: "logout",
		Status: ProviderStatus{
			State:       StatusStateNeedsConfig,
			Title:       "Logged Out",
			Description: "SD-WAN overlay has been stopped and state cleared.",
		},
	}, nil
}

func (s *Service) ReconcileBot(ctx context.Context, botID string, previous, next BotOverlayConfig) error {
	if previous.Enabled && strings.TrimSpace(previous.Provider) != "" {
		s.logger.Info("detach previous overlay", slog.String("bot_id", botID), slog.String("provider", previous.Provider))
		if err := s.detachOverlayBot(ctx, botID, previous); err != nil && !errors.Is(err, ErrWorkspaceContainerMissing) {
			return err
		}
	}
	if !next.Enabled || strings.TrimSpace(next.Provider) == "" {
		return nil
	}
	s.logger.Info("attach current overlay", slog.String("bot_id", botID), slog.String("provider", next.Provider))
	_, ensureErr := s.ensureOverlayBot(ctx, botID, next)
	if errors.Is(ensureErr, ErrWorkspaceContainerMissing) {
		s.logger.Info("skip overlay attach because workspace container is missing", slog.String("bot_id", botID))
		return nil
	}
	return ensureErr
}

func (s *Service) normalizeBotConfig(enabled bool, provider string, config map[string]any) (BotOverlayConfig, error) {
	cfg := BotOverlayConfig{
		Enabled:  enabled,
		Provider: strings.TrimSpace(provider),
		Config:   cloneMap(config),
	}
	if !cfg.Enabled {
		if cfg.Provider == "" {
			cfg.Config = map[string]any{}
		}
		return cfg, nil
	}
	if cfg.Provider == "" {
		cfg.Config = map[string]any{}
		return cfg, nil
	}
	providerImpl, err := s.requireProvider(cfg.Provider)
	if err != nil {
		return BotOverlayConfig{}, err
	}
	cfg.Config, err = providerImpl.NormalizeConfig(cfg.Config)
	if err != nil {
		return BotOverlayConfig{}, err
	}
	return cfg, nil
}

func (s *Service) getBotProvider(ctx context.Context, botID string) (BotOverlayConfig, Provider, error) {
	cfg, err := s.GetBotConfig(ctx, botID)
	if err != nil {
		return BotOverlayConfig{}, nil, err
	}
	if strings.TrimSpace(cfg.Provider) == "" {
		return BotOverlayConfig{}, nil, fmt.Errorf("network provider is not configured for bot %s", botID)
	}
	provider, err := s.requireProvider(cfg.Provider)
	if err != nil {
		return BotOverlayConfig{}, nil, err
	}
	return cfg, provider, nil
}

func (s *Service) statusOverlayBot(ctx context.Context, botID string, cfg BotOverlayConfig) (OverlayStatus, error) {
	req, err := s.buildAttachmentRequest(ctx, botID, cfg)
	if err != nil {
		return OverlayStatus{}, err
	}
	if s.overlayRuntimeUnsupported() {
		return unsupportedOverlayStatus(cfg), nil
	}
	attachmentStatus, err := s.controller.Status(ctx, req)
	if err != nil {
		return OverlayStatus{}, err
	}
	return attachmentStatus.Overlay, nil
}

func composeBotStatus(cfg BotOverlayConfig, overlay OverlayStatus) BotStatus {
	status := BotStatus{
		Provider:     cfg.Provider,
		Attached:     overlay.Attached,
		State:        overlay.State,
		Message:      overlay.Message,
		NetworkIP:    overlay.NetworkIP,
		ProxyAddress: overlay.ProxyAddress,
		Details:      cloneMap(overlay.Details),
	}
	switch overlay.State {
	case "ready":
		status.Title = "Connected"
		status.Description = "SD-WAN overlay is running and attached."
	case "needs_login":
		status.Title = "Login Required"
		status.Description = "Open the authentication URL to complete login."
	case "starting":
		status.Title = "Starting"
		status.Description = firstNonEmptyString(overlay.Message, "SD-WAN overlay is starting.")
	case "degraded":
		status.Title = "Degraded"
		status.Description = firstNonEmptyString(overlay.Message, "SD-WAN overlay is running but reported health issues.")
	case "stopped":
		status.Title = "Stopped"
		status.Description = "SD-WAN overlay exists but is not running."
	case "missing":
		status.Title = "Not Attached"
		status.Description = "SD-WAN overlay is not attached yet. Start the bot workspace first."
	case "unsupported":
		status.Title = "Unsupported"
		status.Description = firstNonEmptyString(overlay.Message, "Provider-backed network is not supported by the current runtime backend.")
	case "disabled":
		status.Title = "Disabled"
		status.Description = "Bot network is disabled."
	default:
		status.Title = strings.ToUpper(firstNonEmptyString(overlay.State, "unknown"))
		status.Description = firstNonEmptyString(overlay.Message, "Overlay status is unavailable.")
	}
	return status
}

func (s *Service) ensureOverlayBot(ctx context.Context, botID string, cfg BotOverlayConfig) (OverlayStatus, error) {
	req, err := s.buildAttachmentRequest(ctx, botID, cfg)
	if err != nil {
		return OverlayStatus{}, err
	}
	if s.overlayRuntimeUnsupported() {
		s.logger.Info("skip overlay ensure because current runtime backend does not support provider sidecars", slog.String("bot_id", botID), slog.String("provider", cfg.Provider), slog.String("runtime", s.runtimeKind))
		return unsupportedOverlayStatus(cfg), nil
	}
	if strings.TrimSpace(req.Runtime.JoinTarget.Path) == "" {
		s.logger.Info("skip overlay ensure because workspace task is not running", slog.String("bot_id", botID), slog.String("provider", cfg.Provider))
		attachmentStatus, statusErr := s.controller.Status(ctx, req)
		if statusErr != nil {
			return OverlayStatus{}, statusErr
		}
		return attachmentStatus.Overlay, nil
	}
	req.OverlayOnly = true
	attachmentStatus, err := s.controller.EnsureAttached(ctx, req)
	if err != nil {
		return OverlayStatus{}, err
	}
	return attachmentStatus.Overlay, nil
}

func (s *Service) detachOverlayBot(ctx context.Context, botID string, cfg BotOverlayConfig) error {
	req, err := s.buildAttachmentRequest(ctx, botID, cfg)
	if err != nil {
		return err
	}
	req.OverlayOnly = true
	return s.controller.Detach(ctx, req)
}

func (s *Service) overlayRuntimeUnsupported() bool {
	return s.runtimeKind == "apple"
}

func unsupportedOverlayStatus(cfg BotOverlayConfig) OverlayStatus {
	return OverlayStatus{
		Provider: cfg.Provider,
		State:    "unsupported",
		Message:  "Provider-backed network sidecars are not supported on the current runtime backend.",
	}
}

func botStatusFromProviderStatus(provider string, status ProviderStatus) BotStatus {
	return BotStatus{
		Provider:    provider,
		State:       string(status.State),
		Title:       status.Title,
		Description: status.Description,
		Details:     cloneMap(status.Details),
	}
}

func (s *Service) requireProvider(kind string) (Provider, error) {
	if s.registry == nil {
		return nil, errors.New("network provider registry is not configured")
	}
	provider, ok := s.registry.Get(kind)
	if !ok {
		return nil, fmt.Errorf("unsupported network provider: %s", kind)
	}
	return provider, nil
}
