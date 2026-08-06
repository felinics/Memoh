package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	dockermount "github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"

	"github.com/memohai/memoh/internal/config"
	containerapi "github.com/memohai/memoh/internal/container"
)

const (
	snapshotImageRepository = "memoh-workspace-snapshot"
	snapshotParentLabel     = "memoh.snapshot_parent"
	bridgeTCPPort           = "9090"
	workspaceContainerPref  = "workspace-"
)

var invalidSnapshotTagChars = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

type Service struct {
	client *client.Client
	logger *slog.Logger
}

func NewService(log *slog.Logger, cfg config.Config) (*Service, error) {
	if log == nil {
		log = slog.Default()
	}
	opts := []client.Opt{client.FromEnv, client.WithAPIVersionNegotiation()}
	if host := strings.TrimSpace(cfg.Docker.Host); host != "" {
		opts = append(opts, client.WithHost(host))
	}
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}
	return &Service{
		client: cli,
		logger: log.With(slog.String("service", "docker")),
	}, nil
}

func (s *Service) Close() error {
	return s.client.Close()
}

func (s *Service) PullImage(ctx context.Context, ref string, opts *containerapi.PullImageOptions) (containerapi.ImageInfo, error) {
	ref = config.NormalizeImageRef(strings.TrimSpace(ref))
	if ref == "" {
		return containerapi.ImageInfo{}, containerapi.ErrInvalidArgument
	}
	reader, err := s.client.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return containerapi.ImageInfo{}, mapDockerErr(err)
	}
	defer func() { _ = reader.Close() }()
	if opts != nil && opts.OnProgress != nil {
		_ = decodePullProgress(reader, opts.OnProgress)
	} else {
		_, _ = io.Copy(io.Discard, reader)
	}
	return s.GetImage(ctx, ref)
}

func (s *Service) GetImage(ctx context.Context, ref string) (containerapi.ImageInfo, error) {
	ref = config.NormalizeImageRef(strings.TrimSpace(ref))
	if ref == "" {
		return containerapi.ImageInfo{}, containerapi.ErrInvalidArgument
	}
	info, err := s.client.ImageInspect(ctx, ref)
	if err != nil {
		return containerapi.ImageInfo{}, mapDockerErr(err)
	}
	return imageInfoFromInspect(ref, info), nil
}

func (s *Service) ListImages(ctx context.Context) ([]containerapi.ImageInfo, error) {
	images, err := s.client.ImageList(ctx, image.ListOptions{All: true})
	if err != nil {
		return nil, mapDockerErr(err)
	}
	out := make([]containerapi.ImageInfo, len(images))
	for i, img := range images {
		out[i] = imageInfoFromSummary(img)
	}
	return out, nil
}

func (s *Service) DeleteImage(ctx context.Context, ref string, _ *containerapi.DeleteImageOptions) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return containerapi.ErrInvalidArgument
	}
	_, err := s.client.ImageRemove(ctx, ref, image.RemoveOptions{Force: true, PruneChildren: true})
	if err != nil {
		normalized := config.NormalizeImageRef(ref)
		if normalized != ref && containerapi.IsNotFound(mapDockerErr(err)) {
			_, err = s.client.ImageRemove(ctx, normalized, image.RemoveOptions{Force: true, PruneChildren: true})
		}
	}
	return mapDockerErr(err)
}

func (*Service) ResolveRemoteDigest(context.Context, string) (string, error) {
	return "", containerapi.ErrNotSupported
}

