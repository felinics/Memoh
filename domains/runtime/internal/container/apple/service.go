package apple

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/memohai/acgo"
	"github.com/memohai/acgo/socktainer"

	containerapi "github.com/memohai/memoh/domains/runtime/container"
	"github.com/memohai/memoh/internal/config"
)

type ServiceConfig struct {
	SocketPath string
	BinaryPath string
}

// ---------------------------------------------------------------------------
// Service & lifecycle
// ---------------------------------------------------------------------------

type Service struct {
	client      *acgo.Client
	manager     *socktainer.Manager
	managerOpts []socktainer.Option
	socketPath  string
	logger      *slog.Logger
	mu          sync.Mutex
}

func NewService(ctx context.Context, log *slog.Logger, cfg ServiceConfig) (*Service, error) {
	var managerOpts []socktainer.Option
	if cfg.BinaryPath != "" {
		managerOpts = append(managerOpts, socktainer.WithBinary(cfg.BinaryPath))
	}
	if cfg.SocketPath != "" {
		managerOpts = append(managerOpts, socktainer.WithSocket(expandHome(cfg.SocketPath)))
	}

	svc := &Service{
		managerOpts: managerOpts,
		logger:      log.With(slog.String("service", "apple-container")),
	}
	if err := svc.startSocktainer(ctx); err != nil {
		return nil, err
	}
	return svc, nil
}

func (s *Service) startSocktainer(ctx context.Context) error {
	mgr := socktainer.NewManager(s.managerOpts...)
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("start socktainer: %w", err)
	}
	client, err := acgo.New(acgo.WithSocketPath(mgr.SocketPath()))
	if err != nil {
		_ = mgr.Stop()
		return fmt.Errorf("create acgo client: %w", err)
	}
	s.manager = mgr
	s.client = client
	s.socketPath = mgr.SocketPath()
	return nil
}

func (s *Service) ensureHealthy(ctx context.Context) error {
	if ok, _ := s.client.IsServing(ctx); ok {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ok, _ := s.client.IsServing(ctx); ok {
		return nil
	}
	s.logger.WarnContext(ctx, "socktainer not responding, restarting")
	_ = s.client.Close()
	_ = s.manager.Stop()
	_ = os.Remove(s.socketPath)
	if err := s.startSocktainer(ctx); err != nil {
		s.logger.ErrorContext(ctx, "socktainer restart failed", slog.Any("error", err))
		return err
	}
	s.logger.InfoContext(ctx, "socktainer restarted successfully")
	return nil
}

func (s *Service) Close() error {
	_ = s.client.Close()
	return s.manager.Stop()
}

// ---------------------------------------------------------------------------
// Images
// ---------------------------------------------------------------------------

func (s *Service) PullImage(ctx context.Context, ref string, _ *containerapi.PullImageOptions) (containerapi.ImageInfo, error) {
	if ref == "" {
		return containerapi.ImageInfo{}, containerapi.ErrInvalidArgument
	}
	if err := s.ensureHealthy(ctx); err != nil {
		return containerapi.ImageInfo{}, err
	}
	img, err := s.client.Pull(ctx, ref)
	if err != nil {
		return containerapi.ImageInfo{}, err
	}
	return toAcgoImageInfo(img), nil
}

func (s *Service) GetImage(ctx context.Context, ref string) (containerapi.ImageInfo, error) {
	if ref == "" {
		return containerapi.ImageInfo{}, containerapi.ErrInvalidArgument
	}
	if err := s.ensureHealthy(ctx); err != nil {
		return containerapi.ImageInfo{}, err
	}
	img, err := s.client.GetImage(ctx, ref)
	if err != nil {
		return containerapi.ImageInfo{}, err
	}
	return toAcgoImageInfo(img), nil
}

func (s *Service) ListImages(ctx context.Context) ([]containerapi.ImageInfo, error) {
	if err := s.ensureHealthy(ctx); err != nil {
		return nil, err
	}
	imgs, err := s.client.ListImages(ctx)
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
	if err := s.ensureHealthy(ctx); err != nil {
		return err
	}
	return s.client.DeleteImage(ctx, ref)
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
	if err := s.ensureHealthy(ctx); err != nil {
		return containerapi.ContainerInfo{}, err
	}
	if _, err := s.client.GetImage(ctx, req.ImageRef); err != nil {
		s.logger.InfoContext(ctx, "image not found locally, pulling", slog.String("image", req.ImageRef))
		if _, pullErr := s.client.Pull(ctx, req.ImageRef); pullErr != nil {
			return containerapi.ContainerInfo{}, fmt.Errorf("pull image %s: %w", req.ImageRef, pullErr)
		}
	}
	ctr, err := s.client.NewContainer(ctx, req.ID, specToCreateOpts(req)...)
	if err != nil {
		return containerapi.ContainerInfo{}, err
	}
	return acgoContainerToInfo(ctx, ctr)
}

func (s *Service) GetContainer(ctx context.Context, id string) (containerapi.ContainerInfo, error) {
	if id == "" {
		return containerapi.ContainerInfo{}, containerapi.ErrInvalidArgument
	}
	if err := s.ensureHealthy(ctx); err != nil {
		return containerapi.ContainerInfo{}, err
	}
	ctr, err := s.client.LoadContainer(ctx, id)
	if err != nil {
		return containerapi.ContainerInfo{}, err
	}
	return acgoContainerToInfo(ctx, ctr)
}

func (s *Service) ListContainers(ctx context.Context) ([]containerapi.ContainerInfo, error) {
	if err := s.ensureHealthy(ctx); err != nil {
		return nil, err
	}
	ctrs, err := s.client.Containers(ctx, acgo.WithListAll())
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
	if err := s.ensureHealthy(ctx); err != nil {
		return err
	}
	ctr, err := s.client.LoadContainer(ctx, id)
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
	if err := s.ensureHealthy(ctx); err != nil {
		return nil, err
	}
	filtersJSON := fmt.Sprintf(`{"label":["%s=%s"]}`, key, value)
	ctrs, err := s.client.Containers(ctx, acgo.WithListAll(), acgo.WithListFilters(filtersJSON))
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
	if err := s.ensureHealthy(ctx); err != nil {
		return err
	}
	ctr, err := s.client.LoadContainer(ctx, containerID)
	if err != nil {
		return err
	}
	return ctr.Start(ctx)
}

func (s *Service) StopContainer(ctx context.Context, containerID string, opts *containerapi.StopTaskOptions) error {
	if containerID == "" {
		return containerapi.ErrInvalidArgument
	}
	if err := s.ensureHealthy(ctx); err != nil {
		return err
	}
	ctr, err := s.client.LoadContainer(ctx, containerID)
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
	if err := s.ensureHealthy(ctx); err != nil {
		return containerapi.TaskInfo{}, err
	}
	ctr, err := s.client.LoadContainer(ctx, containerID)
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
	if err := s.ensureHealthy(ctx); err != nil {
		return nil, err
	}
	ctrs, err := s.client.Containers(ctx, acgo.WithListAll())
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
