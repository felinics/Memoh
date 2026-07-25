package grpctransport

import (
	"encoding/json"
	"errors"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/internal/rpc/channel/turn/turnpb"
)

const (
	legacySessionIDKey  = "SessionID"
	internalThreadIDKey = "ThreadID"
)

// The authenticated server-channel RPC predates the internal Thread
// terminology. Keep its JSON field named SessionID so independently deployed
// server and channel binaries remain compatible while the domain contract uses
// ThreadID exclusively.
func marshalStartTurnCommand(cmd agentdomain.StartTurnCommand) ([]byte, error) {
	return marshalLegacyThreadID(cmd)
}

func unmarshalStartTurnCommand(data []byte, cmd *agentdomain.StartTurnCommand) error {
	return unmarshalLegacyThreadID(data, cmd)
}

func marshalToolApprovalResponse(input agentdomain.ToolApprovalResponse) ([]byte, error) {
	return marshalLegacyThreadID(input)
}

func unmarshalToolApprovalResponse(data []byte, input *agentdomain.ToolApprovalResponse) error {
	return unmarshalLegacyThreadID(data, input)
}

func marshalUserInputResponse(input agentdomain.UserInputResponse) ([]byte, error) {
	return marshalLegacyThreadID(input)
}

func unmarshalUserInputResponse(data []byte, input *agentdomain.UserInputResponse) error {
	return unmarshalLegacyThreadID(data, input)
}

func marshalLegacyThreadID(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	threadID, ok := fields[internalThreadIDKey]
	if !ok {
		return nil, errors.New("turn rpc: internal ThreadID field missing")
	}
	delete(fields, internalThreadIDKey)
	fields[legacySessionIDKey] = threadID
	return json.Marshal(fields)
}

func unmarshalLegacyThreadID(data []byte, value any) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if _, hasLegacy := fields[legacySessionIDKey]; hasLegacy {
		if _, hasInternal := fields[internalThreadIDKey]; hasInternal {
			return errors.New("turn rpc: ambiguous SessionID and ThreadID fields")
		}
		fields[internalThreadIDKey] = fields[legacySessionIDKey]
		delete(fields, legacySessionIDKey)
	}
	adapted, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	return json.Unmarshal(adapted, value)
}

func eventFromProto(event *turnpb.EventResponse) agentdomain.Event {
	if event == nil {
		return agentdomain.Event{}
	}
	return agentdomain.Event{
		RunID:    event.GetRunId(),
		TeamID:   event.GetTeamId(),
		ThreadID: event.GetSessionId(),
		Seq:      event.GetSeq(),
		Kind:     event.GetKind(),
		Payload:  json.RawMessage(event.GetPayload()),
	}
}