func (s *Service) CreateContainer(ctx context.Context, req containerapi.CreateContainerRequest) (containerapi.ContainerInfo, error) {
	if strings.TrimSpace(req.ID) == "" || strings.TrimSpace(req.ImageRef) == "" {
		return containerapi.ContainerInfo{}, containerapi.ErrInvalidArgument
	}
	req.ImageRef = config.NormalizeImageRef(req.ImageRef)
	labels := cloneLabels(req.Labels)
	if req.StorageRef.Key != "" {
		labels[containerapi.StorageKeyLabel] = req.StorageRef.Key
	}
	hostCfg := &container.HostConfig{
		Mounts: toDockerMounts(req.Spec.Mounts),
		DNS:    req.Spec.DNS,
		PortBindings: nat.PortMap{
			nat.Port(bridgeTCPPort + "/tcp"): []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: ""}},
		},
	}
	if req.ResourceLimits.MemoryBytes > 0 {
		hostCfg.Memory = req.ResourceLimits.MemoryBytes
	}
	if req.ResourceLimits.CPUMillicores > 0 {
		hostCfg.NanoCPUs = req.ResourceLimits.CPUMillicores * 1_000_000
	}
	if req.Spec.NetworkJoinTarget.Value != "" {
		hostCfg.NetworkMode = container.NetworkMode("none")
	}
	cfg := &container.Config{
		Image:      req.ImageRef,
		Cmd:        req.Spec.Cmd,
		Env:        upsertEnv(req.Spec.Env, "BRIDGE_TCP_ADDR", ":"+bridgeTCPPort),
		WorkingDir: req.Spec.WorkDir,
		User:       req.Spec.User,
		Tty:        req.Spec.TTY,
		Labels:     labels,
		ExposedPorts: nat.PortSet{
			nat.Port(bridgeTCPPort + "/tcp"): struct{}{},
		},
	}
	resp, err := s.client.ContainerCreate(ctx, cfg, hostCfg, nil, nil, req.ID)
	if err != nil {
		return containerapi.ContainerInfo{}, mapDockerErr(err)
	}
	return s.GetContainer(ctx, resp.ID)
}

func (s *Service) BridgeTarget(botID string) string {
	if strings.TrimSpace(botID) == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	info, err := s.client.ContainerInspect(ctx, workspaceContainerPref+strings.TrimSpace(botID))
	if err != nil {
		return ""
	}
	if host := firstHostPort(info, bridgeTCPPort); host != "" {
		return host
	}
	ip := firstContainerIP(info)
	if ip == "" {
		return ""
	}
	return net.JoinHostPort(ip, bridgeTCPPort)
}

func firstHostPort(info container.InspectResponse, port string) string {
	if info.NetworkSettings == nil {
		return ""
	}
	for _, binding := range info.NetworkSettings.Ports[nat.Port(port+"/tcp")] {
		hostIP := strings.TrimSpace(binding.HostIP)
		hostPort := strings.TrimSpace(binding.HostPort)
		if hostPort == "" {
			continue
		}
		if hostIP == "" || hostIP == "0.0.0.0" || hostIP == "::" {
			hostIP = "127.0.0.1"
		}
		return net.JoinHostPort(hostIP, hostPort)
	}
	return ""
}

func (s *Service) GetContainer(ctx context.Context, id string) (containerapi.ContainerInfo, error) {
	if strings.TrimSpace(id) == "" {
		return containerapi.ContainerInfo{}, containerapi.ErrInvalidArgument
	}
	info, err := s.client.ContainerInspect(ctx, id)
	if err != nil {
		return containerapi.ContainerInfo{}, mapDockerErr(err)
	}
	return containerInfoFromInspect(info), nil
}

func (s *Service) ListContainers(ctx context.Context) ([]containerapi.ContainerInfo, error) {
	items, err := s.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, mapDockerErr(err)
	}
	out := make([]containerapi.ContainerInfo, len(items))
	for i, item := range items {
		out[i] = containerInfoFromSummary(item)
	}
	return out, nil
}

func (s *Service) DeleteContainer(ctx context.Context, id string, opts *containerapi.DeleteContainerOptions) error {
	if strings.TrimSpace(id) == "" {
		return containerapi.ErrInvalidArgument
	}
	removeVolumes := opts != nil && opts.CleanupSnapshot
	err := s.client.ContainerRemove(ctx, id, container.RemoveOptions{Force: true, RemoveVolumes: removeVolumes})
	return mapDockerErr(err)
}

