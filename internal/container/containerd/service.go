package containerd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"syscall"
	"time"

	tasksv1 "github.com/containerd/containerd/api/services/tasks/v1"
	tasktypes "github.com/containerd/containerd/api/types/task"
	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/containerd/v2/core/snapshots"
	cdispec "github.com/containerd/containerd/v2/pkg/cdi"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/containerd/errdefs"
	"github.com/opencontainers/image-spec/identity"
	"github.com/opencontainers/runtime-spec/specs-go"
	cdi "tags.cncf.io/container-device-interface/pkg/cdi"

	"github.com/felinics/memoh/internal/config"
)

var ErrTaskStopTimeout = errors.New("timeout waiting for task to stop")

const runtimeType = "io.containerd.runc.v2"

type DefaultService struct {
	client     *containerd.Client
	namespace  string
	logger     *slog.Logger
	cniBinDir  string
	cniConfDir string
}

func NewService(log *slog.Logger, client *containerd.Client, cfg config.Config) *DefaultService {
	return NewDefaultService(log, client, cfg)
}

func NewDefaultService(log *slog.Logger, client *containerd.Client, cfg config.Config) *DefaultService {
	namespace := cfg.Containerd.Namespace
	if namespace == "" {
		namespace = DefaultNamespace
	}
	cniBinDir := cfg.Workspace.CNIBinaryDir
	if cniBinDir == "" {
		cniBinDir = config.DefaultCNIBinaryDir
	}
	cniConfDir := cfg.Workspace.CNIConfigDir
	if cniConfDir == "" {
		cniConfDir = config.DefaultCNIConfigDir
	}
	return &DefaultService{
		client:     client,
		namespace:  namespace,
		logger:     log.With(slog.String("service", "containerd")),
		cniBinDir:  cniBinDir,
		cniConfDir: cniConfDir,
	}
}

func (s *DefaultService) PullImage(ctx context.Context, ref string, opts *PullImageOptions) (ImageInfo, error) {
	if ref == "" {
		return ImageInfo{}, ErrInvalidArgument
	}
	ref = config.NormalizeImageRef(ref)

	ctx = s.withNamespace(ctx)
	pullOpts := []containerd.RemoteOpt{}
	if opts == nil || opts.Unpack {
		pullOpts = append(pullOpts, containerd.WithPullUnpack)
	}
	if opts != nil && opts.StorageDriver != "" {
		pullOpts = append(pullOpts, containerd.WithPullSnapshotter(opts.StorageDriver))
	}

	// When OnProgress is set, poll content store for active download statuses.
	if opts != nil && opts.OnProgress != nil {
		stop := make(chan struct{})
		go func() {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			cs := s.client.ContentStore()
			for {
				select {
				case <-stop:
					return
				case <-ctx.Done():
					return
				case <-ticker.C:
					statuses, err := cs.ListStatuses(ctx)
					if err != nil {
						continue
					}
					layers := make([]LayerStatus, len(statuses))
					for i, st := range statuses {
						layers[i] = LayerStatus{
							Ref:    st.Ref,
							Offset: st.Offset,
							Total:  st.Total,
						}
					}
					opts.OnProgress(PullProgress{Layers: layers})
				}
			}
		}()
		defer close(stop)
	}

	img, err := s.client.Pull(ctx, ref, pullOpts...)
	if err != nil {
		return ImageInfo{}, mapContainerdErr(err)
	}
	return toImageInfo(img), nil
}

func (s *DefaultService) GetImage(ctx context.Context, ref string) (ImageInfo, error) {
	if ref == "" {
		return ImageInfo{}, ErrInvalidArgument
	}
	ctx = s.withNamespace(ctx)
	img, err := s.getImageWithFallback(ctx, ref)
	if err != nil {
		return ImageInfo{}, mapContainerdErr(err)
	}
	return toImageInfo(img), nil
}

func (s *DefaultService) ListImages(ctx context.Context) ([]ImageInfo, error) {
	ctx = s.withNamespace(ctx)
	imgs, err := s.client.ListImages(ctx)
	if err != nil {
		return nil, mapContainerdErr(err)
	}
	result := make([]ImageInfo, len(imgs))
	for i, img := range imgs {
		result[i] = toImageInfo(img)
	}
	return result, nil
}

