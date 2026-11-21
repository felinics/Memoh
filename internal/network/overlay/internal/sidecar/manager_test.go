package sidecar

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	ctr "github.com/felinics/memoh/internal/container"
	netctl "github.com/felinics/memoh/internal/network"
)

type fakeRuntime struct {
	containers map[string]ctr.CreateContainerRequest
	running    map[string]bool
	starts     int
	// onStart observes the resolv.conf content at the moment the task starts.
	onStart func()
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{containers: map[string]ctr.CreateContainerRequest{}, running: map[string]bool{}}
}

func (f *fakeRuntime) CreateContainer(_ context.Context, req ctr.CreateContainerRequest) (ctr.ContainerInfo, error) {
	f.containers[req.ID] = req
	return ctr.ContainerInfo{ID: req.ID, Labels: req.Labels}, nil
}

func (f *fakeRuntime) GetContainer(_ context.Context, id string) (ctr.ContainerInfo, error) {
	req, ok := f.containers[id]
	if !ok {
		return ctr.ContainerInfo{}, ctr.ErrNotFound
	}
	return ctr.ContainerInfo{ID: id, Labels: req.Labels}, nil
}

func (f *fakeRuntime) DeleteContainer(_ context.Context, id string, _ *ctr.DeleteContainerOptions) error {
	delete(f.containers, id)
	delete(f.running, id)
	return nil
}

func (f *fakeRuntime) StartContainer(_ context.Context, id string, _ *ctr.StartTaskOptions) error {
	if f.onStart != nil {
		f.onStart()
	}
	f.starts++
	f.running[id] = true
	return nil
}

func (f *fakeRuntime) StopContainer(_ context.Context, id string, _ *ctr.StopTaskOptions) error {
	f.running[id] = false
	return nil
}

func (*fakeRuntime) DeleteTask(context.Context, string, *ctr.DeleteTaskOptions) error {
	return nil
}

func (f *fakeRuntime) GetTaskInfo(_ context.Context, id string) (ctr.TaskInfo, error) {
	if _, ok := f.containers[id]; !ok {
		return ctr.TaskInfo{}, ctr.ErrNotFound
	}
	if f.running[id] {
		return ctr.TaskInfo{ContainerID: id, Status: ctr.TaskStatusRunning}, nil
	}
	return ctr.TaskInfo{ContainerID: id, Status: ctr.TaskStatusStopped}, nil
}

func testRequest() netctl.AttachmentRequest {
	return netctl.AttachmentRequest{
		BotID:       "bot-1",
		ContainerID: "workspace-bot-1",
		Overlay:     netctl.BotOverlayConfig{Enabled: true, Provider: "test"},
		Runtime: netctl.RuntimeNetworkRequest{
			JoinTarget: netctl.NetworkJoinTarget{Kind: "netns", Path: "/proc/1/ns/net"},
		},
	}
}

func hostResolvConf(t *testing.T, dataDir string) []byte {
	t.Helper()
	source, err := ctr.ResolveConfSource(dataDir)
	if err != nil {
		t.Fatalf("ResolveConfSource: %v", err)
	}
	data, err := os.ReadFile(source) //nolint:gosec // test reads the resolver config chosen by the code under test.
	if err != nil {
		t.Fatalf("read resolver source: %v", err)
	}
	return data
}

func TestEnsureAttachedGivesSidecarWritableResolvConf(t *testing.T) {
	dataDir := t.TempDir()
	resolvPath := filepath.Join(t.TempDir(), "resolv.conf")
	runtime := newFakeRuntime()
	var atStart []byte
	runtime.onStart = func() {
		atStart, _ = os.ReadFile(resolvPath) //nolint:gosec // test-owned temp path.
	}
	m := Manager{
		Kind:    "test",
		Runtime: runtime,
		DataDir: dataDir,
		BuildSpec: func(netctl.AttachmentRequest) (Spec, error) {
			return Spec{Image: "img", ResolvConfPath: resolvPath}, nil
		},
	}

	if _, err := m.EnsureAttached(context.Background(), testRequest()); err != nil {
		t.Fatalf("EnsureAttached: %v", err)
	}

	created := runtime.containers[m.SidecarID(testRequest())]
	var mount *ctr.MountSpec
	for i := range created.Spec.Mounts {
		if created.Spec.Mounts[i].Destination == "/etc/resolv.conf" {
			mount = &created.Spec.Mounts[i]
		}
	}
	if mount == nil {
		t.Fatalf("sidecar has no /etc/resolv.conf mount: %+v", created.Spec.Mounts)
	}
	if mount.Source != resolvPath || mount.Options[len(mount.Options)-1] != "rw" {
		t.Fatalf("unexpected resolv.conf mount: %+v", *mount)
	}
	if want := hostResolvConf(t, dataDir); string(atStart) != string(want) {
		t.Fatalf("resolv.conf at task start = %q, want host resolver config %q", atStart, want)
	}
}

func TestEnsureAttachedKeepsResolvConfOfRunningSidecar(t *testing.T) {
	resolvPath := filepath.Join(t.TempDir(), "resolv.conf")
	runtime := newFakeRuntime()
	m := Manager{
		Kind:    "test",
		Runtime: runtime,
		DataDir: t.TempDir(),
		BuildSpec: func(netctl.AttachmentRequest) (Spec, error) {
			return Spec{Image: "img", ResolvConfPath: resolvPath}, nil
		},
	}
	if _, err := m.EnsureAttached(context.Background(), testRequest()); err != nil {
		t.Fatalf("EnsureAttached: %v", err)
	}
	clientDNS := []byte("nameserver 100.100.100.100\n")
	if err := os.WriteFile(resolvPath, clientDNS, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := m.EnsureAttached(context.Background(), testRequest()); err != nil {
		t.Fatalf("EnsureAttached: %v", err)
	}
	if _, err := m.Status(context.Background(), testRequest()); err != nil {
		t.Fatalf("Status: %v", err)
	}

	got, err := os.ReadFile(resolvPath) //nolint:gosec // test-owned temp path.
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(clientDNS) {
		t.Fatalf("running sidecar resolv.conf was overwritten: %q", got)
	}
	if runtime.starts != 1 {
		t.Fatalf("running sidecar was restarted %d times", runtime.starts-1)
	}
}

func TestConfigHashChangesWithMounts(t *testing.T) {
	base := Spec{Image: "img"}
	withMount := Spec{Image: "img", Mounts: []ctr.MountSpec{{Destination: "/var/lib/x", Type: "bind", Source: "/data/x", Options: []string{"rbind", "rw"}}}}
	withResolv := Spec{Image: "img", ResolvConfPath: "/data/resolv.conf"}

	if ConfigHash(base, "/proc/1/ns/net") == ConfigHash(withMount, "/proc/1/ns/net") {
		t.Fatal("adding a mount must change the config hash")
	}
	if ConfigHash(base, "/proc/1/ns/net") == ConfigHash(withResolv, "/proc/1/ns/net") {
		t.Fatal("adding a resolv.conf path must change the config hash")
	}
}
