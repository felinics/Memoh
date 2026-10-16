package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/apps"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/connectors"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/server"
)

const (
	oauthTestBotID          = "00000000-0000-0000-0000-000000000056"
	oauthTestUserID         = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	oauthTestInstallationID = "installation-56"
	oauthTestAPIToken       = "cit_test-only-token-56"
	oauthTestClientSecret   = "test-only-client-secret-56"
	oauthClientMissing      = `{"error":"oauth_client_not_configured","message":"PRIVATE the connector's OAuth client is not configured; client_secret=` + oauthTestClientSecret + `"}`
	legacyClientMissing     = `{"error":"validation_failed","message":"PRIVATE oauthsvc: config is missing client_id or client_secret"}`
	unknownAuthMethod       = `{"error":"validation_failed","message":"PRIVATE oauthsvc: unknown auth method: nope"}`
)

// fakeConnectIt answers every call with the wire body Connect-It sends, so
// the SDK decodes it as it does in production.
type fakeConnectIt struct {
	mu            sync.Mutex
	status        int
	body          string
	authorization []string
}

func (f *fakeConnectIt) set(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.body = status, body
}

func (f *fakeConnectIt) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	status, body := f.status, f.body
	f.authorization = append(f.authorization, r.Header.Get("Authorization"))
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(json.RawMessage(body))
}

type oauthChainQueries struct {
	memoryCapabilityQueries
	bindings []dbsqlc.Connector
}

func (q *oauthChainQueries) ListConnectorsByBotID(context.Context, pgtype.UUID) ([]dbsqlc.Connector, error) {
	return q.bindings, nil
}

func (q *oauthChainQueries) CreateConnector(_ context.Context, args dbsqlc.CreateConnectorParams) (dbsqlc.Connector, error) {
	item := dbsqlc.Connector{BotID: args.BotID, ConnectionID: args.ConnectionID, Alias: args.Alias, Enabled: true}
	q.bindings = append(q.bindings, item)
	return item, nil
}

func (q *oauthChainQueries) GetConnectorByConnectionID(_ context.Context, args dbsqlc.GetConnectorByConnectionIDParams) (dbsqlc.Connector, error) {
	for _, item := range q.bindings {
		if item.ConnectionID == args.ConnectionID {
			return item, nil
		}
	}
	return dbsqlc.Connector{}, pgx.ErrNoRows
}

type oauthAppStore struct {
	apps.Store
	status apps.Status
	refs   []apps.ConnectorRef
}

func (s *oauthAppStore) GetByID(_ context.Context, botID, installationID string) (apps.Installation, error) {
	return apps.Installation{ID: installationID, BotID: botID, Status: s.status}, nil
}

func (s *oauthAppStore) ListConnectorRefs(context.Context, string) ([]apps.ConnectorRef, error) {
	return append([]apps.ConnectorRef(nil), s.refs...), nil
}

func (s *oauthAppStore) SetConnectorRefConnection(_ context.Context, _, connectorType, connectionID string) error {
	for i := range s.refs {
		if s.refs[i].ConnectorType == connectorType {
			s.refs[i].ConnectionID = connectionID
		}
	}
	return nil
}

func (s *oauthAppStore) SetStatus(_ context.Context, botID, installationID string, status apps.Status, _ string) (apps.Installation, error) {
	s.status = status
	return apps.Installation{ID: installationID, BotID: botID, Status: status}, nil
}

type oauthChain struct {
	upstream *fakeConnectIt
	queries  *oauthChainQueries
	store    *oauthAppStore
	echo     *echo.Echo
	logs     *bytes.Buffer
}