func (s *Service) ListContainersByLabel(ctx context.Context, key, value string) ([]containerapi.ContainerInfo, error) {
	if strings.TrimSpace(key) == "" {
		return nil, containerapi.ErrInvalidArgument
	}
	label := strings.TrimSpace(key)
	if strings.TrimSpace(value) != "" {
		label += "=" + strings.TrimSpace(value)
	}
	items, err := s.client.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", label)),
	})
	if err != nil {
		return nil, mapDockerErr(err)
	}
	out := make([]containerapi.ContainerInfo, len(items))
	for i, item := range items {
		out[i] = containerInfoFromSummary(item)
	}
	return out, nil
}

func (s *Service) RestoreContainer(ctx context.Context, req containerapi.CreateContainerRequest) (containerapi.ContainerInfo, error) {
	if strings.TrimSpace(req.ID) == "" || strings.TrimSpace(req.StorageRef.Key) == "" {
		return containerapi.ContainerInfo{}, containerapi.ErrInvalidArgument
	}
	req.ImageRef = dockerSnapshotImageRef(req.StorageRef.Key)
	return s.CreateContainer(ctx, req)
}

func (s *Service) StartContainer(ctx context.Context, id string, _ *containerapi.StartTaskOptions) error {
	if strings.TrimSpace(id) == "" {
		return containerapi.ErrInvalidArgument
	}
	return mapDockerErr(s.client.ContainerStart(ctx, id, container.StartOptions{}))
}

func (s *Service) StopContainer(ctx context.Context, id string, opts *containerapi.StopTaskOptions) error {
	if strings.TrimSpace(id) == "" {
		return containerapi.ErrInvalidArgument
	}
	stopOpts := container.StopOptions{}
	if opts != nil {
		if opts.Signal != 0 {
			stopOpts.Signal = dockerSignalName(opts.Signal)
		}
		if opts.Timeout > 0 {
			seconds := int(opts.Timeout.Seconds())
			if seconds == 0 {
				seconds = 1
			}
			stopOpts.Timeout = &seconds
		}
	}
	err := s.client.ContainerStop(ctx, id, stopOpts)
	if err == nil || opts == nil || !opts.Force {
		return mapDockerErr(err)
	}
	if killErr := s.client.ContainerKill(ctx, id, "SIGKILL"); killErr != nil {
		return mapDockerErr(killErr)
	}
	return nil
}

func (s *Service) DeleteTask(ctx context.Context, id string, opts *containerapi.DeleteTaskOptions) error {
	info, err := s.client.ContainerInspect(ctx, id)
	if err != nil {
		return mapDockerErr(err)
	}
	if info.State == nil || !info.State.Running {
		return nil
	}
	if opts != nil && opts.Force {
		return mapDockerErr(s.client.ContainerKill(ctx, id, "SIGKILL"))
	}
	return s.StopContainer(ctx, id, nil)
}

func (s *Service) GetTaskInfo(ctx context.Context, id string) (containerapi.TaskInfo, error) {
	info, err := s.client.ContainerInspect(ctx, id)
	if err != nil {
		return containerapi.TaskInfo{}, mapDockerErr(err)
	}
	return taskInfoFromInspect(info), nil
}

func (s *Service) GetContainerMetrics(ctx context.Context, id string) (containerapi.ContainerMetrics, error) {
	stats, err := s.client.ContainerStatsOneShot(ctx, id)
	if err != nil {
		return containerapi.ContainerMetrics{}, mapDockerErr(err)
	}
	defer func() { _ = stats.Body.Close() }()
	var payload container.StatsResponse
	if err := json.NewDecoder(stats.Body).Decode(&payload); err != nil {
		return containerapi.ContainerMetrics{}, errors.Join(containerapi.ErrRuntime, err)
	}
	return metricsFromStats(payload), nil
}

