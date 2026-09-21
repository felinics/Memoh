package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/botworkspace"
	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/workspace"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// A client that stops reading must end the relay and release the await
// goroutine instead of holding both for the whole stream budget.
func TestStreamWorkspaceProvisioningStopsWhenClientDisconnects(t *testing.T) {
	events := make(chan botworkspace.ProgressEvent, 4)
	events <- botworkspace.ProgressEvent{Type: "pulling", Image: "debian:bookworm-slim"}

	awaitReleased := make(chan struct{})
	await := func(ctx context.Context) (botworkspace.Workspace, error) {
		<-ctx.Done()
		close(awaitReleased)
		return botworkspace.Workspace{}, ctx.Err()
	}

	errorSent := false
	outcome := streamWorkspaceProvisioning(
		context.Background(),
		func(any) bool { return false },
		events,
		await,
		"req-1",
		func(string, string, string) { errorSent = true },
	)

	if !outcome.Disconnected {
		t.Fatalf("outcome = %+v, want Disconnected", outcome)
	}
	if errorSent {
		t.Fatal("no error event should be written to a disconnected client")
	}
	select {
	case <-awaitReleased:
	case <-time.After(2 * time.Second):
		t.Fatal("await was not cancelled after the client disconnected")
	}
}

// restoreWorkspaceManager fakes the preserved-data archive: it exists until
// RestorePreservedData consumes it.
type restoreWorkspaceManager struct {
	containerWorkspace
	preserved  bool
	restored   int
	restoreErr error
}

func (m *restoreWorkspaceManager) HasPreservedData(string) bool { return m.preserved }

func (m *restoreWorkspaceManager) RestorePreservedData(context.Context, string) error {
	m.restored++
	if m.restoreErr != nil {
		return m.restoreErr
	}
	m.preserved = false
	return nil
}

func (m *restoreWorkspaceManager) GetContainerInfo(context.Context, string) (*workspace.ContainerStatus, error) {
	return &workspace.ContainerStatus{
		ContainerID:      "workspace-status",
		WorkspaceBackend: bridge.WorkspaceBackendContainer,
		RuntimeBackend:   "io.containerd.runc.v2",
		Image:            "debian:bookworm-slim",
		Snapshotter:      "overlayfs",
		HasPreservedData: m.preserved,
	}, nil
}

func newRestoreContainerHandler(t *testing.T, ownerID, botID string, manager containerWorkspace, ws workspaceIntents) *ContainerdHandler {
	t.Helper()
	return &ContainerdHandler{
		logger:         slog.Default(),
		manager:        manager,
		cfg:            config.WorkspaceConfig{Snapshotter: "overlayfs"},
		workspaces:     ws,
		botService:     bots.NewService(nil, postgresstore.NewQueries(sqlc.New(&createBotStreamDB{ownerID: ownerID, botID: botID}))),
		accountService: newTestCreateBotAccountService(ownerID),
	}
}

func callCreateContainer(t *testing.T, handler *ContainerdHandler, ownerID, botID, body string) []map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/bots/"+botID+"/container", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(echo.New(), req, rec, ownerID)
	ctx.SetParamNames("bot_id")
	ctx.SetParamValues(botID)
	if err := handler.CreateContainer(ctx); err != nil {
		t.Fatalf("CreateContainer() error = %v", err)
	}
	return decodeSSEEvents(t, rec.Body.String())
}

