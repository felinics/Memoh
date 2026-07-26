package bot

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/domains/api/bot"
	httpfixture "github.com/memohai/memoh/domains/api/http/internal/test"
	"github.com/memohai/memoh/domains/iam/account"
	accountpersistence "github.com/memohai/memoh/domains/iam/account/persistence"
	runtimedomain "github.com/memohai/memoh/domains/runtime"
	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
	ctr "github.com/memohai/memoh/domains/runtime/container"
	"github.com/memohai/memoh/domains/runtime/workspace"
	"github.com/memohai/memoh/internal/apperror"
)

func TestCreateBotStreamsLifecycleWhenSSERequested(t *testing.T) {
	ownerID := "00000000-0000-0000-0000-000000000101"
	botID := "00000000-0000-0000-0000-000000000201"
	botUUID := httpfixture.UUID(botID)

	handler := &UsersHandler{
		service:      newTestCreateBotAccountService(ownerID),
		botService:   httpfixture.NewBotService(nil, &httpfixture.BotStore{CreateID: botID}, createBotACLPresetApplier{}),
		acpWorkspace: &createBotStreamWorkspace{},
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/bots", strings.NewReader(`{
		"name": "stream-bot",
		"display_name": "Stream Bot",
		"acl_preset": "allow_all",
		"wait_for_ready": true
	}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(echo.HeaderAccept, "text/event-stream")
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(echo.New(), req, rec, ownerID)

	if err := handler.CreateBot(ctx); err != nil {
		t.Fatalf("CreateBot() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get(echo.HeaderContentType); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("content type = %q, want text/event-stream", got)
	}

	events := decodeSSEEvents(t, rec.Body.String())
	if len(events) < 2 {
		t.Fatalf("events len = %d, want at least bot_created and ready: %#v", len(events), events)
	}
	if events[0]["type"] != "bot_created" {
		t.Fatalf("first event type = %#v, want bot_created; events=%#v", events[0]["type"], events)
	}
	if got := eventBotID(events[0]); got != botID {
		t.Fatalf("created bot id = %q, want %q", got, botID)
	}
	last := events[len(events)-1]
	if last["type"] != "ready" {
		t.Fatalf("last event type = %#v, want ready; events=%#v", last["type"], events)
	}
	if got := eventBotID(last); got != botID {
		t.Fatalf("ready bot id = %q, want %q", got, botID)
	}
	if handler.botService == nil {
		t.Fatal("bot service should be configured")
	}
	if botUUID.Valid != true {
		t.Fatal("bot UUID helper sanity check failed")
	}
}

func TestCreateBotStreamRequiresWorkspaceLifecycle(t *testing.T) {
	ownerID := "00000000-0000-0000-0000-000000000103"
	botID := "00000000-0000-0000-0000-000000000203"

	handler := &UsersHandler{
		service:    newTestCreateBotAccountService(ownerID),
		botService: httpfixture.NewBotService(nil, &httpfixture.BotStore{CreateID: botID}, createBotACLPresetApplier{}),
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/bots", strings.NewReader(`{
		"name": "misconfigured-bot",
		"display_name": "Misconfigured Bot",
		"acl_preset": "allow_all",
		"wait_for_ready": true
	}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(echo.HeaderAccept, "text/event-stream")
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(echo.New(), req, rec, ownerID)

	err := handler.CreateBot(ctx)
	if err == nil {
		t.Fatal("CreateBot() error = nil, want workspace lifecycle configuration error")
	}
	requireAppErrorStatus(t, err, http.StatusInternalServerError)
}

func TestCreateBotStreamsContainerProgressEvents(t *testing.T) {
	ownerID := "00000000-0000-0000-0000-000000000102"
	botID := "00000000-0000-0000-0000-000000000202"

	handler := &UsersHandler{
		service:    newTestCreateBotAccountService(ownerID),
		botService: httpfixture.NewBotService(nil, &httpfixture.BotStore{CreateID: botID}, createBotACLPresetApplier{}),
		acpWorkspace: &createBotStreamWorkspace{events: []workspace.ContainerSetupEvent{
			{Type: "pulling", Image: "debian:bookworm-slim"},
			{Type: "pull_progress", Layers: []ctr.LayerStatus{{Ref: "layer-1", Offset: 10, Total: 100}}},
			{Type: "creating"},
			{
				Type:             "complete",
				Image:            "debian:bookworm-slim",
				ContainerID:      "workspace-" + botID,
				WorkspaceBackend: bridge.WorkspaceBackendContainer,
				RuntimeBackend:   "io.containerd.kata.v2",
				ContainerPath:    "/data",
				Started:          true,
			},
		}},
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/bots", strings.NewReader(`{
		"name": "progress-bot",
		"display_name": "Progress Bot",
		"acl_preset": "allow_all",
		"wait_for_ready": true
	}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(echo.HeaderAccept, "text/event-stream")
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(echo.New(), req, rec, ownerID)

	if err := handler.CreateBot(ctx); err != nil {
		t.Fatalf("CreateBot() error = %v", err)
	}

	events := decodeSSEEvents(t, rec.Body.String())
	for _, eventType := range []string{"bot_created", "pulling", "pull_progress", "creating", "complete", "ready"} {
		if !hasEventType(events, eventType) {
			t.Fatalf("missing %q event: %#v", eventType, events)
		}
	}
	complete, ok := findEventType(events, "complete")
	if !ok {
		t.Fatalf("complete event missing: %#v", events)
	}
	container, ok := complete["container"].(map[string]any)
	if !ok {
		t.Fatalf("complete container payload = %#v", complete["container"])
	}
	if got := container["container_id"]; got != "workspace-"+botID {
		t.Fatalf("complete container_id = %#v, want %q", got, "workspace-"+botID)
	}
	if got := container["workspace_backend"]; got != bridge.WorkspaceBackendContainer {
		t.Fatalf("complete workspace_backend = %#v, want %q", got, bridge.WorkspaceBackendContainer)
	}
	if got := container["runtime_backend"]; got != "io.containerd.kata.v2" {
		t.Fatalf("complete runtime_backend = %#v, want io.containerd.kata.v2", got)
	}
	if got := container["started"]; got != true {
		t.Fatalf("complete started = %#v, want true", got)
	}
}

func TestCreateBotStreamReportsSetupErrorAfterCreatedBot(t *testing.T) {
	ownerID := "00000000-0000-0000-0000-000000000104"
	botID := "00000000-0000-0000-0000-000000000204"
	botStore := &httpfixture.BotStore{CreateID: botID}

	handler := &UsersHandler{
		logger:       slog.Default(),
		service:      newTestCreateBotAccountService(ownerID),
		botService:   httpfixture.NewBotService(nil, botStore, createBotACLPresetApplier{}),
		acpWorkspace: &createBotStreamWorkspace{err: errors.New("image pull failed")},
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/bots", strings.NewReader(`{
		"name": "setup-failed-bot",
		"display_name": "Setup Failed Bot",
		"acl_preset": "allow_all",
		"wait_for_ready": true
	}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(echo.HeaderAccept, "text/event-stream")
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(echo.New(), req, rec, ownerID)

	if err := handler.CreateBot(ctx); err != nil {
		t.Fatalf("CreateBot() error = %v", err)
	}

	events := decodeSSEEvents(t, rec.Body.String())
	if len(events) < 2 {
		t.Fatalf("events len = %d, want bot_created and error: %#v", len(events), events)
	}
	if events[0]["type"] != "bot_created" {
		t.Fatalf("first event type = %#v, want bot_created; events=%#v", events[0]["type"], events)
	}
	if got := eventBotID(events[0]); got != botID {
		t.Fatalf("created bot id = %q, want %q", got, botID)
	}
	last := events[len(events)-1]
	if last["type"] != "error" {
		t.Fatalf("last event type = %#v, want error; events=%#v", last["type"], events)
	}
	message, _ := last["message"].(string)
	if message != "workspace setup failed" {
		t.Fatalf("error message = %q, want stable workspace setup failure", message)
	}
	setupError := requireStreamLastSetupError(t, botStore.Bot.Metadata)
	if setupError["phase"] != "setup" {
		t.Fatalf("phase = %#v, want setup", setupError["phase"])
	}
	if got, _ := setupError["message"].(string); !strings.Contains(got, "image pull failed") {
		t.Fatalf("persisted message = %q, want setup failure", got)
	}
	if botStore.Bot.Status != bot.BotStatusReady {
		t.Fatalf("bot status = %q, want %q", botStore.Bot.Status, bot.BotStatusReady)
	}
}

func TestCreateBotStreamReportsStableContractErrorAndLeavesBotReady(t *testing.T) {
	ownerID := "00000000-0000-0000-0000-000000000108"
	botID := "00000000-0000-0000-0000-000000000208"
	botStore := &httpfixture.BotStore{CreateID: botID}
	setupErr := errors.Join(
		runtimedomain.ErrWorkspaceImageIncompatible,
		errors.New("missing /opt/memoh/toolkit/bin/node"),
	)
	handler := &UsersHandler{
		logger:       slog.Default(),
		service:      newTestCreateBotAccountService(ownerID),
		botService:   httpfixture.NewBotService(nil, botStore, createBotACLPresetApplier{}),
		acpWorkspace: &createBotStreamWorkspace{err: setupErr},
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/bots", strings.NewReader(`{
		"name": "incompatible-workspace-bot",
		"display_name": "Incompatible Workspace Bot",
		"acl_preset": "allow_all",
		"wait_for_ready": true
	}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(echo.HeaderAccept, "text/event-stream")
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(echo.New(), req, rec, ownerID)

	if err := handler.CreateBot(ctx); err != nil {
		t.Fatalf("CreateBot() error = %v", err)
	}
	events := decodeSSEEvents(t, rec.Body.String())
	last := events[len(events)-1]
	if last["type"] != "error" {
		t.Fatalf("last event type = %#v, want error; events=%#v", last["type"], events)
	}
	if last["code"] != string(apperror.CodeWorkspaceImageIncompatible) {
		t.Fatalf("error code = %#v, want %q", last["code"], apperror.CodeWorkspaceImageIncompatible)
	}
	message, _ := last["message"].(string)
	if strings.Contains(message, "/opt/memoh") {
		t.Fatalf("private workspace path leaked in message %q", message)
	}
	if botStore.Bot.Status != bot.BotStatusReady {
		t.Fatalf("bot status = %q, want %q", botStore.Bot.Status, bot.BotStatusReady)
	}
}

func TestCreateBotStreamReportsACPConfigWriteError(t *testing.T) {
	ownerID := "00000000-0000-0000-0000-000000000106"
	botID := "00000000-0000-0000-0000-000000000206"
	handler := &UsersHandler{
		logger:       slog.Default(),
		service:      newTestCreateBotAccountService(ownerID),
		botService:   httpfixture.NewBotService(nil, &httpfixture.BotStore{CreateID: botID}, createBotACLPresetApplier{}),
		acpWorkspace: &createBotStreamWorkspace{mcpErr: errors.New("bridge unavailable")},
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/bots", strings.NewReader(`{
		"name": "hermes-stream-bot",
		"display_name": "Hermes Stream Bot",
		"acl_preset": "allow_all",
		"wait_for_ready": true,
		"metadata": {
			"acp": {
				"agents": {
					"hermes": {
						"enabled": true,
						"setup_mode": "api_key",
						"managed": {
							"provider": "openrouter",
							"model": "nousresearch/hermes",
							"api_key": "secret-value"
						}
					}
				}
			}
		}
	}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(echo.HeaderAccept, "text/event-stream")
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(echo.New(), req, rec, ownerID)

	if err := handler.CreateBot(ctx); err != nil {
		t.Fatalf("CreateBot() error = %v", err)
	}

	events := decodeSSEEvents(t, rec.Body.String())
	errorEvent, ok := findEventType(events, "error")
	if !ok {
		t.Fatalf("missing ACP config error event: %#v", events)
	}
	message, _ := errorEvent["message"].(string)
	if !strings.Contains(message, "write ACP workspace config: bridge unavailable") {
		t.Fatalf("error message = %q, want ACP config details", message)
	}
	last := events[len(events)-1]
	if last["type"] != "ready" {
		t.Fatalf("last event type = %#v, want ready after ACP config warning; events=%#v", last["type"], events)
	}
}

func TestGetMeReturnsUnauthorizedWhenTokenUserIsMissing(t *testing.T) {
	ownerID := "00000000-0000-0000-0000-000000000105"

	handler := &UsersHandler{
		service: account.NewService(nil, createBotMissingAccountStore{}),
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/users/me", nil)
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(echo.New(), req, rec, ownerID)

	err := handler.GetMe(ctx)
	if err == nil {
		t.Fatal("GetMe() error = nil, want unauthorized")
	}
	requireAppErrorStatus(t, err, http.StatusUnauthorized)
}

func TestCreateBotStreamReturnsUnauthorizedWhenTokenUserIsMissing(t *testing.T) {
	ownerID := "00000000-0000-0000-0000-000000000105"

	handler := &UsersHandler{
		service:    account.NewService(nil, createBotMissingAccountStore{}),
		botService: bot.NewService(nil, nil, nil, nil, nil),
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/bots", strings.NewReader(`{
		"name": "stale-token-bot",
		"display_name": "Stale Token Bot",
		"acl_preset": "allow_all",
		"wait_for_ready": true
	}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(echo.HeaderAccept, "text/event-stream")
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(echo.New(), req, rec, ownerID)

	err := handler.CreateBot(ctx)
	if err == nil {
		t.Fatal("CreateBot() error = nil, want unauthorized")
	}
	requireAppErrorStatus(t, err, http.StatusUnauthorized)
}

func requireAppErrorStatus(t *testing.T, err error, want int) {
	t.Helper()
	if _, ok := apperror.As(err); !ok {
		t.Fatalf("error type = %T, want *apperror.Error", err)
	}
	if got := apperror.KindOf(err).HTTPStatus(); got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
}

func decodeSSEEvents(t *testing.T, raw string) []map[string]any {
	t.Helper()
	events := make([]map[string]any, 0)
	for _, block := range strings.Split(raw, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		for _, line := range strings.Split(block, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var event map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
				t.Fatalf("decode event %q: %v", line, err)
			}
			events = append(events, event)
		}
	}
	return events
}

func eventBotID(event map[string]any) string {
	bot, ok := event["bot"].(map[string]any)
	if !ok {
		return ""
	}
	id, _ := bot["id"].(string)
	return id
}

func hasEventType(events []map[string]any, eventType string) bool {
	_, ok := findEventType(events, eventType)
	return ok
}

func findEventType(events []map[string]any, eventType string) (map[string]any, bool) {
	for _, event := range events {
		if event["type"] == eventType {
			return event, true
		}
	}
	return nil, false
}

func newTestCreateBotAccountService(userID string) *account.Service {
	return account.NewService(nil, createBotAccountStore{userID: userID})
}

type createBotACLPresetApplier struct{}

func (createBotACLPresetApplier) ApplyPreset(context.Context, string, string, string) error {
	return nil
}

type createBotStreamWorkspace struct {
	events []workspace.ContainerSetupEvent
	err    error
	mcpErr error
}

func (w *createBotStreamWorkspace) MCPClient(context.Context, string) (*bridge.Client, error) {
	return nil, w.mcpErr
}

func (*createBotStreamWorkspace) WorkspaceInfo(context.Context, string) (bridge.WorkspaceInfo, error) {
	return bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer}, nil
}

