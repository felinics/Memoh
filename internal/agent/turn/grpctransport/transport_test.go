package grpctransport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"runtime"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	agentfeedback "github.com/felinics/memoh/internal/agent/decision/feedback"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/agent/turn/turnpb"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	intrpc "github.com/felinics/memoh/internal/rpc"
)

func TestStartTurnRoundTrip(t *testing.T) {
	fake := &fakeService{}
	client, cleanup := newTestClient(t, fake, "secret")
	defer cleanup()

	handle, err := client.StartTurn(context.Background(), turn.StartTurnCommand{
		SchemaVersion: 1,
		TeamID:        "team-1",
		BotID:         "bot-1",
		ThreadID:      "session-1",
		Query:         "hello",
		Attachments:   []turn.Attachment{{Name: "a.txt", ContentHash: "hash-1"}},
	})
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}
	event := <-handle.Events()
	if event.RunID != "run-1" || event.ThreadID != "session-1" || event.Seq != 1 || string(event.Payload) != `{"type":"text_delta","text":"hi"}` {
		t.Fatalf("event = %#v", event)
	}
	if fake.started.ThreadID != "session-1" || fake.started.Query != "hello" || fake.started.Attachments[0].ContentHash != "hash-1" {
		t.Fatalf("command = %#v", fake.started)
	}
}

func TestLegacySessionIDJSONWireCompatibility(t *testing.T) {
	tests := []struct {
		name      string
		marshal   func() ([]byte, error)
		unmarshal func([]byte) (string, error)
	}{
		{
			name: "start",
			marshal: func() ([]byte, error) {
				return marshalStartTurnCommand(turn.StartTurnCommand{
					TeamID: "team-1", ThreadID: "thread-1", Query: "hello",
				})
			},
			unmarshal: func(data []byte) (string, error) {
				var value turn.StartTurnCommand
				err := unmarshalStartTurnCommand(data, &value)
				return value.ThreadID, err
			},
		},
		{
			name: "tool approval",
			marshal: func() ([]byte, error) {
				return marshalToolApprovalResponse(turn.ToolApprovalResponse{
					BotID: "bot-1", ThreadID: "thread-1", ApprovalID: "approval-1",
				})
			},
			unmarshal: func(data []byte) (string, error) {
				var value turn.ToolApprovalResponse
				err := unmarshalToolApprovalResponse(data, &value)
				return value.ThreadID, err
			},
		},
		{
			name: "user input",
			marshal: func() ([]byte, error) {
				return marshalUserInputResponse(turn.UserInputResponse{
					BotID: "bot-1", ThreadID: "thread-1", UserInputID: "input-1",
				})
			},
			unmarshal: func(data []byte) (string, error) {
				var value turn.UserInputResponse
				err := unmarshalUserInputResponse(data, &value)
				return value.ThreadID, err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := tt.marshal()
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatalf("decode literal payload: %v", err)
			}
			if _, exists := fields[internalThreadIDKey]; exists {
				t.Fatalf("payload leaked internal %q key: %s", internalThreadIDKey, data)
			}
			rawSessionID, exists := fields[legacySessionIDKey]
			if !exists {
				t.Fatalf("payload missing legacy %q key: %s", legacySessionIDKey, data)
			}
			var sessionID string
			if err := json.Unmarshal(rawSessionID, &sessionID); err != nil {
				t.Fatalf("decode legacy SessionID: %v", err)
			}
			if sessionID != "thread-1" {
				t.Fatalf("legacy SessionID = %q, want thread-1", sessionID)
			}
			threadID, err := tt.unmarshal(data)
			if err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if threadID != "thread-1" {
				t.Fatalf("roundtrip ThreadID = %q, want thread-1", threadID)
			}
		})
	}
}

