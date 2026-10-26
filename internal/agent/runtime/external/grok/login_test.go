package grok

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/runtime/external/agentproc"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

func TestSignInHandsOutTheDeviceURLWhileGrokWaits(t *testing.T) {
	grok := &fakeGrok{approve: make(chan struct{})}
	s := &signIn{id: "login-1", srv: newTestServer(t, grok), done: make(chan struct{})}
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
	if s.err != nil {
		t.Fatalf("sign-in ended with %v", s.err)
	}
}

func TestSignInReportsWhatGrokRefused(t *testing.T) {
	grok := &fakeGrok{authErr: errors.New("Authentication required")}
	s := &signIn{id: "login-1", srv: newTestServer(t, grok), done: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if start, err := s.begin(ctx); err == nil {
		t.Fatalf("begin() = %+v, want Grok's refusal", start)
	}
}

func TestSignInGateSerializesOnlyTheSameAgent(t *testing.T) {
	d := &Driver{signInGates: map[string]chan struct{}{}}
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

func testDriver(t *testing.T, signIns ...*signIn) *Driver {
	t.Helper()
	d := &Driver{
		signIns:     map[string]*signIn{},
		signInGates: map[string]chan struct{}{},
		workspaces:  unreachableBridges{},
		procs:       agentproc.New(agentproc.Options{}),
	}
	t.Cleanup(func() { d.procs.Close(context.Background()) })
	for _, s := range signIns {
		d.signIns[s.id] = s
	}
	return d
}

// testSignIn is a sign-in under way; ended counts how often its process was
// let go.
func testSignIn(id, botID, botAgentID string, ended *int) *signIn {
	return &signIn{
		id: id, botID: botID, botAgentID: botAgentID, done: make(chan struct{}),
		cancel: func() {}, release: func() { *ended++ },
	}
}

func TestEndingASignInLetsItsProcessGoOnce(t *testing.T) {
	ended := 0
	s := testSignIn("login", "bot", "agent", &ended)
	d := testDriver(t, s)
	d.endSignIn(s)
	d.endSignIn(s)
	if ended != 1 || d.signIn("login", "agent") != nil {
		t.Fatalf("process released %d times, tracked = %v", ended, d.signIn("login", "agent") != nil)
	}
}

func TestReplacingSignInEndsOnlyThatAgentsAttempts(t *testing.T) {
	ended := 0
	old := testSignIn("old", "bot", "agent-a", &ended)
	other := testSignIn("other", "bot", "agent-b", new(int))
	d := testDriver(t, old, other)
	d.cancelAgentSignIns("bot", "agent-a")
	if ended != 1 || d.signIn("old", "agent-a") != nil || d.signIn("other", "agent-b") != other {
		t.Fatal("replacement did not isolate and remove the old sign-in")
	}
	// The earlier tab's late poll finds nothing.
	status, err := d.PollDeviceLogin(t.Context(), "", "bot", "agent-a", "old")
	if err != nil || status.Status != "unknown" {
		t.Fatalf("late poll = %+v, %v; want unknown", status, err)
	}
}

type unreachableBridges struct{}

func (unreachableBridges) MCPClient(context.Context, string) (*bridge.Client, error) {
	return nil, errors.New("workspace unreachable")
}

func (unreachableBridges) WorkspaceInfo(context.Context, string) (bridge.WorkspaceInfo, error) {
	return bridge.WorkspaceInfo{}, errors.New("workspace unreachable")
}

func TestDisconnectEndsSignInsUnderWayWithoutWaitingForTheGate(t *testing.T) {
	// Agent deletion purges inside its transaction while a poll may hold the
	// sign-in gate; a purge that waited for the gate could deadlock with it.
	botID, botAgentID := uuid.NewString(), uuid.NewString()
	ended := 0
	d := testDriver(t, testSignIn("a", botID, botAgentID, &ended), testSignIn("b", botID, botAgentID, &ended))
	polling, err := d.lockSignIn(t.Context(), botID, botAgentID)
	if err != nil {
		t.Fatal(err)
	}
	defer polling()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	// The workspace being unreachable fails the disconnect, but only after the
	// sign-ins that would put a login back have ended.
	err = d.PurgeBotAgentAuth(ctx, botID, botAgentID)
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("purge = %v, want the unreachable workspace", err)
	}
	if ended != 2 || len(d.signIns) != 0 {
		t.Fatalf("sign-ins outlived the disconnect: ended = %d, tracked = %d", ended, len(d.signIns))
	}
}