// restore_data is honored once the reconciler has settled the workspace: the
// archive is imported and the single "complete" event reports it.
func TestCreateContainerRestoresPreservedDataWhenRequested(t *testing.T) {
	ownerID := "00000000-0000-0000-0000-000000000103"
	botID := "00000000-0000-0000-0000-000000000203"
	manager := &restoreWorkspaceManager{preserved: true}
	ws := &createBotStreamWorkspace{events: []workspace.ContainerSetupEvent{
		{Type: "creating"},
		{Type: "complete", Image: "debian:bookworm-slim", ContainerID: "workspace-" + botID, Snapshotter: "overlayfs", Started: true, HasPreservedData: true},
	}}
	handler := newRestoreContainerHandler(t, ownerID, botID, manager, ws)

	events := callCreateContainer(t, handler, ownerID, botID, `{"restore_data": true}`)

	if manager.restored != 1 {
		t.Fatalf("RestorePreservedData calls = %d, want 1", manager.restored)
	}
	var order []string
	for _, ev := range events {
		order = append(order, ev["type"].(string))
	}
	if got := strings.Join(order, ","); got != "creating,restoring,complete" {
		t.Fatalf("event order = %s, want creating,restoring,complete", got)
	}
	complete, _ := findEventType(events, "complete")
	container := complete["container"].(map[string]any)
	if container["data_restored"] != true {
		t.Fatalf("complete data_restored = %#v, want true", container["data_restored"])
	}
	if container["has_preserved_data"] != false {
		t.Fatalf("complete has_preserved_data = %#v, want false", container["has_preserved_data"])
	}
	// The manager's view of the settled workspace is authoritative.
	if container["container_id"] != "workspace-status" {
		t.Fatalf("complete container_id = %#v, want the manager's value", container["container_id"])
	}
	if container["snapshotter"] != "overlayfs" || container["runtime_backend"] != "io.containerd.runc.v2" {
		t.Fatalf("complete container = %#v, want manager snapshotter and runtime", container)
	}
}

// The terminal event does not depend on this instance having seen the
// reconciler's "complete" progress: a stream served next to another instance
// still describes the workspace from the manager.
func TestCreateContainerDescribesWorkspaceWithoutProgressEvent(t *testing.T) {
	ownerID := "00000000-0000-0000-0000-000000000106"
	botID := "00000000-0000-0000-0000-000000000206"
	manager := &restoreWorkspaceManager{}
	ws := &createBotStreamWorkspace{events: []workspace.ContainerSetupEvent{{Type: "creating"}}}
	handler := newRestoreContainerHandler(t, ownerID, botID, manager, ws)

	events := callCreateContainer(t, handler, ownerID, botID, `{}`)

	complete, ok := findEventType(events, "complete")
	if !ok {
		t.Fatalf("complete event missing: %#v", events)
	}
	container := complete["container"].(map[string]any)
	if container["container_id"] != "workspace-status" || container["started"] != true || container["data_restored"] != false {
		t.Fatalf("complete container = %#v", container)
	}
}

func TestCreateContainerReportsRestoreFailure(t *testing.T) {
	ownerID := "00000000-0000-0000-0000-000000000104"
	botID := "00000000-0000-0000-0000-000000000204"
	manager := &restoreWorkspaceManager{preserved: true, restoreErr: errors.New("archive corrupt")}
	ws := &createBotStreamWorkspace{events: []workspace.ContainerSetupEvent{
		{Type: "complete", Image: "debian:bookworm-slim", Started: true},
	}}
	handler := newRestoreContainerHandler(t, ownerID, botID, manager, ws)

	events := callCreateContainer(t, handler, ownerID, botID, `{"restore_data": true}`)

	if hasEventType(events, "complete") {
		t.Fatalf("complete must not be sent after a failed restore: %#v", events)
	}
	errEvent, ok := findEventType(events, "error")
	if !ok || errEvent["code"] != "workspace_restore_failed" {
		t.Fatalf("error event = %#v, want workspace_restore_failed", errEvent)
	}
}

// Without restore_data the archive is left alone and the reconciler's
// "complete" event passes through unchanged.
func TestCreateContainerLeavesPreservedDataWithoutRestoreFlag(t *testing.T) {
	ownerID := "00000000-0000-0000-0000-000000000105"
	botID := "00000000-0000-0000-0000-000000000205"
	manager := &restoreWorkspaceManager{preserved: true}
	ws := &createBotStreamWorkspace{events: []workspace.ContainerSetupEvent{
		{Type: "complete", Image: "debian:bookworm-slim", Started: true, HasPreservedData: true},
	}}
	handler := newRestoreContainerHandler(t, ownerID, botID, manager, ws)

	events := callCreateContainer(t, handler, ownerID, botID, `{}`)

	if manager.restored != 0 {
		t.Fatalf("RestorePreservedData calls = %d, want 0", manager.restored)
	}
	if hasEventType(events, "restoring") {
		t.Fatalf("unexpected restoring event: %#v", events)
	}
	complete, ok := findEventType(events, "complete")
	if !ok {
		t.Fatalf("complete event missing: %#v", events)
	}
	container := complete["container"].(map[string]any)
	if container["has_preserved_data"] != true || container["data_restored"] != false {
		t.Fatalf("complete container = %#v, want has_preserved_data=true data_restored=false", container)
	}
}