func (s *DefaultService) DeleteImage(ctx context.Context, ref string, opts *DeleteImageOptions) error {
	if ref == "" {
		return ErrInvalidArgument
	}
	ctx = s.withNamespace(ctx)
	deleteOpts := []images.DeleteOpt{}
	if opts != nil && opts.Synchronous {
		deleteOpts = append(deleteOpts, images.SynchronousDelete())
	}
	return mapContainerdErr(s.client.ImageService().Delete(ctx, ref, deleteOpts...))
}

func specOptsFromSpec(spec ContainerSpec) []oci.SpecOpts {
	var opts []oci.SpecOpts

	if len(spec.Cmd) > 0 {
		opts = append(opts, oci.WithProcessArgs(spec.Cmd...))
	}
	if len(spec.Env) > 0 {
		opts = append(opts, oci.WithEnv(spec.Env))
	}
	if spec.WorkDir != "" {
		opts = append(opts, oci.WithProcessCwd(spec.WorkDir))
	}
	if spec.User != "" {
		opts = append(opts, oci.WithUser(spec.User))
	}
	if spec.TTY {
		opts = append(opts, oci.WithTTY)
	}
	if len(spec.Mounts) > 0 {
		mounts := make([]specs.Mount, len(spec.Mounts))
		for i, m := range spec.Mounts {
			mounts[i] = specs.Mount{
				Destination: m.Destination,
				Type:        m.Type,
				Source:      m.Source,
				Options:     m.Options,
			}
		}
		opts = append(opts, oci.WithMounts(mounts))
	}
	if target := networkJoinTargetValue(spec); target != "" {
		opts = append(opts, withNetworkNamespacePath(target))
	}
	if len(spec.AddedCapabilities) > 0 {
		opts = append(opts, withAddedCapabilities(spec.AddedCapabilities...))
	}
	if len(spec.CDIDevices) > 0 {
		opts = append(opts, withStaticCDIRegistry())
		opts = append(opts, cdispec.WithCDIDevices(spec.CDIDevices...))
	}

	return opts
}

func specOptsFromResourceLimits(limits ResourceLimits) []oci.SpecOpts {
	var opts []oci.SpecOpts
	if limits.MemoryBytes > 0 {
		opts = append(opts, oci.WithMemoryLimit(uint64(limits.MemoryBytes))) //nolint:gosec // validated as non-negative before container creation.
	}
	if limits.CPUMillicores > 0 {
		const cpuCFSPeriod = uint64(100_000)
		quota := limits.CPUMillicores * int64(cpuCFSPeriod) / 1000
		if quota > 0 {
			opts = append(opts, oci.WithCPUCFS(quota, cpuCFSPeriod))
		}
	}
	return opts
}

func networkJoinTargetValue(spec ContainerSpec) string {
	return spec.NetworkJoinTarget.Value
}

func withNetworkNamespacePath(path string) oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, spec *oci.Spec) error {
		if spec.Linux == nil {
			spec.Linux = &specs.Linux{}
		}
		filtered := make([]specs.LinuxNamespace, 0, len(spec.Linux.Namespaces)+1)
		for _, ns := range spec.Linux.Namespaces {
			if ns.Type != specs.NetworkNamespace {
				filtered = append(filtered, ns)
			}
		}
		filtered = append(filtered, specs.LinuxNamespace{
			Type: specs.NetworkNamespace,
			Path: path,
		})
		spec.Linux.Namespaces = filtered
		return nil
	}
}

func withAddedCapabilities(capabilities ...string) oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, spec *oci.Spec) error {
		if spec.Process == nil {
			spec.Process = &specs.Process{}
		}
		if spec.Process.Capabilities == nil {
			spec.Process.Capabilities = &specs.LinuxCapabilities{}
		}
		spec.Process.Capabilities.Bounding = appendUnique(spec.Process.Capabilities.Bounding, capabilities...)
		spec.Process.Capabilities.Effective = appendUnique(spec.Process.Capabilities.Effective, capabilities...)
		spec.Process.Capabilities.Inheritable = appendUnique(spec.Process.Capabilities.Inheritable, capabilities...)
		spec.Process.Capabilities.Permitted = appendUnique(spec.Process.Capabilities.Permitted, capabilities...)
		spec.Process.Capabilities.Ambient = appendUnique(spec.Process.Capabilities.Ambient, capabilities...)
		return nil
	}
}