func TestEventProtoSessionIDCompatibility(t *testing.T) {
	original := turn.Event{
		RunID:    "run-1",
		TeamID:   "team-1",
		ThreadID: "thread-1",
		Seq:      7,
		Kind:     "text_delta",
		Payload:  json.RawMessage(`{"type":"text_delta","text":"hello"}`),
	}
	wire := eventToProto(original)
	if wire.GetSessionId() != "thread-1" {
		t.Fatalf("proto session_id = %q, want thread-1", wire.GetSessionId())
	}
	roundTrip := eventFromProto(wire)
	if roundTrip.ThreadID != original.ThreadID || roundTrip.RunID != original.RunID ||
		roundTrip.TeamID != original.TeamID || roundTrip.Seq != original.Seq ||
		roundTrip.Kind != original.Kind || string(roundTrip.Payload) != string(original.Payload) {
		t.Fatalf("event roundtrip = %#v, want %#v", roundTrip, original)
	}
}

func TestAuthenticationRejectsWrongSecret(t *testing.T) {
	fake := &fakeService{}
	client, cleanup := newTestClient(t, fake, "wrong")
	defer cleanup()
	_, err := client.StartTurn(context.Background(), turn.StartTurnCommand{TeamID: "team-1"})
	if err == nil {
		t.Fatal("expected authentication failure")
	}
}

func TestCancelUnblocksFullClientEventBuffer(t *testing.T) {
	fake := &fakeService{eventCount: 40, cancelled: make(chan struct{}, 1)}
	client, cleanup := newTestClient(t, fake, "secret")
	defer cleanup()

	handle, err := client.StartTurn(context.Background(), turn.StartTurnCommand{TeamID: "team-1"})
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}
	h := handle.(*runHandle)
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for len(h.events) < cap(h.events) {
		select {
		case <-deadline.C:
			t.Fatal("client event buffer did not fill")
		default:
			runtime.Gosched()
		}
	}

	handle.Cancel()
	select {
	case <-h.done:
	case <-time.After(2 * time.Second):
		t.Fatal("client pump remained blocked after Cancel")
	}
	select {
	case <-fake.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("server run was not canceled")
	}
}

func newTestClient(t *testing.T, service turn.Service, clientSecret string) (*Client, func()) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	server := intrpc.NewServer("secret")
	turnpb.RegisterTurnServiceServer(server, NewServer(nil, service))
	go func() { _ = server.Serve(lis) }()
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(intrpc.UnaryClientAuth(clientSecret)),
		grpc.WithStreamInterceptor(intrpc.StreamClientAuth(clientSecret)),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return NewClient(conn), func() { _ = conn.Close(); server.Stop(); _ = lis.Close() }
}

type fakeService struct {
	started    turn.StartTurnCommand
	eventCount int
	cancelled  chan struct{}
}

func (f *fakeService) StartTurn(_ context.Context, cmd turn.StartTurnCommand) (turn.RunHandle, error) {
	f.started = cmd
	eventCount := max(f.eventCount, 1)
	events := make(chan turn.Event, eventCount)
	for i := range eventCount {
		events <- turn.Event{RunID: "run-1", TeamID: cmd.TeamID, ThreadID: cmd.ThreadID, Seq: int64(i + 1), Kind: "text_delta", Payload: json.RawMessage(`{"type":"text_delta","text":"hi"}`)}
	}
	close(events)
	errs := make(chan error)
	close(errs)
	return &fakeHandle{events: events, errs: errs, cancelled: f.cancelled}, nil
}

func (*fakeService) RespondToolApproval(context.Context, turn.ToolApprovalResponse, chan<- json.RawMessage) error {
	return nil
}

func (*fakeService) RespondUserInput(context.Context, turn.UserInputResponse, chan<- json.RawMessage) error {
	return nil
}

func (*fakeService) AdvancePlainTextUserInput(context.Context, userinput.AdvanceTextInput) (userinput.AdvanceTextResult, error) {
	return userinput.AdvanceTextResult{}, nil
}

type fakeHandle struct {
	events    <-chan turn.Event
	errs      <-chan error
	cancelled chan<- struct{}
}

