package application

import (
	"context"
	"log/slog"
	"testing"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
)

// fakeTurnAdmitter captures the Admit input so tests can drive the admission
// hook exactly the way the runtime manager would at activation. Mirrors
// fakeTriggeredAdmitter in service_trigger_test.go.
type fakeTurnAdmitter struct {
	admitInput sessionruntime.AdmitInput
}

func (f *fakeTurnAdmitter) Admit(_ context.Context, input sessionruntime.AdmitInput) (sessionruntime.Admission, error) {
	f.admitInput = input
	return sessionruntime.Admission{
		Started:      true,
		RunID:        "run-1",
		TurnID:       "turn-1",
		TurnPosition: 1,
		Handle: sessionruntime.RunHandle{
			BotID:        input.BotID,
			SessionID:    input.SessionID,
			RunID:        "run-1",
			TurnID:       "turn-1",
			FencingToken: 1,
		},
	}, nil
}

func (*fakeTurnAdmitter) FinishRun(context.Context, sessionruntime.RunHandle, string, string) error {
	return nil
}

func (*fakeTurnAdmitter) MarkInlineDecisionRun(string, string, string) {}

func TestAdmitTurnRunProjectsRequestUserTurn(t *testing.T) {
	t.Parallel()

	admitter := &fakeTurnAdmitter{}
	svc := &Service{logger: slog.New(slog.DiscardHandler), sessionRuntime: admitter}

	cmd := turn.StartTurnCommand{
		TeamID:                  "team-1",
		Mode:                    turn.ModeChat,
		BotID:                   "bot-1",
		ThreadID:                "thread-1",
		Query:                   "hello from telegram",
		ExternalMessageID:       "ext-1",
		CurrentChannel:          "telegram",
		DisplayName:             "Alice",
		SourceChannelIdentityID: "ci-1",
	}
	admission, err := svc.admitTurnRun(context.Background(), cmd, func() {}, nil, nil)
	if err != nil {
		t.Fatalf("admitTurnRun() error = %v", err)
	}
	if admitter.admitInput.Execution.Admission == nil {
		t.Fatal("runtime admission hook is nil")
	}
	view, err := admitter.admitInput.Execution.Admission(context.Background(), admission.Handle)
	if err != nil {
		t.Fatalf("admission hook error = %v", err)
	}
	request := view.RequestUserTurn
	if request == nil {
		t.Fatal("request user turn is nil, want the inbound message projection")
	}
	if request.Text != "hello from telegram" {
		t.Fatalf("request user turn text = %q, want the inbound text", request.Text)
	}
	if request.TurnID != admission.TurnID {
		t.Fatalf("request user turn turn id = %q, want the admitted turn %q", request.TurnID, admission.TurnID)
	}
	if request.Platform != "telegram" || request.SenderDisplayName != "Alice" || request.SenderUserID != "ci-1" {
		t.Fatalf("platform/sender = (%q, %q, %q), want telegram/Alice/ci-1",
			request.Platform, request.SenderDisplayName, request.SenderUserID)
	}
}

func TestAdmitTurnRunDiscussShapeStaysContentless(t *testing.T) {
	t.Parallel()

	admitter := &fakeTurnAdmitter{}
	svc := &Service{logger: slog.New(slog.DiscardHandler), sessionRuntime: admitter}

	// Discuss-shaped commands carry composed context and persist no user
	// message, so the projection must stay empty — otherwise a bubble would
	// render during the run and vanish at the database handover.
	cmd := turn.StartTurnCommand{
		TeamID:   "team-1",
		Mode:     turn.ModeDiscuss,
		BotID:    "bot-1",
		ThreadID: "thread-1",
	}
	admission, err := svc.admitTurnRun(context.Background(), cmd, func() {}, nil, nil)
	if err != nil {
		t.Fatalf("admitTurnRun() error = %v", err)
	}
	view, err := admitter.admitInput.Execution.Admission(context.Background(), admission.Handle)
	if err != nil {
		t.Fatalf("admission hook error = %v", err)
	}
	if view.RequestUserTurn != nil {
		t.Fatalf("request user turn = %#v, want nil for a discuss-shaped command", view.RequestUserTurn)
	}
}