func appendUnique(current []string, values ...string) []string {
	seen := make(map[string]struct{}, len(current))
	for _, item := range current {
		seen[item] = struct{}{}
	}
	out := append([]string(nil), current...)
	for _, item := range values {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func withStaticCDIRegistry() oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, _ *oci.Spec) error {
		_ = cdi.Configure(cdi.WithAutoRefresh(false))
		if err := cdi.Refresh(); err != nil {
			// Invalid specs for other vendors should not block injection of a
			// resolvable device set for the current container.
			return nil
		}
		return nil
	}
}

func (s *DefaultService) CreateContainer(ctx context.Context, req CreateContainerRequest) (ContainerInfo, error) {
	if req.ID == "" || req.ImageRef == "" {
		return ContainerInfo{}, ErrInvalidArgument
	}
	req.ImageRef = config.NormalizeImageRef(req.ImageRef)

	ctx = s.withNamespace(ctx)
	if _, err := s.client.LoadContainer(ctx, req.ID); err == nil {
		return ContainerInfo{}, ErrAlreadyExists
	} else if !errdefs.IsNotFound(err) {
		return ContainerInfo{}, mapContainerdErr(err)
	}
	ctx, done, err := s.client.WithLease(ctx)
	if err != nil {
		return ContainerInfo{}, mapContainerdErr(err)
	}
	defer func() { _ = done(ctx) }()
	image, err := s.getImageWithFallback(ctx, req.ImageRef)
	if err != nil {
		pullOpts := &PullImageOptions{Unpack: true, StorageDriver: req.StorageRef.Driver}
		_, err = s.PullImage(ctx, req.ImageRef, pullOpts)
		if err != nil {
			return ContainerInfo{}, err
		}
		image, err = s.getImageWithFallback(ctx, req.ImageRef)
		if err != nil {
			return ContainerInfo{}, mapContainerdErr(err)
		}
	}
	snapshotter := strings.TrimSpace(req.StorageRef.Driver)
	snapshotID := strings.TrimSpace(req.StorageRef.Key)
	if snapshotID == "" {
		snapshotID = req.ID
	}

	specOpts := []oci.SpecOpts{
		oci.WithDefaultSpecForPlatform("linux/" + runtime.GOARCH),
		oci.WithImageConfig(image),
	}
	specOpts = append(specOpts, specOptsFromSpec(req.Spec)...)
	specOpts = append(specOpts, specOptsFromResourceLimits(req.ResourceLimits)...)

	containerOpts := []containerd.NewContainerOpts{
		containerd.WithImage(image),
	}
	if snapshotter != "" {
		containerOpts = append(containerOpts, containerd.WithSnapshotter(snapshotter))
	}
	if snapshotter != "" {
		parent, err := s.snapshotParentFromLayers(ctx, image)
		if err != nil {
			return ContainerInfo{}, mapContainerdErr(err)
		}
		ok, err := s.snapshotExists(ctx, snapshotter, parent)
		if err != nil {
			return ContainerInfo{}, mapContainerdErr(err)
		}
		if !ok {
			return ContainerInfo{}, fmt.Errorf("parent snapshot %s does not exist", parent)
		}
		if err := s.prepareSnapshot(ctx, snapshotter, snapshotID, parent); err != nil {
			return ContainerInfo{}, mapContainerdErr(err)
		}
		containerOpts = append(containerOpts, containerd.WithSnapshot(snapshotID))
	} else {
		containerOpts = append(containerOpts, containerd.WithNewSnapshot(snapshotID, image))
	}
	containerOpts = append(containerOpts, containerd.WithNewSpec(specOpts...))
	containerOpts = append(containerOpts, containerd.WithRuntime(runtimeType, nil))
	if len(req.Labels) > 0 {
		containerOpts = append(containerOpts, containerd.WithContainerLabels(req.Labels))
	}

	ctrObj, err := s.client.NewContainer(ctx, req.ID, containerOpts...)
	if err != nil {
		return ContainerInfo{}, mapContainerdErr(err)
	}
	info, err := toContainerInfo(ctx, ctrObj)
	return info, mapContainerdErr(err)
}