func (w *createBotStreamWorkspace) SetupBotContainerWithProgress(_ context.Context, _ string, progress workspace.ContainerSetupProgress) error {
	for _, event := range w.events {
		progress(event)
	}
	return w.err
}

type createBotAccountStore struct {
	accountpersistence.Store
	userID string
}

func (s createBotAccountStore) GetByUserID(_ context.Context, userID string) (accountpersistence.Record, error) {
	if userID != s.userID {
		return accountpersistence.Record{}, account.ErrAccountNotFound
	}
	return accountpersistence.Record{ID: userID, Role: "member", IsActive: true}, nil
}

type createBotMissingAccountStore struct {
	accountpersistence.Store
}

func (createBotMissingAccountStore) GetByUserID(context.Context, string) (accountpersistence.Record, error) {
	return accountpersistence.Record{}, account.ErrAccountNotFound
}

func requireStreamLastSetupError(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	var metadata map[string]any
	if err := json.Unmarshal(payload, &metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	workspace, ok := metadata["workspace"].(map[string]any)
	if !ok {
		t.Fatalf("workspace metadata missing: %#v", metadata)
	}
	setupError, ok := workspace["last_setup_error"].(map[string]any)
	if !ok {
		t.Fatalf("last_setup_error missing: %#v", workspace)
	}
	return setupError
}
