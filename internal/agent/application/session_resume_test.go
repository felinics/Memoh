package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/auth"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/runtimefence"
)

type resumeSaveQueries struct {
	dbstore.Queries
	saved sqlc.SaveSessionRunResumeContextParams
	calls int
}

func (q *resumeSaveQueries) SaveSessionRunResumeContext(_ context.Context, arg sqlc.SaveSessionRunResumeContextParams) (int64, error) {
	q.saved = arg
	q.calls++
	return 1, nil
}

func TestResumeContextPreservesDeadlineAndScopesWithoutCredentials(t *testing.T) {
	secret := "resume-test-secret"
	bearer, _, err := auth.GenerateToken("account", secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	scoped, _, err := auth.GenerateChatToken(auth.ChatToken{BotID: "bot", ChatID: "chat", UserID: "sender", RouteID: "route"}, secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	q := &resumeSaveQueries{}
	svc := &Service{queries: q, resumeSecret: secret}
	deadline := time.Now().Add(time.Minute)
	ctx, cancel := context.WithDeadline(runtimefence.WithContext(t.Context(), runtimefence.Fence{BotID: "bot", SessionID: "session", Token: 7}), deadline)
	defer cancel()
	req := ChatRequest{RunID: uuid.NewString(), UserID: "sender", ChatID: "chat", Token: "Bearer " + bearer, ChatToken: "Bearer " + scoped, Query: "do the work"}
	if err := svc.recordRunResumeContext(ctx, req); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(q.saved.ResumeContext), bearer) || strings.Contains(string(q.saved.ResumeContext), scoped) || strings.Contains(string(q.saved.ResumeContext), secret) {
		t.Fatal("credential persisted")
	}
	var saved resumeContext
	if err := json.Unmarshal(q.saved.ResumeContext, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.DeadlineAt == nil || !saved.DeadlineAt.Equal(deadline) || saved.ChatClaims["route_id"] != "route" || saved.TokenClaims["user_id"] != "account" {
		t.Fatalf("saved context=%+v", saved)
	}
	req.ShutdownResume = true
	if err := svc.recordRunResumeContext(ctx, req); err != nil {
		t.Fatal(err)
	}
	if q.calls != 1 {
		t.Fatal("resume rewrote the original recovery intent")
	}
}

func TestResumeRejectsInvalidCredentialAndForeignBotScope(t *testing.T) {
	s := &Service{resumeSecret: "resume-secret"}
	token, _, err := auth.GenerateToken("account", "other-secret", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.resumeClaims(token); err == nil {
		t.Fatal("accepted invalid signature")
	}
	if _, err := s.renewResumeCredential(t.Context(), "bot", map[string]string{"typ": "chat_route", "bot_id": "other", "chat_id": "chat"}); !errors.Is(err, errResumeUnrecoverable) {
		t.Fatalf("cross-bot credential = %v, want an unrecoverable intent", err)
	}
	if _, err := s.renewResumeCredential(t.Context(), "bot", map[string]string{"sub": "account"}); err == nil {
		t.Fatal("renewed account without reauthorization")
	}
	token, err = s.renewResumeCredential(t.Context(), "bot", map[string]string{"typ": "chat_route", "bot_id": "bot", "chat_id": "chat", "route_id": "route", "user_id": "sender"})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jwt.Parse(strings.TrimPrefix(token, "Bearer "), func(_ *jwt.Token) (any, error) { return []byte(s.resumeSecret), nil }, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !parsed.Valid {
		t.Fatal("renewed token invalid")
	}
	claims := parsed.Claims.(jwt.MapClaims)
	if claims["route_id"] != "route" || claims["typ"] != "chat_route" {
		t.Fatal("scope widened")
	}
}

func TestResumeReentersSavedSessionOnceAndKeepsDeadline(t *testing.T) {
	runner := &fakeRunner{chunks: []string{`{"type":"text_delta","delta":"continued"}`, `{"type":"agent_end"}`}}
	s, admitter := newAdmittedTurnTestService(runner)
	s.SetAllowedTeam("00000000-0000-0000-0000-000000000001")
	s.logger = slog.Default()
	deadline := time.Now().Add(time.Minute)
	data := resumeContext{Version: 1, ChatID: "chat", UserID: "sender", Query: "original task", DeadlineAt: &deadline}
	raw, err := json.Marshal(map[string]any{"resume": data})
	if err != nil {
		t.Fatal(err)
	}
	row := sqlc.SessionRun{TeamID: db.ParseUUIDOrEmpty("00000000-0000-0000-0000-000000000001"), RunID: db.ParseUUIDOrEmpty(uuid.NewString()), BotID: db.ParseUUIDOrEmpty(uuid.NewString()), SessionID: db.ParseUUIDOrEmpty(uuid.NewString()), InputJson: raw}
	done, err := s.resumeInterruptedSession(t.Context(), row)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("resume did not finish")
	}
	if !runner.gotReq.UserMessagePersisted || !runner.gotReq.ShutdownResume || runner.gotReq.ThreadID != row.SessionID.String() {
		t.Fatalf("request=%+v", runner.gotReq)
	}
	if !strings.Contains(runner.gotReq.Query, "Original task: original task") {
		t.Fatal("lost task")
	}
	inputs := admitter.admitted()
	if len(inputs) != 1 || inputs[0].InvocationID != "resume:"+row.RunID.String() {
		t.Fatalf("admission=%+v", inputs)
	}
	admitter.mu.Lock()
	admitter.started = false
	admitter.mu.Unlock()
	if _, err := s.resumeInterruptedSession(t.Context(), row); !errors.Is(err, errResumeUnrecoverable) {
		t.Fatalf("duplicate admission = %v, want an unrecoverable intent", err)
	}
	expired := time.Now().Add(-time.Second)
	data.DeadlineAt = &expired
	row.InputJson, _ = json.Marshal(map[string]any{"resume": data})
	if done, err := s.resumeInterruptedSession(t.Context(), row); !errors.Is(err, errResumeUnrecoverable) || done != nil {
		t.Fatalf("expired budget: done=%v err=%v, want an unrecoverable intent", done, err)
	}
	if len(admitter.admitted()) != 2 {
		t.Fatal("expired task reached admission")
	}
}

func TestResumeWaitsForWorkspaceBeforeAdmission(t *testing.T) {
	s, admitter := newAdmittedTurnTestService(&fakeRunner{})
	s.SetAllowedTeam("00000000-0000-0000-0000-000000000001")
	s.resumeReady = func(context.Context, string, string) error { return errors.New("bridge not ready") }
	raw, _ := json.Marshal(map[string]any{"resume": resumeContext{Version: 1, ChatID: "chat", Query: "work"}})
	done, err := s.resumeInterruptedSession(t.Context(), sqlc.SessionRun{TeamID: db.ParseUUIDOrEmpty("00000000-0000-0000-0000-000000000001"), InputJson: raw})
	if err == nil || done != nil {
		t.Fatal("unready workspace admitted")
	}
	if len(admitter.admitted()) != 0 {
		t.Fatal("readiness failure consumed resume identity")
	}
}

type retireResumeQueries struct {
	dbstore.Queries
	rows    []sqlc.SessionRun
	swept   int
	retired []string
}

func (q *retireResumeQueries) ListInterruptedSessionRuns(context.Context, pgtype.UUID) ([]sqlc.SessionRun, error) {
	rows := q.rows
	q.rows = nil
	return rows, nil
}

func (q *retireResumeQueries) RetireSupersededInterruptedSessionRuns(context.Context) (int64, error) {
	q.swept++
	return 0, nil
}

func (q *retireResumeQueries) RetireInterruptedSessionRun(_ context.Context, runID pgtype.UUID) (int64, error) {
	q.retired = append(q.retired, runID.String())
	return 1, nil
}

// An intent that can never continue is retired once instead of being retried
// on every pass; one that may still clear (the workspace is not up yet) keeps
// its intent, and a resumable one is admitted.
func TestResumeScanRetiresIntentsThatCannotContinue(t *testing.T) {
	const teamID = "00000000-0000-0000-0000-000000000001"
	runner := &fakeRunner{chunks: []string{`{"type":"agent_end"}`}}
	s, admitter := newAdmittedTurnTestService(runner)
	s.SetAllowedTeam(teamID)
	s.logger = slog.Default()
	unreadyBot := uuid.NewString()
	s.resumeReady = func(_ context.Context, botID, _ string) error {
		if botID == unreadyBot {
			return errors.New("bridge not ready")
		}
		return nil
	}
	future, past := time.Now().Add(time.Minute), time.Now().Add(-time.Second)
	row := func(botID string, data resumeContext) sqlc.SessionRun {
		raw, err := json.Marshal(map[string]any{"resume": data})
		if err != nil {
			t.Fatal(err)
		}
		return sqlc.SessionRun{TeamID: db.ParseUUIDOrEmpty(teamID), RunID: db.ParseUUIDOrEmpty(uuid.NewString()), BotID: db.ParseUUIDOrEmpty(botID), SessionID: db.ParseUUIDOrEmpty(uuid.NewString()), InputJson: raw}
	}
	expired := row(uuid.NewString(), resumeContext{Version: 1, ChatID: "chat", Query: "late", DeadlineAt: &past})
	unsupported := row(uuid.NewString(), resumeContext{Version: 2, ChatID: "chat", Query: "future format"})
	unready := row(unreadyBot, resumeContext{Version: 1, ChatID: "chat", Query: "wait", DeadlineAt: &future})
	resumable := row(uuid.NewString(), resumeContext{Version: 1, ChatID: "chat", Query: "go on", DeadlineAt: &future})
	q := &retireResumeQueries{rows: []sqlc.SessionRun{expired, unsupported, unready, resumable}}
	s.queries = q

	scan := sessionResumeScan{}
	if err := s.scanInterruptedSessions(t.Context(), s.sessionResumeScopes(), &scan, make(chan struct{}, 4)); err != nil {
		t.Fatal(err)
	}
	if q.swept != 1 {
		t.Fatalf("superseded sweep ran %d times, want once per scope pass", q.swept)
	}
	want := []string{expired.RunID.String(), unsupported.RunID.String()}
	if fmt.Sprint(q.retired) != fmt.Sprint(want) {
		t.Fatalf("retired %v, want %v", q.retired, want)
	}
	inputs := admitter.admitted()
	if len(inputs) != 1 || inputs[0].InvocationID != "resume:"+resumable.RunID.String() {
		t.Fatalf("admission=%+v, want only the resumable run", inputs)
	}
}