func (*DefaultService) snapshotParentFromLayers(ctx context.Context, image containerd.Image) (string, error) {
	diffIDs, err := image.RootFS(ctx)
	if err != nil {
		return "", fmt.Errorf("read image rootfs: %w", err)
	}
	if len(diffIDs) == 0 {
		return "", errors.New("image has no layers")
	}
	chainIDs := identity.ChainIDs(diffIDs)
	return chainIDs[len(chainIDs)-1].String(), nil
}

func (s *DefaultService) snapshotExists(ctx context.Context, snapshotter, key string) (bool, error) {
	if snapshotter == "" || key == "" {
		return false, ErrInvalidArgument
	}
	_, err := s.client.SnapshotService(snapshotter).Stat(ctx, key)
	if err == nil {
		return true, nil
	}
	if errdefs.IsNotFound(err) {
		return false, nil
	}
	return false, err
}

func (s *DefaultService) prepareSnapshot(ctx context.Context, snapshotter, key, parent string) error {
	if snapshotter == "" || key == "" || parent == "" {
		return ErrInvalidArgument
	}
	sn := s.client.SnapshotService(snapshotter)
	if _, err := sn.Stat(ctx, key); err == nil {
		if err := sn.Remove(ctx, key); err != nil {
			return err
		}
	} else if !errdefs.IsNotFound(err) {
		return err
	}
	_, err := sn.Prepare(ctx, key, parent)
	return err
}

func (s *DefaultService) getImageWithFallback(ctx context.Context, ref string) (containerd.Image, error) {
	image, err := s.client.GetImage(ctx, ref)
	if err == nil {
		return image, nil
	}
	// Official Docker Hub images (e.g. "nginx:latest") may be stored under
	// either "docker.io/library/nginx:latest" or the short form. Try both.
	if strings.HasPrefix(ref, "docker.io/library/") {
		short := strings.TrimPrefix(ref, "docker.io/library/")
		if img, altErr := s.client.GetImage(ctx, short); altErr == nil {
			return img, nil
		}
	}
	return nil, err
}

func (s *DefaultService) GetContainer(ctx context.Context, id string) (ContainerInfo, error) {
	if id == "" {
		return ContainerInfo{}, ErrInvalidArgument
	}
	ctx = s.withNamespace(ctx)
	ctrObj, err := s.client.LoadContainer(ctx, id)
	if err != nil {
		return ContainerInfo{}, mapContainerdErr(err)
	}
	info, err := toContainerInfo(ctx, ctrObj)
	return info, mapContainerdErr(err)
}

func (s *DefaultService) ListContainers(ctx context.Context) ([]ContainerInfo, error) {
	ctx = s.withNamespace(ctx)
	ctrs, err := s.client.Containers(ctx)
	if err != nil {
		return nil, mapContainerdErr(err)
	}
	result := make([]ContainerInfo, 0, len(ctrs))
	for _, c := range ctrs {
		info, err := toContainerInfo(ctx, c)
		if err != nil {
			return nil, mapContainerdErr(err)
		}
		result = append(result, info)
	}
	return result, nil
}

