package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// Decision output has a separate backend topic because UI deltas cannot preserve
// channel events such as attachments, reactions and interactive tool requests.
// The command ID isolates successive answers in the same run. Both memory and
// Redis backends support this, including responses routed to a different owner.
func decisionOutputKey(botID, commandID string) Key {
	return Key{BotID: botID, SessionID: "decision-output/" + commandID}
}

// StreamDecisionResponse subscribes before admission so even a continuation that
// finishes before the command acknowledgement cannot outrun its channel reader.
// Cancelling the reader releases only its subscription, not the owning run.
func (m *Manager) StreamDecisionResponse(ctx context.Context, response DecisionResponse, output chan<- json.RawMessage) (DecisionResponseResult, error) {
	if m == nil || m.backend == nil || output == nil {
		return m.RouteDecisionResponse(ctx, response)
	}
	response.BotID = strings.TrimSpace(response.BotID)
	commandID := decisionControlCommandID(response.Type, response.BotID, response.DecisionID, response.ControlID)
	sub, err := m.backend.Subscribe(ctx, decisionOutputKey(response.BotID, commandID))
	if err != nil {
		return DecisionResponseResult{}, err
	}
	defer sub.Close()
	result, err := m.RouteDecisionResponse(ctx, response)
	if err != nil || !result.Handled || !result.Applied || result.Replayed {
		return result, err
	}
	var seq int64
	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case event, ok := <-sub.C:
			if !ok {
				return result, io.ErrUnexpectedEOF
			}
			if event.Type == EventRuntimeDropped || event.Seq != seq+1 {
				return result, errors.New("decision output stream interrupted")
			}
			seq = event.Seq
			if event.Type == "decision_output_end" {
				return result, nil
			}
			select {
			case output <- event.Output:
			case <-ctx.Done():
				return result, ctx.Err()
			}
		}
	}
}

// PublishDecisionOutput runs only after the owner has accepted the corresponding
// agent event. A nil payload closes this continuation, including a new question.
func (m *Manager) PublishDecisionOutput(ctx context.Context, command Command, seq int64, payload json.RawMessage) error {
	key := decisionOutputKey(command.BotID, command.ID)
	kind := "decision_output"
	if payload == nil {
		kind = "decision_output_end"
	}
	return m.backend.Publish(ctx, Event{Type: kind, BotID: key.BotID, SessionID: key.SessionID, RunID: command.RunID, Seq: seq, Output: payload})
}