func (*fakeHandle) RunID() string                                    { return "run-1" }
func (h *fakeHandle) Events() <-chan turn.Event                      { return h.events }
func (h *fakeHandle) Errs() <-chan error                             { return h.errs }
func (*fakeHandle) Inject(context.Context, turn.InjectMessage) error { return nil }
func (*fakeHandle) AddOutboundAssets([]turn.OutboundAssetRef)        {}
func (h *fakeHandle) Cancel() {
	if h.cancelled == nil {
		return
	}
	select {
	case h.cancelled <- struct{}{}:
	default:
	}
}

// scriptedService returns a caller-supplied handle (or start error) so
// tests can drive the run lifecycle precisely.
type scriptedService struct {
	handle   turn.RunHandle
	startErr error
}

func (s *scriptedService) StartTurn(context.Context, turn.StartTurnCommand) (turn.RunHandle, error) {
	if s.startErr != nil {
		return nil, s.startErr
	}
	return s.handle, nil
}

func (*scriptedService) RespondToolApproval(context.Context, turn.ToolApprovalResponse, chan<- json.RawMessage) error {
	return nil
}

func (*scriptedService) RespondUserInput(context.Context, turn.UserInputResponse, chan<- json.RawMessage) error {
	return nil
}

func (*scriptedService) AdvancePlainTextUserInput(context.Context, userinput.AdvanceTextInput) (userinput.AdvanceTextResult, error) {
	return userinput.AdvanceTextResult{}, nil
}

type scriptedHandle struct {
	events       chan turn.Event
	errs         chan error
	injectErr    error
	injectCalled chan struct{}
}

func (*scriptedHandle) RunID() string                             { return "run-scripted" }
func (h *scriptedHandle) Events() <-chan turn.Event               { return h.events }
func (h *scriptedHandle) Errs() <-chan error                      { return h.errs }
func (*scriptedHandle) AddOutboundAssets([]turn.OutboundAssetRef) {}
func (*scriptedHandle) Cancel()                                   {}

func (h *scriptedHandle) Inject(context.Context, turn.InjectMessage) error {
	if h.injectCalled != nil {
		select {
		case h.injectCalled <- struct{}{}:
		default:
		}
	}
	return h.injectErr
}

// TestRunErrorDeliversTailEventsFirst pins the drain fix: events already
// produced by the run must reach the client before the terminating error,
// matching in-process ordering.
func TestRunErrorDeliversTailEventsFirst(t *testing.T) {
	events := make(chan turn.Event, 3)
	for i := range 3 {
		events <- turn.Event{RunID: "run-scripted", Seq: int64(i + 1), Kind: "text_delta", Payload: json.RawMessage(`{"type":"text_delta"}`)}
	}
	close(events)
	errs := make(chan error, 1)
	errs <- errors.New("provider exploded")
	close(errs)

	client, cleanup := newTestClient(t, &scriptedService{handle: &scriptedHandle{events: events, errs: errs}}, "secret")
	defer cleanup()
	handle, err := client.StartTurn(context.Background(), turn.StartTurnCommand{TeamID: "team-1"})
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}
	var got int
	for range handle.Events() {
		got++
	}
	if got != 3 {
		t.Fatalf("received %d tail events before error, want 3", got)
	}
	var runErr error
	for err := range handle.Errs() {
		runErr = err
	}
	if runErr == nil {
		t.Fatal("expected run error after tail events")
	}
}