func (s *DefaultService) DeleteContainer(ctx context.Context, id string, opts *DeleteContainerOptions) error {
	if id == "" {
		return ErrInvalidArgument
	}

	ctx = s.withNamespace(ctx)
	container, err := s.client.LoadContainer(ctx, id)
	if err != nil {
		return mapContainerdErr(err)
	}

	// A stopped task still holds an entry in containerd; container.Delete fails
	// with FAILED_PRECONDITION if any task entry exists. Delete it first.
	if task, err := container.Task(ctx, nil); err == nil {
		if _, err := task.Delete(ctx, containerd.WithProcessKill); err != nil && !errdefs.IsNotFound(err) {
			return mapContainerdErr(err)
		}
	} else if !errdefs.IsNotFound(err) {
		return mapContainerdErr(err)
	}

	deleteOpts := []containerd.DeleteOpts{}
	cleanupSnapshot := true
	if opts != nil {
		cleanupSnapshot = opts.CleanupSnapshot
	}
	if cleanupSnapshot {
		deleteOpts = append(deleteOpts, containerd.WithSnapshotCleanup)
	}

	return mapContainerdErr(container.Delete(ctx, deleteOpts...))
}

func (s *DefaultService) StartContainer(ctx context.Context, containerID string, _ *StartTaskOptions) error {
	if containerID == "" {
		return ErrInvalidArgument
	}

	ctx = s.withNamespace(ctx)
	container, err := s.client.LoadContainer(ctx, containerID)
	if err != nil {
		return mapContainerdErr(err)
	}

	task, err := container.NewTask(ctx, cio.NullIO)
	if err != nil {
		return mapContainerdErr(err)
	}
	return mapContainerdErr(task.Start(ctx))
}

func (s *DefaultService) getTask(ctx context.Context, containerID string) (containerd.Task, context.Context, error) {
	if containerID == "" {
		return nil, nil, ErrInvalidArgument
	}

	ctx = s.withNamespace(ctx)
	container, err := s.client.LoadContainer(ctx, containerID)
	if err != nil {
		return nil, nil, mapContainerdErr(err)
	}
	task, err := container.Task(ctx, nil)
	if err != nil {
		return nil, nil, mapContainerdErr(err)
	}
	return task, ctx, nil
}

func (s *DefaultService) GetTaskInfo(ctx context.Context, containerID string) (TaskInfo, error) {
	task, ctx, err := s.getTask(ctx, containerID)
	if err != nil {
		return TaskInfo{}, mapContainerdErr(err)
	}
	status, err := task.Status(ctx)
	if err != nil {
		return TaskInfo{}, mapContainerdErr(err)
	}
	return TaskInfo{
		ContainerID: containerID,
		ID:          task.ID(),
		PID:         task.Pid(),
		NetworkJoinTarget: NetworkJoinTarget{
			Kind:  "network-namespace",
			Value: networkNamespacePath(task.Pid()),
			PID:   task.Pid(),
		},
		Status:   convertTaskStatus(status.Status),
		ExitCode: status.ExitStatus,
	}, nil
}

func (s *DefaultService) ListTasks(ctx context.Context, opts *ListTasksOptions) ([]TaskInfo, error) {
	ctx = s.withNamespace(ctx)
	request := &tasksv1.ListTasksRequest{}
	if opts != nil && strings.TrimSpace(opts.ContainerID) != "" {
		request.Filter = "container.id==" + strings.TrimSpace(opts.ContainerID)
	}

	response, err := s.client.TaskService().List(ctx, request)
	if err != nil {
		return nil, mapContainerdErr(err)
	}

	tasks := make([]TaskInfo, 0, len(response.Tasks))
	for _, task := range response.Tasks {
		tasks = append(tasks, TaskInfo{
			ContainerID: task.ContainerID,
			ID:          task.ID,
			PID:         task.Pid,
			NetworkJoinTarget: NetworkJoinTarget{
				Kind:  "network-namespace",
				Value: networkNamespacePath(task.Pid),
				PID:   task.Pid,
			},
			Status:   convertContainerdTaskStatus(task.Status),
			ExitCode: task.ExitStatus,
		})
	}

	return tasks, nil
}