func (s *Service) ListTasks(ctx context.Context, _ *containerapi.ListTasksOptions) ([]containerapi.TaskInfo, error) {
	items, err := s.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, mapDockerErr(err)
	}
	out := make([]containerapi.TaskInfo, 0, len(items))
	for _, item := range items {
		out = append(out, taskInfoFromSummary(item))
	}
	return out, nil
}

func (s *Service) SetupNetwork(ctx context.Context, req containerapi.NetworkRequest) (containerapi.NetworkResult, error) {
	info, err := s.client.ContainerInspect(ctx, req.ContainerID)
	if err != nil {
		return containerapi.NetworkResult{}, mapDockerErr(err)
	}
	return containerapi.NetworkResult{IP: firstContainerIP(info)}, nil
}

func (*Service) RemoveNetwork(context.Context, containerapi.NetworkRequest) error {
	return nil
}

func (s *Service) CheckNetwork(ctx context.Context, req containerapi.NetworkRequest) error {
	_, err := s.client.ContainerInspect(ctx, req.ContainerID)
	return mapDockerErr(err)
}

func (s *Service) CommitSnapshot(ctx context.Context, req containerapi.CommitSnapshotRequest) error {
	snapshotter := strings.TrimSpace(req.Source.Driver)
	name := strings.TrimSpace(req.Target.Key)
	key := strings.TrimSpace(req.Source.Key)
	if strings.TrimSpace(name) == "" || strings.TrimSpace(key) == "" {
		return containerapi.ErrInvalidArgument
	}
	if strings.TrimSpace(snapshotter) != "" && strings.TrimSpace(snapshotter) != "docker" {
		return containerapi.ErrNotSupported
	}
	info, err := s.client.ContainerInspect(ctx, key)
	if err != nil {
		return mapDockerErr(err)
	}
	parent := ""
	if info.Config != nil {
		parent = strings.TrimSpace(info.Config.Labels[containerapi.StorageKeyLabel])
	}
	_, err = s.client.ContainerCommit(ctx, key, container.CommitOptions{
		Reference: dockerSnapshotImageRef(name),
		Comment:   "memoh workspace snapshot",
		Config: &container.Config{
			Labels: map[string]string{
				containerapi.StorageKeyLabel: name,
				snapshotParentLabel:          parent,
			},
		},
	})
	return mapDockerErr(err)
}

func (s *Service) ListSnapshots(ctx context.Context, req containerapi.ListSnapshotsRequest) ([]containerapi.SnapshotInfo, error) {
	snapshotter := strings.TrimSpace(req.Driver)
	if strings.TrimSpace(snapshotter) != "" && strings.TrimSpace(snapshotter) != "docker" {
		return nil, containerapi.ErrNotSupported
	}
	images, err := s.client.ImageList(ctx, image.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", containerapi.StorageKeyLabel)),
	})
	if err != nil {
		return nil, mapDockerErr(err)
	}
	out := make([]containerapi.SnapshotInfo, 0, len(images))
	for _, img := range images {
		out = appendImageSnapshots(out, img)
	}
	containers, err := s.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, mapDockerErr(err)
	}
	for _, item := range containers {
		info := containerInfoFromSummary(item)
		if snapshot, ok := activeSnapshotFromContainer(info); ok {
			out = append(out, snapshot)
		}
	}
	return out, nil
}

func appendImageSnapshots(out []containerapi.SnapshotInfo, img image.Summary) []containerapi.SnapshotInfo {
	name := strings.TrimSpace(img.Labels[containerapi.StorageKeyLabel])
	created := time.Unix(img.Created, 0)
	labels := cloneLabels(img.Labels)
	if name != "" {
		out = append(out, containerapi.SnapshotInfo{
			Name:    name,
			Parent:  strings.TrimSpace(img.Labels[snapshotParentLabel]),
			Kind:    "committed",
			Created: created,
			Updated: created,
			Labels:  labels,
		})
	}
	for _, tag := range img.RepoTags {
		tagName, ok := snapshotNameFromImageRef(tag)
		if !ok || tagName == "" || tagName == name {
			continue
		}
		out = append(out, containerapi.SnapshotInfo{
			Name:    tagName,
			Parent:  name,
			Kind:    "committed",
			Created: created,
			Updated: created,
			Labels:  labels,
		})
	}
	return out
}