// newOAuthChain serves the App and connector routes through the HTTP shell's
// access log and error handler, with Connect-It behind a real SDK client.
func newOAuthChain(t *testing.T) oauthChain {
	t.Helper()
	upstream := &fakeConnectIt{}
	connectIt := httptest.NewServer(upstream)
	t.Cleanup(connectIt.Close)

	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	bot := testBotRow(oauthTestBotID, map[string]any{})
	bot.OwnerUserID = testUUID(oauthTestUserID)
	bot.Status = bots.BotStatusReady
	queries := &oauthChainQueries{memoryCapabilityQueries: memoryCapabilityQueries{bot: bot}}
	var cfg config.Config
	cfg.ConnectIt = config.ConnectItConfig{BaseURL: connectIt.URL, APIToken: oauthTestAPIToken}
	connectorService, err := connectors.NewService(logger, cfg, queries)
	if err != nil {
		t.Fatal(err)
	}
	store := &oauthAppStore{status: apps.StatusPartial, refs: []apps.ConnectorRef{
		{InstallationID: oauthTestInstallationID, ConnectorType: "github", Required: true},
	}}
	appService := apps.NewService(apps.Options{
		Store: store, Registry: struct{ apps.RegistryClient }{}, Skills: struct{ apps.SkillPublisher }{},
		Connectors: connectorService, Logger: logger,
	})
	botService := bots.NewService(logger, queries)
	accountService := accounts.NewService(logger, testAdminAccountStore{role: "member"})

	e := echo.New()
	e.HTTPErrorHandler = server.NewHTTPErrorHandler(logger)
	e.Use(server.AccessLog(logger))
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("user", &jwt.Token{Valid: true, Claims: jwt.MapClaims{"sub": oauthTestUserID, "user_id": oauthTestUserID}})
			return next(c)
		}
	})
	NewAppsHandler(logger, appService, botService, accountService).Register(e)
	NewConnectorsHandler(connectorService, botService, accountService).Register(e)
	return oauthChain{upstream: upstream, queries: queries, store: store, echo: e, logs: logs}
}

func (c oauthChain) post(t *testing.T, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	c.logs.Reset()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c.echo.ServeHTTP(rec, req)
	for _, leaked := range []string{oauthTestAPIToken, oauthTestClientSecret, "PRIVATE"} {
		if strings.Contains(c.logs.String(), leaked) {
			t.Fatalf("%q reached the logs: %s", leaked, c.logs.String())
		}
	}
	var record map[string]any
	for _, line := range strings.Split(strings.TrimSpace(c.logs.String()), "\n") {
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) == nil && entry["msg"] == "request" {
			record = entry
		}
	}
	if record == nil {
		t.Fatalf("no request record logged: %s", c.logs.String())
	}
	return rec, record
}

func (c oauthChain) beginOAuth(t *testing.T) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	return c.post(t, "/bots/"+oauthTestBotID+"/apps/"+oauthTestInstallationID+"/connectors/github/oauth", `{"auth_method":"oauth"}`)
}

func assertConnectorProblem(t *testing.T, rec *httptest.ResponseRecorder, record map[string]any, code apperror.Code, fault, level string) {
	t.Helper()
	definition, ok := apperror.Lookup(code)
	if !ok {
		t.Fatalf("%s is not in the catalog", code)
	}
	var problem server.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatalf("response is not a Problem: %d %s", rec.Code, rec.Body.String())
	}
	if rec.Code != definition.HTTPStatus || rec.Header().Get(echo.HeaderContentType) != "application/problem+json" ||
		problem.Code != string(code) || problem.Status != definition.HTTPStatus || problem.Detail != definition.Detail ||
		string(problem.Fault) != fault || len(problem.Args) != 0 {
		t.Fatalf("response = %d %s, want %s %d fault %s", rec.Code, rec.Body.String(), code, definition.HTTPStatus, fault)
	}
	if record["level"] != level || record["reason"] != string(code) || record["fault"] != fault ||
		record["status"] != float64(definition.HTTPStatus) {
		t.Fatalf("request record = %v, want %s %s %s", record, level, code, fault)
	}
	for _, leaked := range []string{oauthTestAPIToken, oauthTestClientSecret, "PRIVATE"} {
		if strings.Contains(rec.Body.String(), leaked) {
			t.Fatalf("%q reached the response: %s", leaked, rec.Body.String())
		}
	}
}

