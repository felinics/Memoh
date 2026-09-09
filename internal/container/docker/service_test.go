package docker

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dockercontainer "github.com/docker/docker/api/types/container"
	dockerimage "github.com/docker/docker/api/types/image"
	dockernetwork "github.com/docker/docker/api/types/network"
	dockerclient "github.com/docker/docker/client"

	"github.com/felinics/memoh/internal/config"
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

func TestNewServiceRejectsNonUserDefinedDockerNetwork(t *testing.T) {
	for _, networkName := range []string{"bridge", "default", "host", "nat", "none", "container:server"} {
		t.Run(networkName, func(t *testing.T) {
			_, err := NewService(context.Background(), slog.New(slog.DiscardHandler), config.Config{
				Docker: config.DockerConfig{Network: networkName},
			})
			if err == nil {
				t.Fatalf("NewService() accepted Docker network %q", networkName)
			}
			if !strings.Contains(err.Error(), "user-defined network") {
				t.Fatalf("NewService() error = %q, want user-defined network guidance", err)
			}
		})
	}
}

func TestNewServiceRejectsWorkspaceNetworkForHostServer(t *testing.T) {
	if runningInContainer() {
		t.Skip("test requires a host process")
	}
	_, err := NewService(context.Background(), slog.New(slog.DiscardHandler), config.Config{
		Docker: config.DockerConfig{Network: "memoh-workspace"},
	})
	if err == nil {
		t.Fatal("NewService() accepted Docker workspace networking from a host process")
	}
	if !strings.Contains(err.Error(), "host deployments use the loopback workspace bridge") {
		t.Fatalf("NewService() error = %q, want host loopback guidance", err)
	}
}

func TestValidateWorkspaceNetworkRequiresStrictBridgeTLS(t *testing.T) {
	const networkName = "memoh-workspace"
	if err := validateWorkspaceNetwork(networkName, "server-id", true, false); err == nil || !strings.Contains(err.Error(), `[bridge_tls].mode = "strict"`) {
		t.Fatalf("validateWorkspaceNetwork() error = %v, want strict bridge TLS guidance", err)
	}
	if err := validateWorkspaceNetwork(networkName, "server-id", true, true); err != nil {
		t.Fatalf("validateWorkspaceNetwork() with strict bridge TLS error = %v", err)
	}
}

func TestValidateWorkspaceNetworkRequiresServerContainer(t *testing.T) {
	err := validateWorkspaceNetwork("memoh-workspace", "", true, true)
	if err == nil || !strings.Contains(err.Error(), "[docker].server_container") {
		t.Fatalf("validateWorkspaceNetwork() error = %v, want server container guidance", err)
	}
}

func TestValidateServerNetworkMembership(t *testing.T) {
	const networkName = "memoh-workspace"
	tests := []struct {
		name        string
		containerID string
		networks    map[string]*dockernetwork.EndpointSettings
		status      int
		wantError   string
		wantRuntime bool
	}{
		{
			name:        "attached with explicit identity",
			containerID: "server-id",
			networks:    map[string]*dockernetwork.EndpointSettings{networkName: {}},
			status:      http.StatusOK,
		},
		{
			name:        "configured container is detached",
			containerID: "server-id",
			networks:    map[string]*dockernetwork.EndpointSettings{"bridge": {}},
			status:      http.StatusOK,
			wantError:   `server container "server-id" is not attached to Docker network "memoh-workspace"`,
		},
		{
			name:        "configured container does not exist",
			containerID: "missing-server-id",
			status:      http.StatusNotFound,
			wantError:   `server container "missing-server-id" does not exist on Docker daemon`,
		},
		{
			name:        "daemon error",
			containerID: "server-id",
			status:      http.StatusInternalServerError,
			wantError:   `inspect Memoh server container "server-id" on Docker daemon`,
			wantRuntime: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			svc := newTestService(t, networkName, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/containers/"+test.containerID+"/json") {
					t.Errorf("unexpected Docker API request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
					return
				}
				if test.status != http.StatusOK {
					http.Error(w, http.StatusText(test.status), test.status)
					return
				}
				writeDockerJSON(t, w, dockercontainer.InspectResponse{
					NetworkSettings: &dockercontainer.NetworkSettings{Networks: test.networks},
				})
			})

			err := validateServerNetworkMembership(context.Background(), svc.client, test.containerID, networkName)
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("validateServerNetworkMembership() error = %v", err)
				}
				return
			}
			if !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("validateServerNetworkMembership() error = %v, want error containing %q", err, test.wantError)
			}
			if test.wantRuntime {
				if !errors.Is(err, containerapi.ErrRuntime) {
					t.Fatalf("validateServerNetworkMembership() error = %v, want ErrRuntime", err)
				}
			} else if !errors.Is(err, containerapi.ErrInvalidArgument) {
				t.Fatalf("validateServerNetworkMembership() error = %v, want ErrInvalidArgument", err)
			}
		})
	}
}

