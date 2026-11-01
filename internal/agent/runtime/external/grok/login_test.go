package grok

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/external/agentproc"
)

func TestSignInHandsOutTheDeviceURLWhileGrokWaits(t *testing.T) {
	grok := &fakeGrok{approve: make(chan struct{})}
	s := &signIn{id: "login-1", conn: newTestProcess(t, grok).conn, done: make(chan struct{})}
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
	s := &signIn{id: "login-1", conn: newTestProcess(t, grok).conn, done: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if start, err := s.begin(ctx); err == nil {
		t.Fatalf("begin() = %+v, want Grok's refusal", start)
	}
}

func TestSignInGateSerializesOnlyTheSameAgent(t *testing.T) {
	d := &Driver{signInGates: map[agentproc.Key]chan struct{}{}}
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