func (s *DefaultService) StopContainer(ctx context.Context, containerID string, opts *StopTaskOptions) error {
	if containerID == "" {
		return ErrInvalidArgument
	}

	task, ctx, err := s.getTask(ctx, containerID)
	if err != nil {
		return mapContainerdErr(err)
	}

	signal := syscall.SIGTERM
	timeout := 10 * time.Second
	force := false
	if opts != nil {
		if opts.Signal != 0 {
			signal = opts.Signal
		}
		if opts.Timeout != 0 {
			timeout = opts.Timeout
		}
		force = opts.Force
	}

	if err := task.Kill(ctx, signal); err != nil {
		return mapContainerdErr(err)
	}

	statusC, err := task.Wait(ctx)
	if err != nil {
		return mapContainerdErr(err)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-statusC:
		return nil
	case <-timer.C:
		if force {
			if err := task.Kill(ctx, syscall.SIGKILL); err != nil {
				return fmt.Errorf("force kill failed: %w", mapContainerdErr(err))
			}
			<-statusC
			return nil
		}
		return ErrTaskStopTimeout
	}
}

func (s *DefaultService) DeleteTask(ctx context.Context, containerID string, opts *DeleteTaskOptions) error {
	if containerID == "" {
		return ErrInvalidArgument
	}

	task, ctx, err := s.getTask(ctx, containerID)
	if err != nil {
		return mapContainerdErr(err)
	}

	if opts != nil && opts.Force {
		// Kill and wait for exit before deleting; containerd rejects Delete on a
		// still-running process even when force is requested.
		_ = task.Kill(ctx, syscall.SIGKILL)
		if statusC, waitErr := task.Wait(ctx); waitErr == nil {
			waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			select {
			case <-statusC:
			case <-waitCtx.Done():
			}
		}
	}

	_, err = task.Delete(ctx)
	return mapContainerdErr(err)
}

func (s *DefaultService) ListContainersByLabel(ctx context.Context, key, value string) ([]ContainerInfo, error) {
	if key == "" {
		return nil, ErrInvalidArgument
	}

	ctx = s.withNamespace(ctx)
	containers, err := s.client.Containers(ctx)
	if err != nil {
		return nil, mapContainerdErr(err)
	}

	filtered := make([]ContainerInfo, 0, len(containers))
	for _, container := range containers {
		ci, err := toContainerInfo(ctx, container)
		if err != nil {
			return nil, mapContainerdErr(err)
		}
		if labelValue, ok := ci.Labels[key]; ok && (value == "" || value == labelValue) {
			filtered = append(filtered, ci)
		}
	}
	return filtered, nil
}

func (s *DefaultService) CommitSnapshot(ctx context.Context, req CommitSnapshotRequest) error {
	snapshotter := strings.TrimSpace(req.Source.Driver)
	name := strings.TrimSpace(req.Target.Key)
	key := strings.TrimSpace(req.Source.Key)
	if snapshotter == "" || name == "" || key == "" {
		return ErrInvalidArgument
	}
	ctx = s.withNamespace(ctx)
	return mapContainerdErr(s.client.SnapshotService(snapshotter).Commit(ctx, name, key))
}

func (s *DefaultService) ListSnapshots(ctx context.Context, req ListSnapshotsRequest) ([]SnapshotInfo, error) {
	snapshotter := strings.TrimSpace(req.Driver)
	if snapshotter == "" {
		return nil, ErrInvalidArgument
	}
	ctx = s.withNamespace(ctx)
	var infos []SnapshotInfo
	if err := s.client.SnapshotService(snapshotter).Walk(ctx, func(_ context.Context, info snapshots.Info) error {
		infos = append(infos, SnapshotInfo{
			Name:    info.Name,
			Parent:  info.Parent,
			Kind:    info.Kind.String(),
			Created: info.Created,
			Updated: info.Updated,
			Labels:  info.Labels,
		})
		return nil
	}); err != nil {
		return nil, mapContainerdErr(err)
	}
	return infos, nil
}

func (s *DefaultService) PrepareSnapshot(ctx context.Context, req PrepareSnapshotRequest) error {
	snapshotter := strings.TrimSpace(req.Target.Driver)
	key := strings.TrimSpace(req.Target.Key)
	parent := strings.TrimSpace(req.Parent.Key)
	if snapshotter == "" || key == "" || parent == "" {
		return ErrInvalidArgument
	}
	ctx = s.withNamespace(ctx)
	_, err := s.client.SnapshotService(snapshotter).Prepare(ctx, key, parent)
	return mapContainerdErr(err)
}

