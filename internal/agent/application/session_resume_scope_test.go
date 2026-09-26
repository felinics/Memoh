package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type (
	resumeScopeKey   struct{}
	testResumeScopes struct {
		teams  []string
		broken bool
	}
)

func (testResumeScopes) CurrentScope(ctx context.Context) string {
	scope, _ := ctx.Value(resumeScopeKey{}).(string)
	return scope
}

func (p testResumeScopes) BindScope(ctx context.Context, scope string) context.Context {
	if p.broken {
		return ctx
	}
	return context.WithValue(ctx, resumeScopeKey{}, scope)
}

func (p testResumeScopes) ListScopePage(_ context.Context, after string, _ int32) (SessionResumeScopePage, error) {
	// One tenant per page exercises the worker's cross-page cursor.
	for i, scope := range p.teams {
		if after == "" || after == scope {
			index := i
			if after != "" {
				index++
			}
			if index >= len(p.teams) {
				return SessionResumeScopePage{Complete: true}, nil
			}
			return SessionResumeScopePage{Scopes: []string{p.teams[index]}, NextCursor: p.teams[index], Complete: index == len(p.teams)-1}, nil
		}
	}
	return SessionResumeScopePage{}, errors.New("unknown scope cursor")
}

type tenantResumeQueries struct {
	dbstore.Queries
	q    *sqlc.Queries
	seen []string
}

func (q *tenantResumeQueries) ListInterruptedSessionRuns(ctx context.Context, after pgtype.UUID) ([]sqlc.SessionRun, error) {
	scope, _ := ctx.Value(resumeScopeKey{}).(string)
	if scope == "" {
		return nil, errors.New("unbound tenant query")
	}
	q.seen = append(q.seen, scope)
	return q.q.ListInterruptedSessionRuns(ctx, after)
}

func TestResumeScopeBindingFailsClosed(t *testing.T) {
	ctx := t.Context()
	if _, err := bindSessionResumeScope(ctx, testResumeScopes{broken: true}, uuid.NewString()); !errors.Is(err, errResumeScopeMismatch) {
		t.Fatalf("missing binding=%v", err)
	}
	s := &Service{resumeScopes: testResumeScopes{}}
	if _, err := s.resumeInterruptedSession(context.WithValue(ctx, resumeScopeKey{}, uuid.NewString()), sqlc.SessionRun{TeamID: db.ParseUUIDOrEmpty(uuid.NewString())}); !errors.Is(err, errResumeScopeMismatch) {
		t.Fatalf("cross-team row=%v", err)
	}
	if err := validateResumeScopePage(SessionResumeScopePage{NextCursor: "same"}, "same"); err == nil {
		t.Fatal("non-advancing page accepted")
	}
}

