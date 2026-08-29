package docker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	dockercontainer "github.com/docker/docker/api/types/container"
	dockerimage "github.com/docker/docker/api/types/image"

	containerapi "github.com/felinics/memoh/internal/container"
)

type testStatusErr struct {
	code int
}

func (e testStatusErr) Error() string { return http.StatusText(e.code) }

func (e testStatusErr) StatusCode() int { return e.code }

func TestDockerSnapshotImageRefSanitizesRuntimeName(t *testing.T) {
	ref := dockerSnapshotImageRef("workspace/foo:snapshot@123")
	if !strings.HasPrefix(ref, snapshotImageRepository+":") {
		t.Fatalf("snapshot image ref = %q, want repo prefix", ref)
	}
	if strings.ContainsAny(strings.TrimPrefix(ref, snapshotImageRepository+":"), "/:@") {
		t.Fatalf("snapshot image ref contains invalid tag chars: %q", ref)
	}
}

func TestContainerInfoKeepsActiveStorageRefAsContainerID(t *testing.T) {
	info := containerInfoFromInspect(dockercontainer.InspectResponse{
		ContainerJSONBase: &dockercontainer.ContainerJSONBase{
			ID:      "docker-container-id",
			Name:    "/workspace-bot-1",
			Created: "2026-01-02T03:04:05Z",
		},
		Config: &dockercontainer.Config{
			Image: "debian:bookworm-slim",
			Labels: map[string]string{
				containerapi.StorageKeyLabel: "workspace-active-1",
			},
		},
	})
	if info.StorageRef.Key != "docker-container-id" {
		t.Fatalf("StorageRef.Key = %q, want container ID", info.StorageRef.Key)
	}
	if info.ID != "workspace-bot-1" {
		t.Fatalf("ID = %q, want container name", info.ID)
	}
	if info.StorageRef.Driver != "docker" {
		t.Fatalf("StorageRef.Driver = %q, want docker", info.StorageRef.Driver)
	}
	if info.StorageRef.Kind != "container" {
		t.Fatalf("StorageRef.Kind = %q, want container", info.StorageRef.Kind)
	}
	if info.Labels[containerapi.StorageKeyLabel] != "workspace-active-1" {
		t.Fatalf("storage label = %q, want workspace-active-1", info.Labels[containerapi.StorageKeyLabel])
	}
}

func TestActiveSnapshotFromContainer(t *testing.T) {
	t.Parallel()

	info := containerapi.ContainerInfo{
		StorageRef: containerapi.StorageRef{Driver: "docker", Key: "container-id", Kind: "container"},
		Labels: map[string]string{
			containerapi.StorageKeyLabel: "snapshot-parent",
		},
	}
	snapshot, ok := activeSnapshotFromContainer(info)
	if !ok {
		t.Fatal("activeSnapshotFromContainer() ok = false, want true")
	}
	if snapshot.Name != "container-id" {
		t.Fatalf("snapshot.Name = %q, want container-id", snapshot.Name)
	}
	if snapshot.Parent != "snapshot-parent" {
		t.Fatalf("snapshot.Parent = %q, want snapshot-parent", snapshot.Parent)
	}
	if snapshot.Kind != "active" {
		t.Fatalf("snapshot.Kind = %q, want active", snapshot.Kind)
	}
}

func TestActiveSnapshotFromContainerSkipsEmptyStorageKey(t *testing.T) {
	t.Parallel()

	_, ok := activeSnapshotFromContainer(containerapi.ContainerInfo{})
	if ok {
		t.Fatal("activeSnapshotFromContainer() ok = true, want false")
	}
}

func TestAppendImageSnapshotsIncludesPreparedTag(t *testing.T) {
	t.Parallel()

	out := appendImageSnapshots(nil, dockerimage.Summary{
		Created: 123,
		Labels: map[string]string{
			containerapi.StorageKeyLabel: "committed-snapshot",
			snapshotParentLabel:          "previous-snapshot",
		},
		RepoTags: []string{
			dockerSnapshotImageRef("committed-snapshot"),
			dockerSnapshotImageRef("prepared-active"),
			"debian:bookworm-slim",
		},
	})
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2", len(out))
	}
	if out[0].Name != "committed-snapshot" || out[0].Parent != "previous-snapshot" {
		t.Fatalf("committed snapshot = %#v", out[0])
	}
	if out[1].Name != "prepared-active" || out[1].Parent != "committed-snapshot" {
		t.Fatalf("prepared snapshot = %#v", out[1])
	}
}

func TestMapDockerErrMapsConflictToAlreadyExists(t *testing.T) {
	err := mapDockerErr(testStatusErr{code: http.StatusConflict})
	if !containerapi.IsAlreadyExists(err) {
		t.Fatalf("mapDockerErr conflict = %v, want already exists", err)
	}

	err = mapDockerErr(errors.New("Conflict. The container name is already in use"))
	if !containerapi.IsAlreadyExists(err) {
		t.Fatalf("mapDockerErr text conflict = %v, want already exists", err)
	}
}

func TestDockerDoesNotExposeHostSnapshotCapabilities(t *testing.T) {
	type snapshotMountProvider interface {
		SnapshotMounts(context.Context, string, string) ([]containerapi.MountInfo, error)
	}
	var svc any = &Service{}
	if _, ok := svc.(snapshotMountProvider); ok {
		t.Fatal("docker service should not expose host-side snapshot mounts")
	}
}

func TestBridgeTargetPrefersPublishedHostPort(t *testing.T) {
	var settings dockercontainer.NetworkSettings
	if err := json.Unmarshal([]byte(`{"Ports":{"9090/tcp":[{"HostIp":"127.0.0.1","HostPort":"49153"}]}}`), &settings); err != nil {
		t.Fatalf("unmarshal network settings: %v", err)
	}
	info := dockercontainer.InspectResponse{
		NetworkSettings: &settings,
	}
	if got, want := firstHostPort(info, bridgeTCPPort), "127.0.0.1:49153"; got != want {
		t.Fatalf("firstHostPort = %q, want %q", got, want)
	}
}