func snapshotNameFromImageRef(ref string) (string, bool) {
	name := strings.TrimSpace(ref)
	prefix := snapshotImageRepository + ":"
	if !strings.HasPrefix(name, prefix) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(name, prefix)), true
}

func activeSnapshotFromContainer(info containerapi.ContainerInfo) (containerapi.SnapshotInfo, bool) {
	name := strings.TrimSpace(info.StorageRef.Key)
	if name == "" {
		return containerapi.SnapshotInfo{}, false
	}
	parent := strings.TrimSpace(info.Labels[containerapi.StorageKeyLabel])
	if parent == name {
		parent = ""
	}
	return containerapi.SnapshotInfo{
		Name:    name,
		Parent:  parent,
		Kind:    "active",
		Created: info.CreatedAt,
		Updated: info.UpdatedAt,
		Labels:  cloneLabels(info.Labels),
	}, true
}

func (s *Service) PrepareSnapshot(ctx context.Context, req containerapi.PrepareSnapshotRequest) error {
	snapshotter := strings.TrimSpace(req.Target.Driver)
	key := strings.TrimSpace(req.Target.Key)
	parent := strings.TrimSpace(req.Parent.Key)
	if strings.TrimSpace(key) == "" || strings.TrimSpace(parent) == "" {
		return containerapi.ErrInvalidArgument
	}
	if strings.TrimSpace(snapshotter) != "" && strings.TrimSpace(snapshotter) != "docker" {
		return containerapi.ErrNotSupported
	}
	target := dockerSnapshotImageRef(key)
	if err := s.client.ImageTag(ctx, dockerSnapshotImageRef(parent), target); err != nil {
		return mapDockerErr(err)
	}
	return nil
}

func toDockerMounts(in []containerapi.MountSpec) []dockermount.Mount {
	out := make([]dockermount.Mount, 0, len(in))
	for _, m := range in {
		if strings.TrimSpace(m.Source) == "" || strings.TrimSpace(m.Destination) == "" {
			continue
		}
		out = append(out, dockermount.Mount{
			Type:     dockerMountType(m.Type),
			Source:   m.Source,
			Target:   m.Destination,
			ReadOnly: hasReadonlyOption(m.Options),
		})
	}
	return out
}

func dockerMountType(raw string) dockermount.Type {
	switch strings.TrimSpace(raw) {
	case "volume":
		return dockermount.TypeVolume
	case "tmpfs":
		return dockermount.TypeTmpfs
	default:
		return dockermount.TypeBind
	}
}

func hasReadonlyOption(options []string) bool {
	for _, opt := range options {
		switch strings.TrimSpace(opt) {
		case "ro", "readonly":
			return true
		}
	}
	return false
}

func cloneLabels(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func upsertEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := append([]string(nil), env...)
	for i, item := range out {
		if strings.HasPrefix(item, prefix) {
			out[i] = prefix + value
			return out
		}
	}
	return append(out, prefix+value)
}

func dockerSnapshotImageRef(name string) string {
	tag := invalidSnapshotTagChars.ReplaceAllString(strings.TrimSpace(name), "-")
	tag = strings.Trim(tag, ".-")
	if tag == "" {
		tag = "snapshot"
	}
	if len(tag) > 128 {
		tag = tag[:128]
		tag = strings.TrimRight(tag, ".-")
	}
	return snapshotImageRepository + ":" + tag
}

func imageInfoFromInspect(ref string, info image.InspectResponse) containerapi.ImageInfo {
	tags := append([]string(nil), info.RepoTags...)
	name := ref
	if len(tags) > 0 {
		name = tags[0]
	}
	return containerapi.ImageInfo{Name: name, ID: info.ID, Tags: tags}
}