// TestInjectFailureKeepsStreamAlive pins the control-frame fix: a failed
// inject loses only the injected message and must not tear down a healthy
// run (which would misreport a successful turn as canceled).
func TestInjectFailureKeepsStreamAlive(t *testing.T) {
	events := make(chan turn.Event)
	errs := make(chan error)
	h := &scriptedHandle{events: events, errs: errs, injectErr: errors.New("inject exploded"), injectCalled: make(chan struct{}, 1)}
	client, cleanup := newTestClient(t, &scriptedService{handle: h}, "secret")
	defer cleanup()
	handle, err := client.StartTurn(context.Background(), turn.StartTurnCommand{TeamID: "team-1"})
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}
	if err := handle.Inject(context.Background(), turn.InjectMessage{Text: "x"}); err != nil {
		t.Fatalf("client inject send: %v", err)
	}
	select {
	case <-h.injectCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("server inject not invoked")
	}
	// The stream must still deliver events after the failed inject.
	go func() {
		events <- turn.Event{RunID: "run-scripted", Seq: 1, Kind: "text_delta", Payload: json.RawMessage(`{"type":"text_delta"}`)}
		close(events)
		close(errs)
	}()
	select {
	case event, ok := <-handle.Events():
		if !ok {
			t.Fatal("events closed before delivering post-inject event")
		}
		if event.Seq != 1 {
			t.Fatalf("unexpected event %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not survive failed inject")
	}
	for range handle.Events() {
	}
	for err := range handle.Errs() {
		t.Fatalf("unexpected run error after failed inject: %v", err)
	}
}

// TestFeedbackErrorSurvivesTransport pins the agentfeedback envelope: typed
// ACP feedback must cross the wire so the channel process can render its
// localized guidance instead of a bare internal error.
func TestFeedbackErrorSurvivesTransport(t *testing.T) {
	events := make(chan turn.Event)
	close(events)
	errs := make(chan error, 1)
	errs <- fmt.Errorf("resolve runtime: %w", sessionpkg.ErrACPAgentNotConfigured)
	close(errs)

	client, cleanup := newTestClient(t, &scriptedService{handle: &scriptedHandle{events: events, errs: errs}}, "secret")
	defer cleanup()
	handle, err := client.StartTurn(context.Background(), turn.StartTurnCommand{TeamID: "team-1"})
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}
	for range handle.Events() {
	}
	var runErr error
	for err := range handle.Errs() {
		runErr = err
	}
	var feedback *agentfeedback.Error
	if !errors.As(runErr, &feedback) {
		t.Fatalf("run error lost feedback identity: %v", runErr)
	}
	if feedback.Code != agentfeedback.CodeAgentNotConfigured {
		t.Fatalf("feedback code = %q", feedback.Code)
	}

	// Start-path errors carry the envelope too.
	direct := agentfeedback.New(agentfeedback.CodeAgentNotEnabled, "agent_not_enabled", 403, "chat.externalAgent.agentNotEnabled", "disabled", nil)
	client2, cleanup2 := newTestClient(t, &scriptedService{startErr: direct}, "secret")
	defer cleanup2()
	_, err = client2.StartTurn(context.Background(), turn.StartTurnCommand{TeamID: "team-1"})
	var startFeedback *agentfeedback.Error
	if !errors.As(err, &startFeedback) || startFeedback.Code != agentfeedback.CodeAgentNotEnabled {
		t.Fatalf("start error lost feedback identity: %v", err)
	}
}

// TestUnknownControlFrameIgnored pins forward compatibility: an
// unrecognized control frame from a newer channel binary must not kill the
// running turn.
func TestUnknownControlFrameIgnored(t *testing.T) {
	events := make(chan turn.Event)
	errs := make(chan error)
	h := &scriptedHandle{events: events, errs: errs}
	client, cleanup := newTestClient(t, &scriptedService{handle: h}, "secret")
	defer cleanup()
	handle, err := client.StartTurn(context.Background(), turn.StartTurnCommand{TeamID: "team-1"})
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}
	rh := handle.(*runHandle)
	if err := rh.send(context.Background(), &turnpb.RunRequest{}); err != nil {
		t.Fatalf("send unknown frame: %v", err)
	}
	go func() {
		events <- turn.Event{RunID: "run-scripted", Seq: 1, Kind: "done", Payload: json.RawMessage(`{"type":"done"}`)}
		close(events)
		close(errs)
	}()
	select {
	case _, ok := <-handle.Events():
		if !ok {
			t.Fatal("events closed before delivering post-frame event")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not survive unknown control frame")
	}
	for range handle.Events() {
	}
	for err := range handle.Errs() {
		t.Fatalf("unexpected run error after unknown frame: %v", err)
	}
}
