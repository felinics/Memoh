package inbound

// Characterization tests for the IM reply a channel user sees when an Agent run
// fails. They pin CURRENT behavior so the failure-classification refactor can
// show exactly which outward values it changes. A value that looks wrong is
// still asserted as it is today and marked "current behavior".

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/identities"
	"github.com/felinics/memoh/internal/channel/route"
)

// scriptedFailureGateway replays raw turn event payloads and then an optional
// terminal error on the turn port, the two ways a run reports failure.
type scriptedFailureGateway struct {
	fakeChatGateway
	payloads []string
	tailErr  error
}

func (f *scriptedFailureGateway) StartTurn(_ context.Context, cmd turn.StartTurnCommand) (turn.RunHandle, error) {
	if f.startErr != nil {
		return nil, f.startErr
	}
	events := make(chan turn.Event, len(f.payloads))
	errs := make(chan error, 1)
	for i, payload := range f.payloads {
		events <- turn.Event{RunID: "run-1", ThreadID: cmd.ThreadID, Seq: int64(i + 1), Payload: json.RawMessage(payload)}
	}
	if f.tailErr != nil {
		errs <- f.tailErr
	}
	close(events)
	close(errs)
	return &fakeTurnRun{events: events, errs: errs}, nil
}

type imFailureResult struct {
	err        error
	errorTexts []string
	sent       []string
}

func newIMFailureProcessor(gateway turn.Service) (*ChannelInboundProcessor, *fakeReplySender, channel.ChannelConfig, channel.InboundMessage) {
	routes := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-1", RouteID: "route-1"}}
	processor := NewChannelInboundProcessor(slog.New(slog.DiscardHandler), nil, routes, routes, gateway,
		&fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "identity-1"}}, &fakePolicyService{}, "", 0)
	msg := channel.InboundMessage{
		BotID: "bot-1", Channel: channel.ChannelType("feishu"), Message: channel.Message{Text: "hello"}, ReplyTarget: "target-id",
		Sender: channel.Identity{SubjectID: "ext-1"}, Conversation: channel.Conversation{ID: "chat-1", Type: channel.ConversationTypePrivate},
	}
	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: msg.Channel}
	return processor, &fakeReplySender{}, cfg, msg
}

func runIMFailure(t *testing.T, gateway *scriptedFailureGateway) imFailureResult {
	t.Helper()
	processor, sender, cfg, msg := newIMFailureProcessor(gateway)
	result := imFailureResult{err: processor.HandleInbound(context.Background(), cfg, msg, sender)}
	for _, event := range sender.events {
		if event.Type == channel.StreamEventError {
			result.errorTexts = append(result.errorTexts, event.Error)
		}
	}
	for _, sent := range sender.sent {
		result.sent = append(result.sent, sent.Message.PlainText())
	}
	return result
}

func assertIMFailure(t *testing.T, got imFailureResult, wantErrCode string, wantErr bool, wantTexts ...string) {
	t.Helper()
	if (got.err != nil) != wantErr || string(apperror.CodeOf(got.err)) != wantErrCode {
		t.Fatalf("HandleInbound error = %v (code %q), want error=%v code %q", got.err, apperror.CodeOf(got.err), wantErr, wantErrCode)
	}
	gotJSON, _ := json.Marshal(got.errorTexts)
	wantJSON, _ := json.Marshal(wantTexts)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("IM error texts = %s, want %s", gotJSON, wantJSON)
	}
	if len(got.sent) != 0 {
		t.Fatalf("IM sent messages = %q, want none", got.sent)
	}
}

// Scenario 1 and 10: a start failure is shown to the IM user.
//
// A plain cause is shown with the generic copy for its fault, never its text;
// a coded cause shows its code's copy.
func TestCharacterizeIMStartFailureText_CurrentBehavior(t *testing.T) {
	t.Parallel()
	assertIMFailure(t, runIMFailure(t, &scriptedFailureGateway{
		fakeChatGateway: fakeChatGateway{startErr: errors.New("SECRET resolve failed")},
	}), "", true, "Something went wrong on the server. Please try again.")
	assertIMFailure(t, runIMFailure(t, &scriptedFailureGateway{
		fakeChatGateway: fakeChatGateway{startErr: apperror.Wrap(apperror.CodeWorkspaceUnreachable, errors.New("SECRET dial"), nil)},
	}), "workspace.unreachable", true, "The workspace could not be reached.")
}

// Scenario 1 and 10 after output started: a turn-port error is shown the same way.
//
// Generic copy for a plain cause, the code's copy for a coded one.
func TestCharacterizeIMTurnErrorText_CurrentBehavior(t *testing.T) {
	t.Parallel()
	assertIMFailure(t, runIMFailure(t, &scriptedFailureGateway{
		payloads: []string{`{"type":"text_delta","delta":"partial"}`},
		tailErr:  errors.New("SECRET provider exploded"),
	}), "", true, "Something went wrong on the server. Please try again.")
	assertIMFailure(t, runIMFailure(t, &scriptedFailureGateway{
		tailErr: apperror.Wrap(apperror.CodeAgentResponseInterrupted, errors.New("SECRET cause"), nil),
	}), "agent.response_interrupted", true, "The model response was interrupted. Please try again.")
}

// Scenario 2: the native runtime gives up after retries; the application layer
// publishes the classified error event and the run ends with agent_abort.
//
// Current behavior: the IM user sees the event's text (the public detail) and
// HandleInbound returns nil; the code is not used.
func TestCharacterizeIMErrorEventText_CurrentBehavior(t *testing.T) {
	t.Parallel()
	assertIMFailure(t, runIMFailure(t, &scriptedFailureGateway{payloads: []string{
		`{"type":"agent_start"}`,
		`{"type":"retry","attempt":1,"maxAttempt":3}`,
		`{"type":"error","code":"agent.provider_overloaded","error":"The model provider is unavailable or overloaded right now. Please try again in a moment."}`,
		`{"type":"agent_abort","messages":[]}`,
	}}), "", false, "The model provider is unavailable or overloaded right now. Please try again in a moment.")
	// An event without a code is shown with the failed run copy, not its text.
	assertIMFailure(t, runIMFailure(t, &scriptedFailureGateway{payloads: []string{
		`{"type":"error","error":"SECRET raw"}`,
		`{"type":"agent_abort","messages":[]}`,
	}}), "", false, "The response could not be completed. Please try again.")
}

// Scenario 4, decision not accepted: the continuation fails before the runtime
// accepts the answer. The user is told to resubmit and the cause is returned.
// (The accepted case is TestAcceptanceReceiptPrecedesFailedContinuation.)
func TestCharacterizeIMContinuationNotAccepted(t *testing.T) {
	t.Parallel()
	processor := NewChannelInboundProcessor(slog.New(slog.DiscardHandler), nil, nil, nil, nil, nil, nil, "", 0)
	sink := &fakeReplySender{}
	msg := channel.InboundMessage{Channel: channel.ChannelType("telegram"), ReplyTarget: "chat"}
	cause := errors.New("SECRET transport failure")
	err := processor.streamContinuationCommand(context.Background(), msg, sink, InboundIdentity{BotID: "bot"},
		func(context.Context, chan<- json.RawMessage) error { return cause })
	if !errors.Is(err, cause) {
		t.Fatalf("error = %v, want the transport cause", err)
	}
	var texts []string
	for _, event := range sink.events {
		if event.Type == channel.StreamEventError {
			texts = append(texts, event.Error)
		}
	}
	if len(texts) != 1 || texts[0] != "Your answer could not be submitted. Please try again." {
		t.Fatalf("IM error texts = %q", texts)
	}
}
