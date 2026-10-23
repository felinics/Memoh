package grok

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/servertable"
	"github.com/felinics/memoh/internal/agentcredential"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

type panicTextSink struct{}

func (panicTextSink) EmitStreamEvent(ev event.StreamEvent) {
	if ev.Type == event.TextDelta {
		panic("grok stream consumer panic")
	}
}

type promptBridges struct{ unreachableBridges }

func (promptBridges) WorkspaceInfo(context.Context, string) (bridge.WorkspaceInfo, error) {
	return bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer}, nil
}

func TestPromptReportsSinkPanicAsInternal(t *testing.T) {
	botID, agentID := uuid.New(), uuid.New()
	srv := newTestServer(t, &fakeGrok{})
	srv.done = make(chan struct{})
	q := &signInQueries{agent: sqlc.BotAgent{
		ID: pgtype.UUID{Bytes: agentID, Valid: true}, BotID: pgtype.UUID{Bytes: botID, Valid: true},
		Runtime: RuntimeType, Enabled: true, Metadata: []byte(`{"auth":"api_key","provider":"grok-build"}`),
	}}
	d := &Driver{
		bridges: promptBridges{}, agents: botagents.NewService(slog.Default(), q), logger: slog.Default(),
		servers: servertable.New(func(context.Context, string) (servertable.Resource, error) { return srv, nil }, slog.Default()),
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result, err := d.Prompt(ctx, external.PromptInput{
		BotID: botID.String(), BotAgentID: agentID.String(), ThreadID: "thread", Prompt: "hello", Sink: panicTextSink{},
	})
	if !external.IsPanic(err) || result.TurnCompleted {
		t.Fatalf("Prompt() = %+v, %v; want failed internal run", result, err)
	}
}

func TestSignInHandsOutTheDeviceURLWhileGrokWaits(t *testing.T) {
	grok := &fakeGrok{approve: make(chan struct{})}
	s := &signIn{id: "login-1", conn: newTestServer(t, grok).conn, done: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start, err := s.begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if start.LoginID != "login-1" || start.UserCode != "ABCD-1234" || start.VerificationURL != "https://accounts.x.ai/oauth2/device?user_code=ABCD-1234" {
		t.Fatalf("begin() = %+v, want the device URL with its code", start)
	}
	select {
	case <-s.done:
		t.Fatal("the sign-in ended before the user confirmed")
	default:
	}

	close(grok.approve)
	select {
	case <-s.done:
	case <-ctx.Done():
		t.Fatal("the sign-in did not end after the user confirmed")
	}
	if s.err != nil || s.meta["email"] != "dev@example.test" {
		t.Fatalf("sign-in ended with err=%v meta=%v", s.err, s.meta)
	}
}

func TestSignInRecoversAuthenticatePanic(t *testing.T) {
	s := &signIn{id: "login-panic", done: make(chan struct{}), logger: slog.Default()}
	go s.authenticateAsync(t.Context(), func(context.Context) (map[string]any, error) {
		panic("decoder panic")
	})
	select {
	case <-s.done:
	case <-time.After(time.Second):
		t.Fatal("authenticate goroutine did not finish")
	}
	if !external.IsPanic(s.err) {
		t.Fatalf("authenticate error = %v, want internal error", s.err)
	}
}

func TestSignInReportsWhatGrokRefused(t *testing.T) {
	grok := &fakeGrok{authErr: errors.New("Authentication required")}
	s := &signIn{id: "login-1", conn: newTestServer(t, grok).conn, done: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if start, err := s.begin(ctx); err == nil {
		t.Fatalf("begin() = %+v, want Grok's refusal", start)
	}
}

func TestSignInGateSerializesOnlyTheSameAgent(t *testing.T) {
	d := &Driver{running: map[string]*agentProcesses{}}
	release, err := d.lockSignIn(t.Context(), "bot", "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := d.lockSignIn(ctx, "bot", "agent-a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("second start entered held gate: %v", err)
	}
	other, err := d.lockSignIn(t.Context(), "bot", "agent-b")
	if err != nil {
		t.Fatal(err)
	}
	other()
	release()
	again, err := d.lockSignIn(t.Context(), "bot", "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	again()
}

func TestReplacingSignInCancelsOnlyThatAgentsPreviousAttempts(t *testing.T) {
	canceled := 0
	old := &signIn{id: "old", botID: "bot", botAgentID: "agent-a", cancel: func() { canceled++ }}
	other := &signIn{id: "other", botID: "bot", botAgentID: "agent-b", cancel: func() { t.Error("other Agent canceled") }}
	d := &Driver{signIns: map[string]*signIn{old.id: old, other.id: other}}
	d.cancelAgentSignIns(t.Context(), "bot", "agent-a")
	if canceled != 1 || d.signIn("old", "agent-a") != nil || d.signIn("other", "agent-b") != other {
		t.Fatal("replacement did not isolate and remove old login")
	}
}

// approvedSignIn is a sign-in whose authenticate has answered: the user
// confirmed the code and Grok wrote its login, which no poll has stored yet.
func approvedSignIn(id, botID, botAgentID string, canceled *int) *signIn {
	s := &signIn{id: id, botID: botID, botAgentID: botAgentID, done: make(chan struct{}), cancel: func() { *canceled++ }}
	close(s.done)
	return s
}

func TestReplacingSignInEndsEvenAnApprovedOne(t *testing.T) {
	// Stored after the newer sign-in, an approved one would bring the older
	// account back and revoke the newer credential, so it ends with the rest.
	canceled := 0
	approved := approvedSignIn("approved", "bot", "agent", &canceled)
	pending := &signIn{id: "pending", botID: "bot", botAgentID: "agent", done: make(chan struct{}), cancel: func() { canceled++ }}
	d := &Driver{running: map[string]*agentProcesses{}, signIns: map[string]*signIn{approved.id: approved, pending.id: pending}}
	d.cancelAgentSignIns(t.Context(), "bot", "agent")
	if canceled != 2 || len(d.signIns) != 0 {
		t.Fatalf("earlier sign-ins left: canceled = %d, tracked = %d", canceled, len(d.signIns))
	}
	// The earlier tab's late poll finds nothing to store. The driver has no
	// credential store here, so a store would panic.
	status, err := d.PollDeviceLogin(t.Context(), "owner", "bot", "agent", "approved")
	if err != nil || status.Status != "unknown" {
		t.Fatalf("late poll = %+v, %v; want unknown", status, err)
	}
}

// signInQueries answers the reads a sign-in's poll and a disconnect make: the
// Agent row and its (absent) credential. Any other query is a test bug.
type signInQueries struct {
	dbstore.Queries
	agent sqlc.BotAgent
}

func (q *signInQueries) GetBotAgentByID(context.Context, sqlc.GetBotAgentByIDParams) (sqlc.BotAgent, error) {
	return q.agent, nil
}

func (*signInQueries) GetBotAgentCredential(context.Context, sqlc.GetBotAgentCredentialParams) (sqlc.GetBotAgentCredentialRow, error) {
	return sqlc.GetBotAgentCredentialRow{}, pgx.ErrNoRows
}

func TestApprovedSignInStoresNothingOnceTheAgentLeftGrokAccount(t *testing.T) {
	botID, botAgentID := uuid.New(), uuid.New()
	q := &signInQueries{agent: sqlc.BotAgent{
		ID: pgtype.UUID{Bytes: botAgentID, Valid: true}, BotID: pgtype.UUID{Bytes: botID, Valid: true},
		Runtime: RuntimeType, Enabled: true, Metadata: []byte(`{"auth":"api_key","provider":"grok-build"}`),
	}}
	canceled := 0
	late := approvedSignIn("late", botID.String(), botAgentID.String(), &canceled)
	// No credential store: storing would replace, and revoke, the API key the
	// Agent now runs on.
	d := &Driver{
		running: map[string]*agentProcesses{},
		signIns: map[string]*signIn{late.id: late},
		agents:  botagents.NewService(slog.Default(), q),
	}
	_, err := d.PollDeviceLogin(t.Context(), uuid.NewString(), botID.String(), botAgentID.String(), "late")
	var failure *external.Failure
	if !errors.As(err, &failure) || failure.Kind != external.FailureCredential || !errors.Is(failure.Err, agentcredential.ErrIncompatible) {
		t.Fatalf("poll after the mode changed: %v", err)
	}
	if canceled != 1 || d.signIn("late", botAgentID.String()) != nil {
		t.Fatal("a sign-in that can no longer be stored was kept")
	}
}

type unreachableBridges struct{}

func (unreachableBridges) MCPClient(context.Context, string) (*bridge.Client, error) {
	return nil, errors.New("workspace unreachable")
}

func (unreachableBridges) WorkspaceInfo(context.Context, string) (bridge.WorkspaceInfo, error) {
	return bridge.WorkspaceInfo{}, errors.New("workspace unreachable")
}

func TestDisconnectEndsSignInsUnderWay(t *testing.T) {
	botID, botAgentID := uuid.NewString(), uuid.NewString()
	canceled := 0
	pending := &signIn{id: "pending", botID: botID, botAgentID: botAgentID, done: make(chan struct{}), cancel: func() { canceled++ }}
	approved := approvedSignIn("approved", botID, botAgentID, &canceled)
	d := &Driver{
		running:     map[string]*agentProcesses{},
		signIns:     map[string]*signIn{pending.id: pending, approved.id: approved},
		credentials: agentcredential.NewService(&signInQueries{}, config.Config{}),
		bridges:     unreachableBridges{},
		servers: servertable.New(func(context.Context, string) (servertable.Resource, error) {
			return nil, errors.New("no process starts here")
		}, slog.Default()),
	}
	// The workspace being unreachable fails the disconnect, but only after the
	// sign-ins that would set a credential again have ended.
	if err := d.PurgeBotAgentAuth(t.Context(), botID, botAgentID); err == nil {
		t.Fatal("disconnect reported success without deleting the login file")
	}
	if canceled != 2 || len(d.signIns) != 0 {
		t.Fatalf("sign-ins outlived the disconnect: canceled = %d, tracked = %d", canceled, len(d.signIns))
	}
}

func TestPurgeDoesNotWaitForAStoringPoll(t *testing.T) {
	// Agent deletion purges inside its transaction, holding the Agent row; a
	// storing poll holds the sign-in gate and waits for that row. A purge that
	// waited for the gate would deadlock with it.
	botID, botAgentID := uuid.NewString(), uuid.NewString()
	canceled := 0
	pending := &signIn{id: "pending", botID: botID, botAgentID: botAgentID, done: make(chan struct{}), cancel: func() { canceled++ }}
	d := &Driver{
		running:     map[string]*agentProcesses{},
		signIns:     map[string]*signIn{pending.id: pending},
		credentials: agentcredential.NewService(&signInQueries{}, config.Config{}),
		bridges:     unreachableBridges{},
		servers: servertable.New(func(context.Context, string) (servertable.Resource, error) {
			return nil, errors.New("no process starts here")
		}, slog.Default()),
	}
	storing, err := d.lockSignIn(t.Context(), botID, botAgentID)
	if err != nil {
		t.Fatal(err)
	}
	defer storing()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err = d.PurgeBotAgentAuth(ctx, botID, botAgentID)
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("purge waited for the sign-in gate a storing poll holds")
	}
	if canceled != 1 || len(d.signIns) != 0 {
		t.Fatalf("purge left sign-ins: canceled = %d, tracked = %d", canceled, len(d.signIns))
	}
}