func TestAppConnectorOAuthReportsMissingOAuthClientApp(t *testing.T) {
	chain := newOAuthChain(t)

	chain.upstream.set(http.StatusUnprocessableEntity, oauthClientMissing)
	rec, record := chain.beginOAuth(t)
	assertConnectorProblem(t, rec, record, apperror.CodeConnectorOAuthClientNotConfigured, "dependency", "ERROR")
	if chain.upstream.authorization[0] != "Bearer "+oauthTestAPIToken {
		t.Fatalf("Connect-It was not called with the deployment token: %q", chain.upstream.authorization)
	}
	if text, _ := record["error"].(string); !strings.Contains(text, "oauth_client_not_configured") || !strings.Contains(text, "HTTP 422") {
		t.Fatalf("the record lost the upstream code or status: %v", record["error"])
	}
	if _, located := record["error_source"].(map[string]any); !located {
		t.Fatalf("the record has no error source: %v", record)
	}
	if len(chain.queries.bindings) != 0 || chain.store.refs[0].ConnectionID != "" || chain.store.status != apps.StatusPartial {
		t.Fatalf("a refused authorization changed state: bindings=%v refs=%v status=%s", chain.queries.bindings, chain.store.refs, chain.store.status)
	}

	chain.upstream.set(http.StatusCreated, `{"connection_id":"conn-56","authorization_url":"https://github.com/login/oauth/authorize?client_id=placeholder"}`)
	rec, _ = chain.beginOAuth(t)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"connection_id":"conn-56"`) {
		t.Fatalf("authorization did not continue once Connect-It was configured: %d %s", rec.Code, rec.Body.String())
	}
	if len(chain.queries.bindings) != 1 || chain.store.refs[0].ConnectionID != "conn-56" || chain.store.status != apps.StatusInstalled {
		t.Fatalf("authorization did not link the connection: bindings=%v refs=%v status=%s", chain.queries.bindings, chain.store.refs, chain.store.status)
	}

	chain.upstream.set(http.StatusUnprocessableEntity, oauthClientMissing)
	rec, record = chain.post(t, "/bots/"+oauthTestBotID+"/connectors/conn-56/reauth", "")
	assertConnectorProblem(t, rec, record, apperror.CodeConnectorOAuthClientNotConfigured, "dependency", "ERROR")
}

func TestAppConnectorOAuthKeepsOtherConnectItAnswers(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       int
		body         string
		code         apperror.Code
		fault, level string
	}{
		{"Connect-It before the stable code", http.StatusUnprocessableEntity, legacyClientMissing, apperror.CodeConnectorRequestRejected, "client", "INFO"},
		{"request validation", http.StatusUnprocessableEntity, unknownAuthMethod, apperror.CodeConnectorRequestRejected, "client", "INFO"},
		{"Connect-It failure", http.StatusInternalServerError, `{"error":"internal","message":"PRIVATE internal error"}`, apperror.CodeConnectorUpstreamUnavailable, "dependency", "ERROR"},
		{"Memoh's API token rejected", http.StatusUnauthorized, `{"error":"unauthorized","message":"PRIVATE invalid API token"}`, apperror.CodeConnectorUpstreamUnavailable, "server", "ERROR"},
		{"Memoh's API token forbidden", http.StatusForbidden, `{"error":"forbidden","message":"PRIVATE forbidden"}`, apperror.CodeConnectorUpstreamUnavailable, "server", "ERROR"},
		{"unrecognized upstream code", http.StatusBadGateway, `{"error":"PRIVATE test-only-client-secret-56","message":"PRIVATE failure"}`, apperror.CodeConnectorUpstreamUnavailable, "dependency", "ERROR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chain := newOAuthChain(t)
			chain.upstream.set(tc.status, tc.body)
			rec, record := chain.beginOAuth(t)
			assertConnectorProblem(t, rec, record, tc.code, tc.fault, tc.level)
			if _, located := record["error_source"].(map[string]any); tc.fault != "client" && !located {
				t.Fatalf("the record has no error source: %v", record)
			}
		})
	}
}