func imageInfoFromSummary(info image.Summary) containerapi.ImageInfo {
	name := info.ID
	if len(info.RepoTags) > 0 {
		name = info.RepoTags[0]
	}
	return containerapi.ImageInfo{Name: name, ID: info.ID, Tags: append([]string(nil), info.RepoTags...)}
}

func containerInfoFromInspect(info container.InspectResponse) containerapi.ContainerInfo {
	created, _ := time.Parse(time.RFC3339Nano, info.Created)
	imageRef := info.Image
	if info.Config != nil && strings.TrimSpace(info.Config.Image) != "" {
		imageRef = info.Config.Image
	}
	labels := map[string]string(nil)
	if info.Config != nil {
		labels = info.Config.Labels
	}
	id := strings.TrimPrefix(strings.TrimSpace(info.Name), "/")
	if id == "" {
		id = info.ID
	}
	return containerapi.ContainerInfo{
		ID:         id,
		Image:      imageRef,
		Labels:     labels,
		StorageRef: containerapi.StorageRef{Driver: "docker", Key: info.ID, Kind: "container"},
		Runtime:    containerapi.RuntimeInfo{Name: "docker"},
		CreatedAt:  created,
		UpdatedAt:  created,
	}
}

func containerInfoFromSummary(info container.Summary) containerapi.ContainerInfo {
	id := info.ID
	for _, name := range info.Names {
		name = strings.TrimPrefix(strings.TrimSpace(name), "/")
		if name != "" {
			id = name
			break
		}
	}
	return containerapi.ContainerInfo{
		ID:         id,
		Image:      info.Image,
		Labels:     info.Labels,
		StorageRef: containerapi.StorageRef{Driver: "docker", Key: info.ID, Kind: "container"},
		Runtime:    containerapi.RuntimeInfo{Name: "docker"},
		CreatedAt:  time.Unix(info.Created, 0),
		UpdatedAt:  time.Unix(info.Created, 0),
	}
}

func taskInfoFromInspect(info container.InspectResponse) containerapi.TaskInfo {
	task := containerapi.TaskInfo{ContainerID: info.ID, ID: info.ID, Status: containerapi.TaskStatusUnknown}
	if info.State == nil {
		return task
	}
	if info.State.Pid > 0 {
		task.PID = uint32(info.State.Pid) //nolint:gosec // Docker PIDs are non-negative here
	}
	if info.State.ExitCode > 0 {
		task.ExitCode = uint32(info.State.ExitCode) //nolint:gosec // Docker exit codes are small non-negative values
	}
	task.Status = taskStatusFromDocker(info.State.Status, info.State.Running, info.State.Paused)
	return task
}

func taskInfoFromSummary(info container.Summary) containerapi.TaskInfo {
	return containerapi.TaskInfo{
		ContainerID: info.ID,
		ID:          info.ID,
		Status:      taskStatusFromDocker(info.State, false, false),
	}
}

func taskStatusFromDocker(status container.ContainerState, running, paused bool) containerapi.TaskStatus {
	if running {
		return containerapi.TaskStatusRunning
	}
	if paused {
		return containerapi.TaskStatusPaused
	}
	switch strings.ToLower(status) {
	case "created":
		return containerapi.TaskStatusCreated
	case "running":
		return containerapi.TaskStatusRunning
	case "paused":
		return containerapi.TaskStatusPaused
	case "exited", "dead", "removing":
		return containerapi.TaskStatusStopped
	default:
		return containerapi.TaskStatusUnknown
	}
}

