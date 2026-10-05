package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/botworkspace"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/server"
	"github.com/felinics/memoh/internal/workspace"
)

// capturedLogs is a logger whose records a test reads back.
type capturedLogs struct {
	logger *slog.Logger
	mu     *sync.Mutex
	buf    *bytes.Buffer
}

func captureLogs() capturedLogs {
	logs := capturedLogs{mu: &sync.Mutex{}, buf: &bytes.Buffer{}}
	logs.logger = slog.New(slog.NewJSONHandler(lockedWriter{logs}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return logs
}

type lockedWriter struct{ logs capturedLogs }

func (w lockedWriter) Write(p []byte) (int, error) {
	w.logs.mu.Lock()
	defer w.logs.mu.Unlock()
	return w.logs.buf.Write(p)
}

func (l capturedLogs) records(t *testing.T) []map[string]any {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(l.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode log record %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

// serveWorkspaceStream runs handle behind the access log, as the server does,
// and returns the events the stream sent.
func serveWorkspaceStream(t *testing.T, logs capturedLogs, handle echo.HandlerFunc, c echo.Context, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	if err := server.AccessLog(logs.logger)(handle)(c); err != nil {
		t.Fatalf("access log returned %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want the stream's 200; body=%s", rec.Code, rec.Body.String())
	}
	return decodeSSEEvents(t, rec.Body.String())
}

// requireCatalogErrorEvent checks that the stream ended with an error event
// for code that carries the code's catalog detail and none of the cause's
// text.
func requireCatalogErrorEvent(t *testing.T, events []map[string]any, code apperror.Code, cause string) {
	t.Helper()
	definition, ok := apperror.Lookup(code)
	if !ok {
		t.Fatalf("code %s is not in the catalog", code)
	}
	event, ok := findEventType(events, "error")
	if !ok || event["code"] != string(code) || event["detail"] != definition.Detail || event["message"] != definition.Detail {
		t.Fatalf("error event = %#v, want %s with detail and message %q", event, code, definition.Detail)
	}
	raw, err := json.Marshal(events)
	if err != nil {
		t.Fatalf("encode events: %v", err)
	}
	if strings.Contains(string(raw), cause) {
		t.Fatalf("events carry the cause %q: %s", cause, raw)
	}
}

// requireStreamFailureRecord checks that the request's access record is its
// only record and attributes the stream's failure: the code, the fault, the
// level that follows from it and the cause's text.
func requireStreamFailureRecord(t *testing.T, logs capturedLogs, code apperror.Code, fault, cause string) {
	t.Helper()
	records := logs.records(t)
	if len(records) != 1 || records[0]["msg"] != "request" {
		t.Fatalf("records = %#v, want the one request record", records)
	}
	record := records[0]
	text, _ := record["error"].(string)
	if record["level"] != "ERROR" || record["status"] != float64(http.StatusOK) || record["reason"] != string(code) || record["fault"] != fault || !strings.Contains(text, cause) {
		t.Fatalf("request record = %#v, want ERROR 200 %s %s with %q", record, code, fault, cause)
	}
}

// failingIntentWorkspace refuses to record the workspace intent.
type failingIntentWorkspace struct {
	*createBotStreamWorkspace
	err error
}

func (w failingIntentWorkspace) EnsurePresent(context.Context, string, string) (botworkspace.Workspace, error) {
	return botworkspace.Workspace{}, w.err
}

func TestCreateContainerReportsAnIntentItCouldNotRecord(t *testing.T) {
	ownerID := "00000000-0000-0000-0000-000000000106"
	botID := "00000000-0000-0000-0000-000000000206"
	ws := failingIntentWorkspace{createBotStreamWorkspace: &createBotStreamWorkspace{}, err: errors.New("intent table locked")}
	handler := newRestoreContainerHandler(t, ownerID, botID, &restoreWorkspaceManager{}, ws)
	logs := captureLogs()
	handler.logger = logs.logger
	ctx, rec := createContainerContext(ownerID, botID, `{}`)

	events := serveWorkspaceStream(t, logs, handler.CreateContainer, ctx, rec)

	requireCatalogErrorEvent(t, events, apperror.CodeWorkspaceCreateFailed, "intent table locked")
	requireStreamFailureRecord(t, logs, apperror.CodeWorkspaceCreateFailed, "server", "intent table locked")
}

// readyLoadFailsDB fails every read of the bot once failing is set.
type readyLoadFailsDB struct {
	*createBotStreamDB
	failing atomic.Bool
}

func (d *readyLoadFailsDB) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	if d.failing.Load() && strings.Contains(query, "FROM bots") && strings.Contains(query, "id = $1") {
		return &createBotStreamRow{scanFunc: func(...any) error { return errors.New("bots read reset by peer") }}
	}
	return d.createBotStreamDB.QueryRow(ctx, query, args...)
}

// readyLoadFailsWorkspace settles the workspace and then breaks the bot read
// the stream makes before its ready event.
type readyLoadFailsWorkspace struct {
	*createBotStreamWorkspace
	db *readyLoadFailsDB
}

func (w readyLoadFailsWorkspace) Await(ctx context.Context, botID string, generation int64) (botworkspace.Workspace, error) {
	settled, err := w.createBotStreamWorkspace.Await(ctx, botID, generation)
	w.db.failing.Store(true)
	return settled, err
}

func TestCreateBotStreamReportsABotItCouldNotLoadOnceReady(t *testing.T) {
	ownerID := "00000000-0000-0000-0000-000000000107"
	botID := "00000000-0000-0000-0000-000000000207"
	db := &readyLoadFailsDB{createBotStreamDB: &createBotStreamDB{ownerID: ownerID, botID: botID}}
	inner := &createBotStreamWorkspace{events: []workspace.ContainerSetupEvent{
		{Type: "complete", Image: "debian:bookworm-slim", Started: true},
	}}
	logs := captureLogs()
	handler := &UsersHandler{
		logger:         logs.logger,
		service:        newTestCreateBotAccountService(ownerID),
		botService:     bots.NewService(nil, postgresstore.NewQueries(sqlc.New(db))),
		workspaceSetup: readyLoadFailsWorkspace{createBotStreamWorkspace: inner, db: db},
	}
	handler.botService.SetWorkspaceIntents(createBotIntents{w: inner})
	req := httptest.NewRequest(http.MethodPost, "/bots", strings.NewReader(`{"name":"ready-bot","display_name":"Ready Bot","acl_preset":"allow_all","wait_for_ready":true}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(echo.HeaderAccept, "text/event-stream")
	rec := httptest.NewRecorder()
	ctx := testAuthContext(echo.New(), req, rec, ownerID)

	events := serveWorkspaceStream(t, logs, handler.CreateBot, ctx, rec)

	if !hasEventType(events, "bot_created") || hasEventType(events, "ready") {
		t.Fatalf("events = %#v, want bot_created and no ready", events)
	}
	requireCatalogErrorEvent(t, events, apperror.CodeBotReadyUpdateFailed, "bots read reset by peer")
	requireStreamFailureRecord(t, logs, apperror.CodeBotReadyUpdateFailed, "server", "bots read reset by peer")
}

func TestStreamWorkspaceProvisioningReportsAnAwaitThatFailed(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code apperror.Code
	}{
		{"the stream budget ran out", context.DeadlineExceeded, apperror.CodeWorkspaceSetupTimeout},
		{"the wait was canceled", context.Canceled, apperror.CodeWorkspaceSetupTimeout},
		{"the workspace could not be read", errors.New("bot_workspaces read reset by peer"), apperror.CodeWorkspaceSetupFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sent []any
			outcome := streamWorkspaceProvisioning(
				context.Background(),
				func(payload any) bool {
					sent = append(sent, payload)
					return true
				},
				make(chan botworkspace.ProgressEvent),
				func(context.Context) (botworkspace.Workspace, error) { return botworkspace.Workspace{}, tc.err },
				"req-await",
			)

			definition, _ := apperror.Lookup(tc.code)
			if len(sent) != 1 {
				t.Fatalf("sent = %#v, want one error event", sent)
			}
			event, _ := sent[0].(createContainerErrorEvent)
			if event.Code != string(tc.code) || event.Detail != definition.Detail || event.Message != definition.Detail || event.RequestID != "req-await" {
				t.Fatalf("event = %#v, want %s with detail and message %q", event, tc.code, definition.Detail)
			}
			if apperror.CodeOf(outcome.Err) != tc.code || !errors.Is(apperror.CauseOf(outcome.Err), tc.err) {
				t.Fatalf("outcome error = %v, want %s caused by %v", outcome.Err, tc.code, tc.err)
			}
		})
	}
}

func TestCreateContainerReportsAWorkspaceThatFailedToSettle(t *testing.T) {
	ownerID := "00000000-0000-0000-0000-000000000108"
	botID := "00000000-0000-0000-0000-000000000208"
	ws := &createBotStreamWorkspace{err: errors.New("image pull failed")}
	handler := newRestoreContainerHandler(t, ownerID, botID, &restoreWorkspaceManager{}, ws)
	logs := captureLogs()
	handler.logger = logs.logger
	ctx, rec := createContainerContext(ownerID, botID, `{}`)

	events := serveWorkspaceStream(t, logs, handler.CreateContainer, ctx, rec)

	if hasEventType(events, "complete") {
		t.Fatalf("events = %#v, want no complete after a failed setup", events)
	}
	requireCatalogErrorEvent(t, events, apperror.CodeWorkspaceSetupFailed, "image pull failed")
	requireStreamFailureRecord(t, logs, apperror.CodeWorkspaceSetupFailed, "server", "image pull failed")
}