func (s *DefaultService) RestoreContainer(ctx context.Context, req CreateContainerRequest) (ContainerInfo, error) {
	snapshotID := strings.TrimSpace(req.StorageRef.Key)
	snapshotter := strings.TrimSpace(req.StorageRef.Driver)
	if req.ID == "" || snapshotID == "" {
		return ContainerInfo{}, ErrInvalidArgument
	}

	ctx = s.withNamespace(ctx)

	imageRef := req.ImageRef
	if imageRef == "" {
		return ContainerInfo{}, ErrInvalidArgument
	}

	image, err := s.getImageWithFallback(ctx, imageRef)
	if err != nil {
		_, pullErr := s.PullImage(ctx, imageRef, &PullImageOptions{
			Unpack:        true,
			StorageDriver: snapshotter,
		})
		if pullErr != nil {
			return ContainerInfo{}, mapContainerdErr(pullErr)
		}
		image, err = s.getImageWithFallback(ctx, imageRef)
		if err != nil {
			return ContainerInfo{}, mapContainerdErr(err)
		}
	}

	specOpts := []oci.SpecOpts{
		oci.WithDefaultSpecForPlatform("linux/" + runtime.GOARCH),
		oci.WithImageConfig(image),
	}
	specOpts = append(specOpts, specOptsFromSpec(req.Spec)...)
	specOpts = append(specOpts, specOptsFromResourceLimits(req.ResourceLimits)...)

	containerOpts := []containerd.NewContainerOpts{
		containerd.WithImage(image),
	}
	if snapshotter != "" {
		containerOpts = append(containerOpts, containerd.WithSnapshotter(snapshotter))
	}
	containerOpts = append(containerOpts,
		containerd.WithSnapshot(snapshotID),
		containerd.WithNewSpec(specOpts...),
	)
	if len(req.Labels) > 0 {
		containerOpts = append(containerOpts, containerd.WithContainerLabels(req.Labels))
	}

	containerOpts = append(containerOpts, containerd.WithRuntime(runtimeType, nil))

	ctrObj, err := s.client.NewContainer(ctx, req.ID, containerOpts...)
	if err != nil {
		return ContainerInfo{}, mapContainerdErr(err)
	}
	info, err := toContainerInfo(ctx, ctrObj)
	return info, mapContainerdErr(err)
}

func (s *DefaultService) SnapshotMounts(ctx context.Context, snapshotter, key string) ([]MountInfo, error) {
	if snapshotter == "" || key == "" {
		return nil, ErrInvalidArgument
	}
	ctx = s.withNamespace(ctx)
	mounts, err := s.client.SnapshotService(snapshotter).Mounts(ctx, key)
	if err != nil {
		return nil, mapContainerdErr(err)
	}
	result := make([]MountInfo, len(mounts))
	for i, m := range mounts {
		result[i] = MountInfo{
			Type:    m.Type,
			Source:  m.Source,
			Options: m.Options,
		}
	}
	return result, nil
}

func (s *DefaultService) SnapshotUsage(ctx context.Context, snapshotter, key string) (SnapshotUsage, error) {
	if snapshotter == "" || key == "" {
		return SnapshotUsage{}, ErrInvalidArgument
	}
	ctx = s.withNamespace(ctx)
	usage, err := s.client.SnapshotService(snapshotter).Usage(ctx, key)
	if err != nil {
		return SnapshotUsage{}, mapContainerdErr(err)
	}
	result := SnapshotUsage{}
	if usage.Size > 0 {
		result.SizeBytes = uint64(usage.Size) //nolint:gosec // negative values are ignored above
	}
	if usage.Inodes > 0 {
		result.Inodes = uint64(usage.Inodes) //nolint:gosec // negative values are ignored above
	}
	return result, nil
}

func (s *DefaultService) SetupNetwork(ctx context.Context, req NetworkRequest) (NetworkResult, error) {
	if req.ContainerID == "" {
		return NetworkResult{}, ErrInvalidArgument
	}
	if req.JoinTarget.PID == 0 {
		task, taskCtx, err := s.getTask(ctx, req.ContainerID)
		if err != nil {
			return NetworkResult{}, mapContainerdErr(err)
		}
		ctx = taskCtx
		req.JoinTarget.PID = task.Pid()
	}
	ip, err := s.setupNetwork(ctx, req)
	if err != nil {
		return NetworkResult{}, mapContainerdErr(err)
	}
	return NetworkResult{IP: ip}, nil
}