func TestValidateServerNetworkMembershipPreservesCancellation(t *testing.T) {
	svc := newTestService(t, "memoh-workspace", func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected Docker API request after cancellation: %s %s", r.Method, r.URL.Path)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := validateServerNetworkMembership(ctx, svc.client, "server-id", "memoh-workspace")
	if !errors.Is(err, context.Canceled) || !errors.Is(err, containerapi.ErrRuntime) {
		t.Fatalf("validateServerNetworkMembership() error = %v, want context.Canceled and ErrRuntime", err)
	}
}

func TestCreateContainerUsesWorkspaceNetworkWithoutPublishingBridge(t *testing.T) {
	var createRequest dockercontainer.CreateRequest
	svc := newTestService(t, "memoh-workspace", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/create"):
			if err := json.NewDecoder(r.Body).Decode(&createRequest); err != nil {
				t.Errorf("decode create request: %v", err)
			}
			writeDockerJSON(t, w, dockercontainer.CreateResponse{ID: "container-id"})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/container-id/json"):
			writeDockerJSON(t, w, dockercontainer.InspectResponse{
				ContainerJSONBase: &dockercontainer.ContainerJSONBase{ID: "container-id", Name: "/workspace-bot-1"},
				Config:            &dockercontainer.Config{Image: "debian:bookworm-slim"},
			})
		default:
			t.Errorf("unexpected Docker API request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})

	_, err := svc.CreateContainer(context.Background(), containerapi.CreateContainerRequest{
		ID:       "workspace-bot-1",
		ImageRef: "debian:bookworm-slim",
	})
	if err != nil {
		t.Fatalf("CreateContainer() error = %v", err)
	}
	if got := string(createRequest.HostConfig.NetworkMode); got != "memoh-workspace" {
		t.Fatalf("NetworkMode = %q, want memoh-workspace", got)
	}
	if len(createRequest.HostConfig.PortBindings) != 0 {
		t.Fatalf("PortBindings = %#v, want no host bridge publication", createRequest.HostConfig.PortBindings)
	}
	if createRequest.NetworkingConfig == nil || createRequest.NetworkingConfig.EndpointsConfig["memoh-workspace"] == nil {
		t.Fatalf("NetworkingConfig = %#v, want memoh-workspace endpoint", createRequest.NetworkingConfig)
	}
}

func TestBridgeTargetUsesWorkspaceContainerDNS(t *testing.T) {
	svc := newTestService(t, "memoh-workspace", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/containers/workspace-bot-1/json") {
			t.Errorf("unexpected Docker API request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		writeDockerJSON(t, w, dockercontainer.InspectResponse{
			NetworkSettings: &dockercontainer.NetworkSettings{
				Networks: map[string]*dockernetwork.EndpointSettings{
					"memoh-workspace": {IPAddress: "172.20.0.3"},
				},
			},
		})
	})

	if got, want := svc.BridgeTarget("bot-1"), "workspace-bot-1:9090"; got != want {
		t.Fatalf("BridgeTarget() = %q, want %q", got, want)
	}
}

func TestSetupNetworkAttachesExistingWorkspace(t *testing.T) {
	connected := false
	svc := newTestService(t, "memoh-workspace", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/workspace-bot-1/json"):
			networks := map[string]*dockernetwork.EndpointSettings{
				"bridge": {IPAddress: "172.17.0.2"},
			}
			if connected {
				networks["memoh-workspace"] = &dockernetwork.EndpointSettings{IPAddress: "172.20.0.4"}
			}
			writeDockerJSON(t, w, dockercontainer.InspectResponse{
				NetworkSettings: &dockercontainer.NetworkSettings{Networks: networks},
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/networks/memoh-workspace/connect"):
			var request struct {
				Container string `json:"Container"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode network connect request: %v", err)
			}
			if request.Container != "workspace-bot-1" {
				t.Errorf("network connect container = %q, want workspace-bot-1", request.Container)
			}
			connected = true
			writeDockerJSON(t, w, struct{}{})
		default:
			t.Errorf("unexpected Docker API request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})

	result, err := svc.SetupNetwork(context.Background(), containerapi.NetworkRequest{ContainerID: "workspace-bot-1"})
	if err != nil {
		t.Fatalf("SetupNetwork() error = %v", err)
	}
	if !connected {
		t.Fatal("SetupNetwork() did not connect the existing workspace")
	}
	if result.IP != "172.20.0.4" {
		t.Fatalf("SetupNetwork() IP = %q, want 172.20.0.4", result.IP)
	}
}

func TestRemoveNetworkDetachesWorkspaceIdempotently(t *testing.T) {
	connected := true
	disconnects := 0
	svc := newTestService(t, "memoh-workspace", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/workspace-bot-1/json"):
			networks := map[string]*dockernetwork.EndpointSettings{}
			if connected {
				networks["memoh-workspace"] = &dockernetwork.EndpointSettings{}
			}
			writeDockerJSON(t, w, dockercontainer.InspectResponse{
				NetworkSettings: &dockercontainer.NetworkSettings{Networks: networks},
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/networks/memoh-workspace/disconnect"):
			var request struct {
				Container string `json:"Container"`
				Force     bool   `json:"Force"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode network disconnect request: %v", err)
			}
			if request.Container != "workspace-bot-1" {
				t.Errorf("network disconnect container = %q, want workspace-bot-1", request.Container)
			}
			if request.Force {
				t.Error("network disconnect unexpectedly forced detachment")
			}
			disconnects++
			connected = false
			writeDockerJSON(t, w, struct{}{})
		default:
			t.Errorf("unexpected Docker API request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})

	request := containerapi.NetworkRequest{ContainerID: "workspace-bot-1"}
	if err := svc.RemoveNetwork(context.Background(), request); err != nil {
		t.Fatalf("RemoveNetwork() error = %v", err)
	}
	if err := svc.RemoveNetwork(context.Background(), request); err != nil {
		t.Fatalf("second RemoveNetwork() error = %v", err)
	}
	if disconnects != 1 {
		t.Fatalf("network disconnect calls = %d, want 1", disconnects)
	}
}

func TestDockerNetworkOperationsRejectEmptyContainerID(t *testing.T) {
	svc := &Service{}
	tests := map[string]func() error{
		"setup": func() error {
			_, err := svc.SetupNetwork(context.Background(), containerapi.NetworkRequest{})
			return err
		},
		"remove": func() error {
			return svc.RemoveNetwork(context.Background(), containerapi.NetworkRequest{})
		},
		"check": func() error {
			return svc.CheckNetwork(context.Background(), containerapi.NetworkRequest{})
		},
	}
	for name, operation := range tests {
		t.Run(name, func(t *testing.T) {
			if err := operation(); !errors.Is(err, containerapi.ErrInvalidArgument) {
				t.Fatalf("network operation error = %v, want ErrInvalidArgument", err)
			}
		})
	}
}

func newTestService(t *testing.T, workspaceNetwork string, handler http.HandlerFunc) *Service {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	cli, err := dockerclient.NewClientWithOpts(
		dockerclient.WithHost(server.URL),
		dockerclient.WithVersion("1.49"),
	)
	if err != nil {
		t.Fatalf("create Docker test client: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return &Service{
		client:           cli,
		logger:           slog.New(slog.DiscardHandler),
		workspaceNetwork: workspaceNetwork,
	}
}

func writeDockerJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode Docker API response: %v", err)
	}
}
