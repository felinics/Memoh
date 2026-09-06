package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"time"
)

// Channel continuations forward the raw agent events the run owner emits after
// an accepted answer. Web reads the session's projected snapshot instead, so a
// channel reader cannot share that state: it needs the raw stream (cards,
// attachments, reactions) and it must survive the owner parking again or a
// notification being lost.
//
// The owner appends each event to a per-command log (DecisionOutputStore); the
// reader keeps a cursor and re-reads from it on every wakeup or tick. Pub/Sub is
// only a wakeup. Run-terminal reconciliation reuses the session snapshot from
// #865/#1107 to notice an owner that died without closing the log.
func decisionOutputRef(botID, commandID string) DecisionOutputRef {
	return DecisionOutputRef{BotID: strings.TrimSpace(botID), CommandID: strings.TrimSpace(commandID)}
}

// Bound retained payloads per command. Overflow is an explicit interrupted
// continuation; it must never silently discard output.
var decisionOutputLimits = DecisionOutputLimits{MaxBytes: 8 << 20, MaxEvents: 32768}

var errDecisionOutputLimitExceeded = errors.New("decision output checkpoint limit exceeded")

func (m *Manager) StreamDecisionResponse(ctx context.Context, response DecisionResponse, output chan<- json.RawMessage) (DecisionResponseResult, error) {
	if m == nil || m.backend == nil || output == nil {
		return m.RouteDecisionResponse(ctx, response)
	}
	response.BotID = strings.TrimSpace(response.BotID)
	commandID := decisionControlCommandID(response.Type, response.BotID, response.DecisionID, response.ControlID)
	ref := decisionOutputRef(response.BotID, commandID)
	// Subscribe before routing: an append committed between acceptance and the
	// first read is still found by that read, but its wakeup would be missed and
	// delivery would wait for the next tick.
	sub, err := m.backend.Subscribe(ctx, ref.topic())
	if err != nil {
		return DecisionResponseResult{}, err
	}
	defer sub.Close()
	response.streamOutput = true
	result, err := m.RouteDecisionResponse(ctx, response)
	if err != nil || !result.Handled || !result.Applied || result.Replayed {
		return result, err
	}
	// Execution deduplication can return success to concurrent callers that both
	// passed routing's replay check. Shared backend arbitration gives only one of
	// those callers permission to forward, including across server instances.
	claimed, err := m.backend.ClaimDecisionOutput(ctx, ref)
	if err != nil {
		return result, err
	}
	if !claimed {
		result.Replayed = true
		return result, nil
	}
	accepted, _ := json.Marshal(map[string]string{"type": "decision_accepted", "decision_id": response.DecisionID})
	select {
	case output <- accepted:
	case <-ctx.Done():
		return result, ctx.Err()
	}
	response.SessionID = result.SessionID
	return result, m.readDecisionOutput(ctx, response, result.RunID, result.Generation, ref, sub, output)
}

func (m *Manager) readDecisionOutput(ctx context.Context, response DecisionResponse, runID, generation string, ref DecisionOutputRef, sub Subscription, output chan<- json.RawMessage) error {
	cursor := 0
	terminalObserved := false
	ticker := time.NewTicker(runtimeReconcileInterval(m.ownerLeaseTTL))
	defer ticker.Stop()
	// drain forwards everything committed past the cursor. It is the only source
	// of truth; wakeups and ticks just decide when to call it.
	drain := func() (bool, error) {
		page, err := m.backend.ReadDecisionOutput(ctx, ref, cursor)
		if err != nil {
			return false, err
		}
		for _, raw := range page.Events {
			select {
			case output <- raw:
				cursor++
			case <-ctx.Done():
				return false, ctx.Err()
			}
		}
		if page.Failed {
			return false, io.ErrUnexpectedEOF
		}
		return page.Done, nil
	}
	// finish decides whether the reader stops. Every error stops it, including a
	// backend read failure: waiting on would hide an interrupted delivery until
	// the request context expires.
	finish := func(done bool, err error) (bool, error) {
		if err == nil && !done {
			return false, nil
		}
		if done || errors.Is(err, io.ErrUnexpectedEOF) {
			// Done or Failed: the log has no further readers. Reclaim the entries
			// now instead of waiting for the state TTL; the claim marker stays.
			if releaseErr := m.backend.ReleaseDecisionOutput(context.WithoutCancel(ctx), ref); releaseErr != nil {
				m.logger.Warn("release decision output log failed", slog.Any("error", releaseErr))
			}
		}
		return true, err
	}
	for {
		if stop, err := finish(drain()); stop {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-sub.C:
			if !ok {
				return io.ErrUnexpectedEOF
			}
			// Any event, including a dropped-notification marker, is a wakeup.
		case <-ticker.C:
			if runID == "" {
				continue
			}
			// An owner crash does not close another process's Redis subscription.
			// Reuse the run snapshot's existing ledger/lease reconciliation instead.
			live, err := m.Snapshot(ctx, response.BotID, response.SessionID)
			if err != nil {
				return err
			}
			ended := live.CurrentRunView == nil || live.CurrentRunView.RunID != runID || !isActiveRunStatus(live.CurrentRunView.Status)
			if !ended && generation != "" && live.CurrentRunView.Generation != generation {
				ended = true
			}
			if !ended {
				terminalObserved = false
				continue
			}
			if stop, err := finish(drain()); stop {
				return err
			}
			if terminalObserved {
				return io.ErrUnexpectedEOF
			}
			// FinishRun can precede the continuation's deferred close. Give that
			// writer one reconciliation interval, rather than guessing that a
			// terminal run proves its output was delivered.
			terminalObserved = true
		}
	}
}

// PublishDecisionOutput appends one raw event (or closes the log with a nil
// payload) and then wakes readers. Commit before notify, exactly as runtime UI
// deltas do; a lost wakeup costs latency, never data.
func (m *Manager) PublishDecisionOutput(ctx context.Context, command Command, seq int64, payload json.RawMessage) error {
	if !command.StreamOutput {
		return nil
	}
	ref := decisionOutputRef(command.BotID, command.ID)
	state, err := m.backend.AppendDecisionOutput(ctx, ref, seq, payload, decisionOutputLimits)
	if err != nil {
		return err
	}
	if state.Applied {
		topic := ref.topic()
		if err := m.backend.Publish(ctx, Event{
			Type: EventDecisionOutput, BotID: topic.BotID, SessionID: topic.SessionID,
			RunID: command.RunID, Seq: int64(state.Length),
		}); err != nil {
			m.logger.Warn("publish decision output wakeup failed; reader will reconcile from log", slog.Any("error", err))
		}
	}
	if state.Exceeded {
		return errDecisionOutputLimitExceeded
	}
	return nil
}