func (s *DefaultService) RemoveNetwork(ctx context.Context, req NetworkRequest) error {
	if req.ContainerID == "" {
		return ErrInvalidArgument
	}
	if req.JoinTarget.PID == 0 {
		task, taskCtx, err := s.getTask(ctx, req.ContainerID)
		if err != nil {
			return mapContainerdErr(err)
		}
		ctx = taskCtx
		req.JoinTarget.PID = task.Pid()
	}
	return mapContainerdErr(s.removeNetwork(ctx, req))
}

func (s *DefaultService) CheckNetwork(ctx context.Context, req NetworkRequest) error {
	if req.ContainerID == "" {
		return ErrInvalidArgument
	}
	if req.JoinTarget.PID == 0 {
		task, taskCtx, err := s.getTask(ctx, req.ContainerID)
		if err != nil {
			return mapContainerdErr(err)
		}
		ctx = taskCtx
		req.JoinTarget.PID = task.Pid()
	}
	return mapContainerdErr(s.checkNetwork(ctx, req))
}

func (s *DefaultService) withNamespace(ctx context.Context) context.Context {
	return namespaces.WithNamespace(ctx, s.namespace)
}

func (*DefaultService) ResolveRemoteDigest(ctx context.Context, ref string) (string, error) {
	if ref == "" {
		return "", ErrInvalidArgument
	}
	ref = config.NormalizeImageRef(ref)
	resolver := docker.NewResolver(docker.ResolverOptions{
		Hosts: docker.ConfigureDefaultRegistries(),
	})
	_, desc, err := resolver.Resolve(ctx, ref)
	if err != nil {
		return "", mapContainerdErr(err)
	}
	return desc.Digest.String(), nil
}

func toImageInfo(img containerd.Image) ImageInfo {
	return ImageInfo{
		Name: img.Name(),
		ID:   img.Target().Digest.String(),
		Tags: []string{img.Name()},
	}
}

func toContainerInfo(ctx context.Context, c containerd.Container) (ContainerInfo, error) {
	info, err := c.Info(ctx)
	if err != nil {
		return ContainerInfo{}, err
	}
	return ContainerInfo{
		ID:         info.ID,
		Image:      info.Image,
		Labels:     info.Labels,
		StorageRef: StorageRef{Driver: info.Snapshotter, Key: info.SnapshotKey, Kind: "snapshot"},
		Runtime:    RuntimeInfo{Name: info.Runtime.Name},
		CreatedAt:  info.CreatedAt,
		UpdatedAt:  info.UpdatedAt,
	}, nil
}

func mapContainerdErr(err error) error {
	if err == nil {
		return nil
	}
	if errdefs.IsNotFound(err) {
		return errors.Join(ErrNotFound, err)
	}
	if errdefs.IsAlreadyExists(err) {
		return errors.Join(ErrAlreadyExists, err)
	}
	return errors.Join(ErrRuntime, err)
}

func convertTaskStatus(s containerd.ProcessStatus) TaskStatus {
	switch s {
	case containerd.Running:
		return TaskStatusRunning
	case containerd.Created:
		return TaskStatusCreated
	case containerd.Stopped:
		return TaskStatusStopped
	case containerd.Paused, containerd.Pausing:
		return TaskStatusPaused
	default:
		return TaskStatusUnknown
	}
}

func convertContainerdTaskStatus(s tasktypes.Status) TaskStatus {
	switch s {
	case tasktypes.Status_RUNNING:
		return TaskStatusRunning
	case tasktypes.Status_CREATED:
		return TaskStatusCreated
	case tasktypes.Status_STOPPED:
		return TaskStatusStopped
	case tasktypes.Status_PAUSED, tasktypes.Status_PAUSING:
		return TaskStatusPaused
	default:
		return TaskStatusUnknown
	}
}
