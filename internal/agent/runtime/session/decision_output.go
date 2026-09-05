package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

// Each answer uses an independent checkpoint: a run can park again without
// ending, and its next question must not reuse the previous answer's cursor.
func decisionOutputKey(botID, commandID string) Key {
	return Key{BotID: botID, SessionID: "decision-output/" + commandID}
}

// Bound retained raw payloads, not the notification buffer. Overflow is an
// explicit interrupted continuation; it must never silently discard output.
const (
	decisionOutputMaxBytes  = 8 << 20
	decisionOutputMaxEvents = 32768
)

func (m *Manager) StreamDecisionResponse(ctx context.Context, response DecisionResponse, output chan<- json.RawMessage) (DecisionResponseResult, error) {
	if m == nil || m.backend == nil || output == nil {
		return m.RouteDecisionResponse(ctx, response)
	}
	response.BotID = strings.TrimSpace(response.BotID)
	commandID := decisionControlCommandID(response.Type, response.BotID, response.DecisionID, response.ControlID)
	key := decisionOutputKey(response.BotID, commandID)
	if _, _, err := m.backend.Update(ctx, key, func(s Snapshot, exists bool) (Snapshot, bool, error) {
		if exists && s.DecisionOutput != nil {
			return s, false, nil
		}
		s = EmptySnapshot(key.BotID, key.SessionID)
		s.Epoch = m.newEpoch()
		s.DecisionOutput = &DecisionOutputCheckpoint{}
		return s, true, nil
	}); err != nil {
		return DecisionResponseResult{}, err
	}
	// Use the web subscription, including snapshot hydration, sequence-gap
	// repair and periodic reconciliation. Pub/Sub is only a wakeup mechanism.
	sub, err := m.Subscribe(ctx, key.BotID, key.SessionID)
	if err != nil {
		return DecisionResponseResult{}, err
	}
	defer sub.Close()
	result, err := m.RouteDecisionResponse(ctx, response)
	if err != nil || !result.Handled || !result.Applied || result.Replayed {
		return result, err
	}
	accepted, _ := json.Marshal(map[string]string{"type": "decision_accepted", "decision_id": response.DecisionID})
	select {
	case output <- accepted:
	case <-ctx.Done():
		return result, ctx.Err()
	}
	return result, m.readDecisionOutput(ctx, response, result.RunID, key, sub, output)
}

func (m *Manager) readDecisionOutput(ctx context.Context, response DecisionResponse, runID string, key Key, sub Subscription, output chan<- json.RawMessage) error {
	cursor := 0
	epoch := ""
	terminalObserved := false
	ticker := time.NewTicker(runtimeReconcileInterval(m.ownerLeaseTTL))
	defer ticker.Stop()
	consume := func(checkpoint *DecisionOutputCheckpoint) (bool, error) {
		if checkpoint == nil {
			return false, nil
		}
		if checkpoint.Offset > cursor {
			return false, io.ErrUnexpectedEOF
		}
		for cursor < checkpoint.Offset+len(checkpoint.Events) {
			select {
			case output <- checkpoint.Events[cursor-checkpoint.Offset]:
				cursor++
			case <-ctx.Done():
				return false, ctx.Err()
			}
		}
		if checkpoint.Failed {
			return false, io.ErrUnexpectedEOF
		}
		return checkpoint.Done, nil
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-sub.C:
			if !ok || event.Type == EventRuntimeDropped {
				return io.ErrUnexpectedEOF
			}
			if epoch != "" && event.Epoch != epoch {
				return io.ErrUnexpectedEOF
			}
			epoch = event.Epoch
			var checkpoint *DecisionOutputCheckpoint
			if event.Snapshot != nil {
				checkpoint = event.Snapshot.DecisionOutput
				if checkpoint == nil {
					return io.ErrUnexpectedEOF
				}
			}
			if event.Delta != nil {
				checkpoint = event.Delta.DecisionOutput
			}
			if done, err := consume(checkpoint); done || err != nil {
				return err
			}
		case <-ticker.C:
			// The end checkpoint is authoritative even if its notification is lost.
			snapshot, exists, err := m.backend.Load(ctx, key)
			if err != nil {
				return err
			}
			if !exists {
				return io.ErrUnexpectedEOF
			}
			if done, err := consume(snapshot.DecisionOutput); done || err != nil {
				return err
			}
			// An owner crash does not close another process's Redis subscription.
			// Reuse the run snapshot's existing ledger/lease reconciliation instead.
			if runID != "" {
				live, err := m.Snapshot(ctx, response.BotID, response.SessionID)
				if err != nil {
					return err
				}
				ended := live.CurrentRunView == nil || live.CurrentRunView.RunID != runID || !isActiveRunStatus(live.CurrentRunView.Status)
				if !ended && live.CurrentRunView.Status == RunStatusWaitingDecision {
					m.mu.Lock()
					decisions := m.decisionStore
					m.mu.Unlock()
					if decisions != nil {
						pending, err := decisions.PendingRuntimeDecisions(ctx, runID)
						if err != nil {
							return err
						}
						for _, target := range pending {
							if target.RunID == runID && target.ID != response.DecisionID {
								ended = true
								break
							}
						}
					}
				}
				if ended {
					// Drain a final checkpoint committed concurrently with the run terminal.
					latest, _, err := m.backend.Load(ctx, key)
					if err != nil {
						return err
					}
					if done, err := consume(latest.DecisionOutput); done || err != nil {
						return err
					}
					if terminalObserved {
						return io.ErrUnexpectedEOF
					}
					// FinishRun can precede the continuation's deferred checkpoint write.
					// Give that writer one reconciliation interval, rather than guessing
					// that a terminal run proves its output was delivered.
					terminalObserved = true
				} else {
					terminalObserved = false
				}
			}
		}
	}
}

// Commit state before notifying, exactly as runtime UI deltas do. A nil payload
// closes this continuation, including when the agent parks on a new question.
func (m *Manager) PublishDecisionOutput(ctx context.Context, command Command, seq int64, payload json.RawMessage) error {
	key := decisionOutputKey(command.BotID, command.ID)
	exceeded := false
	_, _, err := m.updateAndPublish(ctx, key, command.RunID, func(s Snapshot, exists bool) (Snapshot, bool, error) {
		if !exists {
			s = EmptySnapshot(key.BotID, key.SessionID)
			s.Epoch = m.newEpoch()
		}
		if s.DecisionOutput == nil {
			s.DecisionOutput = &DecisionOutputCheckpoint{}
		}
		c := s.DecisionOutput
		if c.Done || c.Failed {
			return s, false, nil
		}
		if seq != int64(len(c.Events)+1) {
			return s, false, errors.New("decision output sequence mismatch")
		}
		if payload == nil {
			c.Done = true
		} else if c.Bytes+len(payload) > decisionOutputMaxBytes || len(c.Events) >= decisionOutputMaxEvents {
			c.Failed = true
			exceeded = true
		} else {
			c.Events = append(c.Events, append(json.RawMessage(nil), payload...))
			c.Bytes += len(payload)
		}
		s.Seq++
		s.UpdatedAt = time.Now().UTC()
		return s, true, nil
	}, func(s Snapshot) RuntimeDelta {
		c := s.DecisionOutput
		delta := &DecisionOutputCheckpoint{Offset: len(c.Events), Done: c.Done, Failed: c.Failed}
		if payload != nil && !c.Failed {
			delta.Offset--
			delta.Events = c.Events[len(c.Events)-1:]
		}
		return RuntimeDelta{DecisionOutput: delta}
	})
	if err == nil && exceeded {
		return errors.New("decision output checkpoint limit exceeded")
	}
	return err
}
