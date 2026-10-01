package grpctransport

import (
	"encoding/json"
	"errors"

	"google.golang.org/grpc/codes"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/agent/turn/turnpb"
	"github.com/felinics/memoh/internal/rpc"
)

const (
	// turnDeferredStatusMessage is part of the private gRPC vocabulary. Keep it
	// centralized because the client uses it to distinguish a deferred
	// admission result from other ResourceExhausted failures.
	turnDeferredStatusMessage = "turn deferred"
)

// turnReasons registers the turn sentinels on the error envelope. The server
// encoding and the client decoding both read it; the codes and messages are
// those of the legacy encoding.
var turnReasons = rpc.Reasons{
	{Err: turn.ErrSessionBusy, Reason: "turn.session_busy", Code: codes.Aborted, Message: "thread busy"},
	{Err: turn.ErrDuplicateTurn, Reason: "turn.duplicate_turn", Code: codes.AlreadyExists, Message: "duplicate turn"},
	// A deferred turn is an accepted admission result, not a failure. It
	// crosses the process boundary so channel adapters can acknowledge the
	// queued message.
	{Err: turn.ErrTurnDeferred, Reason: "turn.deferred", Code: codes.ResourceExhausted, Message: turnDeferredStatusMessage},
	{Err: turn.ErrTeamNotServed, Reason: "turn.team_not_served", Code: codes.PermissionDenied, Message: "team is not served"},
}

// The authenticated server-channel RPC predates the internal Thread
// terminology. Keep its JSON field named SessionID so independently deployed
// server and channel binaries remain compatible while the domain contract uses
// ThreadID exclusively.
// A tagged field at the same embedding depth shadows the domain ThreadID.
// The output still contains SessionID only; the domain JSON used by queue
// payloads is unchanged. Embedding avoids copying the command's field list.
type outgoingThreadID struct {
	SessionID string
	ThreadID  *string `json:"ThreadID,omitempty"`
}

type incomingThreadID struct {
	SessionID json.RawMessage `json:"SessionID"`
	ThreadID  json.RawMessage `json:"ThreadID"`
}

func marshalStartTurnCommand(cmd turn.StartTurnCommand) ([]byte, error) {
	return json.Marshal(struct {
		turn.StartTurnCommand
		outgoingThreadID
	}{cmd, outgoingThreadID{SessionID: cmd.ThreadID}})
}

func unmarshalStartTurnCommand(data []byte, cmd *turn.StartTurnCommand) error {
	if cmd == nil {
		return json.Unmarshal(data, cmd)
	}
	wire := struct {
		*turn.StartTurnCommand
		incomingThreadID
	}{StartTurnCommand: cmd}
	return unmarshalLegacyThreadID(data, &wire, &wire.incomingThreadID, &cmd.ThreadID)
}

func marshalToolApprovalResponse(input turn.ToolApprovalResponse) ([]byte, error) {
	return json.Marshal(struct {
		turn.ToolApprovalResponse
		outgoingThreadID
	}{input, outgoingThreadID{SessionID: input.ThreadID}})
}

func unmarshalToolApprovalResponse(data []byte, input *turn.ToolApprovalResponse) error {
	if input == nil {
		return json.Unmarshal(data, input)
	}
	wire := struct {
		*turn.ToolApprovalResponse
		incomingThreadID
	}{ToolApprovalResponse: input}
	return unmarshalLegacyThreadID(data, &wire, &wire.incomingThreadID, &input.ThreadID)
}

func marshalUserInputResponse(input turn.UserInputResponse) ([]byte, error) {
	return json.Marshal(struct {
		turn.UserInputResponse
		outgoingThreadID
	}{input, outgoingThreadID{SessionID: input.ThreadID}})
}

func unmarshalUserInputResponse(data []byte, input *turn.UserInputResponse) error {
	if input == nil {
		return json.Unmarshal(data, input)
	}
	wire := struct {
		*turn.UserInputResponse
		incomingThreadID
	}{UserInputResponse: input}
	return unmarshalLegacyThreadID(data, &wire, &wire.incomingThreadID, &input.ThreadID)
}

func unmarshalLegacyThreadID(data []byte, wire any, ids *incomingThreadID, threadID *string) error {
	if err := json.Unmarshal(data, wire); err != nil {
		return err
	}
	if ids.SessionID != nil && ids.ThreadID != nil {
		return errors.New("turn rpc: ambiguous SessionID and ThreadID fields")
	}
	value := ids.ThreadID
	if ids.SessionID != nil {
		value = ids.SessionID
	}
	if value == nil {
		return nil
	}
	return json.Unmarshal(value, threadID)
}

func eventFromProto(event *turnpb.EventResponse) turn.Event {
	if event == nil {
		return turn.Event{}
	}
	return turn.Event{
		RunID:    event.GetRunId(),
		TeamID:   event.GetTeamId(),
		ThreadID: event.GetSessionId(),
		Seq:      event.GetSeq(),
		Kind:     event.GetKind(),
		Payload:  json.RawMessage(event.GetPayload()),
	}
}
