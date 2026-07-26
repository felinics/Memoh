package apple

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/memohai/acgo"
	"github.com/memohai/acgo/socktainer"

	containerapi "github.com/memohai/memoh/domains/runtime/container"
	"github.com/memohai/memoh/internal/config"
)

type ServiceConfig struct {
	SocketPath string
	BinaryPath string
}

var (
	errServiceNotStarted = fmt.Errorf("%w: apple container service is not started", containerapi.ErrRuntime)
	errServiceStopped    = fmt.Errorf("%w: apple container service is stopped", containerapi.ErrRuntime)
)

// ---------------------------------------------------------------------------
// Service & lifecycle
// ---------------------------------------------------------------------------

type Service struct {
	logger         *slog.Logger
	newManager     managerFactory
	newClient      clientFactory
	lifetimeCtx    context.Context
	lifetimeCancel context.CancelFunc
	stopped        atomic.Bool
	closeDone      chan struct{}
	closeErr       error

	mu                 sync.Mutex
	started            bool
	client             appleClient
	manager            processManager
	processCancel      context.CancelFunc
	stopLifetimeCancel func() bool
	socketPath         string
}

type processManager interface {
	Start(context.Context) error
	Stop() error
	SocketPath() string
}

type appleClient interface {
	Close() error
	IsServing(context.Context) (bool, error)
	Pull(context.Context, string, ...acgo.PullOpt) (acgo.Image, error)
	GetImage(context.Context, string) (acgo.Image, error)
	ListImages(context.Context, ...acgo.ImageListOpt) ([]acgo.Image, error)
	DeleteImage(context.Context, string, ...acgo.ImageDeleteOpt) error
	NewContainer(context.Context, string, ...acgo.CreateOpt) (acgo.Container, error)
	LoadContainer(context.Context, string) (acgo.Container, error)
	Containers(context.Context, ...acgo.ListOpt) ([]acgo.Container, error)
}

type managerFactory func() processManager
type clientFactory func(string) (appleClient, error)

func NewService(log *slog.Logger, cfg ServiceConfig) *Service {
	var managerOpts []socktainer.Option
	if cfg.BinaryPath != "" {
		managerOpts = append(managerOpts, socktainer.WithBinary(cfg.BinaryPath))
	}
	if cfg.SocketPath != "" {
		managerOpts = append(managerOpts, socktainer.WithSocket(expandHome(cfg.SocketPath)))
	}
	return newService(log, func() processManager {
		return socktainer.NewManager(managerOpts...)
	}, func(socketPath string) (appleClient, error) {
		return acgo.New(acgo.WithSocketPath(socketPath))
	})
}

func newService(log *slog.Logger, newManager managerFactory, newClient clientFactory) *Service {
	if log == nil {
		log = slog.Default()
	}
	lifetimeCtx, lifetimeCancel := context.WithCancel(context.Background())
	return &Service{
		logger:         log.With(slog.String("service", "apple-container")),
		newManager:     newManager,
		newClient:      newClient,
		lifetimeCtx:    lifetimeCtx,
		lifetimeCancel: lifetimeCancel,
		closeDone:      make(chan struct{}),
	}
}

