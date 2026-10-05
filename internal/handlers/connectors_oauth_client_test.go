package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/apps"
	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/connectors"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/errs"
)

const (
	oauthTestBotID          = "00000000-0000-0000-0000-000000000056"
	oauthTestInstallationID = "installation-56"
	oauthClientMissing      = `{"error":"oauth_client_not_configured","message":"PRIVATE the connector's OAuth client is not configured"}`
	legacyClientMissing     = `{"error":"validation_failed","message":"PRIVATE oauthsvc: config is missing client_id or client_secret"}`
	unknownAuthMethod       = `{"error":"validation_failed","message":"PRIVATE oauthsvc: unknown auth method: nope"}`
)

type connectItAnswer struct {
	status int
	body   string
}

// fakeConnectIt answers the OAuth start and reauth calls with the wire bodies
// Connect-It sends, so the SDK decodes them as it does in production.
type fakeConnectIt struct {
	mu     sync.Mutex
	answer connectItAnswer
}

func (f *fakeConnectIt) set(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answer = connectItAnswer{status: status, body: body}
}

func (f *fakeConnectIt) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	answer := f.answer
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(answer.status)
	_ = json.NewEncoder(w).Encode(json.RawMessage(answer.body))
}

type oauthBindingQueries struct {
	dbstore.Queries
	bindings []dbsqlc.Connector
}

func (q *oauthBindingQueries) ListConnectorsByBotID(context.Context, pgtype.UUID) ([]dbsqlc.Connector, error) {
	return q.bindings, nil
}

func (q *oauthBindingQueries) CreateConnector(_ context.Context, args dbsqlc.CreateConnectorParams) (dbsqlc.Connector, error) {
	item := dbsqlc.Connector{BotID: args.BotID, ConnectionID: args.ConnectionID, Alias: args.Alias, Enabled: true}
	q.bindings = append(q.bindings, item)
	return item, nil
}

func (q *oauthBindingQueries) GetConnectorByConnectionID(_ context.Context, args dbsqlc.GetConnectorByConnectionIDParams) (dbsqlc.Connector, error) {
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
	upstream   *fakeConnectIt
	queries    *oauthBindingQueries
	store      *oauthAppStore
	connectors *connectors.Service
	apps       *AppsHandler
}

func newOAuthChain(t *testing.T) oauthChain {
	t.Helper()
	upstream := &fakeConnectIt{}
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	queries := &oauthBindingQueries{}
	var cfg config.Config
	cfg.ConnectIt = config.ConnectItConfig{BaseURL: server.URL, APIToken: "test-only"}
	connectorService, err := connectors.NewService(slog.Default(), cfg, queries)
	if err != nil {
		t.Fatal(err)
	}
	store := &oauthAppStore{status: apps.StatusPartial, refs: []apps.ConnectorRef{
		{InstallationID: oauthTestInstallationID, ConnectorType: "github", Required: true},
	}}
	appService := apps.NewService(apps.Options{
		Store: store, Registry: struct{ apps.RegistryClient }{}, Skills: struct{ apps.SkillPublisher }{},
		Connectors: connectorService,
	})
	return oauthChain{
		upstream: upstream, queries: queries, store: store, connectors: connectorService,
		apps: &AppsHandler{service: appService},
	}
}

func (c oauthChain) beginOAuth(t *testing.T) error {
	t.Helper()
	_, err := c.apps.service.BeginConnectorOAuth(t.Context(), oauthTestBotID, oauthTestInstallationID, "github", "oauth")
	if err == nil {
		return nil
	}
	return c.apps.httpError(err)
}

func assertConnectorProblem(t *testing.T, err error, code apperror.Code, status int, fault string) {
	t.Helper()
	problem, ok := apperror.ProblemFrom(err, "req-oauth")
	if !ok {
		t.Fatalf("not a public error: %v", err)
	}
	if got := string(errs.FaultOf(err)); problem.Code != string(code) || problem.Status != status || got != fault {
		t.Fatalf("problem = %s %d %s, want %s %d %s", problem.Code, problem.Status, got, code, status, fault)
	}
	if strings.Contains(problem.Detail, "PRIVATE") || strings.Contains(problem.Detail, "oauthsvc") {
		t.Fatalf("upstream message leaked into the problem: %q", problem.Detail)
	}
}

func TestAppConnectorOAuthReportsMissingOAuthClientApp(t *testing.T) {
	chain := newOAuthChain(t)

	chain.upstream.set(http.StatusConflict, oauthClientMissing)
	assertConnectorProblem(t, chain.beginOAuth(t), apperror.CodeConnectorOAuthClientNotConfigured, http.StatusServiceUnavailable, "dependency")
	if len(chain.queries.bindings) != 0 || chain.store.refs[0].ConnectionID != "" || chain.store.status != apps.StatusPartial {
		t.Fatalf("a refused authorization changed state: bindings=%v refs=%v status=%s", chain.queries.bindings, chain.store.refs, chain.store.status)
	}

	chain.upstream.set(http.StatusCreated, `{"connection_id":"conn-56","authorization_url":"https://github.com/login/oauth/authorize?client_id=placeholder"}`)
	if err := chain.beginOAuth(t); err != nil {
		t.Fatalf("authorization did not continue once Connect-It was configured: %v", err)
	}
	if len(chain.queries.bindings) != 1 || chain.store.refs[0].ConnectionID != "conn-56" || chain.store.status != apps.StatusInstalled {
		t.Fatalf("authorization did not link the connection: bindings=%v refs=%v status=%s", chain.queries.bindings, chain.store.refs, chain.store.status)
	}

	chain.upstream.set(http.StatusConflict, oauthClientMissing)
	_, err := chain.connectors.Reauthorize(t.Context(), oauthTestBotID, "conn-56")
	assertConnectorProblem(t, connectorHTTPError(err), apperror.CodeConnectorOAuthClientNotConfigured, http.StatusServiceUnavailable, "dependency")
}

func TestAppConnectorOAuthKeepsOlderConnectItRejectionsUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		code   apperror.Code
		want   int
	}{
		{"Connect-It before the stable code", http.StatusUnprocessableEntity, legacyClientMissing, apperror.CodeConnectorRequestRejected, http.StatusBadRequest},
		{"request validation", http.StatusUnprocessableEntity, unknownAuthMethod, apperror.CodeConnectorRequestRejected, http.StatusBadRequest},
		{"other conflict", http.StatusConflict, `{"error":"config_incompatible","message":"PRIVATE"}`, apperror.CodeConnectorConflict, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chain := newOAuthChain(t)
			chain.upstream.set(tc.status, tc.body)
			assertConnectorProblem(t, chain.beginOAuth(t), tc.code, tc.want, "client")
		})
	}
}