func metricsFromStats(stats container.StatsResponse) containerapi.ContainerMetrics {
	totalUsage := stats.CPUStats.CPUUsage.TotalUsage
	memoryLimit := normalizeDockerMemoryLimit(stats.MemoryStats.Limit)
	memory := &containerapi.MemoryMetrics{
		UsageBytes: stats.MemoryStats.Usage,
		LimitBytes: memoryLimit,
	}
	if memoryLimit > 0 {
		memory.UsagePercent = (float64(memory.UsageBytes) / float64(memoryLimit)) * 100
	}
	return containerapi.ContainerMetrics{
		SampledAt: stats.Read,
		CPU: &containerapi.CPUMetrics{
			UsageNanoseconds:  totalUsage,
			UserNanoseconds:   stats.CPUStats.CPUUsage.UsageInUsermode,
			KernelNanoseconds: stats.CPUStats.CPUUsage.UsageInKernelmode,
			UsagePercent:      dockerCPUPercent(stats),
		},
		Memory: memory,
	}
}

func dockerCPUPercent(stats container.StatsResponse) float64 {
	cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage - stats.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(stats.CPUStats.SystemUsage - stats.PreCPUStats.SystemUsage)
	if cpuDelta <= 0 || systemDelta <= 0 {
		return 0
	}
	onlineCPUs := float64(stats.CPUStats.OnlineCPUs)
	if onlineCPUs == 0 {
		onlineCPUs = float64(len(stats.CPUStats.CPUUsage.PercpuUsage))
	}
	if onlineCPUs == 0 {
		onlineCPUs = 1
	}
	return (cpuDelta / systemDelta) * onlineCPUs * 100
}

func normalizeDockerMemoryLimit(limit uint64) uint64 {
	if limit == 0 || limit > uint64(1)<<60 {
		return 0
	}
	return limit
}

func firstContainerIP(info container.InspectResponse) string {
	if info.NetworkSettings == nil {
		return ""
	}
	for _, network := range info.NetworkSettings.Networks {
		if strings.TrimSpace(network.IPAddress) != "" {
			return strings.TrimSpace(network.IPAddress)
		}
	}
	return ""
}

func mapDockerErr(err error) error {
	if err == nil {
		return nil
	}
	if errdefs.IsNotFound(err) {
		return errors.Join(containerapi.ErrNotFound, err)
	}
	if errdefs.IsAlreadyExists(err) || isDockerConflict(err) {
		return errors.Join(containerapi.ErrAlreadyExists, err)
	}
	if client.IsErrConnectionFailed(err) {
		return errors.Join(containerapi.ErrRuntime, fmt.Errorf("docker daemon unavailable: %w", err))
	}
	return errors.Join(containerapi.ErrRuntime, err)
}

type statusCoder interface {
	StatusCode() int
}

func isDockerConflict(err error) bool {
	var statusErr statusCoder
	if errors.As(err, &statusErr) && statusErr.StatusCode() == http.StatusConflict {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "conflict") ||
		strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "is already in use")
}

func dockerSignalName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGQUIT:
		return "SIGQUIT"
	default:
		return "SIGTERM"
	}
}

func decodePullProgress(reader io.Reader, onProgress func(containerapi.PullProgress)) error {
	decoder := json.NewDecoder(reader)
	var layers []containerapi.LayerStatus
	for decoder.More() {
		var event struct {
			ID             string `json:"id"`
			Status         string `json:"status"`
			ProgressDetail struct {
				Current int64 `json:"current"`
				Total   int64 `json:"total"`
			} `json:"progressDetail"`
		}
		if err := decoder.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if event.ID == "" {
			continue
		}
		found := false
		for i := range layers {
			if layers[i].Ref == event.ID {
				layers[i].Offset = event.ProgressDetail.Current
				layers[i].Total = event.ProgressDetail.Total
				found = true
				break
			}
		}
		if !found {
			layers = append(layers, containerapi.LayerStatus{
				Ref:    event.ID,
				Offset: event.ProgressDetail.Current,
				Total:  event.ProgressDetail.Total,
			})
		}
		onProgress(containerapi.PullProgress{Layers: append([]containerapi.LayerStatus(nil), layers...)})
	}
	return nil
}
