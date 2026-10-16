package application

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/runtimefence"
	"github.com/felinics/memoh/internal/team"
)

// resumeLedgerQueries keeps the harness's configuration fakes and sends the
// resume intent reads and writes to PostgreSQL.
type resumeLedgerQueries struct {
	noDecisionQueries
	db *dbsqlc.Queries
}

func (q resumeLedgerQueries) SaveSessionRunResumeContext(ctx context.Context, arg dbsqlc.SaveSessionRunResumeContextParams) (int64, error) {
	return q.db.SaveSessionRunResumeContext(ctx, arg)
}

func (q resumeLedgerQueries) ListInterruptedSessionRuns(ctx context.Context, after pgtype.UUID) ([]dbsqlc.SessionRun, error) {
	return q.db.ListInterruptedSessionRuns(ctx, after)
}

func (q resumeLedgerQueries) RetireSupersededInterruptedSessionRuns(ctx context.Context) (int64, error) {
	return q.db.RetireSupersededInterruptedSessionRuns(ctx)
}

func (q resumeLedgerQueries) RetireInterruptedSessionRun(ctx context.Context, runID pgtype.UUID) (int64, error) {
	return q.db.RetireInterruptedSessionRun(ctx, runID)
}

// resumeProcess is one server process: its runtime and its application
// service, with resume enabled the way the server configures it.
type resumeProcess struct {
	manager *sessionruntime.Manager
	service *Service
}

func (h wsStepHistoryHarness) startProcess(t *testing.T) resumeProcess {
	t.Helper()
	manager := h.restarted(t)
	service, _ := newWSStepHistoryService(t, wsStepHistorySuccess, postgresstore.NewQueriesWithPool(h.pool, dbsqlc.New(h.pool)), manager)
	service.SetResumeSecret("history-reset-resume")
	service.SetSessionResumeScopeProvider(singletonSessionResumeScopes{teamID: team.DefaultTeamID})
	service.queries = resumeLedgerQueries{noDecisionQueries: service.queries.(noDecisionQueries), db: dbsqlc.New(h.pool)}
	return resumeProcess{manager: manager, service: service}
}

// interruptedTurn admits a turn, saves its resume intent the way a streaming
// turn does, and stops it with the process's graceful shutdown.
func (h wsStepHistoryHarness) interruptedTurn(t *testing.T, p resumeProcess, task string) string {
	t.Helper()
	ctx := context.Background()
	admission, err := p.manager.Admit(ctx, sessionruntime.AdmitInput{
		BotID:        h.botID,
		SessionID:    h.sessionID,
		InvocationID: uuid.NewString(),
		Payload:      []byte(`{"kind":"message","text":"` + task + `"}`),
		Execution: sessionruntime.Execution{Admission: func(context.Context, sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error) {
			return sessionruntime.RunAdmissionView{}, nil
		}},
	})
	if err != nil || !admission.Started {
		t.Fatalf("admit turn = (%+v, %v)", admission, err)
	}
	fenced := runtimefence.WithContext(ctx, runtimefence.Fence{BotID: h.botID, SessionID: h.sessionID, Token: admission.Handle.FencingToken})
	if err := p.service.recordRunResumeContext(fenced, ChatRequest{RunID: admission.RunID, ChatID: h.botID, Query: task}); err != nil {
		t.Fatalf("record resume intent: %v", err)
	}
	if err := p.manager.InterruptForShutdown(ctx); err != nil {
		t.Fatalf("graceful shutdown: %v", err)
	}
	var state, code string
	if err := h.pool.QueryRow(ctx, `SELECT state, COALESCE(error_code, '') FROM session_runs WHERE run_id = $1`, admission.RunID).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if state != "lost" || code != sessionruntime.RunErrorInterrupted {
		t.Fatalf("interrupted turn = (%q, %q), want lost with %s", state, code, sessionruntime.RunErrorInterrupted)
	}
	return admission.RunID
}

// resumeScan runs one pass of the process's startup resume worker and reports
// whether it admitted a continuation of runID.
func (h wsStepHistoryHarness) resumeScan(t *testing.T, p resumeProcess, runID string) bool {
	t.Helper()
	ctx := context.Background()
	scan := sessionResumeScan{}
	if err := p.service.scanInterruptedSessions(ctx, p.service.sessionResumeScopes(), &scan, make(chan struct{}, 4)); err != nil {
		t.Fatalf("resume scan: %v", err)
	}
	var resumed string
	err := h.pool.QueryRow(ctx, `SELECT run_id::text FROM session_runs WHERE session_id = $1 AND invocation_id = $2`, h.sessionID, "resume:"+runID).Scan(&resumed)
	if err != nil {
		return false
	}
	// Let the continuation finish before the test tears its runtime down.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var state string
		if err := h.pool.QueryRow(ctx, `SELECT state FROM session_runs WHERE run_id = $1`, resumed).Scan(&state); err == nil && state != "accepted" && state != "running" && state != "finishing" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}

func hasResumeIntent(t *testing.T, h wsStepHistoryHarness, runID string) bool {
	t.Helper()
	var raw []byte
	if err := h.pool.QueryRow(context.Background(), `SELECT input_json FROM session_runs WHERE run_id = $1`, runID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var input map[string]json.RawMessage
	if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	_, ok := input["resume"]
	return ok
}

// A turn interrupted by a graceful shutdown resumes after restart unless its
// history was cleared in between: the clear takes the resume intent with the
// history it refers to. A turn started after the clear resumes as usual.
func TestPostgresHistoryResetRetiresShutdownResume(t *testing.T) {
	for _, scope := range []historyResetScope{historyResetSession, historyResetBot} {
		t.Run(string(scope), func(t *testing.T) {
			h := newWSStepHistoryHarness(t, wsStepHistorySuccess)

			cleared := h.interruptedTurn(t, h.startProcess(t), "cleared task")
			next := h.startProcess(t)
			h.manager = next.manager
			h.clearHistory(t, scope, nil)
			if hasResumeIntent(t, h, cleared) {
				t.Error("clearing history kept the interrupted turn's resume intent")
			}
			if h.resumeScan(t, next, cleared) {
				t.Fatal("resume worker continued a turn whose history was cleared")
			}

			kept := h.interruptedTurn(t, next, "task after the clear")
			if !h.resumeScan(t, h.startProcess(t), kept) {
				t.Fatal("resume worker did not continue a turn started after the clear")
			}
		})
	}
}