// Start starts Socktainer exactly once. The startup context controls readiness,
// but its cancellation is detached after Start returns successfully so it does
// not become the daemon's lifetime context.
func (s *Service) Start(ctx context.Context) error {
	if s.stopped.Load() {
		return errServiceStopped
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped.Load() {
		return errServiceStopped
	}
	if s.started {
		return nil
	}
	return s.startSocktainerLocked(ctx)
}

func (s *Service) startSocktainerLocked(ctx context.Context) error {
	mgr := s.newManager()
	processCtx, processCancel := context.WithCancel(context.WithoutCancel(ctx))
	stopLifetimeCancel := context.AfterFunc(s.lifetimeCtx, processCancel)

	var startupMu sync.Mutex
	relayStartupCancel := true
	stopStartupCancel := context.AfterFunc(ctx, func() {
		startupMu.Lock()
		if relayStartupCancel {
			processCancel()
		}
		startupMu.Unlock()
	})

	startAction := "start socktainer"
	startErr := mgr.Start(processCtx)
	var client appleClient
	if startErr == nil {
		client, startErr = s.newClient(mgr.SocketPath())
		if startErr != nil {
			startAction = "create acgo client"
		}
	}

	startupMu.Lock()
	startupCtxErr := ctx.Err()
	relayStartupCancel = false
	startupMu.Unlock()
	stopStartupCancel()

	if startupCtxErr != nil {
		startErr = startupCtxErr
		startAction = "start socktainer"
	}
	if startErr == nil && s.stopped.Load() {
		startErr = errServiceStopped
	}
	if startErr != nil {
		if client != nil {
			_ = client.Close()
		}
		_ = mgr.Stop()
		processCancel()
		stopLifetimeCancel()
		if errors.Is(startErr, errServiceStopped) {
			return errServiceStopped
		}
		return fmt.Errorf("%s: %w", startAction, startErr)
	}

	s.manager = mgr
	s.client = client
	s.processCancel = processCancel
	s.stopLifetimeCancel = stopLifetimeCancel
	s.socketPath = mgr.SocketPath()
	s.started = true
	return nil
}

func (s *Service) ensureHealthy(ctx context.Context) (appleClient, error) {
	if s.stopped.Load() {
		return nil, errServiceStopped
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped.Load() {
		return nil, errServiceStopped
	}
	if !s.started || s.client == nil {
		return nil, errServiceNotStarted
	}
	healthCtx, cancelHealth := context.WithCancel(ctx)
	stopLifetimeCancel := context.AfterFunc(s.lifetimeCtx, cancelHealth)
	ok, _ := s.client.IsServing(healthCtx)
	stopLifetimeCancel()
	cancelHealth()
	if s.stopped.Load() {
		return nil, errServiceStopped
	}
	if ok {
		return s.client, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.logger.WarnContext(ctx, "socktainer not responding, restarting")
	staleSocket := s.socketPath
	if err := s.stopSocktainerLocked(); err != nil {
		return nil, fmt.Errorf("stop unhealthy socktainer: %w", err)
	}
	_ = os.Remove(staleSocket)
	if s.stopped.Load() {
		return nil, errServiceStopped
	}
	if err := s.startSocktainerLocked(ctx); err != nil {
		s.logger.ErrorContext(ctx, "socktainer restart failed", slog.Any("error", err))
		return nil, err
	}
	s.logger.InfoContext(ctx, "socktainer restarted successfully")
	return s.client, nil
}

// Close initiates shutdown once and waits for the context-bounded portion of
// that shutdown. Socktainer's Stop API has no context, but it is internally
// bounded; keeping it in one owned goroutine lets a timed-out caller retry the
// join without starting another stop operation.
func (s *Service) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if s.stopped.CompareAndSwap(false, true) {
		// Cancel immediately so Close racing with startup or a health restart
		// cannot leave a newly spawned process alive while shutdown waits for the
		// state lock.
		s.lifetimeCancel()
		go func() {
			s.mu.Lock()
			s.closeErr = s.stopSocktainerLocked()
			s.mu.Unlock()
			close(s.closeDone)
		}()
	}
	select {
	case <-s.closeDone:
		return s.closeErr
	default:
	}
	select {
	case <-s.closeDone:
		return s.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) stopSocktainerLocked() error {
	client := s.client
	mgr := s.manager
	processCancel := s.processCancel
	stopLifetimeCancel := s.stopLifetimeCancel

	s.started = false
	s.client = nil
	s.manager = nil
	s.processCancel = nil
	s.stopLifetimeCancel = nil
	s.socketPath = ""

	var closeErr, stopErr error
	if client != nil {
		closeErr = client.Close()
	}
	if mgr != nil {
		stopErr = mgr.Stop()
	}
	if processCancel != nil {
		processCancel()
	}
	if stopLifetimeCancel != nil {
		stopLifetimeCancel()
	}
	return errors.Join(closeErr, stopErr)
}

// ---------------------------------------------------------------------------
// Images
// ---------------------------------------------------------------------------

func (s *Service) PullImage(ctx context.Context, ref string, _ *containerapi.PullImageOptions) (containerapi.ImageInfo, error) {
	if ref == "" {
		return containerapi.ImageInfo{}, containerapi.ErrInvalidArgument
	}
	client, err := s.ensureHealthy(ctx)
	if err != nil {
		return containerapi.ImageInfo{}, err
	}
	img, err := client.Pull(ctx, ref)
	if err != nil {
		return containerapi.ImageInfo{}, err
	}
	return toAcgoImageInfo(img), nil
}

func (s *Service) GetImage(ctx context.Context, ref string) (containerapi.ImageInfo, error) {
	if ref == "" {
		return containerapi.ImageInfo{}, containerapi.ErrInvalidArgument
	}
	client, err := s.ensureHealthy(ctx)
	if err != nil {
		return containerapi.ImageInfo{}, err
	}
	img, err := client.GetImage(ctx, ref)
	if err != nil {
		return containerapi.ImageInfo{}, err
	}
	return toAcgoImageInfo(img), nil
}

func (s *Service) ListImages(ctx context.Context) ([]containerapi.ImageInfo, error) {
	client, err := s.ensureHealthy(ctx)
	if err != nil {
		return nil, err
	}
	imgs, err := client.ListImages(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]containerapi.ImageInfo, len(imgs))
	for i, img := range imgs {
		out[i] = toAcgoImageInfo(img)
	}
	return out, nil
}

func (*Service) ResolveRemoteDigest(_ context.Context, _ string) (string, error) {
	return "", containerapi.ErrNotSupported
}

func (s *Service) DeleteImage(ctx context.Context, ref string, _ *containerapi.DeleteImageOptions) error {
	if ref == "" {
		return containerapi.ErrInvalidArgument
	}
	client, err := s.ensureHealthy(ctx)
	if err != nil {
		return err
	}
	return client.DeleteImage(ctx, ref)
}

// ---------------------------------------------------------------------------
// Containers
// ---------------------------------------------------------------------------

func (s *Service) CreateContainer(ctx context.Context, req containerapi.CreateContainerRequest) (containerapi.ContainerInfo, error) {
	if req.ID == "" || req.ImageRef == "" {
		return containerapi.ContainerInfo{}, containerapi.ErrInvalidArgument
	}
	req.ImageRef = config.NormalizeImageRef(req.ImageRef)
	if len(req.Spec.CDIDevices) > 0 {
		return containerapi.ContainerInfo{}, containerapi.ErrNotSupported
	}
	if req.Spec.NetworkJoinTarget.Value != "" || len(req.Spec.AddedCapabilities) > 0 {
		return containerapi.ContainerInfo{}, containerapi.ErrNotSupported
	}
	client, err := s.ensureHealthy(ctx)
	if err != nil {
		return containerapi.ContainerInfo{}, err
	}
	if _, err := client.GetImage(ctx, req.ImageRef); err != nil {
		s.logger.InfoContext(ctx, "image not found locally, pulling", slog.String("image", req.ImageRef))
		if _, pullErr := client.Pull(ctx, req.ImageRef); pullErr != nil {
			return containerapi.ContainerInfo{}, fmt.Errorf("pull image %s: %w", req.ImageRef, pullErr)
		}
	}
	ctr, err := client.NewContainer(ctx, req.ID, specToCreateOpts(req)...)
	if err != nil {
		return containerapi.ContainerInfo{}, err
	}
	return acgoContainerToInfo(ctx, ctr)
}

func (s *Service) GetContainer(ctx context.Context, id string) (containerapi.ContainerInfo, error) {
	if id == "" {
		return containerapi.ContainerInfo{}, containerapi.ErrInvalidArgument
	}
	client, err := s.ensureHealthy(ctx)
	if err != nil {
		return containerapi.ContainerInfo{}, err
	}
	ctr, err := client.LoadContainer(ctx, id)
	if err != nil {
		return containerapi.ContainerInfo{}, err
	}
	return acgoContainerToInfo(ctx, ctr)
}

func (s *Service) ListContainers(ctx context.Context) ([]containerapi.ContainerInfo, error) {
	client, err := s.ensureHealthy(ctx)
	if err != nil {
		return nil, err
	}
	ctrs, err := client.Containers(ctx, acgo.WithListAll())
	if err != nil {
		return nil, err
	}
	out := make([]containerapi.ContainerInfo, 0, len(ctrs))
	for _, c := range ctrs {
		info, err := acgoContainerToInfo(ctx, c)
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, nil
}

func (s *Service) DeleteContainer(ctx context.Context, id string, opts *containerapi.DeleteContainerOptions) error {
	if id == "" {
		return containerapi.ErrInvalidArgument
	}
	client, err := s.ensureHealthy(ctx)
	if err != nil {
		return err
	}
	ctr, err := client.LoadContainer(ctx, id)
	if err != nil {
		return err
	}
	var deleteOpts []acgo.DeleteOpt
	if opts != nil && opts.CleanupSnapshot {
		deleteOpts = append(deleteOpts, acgo.WithRemoveVolumes())
	}
	deleteOpts = append(deleteOpts, acgo.WithForceDelete())
	return ctr.Delete(ctx, deleteOpts...)
}

func (s *Service) ListContainersByLabel(ctx context.Context, key, value string) ([]containerapi.ContainerInfo, error) {
	if key == "" {
		return nil, containerapi.ErrInvalidArgument
	}
	client, err := s.ensureHealthy(ctx)
	if err != nil {
		return nil, err
	}
	filtersJSON := fmt.Sprintf(`{"label":["%s=%s"]}`, key, value)
	ctrs, err := client.Containers(ctx, acgo.WithListAll(), acgo.WithListFilters(filtersJSON))
	if err != nil {
		return nil, err
	}
	var out []containerapi.ContainerInfo
	for _, c := range ctrs {
		info, err := acgoContainerToInfo(ctx, c)
		if err != nil {
			return nil, err
		}
		if v, ok := info.Labels[key]; ok && (value == "" || v == value) {
			out = append(out, info)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Task / process lifecycle
// ---------------------------------------------------------------------------

func (s *Service) StartContainer(ctx context.Context, containerID string, _ *containerapi.StartTaskOptions) error {
	if containerID == "" {
		return containerapi.ErrInvalidArgument
	}
	client, err := s.ensureHealthy(ctx)
	if err != nil {
		return err
	}
	ctr, err := client.LoadContainer(ctx, containerID)
	if err != nil {
		return err
	}
	return ctr.Start(ctx)
}

func (s *Service) StopContainer(ctx context.Context, containerID string, opts *containerapi.StopTaskOptions) error {
	if containerID == "" {
		return containerapi.ErrInvalidArgument
	}
	client, err := s.ensureHealthy(ctx)
	if err != nil {
		return err
	}
	ctr, err := client.LoadContainer(ctx, containerID)
	if err != nil {
		return err
	}
	timeout := 10
	if opts != nil && opts.Timeout > 0 {
		timeout = int(opts.Timeout.Seconds())
	}
	var stopOpts []acgo.StopOpt
	stopOpts = append(stopOpts, acgo.WithStopTimeout(timeout))
	if opts != nil && opts.Signal != 0 {
		stopOpts = append(stopOpts, acgo.WithStopSignal(opts.Signal.String()))
	}
	if err := ctr.Stop(ctx, stopOpts...); err != nil && opts != nil && opts.Force {
		return ctr.Kill(ctx)
	}
	return nil
}

func (*Service) DeleteTask(context.Context, string, *containerapi.DeleteTaskOptions) error {
	return nil
}

func (s *Service) GetTaskInfo(ctx context.Context, containerID string) (containerapi.TaskInfo, error) {
	if containerID == "" {
		return containerapi.TaskInfo{}, containerapi.ErrInvalidArgument
	}
	client, err := s.ensureHealthy(ctx)
	if err != nil {
		return containerapi.TaskInfo{}, err
	}
	ctr, err := client.LoadContainer(ctx, containerID)
	if err != nil {
		return containerapi.TaskInfo{}, err
	}
	info, err := ctr.Info(ctx)
	if err != nil {
		return containerapi.TaskInfo{}, err
	}
	return containerapi.TaskInfo{
		ContainerID: containerID,
		ID:          containerID,
		Status:      containerStateToTaskStatus(info.State),
	}, nil
}

func (*Service) GetContainerMetrics(context.Context, string) (containerapi.ContainerMetrics, error) {
	return containerapi.ContainerMetrics{}, containerapi.ErrNotSupported
}

func (s *Service) ListTasks(ctx context.Context, opts *containerapi.ListTasksOptions) ([]containerapi.TaskInfo, error) {
	client, err := s.ensureHealthy(ctx)
	if err != nil {
		return nil, err
	}
	ctrs, err := client.Containers(ctx, acgo.WithListAll())
	if err != nil {
		return nil, err
	}
	var out []containerapi.TaskInfo
	for _, c := range ctrs {
		info, err := c.Info(ctx)
		if err != nil {
			continue
		}
		if opts != nil && strings.TrimSpace(opts.ContainerID) != "" && strings.TrimSpace(opts.ContainerID) != info.ID {
			continue
		}
		out = append(out, containerapi.TaskInfo{
			ContainerID: info.ID,
			ID:          info.ID,
			Status:      containerStateToTaskStatus(info.State),
		})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Network (no-op — Apple Container handles networking natively)
// ---------------------------------------------------------------------------

func (*Service) SetupNetwork(context.Context, containerapi.NetworkRequest) (containerapi.NetworkResult, error) {
	return containerapi.NetworkResult{}, nil
}
func (*Service) RemoveNetwork(context.Context, containerapi.NetworkRequest) error { return nil }
func (*Service) CheckNetwork(context.Context, containerapi.NetworkRequest) error  { return nil }

// ---------------------------------------------------------------------------
// Snapshots (not supported on Apple Container)
// ---------------------------------------------------------------------------

func (*Service) CommitSnapshot(context.Context, containerapi.CommitSnapshotRequest) error {
	return containerapi.ErrNotSupported
}

func (*Service) ListSnapshots(context.Context, containerapi.ListSnapshotsRequest) ([]containerapi.SnapshotInfo, error) {
	return nil, containerapi.ErrNotSupported
}

func (*Service) PrepareSnapshot(context.Context, containerapi.PrepareSnapshotRequest) error {
	return containerapi.ErrNotSupported
}

func (*Service) RestoreContainer(context.Context, containerapi.CreateContainerRequest) (containerapi.ContainerInfo, error) {
	return containerapi.ContainerInfo{}, containerapi.ErrNotSupported
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func specToCreateOpts(req containerapi.CreateContainerRequest) []acgo.CreateOpt {
	var opts []acgo.CreateOpt
	opts = append(opts, acgo.WithImage(req.ImageRef))
	if len(req.Spec.Cmd) > 0 {
		opts = append(opts, acgo.WithEntrypoint(req.Spec.Cmd[0]))
		if len(req.Spec.Cmd) > 1 {
			opts = append(opts, acgo.WithCmd(req.Spec.Cmd[1:]...))
		}
	}
	if req.Spec.WorkDir != "" {
		opts = append(opts, acgo.WithWorkdir(req.Spec.WorkDir))
	}
	if req.Spec.User != "" {
		opts = append(opts, acgo.WithUser(req.Spec.User))
	}
	if req.Spec.TTY {
		opts = append(opts, acgo.WithTTY())
	}
	for _, env := range req.Spec.Env {
		if k, v, ok := strings.Cut(env, "="); ok {
			opts = append(opts, acgo.WithEnv(k, v))
		}
	}
	for _, m := range req.Spec.Mounts {
		opts = append(opts, acgo.WithVolume(m.Source, m.Destination))
	}
	for _, dns := range req.Spec.DNS {
		opts = append(opts, acgo.WithDNS(dns))
	}
	for k, v := range req.Labels {
		opts = append(opts, acgo.WithLabel(k, v))
	}
	return opts
}

func toAcgoImageInfo(img acgo.Image) containerapi.ImageInfo {
	return containerapi.ImageInfo{Name: img.Name(), ID: img.ID(), Tags: img.RepoTags()}
}

func acgoContainerToInfo(ctx context.Context, c acgo.Container) (containerapi.ContainerInfo, error) {
	info, err := c.Info(ctx)
	if err != nil {
		return containerapi.ContainerInfo{}, err
	}
	return containerapi.ContainerInfo{
		ID:     info.ID,
		Image:  info.Image,
		Labels: info.Labels,
		StorageRef: containerapi.StorageRef{
			Driver: "apple",
			Key:    info.ID,
			Kind:   "container",
		},
		Runtime:   containerapi.RuntimeInfo{Name: "apple-container"},
		CreatedAt: info.CreatedAt,
		UpdatedAt: info.CreatedAt,
	}, nil
}

func containerStateToTaskStatus(state string) containerapi.TaskStatus {
	switch state {
	case "running":
		return containerapi.TaskStatusRunning
	case "created":
		return containerapi.TaskStatusCreated
	case "exited", "dead":
		return containerapi.TaskStatusStopped
	case "paused":
		return containerapi.TaskStatusPaused
	default:
		return containerapi.TaskStatusUnknown
	}
}

func expandHome(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, path[2:])
	}
	return path
}
