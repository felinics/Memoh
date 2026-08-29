package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	ctr "github.com/felinics/memoh/internal/container"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/workspace"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

func TestCreateBotStreamsLifecycleWhenSSERequested(t *testing.T) {
	ownerID := "00000000-0000-0000-0000-000000000101"
	botID := "00000000-0000-0000-0000-000000000201"
	botUUID := testUUID(botID)

	handler := &UsersHandler{
		service: newTestCreateBotAccountService(ownerID),
		botService: bots.NewService(nil, postgresstore.NewQueries(sqlc.New(&createBotStreamDB{
			ownerID: ownerID,
			botID:   botID,
		}))),
		acpWorkspace: &createBotStreamWorkspace{},
	}

	req := httptest.NewRequest(http.MethodPost, "/bots", strings.NewReader(`{
		"name": "stream-bot",
		"display_name": "Stream Bot",
		"acl_preset": "allow_all",
		"wait_for_ready": true
	}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(echo.HeaderAccept, "text/event-stream")
	rec := httptest.NewRecorder()
	ctx := testAuthContext(echo.New(), req, rec, ownerID)

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

	handler := &UsersHandler{
		service: newTestCreateBotAccountService(ownerID),
		botService: bots.NewService(nil, postgresstore.NewQueries(sqlc.New(&createBotStreamDB{
			ownerID: ownerID,
			botID:   "00000000-0000-0000-0000-000000000203",
		}))),
	}

	req := httptest.NewRequest(http.MethodPost, "/bots", strings.NewReader(`{
		"name": "misconfigured-bot",
		"display_name": "Misconfigured Bot",
		"acl_preset": "allow_all",
		"wait_for_ready": true
	}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(echo.HeaderAccept, "text/event-stream")
	rec := httptest.NewRecorder()
	ctx := testAuthContext(echo.New(), req, rec, ownerID)

	err := handler.CreateBot(ctx)
	if err == nil {
		t.Fatal("CreateBot() error = nil, want workspace lifecycle configuration error")
	}
	httpErr := requireHTTPError(t, err)
	if httpErr.Code != http.StatusInternalServerError {
		t.Fatalf("CreateBot() status = %d, want %d", httpErr.Code, http.StatusInternalServerError)
	}
}

func TestCreateBotStreamsContainerProgressEvents(t *testing.T) {
	ownerID := "00000000-0000-0000-0000-000000000102"
	botID := "00000000-0000-0000-0000-000000000202"

	handler := &UsersHandler{
		service: newTestCreateBotAccountService(ownerID),
		botService: bots.NewService(nil, postgresstore.NewQueries(sqlc.New(&createBotStreamDB{
			ownerID: ownerID,
			botID:   botID,
		}))),
		acpWorkspace: &createBotStreamWorkspace{events: []workspace.ContainerSetupEvent{
			{Type: "pulling", Image: "debian:bookworm-slim"},
			{Type: "pull_progress", Layers: []ctr.LayerStatus{{Ref: "layer-1", Offset: 10, Total: 100}}},
			{Type: "creating"},
			{
				Type:             "complete",
				Image:            "debian:bookworm-slim",
				ContainerID:      "workspace-" + botID,
				WorkspaceBackend: bridge.WorkspaceBackendContainer,
				RuntimeBackend:   "io.containerd.runc.v2",
				ContainerPath:    "/data",
				Started:          true,
			},
		}},
	}

	req := httptest.NewRequest(http.MethodPost, "/bots", strings.NewReader(`{
		"name": "progress-bot",
		"display_name": "Progress Bot",
		"acl_preset": "allow_all",
		"wait_for_ready": true
	}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(echo.HeaderAccept, "text/event-stream")
	rec := httptest.NewRecorder()
	ctx := testAuthContext(echo.New(), req, rec, ownerID)

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
	if got := container["runtime_backend"]; got != "io.containerd.runc.v2" {
		t.Fatalf("complete runtime_backend = %#v, want io.containerd.runc.v2", got)
	}
	if got := container["started"]; got != true {
		t.Fatalf("complete started = %#v, want true", got)
	}
}

func TestCreateBotStreamReportsSetupErrorAfterCreatedBot(t *testing.T) {
	ownerID := "00000000-0000-0000-0000-000000000104"
	botID := "00000000-0000-0000-0000-000000000204"
	streamDB := &createBotStreamDB{
		ownerID: ownerID,
		botID:   botID,
	}

	handler := &UsersHandler{
		logger:       slog.Default(),
		service:      newTestCreateBotAccountService(ownerID),
		botService:   bots.NewService(nil, postgresstore.NewQueries(sqlc.New(streamDB))),
		acpWorkspace: &createBotStreamWorkspace{err: errors.New("image pull failed")},
	}

	req := httptest.NewRequest(http.MethodPost, "/bots", strings.NewReader(`{
		"name": "setup-failed-bot",
		"display_name": "Setup Failed Bot",
		"acl_preset": "allow_all",
		"wait_for_ready": true
	}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(echo.HeaderAccept, "text/event-stream")
	rec := httptest.NewRecorder()
	ctx := testAuthContext(echo.New(), req, rec, ownerID)

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
	setupError := requireStreamLastSetupError(t, streamDB.persistedMetadata)
	if setupError["phase"] != "setup" {
		t.Fatalf("phase = %#v, want setup", setupError["phase"])
	}
	if got, _ := setupError["message"].(string); !strings.Contains(got, "image pull failed") {
		t.Fatalf("persisted message = %q, want setup failure", got)
	}
	if streamDB.status != bots.BotStatusReady {
		t.Fatalf("bot status = %q, want %q", streamDB.status, bots.BotStatusReady)
	}
}

func TestCreateBotStreamReportsStableContractErrorAndLeavesBotReady(t *testing.T) {
	ownerID := "00000000-0000-0000-0000-000000000108"
	botID := "00000000-0000-0000-0000-000000000208"
	streamDB := &createBotStreamDB{ownerID: ownerID, botID: botID}
	setupErr := errors.Join(
		workspace.ErrWorkspaceImageIncompatible,
		errors.New("missing /opt/memoh/toolkit/bin/node"),
	)
	handler := &UsersHandler{
		logger:       slog.Default(),
		service:      newTestCreateBotAccountService(ownerID),
		botService:   bots.NewService(nil, postgresstore.NewQueries(sqlc.New(streamDB))),
		acpWorkspace: &createBotStreamWorkspace{err: setupErr},
	}

	req := httptest.NewRequest(http.MethodPost, "/bots", strings.NewReader(`{
		"name": "incompatible-workspace-bot",
		"display_name": "Incompatible Workspace Bot",
		"acl_preset": "allow_all",
		"wait_for_ready": true
	}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(echo.HeaderAccept, "text/event-stream")
	rec := httptest.NewRecorder()
	ctx := testAuthContext(echo.New(), req, rec, ownerID)

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
	if streamDB.status != bots.BotStatusReady {
		t.Fatalf("bot status = %q, want %q", streamDB.status, bots.BotStatusReady)
	}
}

func TestCreateBotStreamReportsACPConfigWriteError(t *testing.T) {
	ownerID := "00000000-0000-0000-0000-000000000106"
	botID := "00000000-0000-0000-0000-000000000206"
	handler := &UsersHandler{
		logger:  slog.Default(),
		service: newTestCreateBotAccountService(ownerID),
		botService: bots.NewService(nil, postgresstore.NewQueries(sqlc.New(&createBotStreamDB{
			ownerID: ownerID,
			botID:   botID,
		}))),
		acpWorkspace: &createBotStreamWorkspace{mcpErr: errors.New("bridge unavailable")},
	}

	req := httptest.NewRequest(http.MethodPost, "/bots", strings.NewReader(`{
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
	ctx := testAuthContext(echo.New(), req, rec, ownerID)

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
		service: accounts.NewService(nil, createBotMissingAccountStore{}),
	}

	req := httptest.NewRequest(http.MethodGet, "/users/me", nil)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(echo.New(), req, rec, ownerID)

	err := handler.GetMe(ctx)
	if err == nil {
		t.Fatal("GetMe() error = nil, want unauthorized")
	}
	httpErr := requireHTTPError(t, err)
	if httpErr.Code != http.StatusUnauthorized {
		t.Fatalf("GetMe() status = %d, want %d", httpErr.Code, http.StatusUnauthorized)
	}
}

func TestCreateBotStreamReturnsUnauthorizedWhenTokenUserIsMissing(t *testing.T) {
	ownerID := "00000000-0000-0000-0000-000000000105"

	handler := &UsersHandler{
		service:    accounts.NewService(nil, createBotMissingAccountStore{}),
		botService: bots.NewService(nil, nil),
	}

	req := httptest.NewRequest(http.MethodPost, "/bots", strings.NewReader(`{
		"name": "stale-token-bot",
		"display_name": "Stale Token Bot",
		"acl_preset": "allow_all",
		"wait_for_ready": true
	}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(echo.HeaderAccept, "text/event-stream")
	rec := httptest.NewRecorder()
	ctx := testAuthContext(echo.New(), req, rec, ownerID)

	err := handler.CreateBot(ctx)
	if err == nil {
		t.Fatal("CreateBot() error = nil, want unauthorized")
	}
	httpErr := requireHTTPError(t, err)
	if httpErr.Code != http.StatusUnauthorized {
		t.Fatalf("CreateBot() status = %d, want %d", httpErr.Code, http.StatusUnauthorized)
	}
}

func requireHTTPError(t *testing.T, err error) *echo.HTTPError {
	t.Helper()
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error type = %T, want *echo.HTTPError", err)
	}
	return httpErr
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

func newTestCreateBotAccountService(userID string) *accounts.Service {
	return accounts.NewService(nil, createBotAccountStore{userID: userID})
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
	dbstore.AccountStore
	userID string
}

func (s createBotAccountStore) GetByUserID(_ context.Context, userID string) (dbstore.AccountRecord, error) {
	if userID != s.userID {
		return dbstore.AccountRecord{}, pgx.ErrNoRows
	}
	return dbstore.AccountRecord{ID: userID, Role: "member", IsActive: true}, nil
}

type createBotMissingAccountStore struct {
	dbstore.AccountStore
}

func (createBotMissingAccountStore) GetByUserID(context.Context, string) (dbstore.AccountRecord, error) {
	return dbstore.AccountRecord{}, db.ErrNotFound
}

type createBotStreamDB struct {
	ownerID           string
	botID             string
	status            string
	metadata          []byte
	persistedMetadata []byte
}

func (d *createBotStreamDB) Exec(_ context.Context, query string, args ...interface{}) (pgconn.CommandTag, error) {
	if strings.Contains(query, "UPDATE bots") && strings.Contains(query, "SET status = $2") && len(args) > 1 {
		d.status, _ = args[1].(string)
	}
	return pgconn.CommandTag{}, nil
}

func (*createBotStreamDB) Query(context.Context, string, ...interface{}) (pgx.Rows, error) {
	return nil, nil
}

func (d *createBotStreamDB) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	switch {
	case strings.Contains(query, "FROM users") && strings.Contains(query, "id = $1"):
		return &createBotStreamRow{scanFunc: func(_ ...any) error { return nil }}
	case strings.Contains(query, "INSERT INTO bots"):
		if len(args) > 6 {
			if payload, ok := args[6].([]byte); ok {
				d.metadata = append([]byte(nil), payload...)
			}
		}
		return d.botRow(bots.BotStatusCreating)
	case strings.Contains(query, "UPDATE bots") && strings.Contains(query, "metadata = $7"):
		payload, ok := args[6].([]byte)
		if !ok {
			return &createBotStreamRow{scanFunc: func(_ ...any) error { return pgx.ErrNoRows }}
		}
		d.metadata = append([]byte(nil), payload...)
		d.persistedMetadata = append([]byte(nil), payload...)
		return d.botRow(bots.BotStatusReady)
	case strings.Contains(query, "FROM bots") && strings.Contains(query, "id = $1"):
		return d.botRow(bots.BotStatusReady)
	case strings.Contains(query, "FROM bots") && strings.Contains(query, "WHERE name = $1"):
		return &createBotStreamRow{scanFunc: func(_ ...any) error { return pgx.ErrNoRows }}
	default:
		_ = args
		return &createBotStreamRow{scanFunc: func(_ ...any) error { return pgx.ErrNoRows }}
	}
}

func (d *createBotStreamDB) botRow(status string) pgx.Row {
	botID := testUUID(d.botID)
	ownerID := testUUID(d.ownerID)
	metadata := d.metadata
	if len(metadata) == 0 {
		metadata = []byte(`{}`)
	}
	return &createBotStreamRow{scanFunc: func(dest ...any) error {
		if len(dest) < 16 {
			return pgx.ErrNoRows
		}
		*dest[0].(*pgtype.UUID) = botID
		*dest[1].(*pgtype.UUID) = ownerID
		*dest[2].(*string) = "stream-bot"
		*dest[3].(*pgtype.Text) = pgtype.Text{String: "Stream Bot", Valid: true}
		*dest[4].(*pgtype.Text) = pgtype.Text{}
		*dest[5].(*pgtype.Text) = pgtype.Text{}
		*dest[6].(*bool) = true
		*dest[7].(*string) = status
		*dest[8].(*string) = "en"
		*dest[9].(*string) = "medium"
		*dest[10].(*pgtype.UUID) = pgtype.UUID{}
		*dest[11].(*pgtype.UUID) = pgtype.UUID{}
		*dest[12].(*pgtype.UUID) = pgtype.UUID{}
		if len(dest) == 16 {
			*dest[13].(*[]byte) = append([]byte(nil), metadata...)
			*dest[14].(*pgtype.Timestamptz) = pgtype.Timestamptz{Valid: false}
			*dest[15].(*pgtype.Timestamptz) = pgtype.Timestamptz{Valid: false}
			return nil
		}
		*dest[13].(*bool) = false
		*dest[14].(*int32) = 200
		*dest[15].(*pgtype.Int4) = pgtype.Int4{Int32: 50, Valid: true}
		*dest[16].(*pgtype.UUID) = pgtype.UUID{}
		*dest[17].(*[]byte) = append([]byte(nil), metadata...)
		*dest[18].(*pgtype.Timestamptz) = pgtype.Timestamptz{Valid: false}
		*dest[19].(*pgtype.Timestamptz) = pgtype.Timestamptz{Valid: false}
		return nil
	}}
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

type createBotStreamRow struct {
	scanFunc func(dest ...any) error
}

func (r *createBotStreamRow) Scan(dest ...any) error {
	return r.scanFunc(dest...)
}