func TestPostgresResumeScopesUseOrdinaryRoleAndPreserveTenantIntoExecution(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is required")
	}
	ctx := t.Context()
	admin, err := db.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	teams := []string{uuid.NewString(), uuid.NewString()}
	users := []string{uuid.NewString(), uuid.NewString()}
	sessions := map[string]string{}
	for i, teamID := range teams {
		botID, sessionID := uuid.NewString(), uuid.NewString()
		sessions[teamID] = sessionID
		statements := []struct {
			sql  string
			args []any
		}{
			{"INSERT INTO teams(id) VALUES($1)", []any{teamID}},
			{"INSERT INTO users(id,username,is_active) VALUES($1,$2,true)", []any{users[i], "resume-scope-" + uuid.NewString()}},
			{"INSERT INTO team_members(team_id,user_id,role) VALUES($1,$2,'admin')", []any{teamID, users[i]}},
			{"INSERT INTO bots(team_id,id,name,owner_user_id) VALUES($1,$2,$3,$4)", []any{teamID, botID, "resume-scope-" + uuid.NewString(), users[i]}},
			{"INSERT INTO bot_sessions(team_id,id,bot_id,next_turn_position) VALUES($1,$2,$3,2)", []any{teamID, sessionID, botID}},
		}
		for _, stmt := range statements {
			if _, err := admin.Exec(ctx, stmt.sql, stmt.args...); err != nil {
				t.Fatal(err)
			}
		}
		raw, _ := json.Marshal(map[string]any{"resume": resumeContext{Version: 1, ChatID: botID, Query: "resume scoped task"}})
		if _, err := admin.Exec(ctx, `INSERT INTO session_runs(team_id,run_id,bot_id,session_id,invocation_id,turn_id,turn_position,state,input_json,input_fingerprint,error_code)
   VALUES($1,$2,$3,$4,'original',$5,1,'lost',$6,'fixture','session_runtime.interrupted')`, teamID, uuid.NewString(), botID, sessionID, uuid.NewString(), raw); err != nil {
			t.Fatal(err)
		}
		defer func() {
			clean := context.WithoutCancel(ctx)
			_, _ = admin.Exec(clean, "DELETE FROM bots WHERE team_id=$1", teamID)
			_, _ = admin.Exec(clean, "DELETE FROM team_members WHERE team_id=$1", teamID)
			_, _ = admin.Exec(clean, "DELETE FROM users WHERE id=$1", users[i])
			_, _ = admin.Exec(clean, "DELETE FROM teams WHERE id=$1", teamID)
		}()
	}
	role := "resume_qa_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	roleSQL := pgx.Identifier{role}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE ROLE "+roleSQL+" NOLOGIN NOSUPERUSER NOBYPASSRLS"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = admin.Exec(context.WithoutCancel(ctx), "DROP OWNED BY "+roleSQL)
		_, _ = admin.Exec(context.WithoutCancel(ctx), "DROP ROLE "+roleSQL)
	}()
	if _, err := admin.Exec(ctx, "GRANT USAGE ON SCHEMA public TO "+roleSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "GRANT SELECT ON session_runs,bot_sessions,bots TO "+roleSQL); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1 // Force tenant changes to reuse the same physical connection.
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error { _, err := c.Exec(ctx, "SET ROLE "+roleSQL); return err }
	cfg.PrepareConn = func(ctx context.Context, c *pgx.Conn) (bool, error) {
		scope, _ := ctx.Value(resumeScopeKey{}).(string)
		if scope == "" {
			return true, errors.New("tenant context required")
		}
		_, err := c.Exec(ctx, "SELECT set_config('memoh.team_id',$1,false)", scope)
		return err == nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	scopes := testResumeScopes{teams: teams}
	bound := scopes.BindScope(ctx, teams[0])
	var super, bypass bool
	if err := pool.QueryRow(bound, "SELECT rolsuper,rolbypassrls FROM pg_roles WHERE rolname=current_user").Scan(&super, &bypass); err != nil || super || bypass {
		t.Fatalf("role flags: %v %v %v", super, bypass, err)
	}
	var visible int
	if err := pool.QueryRow(bound, "SELECT count(*) FROM session_runs").Scan(&visible); err != nil || visible != 1 {
		t.Fatalf("RLS visible=%d err=%v", visible, err)
	}
	q := &tenantResumeQueries{q: sqlc.New(pool)}
	execution := make(chan string, 2)
	streamer := testChatStreamerFunc(func(runCtx context.Context, req ChatRequest) (<-chan StreamChunk, <-chan error) {
		scope := scopes.CurrentScope(runCtx)
		execution <- scope + ":" + req.ThreadID
		chunks := make(chan StreamChunk, 1)
		chunks <- []byte(`{"type":"agent_end"}`)
		close(chunks)
		errs := make(chan error)
		close(errs)
		return chunks, errs
	})
	s, admitter := newAdmittedTurnTestService(streamer)
	s.logger = slog.Default()
	s.queries = q
	s.SetSessionResumeScopeProvider(scopes)
	scan := sessionResumeScan{}
	slots := make(chan struct{}, 4)
	for range 2 {
		if err := s.scanInterruptedSessions(ctx, scopes, &scan, slots); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		select {
		case got := <-execution:
			found := false
			for teamID, sessionID := range sessions {
				if got == teamID+":"+sessionID {
					found = true
				}
			}
			if !found {
				t.Fatalf("execution lost tenant binding: %s", got)
			}
		case <-time.After(time.Second):
			t.Fatal("tenant did not resume")
		}
	}
	if fmt.Sprint(q.seen) != fmt.Sprint(teams) {
		t.Fatalf("scan scopes=%v", q.seen)
	}
	if len(admitter.admitted()) != 2 {
		t.Fatal("not all tenant sessions admitted")
	}
	// The unbound startup context cannot access the ordinary pool.
	if _, err := sqlc.New(pool).ListInterruptedSessionRuns(ctx, pgtype.UUID{Valid: true}); err == nil {
		t.Fatal("unbound query succeeded")
	}
}
